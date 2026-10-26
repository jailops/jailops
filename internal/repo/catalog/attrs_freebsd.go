//go:build freebsd

package catalog

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// UNVERIFIED: everything in this file has been checked only by a GOOS=freebsd
// cross build. It has not run on FreeBSD: the extattr_*(2) calls on user and
// system namespaces and the NFSv4 ACL calls on ZFS (design section 16, item 11).
// Until a FreeBSD host settles it, a backup of a jail that has attributes or
// ACLs must be treated as not proven to round-trip them.

// DefaultAttrs returns the extended attribute implementation of this platform.
func DefaultAttrs() Attrs { return freebsdAttrs{} }

const (
	bsdUser   = "freebsd:extattr:user:"
	bsdSystem = "freebsd:extattr:system:"
	bsdNFS4   = "freebsd:acl:nfs4"

	aclTypeNFS4 = 0x4 // ACL_TYPE_NFS4
	// struct acl: acl_maxcnt (uint32), acl_cnt (int32), acl_spare[4] (int32), then
	// ACL_MAX_ENTRIES (254) entries of 16 bytes.
	aclStructSize = 4 + 4 + 16 + 254*16
)

type freebsdAttrs struct{}

func (freebsdAttrs) Name() string { return "freebsd" }

func listNS(path string, ns int) ([]string, error) {
	n, err := unix.ExtattrListLink(path, ns, 0, 0)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	for {
		buf := make([]byte, n)
		got, err := unix.ExtattrListLink(path, ns, uintptr(unsafe.Pointer(&buf[0])), len(buf))
		if err != nil {
			return nil, err
		}
		if got > len(buf) {
			n = got
			continue
		}
		// the list is a sequence of (length byte, name) pairs
		var out []string
		for i := 0; i < got; {
			l := int(buf[i])
			if i+1+l > got {
				return nil, errors.New("malformed extattr list")
			}
			out = append(out, string(buf[i+1:i+1+l]))
			i += 1 + l
		}
		return out, nil
	}
}

func getNS(path string, ns int, name string) ([]byte, error) {
	n, err := unix.ExtattrGetLink(path, ns, name, 0, 0)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, nil
	}
	buf := make([]byte, n+1)
	got, err := unix.ExtattrGetLink(path, ns, name, uintptr(unsafe.Pointer(&buf[0])), len(buf))
	if err != nil {
		return nil, err
	}
	return buf[:got], nil
}

func unsupportedFS(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOATTR)
}

// Collect reads the extended attributes of both namespaces and the NFSv4 ACL.
func (freebsdAttrs) Collect(path string) ([]Attr, error) {
	var out []Attr
	for _, ns := range []struct {
		id     int
		prefix string
	}{{unix.EXTATTR_NAMESPACE_USER, bsdUser}, {unix.EXTATTR_NAMESPACE_SYSTEM, bsdSystem}} {
		names, err := listNS(path, ns.id)
		if err != nil {
			if unsupportedFS(err) || errors.Is(err, unix.EPERM) && ns.id == unix.EXTATTR_NAMESPACE_SYSTEM {
				continue
			}
			return nil, fmt.Errorf("listing extended attributes of %s: %w", path, err)
		}
		for _, name := range names {
			v, err := getNS(path, ns.id, name)
			if err != nil {
				if errors.Is(err, unix.ENOATTR) {
					continue
				}
				return nil, fmt.Errorf("reading extended attribute %s of %s: %w", name, path, err)
			}
			out = append(out, Attr{Name: ns.prefix + name, Value: v})
		}
	}
	if acl, err := getNFS4(path); err != nil {
		return nil, err
	} else if acl != nil {
		out = append(out, Attr{Name: bsdNFS4, Value: acl})
	}
	sortAttrs(out)
	return out, nil
}

func getNFS4(path string) ([]byte, error) {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, aclStructSize)
	_, _, e := syscall.Syscall(unix.SYS___ACL_GET_LINK, uintptr(unsafe.Pointer(p)), aclTypeNFS4, uintptr(unsafe.Pointer(&buf[0])))
	if e != 0 {
		if e == unix.EINVAL || e == unix.EOPNOTSUPP || e == unix.ENOENT {
			return nil, nil // a filesystem without NFSv4 ACLs
		}
		return nil, fmt.Errorf("reading the NFSv4 ACL of %s: %w", path, e)
	}
	return buf, nil
}

func setNFS4(path string, acl []byte) error {
	if len(acl) != aclStructSize {
		return errors.New("NFSv4 ACL blob has the wrong size")
	}
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	buf := append([]byte(nil), acl...)
	if _, _, e := syscall.Syscall(unix.SYS___ACL_SET_LINK, uintptr(unsafe.Pointer(p)), aclTypeNFS4, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return e
	}
	return nil
}

// Apply sets extended attributes and the NFSv4 ACL.
func (freebsdAttrs) Apply(path string, attrs []Attr, o ApplyOptions) ([]SkippedAttr, error) {
	var skipped []SkippedAttr
	for _, a := range attrs {
		var err error
		switch {
		case strings.HasPrefix(a.Name, bsdUser), strings.HasPrefix(a.Name, bsdSystem):
			ns, name := unix.EXTATTR_NAMESPACE_USER, strings.TrimPrefix(a.Name, bsdUser)
			if strings.HasPrefix(a.Name, bsdSystem) {
				ns, name = unix.EXTATTR_NAMESPACE_SYSTEM, strings.TrimPrefix(a.Name, bsdSystem)
				if o.NoChown {
					skipped = append(skipped, SkippedAttr{a.Name, "ownership is not restored"})
					continue
				}
			}
			var p unsafe.Pointer
			if len(a.Value) > 0 {
				p = unsafe.Pointer(&a.Value[0])
			}
			_, err = unix.ExtattrSetLink(path, ns, name, uintptr(p), len(a.Value))
		case a.Name == bsdNFS4:
			if o.NoChown {
				skipped = append(skipped, SkippedAttr{a.Name, "ownership is not restored"})
				continue
			}
			err = setNFS4(path, a.Value)
		default:
			skipped = append(skipped, SkippedAttr{a.Name, "not a FreeBSD attribute"})
			continue
		}
		if err != nil {
			if unsupportedFS(err) || (o.Lenient && (errors.Is(err, unix.EPERM))) {
				skipped = append(skipped, SkippedAttr{a.Name, err.Error()})
				continue
			}
			return skipped, fmt.Errorf("setting %s on %s: %w", a.Name, path, err)
		}
	}
	return skipped, nil
}
