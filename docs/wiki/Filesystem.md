# Filesystem

`filesystem.Store` keeps objects in a directory: a local disk in development, a volume in a single-replica deployment.

```go
store, err := filesystem.New(&filesystem.Options{Root: "/var/lib/todo/files"})
defer store.Close()
```

`Root` must exist; `New` creates `objects` and `tmp` inside it.

## Layout and format

Every object is one file below `<Root>/objects`. Every segment of the key but the last is a directory named `<segment>.d`, the last a file named `<segment>.o`: `tasks/42` is `objects/tasks.d/42.o`, `tasks/42/report.pdf` is `objects/tasks.d/42.d/report.pdf.o`. So a key can be an object and a prefix of other keys at once, like on S3. Entries without these suffixes (a `.DS_Store`, a file a person put there) are ignored. The file holds the magic `gvstore1`, the header length as a 4-byte big-endian integer, a JSON header with the content type and metadata, then the content. The files are not plain copies of the uploads; read them through the Store. Back up `<Root>/objects`; `<Root>/tmp` only holds writes in progress.

## Atomicity

A Put writes `<Root>/tmp/<random>`, flushes it to disk, renames it to the key's path and flushes the directory. Readers see the old or the new object, never a partial one, and content and metadata switch together. An open Reader keeps reading the object it opened. Delete removes the file and the directories it leaves empty; a Put that needs such a directory at the same moment creates it again and retries. If flushing the directory fails after the rename, Put returns the error with the new object already in place.

## Confinement

Keys are validated (no `..`, no leading slash), and every access goes through an `os.Root`, which also refuses symlinks that point out of `Root`. `List` leaves symlinks out.

## Limits

- On a case-insensitive filesystem (macOS by default, Windows) keys that differ only in case are one object.
- `List` reads the header of every object it returns; keys are listed in byte order like S3. A `.o` file without a valid header fails the List and names its key.
- No presigning: `storage.PresignGet` returns `ErrNotSupported`; stream through the service.
- A process killed during a Put leaves its file in `<Root>/tmp`; remove files there older than a day.
- Several processes may share `Root` on one host or a POSIX volume. Network filesystems without atomic rename are not supported.
