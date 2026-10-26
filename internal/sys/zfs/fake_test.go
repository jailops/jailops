package zfs

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"jailops.net/jailops/sdk/api"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, c api.Code) {
	t.Helper()
	if !api.IsCode(err, c) {
		t.Fatalf("want %s, got %v", c, err)
	}
}

func newPool(t *testing.T) *Fake {
	f := NewFake("tank")
	must(t, f.Create(ctx, CreateOpts{Name: "tank/img", Props: map[string]string{"jailops:managed": "on"}}))
	must(t, f.Create(ctx, CreateOpts{Name: "tank/img/g1"}))
	return f
}

func TestFakeCreateAndProps(t *testing.T) {
	f := newPool(t)
	wantCode(t, f.Create(ctx, CreateOpts{Name: "tank/img"}), api.CodeAlreadyExists)
	wantCode(t, f.Create(ctx, CreateOpts{Name: "tank/x/y"}), api.CodeNotFound)
	must(t, f.Create(ctx, CreateOpts{Name: "tank/x/y", Parents: true}))
	p, err := f.Get(ctx, "tank/img/g1", "jailops:managed", "jailops:other")
	must(t, err)
	if p["jailops:managed"].Value != "on" || p["jailops:managed"].Source != "inherited from tank/img" || p["jailops:other"].Value != "-" {
		t.Fatalf("%+v", p)
	}
	must(t, f.Set(ctx, "tank/img/g1", map[string]string{"jailops:gen": "1"}))
	ds, err := f.List(ctx, ListOpts{Root: "tank/img", Recursive: true, Props: []string{"jailops:gen"}})
	must(t, err)
	if len(ds) != 2 || ds[1].Props["jailops:gen"] != "1" {
		t.Fatalf("%+v", ds)
	}
	must(t, f.Inherit(ctx, "tank/img/g1", "jailops:gen"))
	if p, _ := f.Get(ctx, "tank/img/g1", "jailops:gen"); p["jailops:gen"].Value != "-" {
		t.Fatal("inherit")
	}
}

func TestFakeSnapshotAtomic(t *testing.T) {
	f := newPool(t)
	must(t, f.Create(ctx, CreateOpts{Name: "tank/j"}))
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/j@a"}}))
	// second name already exists: nothing at all may be created
	err := f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@a", "tank/j@a"}})
	wantCode(t, err, api.CodeAlreadyExists)
	if ok, _ := f.Exists(ctx, "tank/img@a"); ok {
		t.Fatal("snapshot call was not atomic")
	}
	wantCode(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@a", "tank/nope@a"}}), api.CodeNotFound)
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@r"}, Recursive: true}))
	if ok, _ := f.Exists(ctx, "tank/img/g1@r"); !ok {
		t.Fatal("recursive")
	}
}

func TestFakeCloneDestroyRules(t *testing.T) {
	f := newPool(t)
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img/g1@ro"}}))
	must(t, f.Clone(ctx, "tank/img/g1@ro", "tank/jail1", nil, false))
	if o, _ := f.Origin(ctx, "tank/jail1"); o != "tank/img/g1@ro" {
		t.Fatal(o)
	}
	cl, _ := f.Clones(ctx, "tank/img/g1@ro")
	if !reflect.DeepEqual(cl, []string{"tank/jail1"}) {
		t.Fatal(cl)
	}
	err := f.Destroy(ctx, "tank/img/g1@ro", DestroyOpts{})
	if !IsHasClones(err) {
		t.Fatalf("want has_clones, got %v", err)
	}
	if !IsHasClones(f.Destroy(ctx, "tank/img/g1", DestroyOpts{Recursive: true})) {
		t.Fatal("dataset destroy must see the clone")
	}
	wantCode(t, f.Destroy(ctx, "tank/img/g1", DestroyOpts{}), api.CodeBusy)

	// deferred destroy waits for the last clone
	must(t, f.Destroy(ctx, "tank/img/g1@ro", DestroyOpts{Defer: true}))
	if ok, _ := f.Exists(ctx, "tank/img/g1@ro"); !ok {
		t.Fatal("deferred snapshot vanished while cloned")
	}
	must(t, f.Destroy(ctx, "tank/jail1", DestroyOpts{}))
	if ok, _ := f.Exists(ctx, "tank/img/g1@ro"); ok {
		t.Fatal("deferred snapshot must go with last clone")
	}
}

func TestFakeDestroyDependents(t *testing.T) {
	f := newPool(t)
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img/g1@ro"}}))
	must(t, f.Clone(ctx, "tank/img/g1@ro", "tank/jail1", nil, false))
	must(t, f.Destroy(ctx, "tank/img/g1", DestroyOpts{Recursive: true, Dependents: true}))
	for _, n := range []string{"tank/img/g1", "tank/jail1", "tank/img/g1@ro"} {
		if ok, _ := f.Exists(ctx, n); ok {
			t.Errorf("%s survived", n)
		}
	}
}

func TestFakeHolds(t *testing.T) {
	f := newPool(t)
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@s"}}))
	must(t, f.Hold(ctx, "keep", "tank/img@s"))
	wantCode(t, f.Hold(ctx, "keep", "tank/img@s"), api.CodeAlreadyExists)
	if h, _ := f.Holds(ctx, "tank/img@s"); len(h) != 1 || h[0].Tag != "keep" {
		t.Fatal(h)
	}
	if !IsHeld(f.Destroy(ctx, "tank/img@s", DestroyOpts{})) {
		t.Fatal("held snapshot destroyed")
	}
	must(t, f.Destroy(ctx, "tank/img@s", DestroyOpts{Defer: true}))
	must(t, f.Release(ctx, "keep", "tank/img@s"))
	if ok, _ := f.Exists(ctx, "tank/img@s"); ok {
		t.Fatal("deferred destroy must complete on release")
	}
	wantCode(t, f.Release(ctx, "keep", "tank/img@s"), api.CodeNotFound)
}

func TestFakeRename(t *testing.T) {
	f := newPool(t)
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img/g1@ro"}}))
	must(t, f.Clone(ctx, "tank/img/g1@ro", "tank/jail1", nil, false))
	must(t, f.Bookmark(ctx, "tank/img/g1@ro", "tank/img/g1#bm"))
	must(t, f.Rename(ctx, "tank/img", "tank/images", RenameOpts{}))
	if o, _ := f.Origin(ctx, "tank/jail1"); o != "tank/images/g1@ro" {
		t.Fatalf("origin not followed: %q", o)
	}
	if ok, _ := f.Exists(ctx, "tank/images/g1#bm"); !ok {
		t.Fatal("bookmark")
	}
	if ok, _ := f.Exists(ctx, "tank/img"); ok {
		t.Fatal("old name")
	}
	must(t, f.Rename(ctx, "tank/images/g1@ro", "tank/images/g1@base", RenameOpts{}))
	if o, _ := f.Origin(ctx, "tank/jail1"); o != "tank/images/g1@base" {
		t.Fatal(o)
	}
	wantCode(t, f.Rename(ctx, "tank/images", "tank/jail1", RenameOpts{}), api.CodeAlreadyExists)
	wantCode(t, f.Rename(ctx, "tank/images", "tank/images/g1/sub", RenameOpts{}), api.CodeInvalid)
}

func TestFakeRollback(t *testing.T) {
	f := newPool(t)
	for _, s := range []string{"a", "b", "c"} {
		must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@" + s}}))
	}
	must(t, f.Clone(ctx, "tank/img@c", "tank/cl", nil, false))
	f.SetWritten("tank/img", 99)
	if !api.IsCode(f.Rollback(ctx, "tank/img@a", RollbackOpts{}), api.CodeBusy) {
		t.Fatal("rollback past newer snapshots needs -r")
	}
	if !IsHasClones(f.Rollback(ctx, "tank/img@a", RollbackOpts{DestroyNewer: true})) {
		t.Fatal("clone of newer snapshot needs -R")
	}
	must(t, f.Rollback(ctx, "tank/img@a", RollbackOpts{DestroyClones: true}))
	for n, want := range map[string]bool{"tank/img@a": true, "tank/img@b": false, "tank/img@c": false, "tank/cl": false} {
		if ok, _ := f.Exists(ctx, n); ok != want {
			t.Errorf("%s exists=%v", n, ok)
		}
	}
	if w, _ := f.Written(ctx, "tank/img", ""); w != 0 {
		t.Error("written after rollback")
	}
}

func TestFakeSendReceiveChain(t *testing.T) {
	src, dst := newPool(t), NewFake("backup")
	must(t, dst.Create(ctx, CreateOpts{Name: "backup/img"}))
	send := func(o SendOpts) []byte {
		var b bytes.Buffer
		must(t, src.Send(ctx, o, &b))
		return b.Bytes()
	}
	must(t, src.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@1"}}))
	must(t, dst.Receive(ctx, ReceiveOpts{Target: "backup/img/x"}, bytes.NewReader(send(SendOpts{Snapshot: "tank/img@1"}))))
	g1, _ := src.GUID(ctx, "tank/img@1")
	if g, _ := dst.GUID(ctx, "backup/img/x@1"); g != g1 {
		t.Fatal("guid must survive send/receive")
	}
	// full stream onto an existing dataset is refused without Force
	wantCode(t, dst.Receive(ctx, ReceiveOpts{Target: "backup/img/x"}, bytes.NewReader(send(SendOpts{Snapshot: "tank/img@1"}))), api.CodeAlreadyExists)

	must(t, src.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@2"}}))
	must(t, src.Bookmark(ctx, "tank/img@1", "tank/img#b1"))
	inc := send(SendOpts{Snapshot: "tank/img@2", From: "tank/img#b1"})
	// destination modified: chain broken unless forced
	dst.SetWritten("backup/img/x", 5)
	wantCode(t, dst.Receive(ctx, ReceiveOpts{Target: "backup/img/x"}, bytes.NewReader(inc)), api.CodeChainBroken)
	must(t, dst.Receive(ctx, ReceiveOpts{Target: "backup/img/x", Force: true}, bytes.NewReader(inc)))
	if ok, _ := dst.Exists(ctx, "backup/img/x@2"); !ok {
		t.Fatal("incremental not received")
	}
	// source that skips a snapshot does not continue the chain
	must(t, src.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@3", "tank/img@4"}[:1]}))
	must(t, src.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@4"}}))
	wantCode(t, dst.Receive(ctx, ReceiveOpts{Target: "backup/img/x"}, bytes.NewReader(send(SendOpts{Snapshot: "tank/img@4", From: "tank/img@3"}))), api.CodeChainBroken)
	wantCode(t, dst.Receive(ctx, ReceiveOpts{Target: "backup/img/x"}, bytes.NewReader([]byte("garbage\n"))), api.CodeInvalid)
}

func TestFakeListTypes(t *testing.T) {
	f := newPool(t)
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@a"}}))
	must(t, f.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/img@b"}}))
	ds, err := f.List(ctx, ListOpts{Root: "tank/img", Types: []string{"snapshot"}})
	must(t, err)
	if len(ds) != 2 || ds[0].Name != "tank/img@a" || ds[0].Type != "snapshot" {
		t.Fatalf("%+v", ds)
	}
	ds, _ = f.List(ctx, ListOpts{})
	if len(ds) != 3 {
		t.Fatalf("%+v", ds)
	}
	ds, _ = f.List(ctx, ListOpts{Root: "tank", Depth: 1})
	if len(ds) != 2 {
		t.Fatalf("depth: %+v", ds)
	}
	_, err = f.List(ctx, ListOpts{Root: "tank/none"})
	wantCode(t, err, api.CodeNotFound)
}

func TestFakeFeaturesAndPools(t *testing.T) {
	f := NewFake("tank")
	f.SetFeatures("tank", "bookmarks")
	ft, err := f.ActiveFeatures(ctx, "tank")
	if err != nil || !reflect.DeepEqual(ft, []string{"bookmarks"}) {
		t.Fatal(ft, err)
	}
	_, err = f.ActiveFeatures(ctx, "nope")
	wantCode(t, err, api.CodeNotFound)
	if ps, _ := f.ListPools(ctx); len(ps) != 1 {
		t.Fatal(ps)
	}
}
