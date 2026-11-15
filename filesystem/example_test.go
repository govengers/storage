package filesystem_test

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/govengers/storage/filesystem"
)

func ExampleNew() {
	root, err := os.MkdirTemp("", "storage-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(root)

	store, err := filesystem.New(&filesystem.Options{Root: root}) // a mounted volume in a service
	if err != nil {
		fmt.Println(err)
		return
	}
	defer store.Close()

	object, err := store.Put(context.Background(), "invoices/2026/11.pdf", strings.NewReader("%PDF-1.7"), nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(object.Key, object.Size, object.ContentType)
	// Output: invoices/2026/11.pdf 8 application/octet-stream
}
