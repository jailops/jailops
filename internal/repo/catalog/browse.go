package catalog

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// ModeString renders the type and permission bits like ls -l ("drwxr-xr-x").
func (e *Entry) ModeString() string {
	t := map[Kind]byte{KDir: 'd', KFile: '-', KSymlink: 'l', KHardlink: '-', KChar: 'c', KBlock: 'b', KFifo: 'p'}[e.Kind]
	b := []byte("----------")
	b[0] = t
	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if e.Mode&(1<<(8-uint(i))) != 0 {
			b[i+1] = rwx[i]
		}
	}
	if e.Mode&0o4000 != 0 {
		b[3] = map[bool]byte{true: 's', false: 'S'}[b[3] == 'x']
	}
	if e.Mode&0o2000 != 0 {
		b[6] = map[bool]byte{true: 's', false: 'S'}[b[6] == 'x']
	}
	if e.Mode&0o1000 != 0 {
		b[9] = map[bool]byte{true: 't', false: 'T'}[b[9] == 'x']
	}
	return string(b)
}

// Line formats the entry for a long listing.
func (e *Entry) Line(name string) string {
	size := e.Size
	if e.Kind == KDir {
		size = 0
	}
	l := fmt.Sprintf("%s %5d %5d %12d %s %s", e.ModeString(), e.UID, e.GID, size, time.Unix(0, e.MtimeNs).UTC().Format("2006-01-02 15:04"), name)
	switch e.Kind {
	case KSymlink:
		l += " -> " + e.Link
	case KHardlink:
		l += " => " + e.Link
	}
	return l
}

// List returns the children of the directory at p (or the entry itself when it is not a directory).
func (t *Tree) List(p string) ([]*Entry, error) {
	e, err := t.Lookup(p)
	if err != nil {
		return nil, err
	}
	if e.Kind != KDir {
		return []*Entry{e}, nil
	}
	return e.Children, nil
}

// Find returns the paths whose base name matches the glob pattern (path.Match syntax), below
// the directory root ("" = everything), in tree order.
func (t *Tree) Find(root, pattern string) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	r, err := CleanPath(root)
	if err != nil {
		return nil, err
	}
	start, err := t.Lookup(r)
	if err != nil {
		return nil, err
	}
	var out []string
	sub := &Tree{Root: start}
	sub.Walk(func(rel string, e *Entry) bool {
		full := rel
		if r != "" {
			full = r
			if rel != "" {
				full = r + "/" + rel
			}
		}
		if rel != "" {
			if ok, _ := path.Match(pattern, e.Name); ok {
				out = append(out, full)
			}
		}
		return true
	})
	return out, nil
}

// Subtree reports the number of entries and total file bytes below path.
func (t *Tree) Subtree(p string) (entries int, bytes uint64, err error) {
	e, err := t.Lookup(p)
	if err != nil {
		return 0, 0, err
	}
	sub := &Tree{Root: e}
	sub.Walk(func(_ string, x *Entry) bool {
		entries++
		if x.Kind == KFile {
			bytes += x.Size
		}
		return true
	})
	return entries, bytes, nil
}

// Base returns the final component of a path.
func Base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
