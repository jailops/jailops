//go:build !linux && !freebsd

package catalog

import "errors"

// Platforms other than Linux and FreeBSD are not supported: walking and
// restoring need lstat(2) fields, device numbers and ownership calls whose
// layout differs per system.
type statInfo struct {
	Type, Perm, UID, GID, Flags uint32
	Size, Mtime, Ctime          int64
	Ino, Dev, Nlink, Rdev       uint64
}

var ErrSocket = errors.New("socket")

var errUnsupportedOS = errors.New("catalog: this operating system is not supported")

func lstat(path string) (statInfo, error)                         { return statInfo{}, errUnsupportedOS }
func kindOf(path string, si statInfo, e *Entry) error             { return errUnsupportedOS }
func mknod(path string, mode uint32, major, minor uint32) error   { return errUnsupportedOS }
func setFlags(path string, flags uint32, link bool) (bool, error) { return false, errUnsupportedOS }

func typeBits(k Kind) uint32 { return 0 }
