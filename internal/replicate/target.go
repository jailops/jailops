package replicate

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/internal/retention"
	"jailops.net/jailops/sdk/api"
)

// MinDeletionDelay is the shortest delay NewTarget accepts for admin deletion.
const MinDeletionDelay = time.Hour

// Actor is an authenticated administrator.
type Actor struct {
	Name  string
	Roles []string
}

func (a Actor) has(role string) bool {
	for _, r := range a.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// AuditEvent records a state change or a refused attempt.
type AuditEvent struct {
	Time    time.Time
	Actor   string
	Action  string
	Object  string
	Outcome string // ok, refused, error
	Detail  map[string]string
}

// TargetConfig configures a Target.
type TargetConfig struct {
	// Root is the receive area, for example "tank/jailops/replicas".
	Root    string
	ZFS     ZFS
	Catalog Catalog
	Now     func() time.Time
	// Audit is required; it receives every change and every refusal.
	Audit func(AuditEvent)
	// DeletionDelay is the wait between RequestDeletion and ExecuteDeletion.
	DeletionDelay time.Duration
}

// Target is the receiving side of a replication: it owns the receive area,
// applies the protection rules and is the only place replicas are deleted.
type Target struct {
	cfg   TargetConfig
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewTarget validates cfg.
func NewTarget(cfg TargetConfig) (*Target, error) {
	switch {
	case cfg.Root == "" || cfg.ZFS == nil || cfg.Catalog == nil || cfg.Audit == nil:
		return nil, invalid("target needs Root, ZFS, Catalog and Audit")
	case cfg.DeletionDelay < MinDeletionDelay:
		return nil, invalid("deletion delay %v is below the minimum %v", cfg.DeletionDelay, MinDeletionDelay)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Target{cfg: cfg, locks: map[string]*sync.Mutex{}}, nil
}

func (n *Target) lock(key string) func() {
	n.mu.Lock()
	l := n.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		n.locks[key] = l
	}
	n.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (n *Target) audit(actor, action, object, outcome string, kv ...string) {
	d := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		d[kv[i]] = kv[i+1]
	}
	n.cfg.Audit(AuditEvent{Time: n.cfg.Now(), Actor: actor, Action: action, Object: object, Outcome: outcome, Detail: d})
}

// Dataset returns the receiving dataset for a host and chain. The target, never
// the host, chooses it.
func (n *Target) Dataset(hostUUID, chainID string) (string, error) {
	if !ValidID(hostUUID) || !ValidID(chainID) {
		return "", invalid("invalid host or chain id")
	}
	return n.cfg.Root + "/" + hostUUID + "/" + chainID, nil
}

// ReceiveRequest describes one incoming snapshot.
type ReceiveRequest struct {
	JailUUID, Jail, Policy string
	ChainID                string
	SourceDataset          string
	Snapshot               string
	FromGUID, ToGUID       uint64
	Raw                    bool
	// Size is the declared size of this stream; -1 if unknown.
	Size   int64
	Bundle api.RestoreBundle
	// Encrypted is the host's claim that the dataset is natively encrypted;
	// the target cross-checks it against the received dataset.
	Encrypted bool
	// Keys carries the escrow descriptor. The target never receives a key.
	Keys KeyStatus
}

// ReceiveResult is returned after a verified receive.
type ReceiveResult struct {
	Replica Replica
	GUID    uint64
	Bytes   int64
}

// RecvState is what a sender may learn about a chain on the target.
type RecvState struct {
	Chain  Chain
	Exists bool
	Resume *ResumeInfo
}

// Receiver is the only surface a host has on a target. It has no operation that
// deletes, releases holds, rolls back or forces.
type Receiver interface {
	State(ctx context.Context, chainID string) (RecvState, error)
	Receive(ctx context.Context, req ReceiveRequest, stream io.Reader) (ReceiveResult, error)
	// AbortResume discards a partial receive; it cannot touch a received snapshot.
	AbortResume(ctx context.Context, chainID string) error
}

// ForHost returns a Receiver bound to the authenticated host identity. Every
// path and chain the returned Receiver touches is under that host.
func (n *Target) ForHost(hostUUID string) Receiver { return &hostReceiver{n: n, host: hostUUID} }

type hostReceiver struct {
	n    *Target
	host string
}

func (h *hostReceiver) State(ctx context.Context, chainID string) (RecvState, error) {
	return h.n.state(ctx, h.host, chainID)
}

func (h *hostReceiver) Receive(ctx context.Context, req ReceiveRequest, s io.Reader) (ReceiveResult, error) {
	res, err := h.n.receive(ctx, h.host, req, s)
	if err != nil {
		outcome := "error"
		if e := (*api.Error)(nil); errors.As(err, &e) {
			outcome = "refused"
		}
		h.n.audit("host:"+h.host, "replicate.receive", req.ChainID+"@"+req.Snapshot, outcome, "error", err.Error())
	}
	return res, err
}

func (h *hostReceiver) AbortResume(ctx context.Context, chainID string) error {
	return h.n.abortResume(ctx, h.host, chainID)
}

func (n *Target) state(ctx context.Context, host, chainID string) (RecvState, error) {
	ds, err := n.Dataset(host, chainID)
	if err != nil {
		return RecvState{}, err
	}
	c, ok, err := n.cfg.Catalog.Chain(ctx, host, chainID)
	if err != nil {
		return RecvState{}, err
	}
	st := RecvState{Chain: c, Exists: ok}
	if !ok {
		st.Chain = Chain{ID: chainID, HostUUID: host, Dataset: ds}
	}
	if ex, err := n.cfg.ZFS.Exists(ctx, ds); err != nil {
		return RecvState{}, err
	} else if ex {
		if st.Resume, err = n.cfg.ZFS.ResumeInfo(ctx, ds); err != nil {
			return RecvState{}, err
		}
	}
	return st, nil
}

func (n *Target) abortResume(ctx context.Context, host, chainID string) error {
	ds, err := n.Dataset(host, chainID)
	if err != nil {
		return err
	}
	defer n.lock(ds)()
	ri, err := n.cfg.ZFS.ResumeInfo(ctx, ds)
	if err != nil {
		return err
	}
	if ri == nil {
		return api.Errorf(api.CodeNotFound, "no interrupted receive on chain %q", chainID)
	}
	n.audit("host:"+host, "replicate.abort-resume", chainID, "ok")
	return n.cfg.ZFS.AbortResume(ctx, ds)
}

type countReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n.Add(int64(k))
	return k, err
}

func (n *Target) receive(ctx context.Context, host string, req ReceiveRequest, stream io.Reader) (ReceiveResult, error) {
	if !ValidID(req.Snapshot) || !ValidID(req.ChainID) || req.ToGUID == 0 {
		return ReceiveResult{}, invalid("invalid receive request")
	}
	ds, err := n.Dataset(host, req.ChainID)
	if err != nil {
		return ReceiveResult{}, err
	}
	z := n.cfg.ZFS
	defer n.lock(ds)()
	chain, exists, err := n.cfg.Catalog.Chain(ctx, host, req.ChainID)
	if err != nil {
		return ReceiveResult{}, err
	}
	if exists {
		if err := n.ensureHolds(ctx, chain); err != nil {
			return ReceiveResult{}, err
		}
		if chain.Raw != req.Raw {
			return ReceiveResult{}, invalid("chain %q raw flag mismatch", chain.ID)
		}
		if len(chain.Received) > 0 && req.JailUUID != chain.JailUUID {
			return ReceiveResult{}, api.Errorf(api.CodeForbidden, "chain %q belongs to another jail", chain.ID)
		}
	} else {
		chain = Chain{ID: req.ChainID, HostUUID: host, JailUUID: req.JailUUID, Dataset: ds, Source: req.SourceDataset, Raw: req.Raw}
	}
	if err := chain.CheckContinuity(req.FromGUID); err != nil {
		return ReceiveResult{}, err
	}
	if err := CheckRecoverable(req.Raw, req.Encrypted, req.Keys); err != nil {
		return ReceiveResult{}, err
	}
	dsExists, err := z.Exists(ctx, ds)
	if err != nil {
		return ReceiveResult{}, err
	}
	ri, err := z.ResumeInfo(ctx, ds)
	if err != nil && dsExists {
		return ReceiveResult{}, err
	}
	if dsExists && !exists && ri == nil {
		return ReceiveResult{}, api.Errorf(api.CodeAlreadyExists, "dataset %s exists and is not a JailOps chain", ds)
	}
	if dsExists && exists {
		if mg, _ := z.GetProp(ctx, ds, PropManaged); mg != "on" {
			return ReceiveResult{}, api.Errorf(api.CodeForbidden, "dataset %s is not managed by JailOps", ds)
		}
	}
	var prior int64
	if ri != nil {
		if ri.ToGUID != req.ToGUID || ri.FromGUID != req.FromGUID {
			return ReceiveResult{}, api.Errorf(api.CodeConflict, "an interrupted receive for a different snapshot exists").
				WithHint("abort the interrupted receive first")
		}
		prior = ri.BytesReceived
	}
	if dsExists {
		snaps, err := z.Snapshots(ctx, ds)
		if err != nil {
			return ReceiveResult{}, err
		}
		for _, s := range snaps {
			if s.Name == req.Snapshot {
				return ReceiveResult{}, api.Errorf(api.CodeAlreadyExists, "snapshot %s@%s already exists; receives never overwrite", ds, req.Snapshot)
			}
		}
	}
	if _, dup := chain.Find(req.ToGUID); dup {
		return ReceiveResult{}, broken("guid %d already on chain", req.ToGUID)
	}

	cr := &countReader{r: stream}
	res, rerr := z.Receive(ctx, RecvOpts{Dataset: ds}, cr)
	if rerr != nil {
		return ReceiveResult{}, rerr
	}
	quarantine := func(e error, why string) (ReceiveResult, error) {
		_ = z.Hold(ctx, ds, res.Snapshot, TagQuarantine)
		n.audit("host:"+host, "replicate.verify", req.ChainID+"@"+res.Snapshot, "refused", "reason", why)
		return ReceiveResult{}, e
	}
	if res.Snapshot != req.Snapshot || res.GUID != req.ToGUID {
		return quarantine(broken("received snapshot %s guid %d, expected %s guid %d", res.Snapshot, res.GUID, req.Snapshot, req.ToGUID), "guid")
	}
	if req.Size >= 0 && cr.n.Load() != req.Size {
		return quarantine(invalid("received %d bytes, sender declared %d", cr.n.Load(), req.Size), "size")
	}
	if req.Raw && req.Encrypted {
		// A raw stream must produce an encrypted dataset; otherwise the
		// host's Encrypted claim was false.
		if enc, _ := z.GetProp(ctx, ds, "encryption"); enc == "" || enc == "off" {
			return quarantine(invalid("raw stream produced an unencrypted dataset"), "not-encrypted")
		}
	}
	now := n.cfg.Now()
	if !exists {
		_ = z.SetProp(ctx, ds, PropManaged, "on")
		_ = z.SetProp(ctx, ds, PropChain, chain.ID)
	}
	rec := Received{Snapshot: res.Snapshot, GUID: res.GUID, FromGUID: req.FromGUID, KeyID: req.Keys.KeyID, At: now, Bytes: prior + cr.n.Load()}
	if err := chain.Append(rec); err != nil {
		return quarantine(err, "chain")
	}
	b := Replica{
		Name: ReplicaName(chain.ID, res.Snapshot), CreatedAt: now,
		Spec: ReplicaSpec{Jail: req.Jail, JailUUID: req.JailUUID, HostUUID: host, Policy: req.Policy, Chain: chain.ID,
			Snapshot: res.Snapshot, FromGUID: req.FromGUID, ToGUID: res.GUID, Raw: req.Raw, Bundle: req.Bundle},
		Status: ReplicaStatus{Bytes: rec.Bytes, Verified: true, VerifiedAt: now, Held: true},
	}
	// Record first, then hold: a crash between leaves a recorded, unheld
	// snapshot that ensureHolds repairs, never an unrecorded one.
	if err := n.cfg.Catalog.PutChain(ctx, chain); err != nil {
		return ReceiveResult{}, err
	}
	if err := n.cfg.Catalog.PutReplica(ctx, b); err != nil {
		return ReceiveResult{}, err
	}
	if err := n.ensureHolds(ctx, chain); err != nil {
		return ReceiveResult{}, err
	}
	n.audit("host:"+host, "replicate.receive", b.Name, "ok", "bytes", itoa(uint64(rec.Bytes)))
	return ReceiveResult{Replica: b, GUID: res.GUID, Bytes: rec.Bytes}, nil
}

// ensureHolds makes every live snapshot carry TagKeep and only the last one
// carry TagLast. The new hold is taken before the old one is released, so the
// last common snapshot is never unprotected.
func (n *Target) ensureHolds(ctx context.Context, c Chain) error {
	z := n.cfg.ZFS
	live := c.Live()
	if len(live) == 0 {
		return nil
	}
	has := func(r Received) (map[string]bool, error) {
		h, err := z.Holds(ctx, c.Dataset, r.Snapshot)
		m := map[string]bool{}
		for _, t := range h {
			m[t] = true
		}
		return m, err
	}
	for _, r := range live {
		m, err := has(r)
		if err != nil {
			return err
		}
		if !m[TagKeep] {
			if err := z.Hold(ctx, c.Dataset, r.Snapshot, TagKeep); err != nil {
				return err
			}
		}
	}
	last := live[len(live)-1]
	if m, err := has(last); err != nil {
		return err
	} else if !m[TagLast] {
		if err := z.Hold(ctx, c.Dataset, last.Snapshot, TagLast); err != nil {
			return err
		}
	}
	for _, r := range live[:len(live)-1] {
		m, err := has(r)
		if err != nil {
			return err
		}
		if m[TagLast] {
			if err := z.Release(ctx, c.Dataset, r.Snapshot, TagLast); err != nil {
				return err
			}
		}
	}
	return nil
}

// retentionSet builds the engine input for a chain from the catalog and ZFS.
// Only snapshots recorded in the catalog are candidates; foreign snapshots in
// the dataset are never considered.
func (n *Target) retentionSet(ctx context.Context, c Chain) ([]retention.Snapshot, error) {
	infos, err := n.cfg.ZFS.Snapshots(ctx, c.Dataset)
	if err != nil {
		return nil, err
	}
	created := map[string]time.Time{}
	for _, s := range infos {
		created[s.Name] = s.Created
	}
	last, _ := c.Last()
	var out []retention.Snapshot
	for _, r := range c.Live() {
		t, ok := created[r.Snapshot]
		if !ok {
			continue // vanished outside JailOps; nothing to decide
		}
		holds, err := n.cfg.ZFS.Holds(ctx, c.Dataset, r.Snapshot)
		if err != nil {
			return nil, err
		}
		foreign := false
		for _, h := range holds {
			if h != TagKeep && h != TagLast {
				foreign = true
			}
		}
		out = append(out, retention.Snapshot{Name: r.Snapshot, Time: t, Held: foreign,
			LastCommon: r.GUID == last.GUID, Full: r.Full})
	}
	return out, nil
}

func (n *Target) managedChain(ctx context.Context, host, chainID string) (Chain, error) {
	c, ok, err := n.cfg.Catalog.Chain(ctx, host, chainID)
	if err != nil {
		return Chain{}, err
	}
	if !ok {
		return Chain{}, api.Errorf(api.CodeNotFound, "chain %q not found", chainID)
	}
	if mg, err := n.cfg.ZFS.GetProp(ctx, c.Dataset, PropManaged); err != nil || mg != "on" {
		return Chain{}, api.Errorf(api.CodeForbidden, "dataset %s is not managed by JailOps", c.Dataset)
	}
	return c, nil
}

// destroyReplica removes one snapshot: drop the keep hold, destroy, record. If
// the destroy fails the hold is restored.
func (n *Target) destroyReplica(ctx context.Context, c *Chain, snap string) error {
	z := n.cfg.ZFS
	if err := z.Release(ctx, c.Dataset, snap, TagKeep); err != nil {
		return err
	}
	if err := z.DestroySnapshot(ctx, c.Dataset, snap); err != nil {
		_ = z.Hold(ctx, c.Dataset, snap, TagKeep)
		return err
	}
	for i := range c.Received {
		if c.Received[i].Snapshot == snap {
			c.Received[i].Deleted = true
		}
	}
	if err := n.cfg.Catalog.PutChain(ctx, *c); err != nil {
		return err
	}
	return n.cfg.Catalog.DeleteReplica(ctx, ReplicaName(c.ID, snap))
}

// ApplyRetention evaluates the policy for one chain and deletes what it
// expires. It is the only automatic deleter.
func (n *Target) ApplyRetention(ctx context.Context, host, chainID string, p api.RetentionPolicy, opt retention.Options) (retention.Result, error) {
	ds, err := n.Dataset(host, chainID)
	if err != nil {
		return retention.Result{}, err
	}
	defer n.lock(ds)()
	c, err := n.managedChain(ctx, host, chainID)
	if err != nil {
		return retention.Result{}, err
	}
	if err := n.ensureHolds(ctx, c); err != nil {
		return retention.Result{}, err
	}
	set, err := n.retentionSet(ctx, c)
	if err != nil {
		return retention.Result{}, err
	}
	res := retention.Evaluate(set, p, n.cfg.Now(), opt)
	var errs []error
	for _, d := range res.Delete {
		if err := n.destroyReplica(ctx, &c, d.Snapshot.Name); err != nil {
			errs = append(errs, err)
			continue
		}
		n.audit("retention", "replicate.delete", ReplicaName(c.ID, d.Snapshot.Name), "ok", "reason", "expired")
	}
	return res, errors.Join(errs...)
}

func (n *Target) requireAdmin(a Actor, action, object string) error {
	if !a.has(RoleReplicaAdmin) {
		n.audit(a.Name, action, object, "refused", "reason", "missing role "+RoleReplicaAdmin)
		return api.Errorf(api.CodeForbidden, "deleting replicas needs the %s role", RoleReplicaAdmin)
	}
	return nil
}

func (n *Target) deletable(ctx context.Context, host, chainID, snap string) (Chain, Replica, error) {
	c, err := n.managedChain(ctx, host, chainID)
	if err != nil {
		return Chain{}, Replica{}, err
	}
	b, ok, err := n.cfg.Catalog.Replica(ctx, ReplicaName(chainID, snap))
	if err != nil {
		return Chain{}, Replica{}, err
	}
	if !ok || b.Spec.HostUUID != host {
		return Chain{}, Replica{}, api.Errorf(api.CodeNotFound, "replica %s@%s not found", chainID, snap)
	}
	set, err := n.retentionSet(ctx, c)
	if err != nil {
		return Chain{}, Replica{}, err
	}
	for _, s := range set {
		if s.Name == snap {
			if p := retention.StructuralProtections(s, set); len(p) > 0 {
				return Chain{}, Replica{}, api.Errorf(api.CodeForbidden, "%s@%s is protected: %v", chainID, snap, p)
			}
		}
	}
	return c, b, nil
}

// RequestDeletion is step one of admin deletion. It only marks the replica;
// nothing is destroyed. The deletion cannot run before the target's delay.
func (n *Target) RequestDeletion(ctx context.Context, a Actor, host, chainID, snap, reason string) (time.Time, error) {
	obj := ReplicaName(chainID, snap)
	if err := n.requireAdmin(a, "replicate.request-deletion", obj); err != nil {
		return time.Time{}, err
	}
	ds, err := n.Dataset(host, chainID)
	if err != nil {
		return time.Time{}, err
	}
	defer n.lock(ds)()
	_, b, err := n.deletable(ctx, host, chainID, snap)
	if err != nil {
		n.audit(a.Name, "replicate.request-deletion", obj, "refused", "reason", err.Error())
		return time.Time{}, err
	}
	if b.Status.PendingDeletion != nil {
		return *b.Status.PendingDeletion, nil
	}
	at := n.cfg.Now().Add(n.cfg.DeletionDelay)
	b.Status.PendingDeletion = &at
	if err := n.cfg.Catalog.PutReplica(ctx, b); err != nil {
		return time.Time{}, err
	}
	n.audit(a.Name, "replicate.request-deletion", obj, "ok", "reason", reason, "not_before", at.Format(time.RFC3339))
	return at, nil
}

// CancelDeletion withdraws a pending request.
func (n *Target) CancelDeletion(ctx context.Context, a Actor, host, chainID, snap string) error {
	obj := ReplicaName(chainID, snap)
	if err := n.requireAdmin(a, "replicate.cancel-deletion", obj); err != nil {
		return err
	}
	ds, err := n.Dataset(host, chainID)
	if err != nil {
		return err
	}
	defer n.lock(ds)()
	b, ok, err := n.cfg.Catalog.Replica(ctx, obj)
	if err != nil || !ok || b.Spec.HostUUID != host {
		return api.Errorf(api.CodeNotFound, "replica %s not found", obj)
	}
	b.Status.PendingDeletion = nil
	if err := n.cfg.Catalog.PutReplica(ctx, b); err != nil {
		return err
	}
	n.audit(a.Name, "replicate.cancel-deletion", obj, "ok")
	return nil
}

// ExecuteDeletion is step two. It needs the role, a pending request whose
// delay has passed, and re-checks every structural protection.
func (n *Target) ExecuteDeletion(ctx context.Context, a Actor, host, chainID, snap string) error {
	obj := ReplicaName(chainID, snap)
	if err := n.requireAdmin(a, "replicate.execute-deletion", obj); err != nil {
		return err
	}
	ds, err := n.Dataset(host, chainID)
	if err != nil {
		return err
	}
	defer n.lock(ds)()
	c, b, err := n.deletable(ctx, host, chainID, snap)
	if err != nil {
		n.audit(a.Name, "replicate.execute-deletion", obj, "refused", "reason", err.Error())
		return err
	}
	if b.Status.PendingDeletion == nil {
		n.audit(a.Name, "replicate.execute-deletion", obj, "refused", "reason", "not requested")
		return api.Errorf(api.CodeForbidden, "deletion of %s was not requested", obj)
	}
	if now := n.cfg.Now(); now.Before(*b.Status.PendingDeletion) {
		n.audit(a.Name, "replicate.execute-deletion", obj, "refused", "reason", "delay")
		return api.Errorf(api.CodeBusy, "deletion of %s is not allowed before %s", obj, b.Status.PendingDeletion.Format(time.RFC3339))
	}
	if err := n.destroyReplica(ctx, &c, snap); err != nil {
		n.audit(a.Name, "replicate.execute-deletion", obj, "error", "error", err.Error())
		return err
	}
	n.audit(a.Name, "replicate.execute-deletion", obj, "ok")
	return nil
}

// Pending lists replicas with a pending deletion, soonest first.
func (n *Target) Pending(ctx context.Context, host, chainID string) ([]Replica, error) {
	bs, err := n.cfg.Catalog.Replicas(ctx, host, chainID)
	if err != nil {
		return nil, err
	}
	var out []Replica
	for _, b := range bs {
		if b.Status.PendingDeletion != nil {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Status.PendingDeletion.Before(*out[j].Status.PendingDeletion) })
	return out, nil
}
