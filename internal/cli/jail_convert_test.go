package cli_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// `jail convert` changes the layout of a jail's root; --dry-run prints the plan and changes nothing.
func TestJailConvert(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15", "--storage", "default", "--network", "lan=10.20.0.15")
	out := tr.ok("jail", "convert", "web", "--layout", "thick", "--dry-run")
	if !strings.Contains(out, "dry-run: would convert jail web from thin to thick") || !strings.Contains(out, "plan:") {
		t.Fatalf("dry run: %s", out)
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Layout == api.LayoutThick {
		t.Fatal("a dry run converted the jail")
	}
	tr.ok("jail", "convert", "web", "--layout", "thick")
	if j, _ := h.srv.JailIn("main", "web"); j.Spec.Layout != api.LayoutThick {
		t.Fatalf("layout %q", j.Spec.Layout)
	}
	out = tr.ok("jail", "convert", "web", "--layout", "thick", "--dry-run")
	if !strings.Contains(out, "is thick already") {
		t.Fatalf("same layout: %s", out)
	}
	tr.ok("jail", "convert", "web", "--layout", "thin", "--image", "g1")
	for _, args := range [][]string{
		{"jail", "convert", "web"},
		{"jail", "convert", "web", "--layout", "fat"},
		{"jail", "convert", "web", "--layout", "thick", "--image", "g1"},
		{"jail", "convert", "nope", "--layout", "thin"},
	} {
		if _, _, code := tr.do("", args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	tr.golden("jail_convert")
}
