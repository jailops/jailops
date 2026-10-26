//go:build freebsd

package main

import "golang.org/x/sys/unix"

// rawStat is the subset of struct stat this prototype reads, in a shape common to every GOOS so
// walk.go stays portable; only this file and stat_other.go know the real field names and types.
type rawStat struct {
	Ino, Nlink       uint64
	Mode             uint32
	Uid, Gid         uint32
	Size             int64
	MtimeNs, CtimeNs int64
	Flags            uint32 // st_flags (chflags); 0 off FreeBSD
}

func doLstat(path string) (rawStat, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return rawStat{}, err
	}
	return rawStat{
		Ino: st.Ino, Nlink: st.Nlink, Mode: uint32(st.Mode), Uid: st.Uid, Gid: st.Gid,
		Size: st.Size, MtimeNs: unix.TimespecToNsec(st.Mtim), CtimeNs: unix.TimespecToNsec(st.Ctim),
		Flags: st.Flags,
	}, nil
}
