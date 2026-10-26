package main

import (
	"fmt"
	"strings"
	"time"

	"jailops.net/jailops/prototypes/internal/capture"
	"jailops.net/jailops/prototypes/internal/harness"
)

// waitSnapdir retries os.Stat-equivalent WalkTree against a .zfs/snapshot/<name> directory for
// up to 5s, so a slow automount (test 4 measures this precisely) does not fail test 1 outright.
func waitSnapdir(e *harness.Env, dir string) (capture.Tree, time.Duration, error) {
	deadline := time.Now().Add(5 * time.Second)
	start := time.Now()
	var lastErr error
	for {
		t, err := WalkTree(dir)
		if err == nil {
			return t, time.Since(start), nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, time.Since(start), lastErr
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func excludeHot(t capture.Tree) capture.Tree {
	out := capture.Tree{}
	for p, e := range t {
		if !strings.HasPrefix(p, "hot/") {
			out[p] = e
		}
	}
	return out
}

// compareNonHot reports, for every path in want, whether cur has an identical entry; it returns
// the count of exact matches and a short list of the first few mismatches for the detail string.
func compareNonHot(want, got capture.Tree) (matched int, mismatches []string) {
	for p, we := range want {
		ge, ok := got[p]
		if !ok {
			mismatches = append(mismatches, p+": missing")
			continue
		}
		if d := capture.EntryDiff(we, ge); len(d) > 0 {
			mismatches = append(mismatches, fmt.Sprintf("%s: %s", p, fieldNames(d)))
			continue
		}
		matched++
	}
	return matched, mismatches
}

func stabilityReport(a, b capture.Tree) (stable int, unstable []string) {
	for p, ae := range a {
		be, ok := b[p]
		if !ok {
			continue
		}
		if capture.Stable(ae, be) {
			stable++
		} else {
			unstable = append(unstable, fmt.Sprintf("%s: inode %d->%d ctime %d->%d", p, ae.Inode, be.Inode, ae.CtimeNs, be.CtimeNs))
		}
	}
	return stable, unstable
}

// test1 settles section 16 row 9: snapshot metadata fidelity, and inode/ctime stability across
// two snapshots, a rollback, a clone, and a send|recv.
func test1(e *harness.Env, live string, hot []string, wq *writerQ) {
	e.Step("1. snapshot metadata fidelity and inode/ctime stability (row 9)")
	liveDir := mp("live")
	before, err := WalkTree(liveDir)
	if err != nil && !e.DryRun {
		e.Abort("walk live tree: %v", err)
	}
	e.Check("live tree walked before snapshot", err == nil || e.DryRun, fmt.Sprintf("%d entries", len(before)))

	e.Run("zfs", "snapshot", "-r", live+"@s1")
	snap1, delay1, err := waitSnapdirOrDry(e, liveDir+"/.zfs/snapshot/s1")
	e.Check(".zfs/snapshot/s1 became readable", err == nil || e.DryRun, fmt.Sprintf("after %s: err=%v", delay1, err))

	if !e.DryRun && err == nil {
		matched, mism := compareNonHot(excludeHot(before), snap1)
		e.Check("non-hot files identical between live (pre-snapshot) and @s1 view", len(mism) == 0,
			fmt.Sprintf("matched=%d mismatches=%v", matched, firstN(mism, 10)))
		e.Resolve(fmt.Sprintf("snapshot metadata (mode/uid/gid/size/mtime/ctime/inode/xattr/acl/flags) read through .zfs/snapshot equals a live stat taken just before the snapshot: mismatches=%d", len(mism)))
	}
	e.Note("writer goroutine made %d appends to hot files while this ran (crash-consistency, not quiescence)", wq.count())

	time.Sleep(1200 * time.Millisecond) // let a second's worth of wall-clock pass before @s2
	e.Run("zfs", "snapshot", "-r", live+"@s2")
	snap2, delay2, err2 := waitSnapdirOrDry(e, liveDir+"/.zfs/snapshot/s2")
	e.Check(".zfs/snapshot/s2 became readable", err2 == nil || e.DryRun, fmt.Sprintf("after %s", delay2))

	if !e.DryRun && err == nil && err2 == nil {
		stable, unstable := stabilityReport(excludeHot(snap1), excludeHot(snap2))
		e.Check("non-hot files: inode and ctime stable between @s1 and @s2", len(unstable) == 0,
			fmt.Sprintf("stable=%d unstable=%v", stable, firstN(unstable, 10)))
		e.Resolve(fmt.Sprintf("inode/ctime stability across two successive snapshots of the same dataset: unstable=%d of %d", len(unstable), stable+len(unstable)))
	}

	wq.stop()

	// Rollback: destroys @s2 (rollback to an earlier snapshot needs -r for intervening ones).
	e.Run("zfs", "rollback", "-r", live+"@s1")
	afterRollback, errR := WalkTree(liveDir)
	if !e.DryRun && errR == nil && err == nil {
		stable, unstable := stabilityReport(excludeHot(snap1), excludeHot(afterRollback))
		e.Check("inode/ctime stable across zfs rollback to @s1", len(unstable) == 0,
			fmt.Sprintf("stable=%d unstable=%v", stable, firstN(unstable, 10)))
		e.Resolve(fmt.Sprintf("inode/ctime stability across zfs rollback: unstable=%d of %d", len(unstable), stable+len(unstable)))
	}

	// Clone: a new dataset, so the question is informational (ok=true either way), not a pass/fail.
	cloneDS := e.Root + "/live_clone"
	e.Run("zfs", "clone", "-o", "mountpoint="+mp("live_clone"), live+"@s1", cloneDS)
	cloneTree, errC := WalkTree(mp("live_clone"))
	if !e.DryRun && errC == nil && err == nil {
		stable, unstable := stabilityReport(excludeHot(snap1), excludeHot(cloneTree))
		e.Check("inode/ctime across a clone (observation)", true,
			fmt.Sprintf("stable=%d unstable=%d (a clone is a new dataset; L0 should NOT assume stability across one)", stable, len(unstable)))
		e.Resolve(fmt.Sprintf("inode/ctime stability across zfs clone: stable=%d unstable=%d", stable, len(unstable)))
	}

	// send|recv: also a new dataset, same informational treatment.
	recvDS := e.Root + "/live_recv"
	e.Pipe([]string{"zfs", "send", live + "@s1"}, []string{"zfs", "recv", "-u", recvDS})
	e.Run("zfs", "set", "mountpoint="+mp("live_recv"), recvDS)
	e.Run("zfs", "mount", recvDS)
	recvTree, errV := WalkTree(mp("live_recv"))
	if !e.DryRun && errV == nil && err == nil {
		stable, unstable := stabilityReport(excludeHot(snap1), excludeHot(recvTree))
		e.Check("inode/ctime across zfs send|recv (observation)", true,
			fmt.Sprintf("stable=%d unstable=%d (a receiver is a new dataset, possibly a new pool; L0 should NOT assume stability across one)", stable, len(unstable)))
		e.Resolve(fmt.Sprintf("inode/ctime stability across zfs send|recv: stable=%d unstable=%d", stable, len(unstable)))
	}
}

func waitSnapdirOrDry(e *harness.Env, dir string) (capture.Tree, time.Duration, error) {
	if e.DryRun {
		e.Note("would wait for %s to become readable", dir)
		return capture.Tree{}, 0, nil
	}
	return waitSnapdir(e, dir)
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("...and %d more", len(s)-n))
}
