package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
)

// test4 covers the .zfs/snapshot behaviours design 9.4 depends on but does not itself name a
// section 16 row for: automount delay, snapdir visibility, many snapshots, and reading a file
// while its snapshot is destroyed underneath it.
func test4(e *harness.Env, live, liveDir string) {
	e.Step("4. .zfs/snapshot behaviour: automount delay, snapdir visibility, many snapshots, destroy-while-reading")
	if e.DryRun {
		e.Note("would run the .zfs/snapshot behaviour probes")
		return
	}

	e.Run("zfs", "snapshot", live+"@autotest")
	start := time.Now()
	_, delay, err := waitSnapdir(e, liveDir+"/.zfs/snapshot/autotest")
	e.Check(".zfs/snapshot automounts", err == nil, fmt.Sprintf("became readable after %s (measured from snapshot command return at %s)", delay, start.Format(time.RFC3339Nano)))
	e.Resolve(fmt.Sprintf("`.zfs/snapshot/<name>` automount delay: %s", delay))

	snapdir := e.ZfsGet("snapdir", live)
	entries, lsErr := os.ReadDir(liveDir)
	visible := false
	for _, en := range entries {
		if en.Name() == ".zfs" {
			visible = true
		}
	}
	e.Check("snapdir property read", snapdir != "", "snapdir="+snapdir)
	e.Note("`ls` of the mountpoint shows .zfs: %v (err=%v); snapdir property=%s", visible, lsErr, snapdir)
	e.Resolve(fmt.Sprintf("snapdir=%s; .zfs visible in an ordinary directory listing=%v (expected hidden unless snapdir=visible)", snapdir, visible))

	if !skipMany {
		for i := 0; i < manySnaps; i++ {
			e.Quiet("zfs", "snapshot", fmt.Sprintf("%s@m%d", live, i))
		}
		t0 := time.Now()
		list, err := os.ReadDir(liveDir + "/.zfs/snapshot")
		d := time.Since(t0)
		e.Check(fmt.Sprintf("listing .zfs/snapshot with %d+ snapshots present", manySnaps), err == nil,
			fmt.Sprintf("%d entries listed in %s (err=%v)", len(list), d, err))
		e.Resolve(fmt.Sprintf("listing .zfs/snapshot with %d snapshots present took %s", manySnaps, d))
	} else {
		e.Note("many-snapshots probe skipped by --skip-many-snapshots")
	}

	e.Run("zfs", "snapshot", live+"@readtest")
	target := liveDir + "/.zfs/snapshot/readtest/small/f000"
	f, err := os.Open(target)
	e.Check("open a file under .zfs/snapshot/readtest", err == nil, fmt.Sprintf("%s: err=%v", target, err))
	if err == nil {
		e.Run("zfs", "destroy", live+"@readtest")
		time.Sleep(200 * time.Millisecond)
		buf, readErr := io.ReadAll(f)
		f.Close()
		_, statErr := os.Stat(target)
		e.Check("already-open fd under a destroyed snapshot (observation)", true,
			fmt.Sprintf("read after destroy: %d bytes err=%v; stat after destroy: err=%v", len(buf), readErr, statErr))
		e.Resolve(fmt.Sprintf("reading a file via an fd opened under .zfs/snapshot while that snapshot is destroyed: read err=%v, re-stat err=%v", readErr, statErr))
	}
}
