package zfsmodel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/internal/lifecycle/fault"
)

var ctx = context.Background()

func mk(t *testing.T) *Model {
	m := New(&fault.Injector{}, "tank")
	for _, n := range []string{"tank/a", "tank/b"} {
		if err := m.Create(ctx, n, nil); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("want error containing %q, got %v", sub, err)
	}
}

func TestCloneOriginAndDestroy(t *testing.T) {
	m := mk(t)
	m.Snapshot(ctx, nil, "tank/a@ro")
	if err := m.Clone(ctx, "tank/a@ro", "tank/c", nil); err != nil {
		t.Fatal(err)
	}
	wantErr(t, m.Destroy(ctx, "tank/a@ro"), "dependent clones")
	wantErr(t, m.DestroyRecursive(ctx, "tank/a"), "dependent clones")
	wantErr(t, m.Destroy(ctx, "tank/a"), "children")
	// rename follows origin
	if err := m.Rename(ctx, "tank/a", "tank/a2"); err != nil {
		t.Fatal(err)
	}
	if o, _ := m.Origin(ctx, "tank/c"); o != "tank/a2@ro" {
		t.Fatal(o)
	}
	m.Destroy(ctx, "tank/c")
	if err := m.DestroyRecursive(ctx, "tank/a2"); err != nil {
		t.Fatal(err)
	}
}

func TestHoldBlocksDestroy(t *testing.T) {
	m := mk(t)
	m.Snapshot(ctx, nil, "tank/a@ro")
	m.Hold(ctx, "tank/a@ro", "x")
	wantErr(t, m.Destroy(ctx, "tank/a@ro"), "busy")
	wantErr(t, m.DestroyRecursive(ctx, "tank/a"), "busy")
	m.Release(ctx, "tank/a@ro", "x")
	if err := m.Destroy(ctx, "tank/a@ro"); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackAndWritten(t *testing.T) {
	m := mk(t)
	m.WriteFile("tank/a", "f", "one")
	m.Snapshot(ctx, nil, "tank/a@s1")
	m.WriteFile("tank/a", "f", "twotwo")
	m.Snapshot(ctx, nil, "tank/a@s2")
	if w, _ := m.Written(ctx, "tank/a@s1"); w != 6 {
		t.Fatal(w)
	}
	wantErr(t, m.Rollback(ctx, "tank/a@s1", false), "more recent")
	if err := m.Rollback(ctx, "tank/a@s1", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := m.ReadFile("tank/a", "f"); c != "one" {
		t.Fatal(c)
	}
	if s, _ := m.Snapshots(ctx, "tank/a"); len(s) != 1 {
		t.Fatal(s)
	}
	// later snapshot with a clone blocks -r
	m.Snapshot(ctx, nil, "tank/a@s3")
	m.Clone(ctx, "tank/a@s3", "tank/cl", nil)
	wantErr(t, m.Rollback(ctx, "tank/a@s1", true), "clones")
}

func TestAtomicSnapshot(t *testing.T) {
	m := mk(t)
	m.Snapshot(ctx, nil, "tank/b@x")
	before := m.Dump()
	wantErr(t, m.Snapshot(ctx, nil, "tank/a@x", "tank/b@x"), "exists")
	wantErr(t, m.Snapshot(ctx, nil, "tank/a@x", "tank/zz@x"), "does not exist")
	if m.Dump() != before {
		t.Fatal("partial snapshot")
	}
}

func TestInjectedFailureChangesNothing(t *testing.T) {
	m := mk(t)
	before := m.Dump()
	m.F.Reset(1)
	if err := m.Create(ctx, "tank/z", nil); !errors.Is(err, fault.ErrInjected) {
		t.Fatal(err)
	}
	if m.Dump() != before {
		t.Fatal("changed")
	}
}

func TestReplicateIndependent(t *testing.T) {
	m := mk(t)
	m.WriteFile("tank/a", "f", "x")
	m.Snapshot(ctx, nil, "tank/a@s1")
	m.Snapshot(ctx, nil, "tank/a@s2")
	if err := m.Replicate(ctx, "tank/a@s1", "tank/r", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := m.Origin(ctx, "tank/r"); o != "" {
		t.Fatal(o)
	}
	if s, _ := m.Snapshots(ctx, "tank/r"); len(s) != 1 {
		t.Fatal(s)
	}
	if err := m.Destroy(ctx, "tank/a@s1"); err != nil {
		t.Fatal("source snapshot must be free:", err)
	}
}

func TestRenameChildrenAndExisting(t *testing.T) {
	m := mk(t)
	m.Create(ctx, "tank/a/k", nil)
	wantErr(t, m.Rename(ctx, "tank/a", "tank/b"), "exists")
	if err := m.Rename(ctx, "tank/a", "tank/n"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Exists(ctx, "tank/n/k"); !ok {
		t.Fatal("child not moved")
	}
}
