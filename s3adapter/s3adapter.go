// Package s3adapter is a storage.Store on an S3-compatible object store: AWS S3, MinIO, RustFS,
// Ceph RGW, Garage and others that speak the S3 REST API with SigV4. It is also a
// storage.Copier (server-side copy) and a storage.Presigner.
//
// The client is the AWS SDK for Go v2 (service/s3) with its checksum extensions limited to
// what an operation requires, so S3-compatible servers that lack the newer CRC trailers accept
// every request. Uploads are buffered one part at a time (PartSize, 8 MiB by default): a body
// that fits one part is sent with a single PutObject, a larger one as a multipart upload that
// is aborted on any failure, so a failed Put leaves no object and no orphaned parts. A body
// may hold at most 10000 parts, 80 GB with the default part size.
//
// New makes no request: the bucket must exist, created by whoever provisions the store.
package s3adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/govengers/storage"
)

// Part sizes of multipart uploads.
const (
	DefaultPartSize = 8 << 20
	MinimumPartSize = 5 << 20 // the smallest part S3 accepts, except the last
	MaximumPartSize = 5 << 30
	maximumParts    = 10000
	// maximumCopySize is the largest object one CopyObject request copies; Copy reports
	// ErrNotSupported above it, and storage.Copy streams the object instead.
	maximumCopySize = 5 << 30
	// copyAttempts bounds the HeadObject and CopyObject pairs of a Copy whose source is replaced
	// between the two.
	copyAttempts  = 3
	defaultRegion = "us-east-1"
	// abortTimeout bounds the AbortMultipartUpload after a failed upload, which runs even when
	// the caller's context is done.
	abortTimeout = 30 * time.Second
)

// Options configures a Store.
type Options struct {
	// Endpoint is the URL of the S3 API, with scheme: "https://s3.eu-central-1.amazonaws.com",
	// "http://rustfs.storage.svc:9000". Required.
	Endpoint string
	// Region signs the requests; empty means "us-east-1", which most S3-compatible servers
	// expect.
	Region string
	// Bucket holds the objects; required.
	Bucket string
	// AccessKeyID and SecretAccessKey are the static credentials; required. SessionToken is
	// optional, for temporary credentials.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// PathStyle puts the bucket into the path (http://host/bucket/key) instead of the host name
	// (http://bucket.host/key). MinIO, RustFS and most self-hosted servers need it; AWS S3
	// does not.
	PathStyle bool
	// PartSize is the size of one part of a multipart upload and the buffer one Put holds;
	// zero means DefaultPartSize. From MinimumPartSize to MaximumPartSize.
	PartSize int64
	// HTTPClient sends the requests; nil means the SDK's default client. Pass one with an
	// otelhttp transport to trace the requests. Leave its Timeout zero: it would cut off long
	// uploads and downloads, which the context bounds instead.
	HTTPClient *http.Client
}

// Store is a storage.Store on one bucket. It is safe for concurrent use.
type Store struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
	partSize  int64
}

var (
	_ storage.Store     = (*Store)(nil)
	_ storage.Copier    = (*Store)(nil)
	_ storage.Presigner = (*Store)(nil)
)

// New returns a Store on options.Bucket; it makes no request.
func New(options *Options) (*Store, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	region := options.Region
	if region == "" {
		region = defaultRegion
	}
	partSize := options.PartSize
	if partSize == 0 {
		partSize = DefaultPartSize
	}
	credentials := aws.Credentials{
		AccessKeyID: options.AccessKeyID, SecretAccessKey: options.SecretAccessKey, SessionToken: options.SessionToken, Source: "s3adapter",
	}
	clientOptions := s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(options.Endpoint),
		UsePathStyle: options.PathStyle,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return credentials, nil
		}),
		// The default "when supported" sends CRC trailers that many S3-compatible servers reject.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if options.HTTPClient != nil {
		clientOptions.HTTPClient = options.HTTPClient
	}
	client := s3.New(clientOptions)
	return &Store{client: client, presigner: s3.NewPresignClient(client), bucket: options.Bucket, partSize: partSize}, nil
}

func validateOptions(options *Options) error {
	if options == nil || options.Endpoint == "" || options.Bucket == "" || options.AccessKeyID == "" || options.SecretAccessKey == "" {
		return errors.New("s3adapter: Options needs an Endpoint, a Bucket, an AccessKeyID and a SecretAccessKey")
	}
	endpoint, err := url.Parse(options.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return fmt.Errorf("s3adapter: Endpoint %q is no http or https URL", options.Endpoint)
	}
	if options.PartSize != 0 && (options.PartSize < MinimumPartSize || options.PartSize > MaximumPartSize) {
		return fmt.Errorf("s3adapter: PartSize %d is outside %d to %d", options.PartSize, MinimumPartSize, MaximumPartSize)
	}
	return nil
}

// Get opens the object, or its range, with one GetObject request; the content streams from
// the response.
func (s *Store) Get(ctx context.Context, key string, options *storage.GetOptions) (*storage.Reader, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolveGetOptions(options)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, err)
	}
	input := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Range: rangeHeader(resolved)}
	output, err := s.client.GetObject(ctx, input)
	if err != nil && input.Range != nil && resolved.Offset == 0 && errors.Is(mapError(ctx, err), storage.ErrInvalidRange) {
		// S3 rejects every range on an empty object; an Offset of zero is valid there.
		input.Range = nil
		output, err = s.client.GetObject(ctx, input)
	}
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, mapError(ctx, err))
	}
	reader, err := newReader(key, output)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("get %q: %w", key, err), output.Body.Close())
	}
	return reader, nil
}

// rangeHeader is the HTTP Range of a Get; nil reads the whole object.
func rangeHeader(options storage.GetOptions) *string {
	switch {
	case options.Length > 0:
		return aws.String(fmt.Sprintf("bytes=%d-%d", options.Offset, options.Offset+options.Length-1))
	case options.Offset > 0:
		return aws.String(fmt.Sprintf("bytes=%d-", options.Offset))
	default:
		return nil
	}
}

// newReader reads the object and the range from a GetObject response; a ranged response names
// the object's size in its Content-Range ("bytes 2-4/10").
func newReader(key string, output *s3.GetObjectOutput) (*storage.Reader, error) {
	length := aws.ToInt64(output.ContentLength)
	object := toObject(key, length, output.ContentType, output.Metadata, output.LastModified)
	if output.ContentRange == nil {
		return storage.NewReader(object, 0, length, output.Body), nil
	}
	var offset, last int64
	if _, err := fmt.Sscanf(*output.ContentRange, "bytes %d-%d/%d", &offset, &last, &object.Size); err != nil {
		return nil, fmt.Errorf("parse the Content-Range %q: %w", *output.ContentRange, err)
	}
	return storage.NewReader(object, offset, last-offset+1, output.Body), nil
}

func toObject(key string, size int64, contentType *string, metadata map[string]string, modifiedAt *time.Time) *storage.Object {
	object := &storage.Object{Key: key, Size: size, ContentType: aws.ToString(contentType), ModifiedAt: aws.ToTime(modifiedAt)}
	if len(metadata) > 0 {
		object.Metadata = make(map[string]string, len(metadata))
		for name, value := range metadata {
			object.Metadata[strings.ToLower(name)] = value
		}
	}
	return object
}

// Stat reads the object's attributes with a HeadObject request.
func (s *Store) Stat(ctx context.Context, key string) (*storage.Object, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	object, _, err := s.head(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", key, err)
	}
	return object, nil
}

// head reads the object's attributes and its ETag, which names the revision, with a
// HeadObject request.
func (s *Store) head(ctx context.Context, key string) (*storage.Object, *string, error) {
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, nil, mapError(ctx, err)
	}
	return toObject(key, aws.ToInt64(output.ContentLength), output.ContentType, output.Metadata, output.LastModified), output.ETag, nil
}

// Delete removes the object with a DeleteObject request, which S3 answers alike for a missing
// object.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete %q: %w", key, mapError(ctx, err))
	}
	return nil
}

// List reads one page with a ListObjectsV2 request.
func (s *Store) List(ctx context.Context, options *storage.ListOptions) (*storage.Page, error) {
	resolved, err := storage.ResolveListOptions(options)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), MaxKeys: aws.Int32(int32(resolved.Limit))} //nolint:gosec // G115: Limit is at most MaximumListLimit
	if resolved.Prefix != "" {
		input.Prefix = aws.String(resolved.Prefix)
	}
	if resolved.StartAfter != "" {
		input.StartAfter = aws.String(resolved.StartAfter)
	}
	output, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", resolved.Prefix, mapError(ctx, err))
	}
	page := &storage.Page{Objects: make([]storage.Object, 0, len(output.Contents))}
	for _, listed := range output.Contents {
		page.Objects = append(page.Objects, storage.Object{Key: aws.ToString(listed.Key), Size: aws.ToInt64(listed.Size), ModifiedAt: aws.ToTime(listed.LastModified)})
	}
	if aws.ToBool(output.IsTruncated) && len(page.Objects) > 0 {
		page.Next = page.Objects[len(page.Objects)-1].Key
	}
	return page, nil
}

// mapError translates the SDK's error into the storage sentinels, or keeps only its message
// (LIB-010: SDK error types are no part of this API). A done context wins over the SDK's
// rendering of it.
func mapError(ctx context.Context, err error) error {
	if ctxError := ctx.Err(); ctxError != nil {
		return ctxError
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return storage.ErrNotFound
		case "InvalidRange":
			return storage.ErrInvalidRange
		default:
		}
	}
	var responseError *awshttp.ResponseError
	if errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusRequestedRangeNotSatisfiable {
		return storage.ErrInvalidRange
	}
	return fmt.Errorf("s3: %v", err) //nolint:errorlint // LIB-010: the SDK's errors stay out of the API
}
