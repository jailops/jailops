package cli_test

import (
	"strings"
	"testing"
)

func TestPkgRunsPkgInTheJails(t *testing.T) {
	h := manyHarness(t)
	argvs := func() []string {
		var out []string
		for _, c := range h.srv.ExecCalls() {
			out = append(out, c.Jail+": "+strings.Join(c.Argv, " "))
		}
		return out
	}
	// pkg is not in the fake: it exits 127, which is the status of the command
	if _, _, code := h.run("", "pkg", "install", "web1", "--", "nginx", "curl"); code != 127 {
		t.Fatalf("%d", code)
	}
	if got := argvs(); len(got) != 1 || got[0] != "web1: pkg install -y nginx curl" {
		t.Fatalf("%v", got)
	}
	h.run("", "pkg", "remove", "web1", "web2", "--", "vim-tiny")
	h.run("", "pkg", "upgrade", "-l", "role=web")
	h.run("", "pkg", "info", "db", "--", "nginx")
	got := strings.Join(argvs(), "\n")
	for _, want := range []string{"web2: pkg delete -y vim-tiny", "web1: pkg upgrade -y", "db: pkg info nginx"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	for _, c := range h.srv.ExecCalls() {
		if c.TTY {
			t.Fatal("pkg asked for a terminal")
		}
	}
}

func TestPkgUsage(t *testing.T) {
	h := manyHarness(t)
	for name, args := range map[string][]string{
		"unknown action":     {"pkg", "frobnicate", "web1"},
		"install no package": {"pkg", "install", "web1"},
		"no jail":            {"pkg", "info"},
		"names and selector": {"pkg", "info", "web1", "-l", "role=web"},
	} {
		if _, _, code := h.run("", args...); code != 2 {
			t.Errorf("%s: exit %d", name, code)
		}
	}
}
