package cli_test

import (
	"strings"
	"testing"
)

// attach and detach change one policy of a jail and leave the rest of the list as it is.
func TestPolicyAttachAndDetach(t *testing.T) {
	h := newHarness(t)
	h.seed()
	for _, args := range [][]string{
		{"firewall", "policy", "create", "web-public"},
		{"firewall", "policy", "create", "ssh-office"},
		{"firewall", "policy", "create", "monitoring"},
		{"jail", "create", "db", "--image", "freebsd-15", "--storage", "default", "--network", "lan", "--firewall-policy", "ssh-office"},
		{"jail", "create", "web", "--image", "freebsd-15", "--storage", "default", "--network", "lan"},
	} {
		if _, errs, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	list := func(jail string) string {
		out, errs, code := h.run("", "config", "get", jail, "firewall.policies")
		if code != 0 && !strings.Contains(errs, "is not set") {
			t.Fatalf("config get %s: %s", jail, errs)
		}
		return strings.TrimSpace(out)
	}
	run := func(want string, args ...string) {
		t.Helper()
		out, errs, code := h.run("", args...)
		if code != 0 || !strings.Contains(out+errs, want) {
			t.Fatalf("%v: code %d, want %q in %q %q", args, code, want, out, errs)
		}
	}
	// an attach appends after what is there and keeps it
	run("policy web-public attached", "firewall", "policy", "attach", "web-public", "db")
	if got := list("db"); got != "ssh-office,web-public" {
		t.Fatalf("db has %q", got)
	}
	// several jails at once, one of them has it already
	run("jail/db: policy web-public is attached already", "firewall", "policy", "attach", "web-public", "db", "web")
	if got := list("web"); got != "web-public" {
		t.Fatalf("web has %q", got)
	}
	// a detach takes out that name only
	run("policy ssh-office detached", "firewall", "policy", "detach", "ssh-office", "db")
	if got := list("db"); got != "web-public" {
		t.Fatalf("db has %q", got)
	}
	run("jail/db: policy ssh-office is not attached", "firewall", "policy", "detach", "ssh-office", "db")
	// the last one leaves an empty list
	run("detached", "firewall", "policy", "detach", "web-public", "db", "web")
	if got := list("db"); got != "" {
		t.Fatalf("db has %q", got)
	}
	// a policy that does not exist is refused before a jail is touched
	if _, errs, code := h.run("", "firewall", "policy", "attach", "nope", "db"); code == 0 || !strings.Contains(errs, "nope") {
		t.Fatalf("attach of a missing policy: %d %q", code, errs)
	}
	// the mandatory list of the project
	run("policy monitoring is mandatory now", "firewall", "policy", "attach", "monitoring", "--mandatory")
	run("policy monitoring is mandatory already", "firewall", "policy", "attach", "monitoring", "--mandatory")
	out, _, _ := h.run("", "project", "show", "main")
	if !strings.Contains(out, "monitoring") {
		t.Fatalf("project show lacks the mandatory policy: %s", out)
	}
	run("policy monitoring is not mandatory any more", "firewall", "policy", "detach", "monitoring", "--mandatory")
	run("policy monitoring is not mandatory", "firewall", "policy", "detach", "monitoring", "--mandatory")
}

// the completion of attach offers the jails that lack the policy, the one of detach the jails that have it.
func TestPolicyAttachCompletion(t *testing.T) {
	h := newHarness(t)
	h.seed()
	for _, args := range [][]string{
		{"firewall", "policy", "create", "web-public"},
		{"jail", "create", "db", "--image", "freebsd-15", "--storage", "default", "--network", "lan", "--firewall-policy", "web-public"},
		{"jail", "create", "web", "--image", "freebsd-15", "--storage", "default", "--network", "lan"},
	} {
		if _, errs, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	for verb, want := range map[string]struct{ has, lacks string }{
		"attach": {has: "web\n", lacks: "db\n"},
		"detach": {has: "db\n", lacks: "web\n"},
	} {
		out, _, _ := h.run("", "__complete", "firewall", "policy", verb, "web-public", "")
		if !strings.Contains(out, want.has) || strings.Contains(out, want.lacks) {
			t.Errorf("%s completes %q: want %q and not %q", verb, out, want.has, want.lacks)
		}
	}
}

// firewall show starts with the policies of the jail in the order they are rendered.
func TestFirewallShowStartsWithThePolicies(t *testing.T) {
	h := newHarness(t)
	h.seed()
	for _, args := range [][]string{
		{"firewall", "policy", "create", "floor", "--default-in", "deny"},
		{"firewall", "policy", "create", "ssh-office"},
		{"project", "set", "main", "--firewall-policy", "floor"},
		{"jail", "create", "db", "--image", "freebsd-15", "--storage", "default", "--network", "lan", "--firewall-policy", "ssh-office"},
	} {
		if _, errs, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	out, errs, code := h.run("", "firewall", "show", "db")
	if code != 0 {
		t.Fatalf("firewall show: %s", errs)
	}
	i, j, k := strings.Index(out, "floor (project)"), strings.Index(out, "ssh-office"), strings.Index(out, "Reaches jail/db")
	if i < 0 || j < i || k < j {
		t.Fatalf("the policies are not listed first, project's before the jail's own:\n%s", out)
	}
	if !strings.Contains(out, "in deny") {
		t.Fatalf("the default the project policy states is not shown:\n%s", out)
	}
	if strings.Contains(out, "attach one:") {
		t.Fatalf("a jail with policies gets no hint:\n%s", out)
	}
}
