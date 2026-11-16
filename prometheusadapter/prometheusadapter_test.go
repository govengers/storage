package prometheusadapter_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/prometheusadapter"
)

func TestMetrics(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
	require.NoError(t, err)
	store, err := storage.NewObservedStore(&storage.ObservedStoreOptions{Store: memory.New(), Name: "attachments", Observers: []storage.Observer{metrics}})
	require.NoError(t, err)

	_, err = store.Put(t.Context(), "file", strings.NewReader("0123456789"), nil)
	require.NoError(t, err)
	_, err = store.Stat(t.Context(), "missing")
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = store.Put(t.Context(), "file", strings.NewReader("too large"), &storage.PutOptions{MaximumSize: 2})
	require.ErrorIs(t, err, storage.ErrTooLarge)

	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP todo_storage_operations_total Finished storage operations by store, operation and outcome.
# TYPE todo_storage_operations_total counter
todo_storage_operations_total{operation="put",outcome="success",store="attachments"} 1
todo_storage_operations_total{operation="put",outcome="too_large",store="attachments"} 1
todo_storage_operations_total{operation="stat",outcome="not_found",store="attachments"} 1
# HELP todo_storage_bytes_total Bytes stored by put and copy and read by get, by store and operation.
# TYPE todo_storage_bytes_total counter
todo_storage_bytes_total{operation="put",store="attachments"} 10
`), "todo_storage_operations_total", "todo_storage_bytes_total"))
	count, err := testutil.GatherAndCount(registry, "todo_storage_operation_duration_seconds")
	require.NoError(t, err)
	require.Equal(t, 2, count, "one histogram per store and operation")
}

func TestNewValidatesOptionsAndRegistration(t *testing.T) {
	_, err := prometheusadapter.New(nil)
	require.Error(t, err)
	_, err = prometheusadapter.New(&prometheusadapter.Options{Namespace: "todo"})
	require.Error(t, err)
	registry := prometheus.NewRegistry()
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: registry})
	require.Error(t, err)

	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
	require.NoError(t, err)
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
	require.Error(t, err, "a duplicate registration fails")
}
