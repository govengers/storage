package filesystem

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// The file format: magic, the header's length as a big-endian uint32, the header as JSON, the
// content. A new format gets a new magic.
const (
	magic             = "gvstore1"
	prefixLength      = len(magic) + 4
	maximumHeaderSize = 64 << 10 // far above what ValidateMetadata allows; guards against a corrupt length
)

var errCorrupt = errors.New("filesystem: the object file has no valid header")

type header struct {
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func writeHeader(writer io.Writer, stored *header) error {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode the header: %w", err)
	}
	prefix := make([]byte, prefixLength, prefixLength+len(encoded))
	copy(prefix, magic)
	binary.BigEndian.PutUint32(prefix[len(magic):], uint32(len(encoded))) //nolint:gosec // G115: bounded by ValidateMetadata far below 4 GiB
	if _, err := writer.Write(append(prefix, encoded...)); err != nil {
		return fmt.Errorf("write the header: %w", err)
	}
	return nil
}

// readHeader reads the header from the start of file and returns it with the offset of the
// content.
func readHeader(file *os.File) (*header, int64, error) {
	prefix := make([]byte, prefixLength)
	if _, err := io.ReadFull(file, prefix); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errCorrupt, err) //nolint:errorlint // the cause is detail, errCorrupt is the condition
	}
	length := binary.BigEndian.Uint32(prefix[len(magic):])
	if string(prefix[:len(magic)]) != magic || length > maximumHeaderSize {
		return nil, 0, errCorrupt
	}
	encoded := make([]byte, length)
	if _, err := io.ReadFull(file, encoded); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errCorrupt, err) //nolint:errorlint // the cause is detail, errCorrupt is the condition
	}
	stored := &header{}
	if err := json.Unmarshal(encoded, stored); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errCorrupt, err) //nolint:errorlint // the cause is detail, errCorrupt is the condition
	}
	return stored, int64(prefixLength) + int64(length), nil
}
