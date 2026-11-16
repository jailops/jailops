package node_test

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/nodetest"
	"jailops.net/jailops/internal/node/wire"
	"jailops.net/jailops/sdk/api"
)

func TestEscrowBindsTheKeyDomainOnFirstUse(t *testing.T) {
	e := nodetest.NewEnv(t, nodetest.Options{})
	a, b := e.Host(t, "a"), e.Host(t, "b")
	const x, y = "5e62dd65bc023787", "0123456789abcdef"
	blob := []byte("age-encryption.org/v1\n-> X25519 stanza\nblob")
	if err := e.Node.PutEscrow(ctx, a, x, bytes.NewReader(blob), int64(len(blob))); err != nil {
		t.Fatal(err)
	}
	if got := e.Node.Info().KeyDomain; got != x {
		t.Fatalf("the repository is bound to %q", got)
	}
	// another host cannot park an escrow of another domain in this repository
	if err := e.Node.PutEscrow(ctx, b, y, bytes.NewReader(blob), int64(len(blob))); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a second domain was accepted: %v", err)
	}
	// and a session for another key is refused from now on
	f := nodetest.NewFixture(t, 21)
	if _, err := e.Node.BeginWith(ctx, b, wire.BeginRequest{Group: f.Group("b", "j"), Snapshot: nodetest.SnapshotID(e.Clock.Now(), 1), KeyDomain: y}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a session with another key domain: %v", err)
	}
}

func TestHostsSeeOnlyTheirGroupsAndReportOnlyOnThem(t *testing.T) {
	e := nodetest.NewEnv(t, nodetest.Options{})
	owner, other := e.Host(t, "owner"), e.Host(t, "other")
	recovery := e.Host(t, "recovery", "backup-pull", "restore")
	f := nodetest.NewFixture(t, 22)
	g := f.Group("owner", "j")
	snap := nodetest.SnapshotID(e.Clock.Now(), 1)
	f.NewBackup(t, g, snap, []nodetest.Chunk{f.NewChunk(t)}).Upload(t, e.Node, owner, "")

	get := func(h node.Host, path string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", wire.Base+path, nil).WithContext(node.WithHost(ctx, h))
		e.Node.Handler().ServeHTTP(w, r)
		return w.Code
	}
	for _, p := range []string{"/groups/" + g + "/snapshots/" + snap, "/groups/" + g + "/snapshots/" + snap + "/manifest"} {
		if c := get(owner, p); c != 200 {
			t.Errorf("owner %s: %d", p, c)
		}
		if c := get(recovery, p); c != 200 {
			t.Errorf("recovery host %s: %d", p, c)
		}
		if c := get(other, p); c != 404 {
			t.Errorf("another host %s: %d", p, c)
		}
	}
	if gs, err := e.Node.GroupsOf(ctx, other); err != nil || len(gs) != 0 {
		t.Fatalf("another host lists %v %v", gs, err)
	}
	if gs, err := e.Node.GroupsOf(ctx, owner); err != nil || len(gs) != 1 {
		t.Fatalf("the owner lists %v %v", gs, err)
	}

	// restore-test reports: not for another host's group, not for a snapshot that does not exist
	rt := node.RestoreTest{Group: g, Snapshot: snap, Method: "deep", OK: false, Problems: []string{"x"}}
	if err := e.Node.RecordRestoreTest(ctx, other, rt); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("another host reported on the group: %v", err)
	}
	ghost := rt
	ghost.Snapshot = nodetest.SnapshotID(e.Clock.Now(), 9)
	if err := e.Node.RecordRestoreTest(ctx, owner, ghost); err == nil {
		t.Fatal("a report on a snapshot nobody committed was recorded")
	}
	if jobs, _ := e.DB.Jobs(ctx, "restore-test", 50); len(jobs) != 0 {
		t.Fatalf("%d jobs were written by refused reports", len(jobs))
	}
	if err := e.Node.RecordRestoreTest(ctx, recovery, rt); err != nil {
		t.Fatal(err)
	}
}

// Every host has an escrow slot of its own: a host that stores a blob first cannot pre-empt another host's.
func TestEscrowSlotsBelongToTheirHost(t *testing.T) {
	e := nodetest.NewEnv(t, nodetest.Options{})
	squatter, real := e.Host(t, "squatter"), e.Host(t, "real")
	const d = "5e62dd65bc023787"
	junk := []byte("age-encryption.org/v1\n-> X25519 junk")
	good := []byte("age-encryption.org/v1\n-> X25519 stanza\nthe real blob")
	if err := e.Node.PutEscrow(ctx, squatter, d, bytes.NewReader(junk), int64(len(junk))); err != nil {
		t.Fatal(err)
	}
	if err := e.Node.PutEscrow(ctx, real, d, bytes.NewReader(good), int64(len(good))); err != nil {
		t.Fatalf("the real host was refused: %v", err)
	}
	if got, err := e.Node.GetEscrow(ctx, real, d); err != nil || !bytes.Equal(got, good) {
		t.Fatalf("%q %v", got, err)
	}
	if l, _ := e.Node.ListEscrow(ctx, real.ID); len(l) != 1 {
		t.Fatalf("%v", l)
	}
	other := e.Host(t, "other")
	if _, err := e.Node.GetEscrow(ctx, other, d); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("a host without a blob reads another's: %v", err)
	}
	if err := e.Node.DeleteEscrow(ctx, d, "admin", ""); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.Node.ListEscrow(ctx, ""); len(l) != 0 {
		t.Fatalf("%v", l)
	}
}

// A host that has used its quota is refused with quota_exceeded; another host is not affected.
func TestHostQuota(t *testing.T) {
	f := nodetest.NewFixture(t, 23)
	c0, c1 := f.NewChunk(t), f.NewChunk(t)
	e := nodetest.NewEnv(t, nodetest.Options{Configure: func(c *node.Config) { c.HostQuota = int64(len(c0.Blob)) + 1 }})
	a, b := e.Host(t, "a"), e.Host(t, "b")
	sa, err := e.Node.Begin(ctx, a, f.Group("a", "j"), nodetest.SnapshotID(e.Clock.Now(), 1), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Node.PutChunk(ctx, a, sa.ID, c0.ID, bytes.NewReader(c0.Blob), int64(len(c0.Blob))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Node.PutChunk(ctx, a, sa.ID, c1.ID, bytes.NewReader(c1.Blob), int64(len(c1.Blob))); !api.IsCode(err, api.CodeQuotaExceeded) {
		t.Fatalf("over the quota: %v", err)
	}
	sb, err := e.Node.Begin(ctx, b, f.Group("b", "j"), nodetest.SnapshotID(e.Clock.Now(), 2), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Node.PutChunk(ctx, b, sb.ID, c0.ID, bytes.NewReader(c0.Blob), int64(len(c0.Blob))); err != nil {
		t.Fatalf("another host: %v", err)
	}
}
