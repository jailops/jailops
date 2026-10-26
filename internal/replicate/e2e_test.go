package replicate

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/retention"
	"jailops.net/jailops/sdk/api"
)

func TestChainOfFullAndIncrementals(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	g2 := e.snap("s2", 2)
	e.snap("s3", 3)

	r := e.push("s1")
	if r.Plan.Kind != KindFull || !r.Replica.Status.Verified || r.Replica.Spec.FromGUID != 0 {
		t.Fatalf("%+v", r.Plan)
	}
	r = e.push("s2")
	if r.Plan.Kind != KindIncremental || r.Replica.Spec.ToGUID != g2 || r.Replica.Spec.FromGUID == 0 {
		t.Fatalf("%+v", r.Plan)
	}
	e.push("s3")

	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s2,s3" {
		t.Fatalf("node snapshots %s", got)
	}
	// Retained snapshots are held; only the last is the last-common.
	for sn, want := range map[string]string{"s1": "jailops-keep", "s2": "jailops-keep", "s3": "jailops-keep,jailops-last"} {
		if got := strings.Join(holdsOf(t, e.nodeZ, e.chainDS(), sn), ","); got != want {
			t.Errorf("%s holds %q want %q", sn, got, want)
		}
	}
	// Source: base bookmark of the head exists, older ones pruned, no leftover holds.
	bms, _ := e.host.Bookmarks(ctx, src)
	if len(bms) != 1 || !IsOwnBookmark("web", bms[0].Name) {
		t.Fatalf("bookmarks %+v", bms)
	}
	for _, sn := range []string{"s1", "s2", "s3"} {
		if h := holdsOf(t, e.host, src, sn); len(h) != 0 {
			t.Errorf("source %s still held %v", sn, h)
		}
	}
	// Old source snapshots can now go; the next incremental uses the bookmark.
	for _, sn := range []string{"s1", "s2", "s3"} {
		if err := e.host.DestroySnapshot(ctx, src, sn); err != nil {
			t.Fatal(err)
		}
	}
	e.snap("s4", 4)
	r = e.push("s4")
	if r.Plan.Kind != KindIncremental || !r.Plan.From.Bookmark {
		t.Fatalf("from bookmark expected: %+v", r.Plan.From)
	}
	// Up to date.
	if r := e.push("s4"); !r.UpToDate {
		t.Fatal("not noop")
	}
	// Catalog agrees.
	c, ok, _ := e.cat.Chain(ctx, hostA, "web")
	if !ok || len(c.Received) != 4 || c.Received[3].Snapshot != "s4" {
		t.Fatalf("%+v", c)
	}
	if b, ok, _ := e.cat.Replica(ctx, "web@s4"); !ok || !b.Status.Held {
		t.Fatal("replica record")
	}
	// Managed marker set on the node dataset.
	if v, _ := e.nodeZ.GetProp(ctx, e.chainDS(), PropManaged); v != "on" {
		t.Fatal("not marked managed")
	}
}

func TestPullDoesNotTouchSource(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	j := e.job()
	j.Snapshot = "s1"
	if _, err := Pull(ctx, j); err != nil {
		t.Fatal(err)
	}
	j.Snapshot = "s2"
	r, err := Pull(ctx, j)
	if err != nil || r.Plan.Kind != KindIncremental {
		t.Fatal(err)
	}
	for _, l := range e.host.Log() {
		t.Errorf("pull changed the source: %s", l)
	}
}

func TestPushHoldOrderingOnSource(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	e.host.ClearLog()
	e.push("s2")
	log := e.host.Log()
	idx := func(prefix string) int {
		for i, l := range log {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	if !(idx("hold s2") >= 0 && idx("hold s2") < idx("bookmark s2") && idx("bookmark s2") < idx("release s2") && idx("bookmark s2") < idx("release s1")) {
		t.Fatalf("bad order: %v", log)
	}
}

func TestSourceHoldsReleasedOnFailure(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	j := e.job()
	j.Snapshot = "s2"
	j.Receiver = failingReceiver{e.node.ForHost(hostA)}
	if _, err := Push(ctx, j); err == nil {
		t.Fatal("expected failure")
	}
	for _, sn := range []string{"s1", "s2"} {
		if h := holdsOf(t, e.host, src, sn); len(h) != 0 {
			t.Errorf("%s leaked hold %v", sn, h)
		}
	}
}

type failingReceiver struct{ Receiver }

func (failingReceiver) Receive(context.Context, ReceiveRequest, io.Reader) (ReceiveResult, error) {
	return ReceiveResult{}, api.Errorf(api.CodeBusy, "node busy")
}

func TestBrokenChainAfterHostRestore(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	e.push("s2")

	// The host is restored from an older state: its disk has s1 only, then
	// takes new snapshots, one with a colliding name.
	old := e.host
	e.host = newMem()
	e.host.Snap(src, "s1", epoch.Add(24*time.Hour), true) // new guid, same name
	e.host.Snap(src, "s5", epoch.Add(5*24*time.Hour), true)
	_ = old

	j := e.job()
	j.Snapshot = "s5"
	r, err := Push(ctx, j)
	wantCode(t, err, api.CodeChainBroken)
	if r.Plan.Kind != "" {
		t.Fatal("plan returned with error")
	}
	// Nothing changed on the node.
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s2" {
		t.Fatalf("node %s", got)
	}
	// Explicit policy: start a new chain with a full send; the old chain stays.
	j.OnBroken = NewChainOnBroken
	r, err = Push(ctx, j)
	if err != nil || r.Plan.Kind != KindFull || r.Plan.ChainID == "web" {
		t.Fatalf("%+v %v", r.Plan, err)
	}
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s2" {
		t.Fatalf("old chain touched: %s", got)
	}
	if got := e.nodeZ.snapNames(root + "/" + hostA + "/" + r.Plan.ChainID); got != "s5" {
		t.Fatalf("new chain %s", got)
	}
}

func TestInterruptedTransferResumes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attempts  int
		firstFull bool
	}{{"incremental resumed by a second call", 0, false}, {"full resumed by a second call", 0, true}, {"resumed inside one call", 2, false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.snap("s1", 1)
			e.snap("s2", 2)
			target := "s2"
			if !tc.firstFull {
				e.push("s1")
			} else {
				target = "s1"
			}
			e.host.failSend = 100 << 10
			j := e.job()
			j.Snapshot = target
			j.ResumeAttempts = tc.attempts
			r, err := Push(ctx, j)
			if tc.attempts > 0 {
				if err != nil || !r.Resumed {
					t.Fatalf("%+v %v", r, err)
				}
			} else {
				if err == nil || !r.Resumable {
					t.Fatalf("want resumable failure: %+v %v", r, err)
				}
				// Nothing was recorded or protected for the partial snapshot.
				c, _, _ := e.cat.Chain(ctx, hostA, "web")
				for _, x := range c.Received {
					if x.Snapshot == target {
						t.Fatal("partial recorded")
					}
				}
				if len(e.host.Log()) > 0 {
					for _, l := range e.host.Log() {
						if strings.HasPrefix(l, "bookmark "+target) {
							t.Fatal("bookmark taken for a failed transfer")
						}
					}
				}
				r, err = Push(ctx, j)
				if err != nil || !r.Resumed {
					t.Fatalf("resume failed: %+v %v", r, err)
				}
			}
			if r.Replica.Spec.ToGUID == 0 || !r.Replica.Status.Verified {
				t.Fatal("not verified")
			}
			if got := e.nodeZ.snapNames(e.chainDS()); !strings.HasSuffix(got, target) {
				t.Fatalf("%s", got)
			}
			if h := strings.Join(holdsOf(t, e.nodeZ, e.chainDS(), target), ","); h != "jailops-keep,jailops-last" {
				t.Fatalf("holds %s", h)
			}
		})
	}
}

func TestInterruptedThenDifferentSnapshotAbortsPartial(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	e.host.failSend = 50 << 10
	j := e.job()
	j.Snapshot = "s2"
	if _, err := Push(ctx, j); err == nil {
		t.Fatal("expected failure")
	}
	e.snap("s3", 3)
	j.Snapshot = "s3" // the partial state is for s2, so it must be dropped
	r, err := Push(ctx, j)
	if err != nil || !r.Plan.AbortResume || r.Resumed {
		t.Fatalf("%+v %v", r, err)
	}
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s3" {
		t.Fatalf("%s", got)
	}
}

// ---- compromised host ----

func TestCompromisedHostCannotDeleteOrOverwrite(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	e.push("s2")
	before := e.nodeZ.snapNames(e.chainDS())
	headBefore, _, _ := e.cat.Chain(ctx, hostA, "web")

	// The Receiver surface has no way to delete, release, roll back or force.
	rt := reflect.TypeOf((*Receiver)(nil)).Elem()
	for i := 0; i < rt.NumMethod(); i++ {
		n := strings.ToLower(rt.Method(i).Name)
		for _, bad := range []string{"delete", "destroy", "release", "rollback", "force", "retention"} {
			if strings.Contains(n, bad) {
				t.Fatalf("Receiver exposes %s", rt.Method(i).Name)
			}
		}
	}
	if reflect.TypeOf(RecvOpts{}).NumField() != 1 {
		t.Fatal("RecvOpts grew a field; make sure it is not a force flag")
	}

	rcv := e.node.ForHost(hostA)
	evil := newMem()
	evil.Snap(src, "s1", epoch, true) // different guids than the real s1
	evil.Snap(src, "s9", epoch.Add(9*24*time.Hour), true)
	good := func(snap string, from uint64) (ReceiveRequest, io.Reader) {
		s, err := evil.Send(ctx, SendOpts{Dataset: src, Snapshot: snap, Raw: true})
		if err != nil {
			t.Fatal(err)
		}
		g := evilGUID(evil, snap)
		return ReceiveRequest{JailUUID: "jail-1", Jail: "web", ChainID: "web", Snapshot: snap, FromGUID: from, ToGUID: g, Raw: true,
			Encrypted: true, Keys: escrowed(), Size: s.Size}, s
	}
	lastGUID := headBefore.Received[1].GUID

	type attempt struct {
		name string
		do   func() error
		code api.Code
	}
	attempts := []attempt{
		{"full stream over the existing chain", func() error {
			req, s := good("s1", 0)
			_, err := rcv.Receive(ctx, req, s)
			return err
		}, api.CodeChainBroken},
		{"incremental from a guid the node never saw", func() error {
			req, s := good("s9", evilGUID(evil, "s1"))
			_, err := rcv.Receive(ctx, req, s)
			return err
		}, api.CodeChainBroken},
		{"reusing an existing snapshot name from the right base", func() error {
			evil.Snap(src, "s2", epoch.Add(10*24*time.Hour), true)
			req, s := good("s2", lastGUID)
			_, err := rcv.Receive(ctx, req, s)
			return err
		}, api.CodeAlreadyExists},
		{"path traversal in the chain id", func() error {
			req, s := good("s9", 0)
			req.ChainID = "../../other"
			_, err := rcv.Receive(ctx, req, s)
			return err
		}, api.CodeInvalid},
		{"state query with a bad chain id", func() error { _, err := rcv.State(ctx, "a/b"); return err }, api.CodeInvalid},
		{"raw without escrow", func() error {
			req, s := good("s9", lastGUID)
			req.Keys = KeyStatus{State: KeyPresent}
			_, err := rcv.Receive(ctx, req, s)
			return err
		}, api.CodeInvalid},
		{"another jail's chain", func() error {
			req, s := good("s9", lastGUID)
			req.JailUUID = "jail-evil"
			_, err := rcv.Receive(ctx, req, s)
			return err
		}, api.CodeForbidden},
		{"abort a resume that does not exist (cannot be used to delete)", func() error { return rcv.AbortResume(ctx, "web") }, api.CodeNotFound},
		{"non-admin deletion request", func() error {
			_, err := e.node.RequestDeletion(ctx, Actor{Name: "host:" + hostA}, hostA, "web", "s1", "cleanup")
			return err
		}, api.CodeForbidden},
		{"deletion execute without request", func() error {
			return e.node.ExecuteDeletion(ctx, Actor{Name: "root", Roles: []string{RoleReplicaAdmin}}, hostA, "web", "s1")
		}, api.CodeForbidden},
		{"retention for a chain that is not JailOps's", func() error {
			e.nodeZ.Snap(root+"/"+hostA+"/foreign", "x", epoch, false)
			e.cat.PutChain(ctx, Chain{ID: "foreign", HostUUID: hostA, Dataset: root + "/" + hostA + "/foreign"})
			_, err := e.node.ApplyRetention(ctx, hostA, "foreign", api.RetentionPolicy{Last: 1}, retention.Options{})
			return err
		}, api.CodeForbidden},
	}
	for _, a := range attempts {
		err := a.do()
		if !api.IsCode(err, a.code) {
			t.Errorf("%s: want %s got %v", a.name, a.code, err)
		}
	}
	// Direct ZFS deletion of a held snapshot fails (this is what a root
	// compromise on the node would still face, short of removing holds).
	if err := e.nodeZ.DestroySnapshot(ctx, e.chainDS(), "s2"); err == nil {
		t.Fatal("held snapshot destroyed")
	}
	if got := e.nodeZ.snapNames(e.chainDS()); got != before {
		t.Fatalf("node state changed: %s -> %s", before, got)
	}
	headAfter, _, _ := e.cat.Chain(ctx, hostA, "web")
	if !reflect.DeepEqual(headBefore, headAfter) {
		t.Fatal("chain record changed")
	}
	if e.audited("replicate.receive", "refused") < 5 {
		t.Fatalf("refusals not audited: %+v", e.audit)
	}
	// Another host cannot reach this host's chain: it gets its own namespace.
	other := e.node.ForHost("host-b")
	st, err := other.State(ctx, "web")
	if err != nil || st.Exists {
		t.Fatalf("%+v %v", st, err)
	}
}

func evilGUID(z *memZFS, snap string) uint64 {
	s, _ := z.Snapshots(ctx, src)
	for _, x := range s {
		if x.Name == snap {
			return x.GUID
		}
	}
	return 0
}

func TestVerificationFailuresQuarantine(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	// Declared size lies.
	j := e.job()
	j.Snapshot = "s2"
	j.Sender = sizeLiar{LocalSource{e.host}}
	_, err := Push(ctx, j)
	wantCode(t, err, api.CodeInvalid)
	if h := strings.Join(holdsOf(t, e.nodeZ, e.chainDS(), "s2"), ","); h != TagQuarantine {
		t.Fatalf("quarantine hold missing: %s", h)
	}
	c, _, _ := e.cat.Chain(ctx, hostA, "web")
	if len(c.Received) != 1 {
		t.Fatal("unverified snapshot recorded")
	}
	// A claimed-encrypted raw stream of an unencrypted dataset is caught.
	e2 := newEnv(t)
	e2.host.Snap(src, "p1", epoch, false)
	j2 := e2.job()
	_, err = Push(ctx, j2)
	wantCode(t, err, api.CodeInvalid)
}

type sizeLiar struct{ LocalSource }

func (s sizeLiar) Send(ctx context.Context, o SendOpts) (Stream, error) {
	st, err := s.LocalSource.Send(ctx, o)
	st.Size++
	return st, err
}

// ---- retention and deletion on the node ----

func seedChain(e *env, n int) {
	for i := 1; i <= n; i++ {
		e.snap("s"+string(rune('0'+i)), i)
		e.push("s" + string(rune('0'+i)))
	}
}

func TestRetentionIsTheOnlyDeleter(t *testing.T) {
	e := newEnv(t)
	seedChain(e, 6)
	// An operator hold on s3 protects it.
	if err := e.nodeZ.Hold(ctx, e.chainDS(), "s3", "operator"); err != nil {
		t.Fatal(err)
	}
	res, err := e.node.ApplyRetention(ctx, hostA, "web", api.RetentionPolicy{Last: 2}, retention.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// s1 is the only full: protected. s3 held. s5, s6 last. s2 and s4 go.
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s3,s5,s6" {
		t.Fatalf("%s (delete=%v)", got, res.Delete)
	}
	c, _, _ := e.cat.Chain(ctx, hostA, "web")
	if !c.Received[1].Deleted || !c.Received[3].Deleted || c.Received[0].Deleted {
		t.Fatalf("%+v", c.Received)
	}
	if _, ok, _ := e.cat.Replica(ctx, "web@s2"); ok {
		t.Fatal("replica record kept")
	}
	if e.audited("replicate.delete", "ok") != 2 {
		t.Fatal("deletions not audited")
	}
	// The chain still continues from the last common snapshot.
	e.snap("s7", 7)
	if r := e.push("s7"); r.Plan.Kind != KindIncremental {
		t.Fatal("chain broken by retention")
	}
	// Idempotent.
	res, err = e.node.ApplyRetention(ctx, hostA, "web", api.RetentionPolicy{Last: 2}, retention.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Delete {
		if d.Snapshot.Name == "s7" || d.Snapshot.Name == "s6" {
			t.Fatal("deleted head")
		}
	}
}

func TestSafetyWindowProtectsRecent(t *testing.T) {
	e := newEnv(t)
	seedChain(e, 3)
	e.now = epoch.Add(3*24*time.Hour + time.Hour)
	_, err := e.node.ApplyRetention(ctx, hostA, "web", api.RetentionPolicy{Last: 1}, retention.Options{SafetyWindow: 5 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s2,s3" {
		t.Fatal(got)
	}
}

func TestAdminDeletionTwoStep(t *testing.T) {
	e := newEnv(t)
	seedChain(e, 4)
	admin := Actor{Name: "alice", Roles: []string{RoleReplicaAdmin}}

	// Structural protections apply to admins too.
	_, err := e.node.RequestDeletion(ctx, admin, hostA, "web", "s4", "oops")
	wantCode(t, err, api.CodeForbidden) // last common
	_, err = e.node.RequestDeletion(ctx, admin, hostA, "web", "s1", "oops")
	wantCode(t, err, api.CodeForbidden) // only full

	at, err := e.node.RequestDeletion(ctx, admin, hostA, "web", "s2", "contains leaked data")
	if err != nil {
		t.Fatal(err)
	}
	if want := e.now.Add(48 * time.Hour); !at.Equal(want) {
		t.Fatalf("%v", at)
	}
	// Nothing deleted yet; a second request does not extend or shorten.
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s2,s3,s4" {
		t.Fatal(got)
	}
	// Too early.
	e.now = e.now.Add(47 * time.Hour)
	wantCode(t, e.node.ExecuteDeletion(ctx, admin, hostA, "web", "s2"), api.CodeBusy)
	// Wrong role.
	e.now = e.now.Add(2 * time.Hour)
	wantCode(t, e.node.ExecuteDeletion(ctx, Actor{Name: "bob", Roles: []string{"viewer"}}, hostA, "web", "s2"), api.CodeForbidden)
	// Cancel and re-request, then execute after the delay.
	if err := e.node.CancelDeletion(ctx, admin, hostA, "web", "s2"); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.node.ExecuteDeletion(ctx, admin, hostA, "web", "s2"), api.CodeForbidden)
	if _, err := e.node.RequestDeletion(ctx, admin, hostA, "web", "s2", "again"); err != nil {
		t.Fatal(err)
	}
	pend, _ := e.node.Pending(ctx, hostA, "web")
	if len(pend) != 1 {
		t.Fatal("pending list")
	}
	e.now = e.now.Add(49 * time.Hour)
	if err := e.node.ExecuteDeletion(ctx, admin, hostA, "web", "s2"); err != nil {
		t.Fatal(err)
	}
	if got := e.nodeZ.snapNames(e.chainDS()); got != "s1,s3,s4" {
		t.Fatal(got)
	}
	if e.audited("replicate.request-deletion", "ok") != 2 || e.audited("replicate.execute-deletion", "ok") != 1 ||
		e.audited("replicate.execute-deletion", "refused") < 3 {
		t.Fatalf("audit: %+v", e.audit)
	}
}

func TestNewNodeValidation(t *testing.T) {
	_, err := NewTarget(TargetConfig{Root: root, ZFS: newMem(), Catalog: NewMemCatalog(), Audit: func(AuditEvent) {}, DeletionDelay: time.Minute})
	wantCode(t, err, api.CodeInvalid)
	_, err = NewTarget(TargetConfig{Root: root, ZFS: newMem(), Catalog: NewMemCatalog(), DeletionDelay: 48 * time.Hour})
	wantCode(t, err, api.CodeInvalid)
}

// ---- encryption ----

func TestRawRefusedWithoutEscrow(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	for name, k := range map[string]KeyStatus{
		"none":    {State: KeyNone},
		"present": {State: KeyPresent, KeyID: "k1"},
		"zero":    {},
	} {
		j := e.job()
		j.Keys = k
		_, err := Push(ctx, j)
		if !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "escrow") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ok, _ := e.nodeZ.Exists(ctx, e.chainDS()); ok {
		t.Fatal("node touched")
	}
	// A key rotation after escrow without re-escrowing is also refused.
	j := e.job()
	j.Keys.History = append(j.Keys.History, KeyEntry{KeyID: "k2", From: epoch.Add(time.Hour)})
	_, err := Push(ctx, j)
	wantCode(t, err, api.CodeInvalid)
}

func TestUnencryptedReplicationNeedsNoKey(t *testing.T) {
	e := newEnv(t)
	e.host.Snap(src, "p1", epoch, false)
	j := e.job()
	j.Snapshot = "p1"
	j.Raw, j.Encrypted, j.Keys = false, false, KeyStatus{}
	r, err := Push(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if r.Replica.Spec.Raw {
		t.Fatal("replica recorded as raw")
	}
	// The chain keeps its mode: a raw stream cannot continue it.
	e.snap("s2", 2)
	j = e.job()
	j.Snapshot = "s2"
	_, err = Push(ctx, j)
	wantCode(t, err, api.CodeInvalid)
}

func TestRawEncryptedTargetNeedsNoKey(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	e.snap("s2", 2)
	e.push("s1")
	e.push("s2")
	if v, _ := e.nodeZ.GetProp(ctx, e.chainDS(), "encryption"); v == "" || v == "off" {
		t.Fatal("received dataset is not encrypted")
	}
	// Receive and retention above ran with no key anywhere on the node: the
	// node API has no key parameter at all.
	rt := reflect.TypeOf(TargetConfig{})
	for i := 0; i < rt.NumField(); i++ {
		if strings.Contains(strings.ToLower(rt.Field(i).Name), "key") {
			t.Fatal("node config has key material")
		}
	}
}

func TestCompatHook(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	j := e.job()
	j.Compat = fixedCompat{res: CompatResult{Verdict: VerdictNo, Reasons: []string{"x"}}}
	_, err := Push(ctx, j)
	wantCode(t, err, api.CodeZFSIncompatible)
	if ok, _ := e.nodeZ.Exists(ctx, e.chainDS()); ok {
		t.Fatal("transfer started despite NO")
	}
	for _, c := range []CompatChecker{fixedCompat{res: CompatResult{Verdict: VerdictUnknown}}, fixedCompat{err: io.ErrUnexpectedEOF}, fixedCompat{res: CompatResult{Verdict: VerdictVerified}}, nil} {
		e2 := newEnv(t)
		e2.snap("s1", 1)
		j := e2.job()
		j.Compat = c
		r, err := Push(ctx, j)
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		if c == nil || c.(fixedCompat).res.Verdict != VerdictVerified {
			if r.Compat.Verdict != VerdictUnknown {
				t.Fatalf("%+v", r.Compat)
			}
		}
	}
}

type fixedCompat struct {
	res CompatResult
	err error
}

func (f fixedCompat) Check(context.Context, CompatRequest) (CompatResult, error) { return f.res, f.err }

// ---- streaming ----

func TestStreamIsNotBuffered(t *testing.T) {
	e := newEnv(t)
	e.snap("s1", 1)
	cs := &countingSender{LocalSource: LocalSource{e.host}}
	j := e.job()
	j.Sender = cs
	if _, err := Push(ctx, j); err != nil {
		t.Fatal(err)
	}
	if cs.maxRead > copyBuf {
		t.Fatalf("read of %d bytes exceeds the copy buffer", cs.maxRead)
	}
	if cs.total < 300<<10 {
		t.Fatal("stream too small to prove anything")
	}
}

type countingSender struct {
	LocalSource
	maxRead int
	total   int64
}

func (c *countingSender) Send(ctx context.Context, o SendOpts) (Stream, error) {
	s, err := c.LocalSource.Send(ctx, o)
	s.ReadCloser = &spy{ReadCloser: s.ReadCloser, c: c}
	return s, err
}

type spy struct {
	io.ReadCloser
	c *countingSender
}

func (s *spy) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if n > s.c.maxRead {
		s.c.maxRead = n
	}
	s.c.total += int64(n)
	return n, err
}
