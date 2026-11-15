package storage

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sniffLength is the most bytes http.DetectContentType looks at.
const sniffLength = 512

// DetectContentType determines the content type of body from its first bytes (magic numbers,
// the WHATWG MIME sniffing algorithm of http.DetectContentType) and checks it against allowed.
// It returns the detected type, for example "image/png" or "text/plain; charset=utf-8", and a
// reader that yields the whole body, the bytes read for detection included; pass that reader
// to Put instead of body.
//
// An allowed entry is a media type ("application/pdf") or a type with a wildcard subtype
// ("image/*"), compared without parameters and case-insensitively. A detected type outside the
// list fails with ErrUnsupportedContentType; an empty list accepts every type. The sniffer
// knows binary formats by their magic numbers but never reports image/svg+xml or a type it
// cannot recognise: SVG comes out as text/xml or text/plain, unknown binary content as
// application/octet-stream. Never trust the client's Content-Type for an upload served back
// inline.
func DetectContentType(body io.Reader, allowed []string) (string, io.Reader, error) {
	// Not io.ReadFull: it reports a short body as io.ErrUnexpectedEOF, the error a truncated
	// HTTP body returns too.
	head, err := io.ReadAll(io.LimitReader(body, sniffLength))
	if err != nil {
		return "", nil, fmt.Errorf("read the start of the content: %w", err)
	}
	contentType := http.DetectContentType(head)
	if !contentTypeAllowed(contentType, allowed) {
		return "", nil, fmt.Errorf("detected %q, allowed %v: %w", contentType, allowed, ErrUnsupportedContentType)
	}
	return contentType, io.MultiReader(bytes.NewReader(head), body), nil
}

func contentTypeAllowed(contentType string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	mainType, _, _ := strings.Cut(mediaType, "/")
	for _, entry := range allowed {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == mediaType || entry == mainType+"/*" {
			return true
		}
	}
	return false
}
