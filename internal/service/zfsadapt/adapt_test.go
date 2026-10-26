package zfsadapt

import (
	"context"
	"slices"
	"testing"

	"jailops.net/jailops/internal/sys/zfs"
)

var bg = context.Background()

func TestAdapterSemantics(t *testing.T) {
	f := zfs.NewFake("tank")
	a := New(f, nil)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.Create(bg, "tank/j", map[string]string{"jailops:managed": "on"}))
	must(a.Create(bg, "tank/j/root", nil))
	must(a.Create(bg, "tank/j/data", nil))
	// Get returns the local value only.
	if v, err := a.Get(bg, "tank/j", "jailops:managed"); err != nil || v != "on" {
		t.Fatalf("%q %v", v, err)
	}
	if v, _ := a.Get(bg, "tank/j/root", "jailops:managed"); v != "" {
		t.Fatalf("a missing property read %q", v)
	}
	// One atomic snapshot over several datasets; names are short.
	must(a.Snapshot(bg, map[string]string{"jailops:comment": "c"}, "tank/j@s1", "tank/j/root@s1", "tank/j/data@s1"))
	if v, _ := a.Get(bg, "tank/j@s1", "jailops:comment"); v != "c" {
		t.Fatalf("snapshot property %q", v)
	}
	if s, err := a.Snapshots(bg, "tank/j/root"); err != nil || !slices.Equal(s, []string{"s1"}) {
		t.Fatalf("%v %v", s, err)
	}
	ds, err := a.Descendants(bg, "tank/j")
	if err != nil || !slices.Equal(ds, []string{"tank/j/data", "tank/j/root"}) {
		t.Fatalf("%v %v", ds, err)
	}
	f.SetWritten("tank/j/root", 4096)
	if n, err := a.Written(bg, "tank/j/root@s1"); err != nil || n != 4096 {
		t.Fatalf("%d %v", n, err)
	}
	// Clone and the clone rule.
	must(a.Clone(bg, "tank/j/root@s1", "tank/j/root.new", nil))
	if c, _ := a.Clones(bg, "tank/j/root@s1"); !slices.Equal(c, []string{"tank/j/root.new"}) {
		t.Fatalf("%v", c)
	}
	if err := a.Destroy(bg, "tank/j/root@s1"); err == nil {
		t.Fatal("destroyed a snapshot with a clone")
	}
	must(a.Rename(bg, "tank/j/root.new", "tank/j/root.next"))
	must(a.DestroyRecursive(bg, "tank/j/root.next"))
	// Holds.
	must(a.Hold(bg, "tank/j/data@s1", "keep"))
	if h, _ := a.Holds(bg, "tank/j/data@s1"); !slices.Equal(h, []string{"keep"}) {
		t.Fatalf("%v", h)
	}
	must(a.Release(bg, "tank/j/data@s1", "keep"))
	// Replicate makes an independent dataset holding the snapshot.
	must(a.Replicate(bg, "tank/j/data@s1", "tank/j/copy", map[string]string{"jailops:name": "x"}))
	if ok, _ := a.Exists(bg, "tank/j/copy@s1"); !ok {
		t.Fatal("the replicated snapshot is missing")
	}
	if v, _ := a.Get(bg, "tank/j/copy", "jailops:name"); v != "x" {
		t.Fatalf("%q", v)
	}
	// Mount goes through the supplied function.
	var mounted []string
	a.MountFn = func(_ context.Context, d string) error { mounted = append(mounted, d); return nil }
	must(a.Mount(bg, "tank/j"))
	if !slices.Equal(mounted, []string{"tank/j"}) {
		t.Fatalf("%v", mounted)
	}
	if err := New(f, nil).Mount(bg, "tank/j"); err == nil {
		t.Fatal("mount without a runner or function succeeded")
	}
}
