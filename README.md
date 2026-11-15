# storage

[![CI](https://github.com/govengers/storage/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/govengers/storage/actions/workflows/ci.yml)
[![unit tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/storage/badges/unit-tests.json)](https://github.com/govengers/storage/actions/workflows/ci.yml)
[![e2e tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/storage/badges/e2e-tests.json)](test/e2e)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/storage/badges/coverage.json)](https://github.com/govengers/storage/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/govengers/storage.svg)](https://pkg.go.dev/github.com/govengers/storage)
[![License](https://img.shields.io/github/license/govengers/storage)](LICENSE)

Blob storage for govengers services behind one interface: S3-compatible object stores, the local filesystem and memory. Module `github.com/govengers/storage`, Go 1.26, Apache 2.0.

- **One interface, every backend.** `storage.Store` puts, gets (whole or a byte range), stats, deletes and lists objects; `s3adapter` (AWS S3, MinIO, RustFS, Ceph, Garage), `filesystem` and `memory` implement it and pass one conformance suite.
- **Atomic writes.** A failed, cancelled or oversized Put leaves the previous object in place, on S3 (multipart uploads are aborted) and on disk (temporary file, flush, rename).
- **Safe keys.** One key rule for every backend: no traversal, no control characters, bounded length. The filesystem store also confines every access with `os.Root`.
- **Upload helpers.** A size cap that fails instead of truncating, a SHA-256 computed while streaming, and the content type from the first bytes against an allowlist.
- **Copy and presigned URLs** where the backend offers them, with a streaming fallback for copies.
- **Observable.** `ObservedStore` reports every operation to Prometheus and OpenTelemetry adapters, without keys in labels or spans. The library never logs.

```sh
go get github.com/govengers/storage
```

## Usage

```go
// main: pick the backend from the configuration.
backend, err := s3adapter.New(&s3adapter.Options{
    Endpoint:        configuration.StorageEndpoint, // "http://rustfs.storage.svc:9000"
    Bucket:          configuration.StorageBucket,
    AccessKeyID:     configuration.StorageAccessKeyID,
    SecretAccessKey: configuration.StorageSecretAccessKey,
    PathStyle:       true,
})
// or: backend, err := filesystem.New(&filesystem.Options{Root: "/var/lib/todo/files"})
metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
attachments, err := storage.NewObservedStore(&storage.ObservedStoreOptions{
    Store: backend, Name: "attachments", Observers: []storage.Observer{metrics},
})

// upload handler: the content decides the type, the size is capped, the digest is computed on the way.
contentType, body, err := storage.DetectContentType(request.Body, []string{"image/png", "image/jpeg", "application/pdf"})
if err != nil {
    return err // errors.Is(err, storage.ErrUnsupportedContentType): 415
}
digest := storage.NewSHA256Reader(body)
object, err := attachments.Put(ctx, "tasks/"+taskID.String()+"/"+attachmentID.String(), digest, &storage.PutOptions{
    ContentType: contentType,
    MaximumSize: 25 << 20, // errors.Is(err, storage.ErrTooLarge): 413
})
// store object.Size, contentType and digest.Sum() on the attachment row

// download: a range, or a presigned URL straight from the bucket.
reader, err := attachments.Get(ctx, key, &storage.GetOptions{Offset: 1024})
defer reader.Close()
link, err := storage.PresignGet(ctx, attachments, key, &storage.PresignGetOptions{
    ContentDisposition: `attachment; filename="report.pdf"`,
}) // errors.Is(err, storage.ErrNotSupported) on the filesystem: stream it instead
```

| Error | Meaning | HTTP |
|-------|---------|------|
| `ErrNotFound` | no object under the key | 404 |
| `ErrInvalid` | key, prefix, metadata, content type or option breaks the rules | 400 |
| `ErrInvalidRange` | the range starts beyond the object | 416 |
| `ErrTooLarge` | the body exceeds `MaximumSize` | 413 |
| `ErrUnsupportedContentType` | `DetectContentType` found a type outside the allowlist | 415 |
| `ErrNotSupported` | the store lacks the optional capability | |

## Packages

| Import `github.com/govengers/storage/...` | Holds | Depends on |
|---|---|---|
| (root) `storage` | `Store`, `Copier`, `Presigner`, `Copy`, `PresignGet`/`PresignPut`, key and metadata rules, `LimitReader`, `NewSHA256Reader`, `DetectContentType`, `ObservedStore`, `Observer` | standard library |
| `s3adapter` | S3-compatible store: multipart upload, server-side copy, presigned URLs, path-style option | AWS SDK for Go v2 (`service/s3`) |
| `filesystem` | a directory on a local or mounted disk, atomic writes, confined root | standard library |
| `memory` | in memory, for tests and development | standard library |
| `prometheusadapter` | operation, duration and byte metrics on the caller's registry | client_golang |
| `oteladapter` | one span per operation, no keys in spans | OpenTelemetry API |
| `storagetest` | `Run`: the conformance suite of `storage.Store` | testify |

## Guarantees and limits

- A Put is atomic; concurrent puts to one key leave one whole write. There is no compare-and-swap: the last writer wins.
- `List` returns keys in byte order, at most 1000 per page, with size and modification time only.
- Keys are case-sensitive; on a case-insensitive filesystem two keys that differ in case are one file. The filesystem store cannot hold `a` and `a/b` at once.
- A presigned PUT cannot cap the size or check the content: check the stored object before the application uses it.
- `s3adapter` holds one part (8 MiB by default) in memory per running Put and stores up to 10000 parts.

## Documentation

- [docs/wiki](docs/wiki/Home.md): the model, uploads and downloads, the S3 and filesystem backends, observability, testing, gotchas.
- [docs/design.md](docs/design.md): the design with every decision (S1 onwards).
- [pkg.go.dev](https://pkg.go.dev/github.com/govengers/storage): the API.

The repository follows [govengers/standards](https://github.com/govengers/standards), profile `library`. `just check` runs lint, unit tests, the end-to-end suite and govulncheck.

## License

Apache 2.0, see [LICENSE](LICENSE).
