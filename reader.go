package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// Reader streams the content a Get selected. The caller must close it. It is not safe for
// concurrent use.
type Reader struct {
	Object Object // the attributes of the whole object; Size is the whole object's size
	Offset int64  // the first byte this Reader yields
	Length int64  // the number of bytes this Reader yields
	body   io.ReadCloser
}

// NewReader returns the Reader of a Get that yields length bytes of object from offset out of
// body, for Store implementations. Close closes body.
func NewReader(object *Object, offset, length int64, body io.ReadCloser) *Reader {
	return &Reader{Object: *object, Offset: offset, Length: length, body: body}
}

// Read reads the content.
func (r *Reader) Read(buffer []byte) (int, error) {
	return r.body.Read(buffer)
}

// Close releases the content.
func (r *Reader) Close() error {
	return r.body.Close()
}

// LimitReader returns a reader that yields at most limit bytes of reader and then fails with
// ErrTooLarge if reader holds more. Unlike io.LimitReader it never truncates silently: once it
// failed, every later Read fails with the same error. A limit of zero or less yields nothing
// and fails on the first byte.
func LimitReader(reader io.Reader, limit int64) io.Reader {
	return &limitedReader{reader: reader, remaining: limit}
}

type limitedReader struct {
	reader    io.Reader
	remaining int64
	err       error // ErrTooLarge once the probe found a byte past the limit
}

func (r *limitedReader) Read(buffer []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.remaining <= 0 {
		return r.probe()
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

// probe reads one byte past the limit to tell the end of the stream from a stream that is too
// large.
func (r *limitedReader) probe() (int, error) {
	var single [1]byte
	for {
		count, err := r.reader.Read(single[:])
		if count > 0 {
			r.err = ErrTooLarge
			return 0, r.err
		}
		if err != nil {
			return 0, err
		}
	}
}

// SHA256Reader passes a stream through and computes the SHA-256 digest of what was read, for
// content-addressed keys or an integrity check without a second pass over the content.
type SHA256Reader struct {
	reader io.Reader
	hash   hash.Hash
}

// NewSHA256Reader returns a SHA256Reader reading from reader.
func NewSHA256Reader(reader io.Reader) *SHA256Reader {
	digest := sha256.New()
	return &SHA256Reader{reader: io.TeeReader(reader, digest), hash: digest}
}

// Read reads from the underlying reader and hashes the bytes read.
func (r *SHA256Reader) Read(buffer []byte) (int, error) {
	return r.reader.Read(buffer)
}

// Sum returns the hex-encoded SHA-256 digest of every byte read so far.
func (r *SHA256Reader) Sum() string {
	return hex.EncodeToString(r.hash.Sum(nil))
}

// ContextReader returns a reader that fails with the context's error once ctx is done, for
// Store implementations whose writes do not watch a context themselves (a file copy).
func ContextReader(ctx context.Context, reader io.Reader) io.Reader {
	return &contextReader{ctx: ctx, reader: reader}
}

type contextReader struct {
	ctx    context.Context //nolint:containedctx // lives only as long as the one call that reads it
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
