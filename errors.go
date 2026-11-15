package storage

import "errors"

// The conditions a caller branches on. Every Store wraps them with the key and the operation,
// so match them with errors.Is.
var (
	// ErrNotFound: no object is stored under the key (HTTP 404).
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalid: a key, prefix, metadata, content type or option breaks the rules (HTTP 400).
	ErrInvalid = errors.New("storage: invalid argument")
	// ErrInvalidRange: the requested range starts beyond the object (HTTP 416).
	ErrInvalidRange = errors.New("storage: invalid range")
	// ErrTooLarge: the body is larger than PutOptions.MaximumSize or a LimitReader's limit (HTTP 413).
	ErrTooLarge = errors.New("storage: object too large")
	// ErrUnsupportedContentType: DetectContentType found a type outside the allowlist (HTTP 415).
	ErrUnsupportedContentType = errors.New("storage: unsupported content type")
	// ErrNotSupported: the store does not offer the optional capability.
	ErrNotSupported = errors.New("storage: not supported")
)
