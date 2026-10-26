//go:build linux || freebsd

package catalog

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// statInfo is what the catalog needs from lstat(2), the same on every platform.
type statInfo struct {
	Type  uint32 // unix.S_IF* bits
	Perm  uint32 // permission bits plus setuid, setgid, sticky
	UID   uint32
	GID   uint32
	Size  int64
	Mtime int64 // nanoseconds
	Ctime int64
	Ino   uint64
	Dev   uint64
	Nlink uint64
	Rdev  uint64
	Flags uint32 // chflags(2) flags, 0 where the platform has none
}

func lstat(path string) (statInfo, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return statInfo{}, err
	}
	return fromStat(&st), nil
}

var ErrSocket = errors.New("socket")

// kindOf maps the type bits to an entry kind. Sockets give ErrSocket: they
// cannot be archived or restored.
func kindOf(path string, si statInfo, e *Entry) error {
	switch si.Type {
	case unix.S_IFDIR:
		e.Kind = KDir
	case unix.S_IFLNK:
		e.Kind = KSymlink
		l, err := os.Readlink(path)
		if err != nil {
			return err
		}
		e.Link = l
	case unix.S_IFIFO:
		e.Kind = KFifo
	case unix.S_IFCHR:
		e.Kind, e.Major, e.Minor = KChar, unix.Major(si.Rdev), unix.Minor(si.Rdev)
	case unix.S_IFBLK:
		e.Kind, e.Major, e.Minor = KBlock, unix.Major(si.Rdev), unix.Minor(si.Rdev)
	case unix.S_IFSOCK:
		return ErrSocket
	case unix.S_IFREG:
		e.Kind, e.Size = KFile, uint64(si.Size)
	default:
		return fmt.Errorf("%s: unsupported file type %#o", path, si.Type)
	}
	return nil
}

func typeBits(k Kind) uint32 {
	switch k {
	case KChar:
		return unix.S_IFCHR
	case KBlock:
		return unix.S_IFBLK
	}
	return unix.S_IFIFO
}
