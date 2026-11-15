package s3adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/govengers/storage"
)

// Put reads the first part; a body that ends within it is stored with one PutObject request,
// a longer one with a multipart upload.
func (s *Store) Put(ctx context.Context, key string, body io.Reader, options *storage.PutOptions) (*storage.Object, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolvePutOptions(options)
	if err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	reader := storage.ContextReader(ctx, body)
	if resolved.MaximumSize > 0 {
		reader = storage.LimitReader(reader, resolved.MaximumSize)
	}
	first, err := io.ReadAll(io.LimitReader(reader, s.partSize))
	if err != nil {
		return nil, fmt.Errorf("put %q: read the body: %w", key, err)
	}
	size := int64(len(first))
	if size < s.partSize {
		err = s.putObject(ctx, key, first, resolved)
	} else {
		size, err = s.putMultipart(ctx, key, first, reader, resolved)
	}
	if err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	return &storage.Object{Key: key, Size: size, ContentType: resolved.ContentType, Metadata: maps.Clone(resolved.Metadata)}, nil
}

func (s *Store) putObject(ctx context.Context, key string, content []byte, options storage.PutOptions) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(content),
		ContentLength: aws.Int64(int64(len(content))), ContentType: aws.String(options.ContentType), Metadata: options.Metadata,
	})
	if err != nil {
		return mapError(ctx, err)
	}
	return nil
}

// putMultipart uploads first and the rest of reader as parts of one multipart upload and
// returns the bytes stored. Any failure aborts the upload, so S3 keeps no orphaned parts.
func (s *Store) putMultipart(ctx context.Context, key string, first []byte, reader io.Reader, options storage.PutOptions) (size int64, err error) {
	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(options.ContentType), Metadata: options.Metadata,
	})
	if err != nil {
		return 0, fmt.Errorf("start a multipart upload: %w", mapError(ctx, err))
	}
	upload := &multipartUpload{store: s, key: key, uploadID: created.UploadId}
	defer func() {
		if err != nil {
			err = errors.Join(err, upload.abort(ctx))
		}
	}()
	size, err = upload.uploadParts(ctx, first, reader)
	if err != nil {
		return 0, err
	}
	if err := upload.complete(ctx); err != nil {
		return 0, err
	}
	return size, nil
}

type multipartUpload struct {
	store    *Store
	key      string
	uploadID *string
	parts    []types.CompletedPart
}

// uploadParts sends first as part 1 and every following part of reader, reusing first's
// buffer, so one upload holds one part in memory.
func (u *multipartUpload) uploadParts(ctx context.Context, first []byte, reader io.Reader) (int64, error) {
	size := int64(len(first))
	if err := u.uploadPart(ctx, first); err != nil {
		return 0, err
	}
	buffer := first[:u.store.partSize]
	for {
		count, readError := io.ReadFull(reader, buffer)
		if count > 0 {
			if len(u.parts) == maximumParts {
				return 0, fmt.Errorf("the body needs more than %d parts of %d bytes: %w", maximumParts, u.store.partSize, storage.ErrTooLarge)
			}
			if err := u.uploadPart(ctx, buffer[:count]); err != nil {
				return 0, err
			}
			size += int64(count)
		}
		if errors.Is(readError, io.EOF) || errors.Is(readError, io.ErrUnexpectedEOF) {
			return size, nil
		}
		if readError != nil {
			return 0, fmt.Errorf("read the body: %w", readError)
		}
	}
}

func (u *multipartUpload) uploadPart(ctx context.Context, content []byte) error {
	partNumber := aws.Int32(int32(len(u.parts) + 1)) //nolint:gosec // G115: at most maximumParts
	output, err := u.store.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(u.store.bucket), Key: aws.String(u.key), UploadId: u.uploadID, PartNumber: partNumber,
		Body: bytes.NewReader(content), ContentLength: aws.Int64(int64(len(content))),
	})
	if err != nil {
		return fmt.Errorf("upload part %d: %w", *partNumber, mapError(ctx, err))
	}
	u.parts = append(u.parts, types.CompletedPart{PartNumber: partNumber, ETag: output.ETag})
	return nil
}

func (u *multipartUpload) complete(ctx context.Context) error {
	_, err := u.store.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(u.store.bucket), Key: aws.String(u.key), UploadId: u.uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: u.parts},
	})
	if err != nil {
		return fmt.Errorf("complete the multipart upload: %w", mapError(ctx, err))
	}
	return nil
}

// abort discards the upload's parts, also when ctx is done: a cancelled Put must not leave
// parts behind that S3 bills until a lifecycle rule removes them.
func (u *multipartUpload) abort(ctx context.Context) error {
	abortContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()
	_, err := u.store.client.AbortMultipartUpload(abortContext, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(u.store.bucket), Key: aws.String(u.key), UploadId: u.uploadID,
	})
	if err != nil {
		return fmt.Errorf("abort the multipart upload: %w", mapError(abortContext, err))
	}
	return nil
}
