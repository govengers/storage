package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
)

func Example() {
	ctx := context.Background()
	store := memory.New() // filesystem.New or s3adapter.New in a service

	_, err := store.Put(ctx, "tasks/42/notes.txt", strings.NewReader("hello, storage"), &storage.PutOptions{
		ContentType: "text/plain; charset=utf-8",
		Metadata:    map[string]string{"uploaded-by": "user-7"},
		MaximumSize: 10 << 20,
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	reader, err := store.Get(ctx, "tasks/42/notes.txt", &storage.GetOptions{Offset: 7})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(content), reader.Object.Size, reader.Object.ContentType)

	_, err = store.Stat(ctx, "tasks/43/notes.txt")
	fmt.Println(errors.Is(err, storage.ErrNotFound))
	// Output:
	// storage 14 text/plain; charset=utf-8
	// true
}

// An upload from a browser: the content type comes from the bytes, the size is capped, the
// digest is computed on the way.
func ExampleDetectContentType() {
	upload := bytes.NewReader([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR..."))
	contentType, body, err := storage.DetectContentType(upload, []string{"image/png", "image/jpeg", "application/pdf"})
	if err != nil {
		fmt.Println(err) // errors.Is(err, storage.ErrUnsupportedContentType): answer 415
		return
	}
	digest := storage.NewSHA256Reader(body)
	object, err := memory.New().Put(context.Background(), "avatars/user-7", digest, &storage.PutOptions{ContentType: contentType, MaximumSize: 5 << 20})
	if err != nil {
		fmt.Println(err) // errors.Is(err, storage.ErrTooLarge): answer 413
		return
	}
	fmt.Println(object.ContentType, object.Size, digest.Sum()[:12])
	// Output: image/png 19 65e9889afedd
}

func ExampleCopy() {
	ctx := context.Background()
	store := memory.New()
	if _, err := store.Put(ctx, "drafts/report.pdf", strings.NewReader("%PDF-1.7"), &storage.PutOptions{ContentType: "application/pdf"}); err != nil {
		fmt.Println(err)
		return
	}
	copied, err := storage.Copy(ctx, store, "drafts/report.pdf", "published/report.pdf")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(copied.Key, copied.ContentType)
	// Output: published/report.pdf application/pdf
}

func ExampleNewObservedStore() {
	// main wires the adapters: prometheusadapter.New for metrics, oteladapter.New for spans.
	var metrics, tracer storage.Observer = noObserver{}, noObserver{}
	store, err := storage.NewObservedStore(&storage.ObservedStoreOptions{
		Store:     memory.New(),
		Name:      "attachments",
		Observers: []storage.Observer{tracer, metrics},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = storage.PresignGet(context.Background(), store, "a", nil)
	fmt.Println(errors.Is(err, storage.ErrNotSupported))
	// Output: true
}

type noObserver struct{}

func (noObserver) OperationStarted(ctx context.Context, _ *storage.Operation) context.Context {
	return ctx
}

func (noObserver) OperationFinished(context.Context, *storage.Operation, *storage.Outcome) {}
