//go:build linux

package catalog

import "golang.org/x/sys/unix"

func fromStat(st *unix.Stat_t) statInfo {
	return statInfo{
		Type: uint32(st.Mode) & unix.S_IFMT, Perm: uint32(st.Mode) & 0o7777,
		UID: st.Uid, GID: st.Gid, Size: st.Size,
		Mtime: unix.TimespecToNsec(st.Mtim), Ctime: unix.TimespecToNsec(st.Ctim),
		Ino: uint64(st.Ino), Dev: uint64(st.Dev), Nlink: uint64(st.Nlink), Rdev: uint64(st.Rdev),
	}
}

func mknod(path string, mode uint32, major, minor uint32) error {
	return unix.Mknod(path, mode, int(unix.Mkdev(major, minor)))
}

// setFlags applies file flags. Linux has no chflags(2); flags recorded on
// another platform cannot be applied here.
func setFlags(path string, flags uint32, link bool) (applied bool, err error) {
	return false, nil
}
