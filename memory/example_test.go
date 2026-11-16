package memory_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
)

func ExampleNew() {
	store := memory.New()
	for _, key := range []string{"b", "a/2", "a/1"} {
		if _, err := store.Put(context.Background(), key, strings.NewReader(key), nil); err != nil {
			fmt.Println(err)
			return
		}
	}
	page, err := store.List(context.Background(), &storage.ListOptions{Prefix: "a/"})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, object := range page.Objects {
		fmt.Println(object.Key)
	}
	// Output:
	// a/1
	// a/2
}
