//go:build freebsd

package priv

import "golang.org/x/sys/unix"

// systemMountTable reads the host's mount table with getfsstat(2). MNT_NOWAIT returns the cached table and never blocks
// on a server that is slow or gone.
func systemMountTable() ([]mountEntry, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	buf := make([]unix.Statfs_t, n+8) // slack for mounts that appear between the count and the read
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	if n > len(buf) {
		n = len(buf)
	}
	out := make([]mountEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, mountEntry{
			dev:    cStr(buf[i].Mntfromname[:]),
			mp:     cStr(buf[i].Mntonname[:]),
			typ:    cStr(buf[i].Fstypename[:]),
			nosuid: buf[i].Flags&unix.MNT_NOSUID != 0,
		})
	}
	return out, nil
}

// systemForceUnmount undoes the mount on path with unmount(2).
func systemForceUnmount(path string) error {
	return unix.Unmount(path, unix.MNT_FORCE)
}
