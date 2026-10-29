package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/manpage"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(Env{Stdout: &out, Stderr: &errb, Version: "test", Getenv: func(string) string { return "" }}, args)
	return code, out.String(), errb.String()
}

// The hidden man command writes one mdoc page per command; it is not offered in the help.
func TestManPages(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1761609600")
	dir := filepath.Join(t.TempDir(), "man1")
	if code, out, errs := runCLI(t, "man", dir); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	for _, name := range []string{"jailops-sync.1", "jailops-sync-sync.1", "jailops-sync-drift.1", "jailops-sync-secrets-set.1", "jailops-sync-remote-bootstrap.1"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), ".Sh NAME\n.Nm "+strings.TrimSuffix(name, ".1")+"\n") || name != "jailops-sync.1" && !strings.Contains(string(b), ".Sh EXAMPLES") {
			t.Errorf("%s:\n%s", name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "jailops-sync-man.1")); err == nil {
		t.Error("the man command documented itself")
	}
	// every page follows the mdoc conventions (line length, one sentence per line, references in order)
	pages, _ := filepath.Glob(filepath.Join(dir, "*.1"))
	for _, p := range pages {
		b, _ := os.ReadFile(p)
		for _, msg := range manpage.Lint(string(b)) {
			t.Errorf("%s: %s", filepath.Base(p), msg)
		}
	}
	if _, out, _ := runCLI(t, "--help"); strings.Contains(out, "man ") {
		t.Errorf("man is listed in the help:\n%s", out)
	}
}

// A usage error (unknown command or flag, wrong arguments) is exit 2, the fatal status, and says where help is.
func TestUsageErrorsAreFatal(t *testing.T) {
	for _, args := range [][]string{{"frobnicate"}, {"plan", "--bogus"}, {"drift", "extra"}, {"secrets", "set"}} {
		code, _, errs := runCLI(t, args...)
		if code != ExitFatal || !strings.HasPrefix(errs, "Error: ") || !strings.Contains(errs, "run 'jailops-sync --help' for usage") {
			t.Errorf("%v: exit %d %q", args, code, errs)
		}
	}
	for _, args := range [][]string{{"version"}, {"--version"}} {
		if code, out, _ := runCLI(t, args...); code != 0 || strings.TrimSpace(out) != "jailops-sync test" {
			t.Errorf("%v: exit %d %q", args, code, out)
		}
	}
}
