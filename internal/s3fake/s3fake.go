// Package s3fake is an in-process S3 server for the tests of s3adapter: the path-style REST
// calls s3adapter makes (put, get with range, head, delete, list v2, copy, multipart upload)
// on buckets held in memory. It does not check signatures or expiry, and refuses
// aws-chunked bodies, which s3adapter must never send. FailNext injects errors.
package s3fake

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinimumPartSize is the smallest part S3 accepts in a multipart upload, except the last.
const MinimumPartSize = 5 << 20

// The operations FailNext can fail.
const (
	OperationPutObject      = "PutObject"
	OperationUploadPart     = "UploadPart"
	OperationCompleteUpload = "CompleteMultipartUpload"
	OperationCopyObject     = "CopyObject"
	OperationGetObject      = "GetObject"
)

// Server is a fake S3 endpoint; it is safe for concurrent use.
type Server struct {
	*httptest.Server
	mutex    sync.Mutex
	buckets  map[string]map[string]*object
	uploads  map[string]*upload
	failures map[string]int
	nextID   int
}

type object struct {
	content     []byte
	contentType string
	metadata    map[string]string
	modifiedAt  time.Time
	etag        string
}

type upload struct {
	bucket, key string
	contentType string
	metadata    map[string]string
	parts       map[int][]byte
}

// New starts a server holding the given empty buckets; it closes with the test.
func New(cleanup func(func()), buckets ...string) *Server {
	server := &Server{buckets: map[string]map[string]*object{}, uploads: map[string]*upload{}, failures: map[string]int{}}
	for _, bucket := range buckets {
		server.buckets[bucket] = map[string]*object{}
	}
	server.Server = httptest.NewServer(http.HandlerFunc(server.serve))
	cleanup(server.Close)
	return server
}

// FailNext makes the next count requests of operation fail with 403 AccessDenied, which
// clients do not retry.
func (s *Server) FailNext(operation string, count int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.failures[operation] += count
}

// PendingUploads is the number of multipart uploads neither completed nor aborted.
func (s *Server) PendingUploads() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.uploads)
}

func (s *Server) serve(writer http.ResponseWriter, request *http.Request) {
	if strings.Contains(request.Header.Get("Content-Encoding"), "aws-chunked") {
		writeError(writer, http.StatusNotImplemented, "NotImplemented", "aws-chunked bodies are not supported")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(request.URL.Path, "/"), "/")
	s.mutex.Lock()
	defer s.mutex.Unlock()
	objects, found := s.buckets[bucket]
	if !found {
		writeError(writer, http.StatusNotFound, "NoSuchBucket", "the bucket does not exist")
		return
	}
	if key == "" {
		s.list(writer, request, bucket, objects)
		return
	}
	s.route(writer, request, objects, bucket, key)
}

func (s *Server) route(writer http.ResponseWriter, request *http.Request, objects map[string]*object, bucket, key string) {
	query := request.URL.Query()
	switch {
	case request.Method == http.MethodPost && query.Has("uploads"):
		s.createUpload(writer, request, bucket, key)
	case request.Method == http.MethodPost && query.Has("uploadId"):
		s.completeUpload(writer, request, objects, query.Get("uploadId"))
	case request.Method == http.MethodPut && query.Has("uploadId"):
		s.uploadPart(writer, request, query)
	case request.Method == http.MethodDelete && query.Has("uploadId"):
		delete(s.uploads, query.Get("uploadId"))
		writer.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodPut && request.Header.Get("X-Amz-Copy-Source") != "":
		s.copyObject(writer, request, bucket, key)
	case request.Method == http.MethodPut:
		s.putObject(writer, request, objects, key)
	case request.Method == http.MethodGet || request.Method == http.MethodHead:
		s.getObject(writer, request, objects, key)
	case request.Method == http.MethodDelete:
		delete(objects, key)
		writer.WriteHeader(http.StatusNoContent)
	default:
		writeError(writer, http.StatusMethodNotAllowed, "MethodNotAllowed", request.Method)
	}
}

// failed consumes an injected failure of operation and writes it.
func (s *Server) failed(writer http.ResponseWriter, operation string) bool {
	if s.failures[operation] == 0 {
		return false
	}
	s.failures[operation]--
	writeError(writer, http.StatusForbidden, "AccessDenied", "injected failure of "+operation)
	return true
}

func (s *Server) putObject(writer http.ResponseWriter, request *http.Request, objects map[string]*object, key string) {
	content, err := io.ReadAll(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	if s.failed(writer, OperationPutObject) {
		return
	}
	stored := s.newObject(content, request.Header.Get("Content-Type"), metadataOf(request.Header))
	objects[key] = stored
	writer.Header().Set("ETag", stored.etag)
}

func (s *Server) newObject(content []byte, contentType string, metadata map[string]string) *object {
	digest := sha256.Sum256(content)
	return &object{
		content: content, contentType: contentType, metadata: metadata,
		modifiedAt: time.Now().UTC(), etag: `"` + hex.EncodeToString(digest[:16]) + `"`,
	}
}

func metadataOf(header http.Header) map[string]string {
	metadata := map[string]string{}
	for name, values := range header {
		if suffix, found := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); found {
			metadata[suffix] = values[0]
		}
	}
	return metadata
}

func (s *Server) getObject(writer http.ResponseWriter, request *http.Request, objects map[string]*object, key string) {
	stored, found := objects[key]
	if !found {
		writeError(writer, http.StatusNotFound, "NoSuchKey", "the key does not exist")
		return
	}
	if request.Method == http.MethodGet && s.failed(writer, OperationGetObject) {
		return
	}
	selected, ok := parseRange(request.Header.Get("Range"), int64(len(stored.content)))
	start, end := selected.start, selected.end
	if !ok {
		writeError(writer, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "the range is not satisfiable")
		return
	}
	header := writer.Header()
	for name, value := range stored.metadata {
		header.Set("X-Amz-Meta-"+name, value)
	}
	header.Set("Content-Type", stored.contentType)
	header.Set("Last-Modified", stored.modifiedAt.Format(http.TimeFormat))
	header.Set("ETag", stored.etag)
	query := request.URL.Query()
	if disposition := query.Get("response-content-disposition"); disposition != "" {
		header.Set("Content-Disposition", disposition)
	}
	if contentType := query.Get("response-content-type"); contentType != "" {
		header.Set("Content-Type", contentType)
	}
	header.Set("Content-Length", strconv.FormatInt(end-start, 10))
	status := http.StatusOK
	if selected.partial {
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(stored.content)))
		status = http.StatusPartialContent
	}
	writer.WriteHeader(status)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(stored.content[start:end]) // the client hung up; nothing to report to
	}
}

// byteRange is the half-open range [start, end) of a GET; partial when the request named one.
type byteRange struct {
	start, end int64
	partial    bool
}

// parseRange reads "bytes=start-" or "bytes=start-end"; false means unsatisfiable.
func parseRange(rangeHeader string, size int64) (byteRange, bool) {
	if rangeHeader == "" {
		return byteRange{end: size}, true
	}
	first, last, found := strings.Cut(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	start, err := strconv.ParseInt(first, 10, 64)
	if !found || err != nil || start >= size {
		return byteRange{}, false
	}
	selected := byteRange{start: start, end: size, partial: true}
	if last != "" {
		inclusiveEnd, err := strconv.ParseInt(last, 10, 64)
		if err != nil || inclusiveEnd < start {
			return byteRange{}, false
		}
		selected.end = min(inclusiveEnd+1, size)
	}
	return selected, true
}

func (s *Server) copyObject(writer http.ResponseWriter, request *http.Request, bucket, key string) {
	source, err := url.PathUnescape(strings.TrimPrefix(request.Header.Get("X-Amz-Copy-Source"), "/"))
	sourceBucket, sourceKey, _ := strings.Cut(source, "/")
	sourceObjects, found := s.buckets[sourceBucket]
	if err != nil || !found {
		writeError(writer, http.StatusNotFound, "NoSuchBucket", "the source bucket does not exist")
		return
	}
	stored, found := sourceObjects[sourceKey]
	if !found {
		writeError(writer, http.StatusNotFound, "NoSuchKey", "the source key does not exist")
		return
	}
	if sourceBucket == bucket && sourceKey == key && request.Header.Get("X-Amz-Metadata-Directive") != "REPLACE" {
		writeError(writer, http.StatusBadRequest, "InvalidRequest", "copying an object onto itself needs a changed metadata")
		return
	}
	if s.failed(writer, OperationCopyObject) {
		return
	}
	copied := s.newObject(stored.content, stored.contentType, maps.Clone(stored.metadata))
	s.buckets[bucket][key] = copied
	writeXML(writer, http.StatusOK, &copyObjectResult{LastModified: copied.modifiedAt.Format(time.RFC3339), ETag: copied.etag})
}

func (s *Server) createUpload(writer http.ResponseWriter, request *http.Request, bucket, key string) {
	s.nextID++
	uploadID := "upload-" + strconv.Itoa(s.nextID)
	s.uploads[uploadID] = &upload{
		bucket: bucket, key: key, contentType: request.Header.Get("Content-Type"),
		metadata: metadataOf(request.Header), parts: map[int][]byte{},
	}
	writeXML(writer, http.StatusOK, &initiateResult{Bucket: bucket, Key: key, UploadID: uploadID})
}

func (s *Server) uploadPart(writer http.ResponseWriter, request *http.Request, query url.Values) {
	pending, found := s.uploads[query.Get("uploadId")]
	partNumber, err := strconv.Atoi(query.Get("partNumber"))
	if !found || err != nil || partNumber < 1 {
		writeError(writer, http.StatusNotFound, "NoSuchUpload", "the upload does not exist")
		return
	}
	content, err := io.ReadAll(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	if s.failed(writer, OperationUploadPart) {
		return
	}
	pending.parts[partNumber] = content
	writer.Header().Set("ETag", partETag(content))
}

func partETag(content []byte) string {
	digest := sha256.Sum256(content)
	return `"` + hex.EncodeToString(digest[:16]) + `"`
}

func (s *Server) completeUpload(writer http.ResponseWriter, request *http.Request, objects map[string]*object, uploadID string) {
	pending, found := s.uploads[uploadID]
	if !found {
		writeError(writer, http.StatusNotFound, "NoSuchUpload", "the upload does not exist")
		return
	}
	var completion completeRequest
	if err := xml.NewDecoder(request.Body).Decode(&completion); err != nil {
		writeError(writer, http.StatusBadRequest, "MalformedXML", err.Error())
		return
	}
	if s.failed(writer, OperationCompleteUpload) {
		return
	}
	content, code := assemble(pending, completion.Parts)
	if code != "" {
		writeError(writer, http.StatusBadRequest, code, "the parts do not match the upload")
		return
	}
	stored := s.newObject(content, pending.contentType, pending.metadata)
	objects[pending.key] = stored
	delete(s.uploads, uploadID)
	writeXML(writer, http.StatusOK, &completeResult{Bucket: pending.bucket, Key: pending.key, ETag: stored.etag})
}

// assemble joins the listed parts in order, or returns the S3 error code that rejects them.
func assemble(pending *upload, parts []completedPart) ([]byte, string) {
	var content bytes.Buffer
	for index, part := range parts {
		stored, found := pending.parts[part.PartNumber]
		if !found || partETag(stored) != part.ETag || (index > 0 && parts[index-1].PartNumber >= part.PartNumber) {
			return nil, "InvalidPart"
		}
		if index < len(parts)-1 && len(stored) < MinimumPartSize {
			return nil, "EntityTooSmall"
		}
		content.Write(stored)
	}
	if len(parts) == 0 {
		return nil, "MalformedXML"
	}
	return content.Bytes(), ""
}

func (s *Server) list(writer http.ResponseWriter, request *http.Request, bucket string, objects map[string]*object) {
	query := request.URL.Query()
	if request.Method != http.MethodGet || query.Get("list-type") != "2" {
		writeError(writer, http.StatusNotImplemented, "NotImplemented", "only ListObjectsV2 is supported")
		return
	}
	maximumKeys, err := strconv.Atoi(query.Get("max-keys"))
	if err != nil || maximumKeys <= 0 || maximumKeys > 1000 {
		maximumKeys = 1000
	}
	prefix, startAfter := query.Get("prefix"), query.Get("start-after")
	result := &listResult{Name: bucket, Prefix: prefix, StartAfter: startAfter, MaxKeys: maximumKeys}
	for _, key := range slices.Sorted(maps.Keys(objects)) {
		if !strings.HasPrefix(key, prefix) || key <= startAfter {
			continue
		}
		if len(result.Contents) == maximumKeys {
			result.IsTruncated = true
			break
		}
		stored := objects[key]
		result.Contents = append(result.Contents, listEntry{
			Key: key, Size: int64(len(stored.content)), ETag: stored.etag,
			LastModified: stored.modifiedAt.Format("2006-01-02T15:04:05.000Z"), StorageClass: "STANDARD",
		})
	}
	result.KeyCount = len(result.Contents)
	writeXML(writer, http.StatusOK, result)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeXML(writer, status, &errorResult{Code: code, Message: message})
}

func writeXML(writer http.ResponseWriter, status int, body any) {
	encoded, err := xml.Marshal(body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(status)
	_, _ = writer.Write(append([]byte(xml.Header), encoded...)) // the client hung up; nothing to report to
}

type errorResult struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

type initiateResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeRequest struct {
	Parts []completedPart `xml:"Part"`
}

type completedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeResult struct {
	XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
	Bucket  string   `xml:"Bucket"`
	Key     string   `xml:"Key"`
	ETag    string   `xml:"ETag"`
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type listResult struct {
	XMLName     xml.Name    `xml:"ListBucketResult"`
	Name        string      `xml:"Name"`
	Prefix      string      `xml:"Prefix"`
	StartAfter  string      `xml:"StartAfter,omitempty"`
	KeyCount    int         `xml:"KeyCount"`
	MaxKeys     int         `xml:"MaxKeys"`
	IsTruncated bool        `xml:"IsTruncated"`
	Contents    []listEntry `xml:"Contents"`
}

type listEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}
