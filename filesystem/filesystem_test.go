package filesystem_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
	"github.com/govengers/storage/filesystem"
	"github.com/govengers/storage/storagetest"
)

func newStore(t *testing.T, root string) *filesystem.Store {
	t.Helper()
	store, err := filesystem.New(&filesystem.Options{Root: root})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Store {
		root := t.TempDir()
		t.Cleanup(func() { requireNoTemporaryFiles(t, root) })
		return newStore(t, root)
	})
}

// requireNoTemporaryFiles checks that no Put, failed or not, left a file in tmp.
func requireNoTemporaryFiles(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "tmp"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestNewRejectsAMissingRoot(t *testing.T) {
	_, err := filesystem.New(nil)
	require.Error(t, err)
	_, err = filesystem.New(&filesystem.Options{Root: filepath.Join(t.TempDir(), "missing")})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestAKeyCannotNameAFileAndADirectory(t *testing.T) {
	store := newStore(t, t.TempDir())
	_, err := store.Put(t.Context(), "a/b", strings.NewReader("x"), nil)
	require.NoError(t, err)
	_, err = store.Put(t.Context(), "a", strings.NewReader("x"), nil)
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = store.Put(t.Context(), "a/b/c", strings.NewReader("x"), nil)
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = store.Stat(t.Context(), "a/b/c")
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NoError(t, store.Delete(t.Context(), "a"), "a directory is no object to delete")

	require.NoError(t, store.Delete(t.Context(), "a/b"))
	_, err = store.Put(t.Context(), "a", strings.NewReader("x"), nil)
	require.NoError(t, err, "deleting the last key below a frees the name")
}

func TestDeletePrunesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	_, err := store.Put(t.Context(), "deep/er/est/object", strings.NewReader("x"), nil)
	require.NoError(t, err)
	_, err = store.Put(t.Context(), "deep/sibling", strings.NewReader("x"), nil)
	require.NoError(t, err)
	require.NoError(t, store.Delete(t.Context(), "deep/er/est/object"))

	require.NoDirExists(t, filepath.Join(root, "objects", "deep", "er"))
	require.FileExists(t, filepath.Join(root, "objects", "deep", "sibling"))
	require.DirExists(t, filepath.Join(root, "objects"))
}

func TestFilesOutsideTheFormatAreErrors(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "objects", "foreign"), []byte("not a stored object"), 0o600))
	_, err := store.Stat(t.Context(), "foreign")
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrNotFound)
	_, err = store.List(t.Context(), nil)
	require.Error(t, err, "List reports a corrupt file instead of guessing its size")
}

func TestSymlinksCannotLeaveTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store := newStore(t, root)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "objects", "escape")))

	_, err := store.Put(t.Context(), "escape/object", strings.NewReader("x"), nil)
	require.Error(t, err)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing was written outside the root")

	page, err := store.List(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, page.Objects, "List leaves symlinks out")
}
