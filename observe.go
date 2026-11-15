package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// OperationName names a Store operation an ObservedStore reports.
type OperationName string

// The operations an Observer receives.
const (
	OperationPut     OperationName = "put"
	OperationGet     OperationName = "get"
	OperationStat    OperationName = "stat"
	OperationDelete  OperationName = "delete"
	OperationList    OperationName = "list"
	OperationCopy    OperationName = "copy"
	OperationPresign OperationName = "presign"
)

// Operation is one call of an ObservedStore. It names no key: keys can carry personal data
// (file names), which must not reach metrics or traces.
type Operation struct {
	Store string        // ObservedStoreOptions.Name: "attachments", "avatars"
	Name  OperationName // the method called
}

// Outcome is how an operation ended.
type Outcome struct {
	// Bytes is the content moved: stored by a put or a copy, read by a get; zero otherwise.
	Bytes int64
	// Err is nil on success. A get reports the error of Get, or the first read error other
	// than io.EOF, or the error of Close.
	Err error
	// Duration runs from the call to its return; for a get, to the Close of its Reader, so it
	// includes the time the caller took to read.
	Duration time.Duration
}

// ErrorClass is the kind of an operation's outcome: a closed set for metric labels and span
// statuses, which must not carry error messages (they name keys).
type ErrorClass string

// The classes ClassifyError returns.
const (
	ErrorClassSuccess     ErrorClass = "success"
	ErrorClassNotFound    ErrorClass = "not_found"
	ErrorClassInvalid     ErrorClass = "invalid" // ErrInvalid, ErrInvalidRange, ErrUnsupportedContentType
	ErrorClassTooLarge    ErrorClass = "too_large"
	ErrorClassUnsupported ErrorClass = "unsupported" // ErrNotSupported
	ErrorClassCancelled   ErrorClass = "cancelled"   // context.Canceled, context.DeadlineExceeded
	ErrorClassError       ErrorClass = "error"       // everything else: the backend failed
)

// ClassifyError returns the class of an operation's error; nil is ErrorClassSuccess.
func ClassifyError(err error) ErrorClass {
	switch {
	case err == nil:
		return ErrorClassSuccess
	case errors.Is(err, ErrNotFound):
		return ErrorClassNotFound
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrInvalidRange), errors.Is(err, ErrUnsupportedContentType):
		return ErrorClassInvalid
	case errors.Is(err, ErrTooLarge):
		return ErrorClassTooLarge
	case errors.Is(err, ErrNotSupported):
		return ErrorClassUnsupported
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ErrorClassCancelled
	default:
		return ErrorClassError
	}
}

// Observer is told about every operation of an ObservedStore, for metrics, traces or logs;
// prometheusadapter and oteladapter implement it. OperationStarted returns the context the
// operation runs in, so a tracing observer can put its span there for the backend's own
// instrumentation (an otelhttp transport); observers that keep nothing return ctx.
// OperationFinished gets that context back. Both run on the caller's path and must be fast and
// safe for concurrent use. The interface may gain methods in minor versions.
type Observer interface {
	OperationStarted(ctx context.Context, operation *Operation) context.Context
	OperationFinished(ctx context.Context, operation *Operation, outcome *Outcome)
}

// ObservedStoreOptions configures NewObservedStore.
type ObservedStoreOptions struct {
	Store     Store      // required: the store whose operations are observed
	Name      string     // required: Operation.Store, the store's name in metrics and traces
	Observers []Observer // required, at least one; started in order, finished in reverse order
}

// ObservedStore is a Store that reports every operation of the store it wraps to its
// Observers. It is safe for concurrent use. It is a Copier and a Presigner for every store: it
// forwards to the wrapped store's capability and returns ErrNotSupported when that store lacks
// it, which Copy turns into its streaming fallback.
type ObservedStore struct {
	store     Store
	name      string
	observers []Observer
}

var (
	_ Store     = (*ObservedStore)(nil)
	_ Copier    = (*ObservedStore)(nil)
	_ Presigner = (*ObservedStore)(nil)
)

// NewObservedStore wraps options.Store.
func NewObservedStore(options *ObservedStoreOptions) (*ObservedStore, error) {
	if options == nil || options.Store == nil || options.Name == "" || len(options.Observers) == 0 {
		return nil, errors.New("storage: ObservedStoreOptions needs a Store, a Name and at least one Observer")
	}
	for _, observer := range options.Observers {
		if observer == nil {
			return nil, errors.New("storage: ObservedStoreOptions.Observers holds a nil Observer")
		}
	}
	return &ObservedStore{store: options.Store, name: options.Name, observers: append([]Observer(nil), options.Observers...)}, nil
}

// Put stores body through the wrapped store and reports the bytes stored.
func (s *ObservedStore) Put(ctx context.Context, key string, body io.Reader, options *PutOptions) (*Object, error) {
	call := s.start(ctx, OperationPut)
	object, err := s.store.Put(call.ctx, key, body, options)
	call.finish(objectSize(object), err)
	return object, err
}

// Get opens the object through the wrapped store; the operation is reported when the Reader
// is closed, with the bytes read.
func (s *ObservedStore) Get(ctx context.Context, key string, options *GetOptions) (*Reader, error) {
	call := s.start(ctx, OperationGet)
	reader, err := s.store.Get(call.ctx, key, options)
	if err != nil {
		call.finish(0, err)
		return nil, err
	}
	return NewReader(&reader.Object, reader.Offset, reader.Length, &observedBody{body: reader, call: call}), nil
}

// Stat reads the attributes through the wrapped store.
func (s *ObservedStore) Stat(ctx context.Context, key string) (*Object, error) {
	call := s.start(ctx, OperationStat)
	object, err := s.store.Stat(call.ctx, key)
	call.finish(0, err)
	return object, err
}

// Delete removes the object through the wrapped store.
func (s *ObservedStore) Delete(ctx context.Context, key string) error {
	call := s.start(ctx, OperationDelete)
	err := s.store.Delete(call.ctx, key)
	call.finish(0, err)
	return err
}

// List reads a page through the wrapped store.
func (s *ObservedStore) List(ctx context.Context, options *ListOptions) (*Page, error) {
	call := s.start(ctx, OperationList)
	page, err := s.store.List(call.ctx, options)
	call.finish(0, err)
	return page, err
}

// Copy copies inside the wrapped store if it is a Copier and returns ErrNotSupported
// otherwise; use the function Copy, which then streams the object through this store.
func (s *ObservedStore) Copy(ctx context.Context, source, destination string) (*Object, error) {
	copier, ok := s.store.(Copier)
	if !ok {
		return nil, fmt.Errorf("copy %q to %q: %w", source, destination, ErrNotSupported)
	}
	call := s.start(ctx, OperationCopy)
	object, err := copier.Copy(call.ctx, source, destination)
	call.finish(objectSize(object), err)
	return object, err
}

// PresignGet signs through the wrapped store if it is a Presigner and returns ErrNotSupported
// otherwise.
func (s *ObservedStore) PresignGet(ctx context.Context, key string, options *PresignGetOptions) (*PresignedRequest, error) {
	presigner, ok := s.store.(Presigner)
	if !ok {
		return nil, fmt.Errorf("presign a GET of %q: %w", key, ErrNotSupported)
	}
	call := s.start(ctx, OperationPresign)
	request, err := presigner.PresignGet(call.ctx, key, options)
	call.finish(0, err)
	return request, err
}

// PresignPut signs through the wrapped store if it is a Presigner and returns ErrNotSupported
// otherwise.
func (s *ObservedStore) PresignPut(ctx context.Context, key string, options *PresignPutOptions) (*PresignedRequest, error) {
	presigner, ok := s.store.(Presigner)
	if !ok {
		return nil, fmt.Errorf("presign a PUT of %q: %w", key, ErrNotSupported)
	}
	call := s.start(ctx, OperationPresign)
	request, err := presigner.PresignPut(call.ctx, key, options)
	call.finish(0, err)
	return request, err
}

// observedCall is one operation between its start and its finish.
type observedCall struct {
	store     *ObservedStore
	operation *Operation
	ctx       context.Context //nolint:containedctx // the operation's context, handed back to OperationFinished
	start     time.Time
}

func (s *ObservedStore) start(ctx context.Context, name OperationName) *observedCall {
	operation := &Operation{Store: s.name, Name: name}
	start := time.Now()
	for _, observer := range s.observers {
		ctx = observer.OperationStarted(ctx, operation) //nolint:fatcontext // each observer wraps the context of the one before, once per call
	}
	return &observedCall{store: s, operation: operation, ctx: ctx, start: start}
}

func (c *observedCall) finish(bytes int64, err error) {
	outcome := &Outcome{Bytes: bytes, Err: err, Duration: time.Since(c.start)}
	for index := len(c.store.observers) - 1; index >= 0; index-- {
		c.store.observers[index].OperationFinished(c.ctx, c.operation, outcome)
	}
}

func objectSize(object *Object) int64 {
	if object == nil {
		return 0
	}
	return object.Size
}

// observedBody counts what the caller reads and reports the get once, on the first Close.
type observedBody struct {
	body      io.ReadCloser
	call      *observedCall
	bytes     int64
	readError error
	once      sync.Once
}

func (b *observedBody) Read(buffer []byte) (int, error) {
	count, err := b.body.Read(buffer)
	b.bytes += int64(count)
	if err != nil && !errors.Is(err, io.EOF) && b.readError == nil {
		b.readError = err
	}
	return count, err
}

func (b *observedBody) Close() error {
	err := b.body.Close()
	b.once.Do(func() {
		b.call.finish(b.bytes, errors.Join(b.readError, err))
	})
	return err
}
