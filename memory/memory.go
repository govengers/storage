// Package memory is a storage.Store that keeps objects in memory, for tests and local
// development. Nothing survives the process, and every object is held as one byte slice.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/govengers/storage"
)

// Store is an in-memory storage.Store and storage.Copier. It is safe for concurrent use. The
// zero value is not usable; call New.
type Store struct {
	mutex   sync.RWMutex
	objects map[string]*entry
}

type entry struct {
	object  storage.Object
	content []byte
}

var (
	_ storage.Store  = (*Store)(nil)
	_ storage.Copier = (*Store)(nil)
)

// New returns an empty Store.
func New() *Store {
	return &Store{objects: map[string]*entry{}}
}

// Put reads body into memory and stores it under key once it was read completely.
func (s *Store) Put(ctx context.Context, key string, body io.Reader, options *storage.PutOptions) (*storage.Object, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolvePutOptions(options)
	if err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	content, err := readAll(ctx, body, resolved.MaximumSize)
	if err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	stored := &entry{content: content, object: storage.Object{
		Key: key, Size: int64(len(content)), ContentType: resolved.ContentType, Metadata: resolved.Metadata, ModifiedAt: time.Now(),
	}}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	// Checked under the lock: a body that ended as ctx was cancelled, or a deadline that passed
	// while waiting for the lock, is no completed Put.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	s.objects[key] = stored
	return cloneObject(&stored.object), nil
}

func readAll(ctx context.Context, body io.Reader, maximumSize int64) ([]byte, error) {
	reader := storage.ContextReader(ctx, body)
	if maximumSize > 0 {
		reader = storage.LimitReader(reader, maximumSize)
	}
	return io.ReadAll(reader)
}

// Get returns a reader over a snapshot of the object; a later Put does not change it.
func (s *Store) Get(_ context.Context, key string, options *storage.GetOptions) (*storage.Reader, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolveGetOptions(options)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, err)
	}
	stored, err := s.lookup(key)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, err)
	}
	offset, length, err := resolved.Range(stored.object.Size)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, err)
	}
	body := io.NopCloser(bytes.NewReader(stored.content[offset : offset+length]))
	return storage.NewReader(cloneObject(&stored.object), offset, length, body), nil
}

// Stat returns the object's attributes.
func (s *Store) Stat(_ context.Context, key string) (*storage.Object, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	stored, err := s.lookup(key)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", key, err)
	}
	return cloneObject(&stored.object), nil
}

func (s *Store) lookup(key string) (*entry, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	stored, found := s.objects[key]
	if !found {
		return nil, storage.ErrNotFound
	}
	return stored, nil
}

// Delete removes the object.
func (s *Store) Delete(_ context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	s.mutex.Lock()
	delete(s.objects, key)
	s.mutex.Unlock()
	return nil
}

// List sorts the matching keys on every call, so it costs O(n log n) in the number of objects.
func (s *Store) List(_ context.Context, options *storage.ListOptions) (*storage.Page, error) {
	resolved, err := storage.ResolveListOptions(options)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, resolved.Prefix) && key > resolved.StartAfter {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	page := &storage.Page{}
	if len(keys) > resolved.Limit {
		keys = keys[:resolved.Limit]
		page.Next = keys[len(keys)-1]
	}
	page.Objects = make([]storage.Object, 0, len(keys))
	for _, key := range keys {
		object := s.objects[key].object
		page.Objects = append(page.Objects, storage.Object{Key: key, Size: object.Size, ModifiedAt: object.ModifiedAt})
	}
	return page, nil
}

// Copy shares the source's content with the copy; stored content is never modified in place.
func (s *Store) Copy(ctx context.Context, source, destination string) (*storage.Object, error) {
	if err := storage.ValidateKey(source); err != nil {
		return nil, err
	}
	if err := storage.ValidateKey(destination); err != nil {
		return nil, err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("copy %q to %q: %w", source, destination, err)
	}
	stored, found := s.objects[source]
	if !found {
		return nil, fmt.Errorf("copy %q to %q: %w", source, destination, storage.ErrNotFound)
	}
	copied := &entry{content: stored.content, object: stored.object}
	copied.object.Key = destination
	copied.object.ModifiedAt = time.Now()
	s.objects[destination] = copied
	return cloneObject(&copied.object), nil
}

// cloneObject returns a copy the caller may change without touching the stored object.
func cloneObject(object *storage.Object) *storage.Object {
	clone := *object
	clone.Metadata = maps.Clone(object.Metadata)
	return &clone
}
