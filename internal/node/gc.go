package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/sdk/api"
)

// GCOptions tune a garbage collection.
type GCOptions struct {
	// Grace is the age an unreferenced chunk needs before it may be swept,
	// measured from the object's write time. Zero means the node's default.
	// Values under the minimum (12 hours) are refused.
	Grace  time.Duration `json:"grace,omitempty"`
	DryRun bool          `json:"dry_run,omitempty"`
}

// GCStats reports a garbage collection.
type GCStats struct {
	Scanned          int    `json:"scanned"`
	Live             int    `json:"live"`
	KeptYoung        int    `json:"kept_young"`
	KeptPinned       int    `json:"kept_pinned"`
	Removed          int    `json:"removed"`
	BytesReclaimed   int64  `json:"bytes_reclaimed"`
	ChunkObjects     int64  `json:"chunk_objects"`
	ChunkBytes       int64  `json:"chunk_bytes"`
	QuarantinePurged int    `json:"quarantine_purged"`
	OrphansRemoved   int    `json:"orphans_removed"`
	Snapshots        int    `json:"snapshots_marked"`
	DryRun           bool   `json:"dry_run"`
	Grace            string `json:"grace"`
}

const gcSweepBatch = 4096

func (n *Node) beginGC() error {
	n.pinMu.Lock()
	defer n.pinMu.Unlock()
	if n.gcActive {
		return errf(api.CodeBusy, "garbage collection is already running")
	}
	n.gcActive = true
	n.shadow = map[uint64]struct{}{}
	return nil
}

func (n *Node) endGC() {
	n.pinMu.Lock()
	n.gcActive = false
	n.shadow = nil
	n.pinMu.Unlock()
}

func (n *Node) graceFor(g time.Duration) (time.Duration, error) {
	if g == 0 {
		g = n.cfg.Grace
	}
	if g < n.cfg.MinGrace {
		return 0, errf(api.CodeInvalid, "the grace period %v is shorter than the minimum %v", g, n.cfg.MinGrace)
	}
	return g, nil
}

// GC removes chunks that no committed snapshot references. Mark and sweep:
//
//  1. Mark: read every committed snapshot's index; their chunks are live. An
//     unreadable or corrupt index aborts the run: never sweep on a guess.
//  2. Candidates: chunks that are not live and older than the grace period. The age is the
//     time the object was written; nothing ever rewrites or touches a chunk.
//  3. Sweep: each batch of candidates is re-checked under the write lock (pins, then age on
//     the backend) and deleted there. A chunk a session said "present" for or uploaded is
//     never swept, so a long upload outlives the grace period, and a snapshot committed after
//     the mark phase keeps the chunks it references (a commit pins them first).
func (n *Node) GC(ctx context.Context, opts GCOptions, actor string) (*GCStats, ctl.Job, error) {
	grace, err := n.graceFor(opts.Grace)
	if err != nil {
		return nil, ctl.Job{}, err
	}
	if !opts.DryRun {
		if err := n.requireOnline(); err != nil {
			return nil, ctl.Job{}, err
		}
	}
	var st *GCStats
	job, err := n.runJob(ctx, ctl.JobGC, actor, map[string]any{"grace": grace.String(), "dry_run": opts.DryRun}, func(ctx context.Context) (any, error) {
		var e error
		st, e = n.gc(ctx, grace, opts.DryRun)
		return st, e
	})
	return st, job, err
}

// StartGC runs GC in the background and returns its job.
func (n *Node) StartGC(opts GCOptions, actor string) (ctl.Job, error) {
	grace, err := n.graceFor(opts.Grace)
	if err != nil {
		return ctl.Job{}, err
	}
	if !opts.DryRun {
		if err := n.requireOnline(); err != nil {
			return ctl.Job{}, err
		}
	}
	return n.startAsync(ctl.JobGC, actor, map[string]any{"grace": grace.String(), "dry_run": opts.DryRun}, func(ctx context.Context) (any, error) {
		return n.gc(ctx, grace, opts.DryRun)
	})
}

func (n *Node) gc(ctx context.Context, grace time.Duration, dry bool) (*GCStats, error) {
	if _, err := n.Sweep(ctx); err != nil {
		return nil, err
	}
	if err := n.beginGC(); err != nil {
		return nil, err
	}
	defer n.endGC()
	st := &GCStats{DryRun: dry, Grace: grace.String()}
	cutoff := n.now().Add(-grace)
	live, marked, err := n.markLive(ctx)
	if err != nil {
		return nil, err
	}
	st.Snapshots = marked
	type cand struct {
		id   format.ID
		key  string
		size int64
	}
	old := make([]cand, 0, 1024)
	sweep := func() error {
		par := n.cfg.Parallelism
		for i := 0; i < len(old); i += par {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch := old[i:min(i+par, len(old))]
			type outcome struct {
				removed, young bool
				size           int64
			}
			res := make([]outcome, len(batch))
			pinned := make([]bool, len(batch))
			// The write lock is held for about two round trips per batch: nothing a session relies on
			// can be swept, because sessions take the read side whenever they claim a chunk.
			n.gcMu.Lock()
			for j, c := range batch {
				pinned[j] = n.pinned(c.id)
			}
			derr := forEach(ctx, par, len(batch), func(j int) error {
				if pinned[j] {
					return nil
				}
				info, err := n.be.Head(ctx, batch[j].key)
				if err != nil {
					if errors.Is(err, datastore.ErrNotFound) {
						return nil
					}
					return err
				}
				if info.ModTime.After(cutoff) {
					res[j].young = true
					return nil
				}
				if !dry {
					if err := n.be.Delete(ctx, batch[j].key); err != nil {
						return err
					}
				}
				res[j] = outcome{removed: true, size: info.Size}
				return nil
			})
			n.gcMu.Unlock()
			for j := range batch {
				switch {
				case pinned[j]:
					st.KeptPinned++
				case res[j].young:
					st.KeptYoung++
				case res[j].removed:
					st.Removed++
					st.BytesReclaimed += res[j].size
				}
			}
			if derr != nil {
				return derr
			}
		}
		old = old[:0]
		return nil
	}
	err = n.be.List(ctx, chunkDir, func(o datastore.ObjectInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, ok := chunkIDFromKey(o.Key)
		if !ok {
			return nil // not ours: leave it
		}
		st.Scanned++
		st.ChunkObjects++
		st.ChunkBytes += o.Size
		if _, ok := live[liveKey(id)]; ok {
			st.Live++
			return nil
		}
		if o.ModTime.After(cutoff) {
			st.KeptYoung++
			return nil
		}
		old = append(old, cand{id: id, key: o.Key, size: o.Size})
		if len(old) >= gcSweepBatch {
			return sweep()
		}
		return nil
	})
	if err == nil {
		err = sweep()
	}
	if err != nil {
		return st, err
	}
	_ = n.db.SetSetting(ctx, "usage", map[string]any{"chunks": st.ChunkObjects - int64(st.Removed), "bytes": st.ChunkBytes - st.BytesReclaimed, "at": n.now().UTC()})
	if !dry {
		st.QuarantinePurged, _ = n.PurgeQuarantine(ctx, 24*time.Hour)
		st.OrphansRemoved, _ = n.cleanOrphans(ctx, grace)
		if c, ok := datastore.Unwrap(n.raw).(interface {
			CleanTmp(time.Duration) (int, error)
		}); ok {
			_, _ = c.CleanTmp(grace)
		}
	}
	return st, nil
}

// listCommitted returns every (group, snapshot) of the repository that has a
// commit.json, from one listing of groups/.
func (n *Node) listCommitted(ctx context.Context) ([][2]string, error) {
	var out [][2]string
	err := n.be.List(ctx, groupsDir, func(o datastore.ObjectInfo) error {
		g, s, f, ok := splitSnapKey(o.Key)
		if ok && f == fileCommit {
			out = append(out, [2]string{g, s})
		}
		return nil
	})
	return out, err
}

// splitSnapKey splits groups/<g>/<s>/<file>.
func splitSnapKey(key string) (group, snap, file string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "groups" || !format.ValidGroupID(parts[1]) || !format.ValidSnapshotID(parts[2]) {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

// byteBudget bounds the memory of the indexes read at once.
type byteBudget struct {
	mu    sync.Mutex
	cond  *sync.Cond
	left  int64
	total int64
}

func newByteBudget(total int64) *byteBudget {
	b := &byteBudget{left: total, total: total}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *byteBudget) acquire(n int64) int64 {
	n = min(n, b.total)
	b.mu.Lock()
	for b.left < n {
		b.cond.Wait()
	}
	b.left -= n
	b.mu.Unlock()
	return n
}

func (b *byteBudget) release(n int64) {
	b.mu.Lock()
	b.left += n
	b.mu.Unlock()
	b.cond.Broadcast()
}

// markLive is the mark phase of GC: the chunk ids of every committed snapshot.
// An unreadable or corrupt index aborts the run. A snapshot deleted while
// marking is skipped when its commit.json is gone too.
func (n *Node) markLive(ctx context.Context) (map[uint64]struct{}, int, error) {
	snaps, err := n.listCommitted(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("gc aborted: listing snapshots: %w", err)
	}
	live := map[uint64]struct{}{}
	var mu sync.Mutex
	budget := newByteBudget(512 << 20)
	err = forEach(ctx, min(n.cfg.Parallelism, 8), len(snaps), func(i int) error {
		g, s := snaps[i][0], snaps[i][1]
		key := snapPrefix(g, s) + fileIndex
		info, err := n.be.Head(ctx, key)
		if err == nil {
			held := budget.acquire(2 * info.Size)
			defer budget.release(held)
			var b []byte
			b, err = datastore.ReadAll(ctx, n.be, key, format.MaxIndexSize)
			if err == nil {
				var idx *format.Index
				if idx, err = format.ParseIndex(b); err != nil {
					return fmt.Errorf("gc aborted: corrupt index %s/%s: %w", g, s, err)
				}
				mu.Lock()
				for _, e := range idx.Entries {
					if !e.ID.IsZero() {
						live[liveKey(e.ID)] = struct{}{}
					}
				}
				mu.Unlock()
				return nil
			}
		}
		if errors.Is(err, datastore.ErrNotFound) {
			if _, cerr := n.be.Head(ctx, snapPrefix(g, s)+fileCommit); errors.Is(cerr, datastore.ErrNotFound) {
				return nil // deleted while marking: its chunks need no protection
			}
		}
		return fmt.Errorf("gc aborted: reading the index of %s/%s: %w", g, s, err)
	})
	if err != nil {
		return nil, 0, err
	}
	return live, len(snaps), nil
}

// cleanOrphans removes the objects of uploads that never got their commit.json
// (an index and a manifest of a failed commit, the leftovers of an interrupted
// delete) once they are older than grace. Only keys of the form
// groups/<group>/<snapshot>/<file> are considered; group-level objects are never touched.
func (n *Node) cleanOrphans(ctx context.Context, grace time.Duration) (int, error) {
	cutoff := n.now().Add(-grace)
	committed := map[[2]string]bool{}
	type obj struct {
		g, s, key string
		mod       time.Time
	}
	var all []obj
	err := n.be.List(ctx, groupsDir, func(o datastore.ObjectInfo) error {
		g, s, f, ok := splitSnapKey(o.Key)
		if !ok {
			return nil
		}
		if f == fileCommit {
			committed[[2]string{g, s}] = true
		} else {
			all = append(all, obj{g, s, o.Key, o.ModTime})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, o := range all {
		if committed[[2]string{o.g, o.s}] || o.mod.After(cutoff) {
			continue
		}
		l := n.snapLock(o.g, o.s)
		l.Lock()
		// re-check under the snapshot lock, which a commit of the same snapshot holds while it writes
		if _, err := n.be.Head(ctx, snapPrefix(o.g, o.s)+fileCommit); !errors.Is(err, datastore.ErrNotFound) {
			l.Unlock()
			continue
		}
		if info, err := n.be.Head(ctx, o.key); err == nil && !info.ModTime.After(cutoff) {
			if n.be.Delete(ctx, o.key) == nil {
				removed++
			}
		}
		l.Unlock()
	}
	return removed, nil
}
