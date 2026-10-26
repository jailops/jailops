package priv

import (
	"strings"
	"sync"
	"time"
)

// grants is what only the helper decides: which datasets are being restored (a file agent may write them) and which
// snapshots a backup made (a file agent may read them). ZFS user properties cannot carry this, because the daemon may
// set any user property on a dataset it owns: whatever the helper acts on as root must come from something the
// daemon cannot write. The records live in the helper's memory; a helper that restarts forgets them, which ends the
// restore or the backup that was running (it is undone or retried), never widens anything.
type grants struct {
	mu        sync.Mutex
	now       func() time.Time
	restoring map[string]time.Time // dataset -> expiry
	snaps     map[string]time.Time // dataset@snapshot -> expiry
}

// grantTTL bounds how long a restore or a backup may take before its grant lapses.
const grantTTL = 24 * time.Hour

func newGrants() *grants {
	return &grants{now: time.Now, restoring: map[string]time.Time{}, snaps: map[string]time.Time{}}
}

func (g *grants) expiry() time.Time { return g.now().Add(grantTTL) }

// beginRestore records that ds, just created, is being restored: it and everything below it may be written.
func (g *grants) beginRestore(ds string) {
	g.mu.Lock()
	g.restoring[ds] = g.expiry()
	g.mu.Unlock()
}

// restoringDataset reports whether ds, or a dataset above it, is being restored.
func (g *grants) restoringDataset(ds string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for d := ds; d != ""; {
		if exp, ok := g.restoring[d]; ok {
			if now.Before(exp) {
				return true
			}
			delete(g.restoring, d)
		}
		i := strings.LastIndexByte(d, '/')
		if i < 0 {
			break
		}
		d = d[:i]
	}
	return false
}

// endRestore ends the restore of ds (it is complete, or it was undone).
func (g *grants) endRestore(ds string) {
	g.mu.Lock()
	for d := range g.restoring {
		if d == ds || strings.HasPrefix(d, ds+"/") {
			delete(g.restoring, d)
		}
	}
	g.mu.Unlock()
}

// addSnapshot records a snapshot a backup made.
func (g *grants) addSnapshot(full string) {
	g.mu.Lock()
	g.snaps[full] = g.expiry()
	g.mu.Unlock()
}

func (g *grants) backupSnapshot(full string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.snaps[full]
	if ok && !g.now().Before(exp) {
		delete(g.snaps, full)
		return false
	}
	return ok
}

// forget drops everything recorded for a dataset that is gone or renamed (and its snapshots and children).
func (g *grants) forget(name string) {
	ds := dsPart(name)
	g.mu.Lock()
	defer g.mu.Unlock()
	if strings.Contains(name, "@") {
		delete(g.snaps, name)
		return
	}
	for d := range g.restoring {
		if d == ds || strings.HasPrefix(d, ds+"/") {
			delete(g.restoring, d)
		}
	}
	for s := range g.snaps {
		if dsPart(s) == ds || strings.HasPrefix(dsPart(s), ds+"/") {
			delete(g.snaps, s)
		}
	}
}
