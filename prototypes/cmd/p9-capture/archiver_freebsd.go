//go:build freebsd

package main

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// archiver_freebsd.go holds the only FreeBSD-specific calls the round-trip archiver (test 3,
// archiver.go) makes. It is deliberately independent of walk.go/flags.go: design 9.4's round
// trip must not share code with the L0 walker it is checked against, so a bug in one cannot
// silently cancel out against the other.
//
// UNVERIFIED: the extattr list wire format (a sequence of 1-byte-length-prefixed names) and the
// exact EXTATTR_NAMESPACE_USER value are from FreeBSD documentation memory, not a run.

const archExtattrNamespace = unix.EXTATTR_NAMESPACE_USER

func archGetFlags(path string) (uint32, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return 0, err
	}
	return st.Flags, nil
}

func archSetFlags(path string, flags uint32) error {
	if flags == 0 {
		return nil
	}
	return unix.Chflags(path, int(flags))
}

func archListXattr(path string) ([]string, error) {
	buf := make([]byte, 4096)
	n, err := unix.ExtattrListFile(path, archExtattrNamespace, uintptr(unsafe.Pointer(&buf[0])), len(buf))
	if err != nil {
		return nil, err
	}
	var names []string
	b := buf[:n]
	for len(b) > 0 {
		l := int(b[0])
		if l == 0 || l+1 > len(b) {
			break
		}
		names = append(names, string(b[1:1+l]))
		b = b[1+l:]
	}
	return names, nil
}

func archGetXattr(path, name string) ([]byte, error) {
	buf := make([]byte, 65536)
	n, err := unix.ExtattrGetFile(path, archExtattrNamespace, name, uintptr(unsafe.Pointer(&buf[0])), len(buf))
	if err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return out, nil
}

func archSetXattr(path, name string, value []byte) error {
	if len(value) == 0 {
		var zero byte
		_, err := unix.ExtattrSetFile(path, archExtattrNamespace, name, uintptr(unsafe.Pointer(&zero)), 0)
		return err
	}
	_, err := unix.ExtattrSetFile(path, archExtattrNamespace, name, uintptr(unsafe.Pointer(&value[0])), len(value))
	return err
}

func archLutimes(path string, mtimeNs int64) error {
	ts := []unix.Timespec{unix.NsecToTimespec(mtimeNs), unix.NsecToTimespec(mtimeNs)}
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, unix.AT_SYMLINK_NOFOLLOW)
}

func archLchown(path string, uid, gid int) error { return unix.Lchown(path, uid, gid) }
func archSymlink(target, path string) error      { return unix.Symlink(target, path) }
