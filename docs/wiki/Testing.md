# Testing

## Code that uses a store

Take `storage.Store` in the options and give tests `memory.New()`: no disk, no network, every test its own store. Use `filesystem.New` on `t.TempDir()` when the code depends on the filesystem's behaviour.

## A new backend

Run the conformance suite against it:

```go
func TestConformance(t *testing.T) {
    storagetest.Run(t, func(t *testing.T) storage.Store {
        return newBackendForTest(t) // a fresh, empty store; register cleanups on t
    })
}
```

It checks the Store contract, and Copier and Presigner when the store implements them. Presigned requests are sent over HTTP, so their URLs must be reachable from the test.

## The S3 fake

`internal/s3fake` is an in-process S3 on a loopback `httptest` server: put, get with range, head, delete, ListObjectsV2, copy and multipart upload on buckets in memory. `FailNext` injects 403 errors (which the SDK does not retry, so no test waits for a backoff), `PendingUploads` proves that failed uploads were aborted. It does not check signatures or expiry, and refuses aws-chunked bodies, which s3adapter must never send. It is internal to this module.

## The suites

- `just test`: unit tests with the race detector and the coverage gate, cached per package (TST-014); about 7 s cold, under 1 s warm. The coverage gate leaves out the test harnesses (`internal/s3fake`, `storagetest`), which run from the backends' tests.
- `just test-e2e`: `test/e2e` runs the conformance suite on the observed stacks (S3 over HTTP, filesystem) with Prometheus and OpenTelemetry attached, and an upload and download path through `net/http`; skipped when the compiled suite is unchanged (TST-015).
- Nothing starts a database or a container; no test sleeps.
