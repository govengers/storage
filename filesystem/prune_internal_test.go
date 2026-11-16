package filesystem

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPruningNeverRemovesAnObject replays the end of a Delete of "a/b" that paused before
// pruning, while another Delete pruned "a" and a Put stored the object "a".
func TestPruningNeverRemovesAnObject(t *testing.T) {
	store, err := New(&Options{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, err = store.Put(t.Context(), "a", strings.NewReader("x"), nil)
	require.NoError(t, err)

	store.pruneDirectories(path.Dir(objectPath("a/b")))
	_, err = store.Stat(t.Context(), "a")
	require.NoError(t, err, "the object a survives the pruning of the folder a")
}
