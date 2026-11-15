package storage_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/storagetest"
)

// recorder is an Observer that records its calls as "<name> started <operation>" and
// "<name> finished <operation>" lines.
type recorder struct {
	name     string
	mutex    *sync.Mutex
	lines    *[]string
	outcomes map[storage.OperationName]*storage.Outcome
}

type contextKey struct{}

func newRecorders(names ...string) ([]storage.Observer, *[]string, []*recorder) {
	mutex := &sync.Mutex{}
	lines := &[]string{}
	observers := make([]storage.Observer, 0, len(names))
	recorders := make([]*recorder, 0, len(names))
	for _, name := range names {
		observer := &recorder{name: name, mutex: mutex, lines: lines, outcomes: map[storage.OperationName]*storage.Outcome{}}
		observers = append(observers, observer)
		recorders = append(recorders, observer)
	}
	return observers, lines, recorders
}

func (r *recorder) OperationStarted(ctx context.Context, operation *storage.Operation) context.Context {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	*r.lines = append(*r.lines, fmt.Sprintf("%s started %s %s", r.name, operation.Store, operation.Name))
	return context.WithValue(ctx, contextKey{}, r.name)
}

func (r *recorder) OperationFinished(ctx context.Context, operation *storage.Operation, outcome *storage.Outcome) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	*r.lines = append(*r.lines, fmt.Sprintf("%s finished %s in context of %v", r.name, operation.Name, ctx.Value(contextKey{})))
	r.outcomes[operation.Name] = outcome
}

func newObservedStore(t *testing.T, store storage.Store, observers ...storage.Observer) *storage.ObservedStore {
	t.Helper()
	observed, err := storage.NewObservedStore(&storage.ObservedStoreOptions{Store: store, Name: "attachments", Observers: observers})
	require.NoError(t, err)
	return observed
}

func TestObservedStoreConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Store {
		observers, _, _ := newRecorders("only")
		return newObservedStore(t, memory.New(), observers...)
	})
}

func TestObservedStoreWithoutCapabilitiesConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Store {
		observers, _, _ := newRecorders("only")
		return newObservedStore(t, plainStore{memory.New()}, observers...)
	})
}

func TestNewObservedStoreValidatesOptions(t *testing.T) {
	observers, _, _ := newRecorders("a")
	for _, options := range []*storage.ObservedStoreOptions{
		nil,
		{Name: "a", Observers: observers},
		{Store: memory.New(), Observers: observers},
		{Store: memory.New(), Name: "a"},
		{Store: memory.New(), Name: "a", Observers: []storage.Observer{nil}},
	} {
		_, err := storage.NewObservedStore(options)
		require.Error(t, err)
	}
}

func TestObserversRunInOrderAndFinishInReverse(t *testing.T) {
	observers, lines, _ := newRecorders("outer", "inner")
	store := newObservedStore(t, memory.New(), observers...)
	_, err := store.Stat(t.Context(), "missing")
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.Equal(t, []string{
		"outer started attachments stat",
		"inner started attachments stat",
		"inner finished stat in context of inner",
		"outer finished stat in context of inner",
	}, *lines)
}

func TestObservedOutcomes(t *testing.T) {
	observers, _, recorders := newRecorders("only")
	store := newObservedStore(t, memory.New(), observers...)
	outcomes := recorders[0].outcomes

	_, err := store.Put(t.Context(), "file", strings.NewReader("0123456789"), nil)
	require.NoError(t, err)
	require.Equal(t, int64(10), outcomes[storage.OperationPut].Bytes)
	require.NoError(t, outcomes[storage.OperationPut].Err)

	reader, err := store.Get(t.Context(), "file", &storage.GetOptions{Offset: 2})
	require.NoError(t, err)
	require.Nil(t, outcomes[storage.OperationGet], "a get is reported when its reader closes")
	require.Equal(t, int64(2), reader.Offset)
	require.Equal(t, int64(8), reader.Length)
	_, err = io.CopyN(io.Discard, reader, 3)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, reader.Close(), "a second close reports nothing twice")
	require.Equal(t, int64(3), outcomes[storage.OperationGet].Bytes)

	_, err = store.Get(t.Context(), "missing", nil)
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.ErrorIs(t, outcomes[storage.OperationGet].Err, storage.ErrNotFound)

	_, err = store.Copy(t.Context(), "file", "copy")
	require.NoError(t, err)
	require.Equal(t, int64(10), outcomes[storage.OperationCopy].Bytes)

	require.NoError(t, store.Delete(t.Context(), "copy"))
	require.NotNil(t, outcomes[storage.OperationDelete])
	_, err = store.List(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, outcomes[storage.OperationList])
	_, err = store.PresignGet(t.Context(), "file", nil)
	require.ErrorIs(t, err, storage.ErrNotSupported)
	require.Nil(t, outcomes[storage.OperationPresign], "a capability the store lacks is no operation")
}

func TestObservedGetReportsReadErrors(t *testing.T) {
	errFailed := errors.New("read failed")
	observers, _, recorders := newRecorders("only")
	store := newObservedStore(t, failingReadStore{Store: memory.New(), err: errFailed}, observers...)
	_, err := store.Put(t.Context(), "file", strings.NewReader("content"), nil)
	require.NoError(t, err)
	reader, err := store.Get(t.Context(), "file", nil)
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.ErrorIs(t, err, errFailed)
	require.NoError(t, reader.Close())
	require.ErrorIs(t, recorders[0].outcomes[storage.OperationGet].Err, errFailed)
}

// plainStore hides the capabilities of the store it embeds.
type plainStore struct {
	storage.Store
}

// failingReadStore returns readers that fail with err.
type failingReadStore struct {
	storage.Store
	err error
}

func (s failingReadStore) Get(ctx context.Context, key string, options *storage.GetOptions) (*storage.Reader, error) {
	reader, err := s.Store.Get(ctx, key, options)
	if err != nil {
		return nil, err
	}
	return storage.NewReader(&reader.Object, reader.Offset, reader.Length, io.NopCloser(iotest.ErrReader(s.err))), nil
}

func TestClassifyError(t *testing.T) {
	testCases := map[storage.ErrorClass]error{
		storage.ErrorClassSuccess:     nil,
		storage.ErrorClassNotFound:    fmt.Errorf("get %q: %w", "k", storage.ErrNotFound),
		storage.ErrorClassInvalid:     storage.ErrInvalidRange,
		storage.ErrorClassTooLarge:    storage.ErrTooLarge,
		storage.ErrorClassUnsupported: storage.ErrNotSupported,
		storage.ErrorClassCancelled:   context.DeadlineExceeded,
		storage.ErrorClassError:       errors.New("connection reset"),
	}
	for class, err := range testCases {
		require.Equal(t, class, storage.ClassifyError(err), "error %v", err)
	}
	require.Equal(t, storage.ErrorClassInvalid, storage.ClassifyError(storage.ErrUnsupportedContentType))
	require.Equal(t, storage.ErrorClassInvalid, storage.ClassifyError(storage.ErrInvalid))
	require.Equal(t, storage.ErrorClassCancelled, storage.ClassifyError(context.Canceled))
}

// presigningStore signs fixed requests, to observe the forwarding of an ObservedStore.
type presigningStore struct {
	storage.Store
}

func (presigningStore) PresignGet(_ context.Context, key string, _ *storage.PresignGetOptions) (*storage.PresignedRequest, error) {
	return &storage.PresignedRequest{Method: "GET", URL: "https://example.com/" + key}, nil
}

func (presigningStore) PresignPut(_ context.Context, key string, _ *storage.PresignPutOptions) (*storage.PresignedRequest, error) {
	return &storage.PresignedRequest{Method: "PUT", URL: "https://example.com/" + key}, nil
}

func TestObservedPresigning(t *testing.T) {
	observers, lines, _ := newRecorders("only")
	store := newObservedStore(t, presigningStore{memory.New()}, observers...)
	get, err := storage.PresignGet(t.Context(), store, "a", nil)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/a", get.URL)
	put, err := storage.PresignPut(t.Context(), store, "b", nil)
	require.NoError(t, err)
	require.Equal(t, "PUT", put.Method)
	require.Len(t, *lines, 4, "both presigns are observed")

	_, err = newObservedStore(t, plainStore{memory.New()}, observers...).PresignPut(t.Context(), "b", nil)
	require.ErrorIs(t, err, storage.ErrNotSupported)
}
