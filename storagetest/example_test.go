package storagetest_test

import (
	"testing"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/storagetest"
)

// A backend's test runs the suite on a fresh store per subtest.
func ExampleRun() {
	_ = func(t *testing.T) {
		storagetest.Run(t, func(*testing.T) storage.Store { return memory.New() })
	}
}
