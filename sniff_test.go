package storage_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestDetectContentType(t *testing.T) {
	largePNG := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{0}, 2000)...)
	testCases := []struct {
		name      string
		content   []byte
		allowed   []string
		want      string
		wantError error
	}{
		{name: "an allowed type", content: pngHeader, allowed: []string{"image/png"}, want: "image/png"},
		{name: "a wildcard", content: pngHeader, allowed: []string{"application/pdf", "image/*"}, want: "image/png"},
		{name: "case and spaces", content: pngHeader, allowed: []string{" Image/PNG "}, want: "image/png"},
		{name: "parameters are ignored", content: []byte("plain text"), allowed: []string{"text/plain"}, want: "text/plain; charset=utf-8"},
		{name: "no allowlist", content: []byte("%PDF-1.7"), want: "application/pdf"},
		{name: "longer than the sniffed part", content: largePNG, allowed: []string{"image/png"}, want: "image/png"},
		{name: "the claim does not matter", content: []byte("<html><script>"), allowed: []string{"image/*"}, wantError: storage.ErrUnsupportedContentType},
		{name: "svg is no image to the sniffer", content: []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), allowed: []string{"image/*"}, wantError: storage.ErrUnsupportedContentType},
		{name: "an empty body", content: nil, allowed: []string{"image/png"}, wantError: storage.ErrUnsupportedContentType},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			contentType, body, err := storage.DetectContentType(iotest.HalfReader(bytes.NewReader(testCase.content)), testCase.allowed)
			require.ErrorIs(t, err, testCase.wantError)
			if testCase.wantError != nil {
				return
			}
			require.Equal(t, testCase.want, contentType)
			replayed, err := io.ReadAll(body)
			require.NoError(t, err)
			require.Equal(t, testCase.content, replayed, "the reader yields the whole body")
		})
	}
}

func TestDetectContentTypeReadError(t *testing.T) {
	errFailed := errors.New("failed")
	_, _, err := storage.DetectContentType(io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errFailed)), nil)
	require.ErrorIs(t, err, errFailed)
}

func TestDetectContentTypeTruncatedBody(t *testing.T) {
	truncated := io.MultiReader(bytes.NewReader(pngHeader), iotest.ErrReader(io.ErrUnexpectedEOF))
	_, _, err := storage.DetectContentType(truncated, nil)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "a body cut short is no short body")
}
