//go:build !freebsd

package main

import "golang.org/x/sys/unix"

// archiver_other.go stubs archiver_freebsd.go's calls for a non-FreeBSD build host, so the
// package compiles everywhere; the archiver is only ever run on FreeBSD.

func archGetFlags(string) (uint32, error)         { return 0, nil }
func archSetFlags(string, uint32) error           { return nil }
func archListXattr(string) ([]string, error)      { return nil, nil }
func archGetXattr(string, string) ([]byte, error) { return nil, nil }
func archSetXattr(string, string, []byte) error   { return nil }

func archLutimes(path string, mtimeNs int64) error {
	ts := []unix.Timespec{unix.NsecToTimespec(mtimeNs), unix.NsecToTimespec(mtimeNs)}
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, 0x100) // AT_SYMLINK_NOFOLLOW on Linux too
}

func archLchown(path string, uid, gid int) error { return unix.Lchown(path, uid, gid) }
func archSymlink(target, path string) error      { return unix.Symlink(target, path) }
