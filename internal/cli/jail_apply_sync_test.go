package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A jail that jailops-sync keeps is put back by its next sync: apply says so, and says nothing for a jail sync does not keep.
func TestJailApplyWarnsWhenSyncManagesTheJail(t *testing.T) {
	h := newHarness(t)
	h.seed()
	running := api.JailStatus{Power: api.PowerRunning}
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "kept", Project: "main", Labels: map[string]string{"sync.jailops.net/hash": "abc"}}, Spec: api.JailSpec{Image: "freebsd-15"}, Status: running})
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "mine", Project: "main"}, Spec: api.JailSpec{Image: "freebsd-15"}, Status: running})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte("steps:\n  - run: [echo, hi]\n    creates: /tmp/hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := h.run("", "jail", "apply", "kept", "-f", dir)
	if code != 0 || !strings.Contains(errOut, "jail/kept is managed by sync, the next sync may revert this") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = h.run("", "jail", "apply", "mine", "-f", dir)
	if code != 0 || strings.Contains(errOut, "managed by sync") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	// a dry run changes nothing, so it does not warn
	_, errOut, _ = h.run("", "jail", "apply", "kept", "-f", dir, "--dry-run")
	if strings.Contains(errOut, "managed by sync") {
		t.Fatalf("a dry run warned: %q", errOut)
	}
}

// jail apply on many jails: -l and --all select as 'jail base add' does, every jail gets the same rendered recipe and
// its own block of result, a failure stops no other jail and makes the exit code non-zero, a Linux jail is left out.
func TestJailApplySelection(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "alpine-3"},
		Spec: api.ImageSpec{OS: api.OSLinux, Release: "3.22", Arch: "amd64", Source: api.ImageSource{Type: "minirootfs"}}})
	running := api.JailStatus{Power: api.PowerRunning}
	seed := func(name string, os api.OS, tier string) {
		img := "freebsd-15"
		if os == api.OSLinux {
			img = "alpine-3"
		}
		h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: name, Project: "main", Labels: map[string]string{"tier": tier}}, Spec: api.JailSpec{Image: img, OS: os}, Status: running})
	}
	seed("a", api.OSFreeBSD, "web")
	seed("b", api.OSFreeBSD, "web")
	seed("c", api.OSFreeBSD, "db")
	seed("lin", api.OSLinux, "web")
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recipe := filepath.Join(dir, "recipe.yaml")
	body := "vars:\n  pw: {secret: true}\nsteps:\n  - file:\n      path: /usr/local/etc/app.conf\n      content: \"pw=${pw}\\n\"\n      template: true\n"
	if err := os.WriteFile(recipe, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := func() []string {
		var out []string
		for _, c := range h.srv.ApplyCalls() {
			out = append(out, c.Jail)
		}
		return out
	}

	// named and selected together, and nothing at all, are refused before anything is sent
	for _, args := range [][]string{{"a", "--all"}, {"a", "-l", "tier=web"}, {"--all", "-l", "tier=web"}, {}} {
		_, errs, code := h.run("", append([]string{"jail", "apply", "-f", recipe, "--set-file", "pw=" + pw}, args...)...)
		if code != 2 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, errs)
		}
	}
	if len(calls()) != 0 {
		t.Fatalf("a refused command sent %v", calls())
	}

	// a selection that matches nothing is not an error
	if _, errs, code := h.run("", "jail", "apply", "-l", "tier=none", "-f", recipe, "--set-file", "pw="+pw); code != 0 || !strings.Contains(errs, "No jails match.") {
		t.Fatalf("no match: exit %d, stderr %q", code, errs)
	}

	// -l: one block per jail, one rendering
	out, errs, code := h.run("", "jail", "apply", "-l", "tier=web", "-f", recipe, "--set-file", "pw="+pw)
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Join(calls(), " ") != "a b" {
		t.Fatalf("calls %v (a Linux jail is not part of a selection)", calls())
	}
	if !strings.Contains(out, "jail/a:") || !strings.Contains(out, "jail/b:") {
		t.Fatalf("no block per jail:\n%s", out)
	}
	cs := h.srv.ApplyCalls()
	if cs[0].Req.Recipe.Steps[0].File == nil || *cs[0].Req.Recipe.Steps[0].File.Content != "pw=s3cret\n" || !cs[0].Req.Sensitive {
		t.Fatalf("request: %+v", cs[0].Req)
	}
	if a, b := *cs[0].Req.Recipe.Steps[0].File.Content, *cs[1].Req.Recipe.Steps[0].File.Content; a != b {
		t.Fatalf("the jails got different recipes: %q %q", a, b)
	}

	// --dry-run reads and changes nothing, with a selection too
	out, _, code = h.run("", "--dry-run", "jail", "apply", "--all", "-f", recipe, "--set-file", "pw="+pw)
	if code != 0 || strings.Count(out, "dry run, nothing was changed") != 3 {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	for _, c := range h.srv.ApplyCalls()[2:] {
		if !c.Req.DryRun {
			t.Fatalf("a dry run sent a real apply to %s", c.Jail)
		}
	}

	// a failure on one jail: the others are done, the exit code says it
	h.srv.SeedApplyFailure("main", "b", 0, "exit status 1: boom")
	before := len(h.srv.ApplyCalls())
	out, errs, code = h.run("", "jail", "apply", "--all", "-f", recipe, "--set-file", "pw="+pw)
	if code == 0 || !strings.Contains(errs, "jail/b: failed") {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var got []string
	for _, c := range h.srv.ApplyCalls()[before:] {
		got = append(got, c.Jail)
	}
	if strings.Join(got, " ") != "a b c" || !strings.Contains(out, "jail/c:") {
		t.Fatalf("calls %v, out:\n%s", got, out)
	}

	// --validate renders and checks, and sends nothing
	before = len(h.srv.ApplyCalls())
	if out, _, code = h.run("", "jail", "apply", "--all", "-f", recipe, "--set-file", "pw="+pw, "--validate"); code != 0 || !strings.Contains(out, "recipe is valid") || len(h.srv.ApplyCalls()) != before {
		t.Fatalf("validate: exit %d\n%s", code, out)
	}
}
