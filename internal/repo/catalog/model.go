package catalog

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// Kind of catalog entry. The zero value is the end-of-directory marker in the
// encoding.
type Kind byte

// Entry kinds.
const (
	KDir Kind = iota + 1
	KFile
	KSymlink
	KHardlink
	KChar
	KBlock
	KFifo
)

func (k Kind) String() string {
	switch k {
	case KDir:
		return "dir"
	case KFile:
		return "file"
	case KSymlink:
		return "symlink"
	case KHardlink:
		return "hardlink"
	case KChar:
		return "char"
	case KBlock:
		return "block"
	case KFifo:
		return "fifo"
	}
	return "?"
}

// Attr is one extended attribute, ACL or other platform metadata blob, opaque
// to the catalog: a name that says what it is and the bytes. The platform's
// Attrs implementation chooses the names (see Attrs) and is the only code that
// interprets them. Entries hold them sorted by Name.
type Attr struct {
	Name  string
	Value []byte
}

// Entry is one node of the tree.
type Entry struct {
	Kind    Kind
	Name    string // base name; "" for the root
	Mode    uint32 // permission bits and setuid/setgid/sticky
	UID     uint32
	GID     uint32
	MtimeNs int64
	CtimeNs int64
	Inode   uint64
	// Flags are the file flags (chflags(2)); 0 where the platform has none.
	Flags  uint32
	Size   uint64   // regular files
	PayOff uint64   // regular files with Size > 0: offset of the content in the payload
	Sum    [32]byte // regular files with Size > 0: SHA-256 of the content
	// Link is the symlink target; for a hard link it is the path, relative to
	// the root, of the first link, which is the file's identity.
	Link  string
	Major uint32 // char and block devices
	Minor uint32
	Attrs []Attr // sorted by name

	Children []*Entry // directories, sorted by Name
}

// Tree is a whole catalog.
type Tree struct {
	Root *Entry
	// Count is the number of entries including the root.
	Count int
}

// CleanPath normalises a user supplied path ("/a//b/../c", "a/b/") to "a/b"
// form relative to the root; "" and "/" mean the root. It rejects escapes above
// the root.
func CleanPath(p string) (string, error) {
	var parts []string
	for _, c := range strings.Split(p, "/") {
		switch c {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return "", fmt.Errorf("path %q escapes the root", p)
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "/"), nil
}

func (x *Entry) child(name string) *Entry {
	i := sort.Search(len(x.Children), func(i int) bool { return x.Children[i].Name >= name })
	if i < len(x.Children) && x.Children[i].Name == name {
		return x.Children[i]
	}
	return nil
}

// Lookup finds an entry by path ("" = root). Symlinks are not followed.
func (t *Tree) Lookup(p string) (*Entry, error) {
	c, err := CleanPath(p)
	if err != nil {
		return nil, err
	}
	cur := t.Root
	if c == "" {
		return cur, nil
	}
	for _, part := range strings.Split(c, "/") {
		if cur.Kind != KDir {
			return nil, fmt.Errorf("%s: not a directory", p)
		}
		if cur = cur.child(part); cur == nil {
			return nil, fmt.Errorf("%s: no such file or directory", p)
		}
	}
	return cur, nil
}

// Walk visits every entry in pre-order (the order of the payload) with its
// full path ("" for the root). Returning false from fn skips the entry's
// children.
func (t *Tree) Walk(fn func(path string, e *Entry) bool) {
	var rec func(p string, e *Entry)
	rec = func(p string, e *Entry) {
		if !fn(p, e) {
			return
		}
		for _, c := range e.Children {
			cp := c.Name
			if p != "" {
				cp = p + "/" + c.Name
			}
			rec(cp, c)
		}
	}
	rec("", t.Root)
}

// Stats summarises a tree.
type Stats struct {
	Files, Dirs, Symlinks, Hardlinks, Special int
	Bytes                                     uint64
}

// Stats counts the entries.
func (t *Tree) Stats() Stats {
	var s Stats
	t.Walk(func(_ string, e *Entry) bool {
		switch e.Kind {
		case KDir:
			s.Dirs++
		case KFile:
			s.Files++
			s.Bytes += e.Size
		case KSymlink:
			s.Symlinks++
		case KHardlink:
			s.Hardlinks++
		default:
			s.Special++
		}
		return true
	})
	return s
}

func attrsEqual(a, b []Attr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}
