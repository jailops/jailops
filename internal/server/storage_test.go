package server_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// The default storage exists from the first start, with the status ZFS reports.
func TestStorageDefaultAndStatus(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	c := ctxT(t)
	st, err := e.Admin.Storage().Get(c, "default")
	if err != nil {
		t.Fatal(err)
	}
	if st.Spec.Dataset != "tank/jailops" || st.Status.Pool != "tank" || st.Status.Health != "ONLINE" || st.Status.Size == 0 {
		t.Fatalf("%+v", st)
	}
	if err := e.ZFS.Set(c, "tank/jailops", map[string]string{"used": "1000", "available": "9000"}); err != nil {
		t.Fatal(err)
	}
	st, _ = e.Admin.Storage().Get(c, "default")
	if st.Status.Used != 1000 || st.Status.Available != 9000 {
		t.Fatalf("%+v", st.Status)
	}
	// There is no default project: nothing creates a project on its own, not even a restart.
	_, err = e.Admin.Projects().Get(c, "default")
	wantCode(t, err, api.CodeNotFound)
	// Restarting does not create a second storage.
	e.Stop()
	e.Start()
	n := 0
	for _, err := range e.Admin.Storage().List(c) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("%d storage after a restart", n)
	}
}

func TestStorageCreateRegistersWithTheHelper(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	c := ctxT(t)
	create := func(name, ds string) error {
		_, err := e.Admin.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: name}, Spec: api.StorageSpec{Dataset: ds, Compression: "lz4"}})
		return err
	}
	wantCode(t, create("fast", "tank/fast"), api.CodeNotFound)                // the dataset must exist
	wantCode(t, create("other", "nopool/data"), api.CodeNotFound)             // on an imported pool
	wantCode(t, create("bad", "tank"), api.CodeInvalid)                       // not a pool root
	wantCode(t, create("bad2", "tank/a b"), api.CodeInvalid)                  // syntax
	wantCode(t, create("over", "tank/jailops/images"), api.CodeAlreadyExists) // inside the default storage
	if err := e.ZFS.Create(c, zfs.CreateOpts{Name: "tank/fast"}); err != nil {
		t.Fatal(err)
	}
	if err := create("fast", "tank/fast"); err != nil {
		t.Fatal(err)
	}
	for _, ds := range []string{"tank/fast/images", "tank/fast/projects", "tank/fast/deleted"} {
		p, err := e.ZFS.Get(c, ds, "jailops:managed")
		if err != nil || p["jailops:managed"].Value != "on" {
			t.Fatalf("%s: %v %v", ds, err, p)
		}
	}
	if p, _ := e.ZFS.Get(c, "tank/fast", "compression"); p["compression"].Value != "lz4" {
		t.Fatalf("%v", p)
	}
	wantCode(t, create("fast", "tank/fast"), api.CodeAlreadyExists)
	// A dataset that has content and is not ours is refused.
	if err := e.ZFS.Create(c, zfs.CreateOpts{Name: "tank/data"}); err != nil {
		t.Fatal(err)
	}
	if err := e.ZFS.Create(c, zfs.CreateOpts{Name: "tank/data/x"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, create("data", "tank/data"), api.CodeInvalid)
	// Encrypted storage is not implemented, and says so.
	_, err := e.Admin.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: "enc"}, Spec: api.StorageSpec{Dataset: "tank/fast2", Encrypted: true}})
	wantCode(t, err, api.CodeUnsupported)
	// The dataset of a storage cannot change; description can.
	st, etag, _ := e.Admin.Storage().GetETag(c, "fast")
	st.Spec.Dataset = "tank/other"
	_, err = e.Admin.Storage().Update(c, "fast", st, etag)
	wantCode(t, err, api.CodeInvalid)
	// A project grants it; then it cannot be deleted.
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "p"}, Spec: api.ProjectSpec{Storage: []api.ProjectStorage{{Storage: "fast"}}}}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.Admin.Storage().Delete(c, "fast"), api.CodeBusy)
	if err := e.Admin.Projects().Delete(c, "p"); err != nil {
		t.Fatal(err)
	}
	if err := e.Admin.Storage().Delete(c, "fast"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.ZFS.Exists(c, "tank/fast/images"); !ok {
		t.Fatal("deleting a storage destroyed its data")
	}
	wantCode(t, e.Admin.Storage().Delete(c, "default"), api.CodeBusy)
	// Only global identities write storage.
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "site"}}); err != nil {
		t.Fatal(err)
	}
	ac := e.Scoped(api.PermAdmin, "site")
	wantCode(t, func() error {
		_, err := ac.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: "z"}, Spec: api.StorageSpec{Dataset: "tank/z"}})
		return err
	}(), api.CodeForbidden)
}

func TestStorageRegistrationWithTheHelper(t *testing.T) {
	reg := &service.FakeRegistrar{}
	e := dtest.New(t, dtest.Options{Registrar: reg})
	c := ctxT(t)
	if err := e.ZFS.Create(c, zfs.CreateOpts{Name: "tank/fast"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: "fast"}, Spec: api.StorageSpec{Dataset: "tank/fast"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Admin.Storage().Delete(c, "fast"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(reg.Calls(), "; ")
	if !strings.Contains(calls, "register tank/jailops") || !strings.Contains(calls, "register tank/fast") || !strings.Contains(calls, "unregister tank/fast") {
		t.Fatalf("%s", calls)
	}
	// A failing helper fails the create and leaves nothing behind.
	reg.RegisterErr = api.Errorf(api.CodeInternal, "helper down")
	if err := e.ZFS.Create(c, zfs.CreateOpts{Name: "tank/slow"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.Admin.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: "slow"}, Spec: api.StorageSpec{Dataset: "tank/slow"}})
	if err == nil {
		t.Fatal("create succeeded although the helper refused")
	}
	if _, err := e.Admin.Storage().Get(c, "slow"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
}

// A jail on another pool clones a generation on its own pool: the generation
// is received there on demand, once.
func TestPerPoolGenerationResolution(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	if err := e.ZFS.Create(c, zfs.CreateOpts{Name: "tank/fast"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: "fast"}, Spec: api.StorageSpec{Dataset: "tank/fast"}}); err != nil {
		t.Fatal(err)
	}
	pr, err := e.Admin.Projects().Get(c, dtest.SeedProject)
	if err != nil {
		t.Fatal(err)
	}
	pr.Spec.Storage = append(pr.Spec.Storage, api.ProjectStorage{Storage: "fast"})
	if _, err := e.Admin.Projects().Update(c, dtest.SeedProject, pr, ""); err != nil {
		t.Fatal(err)
	}
	sp := webSpec()
	sp.Storage = "fast"
	op, err := e.Admin.Jails().Create(c, sdk.NewJail("quick", sp))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(c)
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if j.Spec.Storage != "fast" {
		t.Fatalf("%+v", j.Spec)
	}
	// The generation exists on the second pool, received from the first, and the root is a clone of it.
	if ok, _ := e.ZFS.Exists(context.Background(), "tank/fast/images/fb/g1@ro"); !ok {
		t.Fatal("the generation was not received on the second pool")
	}
	origin, _ := e.ZFS.Origin(context.Background(), "tank/fast/projects/alpha/jails/quick/root")
	if origin != "tank/fast/images/fb/g1@ro" {
		t.Fatalf("origin %q", origin)
	}
	// A second jail there does not receive it again; the first pool's jail uses its own copy.
	createJail(t, e.Admin, "plain")
	origin, _ = e.ZFS.Origin(context.Background(), "tank/jailops/projects/alpha/jails/plain/root")
	if origin != "tank/jailops/images/fb/g1@ro" {
		t.Fatalf("origin %q", origin)
	}
	// The image reports where its generations live.
	im, _ := e.Admin.Images().Get(c, "fb")
	if len(im.Status.Generations) != 1 || strings.Join(im.Status.Generations[0].Storages, ",") != "default,fast" {
		t.Fatalf("%+v", im.Status.Generations)
	}
	// Datasets of the jail are on its storage only.
	if ok, _ := e.ZFS.Exists(context.Background(), "tank/jailops/projects/alpha/jails/quick"); ok {
		t.Fatal("the jail's datasets are on the wrong pool")
	}
	// An upgrade of a jail on the second pool builds on the first and receives on the second.
	bop, err := e.Admin.Images().Build(c, "fb", api.ImageBuild{FromGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	g2, err := bop.Wait(c)
	if err != nil || g2.Number != 2 {
		t.Fatalf("%v %+v", err, g2)
	}
	uop, err := e.Admin.Jails().Upgrade(c, "quick", api.JailUpgrade{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uop.Wait(c); err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if ok, _ := e.ZFS.Exists(context.Background(), "tank/fast/images/fb/g2@ro"); !ok {
		t.Fatal("generation 2 was not received on the second pool")
	}
}
