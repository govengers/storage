# govengers/storage

Blob storage for govengers services behind one interface. Module `github.com/govengers/storage`, Go 1.26, Apache 2.0.

## Model

An **object** is a byte stream under a **key**, with a content type and string metadata. A **store** (`storage.Store`) puts, gets (whole or a byte range), stats, deletes and lists objects. **Capabilities** a backend may add are separate interfaces: `storage.Copier` (copy inside the backend) and `storage.Presigner` (signed URLs a browser uses without credentials). Call them through `storage.Copy`, `storage.PresignGet` and `storage.PresignPut`, which fall back or report `ErrNotSupported`. An **ObservedStore** wraps a store and reports every operation to **Observers**: `prometheusadapter` for metrics, `oteladapter` for spans.

The application's database row owns an object: it holds the key, the size, the content type and the digest. The store only keeps the bytes. Constructors are `New(options *Options)` and validate at startup; the library never logs.

## Packages

| Import `github.com/govengers/storage/...` | Use it for | Page |
|---|---|---|
| (root) `storage` | the interface, key rules, upload helpers, `ObservedStore` | [Uploads-and-Downloads](Uploads-and-Downloads.md) |
| `s3adapter` | AWS S3 and S3-compatible servers | [S3](S3.md) |
| `filesystem` | a local disk or a mounted volume | [Filesystem](Filesystem.md) |
| `memory` | tests and development | [Testing](Testing.md) |
| `prometheusadapter`, `oteladapter` | metrics and spans | [Observability](Observability.md) |
| `storagetest` | the conformance suite for a backend | [Testing](Testing.md) |

## Which piece for which need

- Uploads from users (attachments, avatars): `DetectContentType` with an allowlist, `NewSHA256Reader`, `Put` with `MaximumSize`. [Uploads-and-Downloads](Uploads-and-Downloads.md#the-upload-path)
- Large files in the browser without passing through the service: `storage.PresignPut` and `storage.PresignGet` on `s3adapter`. [Uploads-and-Downloads](Uploads-and-Downloads.md#presigned-urls)
- Generated documents (PDF exports, reports): `Put` with the known content type, `Get` to stream them back.
- A single-replica deployment or local development without S3: `filesystem` on a volume. [Filesystem](Filesystem.md)
- Unit tests of code that stores files: `memory.New()`. [Testing](Testing.md)

## Wiring order in main

1. Read the configuration: backend kind, endpoint, bucket, credentials or root directory.
2. Build the backend: `s3adapter.New` or `filesystem.New` (defer `Close` for the filesystem).
3. Build the observers: `prometheusadapter.New` on the service's registry, `oteladapter.New` on the tracer provider.
4. `storage.NewObservedStore` per logical store (`attachments`, `exports`), each with its `Name`.
5. Hand the store to the services through their options; they take `storage.Store`, not a backend type.

## Pages

- [Uploads-and-Downloads](Uploads-and-Downloads.md): keys, the upload path, ranges, presigned URLs, serving untrusted content.
- [S3](S3.md): options, path style, multipart, copy, presigning, compatible servers.
- [Filesystem](Filesystem.md): layout, file format, atomicity, limits.
- [Observability](Observability.md): ObservedStore, metrics, spans.
- [Testing](Testing.md): memory store, conformance suite, the S3 fake, the end-to-end suite.
- [Gotchas](Gotchas.md): the rules that are easy to get wrong.
