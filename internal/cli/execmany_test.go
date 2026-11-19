package cli_test

import (
	"strings"
	"testing"
)

func manyHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.seed()
	for _, n := range []string{"web1", "web2", "db"} {
		h.run("", "create", n, "--image", "freebsd-15", "--network", "lan", "--storage", "default")
		h.run("", "start", n)
	}
	h.run("", "label", "web1", "role=web")
	h.run("", "label", "web2", "role=web")
	h.run("", "label", "db", "role=db")
	h.run("", "create", "cold", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	h.run("", "label", "cold", "role=web")
	return h
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestExecOnSeveralNamedJails(t *testing.T) {
	h := manyHarness(t)
	out, errs, code := h.run("", "exec", "web1", "web2", "--", "echo", "hello")
	if code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	got := lines(out)
	if len(got) != 2 {
		t.Fatalf("%q", out)
	}
	for _, want := range []string{"[web1] hello", "[web2] hello"} {
		found := false
		for _, l := range got {
			found = found || l == want
		}
		if !found {
			t.Errorf("no %q in %q", want, out)
		}
	}
	if !strings.Contains(errs, "exec: 2 ok") {
		t.Fatalf("summary: %q", errs)
	}
}

func TestExecOnALabel(t *testing.T) {
	h := manyHarness(t)
	out, errs, code := h.run("", "exec", "-l", "role=web", "--", "echo", "hi")
	if code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	// cold is stopped and was matched by the selector: skipped with a note, not a failure
	if strings.Contains(out, "[cold]") || !strings.Contains(errs, "1 skipped (cold: not running)") || !strings.Contains(errs, "2 ok") {
		t.Fatalf("%q %q", out, errs)
	}
	// without the "--": the command is everything after the flags
	if out, _, code = h.run("", "exec", "-l", "role=db", "echo", "x"); code != 0 || strings.TrimSpace(out) != "[db] x" {
		t.Fatalf("%d %q", code, out)
	}
	// all
	if _, errs, code = h.run("", "exec", "--all", "--", "echo", "x"); code != 0 || !strings.Contains(errs, "3 ok") {
		t.Fatalf("%d %q", code, errs)
	}
	// nothing matches
	if out, _, code = h.run("", "exec", "-l", "role=none", "--", "echo", "x"); code != 0 || !strings.Contains(out, "no jail matched") {
		t.Fatalf("%d %q", code, out)
	}
	// names and a selector together are refused
	if _, _, code = h.run("", "exec", "web1", "-l", "role=web", "--", "echo"); code != 2 {
		t.Fatalf("names and a selector: %d", code)
	}
}

func TestExecManyExitStatus(t *testing.T) {
	h := manyHarness(t)
	// a command that fails on every jail: the highest status wins
	_, errs, code := h.run("", "exec", "web1", "db", "--", "exit", "3")
	if code != 3 || !strings.Contains(errs, "2 failed") || !strings.Contains(errs, "web1: exit 3") {
		t.Fatalf("%d %q", code, errs)
	}
	// a jail that cannot run it at all is 1; a named stopped jail is a failure
	_, errs, code = h.run("", "exec", "web1", "cold", "ghost", "--", "echo", "x")
	if code != 1 || !strings.Contains(errs, "1 ok") || !strings.Contains(errs, "cold: not running") || !strings.Contains(errs, "ghost:") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestExecManyOutputIsPrefixedAndSeparated(t *testing.T) {
	h := manyHarness(t)
	out, errs, code := h.run("", "exec", "web1", "web2", "--", "stderr", "oops")
	if code != 0 || out != "" {
		t.Fatalf("stdout must be empty: %d %q", code, out)
	}
	for _, want := range []string{"[web1] oops", "[web2] oops"} {
		if !strings.Contains(errs, want+"\n") {
			t.Errorf("no %q in %q", want, errs)
		}
	}
}

func TestExecManyRefusesATerminalAndStdin(t *testing.T) {
	h := manyHarness(t)
	if _, _, code := h.run("", "exec", "-t", "web1", "web2", "--", "echo", "x"); code != 2 {
		t.Fatalf("-t with several jails: %d", code)
	}
	// stdin is not forwarded: cat sees the end of input at once
	out, _, code := h.run("typed\n", "exec", "web1", "web2", "--", "cat")
	if code != 0 || out != "" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestExecManyParallelBound(t *testing.T) {
	h := manyHarness(t)
	if _, _, code := h.run("", "exec", "--parallel", "0", "web1", "web2", "--", "echo"); code != 2 {
		t.Fatalf("parallel 0: %d", code)
	}
	if _, _, code := h.run("", "exec", "--parallel", "1", "web1", "web2", "db", "--", "sleep", "30"); code != 0 {
		t.Fatalf("parallel 1: %d", code)
	}
	if p := h.srv.PeakExecs(); p != 1 {
		t.Fatalf("--parallel 1 ran %d at once", p)
	}
	if _, _, code := h.run("", "exec", "--parallel", "3", "web1", "web2", "db", "--", "sleep", "200"); code != 0 {
		t.Fatalf("parallel 3: %d", code)
	}
	if p := h.srv.PeakExecs(); p < 2 || p > 3 {
		t.Fatalf("--parallel 3 ran %d at once", p)
	}
	if _, _, code := h.run("", "exec", "--parallel", "99", "web1", "web2", "--", "echo"); code != 2 {
		t.Fatalf("parallel 99: %d", code)
	}
}

func TestExecManyDryRunRunsNothing(t *testing.T) {
	h := manyHarness(t)
	out, _, code := h.run("", "--dry-run", "exec", "web1", "web2", "--", "rm", "-rf", "/")
	if code != 0 || !strings.Contains(out, "dry-run") {
		t.Fatalf("%d %q", code, out)
	}
	for _, c := range h.srv.ExecCalls() {
		if len(c.Argv) > 0 && c.Argv[0] == "rm" {
			t.Fatal("dry-run ran the command")
		}
	}
}

func TestExecControlCharactersOfAJailAreNotPassedOn(t *testing.T) {
	h := manyHarness(t)
	out, _, _ := h.run("", "exec", "web1", "web2", "--", "echo", "a\x1b]0;owned\x07b")
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\x07") {
		t.Fatalf("%q", out)
	}
}
