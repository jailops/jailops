package priv

import (
	"context"
	"slices"
	"strings"
)

// mountPoints lists the host's mounts keyed by "<mount point>\x00<source>", one per mount. It reads the table with a
// syscall (mountTableReader), never by running a program: the after-check of a live mount must not exec a binary a
// tenant could have covered with that very mount. A table that cannot be read is nil, and callers fail closed.
func (l *Local) mountPoints(ctx context.Context) map[string]string {
	ents, err := mountTableReader()
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, e := range ents {
		m[e.mp+"\x00"+e.dev] = e.mp
	}
	return m
}

// strayMounts returns the rows of after that are not in before and are not the mount that was asked for: a mount on
// another path, or a nullfs mount on dest whose source is not one of sources (a link swapped into the source's path
// carries the mount to another directory). sources is empty for a mount whose source is not checked.
func strayMounts(before map[string]string, after []mountEntry, dest string, sources []string) []mountEntry {
	var out []mountEntry
	for _, e := range after {
		if _, had := before[e.mp+"\x00"+e.dev]; had {
			continue
		}
		if e.mp == dest && (len(sources) == 0 || e.typ != "nullfs" || slices.Contains(sources, e.dev)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// checkMountLanded is the second half of a check that cannot be made once. FreeBSD has no mount through a file
// descriptor: nmount(2) takes the mount point as a path and resolves it with every link in it (and mount_nullfs(8)
// resolves both paths with realpath(3) first). The path lies in a tree a running jail controls, and between the
// helper's look at it and the mount the jail can swap a directory of the path for a symbolic link, which carries the
// mount to wherever it points. The kernel records where the mount really landed (the global path of the covered
// vnode, and the resolved source), so the helper compares the host's mounts before and after: a mount that appeared
// anywhere but at dest, or on dest from another source, is undone at once by its file system ID (unmount(2) with
// MNT_BYFSID resolves no path, so a second swap cannot redirect the undo). sources are the paths the source may
// resolve to (empty: the source is not checked). before is nil when the list could not be read.
func (l *Local) checkMountLanded(ctx context.Context, before map[string]string, dest string, sources ...string) error {
	after, err := mountTableReader()
	// Fail closed: a table we cannot read before or after the mount leaves us unable to tell where the mount landed, so
	// we undo the path we asked for and refuse rather than leave a mount we could not verify. With getfsstat this is
	// effectively unreachable, but the safe answer when the check cannot run is no.
	if before == nil || err != nil {
		_ = forceUnmount(dest)
		return forbidden("mount %s: the host's mount table could not be read, so the mount could not be verified; it was undone", dest)
	}
	stray := strayMounts(before, after, dest, sources)
	var where []string
	for _, e := range stray {
		_ = forceUnmountEntry(e)
		where = append(where, e.mp+" (from "+e.dev+")")
	}
	if len(stray) > 0 {
		return forbidden("the mount landed on %s and not on %s from %s (a link in a path): it was unmounted", strings.Join(where, ", "), dest, strings.Join(sources, " or "))
	}
	return nil
}
