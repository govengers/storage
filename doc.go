// Package storage is blob storage for govengers services behind one interface.
//
// A Store keeps objects: a byte stream under a key, with a content type and string metadata.
// The backends live in subpackages: s3adapter (S3-compatible object stores: AWS S3, MinIO,
// RustFS, Ceph), filesystem (a directory on a local or mounted disk) and memory (tests and
// development). Optional capabilities are separate interfaces: Copier (server-side copy) and
// Presigner (URLs a browser uses without credentials); use the package functions Copy,
// PresignGet and PresignPut, which fall back or report ErrNotSupported.
//
// Every Store validates keys and metadata the same way (ValidateKey, ValidateMetadata), so a
// key that works on one backend works on every other. The helpers LimitReader,
// NewSHA256Reader and DetectContentType handle untrusted uploads: a size cap that fails
// instead of truncating, a digest computed while streaming, and the content type from the
// first bytes checked against an allowlist.
//
// ObservedStore reports every operation to Observers; prometheusadapter turns them into
// metrics and oteladapter into spans. storagetest is the conformance suite every backend
// passes.
package storage
