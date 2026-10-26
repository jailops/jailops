//go:build freebsd

package catalog

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// UNVERIFIED: stat(2) fields, mknod(2) and chflags(2) have been checked only by
// a GOOS=freebsd cross build, not run on FreeBSD (design section 16, item 11).

func fromStat(st *unix.Stat_t) statInfo {
	return statInfo{
		Type: uint32(st.Mode) & unix.S_IFMT, Perm: uint32(st.Mode) & 0o7777,
		UID: st.Uid, GID: st.Gid, Size: st.Size,
		Mtime: unix.TimespecToNsec(st.Mtim), Ctime: unix.TimespecToNsec(st.Ctim),
		Ino: st.Ino, Dev: st.Dev, Nlink: st.Nlink, Rdev: st.Rdev, Flags: st.Flags,
	}
}

func mknod(path string, mode uint32, major, minor uint32) error {
	return unix.Mknod(path, mode, unix.Mkdev(major, minor))
}

// setFlags applies chflags(2) flags: chflags for files and directories,
// lchflags for symlinks (the flags of the link itself).
func setFlags(path string, flags uint32, link bool) (applied bool, err error) {
	if flags == 0 {
		return true, nil
	}
	if !link {
		return true, unix.Chflags(path, int(flags))
	}
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return false, err
	}
	if _, _, e := syscall.Syscall(unix.SYS_LCHFLAGS, uintptr(unsafe.Pointer(p)), uintptr(flags), 0); e != 0 {
		return true, e
	}
	return true, nil
}
