package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

const requirementsRecipe = "requirements:\n  - check: [test, -x, /usr/sbin/sshd]\n    message: sshd is not installed in the jail\nsteps:\n  - run: [echo, hi]\n    creates: /tmp/hi\n"

// requirements: --validate checks them offline (and says how many there are), a dry run lists them as not checked, a real
// apply that finds one unmet fails for that jail alone with the message, and a Linux jail is refused by name.
func TestJailApplyRequirements(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "alpine-3"},
		Spec: api.ImageSpec{OS: api.OSLinux, Release: "3.22", Arch: "amd64", Source: api.ImageSource{Type: "minirootfs"}}})
	running := api.JailStatus{Power: api.PowerRunning}
	for _, n := range []string{"a", "b"} {
		h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: n, Project: "main"}, Spec: api.JailSpec{Image: "freebsd-15"}, Status: running})
	}
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "lin", Project: "main"}, Spec: api.JailSpec{Image: "alpine-3", OS: api.OSLinux}, Status: running})
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "recipe.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	recipe := write(requirementsRecipe)

	out, errs, code := h.run("", "jail", "apply", "-f", recipe, "--validate")
	if code != 0 || !strings.Contains(out, "recipe is valid: 1 steps, 1 requirements") || len(h.srv.ApplyCalls()) != 0 {
		t.Fatalf("validate: %d %q %q", code, out, errs)
	}
	for body, want := range map[string]string{
		"requirements:\n  - check: [/usr/bin/true]\nsteps:\n  - run: [echo]\n":                         "requirements[0].message",
		"requirements:\n  - message: m\nsteps:\n  - run: [echo]\n":                                     "requirements[0].check",
		"requirements:\n  - {check: [/usr/bin/true], message: m, extra: 1}\nsteps:\n  - run: [echo]\n": "unknown field \"extra\"",
	} {
		if _, errs, code := h.run("", "jail", "apply", "-f", write(body), "--validate"); code != 2 || !strings.Contains(errs, want) {
			t.Errorf("%q: %d %q", want, code, errs)
		}
	}
	recipe = write(requirementsRecipe)

	// a dry run lists the requirement as not checked
	out, _, code = h.run("", "--dry-run", "jail", "apply", "a", "-f", recipe)
	if code != 0 || !strings.Contains(out, "sshd is not installed in the jail") || !strings.Contains(out, "not checked") {
		t.Fatalf("dry run: %d\n%s", code, out)
	}

	// an unmet requirement fails the jail it is unmet in; the others go on
	h.srv.SeedUnmetRequirement("main", "a", "install sshd")
	out, errs, code = h.run("", "jail", "apply", "a", "-f", recipe)
	if code != 1 || !strings.Contains(errs, "requirement not met: sshd is not installed in the jail") {
		t.Fatalf("an unmet requirement: %d %q", code, errs)
	}
	before := len(h.srv.ApplyCalls())
	out, errs, code = h.run("", "jail", "apply", "--all", "-f", recipe)
	if code != 1 || !strings.Contains(errs, "jail/a: failed:") || !strings.Contains(errs, "requirement not met") || !strings.Contains(out, "jail/b:") || !strings.Contains(out, "met") {
		t.Fatalf("--all: %d\n%s\n%s", code, out, errs)
	}
	var got []string
	for _, c := range h.srv.ApplyCalls()[before:] {
		got = append(got, c.Jail)
	}
	if strings.Join(got, " ") != "a b" {
		t.Fatalf("calls %v: a Linux jail is not part of a selection", got)
	}

	// a Linux jail that is named is refused: recipes are for FreeBSD jails
	_, errs, code = h.run("", "jail", "apply", "lin", "-f", recipe)
	if code == 0 || !strings.Contains(errs, "Linux jail") {
		t.Fatalf("a Linux jail: %d %q", code, errs)
	}
}
