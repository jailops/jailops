package priv

import (
	"context"
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

// landedElsewhere returns the mount points that appeared between before and after, other than dest.
func landedElsewhere(before, after map[string]string, dest string) []string {
	var out []string
	for k, mp := range after {
		if _, had := before[k]; !had && mp != dest {
			out = append(out, mp)
		}
	}
	return out
}

// checkMountLanded is the second half of a check that cannot be made once: the path of a mount lies in a tree a running
// jail controls, and between the helper's look at it and mount(8) the jail can swap a directory of the path for a
// symbolic link, which carries the mount to wherever it points. So the helper compares the host's mounts before and
// after, and a mount that appeared anywhere but at the path it was asked for is unmounted at once (the window is that
// of one mount(8) call, and what is left behind is nothing). before is nil when the list could not be read.
func (l *Local) checkMountLanded(ctx context.Context, before map[string]string, dest string) error {
	after := l.mountPoints(ctx)
	// Fail closed: a table we cannot read before or after the mount leaves us unable to tell where the mount landed, so
	// we undo the path we asked for and refuse rather than leave a mount we could not verify. With getfsstat this is
	// effectively unreachable, but the safe answer when the check cannot run is no.
	if before == nil || after == nil {
		_ = forceUnmount(dest)
		return forbidden("mount %s: the host's mount table could not be read, so the mount could not be verified; it was undone", dest)
	}
	stray := landedElsewhere(before, after, dest)
	for _, mp := range stray {
		_ = forceUnmount(mp)
	}
	if len(stray) > 0 {
		return forbidden("the mount landed on %s and not on %s (a link in the path): it was unmounted", strings.Join(stray, ", "), dest)
	}
	return nil
}
