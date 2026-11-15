// Package filesystem is a storage.Store in a directory of a local or mounted disk.
//
// Every object is one file below <Root>/objects, at the path of its key, holding a small header
// (content type and metadata) in front of the content; the files are no plain copies of the
// content. A Put writes to <Root>/tmp, flushes the file to disk and renames it into place, so
// readers see the old or the new object, never a partial one. All file access goes through an
// os.Root, which confines it to Root even when the directory holds symlinks.
//
// Limits: a key cannot name a file and a directory at once, so "a" and "a/b" cannot both be
// stored (the second Put fails with storage.ErrInvalid); on a case-insensitive filesystem keys
// that differ only in case are one object; List reads one header per listed object. Several
// processes may share Root on one host or a POSIX shared volume; files a crashed Put left in
// <Root>/tmp are not removed automatically.
package filesystem

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"syscall"
	"time"

	"github.com/govengers/storage"
)

const (
	objectsDirectory    = "objects"
	temporaryDirectory  = "tmp"
	filePermission      = 0o600
	directoryPermission = 0o750
	// publishAttempts bounds the retries of a rename whose directory a concurrent Delete pruned.
	publishAttempts = 3
)

// Options configures a Store.
type Options struct {
	// Root is an existing directory the Store owns; required. New creates the subdirectories
	// objects and tmp in it.
	Root string
}

// Store is a storage.Store on a filesystem. It is safe for concurrent use. Close releases
// the directory.
type Store struct {
	root *os.Root
}

var _ storage.Store = (*Store)(nil)

// New opens options.Root and creates its objects and tmp directories.
func New(options *Options) (*Store, error) {
	if options == nil || options.Root == "" {
		return nil, errors.New("filesystem: Options.Root is required")
	}
	root, err := os.OpenRoot(options.Root)
	if err != nil {
		return nil, fmt.Errorf("filesystem: open the root %s: %w", options.Root, err)
	}
	for _, directory := range []string{objectsDirectory, temporaryDirectory} {
		if err := root.MkdirAll(directory, directoryPermission); err != nil {
			return nil, errors.Join(fmt.Errorf("filesystem: create %s in %s: %w", directory, options.Root, err), root.Close())
		}
	}
	return &Store{root: root}, nil
}

// Close releases the root directory; the Store is unusable afterwards.
func (s *Store) Close() error {
	return s.root.Close()
}

// Put writes body to a temporary file, flushes it and renames it to the key's path.
func (s *Store) Put(ctx context.Context, key string, body io.Reader, options *storage.PutOptions) (*storage.Object, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolvePutOptions(options)
	if err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	temporaryName, written, err := s.writeTemporary(ctx, body, resolved)
	if err != nil {
		return nil, fmt.Errorf("put %q: %w", key, err)
	}
	if err := s.publish(temporaryName, objectPath(key)); err != nil {
		return nil, errors.Join(fmt.Errorf("put %q: %w", key, err), ignoreNotExist(s.root.Remove(temporaryName)))
	}
	return &storage.Object{
		Key: key, Size: written.size, ContentType: resolved.ContentType, Metadata: maps.Clone(resolved.Metadata), ModifiedAt: written.modifiedAt,
	}, nil
}

// written describes a flushed temporary file; the rename keeps its modification time.
type written struct {
	size       int64
	modifiedAt time.Time
}

// writeTemporary writes the header and body to a new file in the tmp directory, flushed to
// disk, and returns its name; on failure it removes the file.
func (s *Store) writeTemporary(ctx context.Context, body io.Reader, options storage.PutOptions) (_ string, _ written, err error) {
	name := path.Join(temporaryDirectory, rand.Text())
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePermission)
	if err != nil {
		return "", written{}, fmt.Errorf("create a temporary file: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, ignoreNotExist(s.root.Remove(name)))
		}
	}()
	result, err := writeContent(ctx, file, body, options)
	if err != nil {
		return "", written{}, errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return "", written{}, fmt.Errorf("close the temporary file: %w", err)
	}
	return name, result, nil
}

func writeContent(ctx context.Context, file *os.File, body io.Reader, options storage.PutOptions) (written, error) {
	if err := writeHeader(file, &header{ContentType: options.ContentType, Metadata: options.Metadata}); err != nil {
		return written{}, err
	}
	reader := storage.ContextReader(ctx, body)
	if options.MaximumSize > 0 {
		reader = storage.LimitReader(reader, options.MaximumSize)
	}
	size, err := io.Copy(file, reader)
	if err != nil {
		return written{}, fmt.Errorf("write the content: %w", err)
	}
	if err := file.Sync(); err != nil {
		return written{}, fmt.Errorf("flush the content: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return written{}, fmt.Errorf("stat the temporary file: %w", err)
	}
	return written{size: size, modifiedAt: info.ModTime()}, nil
}

// publish renames the temporary file to target, creating its directories, and flushes the
// directory entry. A concurrent Delete may prune the directory between the two steps, so a
// missing directory is retried.
func (s *Store) publish(temporaryName, target string) error {
	directory := path.Dir(target)
	for attempt := 1; ; attempt++ {
		if err := s.root.MkdirAll(directory, directoryPermission); err != nil {
			if isPathConflict(err) {
				return fmt.Errorf("a parent of the key is an object: %w", storage.ErrInvalid)
			}
			return fmt.Errorf("create the directory: %w", err)
		}
		err := s.root.Rename(temporaryName, target)
		switch {
		case err == nil:
			return s.syncDirectory(directory)
		case errors.Is(err, syscall.EISDIR) || errors.Is(err, fs.ErrExist):
			return fmt.Errorf("the key is a prefix of other keys: %w", storage.ErrInvalid)
		case errors.Is(err, fs.ErrNotExist) && attempt < publishAttempts:
			continue
		default:
			return fmt.Errorf("rename into place: %w", err)
		}
	}
}

func (s *Store) syncDirectory(directory string) error {
	handle, err := s.root.Open(directory)
	if err != nil {
		return fmt.Errorf("open the directory to flush it: %w", err)
	}
	return errors.Join(handle.Sync(), handle.Close())
}

// Get opens the object's file and positions it at the start of the range.
func (s *Store) Get(_ context.Context, key string, options *storage.GetOptions) (*storage.Reader, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	resolved, err := storage.ResolveGetOptions(options)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, err)
	}
	file, object, contentStart, err := s.open(key)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", key, err)
	}
	offset, length, err := resolved.Range(object.Size)
	if err == nil {
		_, err = file.Seek(contentStart+offset, io.SeekStart)
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("get %q: %w", key, err), file.Close())
	}
	body := &rangeBody{Reader: io.LimitReader(file, length), Closer: file}
	return storage.NewReader(object, offset, length, body), nil
}

type rangeBody struct {
	io.Reader
	io.Closer
}

// Stat reads the object's header and file attributes.
func (s *Store) Stat(_ context.Context, key string) (*storage.Object, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	file, object, _, err := s.open(key)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", key, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("stat %q: %w", key, err)
	}
	return object, nil
}

// open opens the object's file and reads its header; the caller closes the file. It returns
// the offset of the content in the file.
func (s *Store) open(key string) (file *os.File, object *storage.Object, contentStart int64, err error) {
	file, err = s.root.Open(objectPath(key))
	if err != nil {
		return nil, nil, 0, notFound(err)
	}
	object, contentStart, err = readObject(file, key)
	if err != nil {
		return nil, nil, 0, errors.Join(err, file.Close())
	}
	return file, object, contentStart, nil
}

func readObject(file *os.File, key string) (*storage.Object, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat the file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, storage.ErrNotFound // a directory: the key is a prefix of other keys
	}
	stored, contentStart, err := readHeader(file)
	if err != nil {
		return nil, 0, err
	}
	return &storage.Object{
		Key: key, Size: info.Size() - contentStart, ContentType: stored.ContentType, Metadata: stored.Metadata, ModifiedAt: info.ModTime(),
	}, contentStart, nil
}

// Delete removes the object's file and the directories it leaves empty.
func (s *Store) Delete(_ context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	target := objectPath(key)
	info, err := s.root.Lstat(target)
	if err != nil {
		return ignoreNotExist(notFound(err))
	}
	if !info.Mode().IsRegular() {
		return nil // a directory: no object is stored under the key
	}
	if err := s.root.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete %q: %w", key, err)
	}
	s.pruneDirectories(path.Dir(target))
	return nil
}

// pruneDirectories removes directory and its parents up to the objects directory while they
// are empty, so List does not walk them and their names are free for objects again. It stops
// at the first directory that is not empty, the usual case, which is no error.
func (s *Store) pruneDirectories(directory string) {
	for ; directory != objectsDirectory; directory = path.Dir(directory) {
		if s.root.Remove(directory) != nil {
			return
		}
	}
}

func objectPath(key string) string {
	return objectsDirectory + "/" + key
}

// notFound maps the errors of a path that does not lead to a file to storage.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return storage.ErrNotFound
	}
	return err
}

func ignoreNotExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

func isPathConflict(err error) bool {
	return errors.Is(err, syscall.ENOTDIR) || errors.Is(err, fs.ErrExist)
}
