//go:build freebsd

package priv

import (
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// UNVERIFIED: everything in this file has been compiled for FreeBSD but not
// run there.

const platformHasStat = true

// fileStat is the part of struct stat the copy needs.
type fileStat struct {
	Dev, Ino, Nlink uint64
	UID, GID        uint32
	Atime           time.Time
	Flags           uint32
}

func (s fileStat) id() fileID { return fileID{s.Dev, s.Ino} }

func statOf(fi os.FileInfo) (fileStat, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{}, false
	}
	return fileStat{
		Dev: uint64(st.Dev), Ino: uint64(st.Ino), Nlink: uint64(st.Nlink),
		UID: st.Uid, GID: st.Gid, Atime: time.Unix(int64(st.Atimespec.Sec), int64(st.Atimespec.Nsec)),
		Flags: st.Flags,
	}, true
}

func setLinkTimes(dir *os.File, name string, atime, mtime time.Time) error {
	ts := []unix.Timespec{unix.NsecToTimespec(atime.UnixNano()), unix.NsecToTimespec(mtime.UnixNano())}
	return unix.UtimesNanoAt(int(dir.Fd()), name, ts, unix.AT_SYMLINK_NOFOLLOW)
}

const maxXattrValue = 64 << 10

// copyXattrs copies extended attributes of the user and system namespaces.
// The system namespace needs privilege; what cannot be copied is counted.
func copyXattrs(src, dst *os.File) (skipped int) {
	sfd, dfd := int(src.Fd()), int(dst.Fd())
	for _, ns := range []int{unix.EXTATTR_NAMESPACE_USER, unix.EXTATTR_NAMESPACE_SYSTEM} {
		n, err := unix.ExtattrListFd(sfd, ns, 0, 0)
		if err != nil || n <= 0 {
			continue
		}
		list := make([]byte, n)
		n, err = unix.ExtattrListFd(sfd, ns, uintptr(unsafe.Pointer(&list[0])), len(list))
		if err != nil {
			skipped++
			continue
		}
		// The list is a sequence of (length byte, name).
		for i := 0; i < n; {
			l := int(list[i])
			if i+1+l > n {
				skipped++
				break
			}
			name := string(list[i+1 : i+1+l])
			i += 1 + l
			sz, err := unix.ExtattrGetFd(sfd, ns, name, 0, 0)
			if err != nil || sz > maxXattrValue {
				skipped++
				continue
			}
			val := make([]byte, max(sz, 1))
			if sz > 0 {
				if sz, err = unix.ExtattrGetFd(sfd, ns, name, uintptr(unsafe.Pointer(&val[0])), sz); err != nil {
					skipped++
					continue
				}
			}
			if _, err := unix.ExtattrSetFd(dfd, ns, name, uintptr(unsafe.Pointer(&val[0])), sz); err != nil {
				skipped++
			}
			runtime.KeepAlive(val)
		}
		runtime.KeepAlive(list)
	}
	return skipped
}

// copyFlags sets the file flags (chflags(2)); it runs after everything else
// because an immutable flag blocks further changes.
func copyFlags(f *os.File, flags uint32) error { return unix.Fchflags(int(f.Fd()), int(flags)) }
