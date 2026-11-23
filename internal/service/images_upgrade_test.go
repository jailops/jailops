package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// tickingEnv is newEnv with a clock that moves one second per reading: the
// rollback snapshot of a jail is named by the second, and these tests move a
// jail again within a second of its last move.
func tickingEnv(t *testing.T) (*dtest.Env, *service.Service) {
	t.Helper()
	var mu sync.Mutex
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	e := dtest.New(t, dtest.Options{Now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}})
	e.Seed()
	return e, e.D.Service()
}

// upgradeImage runs an image upgrade to the end and returns its result.
func upgradeImage(t *testing.T, s *service.Service, ctx context.Context, image string, req api.ImageUpgrade) api.ImageUpgradeResult {
	t.Helper()
	op, err := s.UpgradeImage(ctx, image, req)
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, s, op)
	if got.Status != api.OpSuccess {
		t.Fatalf("upgrade image: %+v", got)
	}
	var res api.ImageUpgradeResult
	if err := json.Unmarshal(got.Metadata, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// twoProjectJails makes the jails of the image upgrade tests: in alpha "web"
// (running), "db" (stopped) and "pin" (pinned to fb#g1); in beta "api".
func twoProjectJails(t *testing.T, e *dtest.Env, s *service.Service) {
	t.Helper()
	alpha := as(globalAdmin, dtest.SeedProject)
	net := e.MakeNetwork("beta")
	if _, err := s.CreateProject(as(globalAdmin, ""), api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	mk := func(ctx context.Context, j api.Jail) {
		t.Helper()
		op, err := s.CreateJail(ctx, j)
		if err != nil {
			t.Fatal(err)
		}
		if got := wait(t, s, op); got.Status != api.OpSuccess {
			t.Fatalf("create %s: %+v\n%s", j.Metadata.Name, got, e.Log())
		}
	}
	mk(alpha, dtest.WebJail("web"))
	mk(alpha, dtest.WebJail("db"))
	pin := dtest.WebJail("pin")
	pin.Spec.Image = "fb#g1"
	mk(alpha, pin)
	mk(as(globalAdmin, "beta"), dtest.WebJailOn("api", net))
	op, err := s.JailAction(alpha, "web", api.JailAction{Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("start web: %+v\n%s", got, e.Log())
	}
}

func generationOf(t *testing.T, s *service.Service, project, name string) (gen string, running bool) {
	t.Helper()
	j, err := s.GetJail(as(globalAdmin, project), name)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status.ImageGeneration, j.Status.Power == api.PowerRunning
}

// An image upgrade builds the next generation for the jails made from it later and touches no jail.
func TestUpgradeImageBuildsAGenerationAndTouchesNoJail(t *testing.T) {
	e, s := tickingEnv(t)
	twoProjectJails(t, e, s)
	host := as(globalAdmin, "")
	alpha := as(globalAdmin, dtest.SeedProject)
	before, _ := s.GetJail(alpha, "web")
	e.SetRepoVersion("15.1p2")
	res := upgradeImage(t, s, host, "fb", api.ImageUpgrade{})
	if res.Unchanged || res.Generation == nil || res.Generation.Name != "fb#g2" || res.From != "fb#g1" {
		t.Fatalf("%+v", res)
	}
	after, _ := s.GetJail(alpha, "web")
	if after.Status.ImageGeneration != "fb#g1" || after.Status.Power != api.PowerRunning || after.Status.JID != before.Status.JID || !after.Status.StartedAt.Equal(before.Status.StartedAt) {
		t.Fatalf("the image upgrade touched web: %+v -> %+v", before.Status, after.Status)
	}
	for _, c := range []struct{ project, name string }{{"alpha", "db"}, {"alpha", "pin"}, {"beta", "api"}} {
		if gen, _ := generationOf(t, s, c.project, c.name); gen != "fb#g1" {
			t.Fatalf("%s/%s says %s", c.project, c.name, gen)
		}
	}
	if h, _ := s.UpdateHistory(alpha, "web"); len(h) != 0 {
		t.Fatalf("the image upgrade wrote the history of a jail: %+v", h)
	}
	// nothing new: no generation is kept
	res = upgradeImage(t, s, host, "fb", api.ImageUpgrade{})
	if !res.Unchanged || res.Generation != nil {
		t.Fatalf("%+v", res)
	}
	// a new jail is copied from the new generation
	op, err := s.CreateJail(alpha, dtest.WebJail("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	if gen, _ := generationOf(t, s, "alpha", "fresh"); gen != "fb#g2" {
		t.Fatalf("a new jail was copied from %s", gen)
	}
}

func TestUpgradeImageIsAGlobalAdminsOperation(t *testing.T) {
	e, s := tickingEnv(t)
	twoProjectJails(t, e, s)
	host := as(globalAdmin, "")
	e.SetRepoVersion("15.1p2")
	for _, ctx := range []context.Context{as(alphaOnly, dtest.SeedProject), as(alphaOnly, ""), as(nobody, "")} {
		_, err := s.UpgradeImage(ctx, "fb", api.ImageUpgrade{})
		wantCode(t, err, api.CodeForbidden)
	}
	if im, _ := s.GetImage(host, "fb"); len(im.Status.Generations) != 1 {
		t.Fatalf("%+v", im.Status)
	}
	_, err := s.UpgradeImage(host, "nope", api.ImageUpgrade{})
	wantCode(t, err, api.CodeNotFound)
	// An image without a generation says what to run first.
	if _, err := s.CreateImage(host, api.Image{Metadata: api.ObjectMeta{Name: "empty"}, Spec: api.ImageSpec{
		OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpgradeImage(host, "empty", api.ImageUpgrade{})
	wantCode(t, err, api.CodeNotFound)
	var ae *api.Error
	if !errors.As(err, &ae) || !strings.Contains(ae.Hint, "jailops image build empty") {
		t.Fatalf("%v", err)
	}
	// A dry run plans the build; nothing changes.
	res := upgradeImage(t, s, host, "fb", api.ImageUpgrade{DryRun: true})
	if !res.DryRun || res.Generation != nil || len(res.Plan) == 0 {
		t.Fatalf("%+v", res)
	}
	if im, _ := s.GetImage(host, "fb"); len(im.Status.Generations) != 1 {
		t.Fatalf("a dry run built a generation: %+v", im.Status)
	}
}

// An older release is refused, and so is a release without its type or with a typo in it: such a name would be built from
// base_latest without a word.
func TestImageReleaseOlderThan151OrWithoutItsTypeIsRefused(t *testing.T) {
	_, s := tickingEnv(t)
	host := as(globalAdmin, "")
	for _, r := range []string{"15.0-RELEASE", "14.3-RELEASE", "bogus", "15.1", "15.1-RELESE"} {
		_, err := s.CreateImage(host, api.Image{Metadata: api.ObjectMeta{Name: "old"}, Spec: api.ImageSpec{
			OS: api.OSFreeBSD, Release: r, Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}})
		wantCode(t, err, api.CodeInvalid)
	}
	for i, r := range []string{"15.1-RELEASE", "15.2-PRERELEASE", "16.0-CURRENT"} {
		if _, err := s.CreateImage(host, api.Image{Metadata: api.ObjectMeta{Name: "ok" + string(rune('a'+i))}, Spec: api.ImageSpec{
			OS: api.OSFreeBSD, Release: r, Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}}); err != nil {
			t.Fatalf("%s: %v", r, err)
		}
	}
}
