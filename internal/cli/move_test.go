package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// seedNewRelease adds the image of a newer release with a generation, and an image of another OS.
func (h *harness) seedNewRelease() {
	h.t.Helper()
	h.seed()
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "freebsd-15-1"}, Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}})
	h.srv.SeedGeneration("freebsd-15-1")
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "alpine-3"}, Spec: api.ImageSpec{OS: api.OSLinux, Release: "3.22", Arch: "amd64", Source: api.ImageSource{Type: "minirootfs"}}})
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "freebsd-15-arm"}, Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "arm64", Source: api.ImageSource{Type: "pkgbase"}}})
}

func TestJailUpgradeImageMovesOneJailAndSeveral(t *testing.T) {
	h := newHarness(t)
	h.seedNewRelease()
	tr := h.transcript()
	for _, n := range []string{"web", "db"} {
		tr.ok("jail", "create", n, "--image", "freebsd-15", "--network", "lan", "--storage", "default", "--label", "env=prod")
	}
	before := len(h.mutations())
	tr.ok("jail", "upgrade", "web", "--image", "freebsd-15-1", "--dry-run")
	var up api.JailUpgrade
	_ = json.Unmarshal(h.lastBody("POST", "/api/v1/jails/web/upgrade"), &up)
	if !up.DryRun || up.Image != "freebsd-15-1" || up.Generation != "" || len(h.mutations()) != before+1 {
		t.Fatalf("dry-run request: %+v %v", up, h.mutations())
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Image != "freebsd-15" {
		t.Fatalf("dry run moved the jail: %s", j.Spec.Image)
	}
	tr.ok("jail", "upgrade", "web", "--image", "freebsd-15-1")
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Image != "freebsd-15-1" || j.Status.ImageGeneration != "g1" {
		t.Fatalf("%s %s", j.Spec.Image, j.Status.ImageGeneration)
	}
	// several jails: by name and by label, one request each, in order
	tr.ok("jail", "create", "api", "--image", "freebsd-15", "--network", "lan", "--storage", "default", "--label", "env=prod")
	tr.ok("jail", "upgrade", "-l", "env=prod", "--image", "freebsd-15-1")
	for _, n := range []string{"db", "api"} {
		if j, _ := h.srv.JailIn("main", n); j.Spec.Image != "freebsd-15-1" {
			t.Fatalf("%s is on %s", n, j.Spec.Image)
		}
	}
	// --generation names a generation of the new image
	out, _, code := tr.do("", "jail", "upgrade", "web", "db", "--generation", "g1")
	if code != 2 || !strings.Contains(out, "") {
		t.Fatalf("a generation for several jails without an image: %d", code)
	}
}

func TestJailUpgradeImageRefusals(t *testing.T) {
	h := newHarness(t)
	h.seedNewRelease()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	for _, args := range [][]string{
		{"jail", "upgrade", "web", "--image", "freebsd-15-1#g1"}, // a generation, not an image
		{"jail", "upgrade", "web", "--image", "nope"},            // unknown image
	} {
		if _, _, code := tr.do("", args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Image != "freebsd-15" {
		t.Fatal(j.Spec.Image)
	}
}

func TestImageUpgradeToMovesTheJailsOfAnImage(t *testing.T) {
	h := newHarness(t)
	h.seedNewRelease()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	tr.ok("jail", "create", "pin", "--image", "freebsd-15#g1", "--network", "lan", "--storage", "default")
	out, _, _ := tr.do("", "image", "upgrade", "freebsd-15", "--to", "freebsd-15-1", "--dry-run")
	if !strings.Contains(out, "would move 1 of 2 jails of freebsd-15 to freebsd-15-1#g1") {
		t.Fatalf("dry run: %s", out)
	}
	var u api.ImageUpgrade
	_ = json.Unmarshal(h.lastBody("POST", "/api/v1/images/freebsd-15/upgrade"), &u)
	if u.To != "freebsd-15-1" || !u.DryRun {
		t.Fatalf("%+v", u)
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Image != "freebsd-15" {
		t.Fatalf("dry run moved %s", j.Spec.Image)
	}
	out, _, _ = tr.do("", "image", "upgrade", "freebsd-15", "--to", "freebsd-15-1")
	if !strings.Contains(out, "the jails move to freebsd-15-1#g1") || !strings.Contains(out, "upgraded") || !strings.Contains(out, "pinned to freebsd-15#g1") {
		t.Fatalf("%s", out)
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Image != "freebsd-15-1" {
		t.Fatalf("%s", j.Spec.Image)
	}
	if j, _ := h.srv.JailIn("main", "pin"); j.Spec.Image != "freebsd-15#g1" {
		t.Fatalf("a pinned jail moved: %s", j.Spec.Image)
	}
	if _, _, code := tr.do("", "image", "upgrade", "freebsd-15", "--to", "freebsd-15"); code == 0 {
		t.Fatal("to the image itself")
	}
	if _, _, code := tr.do("", "image", "upgrade", "freebsd-15", "--to", "freebsd-15-1", "--no-jails"); code == 0 {
		t.Fatal("no-jails with to")
	}
}

func TestImageCreateBuildBuildsTheFirstGeneration(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	out, _, code := tr.do("", "image", "create", "freebsd-15-1", "--release", "15.1-RELEASE", "--build")
	if code != 0 || !strings.Contains(out, "image/freebsd-15-1") {
		t.Fatalf("%d %s", code, out)
	}
	gens, _, _ := tr.do("", "image", "generation", "list", "freebsd-15-1")
	if !strings.Contains(gens, "freebsd-15-1#g1") {
		t.Fatalf("no generation was built:\n%s", gens)
	}
	if _, _, code := tr.do("", "image", "create", "--build", "-f", "x.yaml"); code != 2 {
		t.Fatalf("--build with -f: %d", code)
	}
	// without --build nothing is built
	tr.ok("image", "create", "freebsd-15-2", "--release", "15.2-RELEASE")
	gens, _, _ = tr.do("", "image", "generation", "list", "freebsd-15-2")
	if strings.Contains(gens, "freebsd-15-2#g1") {
		t.Fatalf("built without --build:\n%s", gens)
	}
}
