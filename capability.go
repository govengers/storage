package storage

import (
	"context"
	"errors"
	"fmt"
)

// Copy stores the object under source under destination as well, with its content type and
// metadata, and returns the copy's attributes. A Copier copies inside the backend; for every
// other store, and when the Copier reports ErrNotSupported, Copy streams the object through Get
// and Put. It returns ErrNotFound for a missing source.
func Copy(ctx context.Context, store Store, source, destination string) (*Object, error) {
	if copier, ok := store.(Copier); ok {
		object, err := copier.Copy(ctx, source, destination)
		if !errors.Is(err, ErrNotSupported) {
			return object, err
		}
	}
	return streamCopy(ctx, store, source, destination)
}

func streamCopy(ctx context.Context, store Store, source, destination string) (object *Object, err error) {
	if err := ValidateKey(destination); err != nil {
		return nil, err
	}
	reader, err := store.Get(ctx, source, nil)
	if err != nil {
		return nil, fmt.Errorf("copy %q to %q: %w", source, destination, err)
	}
	defer func() {
		if closeError := reader.Close(); closeError != nil && err == nil {
			err = fmt.Errorf("copy %q to %q: close the source: %w", source, destination, closeError)
		}
	}()
	object, err = store.Put(ctx, destination, reader, &PutOptions{ContentType: reader.Object.ContentType, Metadata: reader.Object.Metadata})
	if err != nil {
		return nil, fmt.Errorf("copy %q to %q: %w", source, destination, err)
	}
	return object, nil
}

// PresignGet signs a GET of the object under key if store is a Presigner, and returns
// ErrNotSupported otherwise. Serve the object through the application instead in that case.
func PresignGet(ctx context.Context, store Store, key string, options *PresignGetOptions) (*PresignedRequest, error) {
	presigner, ok := store.(Presigner)
	if !ok {
		return nil, fmt.Errorf("presign a GET of %q: %w", key, ErrNotSupported)
	}
	return presigner.PresignGet(ctx, key, options)
}

// PresignPut signs a PUT of an object under key if store is a Presigner, and returns
// ErrNotSupported otherwise. Accept the upload through the application instead in that case.
func PresignPut(ctx context.Context, store Store, key string, options *PresignPutOptions) (*PresignedRequest, error) {
	presigner, ok := store.(Presigner)
	if !ok {
		return nil, fmt.Errorf("presign a PUT of %q: %w", key, ErrNotSupported)
	}
	return presigner.PresignPut(ctx, key, options)
}
