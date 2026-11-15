// Package storagetest is the conformance suite of storage.Store: every backend in this module
// passes it, and a Store implemented elsewhere should too.
//
//	func TestConformance(t *testing.T) {
//	    storagetest.Run(t, func(t *testing.T) storage.Store { return memory.New() })
//	}
//
// Run checks the Store contract and, when the store implements them, storage.Copier and
// storage.Presigner; presigned requests are sent with a plain net/http client, so their URLs
// must be reachable from the test.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
)

// NewStore returns an empty store for one test. It registers its own cleanup with t.
type NewStore func(t *testing.T) storage.Store

// Run runs the conformance suite against stores from newStore, one fresh store per subtest,
// in parallel.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	tests := map[string]func(t *testing.T, store storage.Store){
		"RoundTrip":           testRoundTrip,
		"Defaults":            testDefaults,
		"Overwrite":           testOverwrite,
		"Ranges":              testRanges,
		"Missing":             testMissing,
		"Delete":              testDelete,
		"InvalidArguments":    testInvalidArguments,
		"MaximumSize":         testMaximumSize,
		"FailedBody":          testFailedBody,
		"CancelledContext":    testCancelledContext,
		"List":                testList,
		"ListPages":           testListPages,
		"ResultsAreCopies":    testResultsAreCopies,
		"ConcurrentPuts":      testConcurrentPuts,
		"Copy":                testCopy,
		"Copier":              testCopier,
		"PresignGet":          testPresignGet,
		"PresignPut":          testPresignPut,
		"PresignUnsupported":  testPresignUnsupported,
		"PresignInvalid":      testPresignInvalid,
		"ReadersAreSnapshots": testReadersAreSnapshots,
	}
	for _, name := range slices.Sorted(maps.Keys(tests)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tests[name](t, newStore(t))
		})
	}
}

func put(t *testing.T, store storage.Store, key, content string, options *storage.PutOptions) *storage.Object {
	t.Helper()
	object, err := store.Put(t.Context(), key, strings.NewReader(content), options)
	require.NoError(t, err, "put %q", key)
	return object
}

func read(t *testing.T, store storage.Store, key string, options *storage.GetOptions) (*storage.Reader, string) {
	t.Helper()
	reader, err := store.Get(t.Context(), key, options)
	require.NoError(t, err, "get %q", key)
	content, err := io.ReadAll(reader)
	require.NoError(t, err, "read %q", key)
	require.NoError(t, reader.Close())
	return reader, string(content)
}

func testRoundTrip(t *testing.T, store storage.Store) {
	metadata := map[string]string{"owner": "user-42", "x-origin": "upload form"}
	before := time.Now().Add(-time.Minute)
	stored := put(t, store, "tasks/42/notes.txt", "hello, storage", &storage.PutOptions{ContentType: "text/plain; charset=utf-8", Metadata: metadata})
	require.Equal(t, "tasks/42/notes.txt", stored.Key)
	require.Equal(t, int64(len("hello, storage")), stored.Size)
	require.Equal(t, "text/plain; charset=utf-8", stored.ContentType)
	require.Equal(t, metadata, stored.Metadata)

	reader, content := read(t, store, "tasks/42/notes.txt", nil)
	require.Equal(t, "hello, storage", content)
	require.Equal(t, int64(0), reader.Offset)
	require.Equal(t, stored.Size, reader.Length)
	requireObject(t, reader.Object, stored, before)

	stat, err := store.Stat(t.Context(), "tasks/42/notes.txt")
	require.NoError(t, err)
	requireObject(t, *stat, stored, before)
}

func requireObject(t *testing.T, got storage.Object, want *storage.Object, notBefore time.Time) {
	t.Helper()
	require.Equal(t, want.Key, got.Key)
	require.Equal(t, want.Size, got.Size)
	require.Equal(t, want.ContentType, got.ContentType)
	require.Equal(t, want.Metadata, got.Metadata)
	require.True(t, got.ModifiedAt.After(notBefore), "ModifiedAt %v is not after %v", got.ModifiedAt, notBefore)
}

func testDefaults(t *testing.T, store storage.Store) {
	stored := put(t, store, "empty", "", nil)
	require.Equal(t, int64(0), stored.Size)
	require.Equal(t, storage.DefaultContentType, stored.ContentType)
	require.Nil(t, stored.Metadata)

	reader, content := read(t, store, "empty", nil)
	require.Empty(t, content)
	require.Equal(t, storage.DefaultContentType, reader.Object.ContentType)
	require.Nil(t, reader.Object.Metadata)

	_, content = read(t, store, "empty", &storage.GetOptions{Length: 10})
	require.Empty(t, content, "an offset of zero is valid on an empty object")
}

func testOverwrite(t *testing.T, store storage.Store) {
	put(t, store, "report.pdf", "first version", &storage.PutOptions{ContentType: "application/pdf", Metadata: map[string]string{"version": "1"}})
	put(t, store, "report.pdf", "second", &storage.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"revision": "2"}})

	reader, content := read(t, store, "report.pdf", nil)
	require.Equal(t, "second", content)
	require.Equal(t, "text/plain", reader.Object.ContentType)
	require.Equal(t, map[string]string{"revision": "2"}, reader.Object.Metadata, "metadata is replaced, not merged")
}

func testRanges(t *testing.T, store storage.Store) {
	put(t, store, "digits", "0123456789", nil)
	testCases := []struct {
		name       string
		options    storage.GetOptions
		want       string
		wantOffset int64
	}{
		{name: "whole object", options: storage.GetOptions{}, want: "0123456789"},
		{name: "from an offset to the end", options: storage.GetOptions{Offset: 7}, want: "789", wantOffset: 7},
		{name: "a slice", options: storage.GetOptions{Offset: 2, Length: 3}, want: "234", wantOffset: 2},
		{name: "a prefix", options: storage.GetOptions{Length: 4}, want: "0123"},
		{name: "cut at the end", options: storage.GetOptions{Offset: 8, Length: 100}, want: "89", wantOffset: 8},
		{name: "the last byte", options: storage.GetOptions{Offset: 9, Length: 1}, want: "9", wantOffset: 9},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			reader, content := read(t, store, "digits", &testCase.options)
			require.Equal(t, testCase.want, content)
			require.Equal(t, testCase.wantOffset, reader.Offset)
			require.Equal(t, int64(len(testCase.want)), reader.Length)
			require.Equal(t, int64(10), reader.Object.Size, "Object describes the whole object")
		})
	}
	for _, options := range []storage.GetOptions{{Offset: 10}, {Offset: 11, Length: 1}, {Offset: -1}, {Length: -1}} {
		_, err := store.Get(t.Context(), "digits", &options)
		require.ErrorIs(t, err, storage.ErrInvalidRange, "range %+v", options)
	}
	_, err := store.Get(t.Context(), "missing", &storage.GetOptions{Offset: 5})
	require.ErrorIs(t, err, storage.ErrNotFound, "a missing object is not found, whatever the range")
}

func testMissing(t *testing.T, store storage.Store) {
	_, err := store.Get(t.Context(), "missing/object", nil)
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = store.Stat(t.Context(), "missing/object")
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NoError(t, store.Delete(t.Context(), "missing/object"), "deleting a missing object is no error")

	put(t, store, "folder/object", "x", nil)
	_, err = store.Stat(t.Context(), "folder")
	require.ErrorIs(t, err, storage.ErrNotFound, "a prefix of a key is no object")
	_, err = store.Get(t.Context(), "folder", nil)
	require.ErrorIs(t, err, storage.ErrNotFound, "a prefix of a key is no object")
}

func testDelete(t *testing.T, store storage.Store) {
	put(t, store, "a/b/c.txt", "c", nil)
	put(t, store, "a/d.txt", "d", nil)
	require.NoError(t, store.Delete(t.Context(), "a/b/c.txt"))

	_, err := store.Stat(t.Context(), "a/b/c.txt")
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.Equal(t, []string{"a/d.txt"}, listKeys(t, store, ""))

	put(t, store, "a/b/c.txt", "again", nil)
	_, content := read(t, store, "a/b/c.txt", nil)
	require.Equal(t, "again", content, "a deleted key can be stored again")
}

func testInvalidArguments(t *testing.T, store storage.Store) {
	longSegment := strings.Repeat("s", storage.MaximumKeySegmentLength+1)
	for _, key := range []string{"", "/leading", "trailing/", "double//slash", "../escape", "a/./b", "a/../b", "back\\slash", "tab\tkey", longSegment, "\xff"} {
		_, err := store.Put(t.Context(), key, strings.NewReader("x"), nil)
		require.ErrorIs(t, err, storage.ErrInvalid, "put %q", key)
		_, err = store.Get(t.Context(), key, nil)
		require.ErrorIs(t, err, storage.ErrInvalid, "get %q", key)
		_, err = store.Stat(t.Context(), key)
		require.ErrorIs(t, err, storage.ErrInvalid, "stat %q", key)
		require.ErrorIs(t, store.Delete(t.Context(), key), storage.ErrInvalid, "delete %q", key)
	}
	for _, options := range []storage.PutOptions{
		{ContentType: "not a media type"},
		{ContentType: "text/plain\r\nx-injected: 1"},
		{Metadata: map[string]string{"Upper": "x"}},
		{Metadata: map[string]string{"under_score": "x"}},
		{Metadata: map[string]string{"key": "ünïcode"}},
		{Metadata: map[string]string{"key": ""}},
		{Metadata: map[string]string{"key": strings.Repeat("v", storage.MaximumMetadataSize)}},
		{MaximumSize: -1},
	} {
		_, err := store.Put(t.Context(), "valid", strings.NewReader("x"), &options)
		require.ErrorIs(t, err, storage.ErrInvalid, "put options %+v", options)
	}
	for _, options := range []storage.ListOptions{{Prefix: "/x"}, {Prefix: "a//"}, {Prefix: "../"}, {Limit: -1}} {
		_, err := store.List(t.Context(), &options)
		require.ErrorIs(t, err, storage.ErrInvalid, "list options %+v", options)
	}
	require.Empty(t, listKeys(t, store, ""), "nothing was stored")
}

func testMaximumSize(t *testing.T, store storage.Store) {
	put(t, store, "upload", "previous", nil)
	_, err := store.Put(t.Context(), "upload", strings.NewReader("eleven byte"), &storage.PutOptions{MaximumSize: 10})
	require.ErrorIs(t, err, storage.ErrTooLarge)
	_, content := read(t, store, "upload", nil)
	require.Equal(t, "previous", content, "a rejected put leaves the previous object")

	_, err = store.Put(t.Context(), "fresh", strings.NewReader("eleven byte"), &storage.PutOptions{MaximumSize: 10})
	require.ErrorIs(t, err, storage.ErrTooLarge)
	_, err = store.Stat(t.Context(), "fresh")
	require.ErrorIs(t, err, storage.ErrNotFound, "a rejected put stores nothing")

	stored := put(t, store, "upload", "ten bytes!", &storage.PutOptions{MaximumSize: 10})
	require.Equal(t, int64(10), stored.Size, "a body of exactly MaximumSize fits")
}

// errBody is the error failingReader returns after its content.
var errBody = errors.New("storagetest: the body failed")

type failingReader struct {
	content io.Reader
}

func (r *failingReader) Read(buffer []byte) (int, error) {
	count, err := r.content.Read(buffer)
	if errors.Is(err, io.EOF) {
		return count, errBody
	}
	return count, err
}

func testFailedBody(t *testing.T, store storage.Store) {
	put(t, store, "upload", "previous", nil)
	_, err := store.Put(t.Context(), "upload", &failingReader{content: strings.NewReader("partial")}, nil)
	require.Error(t, err)
	_, content := read(t, store, "upload", nil)
	require.Equal(t, "previous", content, "a failed put leaves the previous object")

	_, err = store.Put(t.Context(), "fresh", &failingReader{content: strings.NewReader("partial")}, nil)
	require.Error(t, err)
	_, err = store.Stat(t.Context(), "fresh")
	require.ErrorIs(t, err, storage.ErrNotFound, "a failed put stores nothing")
}

func testCancelledContext(t *testing.T, store storage.Store) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.Put(ctx, "cancelled", strings.NewReader("content"), nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = store.Stat(t.Context(), "cancelled")
	require.ErrorIs(t, err, storage.ErrNotFound, "a cancelled put stores nothing")
}

// listKeys reads every page of prefix with a small limit, so paging is part of every listing.
func listKeys(t *testing.T, store storage.Store, prefix string) []string {
	t.Helper()
	keys := []string{}
	options := &storage.ListOptions{Prefix: prefix, Limit: 2}
	for {
		page, err := store.List(t.Context(), options)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Objects), 2)
		for _, object := range page.Objects {
			keys = append(keys, object.Key)
		}
		if page.Next == "" {
			return keys
		}
		require.NotEmpty(t, page.Objects, "a page with a Next holds objects")
		options.StartAfter = page.Next
	}
}

func testList(t *testing.T, store storage.Store) {
	// '-' < '.' < '/' in byte order: the order of keys, not of a directory walk.
	keys := []string{"docs/a/b.txt", "docs/a.txt", "docs/a-c.txt", "docs/b/c/d.txt", "doc.txt", "other/x.txt", "docs/z"}
	for _, key := range keys {
		put(t, store, key, "content of "+key, &storage.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"k": "v"}})
	}
	sorted := slices.Sorted(slices.Values(keys))
	require.Equal(t, sorted, listKeys(t, store, ""))
	require.Equal(t, []string{"doc.txt", "docs/a-c.txt", "docs/a.txt", "docs/a/b.txt", "docs/b/c/d.txt", "docs/z"}, listKeys(t, store, "doc"))
	require.Equal(t, []string{"docs/a-c.txt", "docs/a.txt", "docs/a/b.txt"}, listKeys(t, store, "docs/a"))
	require.Equal(t, []string{"docs/a/b.txt"}, listKeys(t, store, "docs/a/"))
	require.Equal(t, []string{"docs/b/c/d.txt"}, listKeys(t, store, "docs/b/c"))
	require.Empty(t, listKeys(t, store, "nothing/"))
	require.Empty(t, listKeys(t, store, "docs/z/"))

	page, err := store.List(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, page.Objects, len(keys))
	require.Empty(t, page.Next)
	for _, object := range page.Objects {
		require.Equal(t, int64(len("content of "+object.Key)), object.Size, object.Key)
		require.False(t, object.ModifiedAt.IsZero(), object.Key)
		require.Empty(t, object.ContentType, "List carries no content type")
		require.Nil(t, object.Metadata, "List carries no metadata")
	}
}

func testListPages(t *testing.T, store storage.Store) {
	for index := range 5 {
		put(t, store, fmt.Sprintf("page/%d", index), "x", nil)
	}
	page, err := store.List(t.Context(), &storage.ListOptions{Prefix: "page/", Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Objects, 5)
	require.Empty(t, page.Next, "a page that holds the rest is the last")

	page, err = store.List(t.Context(), &storage.ListOptions{Prefix: "page/", Limit: 3})
	require.NoError(t, err)
	require.Len(t, page.Objects, 3)
	require.Equal(t, "page/2", page.Next)

	page, err = store.List(t.Context(), &storage.ListOptions{Prefix: "page/", StartAfter: "page/1x"})
	require.NoError(t, err)
	require.Equal(t, "page/2", page.Objects[0].Key, "StartAfter need not be a stored key")

	page, err = store.List(t.Context(), &storage.ListOptions{Prefix: "page/", StartAfter: "page/4"})
	require.NoError(t, err)
	require.Empty(t, page.Objects)
	require.Empty(t, page.Next)
}

func testResultsAreCopies(t *testing.T, store storage.Store) {
	metadata := map[string]string{"owner": "a"}
	stored := put(t, store, "object", "x", &storage.PutOptions{Metadata: metadata})
	metadata["owner"] = "changed after put"
	stored.Metadata["owner"] = "changed in the result"

	stat, err := store.Stat(t.Context(), "object")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"owner": "a"}, stat.Metadata)
	stat.Metadata["owner"] = "changed in a stat"

	reader, _ := read(t, store, "object", nil)
	require.Equal(t, map[string]string{"owner": "a"}, reader.Object.Metadata)
}

func testConcurrentPuts(t *testing.T, store storage.Store) {
	const writers = 8
	contents := make([]string, writers)
	errs := make([]error, writers)
	var waitGroup sync.WaitGroup
	for index := range writers {
		contents[index] = strings.Repeat(string(rune('a'+index)), 1000+index)
		waitGroup.Go(func() {
			_, errs[index] = store.Put(t.Context(), "contended", strings.NewReader(contents[index]), nil)
		})
	}
	waitGroup.Wait()
	require.NoError(t, errors.Join(errs...))
	reader, content := read(t, store, "contended", nil)
	require.Contains(t, contents, content, "the object is one whole write, never a mix")
	require.Equal(t, int64(len(content)), reader.Object.Size)
}

func testReadersAreSnapshots(t *testing.T, store storage.Store) {
	put(t, store, "object", "first content", nil)
	reader, err := store.Get(t.Context(), "object", nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()
	head := make([]byte, 6)
	_, err = io.ReadFull(reader, head)
	require.NoError(t, err)

	put(t, store, "object", "second content, longer", nil)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "first content", string(head)+string(rest), "an open reader keeps reading the object it opened")
}

func testCopy(t *testing.T, store storage.Store) {
	testCopyContract(t, func(ctx context.Context, source, destination string) (*storage.Object, error) {
		return storage.Copy(ctx, store, source, destination)
	}, store)
}

func testCopier(t *testing.T, store storage.Store) {
	copier, ok := store.(storage.Copier)
	if !ok {
		t.Skip("the store is no storage.Copier")
	}
	object, err := copier.Copy(t.Context(), "missing", "destination")
	if errors.Is(err, storage.ErrNotSupported) {
		t.Skip("the store's Copy reports ErrNotSupported")
	}
	require.Nil(t, object)
	testCopyContract(t, copier.Copy, store)
}

type copyFunction func(ctx context.Context, source, destination string) (*storage.Object, error)

func testCopyContract(t *testing.T, copyObject copyFunction, store storage.Store) {
	metadata := map[string]string{"owner": "user-42"}
	put(t, store, "source.bin", "the content", &storage.PutOptions{ContentType: "image/png", Metadata: metadata})
	put(t, store, "copies/destination.bin", "old", nil)

	copied, err := copyObject(t.Context(), "source.bin", "copies/destination.bin")
	require.NoError(t, err)
	require.Equal(t, "copies/destination.bin", copied.Key)
	require.Equal(t, int64(len("the content")), copied.Size)

	reader, content := read(t, store, "copies/destination.bin", nil)
	require.Equal(t, "the content", content)
	require.Equal(t, "image/png", reader.Object.ContentType)
	require.Equal(t, metadata, reader.Object.Metadata)

	_, content = read(t, store, "source.bin", nil)
	require.Equal(t, "the content", content, "the source stays")

	_, err = copyObject(t.Context(), "source.bin", "source.bin")
	require.NoError(t, err, "copying an object onto itself is no error")
	_, content = read(t, store, "source.bin", nil)
	require.Equal(t, "the content", content)

	_, err = copyObject(t.Context(), "missing", "copies/other")
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = copyObject(t.Context(), "source.bin", "../escape")
	require.ErrorIs(t, err, storage.ErrInvalid)
}

func presigner(t *testing.T, store storage.Store) storage.Presigner {
	t.Helper()
	presigner, ok := store.(storage.Presigner)
	if !ok {
		t.Skip("the store is no storage.Presigner")
	}
	_, err := presigner.PresignGet(t.Context(), "probe", nil)
	if errors.Is(err, storage.ErrNotSupported) {
		t.Skip("the store's PresignGet reports ErrNotSupported")
	}
	return presigner
}

// response is an answer to a presigned request, read completely.
type response struct {
	status int
	header http.Header
	body   []byte
}

func send(t *testing.T, presigned *storage.PresignedRequest, body io.Reader) *response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), presigned.Method, presigned.URL, body)
	require.NoError(t, err)
	for name, values := range presigned.Header {
		request.Header[name] = values
	}
	answer, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	require.NoError(t, err)
	content, err := io.ReadAll(answer.Body)
	require.NoError(t, errors.Join(err, answer.Body.Close()))
	return &response{status: answer.StatusCode, header: answer.Header, body: content}
}

func testPresignGet(t *testing.T, store storage.Store) {
	presigner := presigner(t, store)
	put(t, store, "shared/report.pdf", "%PDF-1.7 content", &storage.PutOptions{ContentType: "application/pdf"})

	presigned, err := presigner.PresignGet(t.Context(), "shared/report.pdf", &storage.PresignGetOptions{
		Expiry: time.Minute, ContentDisposition: `attachment; filename="report.pdf"`,
	})
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, presigned.Method)
	require.WithinDuration(t, time.Now().Add(time.Minute), presigned.ExpiresAt, 10*time.Second)

	answer := send(t, presigned, nil)
	require.Equal(t, http.StatusOK, answer.status)
	require.Equal(t, "%PDF-1.7 content", string(answer.body))
	require.Equal(t, `attachment; filename="report.pdf"`, answer.header.Get("Content-Disposition"))
	require.Equal(t, "application/pdf", answer.header.Get("Content-Type"))
}

func testPresignPut(t *testing.T, store storage.Store) {
	presigner := presigner(t, store)
	presigned, err := presigner.PresignPut(t.Context(), "uploads/photo.png", &storage.PresignPutOptions{ContentType: "image/png"})
	require.NoError(t, err)
	require.Equal(t, http.MethodPut, presigned.Method)
	require.Equal(t, "image/png", presigned.Header.Get("Content-Type"), "the signed content type is a header to send")
	require.WithinDuration(t, time.Now().Add(storage.DefaultPresignExpiry), presigned.ExpiresAt, 10*time.Second)

	answer := send(t, presigned, bytes.NewReader([]byte("\x89PNG\r\n\x1a\n")))
	require.Equal(t, http.StatusOK, answer.status, string(answer.body))
	stat, err := store.Stat(t.Context(), "uploads/photo.png")
	require.NoError(t, err)
	require.Equal(t, "image/png", stat.ContentType)
	require.Equal(t, int64(8), stat.Size)
}

func testPresignUnsupported(t *testing.T, store storage.Store) {
	if _, ok := store.(storage.Presigner); ok {
		t.Skip("the store is a storage.Presigner")
	}
	_, err := storage.PresignGet(t.Context(), store, "key", nil)
	require.ErrorIs(t, err, storage.ErrNotSupported)
	_, err = storage.PresignPut(t.Context(), store, "key", nil)
	require.ErrorIs(t, err, storage.ErrNotSupported)
}

func testPresignInvalid(t *testing.T, store storage.Store) {
	presigner := presigner(t, store)
	_, err := presigner.PresignGet(t.Context(), "../escape", nil)
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = presigner.PresignGet(t.Context(), "key", &storage.PresignGetOptions{Expiry: storage.MaximumPresignExpiry + time.Second})
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = presigner.PresignGet(t.Context(), "key", &storage.PresignGetOptions{Expiry: -time.Second})
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = presigner.PresignGet(t.Context(), "key", &storage.PresignGetOptions{ContentDisposition: "attachment\r\nx: y"})
	require.ErrorIs(t, err, storage.ErrInvalid)
	_, err = presigner.PresignPut(t.Context(), "key", &storage.PresignPutOptions{ContentType: "not a media type"})
	require.ErrorIs(t, err, storage.ErrInvalid)
}
