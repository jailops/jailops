package cli_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
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

// pkg(8) is FreeBSD's: a Linux jail is silently not part of a selection, a named Linux jail is refused, and a
// selection with nothing but Linux jails runs nothing and succeeds.
func TestPkgLeavesLinuxJailsOut(t *testing.T) {
	h := manyHarness(t)
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "alpine-3"},
		Spec: api.ImageSpec{OS: api.OSLinux, Release: "3.22", Arch: "amd64", Source: api.ImageSource{Type: "minirootfs"}}})
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "alp", Project: "main", Labels: map[string]string{"role": "web", "os": "linux"}},
		Spec: api.JailSpec{Image: "alpine-3", OS: api.OSLinux}, Status: api.JailStatus{Power: api.PowerRunning}})
	ran := func() []string {
		var out []string
		for _, c := range h.srv.ExecCalls() {
			out = append(out, c.Jail)
		}
		return out
	}
	inLinux := func() bool {
		for _, j := range ran() {
			if j == "alp" {
				return true
			}
		}
		return false
	}

	out, errs, _ := h.run("", "pkg", "upgrade", "-l", "role=web")
	if inLinux() || strings.Contains(out+errs, "alp") || strings.Contains(out+errs, "Linux") {
		t.Fatalf("selector: %v %q %q", ran(), out, errs)
	}
	n := len(ran())
	out, errs, _ = h.run("", "pkg", "info", "alp", "web1")
	if inLinux() || strings.Contains(out+errs, "Linux") || len(ran()) != n+1 {
		t.Fatalf("several names: %v %q %q", ran(), out, errs)
	}

	_, errs, code := h.run("", "pkg", "install", "alp", "--", "nginx")
	if code == 0 || !strings.Contains(errs, "jail alp is a Linux jail: pkg is FreeBSD's package manager; use the distribution's own with jailops exec") || inLinux() {
		t.Fatalf("named: %d %q", code, errs)
	}

	n = len(ran())
	if _, errs, code := h.run("", "pkg", "upgrade", "-l", "os=linux"); code != 0 || strings.Contains(errs, "Linux") || len(ran()) != n {
		t.Fatalf("only Linux: %d %q", code, errs)
	}
}
