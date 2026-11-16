package filesystem_test

import (
	"context"
	"io"
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

func TestLayout(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	for _, key := range []string{"tasks/42", "tasks/42/attachment"} {
		_, err := store.Put(t.Context(), key, strings.NewReader("x"), nil)
		require.NoError(t, err)
	}
	require.FileExists(t, filepath.Join(root, "objects", "tasks.d", "42.o"))
	require.FileExists(t, filepath.Join(root, "objects", "tasks.d", "42.d", "attachment.o"))
}

func TestDeletePrunesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	_, err := store.Put(t.Context(), "deep/er/est/object", strings.NewReader("x"), nil)
	require.NoError(t, err)
	_, err = store.Put(t.Context(), "deep/sibling", strings.NewReader("x"), nil)
	require.NoError(t, err)
	require.NoError(t, store.Delete(t.Context(), "deep/er/est/object"))

	require.NoDirExists(t, filepath.Join(root, "objects", "deep.d", "er.d"))
	require.FileExists(t, filepath.Join(root, "objects", "deep.d", "sibling.o"))
	require.DirExists(t, filepath.Join(root, "objects"))
}

func TestCancelledPutLeavesNoDirectories(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	ctx, cancel := context.WithCancel(t.Context())
	_, err := store.Put(ctx, "tasks/42/upload", cancelAtEOF{cancel: cancel}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.NoDirExists(t, filepath.Join(root, "objects", "tasks.d"), "the directories of a cancelled Put are pruned")
	requireNoTemporaryFiles(t, root)
}

// cancelAtEOF cancels the Put's context in the Read that ends the body, so the Put fails at
// its commit point after creating the object's directories.
type cancelAtEOF struct{ cancel context.CancelFunc }

func (r cancelAtEOF) Read([]byte) (int, error) {
	r.cancel()
	return 0, io.EOF
}

func TestForeignFiles(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	_, err := store.Put(t.Context(), "docs/a.txt", strings.NewReader("x"), nil)
	require.NoError(t, err)
	for _, name := range []string{".DS_Store", "notes", "docs.d", ".o", "...d"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "objects", "docs.d", name), []byte("not a stored object"), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(root, "objects", "docs.d", "folder"), 0o750))
	require.NoError(t, os.Mkdir(filepath.Join(root, "objects", "docs.d", "folder.o"), 0o750))
	require.Equal(t, []string{"docs/a.txt"}, listKeys(t, store), "files and directories without the store's names are left out")
	_, err = store.Stat(t.Context(), "docs/folder")
	require.ErrorIs(t, err, storage.ErrNotFound)

	require.NoError(t, os.WriteFile(filepath.Join(root, "objects", "docs.d", "corrupt.o"), []byte("not a stored object"), 0o600))
	_, err = store.Stat(t.Context(), "docs/corrupt")
	require.Error(t, err)
	require.NotErrorIs(t, err, storage.ErrNotFound)
	_, err = store.List(t.Context(), nil)
	require.ErrorContains(t, err, `"docs/corrupt"`, "List names the corrupt object instead of guessing its size")
}

func listKeys(t *testing.T, store storage.Store) []string {
	t.Helper()
	page, err := store.List(t.Context(), nil)
	require.NoError(t, err)
	keys := []string{}
	for _, object := range page.Objects {
		keys = append(keys, object.Key)
	}
	return keys
}

func TestSymlinksCannotLeaveTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store := newStore(t, root)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "objects", "escape.d")))

	_, err := store.Put(t.Context(), "escape/object", strings.NewReader("x"), nil)
	require.Error(t, err)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing was written outside the root")

	page, err := store.List(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, page.Objects, "List leaves symlinks out")
}
