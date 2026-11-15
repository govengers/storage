# Observability

The library never logs. `storage.NewObservedStore` wraps a store and reports every operation to its observers:

```go
metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
tracer, err := oteladapter.New(&oteladapter.Options{TracerProvider: traces.TracerProvider()})
attachments, err := storage.NewObservedStore(&storage.ObservedStoreOptions{
    Store: backend, Name: "attachments", Observers: []storage.Observer{tracer, metrics},
})
```

Observers start in order and finish in reverse. A get is reported when its Reader is closed, with the bytes read; an unclosed Reader is never reported (and leaks a connection or a file). The `Operation` names the store and the operation, never the key: keys can hold personal data.

## Metrics

One `Metrics` serves every observed store of a process; `Name` tells them apart.

```
<namespace>_storage_operations_total{store,operation,outcome}
<namespace>_storage_operation_duration_seconds{store,operation}
<namespace>_storage_bytes_total{store,operation}      bytes stored by put and copy, read by get
```

`operation` is `put`, `get`, `stat`, `delete`, `list`, `copy` or `presign`. `outcome` is `storage.ClassifyError` of the error: `success`, `not_found`, `invalid`, `too_large`, `unsupported`, `cancelled`, `error`. Alert on `outcome="error"`; the other failures are the caller's input. The duration of a get includes the time the client took to read.

## Spans

`oteladapter` opens one internal span `storage.<operation>` per operation with `storage.store`, `storage.operation`, `storage.bytes` and, on failure, `storage.error_class`. A failure sets the error status with the class as description; the error message stays out of the span, because it names the key. `not_found` leaves the status unset: a missing object is an answer. With an otelhttp transport on the `s3adapter` HTTP client, the S3 requests become children of the operation's span.

## Wide events

There is no wide-event adapter: the handler that called the store records the failure on its own wide event, where the key and the error belong.
