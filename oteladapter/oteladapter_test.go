package oteladapter_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/oteladapter"
)

func newTracedStore(t *testing.T, store storage.Store) (*storage.ObservedStore, *tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer, err := oteladapter.New(&oteladapter.Options{TracerProvider: provider})
	require.NoError(t, err)
	observed, err := storage.NewObservedStore(&storage.ObservedStoreOptions{Store: store, Name: "attachments", Observers: []storage.Observer{tracer}})
	require.NoError(t, err)
	return observed, recorder, provider
}

func attributes(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	values := map[attribute.Key]attribute.Value{}
	for _, keyValue := range span.Attributes() {
		values[keyValue.Key] = keyValue.Value
	}
	return values
}

func TestSpans(t *testing.T) {
	store, recorder, provider := newTracedStore(t, memory.New())
	ctx, parent := provider.Tracer("test").Start(t.Context(), "request")
	_, err := store.Put(ctx, "users/jane/cv.pdf", strings.NewReader("0123456789"), nil)
	require.NoError(t, err)
	_, err = store.Stat(ctx, "missing")
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = store.Put(ctx, "too-large", strings.NewReader("0123456789"), &storage.PutOptions{MaximumSize: 2})
	require.ErrorIs(t, err, storage.ErrTooLarge)
	parent.End()

	spans := recorder.Ended()
	require.Len(t, spans, 4)
	put, stat, failed := spans[0], spans[1], spans[2]

	require.Equal(t, "storage.put", put.Name())
	require.Equal(t, parent.SpanContext().SpanID(), put.Parent().SpanID(), "the span is a child of the caller's")
	require.Equal(t, "attachments", attributes(put)[oteladapter.AttributeStore].AsString())
	require.Equal(t, "put", attributes(put)[oteladapter.AttributeOperation].AsString())
	require.Equal(t, int64(10), attributes(put)[oteladapter.AttributeBytes].AsInt64())
	require.Equal(t, codes.Unset, put.Status().Code)

	require.Equal(t, "storage.stat", stat.Name())
	require.Equal(t, codes.Unset, stat.Status().Code, "a missing object is no failure")
	require.Equal(t, "not_found", attributes(stat)[oteladapter.AttributeErrorClass].AsString())

	require.Equal(t, codes.Error, failed.Status().Code)
	require.Equal(t, "too_large", failed.Status().Description, "the status names the class, not the message with the key")
	for _, span := range spans {
		for _, keyValue := range span.Attributes() {
			require.NotContains(t, keyValue.Value.String(), "jane", "no key reaches a span")
		}
		require.Empty(t, span.Events(), "no error message reaches a span")
	}
}

func TestTwoTracersEndTheirOwnSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	first, err := oteladapter.New(&oteladapter.Options{TracerProvider: provider})
	require.NoError(t, err)
	second, err := oteladapter.New(&oteladapter.Options{TracerProvider: provider})
	require.NoError(t, err)
	store, err := storage.NewObservedStore(&storage.ObservedStoreOptions{Store: memory.New(), Name: "a", Observers: []storage.Observer{first, second}})
	require.NoError(t, err)
	require.NoError(t, store.Delete(t.Context(), "key"))
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	require.NotEqual(t, spans[0].SpanContext().SpanID(), spans[1].SpanContext().SpanID())
}

func TestNewRequiresAProvider(t *testing.T) {
	_, err := oteladapter.New(nil)
	require.Error(t, err)
	_, err = oteladapter.New(&oteladapter.Options{})
	require.Error(t, err)
}
