package service_test

import (
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// An image's automatic upgrade builds the next generation and touches no jail: no package is upgraded inside a jail,
// no jail moves or restarts. A jail takes its patches by its own update policy.
func TestAutoUpgradeBuildsAGenerationAndTouchesNoJail(t *testing.T) {
	e, s := tickingEnv(t)
	twoProjectJails(t, e, s)
	host := as(globalAdmin, "")
	if _, err := s.PatchImage(host, "fb", map[string]any{"spec": map[string]any{"auto_upgrade": map[string]any{"enabled": true, "at": "03:00"}}}, -1); err != nil {
		t.Fatal(err)
	}
	im, _ := s.GetImage(host, "fb")
	if au := im.Spec.AutoUpgrade; au == nil || !au.Enabled || au.At != "03:00" {
		t.Fatalf("%+v", im.Spec)
	}
	if im.Status.AutoUpgrade == nil || im.Status.AutoUpgrade.NextAt.IsZero() || len(im.Status.AutoUpgrade.Recent) != 0 {
		t.Fatalf("status before a run: %+v", im.Status.AutoUpgrade)
	}
	if _, err := s.PatchImage(host, "fb", map[string]any{"spec": map[string]any{"auto_upgrade": map[string]any{"enabled": true, "at": "25:00"}}}, -1); err == nil {
		t.Fatal("a bad time of day was accepted")
	}

	calls := len(e.Run.Calls()) // the jails' own setup ran pkg before
	// Nothing new: no generation.
	if run1 := s.AutoUpgradeImage(host, "fb"); run1.Result != api.ImageUpgradeRunUnchanged || run1.Generation != "" {
		t.Fatalf("%+v\n%s", run1, e.Log())
	}
	// A newer package set: g2 is built for new jails; the running jail keeps its generation, jid and start time.
	e.SetRepoVersion("15.1p2")
	alpha := as(globalAdmin, dtest.SeedProject)
	before, _ := s.GetJail(alpha, "web")
	if run2 := s.AutoUpgradeImage(host, "fb"); run2.Result != api.ImageUpgradeRunOK || run2.Generation != "fb#g2" {
		t.Fatalf("%+v\n%s", run2, e.Log())
	}
	after, _ := s.GetJail(alpha, "web")
	if after.Status.ImageGeneration != "fb#g1" || after.Status.Power != api.PowerRunning || after.Status.JID != before.Status.JID || !after.Status.StartedAt.Equal(before.Status.StartedAt) {
		t.Fatalf("the automatic upgrade moved or restarted web: %+v -> %+v", before.Status, after.Status)
	}
	for _, c := range e.Run.Calls()[calls:] {
		if c.Cmd.Name == "pkg" && (slices.Contains(c.Cmd.Args, "-j") || strings.Contains(strings.Join(c.Cmd.Args, " "), "/jails/")) {
			t.Fatalf("the image's automatic upgrade ran pkg on a jail: %v", c.Cmd.Args)
		}
	}
	if h := mustHistory(t, s, "web"); len(h) != 0 {
		t.Fatalf("update records from the image's run: %+v", h)
	}
	im, _ = s.GetImage(host, "fb")
	if st := im.Status.AutoUpgrade; st == nil || len(st.Recent) != 2 || st.Recent[0].Generation != "fb#g2" {
		t.Fatalf("status after two runs: %+v", st)
	}
}

func TestAutoUpgradeLeavesPinnedJailsAndNeedsAGeneration(t *testing.T) {
	e, s := tickingEnv(t)
	twoProjectJails(t, e, s)
	host := as(globalAdmin, "")
	// web is pinned to its generation.
	if _, err := s.PatchJail(as(globalAdmin, dtest.SeedProject), "web", map[string]any{"spec": map[string]any{"image": "fb#g1"}}, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchImage(host, "fb", map[string]any{"spec": map[string]any{"auto_upgrade": map[string]any{"enabled": true}}}, -1); err != nil {
		t.Fatal(err)
	}
	e.SetRepoVersion("15.1p2")
	r := s.AutoUpgradeImage(host, "fb")
	if r.Result != api.ImageUpgradeRunOK || r.Generation != "fb#g2" {
		t.Fatalf("%+v\n%s", r, e.Log())
	}
	if gen, _ := generationOf(t, s, "alpha", "web"); gen != "fb#g1" {
		t.Fatalf("a pinned jail moved to %s", gen)
	}
	// the pinned jail gets no pending note: its pin is the user's choice
	for _, rec := range mustHistory(t, s, "web") {
		if rec.Kind == api.UpdateKindOS {
			t.Fatalf("a pinned jail got %+v", rec)
		}
	}

	// An image without a generation fails with the command that makes one.
	if _, err := s.CreateImage(host, api.Image{Metadata: api.ObjectMeta{Name: "empty"}, Spec: api.ImageSpec{
		OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"},
		AutoUpgrade: &api.ImageAutoUpgrade{Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	r = s.AutoUpgradeImage(host, "empty")
	if r.Result != api.ImageUpgradeRunFailed || !strings.Contains(r.Detail, "jailops image build empty") {
		t.Fatalf("%+v", r)
	}
}

func mustHistory(t *testing.T, s *service.Service, name string) []api.UpdateRecord {
	t.Helper()
	h, err := s.UpdateHistory(as(globalAdmin, dtest.SeedProject), name)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
