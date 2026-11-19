package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// buildImage creates an image of a release and builds its first generation.
func buildImage(t *testing.T, s *service.Service, name, release string) {
	t.Helper()
	host := as(globalAdmin, "")
	if _, err := s.CreateImage(host, api.Image{Metadata: api.ObjectMeta{Name: name}, Spec: api.ImageSpec{
		OS: api.OSFreeBSD, Release: release, Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}}); err != nil {
		t.Fatal(err)
	}
	op, err := s.BuildGeneration(host, name, api.ImageBuild{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("build %s: %+v", name, got)
	}
}

func moveJail(t *testing.T, s *service.Service, ctx context.Context, name string, up api.JailUpgrade) (service.ActionResult, api.Operation) {
	t.Helper()
	op, err := s.UpgradeJail(ctx, name, up)
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, s, op)
	var ar service.ActionResult
	if got.Status == api.OpSuccess {
		if err := json.Unmarshal(got.Metadata, &ar); err != nil {
			t.Fatal(err)
		}
	}
	return ar, got
}

func TestJailMovesToAnotherImageAndFollowsIt(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("%+v\n%s", got, e.Log())
	}
	buildImage(t, s, "fb2", "15.1-RELEASE")

	// a dry run plans the move and changes nothing
	ar, got := moveJail(t, s, alpha, "web", api.JailUpgrade{Image: "fb2", DryRun: true})
	if got.Status != api.OpSuccess || !ar.DryRun || ar.Upgrade == nil || ar.Upgrade.From != "fb#g1" || ar.Upgrade.To != "fb2#g1" || ar.Upgrade.Image != "fb2" || len(ar.Plan) == 0 {
		t.Fatalf("%+v %+v", got, ar)
	}
	if j, _ := s.GetJail(alpha, "web"); j.Spec.Image != "fb" {
		t.Fatalf("a dry run changed the image: %s", j.Spec.Image)
	}

	ar, got = moveJail(t, s, alpha, "web", api.JailUpgrade{Image: "fb2"})
	if got.Status != api.OpSuccess || ar.Upgrade == nil || ar.Upgrade.To != "fb2#g1" {
		t.Fatalf("%+v\n%s", got, e.Log())
	}
	j, err := s.GetJail(alpha, "web")
	if err != nil {
		t.Fatal(err)
	}
	if j.Spec.Image != "fb2" || j.Status.Generation != "fb2#g1" {
		t.Fatalf("the jail follows %s on %s", j.Spec.Image, j.Status.Generation)
	}
	// the history says what moved
	hist, err := s.UpdateHistory(alpha, "web")
	if err != nil || len(hist) != 1 || hist[0].Kind != api.UpdateKindOS || hist[0].From != "fb#g1" || hist[0].To != "fb2#g1" || hist[0].Result != api.UpdateOK {
		t.Fatalf("%+v %v", hist, err)
	}
	// the old image has no user any more: it can be deleted, the new one holds the jail
	if g, err := s.GetGeneration(as(globalAdmin, ""), "fb2", 1); err != nil || len(g.Refs) != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	// and the root rollback goes back to the image the jail had
	rop, err := s.RollbackJail(alpha, "web", api.JailRollback{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, rop); got.Status != api.OpSuccess {
		t.Fatalf("%+v\n%s", got, e.Log())
	}
	if j, _ := s.GetJail(alpha, "web"); j.Spec.Image != "fb" || j.Status.Generation != "fb#g1" {
		t.Fatalf("%s on %s", j.Spec.Image, j.Status.Generation)
	}
}

func TestJailMoveRefusals(t *testing.T) {
	e, s := tickingEnv(t)
	e.SeedLinux()
	alpha := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("%+v\n%s", got, e.Log())
	}
	buildImage(t, s, "future", "15.2-RELEASE") // the host runs 15.1; an older release cannot be an image (15.1 is the oldest)
	// refused before an operation is submitted
	for _, tc := range []struct {
		name, image string
		code        api.Code
		want        string
	}{
		{"another OS", "alp", api.CodeInvalid, "one OS"},
		{"unknown image", "nope", api.CodeNotFound, ""},
		{"a generation, not an image", "fb2#g1", api.CodeInvalid, "names an image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.UpgradeJail(alpha, "web", api.JailUpgrade{Image: tc.image})
			wantCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	// refused by the plan: the host kernel is older than the image's release
	t.Run("newer than the host kernel", func(t *testing.T) {
		_, got := moveJail(t, s, alpha, "web", api.JailUpgrade{Image: "future"})
		if got.Status == api.OpSuccess || got.Error == nil || got.Error.Code != api.CodeVersionSkew || !strings.Contains(got.Error.Hint, "upgrade the host first") {
			t.Fatalf("%+v", got)
		}
	})
	if j, _ := s.GetJail(alpha, "web"); j.Spec.Image != "fb" {
		t.Fatal(j.Spec.Image)
	}
}

func TestImageUpgradeToMovesEveryThinJailOfTheImage(t *testing.T) {
	e, s := tickingEnv(t)
	twoProjectJails(t, e, s)
	host := as(globalAdmin, "")
	buildImage(t, s, "fb2", "15.1-RELEASE")

	// a dry run checks every jail and moves none
	res := upgradeImage(t, s, host, "fb", api.ImageUpgrade{To: "fb2", DryRun: true})
	want := map[string]string{"alpha/db": api.ImageUpgradePlanned, "alpha/pin": api.ImageUpgradeSkipped, "alpha/web": api.ImageUpgradePlanned, "beta/api": api.ImageUpgradePlanned}
	if len(res.Jails) != len(want) || !res.DryRun {
		t.Fatalf("%+v", res)
	}
	for _, j := range res.Jails {
		if j.Status != want[j.Project+"/"+j.Jail] || j.Status == api.ImageUpgradePlanned && j.To != "fb2#g1" {
			t.Fatalf("%+v", j)
		}
	}
	if gen, _ := generationOf(t, s, "alpha", "web"); gen != "fb#g1" {
		t.Fatalf("a dry run moved a jail: %s", gen)
	}

	res = upgradeImage(t, s, host, "fb", api.ImageUpgrade{To: "fb2"})
	for _, j := range res.Jails {
		if j.Status != want[j.Project+"/"+j.Jail] && !(j.Status == api.ImageUpgradeUpgraded && want[j.Project+"/"+j.Jail] == api.ImageUpgradePlanned) {
			t.Fatalf("%+v", j)
		}
		if j.Status == api.ImageUpgradeUpgraded && (j.From != "fb#g1" || j.To != "fb2#g1" || j.Record == "") {
			t.Fatalf("%+v", j)
		}
	}
	for _, c := range []struct{ project, name, gen string }{{"alpha", "web", "fb2#g1"}, {"alpha", "db", "fb2#g1"}, {"beta", "api", "fb2#g1"}, {"alpha", "pin", "fb#g1"}} {
		if gen, _ := generationOf(t, s, c.project, c.name); gen != c.gen {
			t.Errorf("%s/%s on %s, want %s", c.project, c.name, gen, c.gen)
		}
	}

	// the target must exist, differ from the image, have a generation and be named without one
	for _, tc := range []struct {
		to   string
		code api.Code
	}{{"nope", api.CodeNotFound}, {"fb", api.CodeInvalid}, {"fb2#g1", api.CodeInvalid}} {
		if _, err := s.UpgradeImage(host, "fb", api.ImageUpgrade{To: tc.to}); !api.IsCode(err, tc.code) {
			t.Errorf("to %q: %v", tc.to, err)
		}
	}
	if _, err := s.UpgradeImage(host, "fb", api.ImageUpgrade{To: "fb2", NoJails: true}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("no_jails with to: %v", err)
	}
}
