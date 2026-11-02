package priv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Commit confirmed (docs/appliance.md): a change that can cut the administrator off is applied together with a
// pending record, <host-dir>/pending.json, written and synced before the host changes. It names the snapshot to put
// back and the deadline. The helper's own timer puts the snapshot back at the deadline unless the change was
// confirmed; the daemon can confirm or revert, never move the deadline. The record survives a restart of the helper
// (ResumePending re-arms the timer, or reverts at once when the deadline passed) and a reboot (the rc.d script
// jailops_revert runs RevertPendingAtBoot before netif and pf). One change waits at a time; while it waits every other
// change of the host is refused as busy. How the last one ended is kept in pending.last.json.

// hostPendingRec is pending.json.
type hostPendingRec struct {
	ID            string    `json:"id"`
	Module        string    `json:"module"`
	Snapshot      string    `json:"snapshot"`
	Created       time.Time `json:"created"`
	Deadline      time.Time `json:"deadline"`
	ConfirmWithin int       `json:"confirm_within"`
}

func newPendingID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (l *Local) readPending() (*hostPendingRec, error) {
	var r hostPendingRec
	ok, err := l.readHostJSON("pending.json", &r)
	if err != nil || !ok {
		return nil, err
	}
	if !slices.Contains(api.HostModules, r.Module) || api.ValidHostSnapshotName(r.Snapshot) != nil || r.Snapshot == api.HostSnapshotLatest {
		return nil, fmt.Errorf("%s/pending.json: not a pending record of the helper", l.cfg.HostDir)
	}
	return &r, nil
}

func (l *Local) readLastOutcome() *api.HostPendingOutcome {
	var o api.HostPendingOutcome
	if ok, err := l.readHostJSON("pending.last.json", &o); err != nil || !ok {
		return nil
	}
	return &o
}

func (l *Local) pendingView(r hostPendingRec) api.HostPendingChange {
	left := int(math.Ceil(r.Deadline.Sub(l.now()).Seconds()))
	if left < 0 {
		left = 0
	}
	return api.HostPendingChange{ID: r.ID, Module: r.Module, Snapshot: r.Snapshot, Created: r.Created, Deadline: r.Deadline,
		SecondsLeft: left, ConfirmWithin: r.ConfirmWithin}
}

// busyPending is the refusal of a change while another one waits for its confirmation.
func (l *Local) busyPending(r hostPendingRec) *api.Error {
	return api.Errorf(api.CodeBusy, "a %s change (id %s) waits for its confirmation until %s", r.Module, r.ID, r.Deadline.UTC().Format(time.RFC3339)).
		WithHint("confirm it with `jailops host confirm`, or undo it with `jailops host revert`, then try again").
		WithDetail("pending", r.ID).WithDetail("module", r.Module)
}

// SetHostNote sets where the helper logs what happens to a pending change without a request (a timeout, a timer
// re-armed after a restart): its audit log. Call it before serving.
func (l *Local) SetHostNote(f func(msg string, a ...any)) {
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	l.hostNote = f
}

func (l *Local) note(msg string, attrs ...any) {
	if l.hostNote != nil {
		l.hostNote(msg, attrs...)
	}
}

// armPending starts the timer of a record (hostMu held).
func (l *Local) armPending(r hostPendingRec) {
	if l.pendStop != nil {
		l.pendStop()
	}
	d := r.Deadline.Sub(l.now())
	if d < 0 {
		d = 0
	}
	after := l.d.AfterFunc
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	id := r.ID
	l.pendStop = after(d, func() { l.pendingTimeout(id) })
}

func (l *Local) disarm() {
	if l.pendStop != nil {
		l.pendStop()
		l.pendStop = nil
	}
}

// pendingTimeout is the timer: the change of record id was not confirmed in time.
func (l *Local) pendingTimeout(id string) {
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	r, err := l.readPending()
	if err != nil || r == nil || r.ID != id {
		return // confirmed or reverted meanwhile
	}
	l.pendStop = nil
	_, _ = l.revertPending(context.Background(), *r, api.HostPendingTimedOut, "nobody confirmed it within "+fmt.Sprint(r.ConfirmWithin)+" seconds", true)
}

// revertPending puts the snapshot of r back (hostMu held). effective loads it (pfctl); at boot only the files go back
// and the rc.d scripts that follow load them. The record goes, the outcome is kept.
func (l *Local) revertPending(ctx context.Context, r hostPendingRec, outcome, detail string, effective bool) (*api.HostPendingOutcome, error) {
	l.disarm()
	_, err := l.restoreSnapshot(ctx, r.Snapshot, false, false, effective, true)
	o := &api.HostPendingOutcome{ID: r.ID, Module: r.Module, Snapshot: r.Snapshot, Outcome: outcome, Time: l.now().UTC(), Detail: detail}
	if err != nil {
		o.Outcome = api.HostPendingRevertFailed
		o.Detail = fmt.Sprintf("%s; putting snapshot %s back failed: %v (restore it with jailops host restore %s)", detail, r.Snapshot, err, r.Snapshot)
	}
	if werr := l.writeHostJSON("pending.last.json", o); werr != nil && err == nil {
		err = werr
	}
	// The record goes even when the revert failed: a second attempt by the timer would fail the same way, and the
	// outcome names the snapshot to put back by hand.
	if rerr := l.writeHostJSON("pending.json", (*hostPendingRec)(nil)); rerr != nil && err == nil {
		err = rerr
	}
	l.note("host.pending."+o.Outcome, "id", r.ID, "module", r.Module, "snapshot", r.Snapshot, "detail", o.Detail)
	return o, err
}

// HostPending implements Host. It needs no assertion.
func (l *Local) HostPending(ctx context.Context) (api.HostPending, error) {
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	return l.pendingState()
}

func (l *Local) pendingState() (api.HostPending, error) {
	out := api.HostPending{Last: l.readLastOutcome()}
	r, err := l.readPending()
	if err != nil {
		return out, err
	}
	if r != nil {
		v := l.pendingView(*r)
		out.Pending = &v
	}
	return out, nil
}

// pendingFor finds the record a confirmation or a revert means.
func (l *Local) pendingFor(id string) (*hostPendingRec, error) {
	if err := api.ValidHostPendingID(id); err != nil {
		return nil, err
	}
	r, err := l.readPending()
	if err != nil {
		return nil, err
	}
	if r == nil {
		e := api.Errorf(api.CodeNotFound, "no host change waits for a confirmation")
		if o := l.readLastOutcome(); o != nil && (id == "" || o.ID == id) {
			e = api.Errorf(api.CodeNotFound, "no host change waits for a confirmation; the last one (%s, id %s) was %s at %s",
				o.Module, o.ID, o.Outcome, o.Time.UTC().Format(time.RFC3339))
		}
		return nil, e
	}
	if id != "" && id != r.ID {
		return nil, api.Errorf(api.CodeConflict, "the pending change is %s, not %s", r.ID, id).WithHint("check it with `jailops host pending`")
	}
	return r, nil
}

// HostPendingConfirm implements Host: the change stays, the timer stops.
func (l *Local) HostPendingConfirm(ctx context.Context, id string) (api.HostPending, error) {
	if !isAdmin(ctx) {
		return api.HostPending{}, forbidden("confirming a host change needs the admin assertion")
	}
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	r, err := l.pendingFor(id)
	if err != nil {
		return api.HostPending{}, err
	}
	if err := l.hostAllowed(ctx, r.Module); err != nil {
		return api.HostPending{}, err
	}
	l.disarm()
	o := &api.HostPendingOutcome{ID: r.ID, Module: r.Module, Snapshot: r.Snapshot, Outcome: api.HostPendingConfirmed, Time: l.now().UTC()}
	if err := l.writeHostJSON("pending.last.json", o); err != nil {
		l.armPending(*r)
		return api.HostPending{}, err
	}
	if err := l.writeHostJSON("pending.json", (*hostPendingRec)(nil)); err != nil {
		l.armPending(*r)
		return api.HostPending{}, err
	}
	l.note("host.pending.confirmed", "id", r.ID, "module", r.Module)
	return l.pendingState()
}

// HostPendingRevert implements Host: the snapshot goes back now. It needs the admin assertion but not the module:
// undoing is always allowed.
func (l *Local) HostPendingRevert(ctx context.Context, id string) (api.HostPending, error) {
	if !isAdmin(ctx) {
		return api.HostPending{}, forbidden("reverting a host change needs the admin assertion")
	}
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	r, err := l.pendingFor(id)
	if err != nil {
		return api.HostPending{}, err
	}
	if _, err := l.revertPending(ctx, *r, api.HostPendingReverted, "reverted on request", true); err != nil {
		return api.HostPending{}, err
	}
	return l.pendingState()
}

// ResumePending is called when the helper starts, before it accepts a request: a record whose deadline passed while
// the helper was not running is reverted at once; otherwise its timer is armed for the time left.
func (l *Local) ResumePending(ctx context.Context) (*api.HostPendingOutcome, error) {
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	r, err := l.readPending()
	if err != nil || r == nil {
		return nil, err
	}
	if !l.now().Before(r.Deadline) {
		return l.revertPending(ctx, *r, api.HostPendingTimedOut, "the deadline passed while the helper was not running", true)
	}
	l.armPending(*r)
	l.note("host.pending.rearmed", "id", r.ID, "module", r.Module, "deadline", r.Deadline.Format(time.RFC3339))
	return nil, nil
}

// RevertPendingAtBoot is what the rc.d script jailops_revert runs before netif and pf: an unconfirmed change never
// survives a reboot. Only the files go back; the network and pf start with them.
func (l *Local) RevertPendingAtBoot(ctx context.Context) (*api.HostPendingOutcome, error) {
	l.hostMu.Lock()
	defer l.hostMu.Unlock()
	r, err := l.readPending()
	if err != nil || r == nil {
		return nil, err
	}
	return l.revertPending(ctx, *r, api.HostPendingRevertedAtBoot, "the host restarted before the change was confirmed", false)
}
