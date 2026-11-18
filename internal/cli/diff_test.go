package cli_test

import (
	"strings"
	"testing"
)

// 'jail diff' lists what a thin jail holds on top of its image, by dataset, with the counts.
func TestJailDiff(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15@stable", "--storage", "default")
	tr.ok("jail", "diff", "web")
	tr.ok("jail", "diff", "web", "--summary")
	tr.ok("jail", "diff", "web", "--dataset", "etc")
	tr.ok("jail", "diff", "web", "--packages")
	tr.ok("jail", "diff", "web", "-o", "json")
	tr.do("", "jail", "diff", "web", "--dataset", "nope")
	tr.do("", "jail", "diff", "web", "--limit", "0")
	tr.do("", "jail", "diff", "nobody")
	tr.golden("jail_diff")

	out, _, code := h.run("", "jail", "diff", "web")
	if code != 0 || !strings.Contains(out, "M /etc/rc.conf") || !strings.Contains(out, "+ /usr/local/etc/web.conf") {
		t.Fatalf("jail diff: %d %s", code, out)
	}
}
