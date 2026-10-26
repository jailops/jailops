//go:build linux

package priv

import (
	"bytes"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

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
		UID: st.Uid, GID: st.Gid, Atime: time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)),
	}, true
}

// setLinkTimes sets the times of the symlink name inside dir without
// following it.
func setLinkTimes(dir *os.File, name string, atime, mtime time.Time) error {
	ts := []unix.Timespec{unix.NsecToTimespec(atime.UnixNano()), unix.NsecToTimespec(mtime.UnixNano())}
	return unix.UtimesNanoAt(int(dir.Fd()), name, ts, unix.AT_SYMLINK_NOFOLLOW)
}

const maxXattrValue = 64 << 10

// copyXattrs copies extended attributes from src to dst and returns how many
// it could not copy (security.* and trusted.* need privilege).
func copyXattrs(src, dst *os.File) (skipped int) {
	sfd, dfd := int(src.Fd()), int(dst.Fd())
	n, err := unix.Flistxattr(sfd, nil)
	if err != nil || n <= 0 {
		return 0
	}
	buf := make([]byte, n)
	if n, err = unix.Flistxattr(sfd, buf); err != nil {
		return 1
	}
	for _, name := range bytes.Split(buf[:n], []byte{0}) {
		if len(name) == 0 {
			continue
		}
		key := string(name)
		sz, err := unix.Fgetxattr(sfd, key, nil)
		if err != nil || sz > maxXattrValue {
			skipped++
			continue
		}
		val := make([]byte, sz)
		if sz > 0 {
			if sz, err = unix.Fgetxattr(sfd, key, val); err != nil {
				skipped++
				continue
			}
		}
		if err := unix.Fsetxattr(dfd, key, val[:sz], 0); err != nil {
			skipped++
		}
	}
	return skipped
}

// copyFlags: Linux has no BSD file flags.
func copyFlags(*os.File, uint32) error { return nil }
