# Gotchas

- **Close every Reader.** An open Reader holds an HTTP connection or a file, and an ObservedStore reports the get only on Close.
- **Pass the reader DetectContentType returns.** It replays the sniffed bytes; the original body has lost them.
- **The client's Content-Type is a claim.** Detect the type from the bytes for anything served back.
- **SVG is not an image to the sniffer.** It comes out as `text/xml` or `text/plain`. Accept it only deliberately, never serve it inline.
- **A presigned PUT checks nothing.** Verify size and type of the stored object before using it.
- **Presigned URLs carry the configured endpoint.** A cluster-internal endpoint gives URLs the browser cannot reach.
- **Use storage.Copy and storage.PresignGet, not type assertions.** An ObservedStore implements Copier and Presigner for every store and reports `ErrNotSupported` where the backend lacks them.
- **Delete is idempotent.** Deleting a missing key is no error; check with Stat if the difference matters.
- **List carries no content type or metadata.** S3 does not list them; Stat the objects you need.
- **No compare-and-swap.** Concurrent puts to one key: the last one wins. Use a new key per version (an ID) and switch the database row.
- **Set an abort rule for incomplete multipart uploads on the bucket.** A killed process cannot abort its upload.
- **Keys are case-sensitive, macOS and Windows filesystems are not.** Lowercase generated keys, or use IDs.
- **Never use a file name as a key segment.** It carries personal data into logs of the backend and into your bucket listing; use an ID.
