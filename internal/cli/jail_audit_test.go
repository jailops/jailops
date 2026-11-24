package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/internal/cli"
	"jailops.net/jailops/sdk/api"
)

// `jail audit` prints one line per check with the fixes below, and exits 1 only for a critical finding; --suggest adds
// the suggestions; -o json is the API's answer.
func TestJailAuditCommand(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	tr.ok("jail", "create", "db", "--image", "freebsd-15", "--network", "lan", "--storage", "default", "--label", "audit=critical")
	if out := tr.ok("jail", "audit", "web"); strings.Contains(out, "cron.jitter") || !strings.Contains(out, "pkg.vulnerabilities") || !strings.Contains(out, "0 critical") {
		t.Fatalf("clean: %s", out)
	}
	// --suggest adds the suggestions, marked, and they count for nothing
	if out := tr.ok("jail", "audit", "web", "--suggest"); !strings.Contains(out, "suggestion: cron starts") || !strings.Contains(out, "disk.limit (suggestion): jailops config set web") || !strings.Contains(out, "0 critical, 0 warning(s)") {
		t.Fatalf("suggest: %s", out)
	}
	out, _, code := tr.do("", "jail", "audit", "db")
	if code != cli.ExitFailure || !strings.Contains(out, "CRITICAL") || !strings.Contains(out, "pw userdel") {
		t.Fatalf("critical: %d %s", code, out)
	}
	out, _, code = tr.do("", "jail", "audit", "db", "-o", "json")
	var a api.JailAudit
	if code != cli.ExitFailure || json.Unmarshal([]byte(out), &a) != nil || a.Critical != 1 {
		t.Fatalf("json: %d %s", code, out)
	}
	tr.golden("jail_audit")
}
