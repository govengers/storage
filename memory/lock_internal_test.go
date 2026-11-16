package memory

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
)

// A write whose context ends while it waits for the lock a List holds stores nothing.
func TestWriteCancelledWhileWaitingForTheLock(t *testing.T) {
	testCases := map[string]func(ctx context.Context, store *Store) error{
		"put": func(ctx context.Context, store *Store) error {
			_, err := store.Put(ctx, "written", strings.NewReader("content"), nil)
			return err
		},
		"copy": func(ctx context.Context, store *Store) error {
			_, err := store.Copy(ctx, "source", "written")
			return err
		},
	}
	for name, write := range testCases {
		t.Run(name, func(t *testing.T) {
			store := New()
			_, err := store.Put(t.Context(), "source", strings.NewReader("content"), nil)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())

			store.mutex.RLock() // a List in progress
			result := make(chan error, 1)
			go func() { result <- write(ctx, store) }()
			for store.mutex.TryRLock() { // fails once the write waits for the lock
				store.mutex.RUnlock()
				runtime.Gosched()
			}
			cancel()
			store.mutex.RUnlock()

			require.ErrorIs(t, <-result, context.Canceled)
			_, err = store.Stat(t.Context(), "written")
			require.ErrorIs(t, err, storage.ErrNotFound)
		})
	}
}
