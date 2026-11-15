package storage_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
)

func TestValidateKey(t *testing.T) {
	valid := []string{
		"a", "tasks/42/notes.txt", ".hidden", "a/.well-known/x", "..dots", "spaces are fine", "ümlaut/ß",
		strings.Repeat("s", storage.MaximumKeySegmentLength),
		strings.Repeat(strings.Repeat("s", 100)+"/", 10) + strings.Repeat("s", 14), // exactly 1024 bytes
	}
	for _, key := range valid {
		require.NoError(t, storage.ValidateKey(key), "key %q", key)
	}
	invalid := []string{
		"", "/a", "a/", "a//b", ".", "..", "a/./b", "a/../b", "../a", "a\\b", "a\x00b", "a\nb", "a\u0085b", "\xff",
		strings.Repeat("s", storage.MaximumKeySegmentLength+1),
		strings.Repeat(strings.Repeat("s", 100)+"/", 10) + strings.Repeat("s", 15),
	}
	for _, key := range invalid {
		require.ErrorIs(t, storage.ValidateKey(key), storage.ErrInvalid, "key %q", key)
	}
}

func TestValidatePrefix(t *testing.T) {
	for _, prefix := range []string{"", "a", "a/", "a/b", "docs/re", "a/.", "a/..", "."} {
		require.NoError(t, storage.ValidatePrefix(prefix), "prefix %q", prefix)
	}
	for _, prefix := range []string{"/", "/a", "a//", "../", "a/../b", "./a", "a\x01", "\xff", strings.Repeat("s", storage.MaximumKeyLength+1)} {
		require.ErrorIs(t, storage.ValidatePrefix(prefix), storage.ErrInvalid, "prefix %q", prefix)
	}
}

func TestValidateMetadata(t *testing.T) {
	require.NoError(t, storage.ValidateMetadata(nil))
	require.NoError(t, storage.ValidateMetadata(map[string]string{"owner": "user-42", "x-1": "a b", "9lives": "~!"}))
	for _, metadata := range []map[string]string{
		{"": "x"},
		{"-leading": "x"},
		{"Upper": "x"},
		{"under_score": "x"},
		{"dot.ted": "x"},
		{"key": ""},
		{"key": " padded"},
		{"key": "tab\t"},
		{"key": "ü"},
		{"key": "line\nbreak"},
		{"key": strings.Repeat("v", storage.MaximumMetadataSize)},
	} {
		require.ErrorIs(t, storage.ValidateMetadata(metadata), storage.ErrInvalid, "metadata %v", metadata)
	}
}

func TestValidateContentType(t *testing.T) {
	for _, contentType := range []string{"image/png", "text/plain; charset=utf-8", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"} {
		require.NoError(t, storage.ValidateContentType(contentType), contentType)
	}
	for _, contentType := range []string{"", "png", "text/plain\r\nx: y", "text/ü", "image/" + strings.Repeat("x", storage.MaximumContentTypeLength)} {
		require.ErrorIs(t, storage.ValidateContentType(contentType), storage.ErrInvalid, contentType)
	}
}

func TestResolvePutOptions(t *testing.T) {
	resolved, err := storage.ResolvePutOptions(nil)
	require.NoError(t, err)
	require.Equal(t, storage.PutOptions{ContentType: storage.DefaultContentType}, resolved)

	metadata := map[string]string{"owner": "a"}
	resolved, err = storage.ResolvePutOptions(&storage.PutOptions{Metadata: metadata, MaximumSize: 10})
	require.NoError(t, err)
	metadata["owner"] = "changed"
	require.Equal(t, "a", resolved.Metadata["owner"], "the metadata is cloned")
	require.Equal(t, int64(10), resolved.MaximumSize)

	resolved, err = storage.ResolvePutOptions(&storage.PutOptions{Metadata: map[string]string{}})
	require.NoError(t, err)
	require.Nil(t, resolved.Metadata, "empty metadata is none")

	_, err = storage.ResolvePutOptions(&storage.PutOptions{MaximumSize: -1})
	require.ErrorIs(t, err, storage.ErrInvalid)
}

func TestGetOptionsRange(t *testing.T) {
	testCases := []struct {
		name           string
		options        storage.GetOptions
		size           int64
		offset, length int64
		invalid        bool
	}{
		{name: "whole object", size: 10, length: 10},
		{name: "empty object", size: 0, length: 0},
		{name: "empty object with a length", options: storage.GetOptions{Length: 5}, size: 0, length: 0},
		{name: "offset to the end", options: storage.GetOptions{Offset: 4}, size: 10, offset: 4, length: 6},
		{name: "slice", options: storage.GetOptions{Offset: 4, Length: 2}, size: 10, offset: 4, length: 2},
		{name: "cut at the end", options: storage.GetOptions{Offset: 8, Length: 5}, size: 10, offset: 8, length: 2},
		{name: "offset at the size", options: storage.GetOptions{Offset: 10}, size: 10, invalid: true},
		{name: "offset beyond", options: storage.GetOptions{Offset: 11}, size: 10, invalid: true},
		{name: "offset on an empty object", options: storage.GetOptions{Offset: 1}, size: 0, invalid: true},
		{name: "negative offset", options: storage.GetOptions{Offset: -1}, size: 10, invalid: true},
		{name: "negative length", options: storage.GetOptions{Length: -1}, size: 10, invalid: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			offset, length, err := testCase.options.Range(testCase.size)
			if testCase.invalid {
				require.ErrorIs(t, err, storage.ErrInvalidRange)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.offset, offset)
			require.Equal(t, testCase.length, length)
		})
	}
	_, err := storage.ResolveGetOptions(&storage.GetOptions{Offset: -1})
	require.ErrorIs(t, err, storage.ErrInvalidRange)
}

func TestResolveListOptions(t *testing.T) {
	resolved, err := storage.ResolveListOptions(nil)
	require.NoError(t, err)
	require.Equal(t, storage.MaximumListLimit, resolved.Limit)
	resolved, err = storage.ResolveListOptions(&storage.ListOptions{Limit: storage.MaximumListLimit + 1})
	require.NoError(t, err)
	require.Equal(t, storage.MaximumListLimit, resolved.Limit, "a larger limit is capped")
	resolved, err = storage.ResolveListOptions(&storage.ListOptions{Limit: 7, Prefix: "a/", StartAfter: "a/b"})
	require.NoError(t, err)
	require.Equal(t, storage.ListOptions{Limit: 7, Prefix: "a/", StartAfter: "a/b"}, resolved)
	_, err = storage.ResolveListOptions(&storage.ListOptions{Limit: -1})
	require.ErrorIs(t, err, storage.ErrInvalid)
}

func TestResolvePresignOptions(t *testing.T) {
	get, err := storage.ResolvePresignGetOptions(nil)
	require.NoError(t, err)
	require.Equal(t, storage.DefaultPresignExpiry, get.Expiry)
	get, err = storage.ResolvePresignGetOptions(&storage.PresignGetOptions{Expiry: time.Hour, ContentType: "image/png", ContentDisposition: `inline; filename="a.png"`})
	require.NoError(t, err)
	require.Equal(t, time.Hour, get.Expiry)
	for _, options := range []storage.PresignGetOptions{
		{Expiry: -time.Second}, {Expiry: storage.MaximumPresignExpiry + time.Second}, {ContentType: "nope"}, {ContentDisposition: "a\r\nb"}, {ContentDisposition: "; broken"},
	} {
		_, err := storage.ResolvePresignGetOptions(&options)
		require.ErrorIs(t, err, storage.ErrInvalid, "options %+v", options)
	}

	put, err := storage.ResolvePresignPutOptions(nil)
	require.NoError(t, err)
	require.Equal(t, storage.PresignPutOptions{Expiry: storage.DefaultPresignExpiry, ContentType: storage.DefaultContentType}, put)
	_, err = storage.ResolvePresignPutOptions(&storage.PresignPutOptions{Expiry: storage.MaximumPresignExpiry + 1})
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = storage.ResolvePresignPutOptions(&storage.PresignPutOptions{ContentType: "nope"})
	require.ErrorIs(t, err, storage.ErrInvalid)
}
