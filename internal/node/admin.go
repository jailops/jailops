package node

import (
	"context"
	"errors"
	"time"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/sdk/api"
)

func checkIDs(group, snap string) error {
	if !format.ValidGroupID(group) || !format.ValidSnapshotID(snap) {
		return errf(api.CodeInvalid, "malformed group or snapshot id")
	}
	return nil
}

// requireCommitted refuses a snapshot without a commit.json.
func (n *Node) requireCommitted(ctx context.Context, group, snap string) error {
	if err := checkIDs(group, snap); err != nil {
		return err
	}
	if _, err := n.be.Head(ctx, snapPrefix(group, snap)+fileCommit); err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return errf(api.CodeNotFound, "no such snapshot")
		}
		return asAPIError(err)
	}
	return nil
}

// SetProtected sets or clears the protection marker of a committed snapshot.
// Retention, deletion requests and deletion refuse a protected snapshot.
func (n *Node) SetProtected(ctx context.Context, group, snap string, protected bool, actor string) error {
	if err := n.requireCommitted(ctx, group, snap); err != nil {
		return err
	}
	key := snapPrefix(group, snap) + fileProt
	var err error
	if protected {
		err = datastore.PutBytes(ctx, n.be, key, []byte("protected\n"), datastore.PutOptions{})
	} else {
		err = n.be.Delete(ctx, key)
	}
	if err != nil {
		return asAPIError(err)
	}
	if err := n.db.SetProtected(ctx, group, snap, protected); err != nil && !errors.Is(err, ctl.ErrNotFound) {
		return err
	}
	action := "snapshot.protect"
	if !protected {
		action = "snapshot.unprotect"
	}
	n.audit(ctx, actor, action, group+"/"+snap, nil, "")
	return nil
}

func (n *Node) isProtected(ctx context.Context, group, snap string) (bool, error) {
	_, err := n.be.Head(ctx, snapPrefix(group, snap)+fileProt)
	if errors.Is(err, datastore.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// deleteSnapshot removes a committed snapshot; its chunks are left for GC. The
// commit.json goes first, so the snapshot disappears atomically and a crash
// leaves only orphans, which GC removes after the grace period. A protected
// snapshot is refused. A protection set between the check and the delete wins.
func (n *Node) deleteSnapshot(ctx context.Context, group, snap string) error {
	if err := checkIDs(group, snap); err != nil {
		return err
	}
	prefix := snapPrefix(group, snap)
	l := n.snapLock(group, snap)
	l.Lock()
	defer l.Unlock()
	marker, err := datastore.ReadAll(ctx, n.be, prefix+fileCommit, 64<<10)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return errf(api.CodeNotFound, "no such snapshot")
		}
		return asAPIError(err)
	}
	if p, err := n.isProtected(ctx, group, snap); err != nil {
		return asAPIError(err)
	} else if p {
		return ErrProtected
	}
	if err := n.be.Delete(ctx, prefix+fileCommit); err != nil {
		return asAPIError(err)
	}
	if p, _ := n.isProtected(ctx, group, snap); p {
		if perr := datastore.PutBytes(ctx, n.be, prefix+fileCommit, marker, datastore.PutOptions{IfAbsent: true}); perr != nil && !errors.Is(perr, datastore.ErrExists) {
			return asAPIError(perr)
		}
		return ErrProtected
	}
	for _, f := range []string{fileIndex, fileManif, fileNotes, fileVerify, fileDelete, fileProt} {
		if err := n.be.Delete(ctx, prefix+f); err != nil {
			return asAPIError(err)
		}
	}
	return n.db.DeleteSnapshotRow(ctx, group, snap)
}

// MinDeletionDelay is the shortest wait between a deletion request and its execution.
const MinDeletionDelay = time.Hour

// Deletion is the marker of a pending deletion request.
type Deletion struct {
	RequestedAt time.Time `json:"requested_at"`
	After       time.Time `json:"execute_after"`
	RequestedBy string    `json:"requested_by"`
}

// RequestDeletion is step one of deleting a snapshot by hand: it records the
// request and when it may be executed. Nothing is removed. The delay protects
// against mistakes and against a compromised account. A protected snapshot is refused.
func (n *Node) RequestDeletion(ctx context.Context, group, snap, actor string, delay time.Duration) (Deletion, error) {
	if err := n.requireCommitted(ctx, group, snap); err != nil {
		return Deletion{}, err
	}
	if delay < 0 {
		return Deletion{}, errf(api.CodeInvalid, "negative delay")
	}
	if delay == 0 {
		delay = n.cfg.DeletionDelay
	}
	// The delay is the defence against a compromised admin account, so a request may lengthen it and never
	// shorten it below what the node is configured with (and never below an hour).
	if delay < n.cfg.DeletionDelay || delay < MinDeletionDelay {
		return Deletion{}, errf(api.CodeInvalid, "the deletion delay cannot be shorter than %v", max(n.cfg.DeletionDelay, MinDeletionDelay))
	}
	if p, err := n.isProtected(ctx, group, snap); err != nil {
		return Deletion{}, asAPIError(err)
	} else if p {
		return Deletion{}, errf(api.CodeBusy, "the snapshot is protected: unprotect it first")
	}
	now := n.now().UTC().Truncate(time.Microsecond)
	d := Deletion{RequestedAt: now, After: now.Add(delay), RequestedBy: actor}
	if err := putJSON(ctx, n.be, snapPrefix(group, snap)+fileDelete, d, false); err != nil {
		return d, asAPIError(err)
	}
	if err := n.db.SetPendingDeletion(ctx, group, snap, &d.RequestedAt, &d.After, actor); err != nil && !errors.Is(err, ctl.ErrNotFound) {
		return d, err
	}
	n.audit(ctx, actor, "snapshot.delete.request", group+"/"+snap, map[string]any{"execute_after": d.After}, "")
	return d, nil
}

// CancelDeletion withdraws a deletion request.
func (n *Node) CancelDeletion(ctx context.Context, group, snap, actor string) error {
	if err := n.requireCommitted(ctx, group, snap); err != nil {
		return err
	}
	if err := n.be.Delete(ctx, snapPrefix(group, snap)+fileDelete); err != nil {
		return asAPIError(err)
	}
	if err := n.db.SetPendingDeletion(ctx, group, snap, nil, nil, ""); err != nil && !errors.Is(err, ctl.ErrNotFound) {
		return err
	}
	n.audit(ctx, actor, "snapshot.delete.cancel", group+"/"+snap, nil, "")
	return nil
}

// ExecuteDeletion is step two: it deletes the snapshot if a request exists and its delay has passed.
func (n *Node) ExecuteDeletion(ctx context.Context, group, snap, actor string) error {
	if err := n.requireOnline(); err != nil {
		return err
	}
	if err := n.requireCommitted(ctx, group, snap); err != nil {
		return err
	}
	var d Deletion
	if err := readJSON(ctx, n.be, snapPrefix(group, snap)+fileDelete, &d); err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return errf(api.CodeInvalid, "there is no deletion request for this snapshot")
		}
		return asAPIError(err)
	}
	if now := n.now(); now.Before(d.After) {
		return errf(api.CodeBusy, "the deletion request becomes executable at %s", d.After.Format(time.RFC3339)).WithDetail("execute_after", d.After.Format(time.RFC3339))
	}
	if err := n.deleteSnapshot(ctx, group, snap); err != nil {
		return asAPIError(err)
	}
	n.audit(ctx, actor, "snapshot.delete.execute", group+"/"+snap, map[string]any{"requested_by": d.RequestedBy}, "")
	return nil
}

// DeleteSnapshotNow removes a snapshot without the two-step delay. It exists for
// retention and for tests; the administration API never exposes it.
func (n *Node) deleteNow(ctx context.Context, group, snap, actor string) error {
	err := n.deleteSnapshot(ctx, group, snap)
	if err == nil {
		n.audit(ctx, actor, "snapshot.delete", group+"/"+snap, nil, "")
	}
	return err
}

// SetRetentionPolicy validates and stores a retention policy. scope is
// "default" or a group id.
func (n *Node) SetRetentionPolicy(ctx context.Context, scope string, p api.RetentionPolicy, tz, actor string) error {
	if scope != "default" && !format.ValidGroupID(scope) {
		return errf(api.CodeInvalid, "the scope is %q or a group id", "default")
	}
	for _, v := range []int{p.Last, p.Hourly, p.Daily, p.Weekly, p.Monthly, p.Yearly} {
		if v < 0 || v > 100000 {
			return errf(api.CodeInvalid, "retention counts must be between 0 and 100000")
		}
	}
	if p.Last+p.Hourly+p.Daily+p.Weekly+p.Monthly+p.Yearly == 0 {
		return errf(api.CodeInvalid, "a retention policy that keeps nothing would delete every backup; set at least one count")
	}
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return errf(api.CodeInvalid, "unknown time zone %q", tz)
	}
	if err := n.db.SetRetentionPolicy(ctx, scope, p, tz, actor); err != nil {
		return err
	}
	n.audit(ctx, actor, "retention.policy.set", scope, map[string]any{"policy": p, "timezone": tz}, "")
	return nil
}
