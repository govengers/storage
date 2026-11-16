package storage

import (
	"fmt"
	"maps"
	"mime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The limits ValidateKey and ValidateMetadata enforce. They are the strictest of the supported
// backends, so a key or metadata set that one Store accepts works on every other (keys that
// differ only in case aside, which a case-insensitive filesystem merges).
const (
	MaximumKeyLength = 1024 // bytes; the S3 limit
	// MaximumKeySegmentLength is in bytes between two slashes: the 255 of common filesystems
	// less the two the filesystem store appends to every name.
	MaximumKeySegmentLength  = 253
	MaximumMetadataSize      = 2048 // bytes of all metadata keys and values together; the S3 limit
	MaximumContentTypeLength = 255  // bytes
)

const keySeparator = "/"

// ValidateKey reports whether key may name an object, wrapping ErrInvalid when not. A key is
// valid UTF-8 of at most MaximumKeyLength bytes, made of segments separated by single slashes.
// A segment is not empty, "." or "..", holds no control character and no backslash, and is at
// most MaximumKeySegmentLength bytes long. So a key never starts or ends with a slash and can
// never leave the directory of a filesystem store. Keys are case-sensitive; on a
// case-insensitive filesystem two keys that differ only in case name the same file.
func ValidateKey(key string) error {
	if key == "" || len(key) > MaximumKeyLength {
		return fmt.Errorf("key of %d bytes, want 1 to %d: %w", len(key), MaximumKeyLength, ErrInvalid)
	}
	if !utf8.ValidString(key) {
		return fmt.Errorf("key %q is not valid UTF-8: %w", key, ErrInvalid)
	}
	for segment := range strings.SplitSeq(key, keySeparator) {
		if err := validateSegment(segment); err != nil {
			return fmt.Errorf("key %q: %w", key, err)
		}
	}
	return nil
}

// ValidatePrefix reports whether prefix may select keys in a List, wrapping ErrInvalid when
// not. The empty prefix selects every key. Otherwise the rules of ValidateKey apply to every
// segment but the last, which may be empty (a prefix ending in a slash) or the start of a
// segment.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaximumKeyLength || !utf8.ValidString(prefix) {
		return fmt.Errorf("prefix %q is longer than %d bytes or not valid UTF-8: %w", prefix, MaximumKeyLength, ErrInvalid)
	}
	segments := strings.Split(prefix, keySeparator)
	last := len(segments) - 1
	for index, segment := range segments {
		check := validateSegment
		if index == last {
			check = validateCharacters // only filters names, never resolved as a path
		}
		if err := check(segment); err != nil {
			return fmt.Errorf("prefix %q: %w", prefix, err)
		}
	}
	return nil
}

func validateSegment(segment string) error {
	if segment == "" || segment == "." || segment == ".." || len(segment) > MaximumKeySegmentLength {
		return fmt.Errorf("segment %q is empty, a dot segment or longer than %d bytes: %w", segment, MaximumKeySegmentLength, ErrInvalid)
	}
	return validateCharacters(segment)
}

func validateCharacters(segment string) error {
	for _, character := range segment {
		if unicode.IsControl(character) || character == '\\' {
			return fmt.Errorf("segment %q holds a control character or a backslash: %w", segment, ErrInvalid)
		}
	}
	return nil
}

// ValidateMetadata reports whether metadata may be stored with an object, wrapping ErrInvalid
// when not. Keys are lowercase ASCII letters, digits and hyphens and start with a letter or
// digit; values are non-empty printable ASCII without leading or trailing spaces. Both travel
// as HTTP headers on S3, where anything else is changed or rejected. Together they are at most
// MaximumMetadataSize bytes.
func ValidateMetadata(metadata map[string]string) error {
	size := 0
	for key, value := range metadata {
		if !validMetadataKey(key) {
			return fmt.Errorf("metadata key %q: %w", key, ErrInvalid)
		}
		if !validMetadataValue(value) {
			return fmt.Errorf("metadata value of %q: %w", key, ErrInvalid)
		}
		size += len(key) + len(value)
	}
	if size > MaximumMetadataSize {
		return fmt.Errorf("metadata of %d bytes, at most %d: %w", size, MaximumMetadataSize, ErrInvalid)
	}
	return nil
}

func validMetadataKey(key string) bool {
	if key == "" || key[0] == '-' {
		return false
	}
	for _, character := range []byte(key) {
		isLowerLetter := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		if !isLowerLetter && !isDigit && character != '-' {
			return false
		}
	}
	return true
}

func validMetadataValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && printableASCII(value)
}

func printableASCII(text string) bool {
	for _, character := range []byte(text) {
		if character < ' ' || character > '~' {
			return false
		}
	}
	return true
}

// ValidateContentType reports whether contentType may be stored with an object, wrapping
// ErrInvalid when not: printable ASCII of at most MaximumContentTypeLength bytes that
// mime.ParseMediaType accepts as a type/subtype with optional parameters.
func ValidateContentType(contentType string) error {
	if len(contentType) > MaximumContentTypeLength || !printableASCII(contentType) {
		return fmt.Errorf("content type %q: %w", contentType, ErrInvalid)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("content type %q: %v: %w", contentType, err, ErrInvalid) //nolint:errorlint // the parser's message is detail, ErrInvalid is the condition
	}
	if mainType, subtype, found := strings.Cut(mediaType, "/"); !found || mainType == "" || subtype == "" {
		return fmt.Errorf("content type %q is no type/subtype: %w", contentType, ErrInvalid)
	}
	return nil
}

// ResolvePutOptions validates options and returns them with the defaults applied and the
// metadata cloned, for Store implementations. A nil options means the defaults.
func ResolvePutOptions(options *PutOptions) (PutOptions, error) {
	var resolved PutOptions
	if options != nil {
		resolved = *options
	}
	if resolved.ContentType == "" {
		resolved.ContentType = DefaultContentType
	}
	if err := ValidateContentType(resolved.ContentType); err != nil {
		return PutOptions{}, err
	}
	if err := ValidateMetadata(resolved.Metadata); err != nil {
		return PutOptions{}, err
	}
	if resolved.MaximumSize < 0 {
		return PutOptions{}, fmt.Errorf("maximum size %d: %w", resolved.MaximumSize, ErrInvalid)
	}
	if len(resolved.Metadata) == 0 {
		resolved.Metadata = nil
	} else {
		resolved.Metadata = maps.Clone(resolved.Metadata)
	}
	return resolved, nil
}

// ResolveGetOptions validates options for Store implementations; a nil options reads the whole
// object.
func ResolveGetOptions(options *GetOptions) (GetOptions, error) {
	if options == nil {
		return GetOptions{}, nil
	}
	if options.Offset < 0 || options.Length < 0 {
		return GetOptions{}, fmt.Errorf("range offset %d, length %d: %w", options.Offset, options.Length, ErrInvalidRange)
	}
	return *options, nil
}

// Range returns the bytes [offset, offset+length) a Get with these options reads from an
// object of size bytes, or ErrInvalidRange when Offset is not smaller than size (an Offset of
// zero is always valid).
func (o GetOptions) Range(size int64) (offset, length int64, err error) {
	if o.Offset < 0 || o.Length < 0 || (o.Offset > 0 && o.Offset >= size) {
		return 0, 0, fmt.Errorf("range offset %d, length %d of %d bytes: %w", o.Offset, o.Length, size, ErrInvalidRange)
	}
	length = size - o.Offset
	if o.Length > 0 && o.Length < length {
		length = o.Length
	}
	return o.Offset, length, nil
}

// ResolveListOptions validates options and applies the default limit, for Store
// implementations; a nil options lists the first page of every object.
func ResolveListOptions(options *ListOptions) (ListOptions, error) {
	var resolved ListOptions
	if options != nil {
		resolved = *options
	}
	if err := ValidatePrefix(resolved.Prefix); err != nil {
		return ListOptions{}, err
	}
	if resolved.Limit < 0 {
		return ListOptions{}, fmt.Errorf("list limit %d: %w", resolved.Limit, ErrInvalid)
	}
	if resolved.Limit == 0 || resolved.Limit > MaximumListLimit {
		resolved.Limit = MaximumListLimit
	}
	return resolved, nil
}

// ResolvePresignGetOptions validates options and applies the default expiry, for Presigner
// implementations; a nil options means the defaults.
func ResolvePresignGetOptions(options *PresignGetOptions) (PresignGetOptions, error) {
	var resolved PresignGetOptions
	if options != nil {
		resolved = *options
	}
	expiry, err := resolveExpiry(resolved.Expiry)
	if err != nil {
		return PresignGetOptions{}, err
	}
	resolved.Expiry = expiry
	if resolved.ContentType != "" {
		if err := ValidateContentType(resolved.ContentType); err != nil {
			return PresignGetOptions{}, err
		}
	}
	if resolved.ContentDisposition != "" {
		if err := validateContentDisposition(resolved.ContentDisposition); err != nil {
			return PresignGetOptions{}, err
		}
	}
	return resolved, nil
}

// ResolvePresignPutOptions validates options and applies the defaults, for Presigner
// implementations; a nil options means the defaults.
func ResolvePresignPutOptions(options *PresignPutOptions) (PresignPutOptions, error) {
	var resolved PresignPutOptions
	if options != nil {
		resolved = *options
	}
	expiry, err := resolveExpiry(resolved.Expiry)
	if err != nil {
		return PresignPutOptions{}, err
	}
	resolved.Expiry = expiry
	if resolved.ContentType == "" {
		resolved.ContentType = DefaultContentType
	}
	if err := ValidateContentType(resolved.ContentType); err != nil {
		return PresignPutOptions{}, err
	}
	return resolved, nil
}

func resolveExpiry(expiry time.Duration) (time.Duration, error) {
	if expiry < 0 || expiry > MaximumPresignExpiry {
		return 0, fmt.Errorf("presign expiry %v, want up to %v: %w", expiry, MaximumPresignExpiry, ErrInvalid)
	}
	if expiry == 0 {
		return DefaultPresignExpiry, nil
	}
	return expiry, nil
}

// validateContentDisposition accepts what mime.ParseMediaType parses ("attachment;
// filename=\"report.pdf\""), in printable ASCII: the value becomes a response header.
func validateContentDisposition(disposition string) error {
	if len(disposition) > MaximumContentTypeLength || !printableASCII(disposition) {
		return fmt.Errorf("content disposition %q: %w", disposition, ErrInvalid)
	}
	if _, _, err := mime.ParseMediaType(disposition); err != nil {
		return fmt.Errorf("content disposition %q: %v: %w", disposition, err, ErrInvalid) //nolint:errorlint // the parser's message is detail, ErrInvalid is the condition
	}
	return nil
}
