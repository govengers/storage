package s3adapter_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
	"github.com/govengers/storage/internal/s3fake"
	"github.com/govengers/storage/s3adapter"
	"github.com/govengers/storage/storagetest"
)

const bucket = "objects"

func newStore(t *testing.T, server *s3fake.Server, partSize int64) *s3adapter.Store {
	t.Helper()
	store, err := s3adapter.New(&s3adapter.Options{
		Endpoint: server.URL, Bucket: bucket, AccessKeyID: "access", SecretAccessKey: "secret", PathStyle: true, PartSize: partSize,
	})
	require.NoError(t, err)
	return store
}

func TestConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Store {
		server := s3fake.New(t.Cleanup, bucket)
		t.Cleanup(func() { require.Zero(t, server.PendingUploads(), "every multipart upload was completed or aborted") })
		return newStore(t, server, 0)
	})
}

func TestNewValidatesOptions(t *testing.T) {
	valid := s3adapter.Options{Endpoint: "http://localhost:9000", Bucket: bucket, AccessKeyID: "access", SecretAccessKey: "secret"}
	testCases := map[string]func(options *s3adapter.Options){
		"no endpoint":         func(options *s3adapter.Options) { options.Endpoint = "" },
		"endpoint not a URL":  func(options *s3adapter.Options) { options.Endpoint = "localhost:9000" },
		"endpoint not http":   func(options *s3adapter.Options) { options.Endpoint = "ftp://localhost" },
		"no bucket":           func(options *s3adapter.Options) { options.Bucket = "" },
		"no access key":       func(options *s3adapter.Options) { options.AccessKeyID = "" },
		"no secret":           func(options *s3adapter.Options) { options.SecretAccessKey = "" },
		"part size too small": func(options *s3adapter.Options) { options.PartSize = s3adapter.MinimumPartSize - 1 },
		"part size too large": func(options *s3adapter.Options) { options.PartSize = s3adapter.MaximumPartSize + 1 },
	}
	for name, change := range testCases {
		t.Run(name, func(t *testing.T) {
			options := valid
			change(&options)
			_, err := s3adapter.New(&options)
			require.Error(t, err)
		})
	}
	_, err := s3adapter.New(nil)
	require.Error(t, err)
	_, err = s3adapter.New(&valid)
	require.NoError(t, err)
}
