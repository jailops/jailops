package cli_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// 'list' shows what the Internet reaches, 'firewall show' and 'info' say what reaches a jail, and 'firewall exposed' lists
// everything open to the Internet across the jails.
func TestReachViews(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "freebsd-151"},
		Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}})
	h.srv.SeedGeneration("freebsd-151")
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-151", "--network", "lan", "--storage", "default", "--start")
	tr.ok("expose", "web", "80", "--host-port", "8080")
	tr.ok("list")
	tr.ok("firewall", "show", "web")
	tr.ok("firewall", "show", "web", "-o", "json")
	tr.ok("firewall", "exposed")
	tr.ok("firewall", "exposed", "--tunnels")
	tr.do("", "firewall", "show", "nobody")
	tr.golden("reach_views")

	out, _, code := h.run("", "list")
	if code != 0 || !strings.Contains(out, "EXPOSED") {
		t.Fatalf("list has no EXPOSED column: %d %s", code, out)
	}
	out, _, code = h.run("", "firewall", "exposed")
	if code != 0 || !strings.Contains(out, "web") || !strings.Contains(out, "8080") {
		t.Fatalf("firewall exposed: %d %s", code, out)
	}
}
