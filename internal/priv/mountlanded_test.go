package priv

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// mountTableFrom parses an fstab-style listing (as mount -p prints) into mount-table rows, for tests that seed the
// host's mounts through the stubbed reader.
func mountTableFrom(list string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(list, "\n") {
		if f := strings.Fields(line); len(f) >= 3 {
			out = append(out, mountEntry{dev: f[0], mp: f[1], typ: f[2]})
		}
	}
	return out
}

// withMountStubs swaps the syscall-backed mount-table reader and unmounter for test doubles and restores them.
func withMountStubs(t *testing.T, table func() ([]mountEntry, error)) *[]string {
	t.Helper()
	var unmounted []string
	origTable, origUnmount, origEntry := mountTableReader, forceUnmount, forceUnmountEntry
	mountTableReader = table
	forceUnmount = func(p string) error { unmounted = append(unmounted, p); return nil }
	forceUnmountEntry = func(e mountEntry) error { unmounted = append(unmounted, "fsid:"+e.mp); return nil }
	t.Cleanup(func() { mountTableReader = origTable; forceUnmount = origUnmount; forceUnmountEntry = origEntry })
	return &unmounted
}

func TestCheckMountLandedCleanLanding(t *testing.T) {
	before := map[string]string{"/a\x00tank/a": "/a"}
	after := []mountEntry{{dev: "tank/a", mp: "/a", typ: "zfs"}, {dev: "vol", mp: "/jail/srv", typ: "nullfs"}}
	unmounted := withMountStubs(t, func() ([]mountEntry, error) { return after, nil })
	l := &Local{}
	if err := l.checkMountLanded(context.Background(), before, "/jail/srv"); err != nil {
		t.Fatalf("clean landing should pass: %v", err)
	}
	if len(*unmounted) != 0 {
		t.Fatalf("nothing should be unmounted, got %v", *unmounted)
	}
}

func TestCheckMountLandedStrayIsUnmountedBySyscall(t *testing.T) {
	before := map[string]string{"/a\x00tank/a": "/a"}
	// the asked-for mount at /jail/srv did not appear; instead a writable volume landed on /sbin (a link in the path).
	after := []mountEntry{{dev: "tank/a", mp: "/a", typ: "zfs"}, {dev: "vol", mp: "/sbin", typ: "nullfs"}}
	unmounted := withMountStubs(t, func() ([]mountEntry, error) { return after, nil })
	l := &Local{}
	err := l.checkMountLanded(context.Background(), before, "/jail/srv")
	if err == nil {
		t.Fatal("a mount that landed elsewhere must be refused")
	}
	if !slices.Contains(*unmounted, "fsid:/sbin") {
		t.Fatalf("the stray mount on /sbin must be undone by its file system ID, got %v", *unmounted)
	}
}

func TestCheckMountLandedWrongSourceIsUndone(t *testing.T) {
	before := map[string]string{"/a\x00tank/a": "/a"}
	// the mount point is right, but the source path crossed a link: the jail sees the host's /etc.
	after := []mountEntry{{dev: "tank/a", mp: "/a", typ: "zfs"}, {dev: "/etc", mp: "/jail/srv", typ: "nullfs"}}
	unmounted := withMountStubs(t, func() ([]mountEntry, error) { return after, nil })
	l := &Local{}
	if err := l.checkMountLanded(context.Background(), before, "/jail/srv", "/vols/a"); err == nil {
		t.Fatal("a mount from another source must be refused")
	}
	if !slices.Contains(*unmounted, "fsid:/jail/srv") {
		t.Fatalf("the mount from the wrong source must be undone, got %v", *unmounted)
	}
	// the same mount from the asked-for source passes
	after[1].dev = "/vols/a"
	*unmounted = nil
	if err := l.checkMountLanded(context.Background(), before, "/jail/srv", "/vols/a"); err != nil || len(*unmounted) != 0 {
		t.Fatalf("the asked-for source should pass: %v %v", err, *unmounted)
	}
}

func TestCheckMountLandedFailsClosedWhenTableUnreadable(t *testing.T) {
	before := map[string]string{"/a\x00tank/a": "/a"}
	unmounted := withMountStubs(t, func() ([]mountEntry, error) { return nil, errors.New("getfsstat failed") })
	l := &Local{}
	err := l.checkMountLanded(context.Background(), before, "/jail/srv")
	if err == nil {
		t.Fatal("an unreadable mount table must fail closed, not pass")
	}
	if !slices.Contains(*unmounted, "/jail/srv") {
		t.Fatalf("the asked-for mount must be undone when it cannot be verified, got %v", *unmounted)
	}
}

func TestCheckMountLandedFailsClosedWhenBeforeNil(t *testing.T) {
	after := []mountEntry{{dev: "vol", mp: "/jail/srv", typ: "nullfs"}}
	unmounted := withMountStubs(t, func() ([]mountEntry, error) { return after, nil })
	l := &Local{}
	err := l.checkMountLanded(context.Background(), nil, "/jail/srv")
	if err == nil {
		t.Fatal("a missing before-snapshot must fail closed")
	}
	if !slices.Contains(*unmounted, "/jail/srv") {
		t.Fatalf("the asked-for mount must be undone, got %v", *unmounted)
	}
}
