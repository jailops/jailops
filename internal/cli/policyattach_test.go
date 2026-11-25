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

// attachmentsHost makes a project with a mandatory policy, a jail with policies of its own, a jail that gets its policy from
// a profile and a jail with none.
func attachmentsHost(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.seed()
	for _, args := range [][]string{
		{"firewall", "policy", "create", "floor", "--default-in", "deny"},
		{"firewall", "policy", "create", "web-public", "--default-out", "deny"},
		{"firewall", "policy", "create", "ssh-office"},
		{"profile", "create", "base", "--set", "firewall.policies=ssh-office"},
		{"jail", "create", "web", "--image", "freebsd-15", "--storage", "default", "--network", "lan", "--label", "env=prod", "--firewall-policy", "web-public"},
		{"jail", "create", "db", "--image", "freebsd-15", "--storage", "default", "--network", "lan", "--label", "env=prod"},
		{"jail", "create", "mail", "--image", "freebsd-15", "--storage", "default", "--network", "lan"},
		{"config", "set", "db", "profiles", "base"},
		{"firewall", "policy", "attach", "floor", "--mandatory"},
	} {
		if _, errs, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s", args, errs)
		}
	}
	return h
}

func TestPolicyAttachments(t *testing.T) {
	h := attachmentsHost(t)
	out, errs, code := h.run("", "firewall", "policy", "attachments", "-o", "csv")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	// every jail has the mandatory policy first; web its own, db the one of its profile; the duplicate is shown twice
	want := `JAIL,POLICY,SOURCE,#,DEFAULT-IN,DEFAULT-OUT
db,floor,project,1,deny,-
db,ssh-office,profile:base,2,-,-
mail,floor,project,1,deny,-
web,floor,project,1,deny,-
web,web-public,jail,2,-,deny
`
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	// a policy that is mandatory and attached to a jail renders twice, and shows twice
	if _, errs, code := h.run("", "firewall", "policy", "attach", "floor", "web"); code != 0 {
		t.Fatal(errs)
	}
	out, _, _ = h.run("", "firewall", "policy", "attachments", "--jail", "web", "-o", "csv", "-c", "jail,policy,source,#")
	if want := "JAIL,POLICY,SOURCE,#\nweb,floor,project,1\nweb,web-public,jail,2\nweb,floor,jail,3\n"; out != want {
		t.Fatalf("duplicate: got\n%s", out)
	}
	// filters
	out, _, _ = h.run("", "firewall", "policy", "attachments", "--policy", "ssh-office", "-o", "name")
	if out != "db\n" {
		t.Fatalf("--policy: %q", out)
	}
	out, _, _ = h.run("", "firewall", "policy", "attachments", "-l", "env=prod", "--policy", "floor", "-o", "name")
	if out != "db\nweb\n" {
		t.Fatalf("-l: %q", out)
	}
	if _, errs, code := h.run("", "firewall", "policy", "attachments", "--policy", "nope"); code == 0 || !strings.Contains(errs, "nope") {
		t.Fatalf("unknown policy: %d %q", code, errs)
	}
	if _, errs, code := h.run("", "firewall", "policy", "attachments", "--jail", "nope"); code == 0 || !strings.Contains(errs, "nope") {
		t.Fatalf("unknown jail: %d %q", code, errs)
	}
	// sort by a column, the project column of -o wide, json members
	out, _, _ = h.run("", "firewall", "policy", "attachments", "--sort", "policy", "-o", "csv", "-c", "policy,jail")
	if !strings.HasPrefix(out, "POLICY,JAIL\nfloor,db\nfloor,mail\nfloor,web\nfloor,web\n") {
		t.Fatalf("--sort: %q", out)
	}
	out, _, _ = h.run("", "firewall", "policy", "attachments", "--jail", "db", "-o", "json")
	for _, s := range []string{`"project": "main"`, `"source": "profile"`, `"profile": "base"`, `"position": 2`, `"default_in": "deny"`} {
		if !strings.Contains(out, s) {
			t.Fatalf("json lacks %s: %s", s, out)
		}
	}
	out, _, _ = h.run("", "firewall", "policy", "attachments", "-o", "wide", "--jail", "db")
	if !strings.Contains(out, "PROJECT") || !strings.Contains(out, "main") {
		t.Fatalf("wide: %s", out)
	}
}

// without a mandatory policy a jail with none is named under the table; with nothing attached the command says how to.
func TestPolicyAttachmentsNoneAndEmpty(t *testing.T) {
	h := attachmentsHost(t)
	h.run("", "firewall", "policy", "detach", "floor", "--mandatory")
	out, errs, _ := h.run("", "firewall", "policy", "attachments", "-o", "table")
	if !strings.Contains(out, "web-public") || !strings.Contains(errs, "open to their project): mail") {
		t.Fatalf("footer: %q %q", out, errs)
	}
	h.run("", "firewall", "policy", "detach", "web-public", "web")
	h.run("", "config", "unset", "db", "profiles")
	out, errs, code := h.run("", "firewall", "policy", "attachments")
	if code != 0 || out != "" || !strings.Contains(errs, "No firewall policy is attached in project main. Attach one with: jailops firewall policy attach POLICY JAIL...") {
		t.Fatalf("empty: %d %q %q", code, out, errs)
	}
	out, _, _ = h.run("", "firewall", "policy", "attachments", "-o", "json")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty json: %q", out)
	}
}
