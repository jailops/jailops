//go:build linux

package catalog

import (
	"errors"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// DefaultAttrs returns the extended attribute implementation of this platform.
func DefaultAttrs() Attrs { return linuxAttrs{} }

const linuxPrefix = "linux:xattr:"

type linuxAttrs struct{}

func (linuxAttrs) Name() string { return "linux" }

var xattrBufs = &sync.Pool{New: func() any { b := make([]byte, 4096); return &b }}

// Collect reads every extended attribute of path (not following symlinks).
func (linuxAttrs) Collect(path string) ([]Attr, error) {
	bp := xattrBufs.Get().(*[]byte)
	defer xattrBufs.Put(bp)
	var names []byte
	for {
		sz, err := unix.Llistxattr(path, *bp)
		if err == nil {
			names = (*bp)[:sz]
			break
		}
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOSYS) {
			return nil, nil
		}
		if !errors.Is(err, unix.ERANGE) {
			return nil, err
		}
		need, err := unix.Llistxattr(path, nil)
		if err != nil {
			return nil, err
		}
		*bp = make([]byte, max(need, 2*len(*bp)))
	}
	if len(names) == 0 {
		return nil, nil
	}
	var list []string
	start := 0
	for i := 0; i < len(names); i++ {
		if names[i] != 0 {
			continue
		}
		if i > start {
			list = append(list, string(names[start:i]))
		}
		start = i + 1
	}
	vp := xattrBufs.Get().(*[]byte)
	defer xattrBufs.Put(vp)
	var out []Attr
	for _, name := range list {
		var val []byte
		for {
			vsz, err := unix.Lgetxattr(path, name, *vp)
			if err == nil {
				val = append([]byte{}, (*vp)[:vsz]...)
				break
			}
			if errors.Is(err, unix.ENODATA) {
				break // removed since the list was read
			}
			if !errors.Is(err, unix.ERANGE) {
				return nil, err
			}
			need, err := unix.Lgetxattr(path, name, nil)
			if err != nil {
				if errors.Is(err, unix.ENODATA) {
					break
				}
				return nil, err
			}
			*vp = make([]byte, max(need, 2*len(*vp)))
		}
		if val == nil {
			continue
		}
		out = append(out, Attr{Name: linuxPrefix + name, Value: val})
	}
	sortAttrs(out)
	return out, nil
}

func tolerable(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP)
}

// lsetxattr is a seam for tests.
var lsetxattr = unix.Lsetxattr

// Apply sets the attributes. security.* and trusted.* are never applied when
// ownership is not restored.
func (linuxAttrs) Apply(path string, attrs []Attr, o ApplyOptions) ([]SkippedAttr, error) {
	var skipped []SkippedAttr
	for _, a := range attrs {
		if !strings.HasPrefix(a.Name, linuxPrefix) {
			skipped = append(skipped, SkippedAttr{a.Name, "not a Linux attribute"})
			continue
		}
		name := strings.TrimPrefix(a.Name, linuxPrefix)
		if o.NoChown && (strings.HasPrefix(name, "security.") || strings.HasPrefix(name, "trusted.")) {
			skipped = append(skipped, SkippedAttr{a.Name, "ownership is not restored"})
			continue
		}
		if err := lsetxattr(path, name, a.Value, 0); err != nil {
			switch {
			case errors.Is(err, unix.ENOTSUP):
				skipped = append(skipped, SkippedAttr{a.Name, "the destination filesystem has no extended attributes"})
				continue
			case errors.Is(err, unix.EPERM) && o.NoChown:
				skipped = append(skipped, SkippedAttr{a.Name, "not permitted without privileges"})
				continue
			case o.Lenient && tolerable(err):
				skipped = append(skipped, SkippedAttr{a.Name, err.Error()})
				continue
			}
			return skipped, err
		}
	}
	return skipped, nil
}
