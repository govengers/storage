package filesystem

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/govengers/storage"
)

// List walks the directories below the prefix in key order and stops after the page. Keys
// order bytewise, a directory walk orders names, and the two differ ("a-c" < "a/b" but
// "a" < "a-c"): sorting each directory's entries with a slash after directory names restores
// key order, because every key below a directory starts with its name and a slash.
func (s *Store) List(ctx context.Context, options *storage.ListOptions) (*storage.Page, error) {
	resolved, err := storage.ResolveListOptions(options)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	directory, namePrefix := splitPrefix(resolved.Prefix)
	walker := &lister{store: s, startAfter: resolved.StartAfter, want: resolved.Limit + 1} // one more tells whether a next page exists
	if err := walker.walk(ctx, directory, namePrefix); err != nil {
		return nil, fmt.Errorf("list %q: %w", resolved.Prefix, err)
	}
	page := &storage.Page{Objects: walker.objects}
	if len(page.Objects) > resolved.Limit {
		page.Objects = page.Objects[:resolved.Limit]
		page.Next = page.Objects[resolved.Limit-1].Key
	}
	return page, nil
}

// splitPrefix splits a prefix into the directory every match lies in and the start of the
// names in it: "docs/a" is "a..." in "docs", "docs/" is everything in "docs".
func splitPrefix(prefix string) (directory, namePrefix string) {
	index := strings.LastIndex(prefix, "/")
	if index < 0 {
		return "", prefix
	}
	return prefix[:index], prefix[index+1:]
}

type lister struct {
	store      *Store
	startAfter string
	want       int
	objects    []storage.Object
}

type listEntry struct {
	name      string
	directory bool
	sortKey   string
}

func (l *lister) done() bool {
	return len(l.objects) >= l.want
}

// walk appends the objects below directory (a key prefix without its trailing slash, empty for
// the top) whose names start with namePrefix.
func (l *lister) walk(ctx context.Context, directory, namePrefix string) error {
	entries, err := l.readDirectory(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.done() {
			return nil
		}
		if !strings.HasPrefix(entry.name, namePrefix) {
			continue
		}
		if err := l.visit(ctx, path.Join(directory, entry.name), entry.directory); err != nil {
			return err
		}
	}
	return nil
}

// visit descends into a directory unless all its keys lie before the page, or appends a file's
// object if it lies after startAfter.
func (l *lister) visit(ctx context.Context, key string, directory bool) error {
	switch {
	case directory && !l.subtreeSkipped(key+"/"):
		return l.walk(ctx, key, "")
	case !directory && key > l.startAfter:
		return l.appendObject(key)
	default:
		return nil
	}
}

// subtreeSkipped reports whether every key below subtreePrefix sorts before or at startAfter.
func (l *lister) subtreeSkipped(subtreePrefix string) bool {
	return l.startAfter > subtreePrefix && !strings.HasPrefix(l.startAfter, subtreePrefix)
}

// readDirectory returns the entries of a directory in key order, named by key segment; a
// missing directory, or a file where the prefix expects one, has none. Entries other than
// directories and files with the store's suffixes and a valid segment as name are left out:
// symlinks, a .DS_Store, files a person put there.
func (l *lister) readDirectory(directory string) ([]listEntry, error) {
	handle, err := l.store.root.Open(directoryPath(directory))
	if err != nil {
		return nil, ignoreNotExist(notFound(err))
	}
	dirEntries, err := handle.ReadDir(-1)
	err = errors.Join(err, handle.Close())
	if err != nil {
		return nil, ignoreNotExist(notFound(err))
	}
	entries := make([]listEntry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		isDirectory := dirEntry.IsDir()
		suffix := objectSuffix
		if isDirectory {
			suffix = directorySuffix
		}
		name, found := strings.CutSuffix(dirEntry.Name(), suffix)
		if !found || (!isDirectory && !dirEntry.Type().IsRegular()) || storage.ValidateKey(name) != nil {
			continue
		}
		sortKey := name
		if isDirectory {
			sortKey += "/"
		}
		entries = append(entries, listEntry{name: name, directory: isDirectory, sortKey: sortKey})
	}
	slices.SortFunc(entries, func(left, right listEntry) int { return cmp.Compare(left.sortKey, right.sortKey) })
	return entries, nil
}

// appendObject adds the object's key, size and modification time; one deleted since the
// directory was read is left out.
func (l *lister) appendObject(key string) error {
	object, err := l.store.stat(key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("object %q: %w", key, err)
	}
	l.objects = append(l.objects, storage.Object{Key: key, Size: object.Size, ModifiedAt: object.ModifiedAt})
	return nil
}
