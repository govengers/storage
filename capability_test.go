package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
)

// unsupportedCopier is a Copier whose Copy always reports ErrNotSupported.
type unsupportedCopier struct {
	storage.Store
	calls int
}

func (c *unsupportedCopier) Copy(context.Context, string, string) (*storage.Object, error) {
	c.calls++
	return nil, storage.ErrNotSupported
}

func TestCopyFallsBackToStreaming(t *testing.T) {
	copier := &unsupportedCopier{Store: memory.New()}
	_, err := copier.Put(t.Context(), "source", strings.NewReader("content"), &storage.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"a": "b"}})
	require.NoError(t, err)

	copied, err := storage.Copy(t.Context(), copier, "source", "destination")
	require.NoError(t, err)
	require.Equal(t, 1, copier.calls, "the Copier was asked first")
	require.Equal(t, int64(len("content")), copied.Size)
	stat, err := copier.Stat(t.Context(), "destination")
	require.NoError(t, err)
	require.Equal(t, "text/plain", stat.ContentType)
	require.Equal(t, map[string]string{"a": "b"}, stat.Metadata)

	_, err = storage.Copy(t.Context(), plainStore{memory.New()}, "missing", "destination")
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = storage.Copy(t.Context(), plainStore{memory.New()}, "missing", "/invalid")
	require.ErrorIs(t, err, storage.ErrInvalid, "the destination is checked before the source is read")
}

func TestPresignWithoutPresigner(t *testing.T) {
	_, err := storage.PresignGet(t.Context(), memory.New(), "key", nil)
	require.ErrorIs(t, err, storage.ErrNotSupported)
	_, err = storage.PresignPut(t.Context(), memory.New(), "key", nil)
	require.ErrorIs(t, err, storage.ErrNotSupported)
}
