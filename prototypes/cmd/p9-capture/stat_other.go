//go:build !freebsd

package main

import "golang.org/x/sys/unix"

// rawStat mirrors stat_freebsd.go; see there. Flags is always 0 off FreeBSD.
type rawStat struct {
	Ino, Nlink       uint64
	Mode             uint32
	Uid, Gid         uint32
	Size             int64
	MtimeNs, CtimeNs int64
	Flags            uint32
}

func doLstat(path string) (rawStat, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return rawStat{}, err
	}
	return rawStat{
		Ino: st.Ino, Nlink: st.Nlink, Mode: st.Mode, Uid: st.Uid, Gid: st.Gid,
		Size: st.Size, MtimeNs: unix.TimespecToNsec(st.Mtim), CtimeNs: unix.TimespecToNsec(st.Ctim),
	}, nil
}
