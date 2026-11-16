package memory_test

import (
	"testing"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/storagetest"
)

func TestConformance(t *testing.T) {
	storagetest.Run(t, func(*testing.T) storage.Store { return memory.New() })
}
