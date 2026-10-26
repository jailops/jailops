package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/sdk/api"
)

// Verification modes.
const (
	// VerifyAll checks every snapshot and reads every chunk object (a scrub, including unreferenced chunks).
	VerifyAll = "all"
	// VerifyNew checks snapshots that were never verified.
	VerifyNew = "new"
	// VerifyStale checks snapshots never verified or last verified more than StaleDays ago.
	VerifyStale = "stale"
)

// DefaultStaleDays is the age after which VerifyStale checks a snapshot again.
const DefaultStaleDays = 30

// VerifyOptions tune a verification job.
type VerifyOptions struct {
	Mode string `json:"mode,omitempty"`
	// StaleDays is the age for VerifyStale (default DefaultStaleDays).
	StaleDays int `json:"stale_days,omitempty"`
	// Quarantine moves corrupt chunks aside so that they stop satisfying a host's presence check.
	Quarantine bool `json:"quarantine,omitempty"`
}

// Problem is something verification found wrong.
type Problem struct {
	Kind   string `json:"kind"` // chunk-corrupt, chunk-missing, index-corrupt, manifest-corrupt, receipt-corrupt, partial-snapshot
	Object string `json:"object"`
	Detail string `json:"detail"`
}

// SnapshotResult is the outcome for one snapshot.
type SnapshotResult struct {
	Group    string   `json:"group"`
	Snapshot string   `json:"snapshot"`
	State    string   `json:"state"` // ok or failed
	Chunks   int      `json:"chunks"`
	Problems []string `json:"problems,omitempty"`
}

// VerifyStats reports a verification run.
type VerifyStats struct {
	Mode        string           `json:"mode"`
	Chunks      int              `json:"chunks_read"` // chunk objects read (each once per job, unless the cache overflowed)
	Snapshots   int              `json:"snapshots"`
	Skipped     int              `json:"skipped"`
	Failed      int              `json:"failed"`
	Quarantined int              `json:"quarantined"`
	Problems    []Problem        `json:"problems,omitempty"`
	Results     []SnapshotResult `json:"results,omitempty"`
}

// Verification states of a snapshot.
const (
	VerifyOK     = "ok"
	VerifyFailed = "failed"
)

const (
	maxVerifyHistory = 20
	maxProblems      = 200
	verifyBatch      = 256
	verifyCacheMax   = 1 << 20
)

// VerifyEvent is one verification of a snapshot, as recorded in verify.json.
type VerifyEvent struct {
	At       time.Time `json:"at"`
	State    string    `json:"state"`
	Chunks   int       `json:"chunks"`
	Problems []string  `json:"problems,omitempty"`
}

// VerifyRecord is verify.json: the latest state and a bounded history, newest last.
type VerifyRecord struct {
	State   string        `json:"state"`
	At      time.Time     `json:"at"`
	History []VerifyEvent `json:"history"`
	// Failed, Verified and Detail are the fields of the repository store's record
	// (internal/repo/store.VerifyRecord): a host-side store reading this file sees them.
	Failed   bool      `json:"failed"`
	Verified time.Time `json:"verified,omitzero"`
	Detail   string    `json:"detail,omitempty"`
}

type chunkState int

const (
	chunkOK chunkState = iota + 1
	chunkBad
	chunkGone
)

// stateCache remembers chunk states in two generations of at most limit/2 entries.
type stateCache struct {
	cur, prev map[format.ID]chunkState
	half      int
}

func newStateCache(limit int) *stateCache {
	h := max(limit/2, 1)
	return &stateCache{cur: map[format.ID]chunkState{}, prev: map[format.ID]chunkState{}, half: h}
}

func (c *stateCache) get(id format.ID) (chunkState, bool) {
	if st, ok := c.cur[id]; ok {
		return st, true
	}
	st, ok := c.prev[id]
	return st, ok
}

func (c *stateCache) put(id format.ID, st chunkState) {
	if len(c.cur) >= c.half {
		c.prev, c.cur = c.cur, make(map[format.ID]chunkState, c.half)
	}
	c.cur[id] = st
}

// Verify runs a keyless verification job and records its outcome: trailers of
// chunks, manifests and indexes, the index and manifest digests against the
// receipt, and the existence of every referenced chunk. Each chunk is read at
// most once per job. A corrupt or missing chunk marks EVERY snapshot that
// references it as failed, not only the ones selected. Results go to
// verify.json of each snapshot and to the control plane.
func (n *Node) Verify(ctx context.Context, opts VerifyOptions, actor string) (*VerifyStats, ctl.Job, error) {
	if err := n.checkVerify(&opts); err != nil {
		return nil, ctl.Job{}, err
	}
	var st *VerifyStats
	job, err := n.runJob(ctx, ctl.JobVerify, actor, opts, func(ctx context.Context) (any, error) {
		var e error
		st, e = n.verify(ctx, opts)
		return summarize(st), e
	})
	return st, job, err
}

// StartVerify runs Verify in the background.
func (n *Node) StartVerify(opts VerifyOptions, actor string) (ctl.Job, error) {
	if err := n.checkVerify(&opts); err != nil {
		return ctl.Job{}, err
	}
	return n.startAsync(ctl.JobVerify, actor, opts, func(ctx context.Context) (any, error) {
		st, err := n.verify(ctx, opts)
		return summarize(st), err
	})
}

func (n *Node) checkVerify(o *VerifyOptions) error {
	switch o.Mode {
	case "":
		o.Mode = VerifyNew
	case VerifyAll, VerifyNew, VerifyStale:
	default:
		return errf(api.CodeInvalid, "unknown verify mode %q (all, new or stale)", o.Mode)
	}
	if o.StaleDays <= 0 {
		o.StaleDays = DefaultStaleDays
	}
	if err := n.requireOnline(); err != nil {
		return err // verify.json is written
	}
	return nil
}

// summarize is the job result: the counters and the failed snapshots, not the full per-snapshot list.
func summarize(st *VerifyStats) any {
	if st == nil {
		return nil
	}
	c := *st
	c.Results = nil
	for _, r := range st.Results {
		if r.State == VerifyFailed {
			c.Results = append(c.Results, r)
		}
	}
	return &c
}

type fetched struct {
	st   chunkState
	verr error
	err  error // backend error
}

func (n *Node) verify(ctx context.Context, o VerifyOptions) (*VerifyStats, error) {
	vs := &VerifyStats{Mode: o.Mode}
	add := func(kind, obj, detail string) {
		if len(vs.Problems) < maxProblems {
			vs.Problems = append(vs.Problems, Problem{kind, obj, detail})
		}
	}
	badSet := map[format.ID]chunkState{} // exact: corrupt and missing chunks are few
	cache := newStateCache(verifyCacheMax)
	lookup := func(id format.ID) (chunkState, bool) {
		if s, ok := badSet[id]; ok {
			return s, true
		}
		return cache.get(id)
	}
	fetch := func(id format.ID) fetched {
		key := chunkKey(id)
		info, err := n.be.Head(ctx, key)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				return fetched{st: chunkGone}
			}
			return fetched{err: err}
		}
		if info.Size > format.MaxBlobSize {
			return fetched{st: chunkBad, verr: fmt.Errorf("object of %d bytes is larger than any blob", info.Size)}
		}
		b, err := datastore.ReadAll(ctx, n.be, key, format.MaxBlobSize)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				return fetched{st: chunkGone}
			}
			return fetched{err: err}
		}
		if verr := format.VerifyBlob(b); verr != nil {
			return fetched{st: chunkBad, verr: verr}
		}
		return fetched{st: chunkOK}
	}
	pending := map[format.ID]fetched{}
	par := min(n.cfg.Parallelism, 8) // up to 8 blobs in memory at a time
	prefetch := func(ids []format.ID) {
		var todo []format.ID
		for _, id := range ids {
			if _, ok := lookup(id); ok {
				continue
			}
			if _, ok := pending[id]; ok {
				continue
			}
			pending[id] = fetched{}
			todo = append(todo, id)
		}
		res := make([]fetched, len(todo))
		_ = forEach(ctx, par, len(todo), func(i int) error {
			res[i] = fetch(todo[i])
			return nil
		})
		for i, id := range todo {
			pending[id] = res[i]
		}
	}
	check := func(id format.ID, remember bool) (chunkState, error) {
		if st, ok := lookup(id); ok {
			return st, nil
		}
		r, ok := pending[id]
		if !ok || r.st == 0 && r.err == nil {
			r = fetch(id)
		}
		delete(pending, id)
		if r.err != nil {
			return 0, r.err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		switch r.st {
		case chunkGone:
			badSet[id] = chunkGone
			return chunkGone, nil
		case chunkBad:
			vs.Chunks++
			badSet[id] = chunkBad
			add("chunk-corrupt", id.String(), r.verr.Error())
			if o.Quarantine {
				if moved, qerr := n.Quarantine(ctx, id); qerr != nil {
					return 0, qerr
				} else if moved {
					vs.Quarantined++
				}
			}
			return chunkBad, nil
		}
		vs.Chunks++
		if remember {
			cache.put(id, chunkOK)
		}
		return chunkOK, nil
	}

	// Which snapshots exist (the repository is the truth) and what the control plane knows of them.
	committed, err := n.listCommitted(ctx)
	if err != nil {
		return vs, err
	}
	slices.SortFunc(committed, func(a, b [2]string) int {
		if a[0] != b[0] {
			return compareStr(a[0], b[0])
		}
		return compareStr(a[1], b[1])
	})
	rows, err := n.db.Snapshots(ctx, "")
	if err != nil {
		return vs, err
	}
	known := map[[2]string]ctl.Snapshot{}
	for _, r := range rows {
		known[[2]string{r.GroupID, r.SnapshotID}] = r
	}
	now := n.now()
	var selected [][2]string
	for _, k := range committed {
		r, have := known[k]
		take := true
		switch o.Mode {
		case VerifyNew:
			take = !have || r.VerifyState == ""
		case VerifyStale:
			take = !have || r.VerifiedAt == nil || now.Sub(*r.VerifiedAt) > time.Duration(o.StaleDays)*24*time.Hour
		}
		if take {
			selected = append(selected, k)
		} else {
			vs.Skipped++
		}
	}
	done := map[[2]string]bool{}
	for _, k := range selected {
		if err := ctx.Err(); err != nil {
			return vs, err
		}
		res := SnapshotResult{Group: k[0], Snapshot: k[1], State: VerifyOK}
		if err := n.verifySnapshot(ctx, k[0], k[1], &res, add, prefetch, func(id format.ID) (chunkState, error) { return check(id, true) }); err != nil {
			return vs, err
		}
		vs.Snapshots++
		done[k] = true
		if res.State == VerifyFailed {
			vs.Failed++
		}
		vs.Results = append(vs.Results, res)
		n.recordVerify(ctx, res, now)
	}
	// A chunk found corrupt or missing breaks every snapshot that references it, selected or not.
	if len(badSet) > 0 {
		for _, k := range committed {
			if done[k] {
				continue
			}
			ids, err := n.snapshotChunkIDs(ctx, k[0], k[1])
			if err != nil {
				continue // an unreadable index is a finding of its own, made when that snapshot is verified
			}
			var probs []string
			for _, id := range ids {
				switch badSet[id] {
				case chunkBad:
					probs = append(probs, "chunk-corrupt: "+id.String())
				case chunkGone:
					probs = append(probs, "chunk-missing: "+id.String())
				}
				if len(probs) >= 5 {
					break
				}
			}
			if len(probs) == 0 {
				continue
			}
			res := SnapshotResult{Group: k[0], Snapshot: k[1], State: VerifyFailed, Problems: probs}
			vs.Results = append(vs.Results, res)
			vs.Failed++
			n.recordVerify(ctx, res, now)
		}
	}
	if o.Mode == VerifyAll {
		var names []format.ID
		flush := func() error {
			prefetch(names)
			for _, id := range names {
				if _, ok := lookup(id); ok {
					continue
				}
				if _, err := check(id, false); err != nil {
					return err
				}
			}
			names = names[:0]
			return nil
		}
		err = n.be.List(ctx, chunkDir, func(obj datastore.ObjectInfo) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, ok := chunkIDFromKey(obj.Key)
			if !ok {
				add("chunk-corrupt", obj.Key, "object name is not a chunk id")
				return nil
			}
			names = append(names, id)
			if len(names) >= verifyBatch {
				return flush()
			}
			return nil
		})
		if err == nil {
			err = flush()
		}
		if err != nil {
			return vs, err
		}
		// a chunk the scrub found corrupt after the snapshots were done may still break snapshots
		// that were marked ok: mark them now
		for _, k := range committed {
			ids, err := n.snapshotChunkIDs(ctx, k[0], k[1])
			if err != nil {
				continue
			}
			var probs []string
			for _, id := range ids {
				if s := badSet[id]; s == chunkBad {
					probs = append(probs, "chunk-corrupt: "+id.String())
					break
				}
			}
			if len(probs) > 0 && !failedIn(vs.Results, k) {
				res := SnapshotResult{Group: k[0], Snapshot: k[1], State: VerifyFailed, Problems: probs}
				vs.Results = append(vs.Results, res)
				vs.Failed++
				n.recordVerify(ctx, res, now)
			}
		}
	}
	return vs, nil
}

func failedIn(rs []SnapshotResult, k [2]string) bool {
	for _, r := range rs {
		if r.Group == k[0] && r.Snapshot == k[1] && r.State == VerifyFailed {
			return true
		}
	}
	return false
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (n *Node) snapshotChunkIDs(ctx context.Context, group, snap string) ([]format.ID, error) {
	b, err := datastore.ReadAll(ctx, n.be, snapPrefix(group, snap)+fileIndex, format.MaxIndexSize)
	if err != nil {
		return nil, err
	}
	idx, err := format.ParseIndex(b)
	if err != nil {
		return nil, err
	}
	return idx.ChunkIDs(), nil
}

func (n *Node) verifySnapshot(ctx context.Context, group, snap string, res *SnapshotResult, add func(kind, obj, detail string),
	prefetch func([]format.ID), check func(format.ID) (chunkState, error)) error {
	obj := group + "/" + snap
	fail := func(kind, detail string) {
		res.State = VerifyFailed
		res.Problems = append(res.Problems, kind+": "+detail)
		add(kind, obj, detail)
	}
	prefix := snapPrefix(group, snap)
	rcb, err := datastore.ReadAll(ctx, n.be, prefix+fileCommit, 64<<10)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			res.State = "" // deleted meanwhile: nothing to record
			return nil
		}
		return fmt.Errorf("reading the receipt of %s: %w", obj, err)
	}
	rc, err := format.ParseReceipt(rcb)
	if err != nil {
		fail("receipt-corrupt", err.Error())
		return nil
	}
	ib, err := datastore.ReadAll(ctx, n.be, prefix+fileIndex, format.MaxIndexSize)
	if err != nil {
		if !errors.Is(err, datastore.ErrNotFound) {
			return fmt.Errorf("reading the index of %s: %w", obj, err)
		}
		fail("partial-snapshot", "index unreadable: "+err.Error())
		return nil
	}
	switch {
	case rc.IndexDigest != format.FileDigest(ib):
		fail("index-corrupt", "the index differs from the commit receipt")
		return nil
	}
	idx, err := format.ParseIndex(ib)
	if err != nil {
		fail("index-corrupt", err.Error())
		return nil
	}
	mb, err := datastore.ReadAll(ctx, n.be, prefix+fileManif, format.MaxManifestSize)
	switch {
	case errors.Is(err, datastore.ErrNotFound):
		fail("partial-snapshot", "manifest unreadable: "+err.Error())
	case err != nil:
		return fmt.Errorf("reading the manifest of %s: %w", obj, err)
	case format.VerifyManifestBlob(mb) != nil:
		fail("manifest-corrupt", "trailer mismatch")
	case rc.ManifestDigest != format.FileDigest(mb):
		fail("manifest-corrupt", "the manifest differs from the commit receipt")
	}
	ids := idx.ChunkIDs()
	for b := 0; b < len(ids); b += verifyBatch {
		batch := ids[b:min(b+verifyBatch, len(ids))]
		prefetch(batch)
		for _, id := range batch {
			res.Chunks++
			st, err := check(id)
			if err != nil {
				// a backend error is not evidence of damage: it aborts the job instead of failing the snapshot
				return fmt.Errorf("reading chunk %s: %w", id, err)
			}
			switch st {
			case chunkGone:
				fail("chunk-missing", id.String())
			case chunkBad:
				res.State = VerifyFailed
				res.Problems = append(res.Problems, "chunk-corrupt: "+id.String())
			}
		}
	}
	return nil
}

// recordVerify writes verify.json and the control plane row of a checked snapshot.
func (n *Node) recordVerify(ctx context.Context, res SnapshotResult, at time.Time) {
	if res.State == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	key := snapPrefix(res.Group, res.Snapshot) + fileVerify
	var rec VerifyRecord
	_ = readJSON(ctx, n.be, key, &rec) // an unreadable record counts as never verified
	ev := VerifyEvent{At: at.UTC(), State: res.State, Chunks: res.Chunks, Problems: res.Problems}
	rec.State, rec.At = ev.State, ev.At
	rec.Failed, rec.Verified = ev.State == VerifyFailed, ev.At
	rec.Detail = ""
	if len(ev.Problems) > 0 {
		rec.Detail = ev.Problems[0]
	}
	rec.History = append(rec.History, ev)
	if len(rec.History) > maxVerifyHistory {
		rec.History = rec.History[len(rec.History)-maxVerifyHistory:]
	}
	l := n.snapLock(res.Group, res.Snapshot)
	l.Lock()
	defer l.Unlock()
	if _, err := n.be.Head(ctx, snapPrefix(res.Group, res.Snapshot)+fileCommit); err != nil {
		return // deleted meanwhile
	}
	b, _ := json.Marshal(rec)
	_ = datastore.PutBytes(ctx, n.be, key, b, datastore.PutOptions{})
	_ = n.db.SetVerify(ctx, res.Group, res.Snapshot, res.State, at, res.Problems)
}
