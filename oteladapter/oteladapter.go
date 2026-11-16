// Package oteladapter traces the operations of a storage.ObservedStore with OpenTelemetry: one
// span per operation, named storage.<operation> ("storage.put"), with the store's name and the
// bytes moved. The span is in the context the backend runs in, so the requests of an
// s3adapter.Store whose HTTPClient has an otelhttp transport become its children. The package
// depends on the OpenTelemetry API only; the service builds the SDK provider
// (github.com/govengers/telemetry/tracing).
package oteladapter

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/govengers/storage"
)

// ScopeName is the instrumentation scope of the spans.
const ScopeName = "github.com/govengers/storage/oteladapter"

// The span attributes. Keys are never recorded: they can carry personal data (TRC-006).
const (
	AttributeStore      = "storage.store"
	AttributeOperation  = "storage.operation"
	AttributeBytes      = "storage.bytes"
	AttributeErrorClass = "storage.error_class"
)

// Options configures a Tracer.
type Options struct {
	// TracerProvider starts the spans; required. Pass the provider main builds, or
	// otel.GetTracerProvider() when main installed it globally.
	TracerProvider trace.TracerProvider
}

// Tracer is the storage.Observer that opens and ends the spans; it is safe for concurrent use.
type Tracer struct {
	tracer trace.Tracer
}

var _ storage.Observer = (*Tracer)(nil)

// spanKey finds the span of one Tracer in an operation's context, so two Tracers on one store
// end their own spans.
type spanKey struct {
	tracer *Tracer
}

// New returns a Tracer on options.TracerProvider.
func New(options *Options) (*Tracer, error) {
	if options == nil || options.TracerProvider == nil {
		return nil, errors.New("oteladapter: Options.TracerProvider is required")
	}
	return &Tracer{tracer: options.TracerProvider.Tracer(ScopeName)}, nil
}

// OperationStarted starts the operation's span as a child of the span in ctx.
func (t *Tracer) OperationStarted(ctx context.Context, operation *storage.Operation) context.Context {
	ctx, span := t.tracer.Start(ctx, "storage."+string(operation.Name), trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(
		attribute.String(AttributeStore, operation.Store),
		attribute.String(AttributeOperation, string(operation.Name)),
	))
	return context.WithValue(ctx, spanKey{tracer: t}, span)
}

// OperationFinished records the bytes and the outcome and ends the span. A failed operation
// gets the error status with its storage.ErrorClass as description and the attribute
// storage.error_class; the error's message stays out of the span because it names the key
// (record it on the caller's wide event). A missing object is an answer, not a failure:
// storage.ErrNotFound sets the class but leaves the status unset.
func (t *Tracer) OperationFinished(ctx context.Context, _ *storage.Operation, outcome *storage.Outcome) {
	span, ok := ctx.Value(spanKey{tracer: t}).(trace.Span)
	if !ok {
		return
	}
	defer span.End()
	if outcome.Bytes > 0 {
		span.SetAttributes(attribute.Int64(AttributeBytes, outcome.Bytes))
	}
	if outcome.Err == nil {
		return
	}
	class := storage.ClassifyError(outcome.Err)
	span.SetAttributes(attribute.String(AttributeErrorClass, string(class)))
	if class != storage.ErrorClassNotFound {
		span.SetStatus(codes.Error, string(class))
	}
}
