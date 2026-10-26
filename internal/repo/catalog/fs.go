package catalog

import (
	"io"
	"os"
)

// ScanFS is the file tree a scan reads. The local implementation reads the local file system; the
// agent implementation (internal/fsagent) reads a tree through a process that holds the privileges
// to read it, for a caller that does not.
//
// Paths are the ones StatEntry and ReadNames are given: the root first, then what the same
// implementation returned names for.
type ScanFS interface {
	// StatEntry reads everything the catalog stores about one entry (including extended
	// attributes, ACLs and a symlink's target), and the link count and device that identify hard
	// links. A socket is ErrSocket. It never follows symlinks.
	StatEntry(path, name string) (e *Entry, nlink, dev uint64, err error)
	// ReadNames lists a directory (unsorted, without . and ..).
	ReadNames(path string) ([]string, error)
	// Open opens a regular file for reading.
	Open(path string) (io.ReadCloser, error)
}

// LocalFS reads the local file system; attrs collects extended attributes (nil = the platform's).
func LocalFS(attrs Attrs) ScanFS {
	if attrs == nil {
		attrs = DefaultAttrs()
	}
	return localFS{attrs}
}

type localFS struct{ attrs Attrs }

func (l localFS) StatEntry(path, name string) (*Entry, uint64, uint64, error) {
	return statEntry(path, name, l.attrs)
}

func (localFS) ReadNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

func (localFS) Open(path string) (io.ReadCloser, error) { return os.Open(path) }
