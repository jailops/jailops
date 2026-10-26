package node

import (
	"context"
	"errors"
	"sort"
	"time"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/sdk/api"
)

// ReconcileStats reports a reconcile.
type ReconcileStats struct {
	Groups  int `json:"groups"`
	Added   int `json:"snapshots_added"`
	Updated int `json:"snapshots_updated"`
	Removed int `json:"snapshots_removed"`
	// Unreadable counts snapshots whose receipt could not be parsed; they are left out of the index.
	Unreadable int `json:"unreadable"`
}

// Reconcile rebuilds the control plane's view of the repository from the
// repository listing: groups, committed snapshots with their receipts, the
// protection marker, the deletion request marker and the verification record.
// Rows for snapshots that no longer exist are removed. Users, tokens,
// enrolments, policies and the audit log are not in the repository and are not
// touched.
func (n *Node) Reconcile(ctx context.Context, actor string) (*ReconcileStats, ctl.Job, error) {
	var st *ReconcileStats
	job, err := n.runJob(ctx, ctl.JobReconcile, actor, nil, func(ctx context.Context) (any, error) {
		var e error
		st, e = n.reconcile(ctx)
		return st, e
	})
	return st, job, err
}

// StartReconcile runs Reconcile in the background.
func (n *Node) StartReconcile(actor string) (ctl.Job, error) {
	return n.startAsync(ctl.JobReconcile, actor, nil, func(ctx context.Context) (any, error) { return n.reconcile(ctx) })
}

func (n *Node) reconcile(ctx context.Context) (*ReconcileStats, error) {
	st := &ReconcileStats{}
	type flags struct{ prot, del, verify bool }
	found := map[[2]string]*flags{}
	groups := map[string]bool{}
	err := n.be.List(ctx, groupsDir, func(o datastore.ObjectInfo) error {
		g, s, f, ok := splitSnapKey(o.Key)
		if !ok {
			return nil
		}
		k := [2]string{g, s}
		fl := found[k]
		if fl == nil {
			fl = &flags{}
			found[k] = fl
		}
		switch f {
		case fileCommit:
			groups[g] = true
		case fileProt:
			fl.prot = true
		case fileDelete:
			fl.del = true
		case fileVerify:
			fl.verify = true
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	// only snapshots with a commit.json exist
	var keys [][2]string
	for k := range found {
		if groups[k[0]] {
			if _, err := n.be.Head(ctx, snapPrefix(k[0], k[1])+fileCommit); err == nil {
				keys = append(keys, k)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	have := map[[2]string]ctl.Snapshot{}
	rows, err := n.db.Snapshots(ctx, "")
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		have[[2]string{r.GroupID, r.SnapshotID}] = r
	}
	seenGroup := map[string]bool{}
	live := map[[2]string]bool{}
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		fl := found[k]
		prefix := snapPrefix(k[0], k[1])
		rcb, err := datastore.ReadAll(ctx, n.be, prefix+fileCommit, 64<<10)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				continue
			}
			return st, err
		}
		rc, perr := format.ParseReceipt(rcb)
		if perr != nil {
			st.Unreadable++
			continue
		}
		snap := ctl.Snapshot{GroupID: k[0], SnapshotID: k[1], CommittedAt: rc.Committed, ChunkCount: rc.ChunkCount, IndexBytes: rc.IndexBytes,
			IndexDigest: rc.IndexDigest, ManifestDigest: rc.ManifestDigest, Source: rc.Source, Protected: fl.prot}
		if fl.verify {
			var rec VerifyRecord
			if readJSON(ctx, n.be, prefix+fileVerify, &rec) == nil && rec.State == "" && !rec.Verified.IsZero() {
				// written by the repository store, not by a node
				rec.State, rec.At = VerifyOK, rec.Verified
				if rec.Failed {
					rec.State = VerifyFailed
				}
			}
			if rec.State == VerifyOK || rec.State == VerifyFailed {
				snap.VerifyState = rec.State
				at := rec.At
				snap.VerifiedAt = &at
				if len(rec.History) > 0 {
					snap.VerifyProblems = rec.History[len(rec.History)-1].Problems
				}
			}
		}
		if fl.del {
			var d Deletion
			if readJSON(ctx, n.be, prefix+fileDelete, &d) == nil {
				req, after := d.RequestedAt, d.After
				snap.PendingDeletionRequestedAt, snap.PendingDeletionAfter, snap.PendingDeletionBy = &req, &after, d.RequestedBy
			}
		}
		if err := n.db.UpsertSnapshot(ctx, snap); err != nil {
			return st, err
		}
		live[k] = true
		seenGroup[k[0]] = true
		if _, ok := have[k]; ok {
			st.Updated++
		} else {
			st.Added++
		}
	}
	for k := range have {
		if !live[k] {
			if err := n.db.DeleteSnapshotRow(ctx, k[0], k[1]); err != nil {
				return st, err
			}
			st.Removed++
		}
	}
	// group ownership comes from the owner markers; a host that is not enrolled (yet) leaves the group unowned
	for g := range groups {
		var o GroupOwner
		if readJSON(ctx, n.be, groupOwnerKey(g), &o) != nil {
			continue
		}
		if h, err := n.db.HostByName(ctx, o.Host); err == nil {
			_ = n.db.SetGroupOwner(ctx, g, h.ID)
		}
	}
	st.Groups = len(seenGroup)
	return st, nil
}

// ---- status ---------------------------------------------------------------------

// Usage is the last known size of the chunk area (measured by GC).
type Usage struct {
	Chunks int64     `json:"chunks"`
	Bytes  int64     `json:"bytes"`
	At     time.Time `json:"at"`
}

// Status describes the repository for the API and the CLI.
func (n *Node) Status(ctx context.Context) (RepositoryStatus, error) {
	st := RepositoryStatus{ID: n.meta.ID, Mode: n.be.Mode().String(), Backend: datastore.Unwrap(n.raw).Kind()}
	stats, err := n.db.Stats(ctx)
	if err != nil {
		return st, err
	}
	st.Snapshots = stats.Snapshots
	st.Groups = stats.Groups
	st.Failed, st.Protected, st.PendingDeletion = stats.Failed, stats.Protected, stats.Pending
	var u Usage
	if err := n.db.Setting(ctx, "usage", &u); err == nil {
		st.Chunks, st.Bytes = u.Chunks, u.Bytes
	}
	if j, err := n.db.LastJob(ctx, ctl.JobGC); err == nil && j.FinishedAt != nil && j.State == ctl.JobSucceeded {
		st.LastGC = *j.FinishedAt
	}
	if j, err := n.db.LastJob(ctx, ctl.JobVerify); err == nil && j.FinishedAt != nil && j.State == ctl.JobSucceeded {
		st.LastVerify = *j.FinishedAt
	}
	if q, err := n.ListQuarantineNames(ctx); err == nil {
		st.Quarantined = q
	}
	open, err := n.db.OpenSessions(ctx)
	if err == nil {
		st.OpenSessions = len(open)
	}
	return st, nil
}

// RepositoryStatus is api.RepositoryStatus plus the counters the management UI shows.
type RepositoryStatus struct {
	ID              string    `json:"id"`
	Mode            string    `json:"mode"`
	Backend         string    `json:"backend"`
	Snapshots       int       `json:"snapshots"`
	Chunks          int64     `json:"chunks"`
	Bytes           int64     `json:"bytes"`
	LastGC          time.Time `json:"last_gc,omitzero"`
	LastVerify      time.Time `json:"last_verify,omitzero"`
	Quarantined     int       `json:"quarantined"`
	OpenSessions    int       `json:"open_sessions"`
	Groups          int       `json:"groups"`
	Failed          int       `json:"failed_snapshots"`
	Protected       int       `json:"protected_snapshots"`
	PendingDeletion int       `json:"pending_deletions"`
}

// API returns the wire type.
func (s RepositoryStatus) API() api.RepositoryStatus {
	return api.RepositoryStatus{ID: s.ID, Mode: s.Mode, Backend: s.Backend, Snapshots: s.Snapshots, Chunks: s.Chunks, Bytes: s.Bytes,
		LastGC: s.LastGC, LastVerify: s.LastVerify, Quarantined: s.Quarantined, OpenSessions: s.OpenSessions}
}

// ListQuarantineNames counts the quarantined objects without reading them.
func (n *Node) ListQuarantineNames(ctx context.Context) (int, error) {
	c := 0
	err := n.be.List(ctx, quarDir, func(datastore.ObjectInfo) error { c++; return nil })
	return c, err
}
