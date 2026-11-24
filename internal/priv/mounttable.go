package priv

// mountEntry is one row of the host's mount table: the source (device), the mount point, the file system type and
// whether it has nosuid.
type mountEntry struct {
	dev, mp, typ string
	// nosuid says the mount ignores the set-user-ID and set-group-ID bits.
	nosuid bool
	// fsid is the kernel's file system ID ("FSID:val0:val1", empty where the table has none), which unmount(2) takes
	// with MNT_BYFSID instead of a path.
	fsid string
}

// mountTableReader reads the host's mount table without executing any program. The after-check of a live nullfs mount
// (checkMountLanded) runs once the mount is in place: were it to read the table by running mount(8), a tenant whose
// writable volume has just landed on /sbin would have the host run its own binary as root. Reading the table with a
// syscall removes that path. Tests replace this.
var mountTableReader = systemMountTable

// forceUnmount removes the file system mounted on path with the unmount(2) syscall, so a stray mount is undone without
// executing umount(8) (which could itself sit under the stray mount). Tests replace this.
var forceUnmount = systemForceUnmount

// forceUnmountEntry removes the file system of a row of the mount table. With the row's file system ID it never
// resolves a path: unmount(2) by path follows links, and the path of a stray mount lies in a tree the jail controls,
// so a link swapped in after the mount would carry the undo to another file system. Tests replace this.
var forceUnmountEntry = systemForceUnmountEntry

// cStr turns a NUL-terminated C string held in a fixed byte array into a Go string.
func cStr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
