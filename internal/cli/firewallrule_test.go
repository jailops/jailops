package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The rules of a policy are added, listed, changed, moved, switched and removed one at a time, by number or by name.
func TestFirewallRuleCommands(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("firewall", "address-set", "create", "office", "--address", "192.0.2.0/24", "--comment", "the office")
	tr.ok("firewall", "policy", "create", "web", "--comment", "the public web server")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "allow", "--proto", "tcp", "--port", "80,443", "--name", "site", "--comment", "the site")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "allow", "--proto", "tcp", "--from", "set:office", "--port", "22", "--comment", "ssh from the office")
	tr.ok("firewall", "rule", "add", "web", "out", "--action", "allow", "--proto", "tcp", "--to", "192.0.2.50", "--port", "5432", "--comment", "the database")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "deny", "--at", "1", "--from", "198.51.100.0/24", "--comment", "a network that is not welcome")
	tr.ok("firewall", "rule", "list", "web")
	tr.ok("firewall", "rule", "list", "web", "-o", "json")
	// the number is the one policy show prints
	tr.ok("firewall", "policy", "show", "web")
	tr.ok("firewall", "rule", "edit", "web", "site", "--port", "80,443,8080", "--comment", "the site and the test site")
	tr.ok("firewall", "rule", "edit", "web", "3", "--from", "set:office,203.0.113.7")
	tr.ok("firewall", "rule", "edit", "web", "3", "--from", "")
	tr.ok("firewall", "rule", "disable", "web", "2", "site")
	tr.ok("firewall", "rule", "enable", "web", "site")
	tr.ok("firewall", "rule", "move", "web", "4", "--to", "1")
	tr.ok("firewall", "rule", "move", "web", "site", "--after", "4")
	tr.ok("firewall", "rule", "move", "web", "2", "--before", "1")
	tr.ok("firewall", "rule", "move", "web", "1", "--to", "1")
	tr.ok("firewall", "rule", "list", "web")
	tr.ok("firewall", "rule", "remove", "web", "--name", "site")
	tr.ok("firewall", "rule", "remove", "web", "1")
	tr.ok("firewall", "rule", "list", "web")
	tr.ok("firewall", "--dry-run", "rule", "remove", "web", "1")
	tr.ok("firewall", "rule", "list", "web", "-o", "name")
	tr.golden("firewall_rules")
}

// What a rule command refuses, and says.
func TestFirewallRuleRefusals(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedFirewallPolicy(api.FirewallPolicy{
		Metadata: api.ObjectMeta{Name: "hub-db", Project: "main", ManagedBy: "hub:h1"},
		Spec:     api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}})
	tr := h.transcript()
	tr.ok("firewall", "policy", "create", "web")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "allow", "--proto", "tcp", "--port", "80", "--name", "site", "--comment", "the site")
	for _, args := range [][]string{
		{"rule", "add", "web", "in"},                                                                               // no action
		{"rule", "add", "web", "sideways", "--action", "allow"},                                                    // not a direction
		{"rule", "add", "web", "in", "--action", "allow", "--to", "192.0.2.5"},                                     // an inbound rule names its source
		{"rule", "add", "web", "out", "--action", "allow", "--from", "192.0.2.5"},                                  // an outbound rule names its destination
		{"rule", "add", "web", "in", "--action", "allow", "--name", "site", "--port", "81", "--proto", "tcp"},      // a name is used once
		{"rule", "add", "nothing", "in", "--action", "allow"},                                                      // no such policy
		{"rule", "add", "web", "in", "--action", "allow", "--at", "9"},                                             // no such position
		{"rule", "add", "web", "in", "--action", "allow", "--from", "@office"},                                     // @ is gone
		{"rule", "remove", "web", "7"},                                                                             // no such rule
		{"rule", "remove", "web", "ghost"},                                                                         // no such name
		{"rule", "remove", "web"},                                                                                  // which one
		{"rule", "edit", "web", "1"},                                                                               // nothing to change
		{"rule", "move", "web", "1"},                                                                               // where
		{"rule", "move", "web", "1", "--to", "1", "--after", "1"},                                                  // one place only
		{"rule", "add", "hub-db", "in", "--action", "allow", "--proto", "tcp", "--port", "5432", "--comment", "x"}, // managed by a hub
		{"rule", "remove", "hub-db", "1"},
	} {
		tr.do("", append([]string{"firewall"}, args...)...)
	}
	tr.golden("firewall_rule_refusals")
}

// A rule that lets the whole Internet in gets one note that says expose does the same and more; a rule with named
// sources gets none of it.
func TestFirewallRuleNoteOfTheInternet(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("firewall", "address-set", "create", "office", "--address", "192.0.2.0/24", "--comment", "the office")
	tr.ok("firewall", "policy", "create", "web", "--comment", "the web server")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "allow", "--proto", "tcp", "--from", "any", "--port", "80", "--comment", "the site")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "allow", "--proto", "tcp", "--from", "::/0", "--port", "443", "--comment", "the site over IPv6")
	tr.ok("firewall", "rule", "add", "web", "in", "--action", "allow", "--proto", "tcp", "--from", "set:office", "--port", "22", "--comment", "ssh from the office")
	tr.ok("firewall", "rule", "edit", "web", "3", "--from", "0.0.0.0/0")
	tr.ok("firewall", "rule", "edit", "web", "3", "--from", "set:office")
	tr.ok("firewall", "rule", "add", "web", "out", "--action", "allow", "--proto", "tcp", "--to", "any", "--port", "443", "--comment", "outbound is not it")
	tr.golden("firewall_internet_note")
}

// The same note comes with a policy written from a file and with the check of a file, once.
func TestFirewallPolicyFileNoteOfTheInternet(t *testing.T) {
	h := newHarness(t)
	h.seed()
	dir := t.TempDir()
	open := "apiVersion: jailops/v1\nkind: FirewallPolicy\nmetadata:\n  name: open\nspec:\n  default_in: deny\n  default_out: allow\n  rules:\n    - {action: allow, direction: in, proto: tcp, from: [any], ports: ['80', '443'], comment: the site}\n    - {action: allow, direction: in, proto: tcp, from: ['::/0'], ports: ['8080'], comment: the test site}\n"
	named := strings.ReplaceAll(strings.ReplaceAll(open, "from: [any]", "from: ['192.0.2.0/24']"), "from: ['::/0']", "from: ['198.51.100.0/24']")
	writeFile(t, dir, "open.yaml", open, 0o644)
	writeFile(t, dir, "named.yaml", strings.ReplaceAll(named, "name: open", "name: named"), 0o644)
	const note = "note: this lets the Internet in; jailops expose does the same and also adds the IPv4 forward"
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"firewall", "validate", "-f", filepath.Join(dir, "open.yaml"), "--offline"}, 1},
		{[]string{"firewall", "validate", "-f", filepath.Join(dir, "named.yaml"), "--offline"}, 0},
		{[]string{"firewall", "policy", "create", "-f", filepath.Join(dir, "open.yaml")}, 1},
		{[]string{"firewall", "policy", "create", "-f", filepath.Join(dir, "named.yaml")}, 0},
	} {
		_, errs, code := h.run("", c.args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", c.args, code, errs)
		}
		if got := strings.Count(errs, note); got != c.want {
			t.Errorf("%v: the note was said %d times, want %d: %q", c.args, got, c.want, errs)
		}
	}
}
