package s3adapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
	"github.com/govengers/storage/internal/s3fake"
	"github.com/govengers/storage/s3adapter"
)

const partSize = s3adapter.MinimumPartSize

// content returns size bytes that differ per position, so a part in the wrong place shows.
func content(size int) []byte {
	pattern := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	return bytes.Repeat(pattern, size/len(pattern)+1)[:size]
}

func readAll(t *testing.T, store storage.Store, key string) []byte {
	t.Helper()
	reader, err := store.Get(t.Context(), key, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()
	stored, err := io.ReadAll(reader)
	require.NoError(t, err)
	return stored
}

func TestMultipartUpload(t *testing.T) {
	for name, size := range map[string]int{"several parts": 2*partSize + 12345, "exactly one part": partSize, "one byte past a part": partSize + 1} {
		t.Run(name, func(t *testing.T) {
			server := s3fake.New(t.Cleanup, bucket)
			store := newStore(t, server, partSize)
			body := content(size)
			object, err := store.Put(t.Context(), "large/file.bin", bytes.NewReader(body), &storage.PutOptions{
				ContentType: "application/zip", Metadata: map[string]string{"owner": "user-42"},
			})
			require.NoError(t, err)
			require.Equal(t, int64(size), object.Size)
			require.Equal(t, body, readAll(t, store, "large/file.bin"))

			stat, err := store.Stat(t.Context(), "large/file.bin")
			require.NoError(t, err)
			require.Equal(t, "application/zip", stat.ContentType)
			require.Equal(t, map[string]string{"owner": "user-42"}, stat.Metadata)
			require.Zero(t, server.PendingUploads())
		})
	}
}

func TestFailedMultipartUploadsAreAborted(t *testing.T) {
	for name, operation := range map[string]string{"a part": s3fake.OperationUploadPart, "the completion": s3fake.OperationCompleteUpload} {
		t.Run(name, func(t *testing.T) {
			server := s3fake.New(t.Cleanup, bucket)
			store := newStore(t, server, partSize)
			_, err := store.Put(t.Context(), "file.bin", strings.NewReader("previous"), nil)
			require.NoError(t, err)

			server.FailNext(operation, 1)
			_, err = store.Put(t.Context(), "file.bin", bytes.NewReader(content(2*partSize+1)), nil)
			require.ErrorContains(t, err, "AccessDenied")
			require.Zero(t, server.PendingUploads(), "the upload was aborted")
			require.Equal(t, []byte("previous"), readAll(t, store, "file.bin"))
		})
	}
}

func TestMultipartUploadAboveTheMaximumSize(t *testing.T) {
	server := s3fake.New(t.Cleanup, bucket)
	store := newStore(t, server, partSize)
	_, err := store.Put(t.Context(), "file.bin", bytes.NewReader(content(2*partSize)), &storage.PutOptions{MaximumSize: partSize + 10})
	require.ErrorIs(t, err, storage.ErrTooLarge)
	require.Zero(t, server.PendingUploads())
	_, err = store.Stat(t.Context(), "file.bin")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

// cancellingReader cancels the upload's context once its first part was read.
type cancellingReader struct {
	reader io.Reader
	read   int
	cancel context.CancelFunc
}

func (r *cancellingReader) Read(buffer []byte) (int, error) {
	if r.read >= partSize {
		r.cancel()
	}
	count, err := r.reader.Read(buffer)
	r.read += count
	return count, err
}

func TestCancelledMultipartUploadIsAborted(t *testing.T) {
	server := s3fake.New(t.Cleanup, bucket)
	store := newStore(t, server, partSize)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := store.Put(ctx, "file.bin", &cancellingReader{reader: bytes.NewReader(content(3 * partSize)), cancel: cancel}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, server.PendingUploads(), "the abort ran on a context of its own")
}

func TestSingleRequestFailure(t *testing.T) {
	server := s3fake.New(t.Cleanup, bucket)
	store := newStore(t, server, 0)
	server.FailNext(s3fake.OperationPutObject, 1)
	_, err := store.Put(t.Context(), "file.txt", strings.NewReader("x"), nil)
	require.ErrorContains(t, err, "AccessDenied")

	server.FailNext(s3fake.OperationGetObject, 1)
	_, err = store.Put(t.Context(), "file.txt", strings.NewReader("x"), nil)
	require.NoError(t, err)
	_, err = store.Get(t.Context(), "file.txt", nil)
	require.ErrorContains(t, err, "AccessDenied")
	require.NotErrorIs(t, err, storage.ErrNotFound)
}

func TestMissingBucketIsNoMissingObject(t *testing.T) {
	server := s3fake.New(t.Cleanup, "another-bucket")
	store := newStore(t, server, 0)
	_, err := store.Get(t.Context(), "file.txt", nil)
	require.ErrorContains(t, err, "NoSuchBucket")
	require.NotErrorIs(t, err, storage.ErrNotFound)
}

func TestCopyFailure(t *testing.T) {
	server := s3fake.New(t.Cleanup, bucket)
	store := newStore(t, server, 0)
	_, err := store.Put(t.Context(), "source", strings.NewReader("x"), nil)
	require.NoError(t, err)
	server.FailNext(s3fake.OperationCopyObject, 1)
	_, err = store.Copy(t.Context(), "source", "destination")
	require.ErrorContains(t, err, "AccessDenied")
}

// hostRecorder records the host of a request and answers 403, which the SDK does not retry.
type hostRecorder struct {
	host string
}

func (r *hostRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	r.host = request.URL.Host
	return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
}

func TestVirtualHostedStyle(t *testing.T) {
	recorder := &hostRecorder{}
	store, err := s3adapter.New(&s3adapter.Options{
		Endpoint: "https://s3.example.com", Bucket: bucket, AccessKeyID: "access", SecretAccessKey: "secret",
		HTTPClient: &http.Client{Transport: recorder},
	})
	require.NoError(t, err)
	_, err = store.Stat(t.Context(), "file.txt")
	require.Error(t, err)
	require.Equal(t, bucket+".s3.example.com", recorder.host, "without PathStyle the bucket is part of the host name")
}

// truncatedReader yields its content and then io.ErrUnexpectedEOF, like an HTTP request body
// whose client hung up before Content-Length bytes arrived.
type truncatedReader struct {
	content io.Reader
}

func (r *truncatedReader) Read(buffer []byte) (int, error) {
	count, err := r.content.Read(buffer)
	if errors.Is(err, io.EOF) {
		return count, io.ErrUnexpectedEOF
	}
	return count, err
}

func TestTruncatedBodyIsNoUpload(t *testing.T) {
	for name, size := range map[string]int{"within the first part": partSize / 2, "within a later part": partSize + 1, "at a part boundary": 2 * partSize} {
		t.Run(name, func(t *testing.T) {
			server := s3fake.New(t.Cleanup, bucket)
			store := newStore(t, server, partSize)
			_, err := store.Put(t.Context(), "file.bin", strings.NewReader("previous"), nil)
			require.NoError(t, err)

			_, err = store.Put(t.Context(), "file.bin", &truncatedReader{content: bytes.NewReader(content(size))}, nil)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.Zero(t, server.PendingUploads(), "the upload was aborted")
			require.Equal(t, []byte("previous"), readAll(t, store, "file.bin"), "a truncated body replaces nothing")
		})
	}
}

// beforeCopy runs replace once, right before the first CopyObject request is sent.
type beforeCopy struct {
	replace func()
	once    sync.Once
}

func (b *beforeCopy) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Header.Get("X-Amz-Copy-Source") != "" {
		b.once.Do(b.replace)
	}
	return http.DefaultTransport.RoundTrip(request)
}

func TestCopyReturnsTheAttributesOfTheCopiedRevision(t *testing.T) {
	server := s3fake.New(t.Cleanup, bucket)
	writer := newStore(t, server, 0)
	_, err := writer.Put(t.Context(), "source", strings.NewReader("1"), &storage.PutOptions{ContentType: "text/plain"})
	require.NoError(t, err)
	hook := &beforeCopy{replace: func() {
		_, err := writer.Put(t.Context(), "source", bytes.NewReader(content(100)), &storage.PutOptions{ContentType: "application/pdf"})
		require.NoError(t, err)
	}}
	store, err := s3adapter.New(&s3adapter.Options{
		Endpoint: server.URL, Bucket: bucket, AccessKeyID: "access", SecretAccessKey: "secret", PathStyle: true,
		HTTPClient: &http.Client{Transport: hook},
	})
	require.NoError(t, err)

	copied, err := store.Copy(t.Context(), "source", "destination")
	require.NoError(t, err)
	stat, err := store.Stat(t.Context(), "destination")
	require.NoError(t, err)
	require.Equal(t, int64(100), stat.Size, "the copy holds the replacement")
	require.Equal(t, stat.Size, copied.Size, "Copy reports the revision it copied")
	require.Equal(t, stat.ContentType, copied.ContentType)
}
