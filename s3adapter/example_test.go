package s3adapter_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/govengers/storage"
	"github.com/govengers/storage/s3adapter"
)

// A store on a self-hosted S3-compatible server; main reads the values from its configuration.
// The upload goes from the browser straight to the bucket.
func ExampleNew() {
	store, err := s3adapter.New(&s3adapter.Options{
		Endpoint:        "http://rustfs.storage.svc:9000",
		Bucket:          "todo-attachments",
		AccessKeyID:     "todo",
		SecretAccessKey: "from-the-secret-store",
		PathStyle:       true,
		HTTPClient:      &http.Client{}, // &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)} to trace
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	upload, err := storage.PresignPut(context.Background(), store, "uploads/7f3c.png", &storage.PresignPutOptions{
		ContentType: "image/png", Expiry: 10 * time.Minute,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(upload.Method, upload.Header.Get("Content-Type"))
	// Output: PUT image/png
}
