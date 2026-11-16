# Uploads and downloads

## Keys

The application chooses the key; a client never names it. Use IDs, not file names: `tasks/<task id>/<attachment id>`. The original file name belongs on the database row and in the `Content-Disposition` of the download.

`storage.ValidateKey` is the one rule every backend applies: valid UTF-8, 1 to 1024 bytes, segments separated by single slashes, every segment 1 to 253 bytes (the filesystem store appends two to each name), not `.` or `..`, no control character, no backslash. Every Store method rejects anything else with `ErrInvalid` before it touches the backend, so a key built from user input can never leave a filesystem root.

## The upload path

```go
contentType, body, err := storage.DetectContentType(request.Body, []string{"image/png", "image/jpeg", "image/webp", "application/pdf"})
if err != nil {
    return err // ErrUnsupportedContentType: 415
}
digest := storage.NewSHA256Reader(body)
object, err := store.Put(ctx, key, digest, &storage.PutOptions{ContentType: contentType, MaximumSize: 25 << 20})
if err != nil {
    return err // ErrTooLarge: 413; nothing was stored
}
attachment := Attachment{Key: key, Size: object.Size, ContentType: contentType, SHA256: digest.Sum(), FileName: header.Filename}
```

- `DetectContentType` reads the first 512 bytes, determines the type by magic numbers (`http.DetectContentType`) and checks it against the allowlist (media types and `type/*` wildcards). It returns a reader that replays the sniffed bytes: pass that one on, not the original body.
- `MaximumSize` fails with `ErrTooLarge` and stores nothing. `storage.LimitReader` does the same for any reader. Neither truncates silently, unlike `io.LimitReader`. Also cap the request body in the HTTP layer (`http.MaxBytesReader`), so a client cannot keep a connection busy beyond the limit.
- `NewSHA256Reader` hashes while the content streams; `Sum` is valid after the Put read the body to the end.
- The client's `Content-Type` is a claim. Never store it unchecked for content you serve back.

## Ranges

`GetOptions{Offset, Length}` reads a byte range: `Length` zero reads to the end, a range past the end is cut there, an `Offset` at or beyond the size is `ErrInvalidRange` (416), except zero, which is valid on an empty object too. `Reader.Object.Size` is always the whole object's size, `Reader.Offset` and `Reader.Length` describe what the reader yields; that is what a `Content-Range` header needs.

## Presigned URLs

On `s3adapter`, a browser can upload and download without the bytes passing through the service:

```go
upload, err := storage.PresignPut(ctx, store, key, &storage.PresignPutOptions{ContentType: "image/png", Expiry: 10 * time.Minute})
// hand upload.Method, upload.URL and upload.Header to the browser; it sends exactly these headers

download, err := storage.PresignGet(ctx, store, key, &storage.PresignGetOptions{
    ContentDisposition: `attachment; filename="report.pdf"`,
})
```

- Both return `ErrNotSupported` on stores without presigning (filesystem, memory): fall back to streaming through the service.
- A presigned PUT checks nothing: not the size, not the content. After the browser reports the upload, `Stat` the object for its size and run `DetectContentType` on a `Get` of its first bytes before the application accepts it; delete it otherwise.
- Expiry defaults to 15 minutes, at most 7 days. Keep it short: a URL is a bearer credential for one object.

## Serving untrusted content

An uploaded file served inline from the application's origin can run script (HTML, SVG). Serve user content with `Content-Disposition: attachment` unless the type is on a short inline list (`image/png`, `image/jpeg`, `image/webp`, `application/pdf`), set `X-Content-Type-Options: nosniff`, and never inline `image/svg+xml`. `PresignGetOptions.ContentDisposition` sets the disposition on presigned downloads.
