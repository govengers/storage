package storage

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Store keeps objects under keys. Implementations are safe for concurrent use. Every method
// validates its key with ValidateKey and returns an error wrapping ErrInvalid for a key it
// rejects, before it touches the backend.
type Store interface {
	// Put stores body under key, replacing an object stored there. It reads body to the end
	// and does not close it. The object becomes visible only once body was read completely and
	// stored, and only if ctx is not done by then: a failed or cancelled body, or one larger
	// than PutOptions.MaximumSize (ErrTooLarge), leaves the previous object, or none, in place.
	// An error after the backend committed the write (a lost S3 response, a failed flush of a
	// directory) can leave the new object in place; readers see one of the two, never a mix.
	// The result's ModifiedAt is zero on stores that do not report it on write (S3); call Stat
	// for it. A nil options means the defaults.
	Put(ctx context.Context, key string, body io.Reader, options *PutOptions) (*Object, error)
	// Get opens the object stored under key, or the byte range GetOptions selects. It returns
	// ErrNotFound for a missing object and ErrInvalidRange for a range outside of it. The
	// caller must close the Reader. A nil options reads the whole object.
	Get(ctx context.Context, key string, options *GetOptions) (*Reader, error)
	// Stat returns the attributes of the object stored under key, or ErrNotFound.
	Stat(ctx context.Context, key string) (*Object, error)
	// Delete removes the object stored under key. Deleting a missing object is no error.
	Delete(ctx context.Context, key string) error
	// List returns one page of the objects whose keys start with ListOptions.Prefix, in
	// ascending byte order of their keys. The objects carry Key, Size and ModifiedAt only; S3
	// does not list content types or metadata, so neither does any other store. A nil options
	// lists the first page of every object.
	List(ctx context.Context, options *ListOptions) (*Page, error)
}

// Copier is a Store that copies objects inside the backend without streaming them through the
// caller. Use the function Copy, which falls back to Get and Put for every other Store.
type Copier interface {
	// Copy stores the content, content type and metadata of the object under source under
	// destination, replacing an object stored there, and returns the copy's attributes. It
	// returns ErrNotFound for a missing source and ErrNotSupported when it cannot copy this
	// object (a wrapper around a store without Copy, an object too large for one copy request).
	Copy(ctx context.Context, source, destination string) (*Object, error)
}

// Presigner is a Store that signs requests a client without credentials can send directly to
// the backend for a limited time: a browser downloading or uploading an object. Use the
// functions PresignGet and PresignPut; a Presigner may return ErrNotSupported.
type Presigner interface {
	// PresignGet signs a GET of the object under key. It does not check that the object exists.
	PresignGet(ctx context.Context, key string, options *PresignGetOptions) (*PresignedRequest, error)
	// PresignPut signs a PUT of an object under key. The backend cannot enforce a size limit or
	// the content on a presigned PUT: check the stored object (Stat, DetectContentType on a
	// Get) before the application uses it.
	PresignPut(ctx context.Context, key string, options *PresignPutOptions) (*PresignedRequest, error)
}

// Object holds the attributes of a stored object.
type Object struct {
	Key         string
	Size        int64             // bytes of the whole object
	ContentType string            // empty in List results
	Metadata    map[string]string // lowercase keys; nil in List results and for an object without metadata
	ModifiedAt  time.Time         // the time the backend recorded for the last write
}

// PutOptions configures a Put.
type PutOptions struct {
	// ContentType is stored with the object; empty means DefaultContentType. It must parse as a
	// media type (mime.ParseMediaType). Detect it from the content with DetectContentType when
	// the client's claim cannot be trusted.
	ContentType string
	// Metadata is stored with the object, see ValidateMetadata for the rules.
	Metadata map[string]string
	// MaximumSize caps the body in bytes: a larger body fails with ErrTooLarge and stores
	// nothing. Zero means no limit; a negative value is invalid.
	MaximumSize int64
}

// GetOptions selects the byte range a Get reads.
type GetOptions struct {
	// Offset is the first byte to read. It must be smaller than the object's size; an Offset of
	// zero is valid for every object, an empty one included.
	Offset int64
	// Length is the number of bytes to read from Offset; zero reads to the end of the object.
	// A range that reaches beyond the end is cut at the end.
	Length int64
}

// ListOptions selects a page of a List.
type ListOptions struct {
	// Prefix selects the keys that start with it; empty lists every key. It follows the rules of
	// ValidatePrefix.
	Prefix string
	// StartAfter skips every key up to and including it: pass Page.Next to read the next page.
	StartAfter string
	// Limit is the most objects one page holds; zero or a value above MaximumListLimit means
	// MaximumListLimit.
	Limit int
}

// Page is one page of a List.
type Page struct {
	Objects []Object
	// Next is the ListOptions.StartAfter of the next page; empty when this page is the last.
	Next string
}

// PresignGetOptions configures PresignGet.
type PresignGetOptions struct {
	// Expiry is how long the URL stays valid: zero means DefaultPresignExpiry, at most
	// MaximumPresignExpiry.
	Expiry time.Duration
	// ContentDisposition overrides the Content-Disposition header of the response, for example
	// `attachment; filename="report.pdf"`, so a browser downloads untrusted content instead of
	// rendering it. Empty keeps the backend's default.
	ContentDisposition string
	// ContentType overrides the Content-Type header of the response; empty sends the stored one.
	ContentType string
}

// PresignPutOptions configures PresignPut.
type PresignPutOptions struct {
	// Expiry is how long the URL stays valid: zero means DefaultPresignExpiry, at most
	// MaximumPresignExpiry.
	Expiry time.Duration
	// ContentType is signed into the request: the client must send exactly this Content-Type.
	// Empty means DefaultContentType.
	ContentType string
}

// PresignedRequest is a signed request a client sends as it is: the method, the URL and every
// header in Header.
type PresignedRequest struct {
	Method    string
	URL       string
	Header    http.Header
	ExpiresAt time.Time
}

// Defaults and limits of the options.
const (
	DefaultContentType   = "application/octet-stream"
	MaximumListLimit     = 1000 // the most keys S3 returns in one page
	DefaultPresignExpiry = 15 * time.Minute
	MaximumPresignExpiry = 7 * 24 * time.Hour // the longest validity SigV4 allows
)
