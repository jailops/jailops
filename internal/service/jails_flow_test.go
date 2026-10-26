package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// These tests drive the service directly (no HTTP) on the in-memory ZFS, jail
// and file fakes of dtest. They cover the paths the HTTP contract tests only
// reach indirectly: scope checks, error mapping and rollback of create.

func newEnv(t *testing.T) (*dtest.Env, *service.Service) {
	t.Helper()
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	return e, e.D.Service()
}

// as returns a context for an identity acting in a project ("" for none).
func as(a service.Actor, project string) context.Context {
	ctx := service.WithActor(context.Background(), a)
	if project != "" {
		ctx = service.WithProject(ctx, project)
	}
	return ctx
}

var (
	globalAdmin = service.Actor{Name: "root", Kind: "user", Perm: api.PermAdmin, Global: true}
	alphaOnly   = service.Actor{Name: "ann", Kind: "user", Perm: api.PermAdmin, Projects: []string{dtest.SeedProject}}
	nobody      = service.Actor{Name: "nobody", Kind: "user", Perm: api.PermAdmin}
)

func wait(t *testing.T, s *service.Service, op api.Operation) api.Operation {
	t.Helper()
	got, err := s.WaitOperation(context.Background(), op.ID, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantCode(t *testing.T, err error, code api.Code) {
	t.Helper()
	if err == nil || !api.IsCode(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestCreateJailNeedsAProjectAndScope(t *testing.T) {
	_, s := newEnv(t)
	j := dtest.WebJail("web")
	_, err := s.CreateJail(as(globalAdmin, ""), j)
	wantCode(t, err, api.CodeInvalid) // there is no default project
	_, err = s.CreateJail(as(nobody, dtest.SeedProject), j)
	if err == nil {
		t.Fatal("an identity with no scope created a jail")
	}
	_, err = s.CreateJail(as(globalAdmin, "ghost"), j)
	wantCode(t, err, api.CodeNotFound)
	j.Metadata.Project = "other"
	_, err = s.CreateJail(as(globalAdmin, dtest.SeedProject), j)
	if err == nil {
		t.Fatal("a body naming another project than the request was accepted")
	}
}

func TestCreateJailRefusesWhatIsNotImplemented(t *testing.T) {
	_, s := newEnv(t)
	for name, mod := range map[string]func(*api.Jail){
		"packages":  func(j *api.Jail) { j.Spec.Packages = []string{"nginx"} },
		"encrypted": func(j *api.Jail) { j.Spec.Encrypted = true },
	} {
		j := dtest.WebJail("u-" + name)
		mod(&j)
		op, err := s.CreateJail(as(globalAdmin, dtest.SeedProject), j)
		if err == nil {
			// refused inside the operation instead
			got := wait(t, s, op)
			if got.Status != api.OpFailure || got.Error == nil || got.Error.Code != api.CodeUnsupported {
				t.Fatalf("%s: %+v", name, got)
			}
			continue
		}
		wantCode(t, err, api.CodeUnsupported)
		if hintOf(err) == "" {
			t.Errorf("%s: the refusal names no alternative", name)
		}
	}
	// nothing was created
	l, _ := s.ListJails(as(globalAdmin, dtest.SeedProject), nil)
	if len(l) != 0 {
		t.Fatalf("%d jails left", len(l))
	}
}

func TestJailLifecycleErrors(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(ctx, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	_, err = s.CreateJail(ctx, dtest.WebJail("web"))
	wantCode(t, err, api.CodeAlreadyExists)

	// other projects' identities do not see the jail
	_, err = s.GetJail(as(alphaOnly, "ghost"), "web")
	if err == nil {
		t.Fatal("an identity read a jail through a project it has no scope in")
	}
	if _, err := s.GetJail(as(alphaOnly, dtest.SeedProject), "web"); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetJail(ctx, "ghost")
	wantCode(t, err, api.CodeNotFound)

	// the actions
	_, err = s.JailAction(ctx, "web", api.JailAction{Action: "freeze"})
	wantCode(t, err, api.CodeInvalid)
	_, err = s.JailAction(ctx, "web", api.JailAction{Action: "start", Timeout: -1})
	wantCode(t, err, api.CodeInvalid)
	_, err = s.JailAction(ctx, "ghost", api.JailAction{Action: "start"})
	wantCode(t, err, api.CodeNotFound)
	op, err = s.JailAction(ctx, "web", api.JailAction{Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("start: %+v", got)
	}
	// a running jail is not deleted
	_, err = s.DeleteJail(ctx, "web")
	wantCode(t, err, api.CodeBusy)
	op, _ = s.JailAction(ctx, "web", api.JailAction{Action: "stop"})
	wait(t, s, op)
	op, err = s.DeleteJail(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("delete: %+v", got)
	}
	_, err = s.GetJail(ctx, "web")
	wantCode(t, err, api.CodeNotFound)
	for _, ds := range dsNames(e) {
		if strings.Contains(ds, "alpha/jails/web") {
			t.Errorf("dataset %s left after delete", ds)
		}
	}
}

// TestCreateJailRollsBackAtEveryStep fails each mutating file operation of the
// create in turn; no failure may leave a jail record or a dataset behind.
func TestCreateJailRollsBackAtEveryStep(t *testing.T) {
	probe, ps := newEnv(t)
	probe.Fault.Reset(0)
	op, err := ps.CreateJail(as(globalAdmin, dtest.SeedProject), dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, ps, op)
	steps := probe.Fault.Count()
	if steps == 0 {
		t.Skip("the file fake saw no mutating operation during create, so there is nothing to inject")
	}
	if steps > 40 {
		steps = 40
	}
	for at := 1; at <= steps; at++ {
		e, s := newEnv(t)
		before := len(dsNames(e))
		e.Fault.Reset(at)
		ctx := as(globalAdmin, dtest.SeedProject)
		op, err := s.CreateJail(ctx, dtest.WebJail("web"))
		if err != nil {
			continue
		}
		got := wait(t, s, op)
		e.Fault.Reset(0)
		if got.Status == api.OpSuccess {
			continue // this step is not on the create path
		}
		if _, err := s.GetJail(ctx, "web"); !api.IsCode(err, api.CodeNotFound) {
			t.Errorf("step %d: the failed create left a jail record (%v)", at, err)
		}
		for _, ds := range dsNames(e) {
			if strings.Contains(ds, "jails/web") {
				t.Errorf("step %d: dataset %s left after a failed create (%d datasets before)", at, ds, before)
			}
		}
	}
}

func TestStorageServiceChecks(t *testing.T) {
	e, s := newEnv(t)
	st := api.Storage{Metadata: api.ObjectMeta{Name: "bulk"}, Spec: api.StorageSpec{Dataset: "tank/bulk"}}
	_, err := s.CreateStorage(as(alphaOnly, dtest.SeedProject), st)
	wantCode(t, err, api.CodeForbidden) // host-level writes need a global identity
	enc := st
	enc.Spec.Encrypted = true
	_, err = s.CreateStorage(as(globalAdmin, ""), enc)
	wantCode(t, err, api.CodeUnsupported)
	if hintOf(err) == "" {
		t.Error("the refusal names no manual alternative")
	}
	bad := st
	bad.Spec.Dataset = "../etc"
	_, err = s.CreateStorage(as(globalAdmin, ""), bad)
	if err == nil {
		t.Fatal("a bad dataset name was accepted")
	}
	// the dataset must exist
	_, err = s.CreateStorage(as(globalAdmin, ""), st)
	if err == nil {
		t.Fatal("a storage on a dataset that does not exist was registered")
	}
	if err := e.ZFS.Create(context.Background(), zfs.CreateOpts{Name: "tank/bulk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateStorage(as(globalAdmin, ""), st); err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	_, err = s.CreateStorage(as(globalAdmin, ""), st)
	wantCode(t, err, api.CodeAlreadyExists)
	// a storage a project grants cannot be deleted
	if err := s.DeleteStorage(as(alphaOnly, ""), "bulk", 0); err == nil {
		t.Fatal("a project identity deleted storage")
	}
	if err := s.DeleteStorage(as(globalAdmin, ""), "default", 0); err == nil {
		t.Fatal("a storage granted to a project was deleted")
	}
	if err := s.DeleteStorage(as(globalAdmin, ""), "bulk", 1); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.ZFS.Exists(context.Background(), "tank/bulk"); !ok {
		t.Fatal("deleting a storage destroyed its dataset")
	}
}

func TestProjectServiceChecks(t *testing.T) {
	_, s := newEnv(t)
	p := api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{Storage: []api.ProjectStorage{{Storage: "default"}}}}
	_, err := s.CreateProject(as(alphaOnly, ""), p)
	wantCode(t, err, api.CodeForbidden)
	if _, err := s.CreateProject(as(globalAdmin, ""), p); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateProject(as(globalAdmin, ""), p)
	wantCode(t, err, api.CodeAlreadyExists)
	bad := api.Project{Metadata: api.ObjectMeta{Name: "Bad Name!"}}
	if _, err := s.CreateProject(as(globalAdmin, ""), bad); err == nil {
		t.Fatal("a bad project name was accepted")
	}
	// a project with a jail cannot be deleted
	op, err := s.CreateJail(as(globalAdmin, dtest.SeedProject), dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	if err := s.DeleteProject(as(globalAdmin, ""), dtest.SeedProject, 0); err == nil {
		t.Fatal("a project with a jail was deleted")
	}
	if err := s.DeleteProject(as(globalAdmin, ""), "beta", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(as(globalAdmin, ""), "beta", 0); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
}

func hintOf(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Hint
	}
	return ""
}

func dsNames(e *dtest.Env) []string {
	l, err := e.ZFS.List(context.Background(), zfs.ListOpts{Root: "tank", Recursive: true})
	if err != nil {
		e.T.Fatal(err)
	}
	var out []string
	for _, d := range l {
		out = append(out, d.Name)
	}
	return out
}
