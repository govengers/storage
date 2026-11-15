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
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/govengers/storage"
)

// Copy copies inside the bucket with one CopyObject request, keeping the content type and
// metadata. It reads the source's attributes first and returns storage.ErrNotSupported for a
// source above 5 GiB, the limit of one CopyObject; storage.Copy then streams it. The copy is
// bound to the revision it read with x-amz-copy-source-if-match, so the attributes it returns
// are those of the copied content; a source replaced in between is read again.
func (s *Store) Copy(ctx context.Context, source, destination string) (*storage.Object, error) {
	if err := storage.ValidateKey(destination); err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		object, etag, err := s.head(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("copy %q to %q: %w", source, destination, err)
		}
		if source == destination {
			return object, nil // S3 refuses to copy an object onto itself unchanged
		}
		if object.Size > maximumCopySize {
			return nil, fmt.Errorf("copy %q of %d bytes in one request: %w", source, object.Size, storage.ErrNotSupported)
		}
		output, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(destination), CopySource: aws.String(copySource(s.bucket, source)), CopySourceIfMatch: etag,
		})
		if err != nil && isPreconditionFailed(err) && attempt < copyAttempts {
			continue // the source was replaced after the HeadObject
		}
		if err != nil {
			return nil, fmt.Errorf("copy %q to %q: %w", source, destination, mapError(ctx, err))
		}
		object.Key = destination
		if output.CopyObjectResult != nil {
			object.ModifiedAt = aws.ToTime(output.CopyObjectResult.LastModified)
		}
		return object, nil
	}
}

func isPreconditionFailed(err error) bool {
	var responseError *awshttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusPreconditionFailed
}

// copySource is the x-amz-copy-source header: bucket and key, every segment URL-encoded. S3
// decodes the header as a form value, where a literal '+' is a space, so '+' is escaped too.
func copySource(bucket, key string) string {
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		segments[index] = strings.ReplaceAll(url.PathEscape(segment), "+", "%2B")
	}
	return url.PathEscape(bucket) + "/" + strings.Join(segments, "/")
}

// PresignGet signs a GetObject; ContentDisposition and ContentType become the
// response-content-disposition and response-content-type parameters S3 answers with.
func (s *Store) PresignGet(ctx context.Context, key string, options *storage.PresignGetOptions) (*storage.PresignedRequest, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolvePresignGetOptions(options)
	if err != nil {
		return nil, fmt.Errorf("presign a GET of %q: %w", key, err)
	}
	input := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if resolved.ContentDisposition != "" {
		input.ResponseContentDisposition = aws.String(resolved.ContentDisposition)
	}
	if resolved.ContentType != "" {
		input.ResponseContentType = aws.String(resolved.ContentType)
	}
	signed, err := s.presigner.PresignGetObject(ctx, input, s3.WithPresignExpires(resolved.Expiry))
	if err != nil {
		return nil, fmt.Errorf("presign a GET of %q: %w", key, mapError(ctx, err))
	}
	return toPresignedRequest(signed, resolved.Expiry), nil
}

// PresignPut signs a PutObject with the content type as a signed header.
func (s *Store) PresignPut(ctx context.Context, key string, options *storage.PresignPutOptions) (*storage.PresignedRequest, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolvePresignPutOptions(options)
	if err != nil {
		return nil, fmt.Errorf("presign a PUT of %q: %w", key, err)
	}
	signed, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(resolved.ContentType),
	}, s3.WithPresignExpires(resolved.Expiry))
	if err != nil {
		return nil, fmt.Errorf("presign a PUT of %q: %w", key, mapError(ctx, err))
	}
	return toPresignedRequest(signed, resolved.Expiry), nil
}

// toPresignedRequest drops the signed Host header: clients derive it from the URL, and
// net/http ignores it in a header map.
func toPresignedRequest(signed *v4.PresignedHTTPRequest, expiry time.Duration) *storage.PresignedRequest {
	header := signed.SignedHeader.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Del("Host")
	return &storage.PresignedRequest{Method: signed.Method, URL: signed.URL, Header: header, ExpiresAt: time.Now().Add(expiry)}
}
