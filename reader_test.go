package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/govengers/storage"
)

func TestLimitReader(t *testing.T) {
	testCases := []struct {
		name      string
		content   string
		limit     int64
		want      string
		wantError error
	}{
		{name: "below the limit", content: "abc", limit: 5, want: "abc"},
		{name: "exactly the limit", content: "abcde", limit: 5, want: "abcde"},
		{name: "above the limit", content: "abcdef", limit: 5, want: "abcde", wantError: storage.ErrTooLarge},
		{name: "empty with a zero limit", content: "", limit: 0, want: ""},
		{name: "content with a zero limit", content: "a", limit: 0, want: "", wantError: storage.ErrTooLarge},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			// OneByteReader makes every read short, so the limit is crossed between reads.
			content, err := io.ReadAll(storage.LimitReader(iotest.OneByteReader(strings.NewReader(testCase.content)), testCase.limit))
			require.ErrorIs(t, err, testCase.wantError)
			require.Equal(t, testCase.want, string(content))
		})
	}
	errFailed := errors.New("failed")
	_, err := io.ReadAll(storage.LimitReader(iotest.ErrReader(errFailed), 5))
	require.ErrorIs(t, err, errFailed)
	_, err = io.ReadAll(storage.LimitReader(io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errFailed)), 3))
	require.ErrorIs(t, err, errFailed, "an error at the limit is the reader's, not ErrTooLarge")
}

func TestSHA256Reader(t *testing.T) {
	reader := storage.NewSHA256Reader(strings.NewReader("hello"))
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
	require.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", reader.Sum())
	require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", storage.NewSHA256Reader(bytes.NewReader(nil)).Sum())
}

func TestContextReader(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader := storage.ContextReader(ctx, strings.NewReader("abcdef"))
	buffer := make([]byte, 3)
	_, err := reader.Read(buffer)
	require.NoError(t, err)
	cancel()
	_, err = reader.Read(buffer)
	require.ErrorIs(t, err, context.Canceled)
}
