package cli_test

import (
	"strings"
	"testing"
)

func TestLabelSetListAndRemove(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "create", "web", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default")
	tr := h.transcript()
	if out, _, code := tr.do("", "label", "web", "backup=yes", "tier=frontend"); code != 0 || !strings.Contains(out, "backup=yes") || !strings.Contains(out, "tier=frontend") {
		t.Fatalf("set: %d %q", code, out)
	}
	if out, _, code := tr.do("", "label", "web", "tier-"); code != 0 || strings.Contains(out, "tier") || !strings.Contains(out, "backup=yes") {
		t.Fatalf("remove: %d %q", code, out)
	}
	if out, _, code := tr.do("", "label", "web"); code != 0 || strings.TrimSpace(out) != "backup=yes" {
		t.Fatalf("list: %d %q", code, out)
	}
	if _, _, code := tr.do("", "label", "web", "novalue"); code == 0 {
		t.Fatal("a label without = or a trailing dash must be refused")
	}
	if out, _, code := tr.do("", "--dry-run", "label", "web", "x=y"); code != 0 || strings.Contains(out, "x=y\n") && !strings.Contains(out, "dry") {
		t.Fatalf("dry run: %d %q", code, out)
	}
	if out, _, _ := tr.do("", "label", "web"); strings.Contains(out, "x=") {
		t.Fatalf("a dry run must change nothing: %q", out)
	}
}
