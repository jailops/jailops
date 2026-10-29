package backupd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes one command line with no configuration file: only what needs none may succeed.
func runNoConfig(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	st := RunCLI(context.Background(), append([]string{"-c", filepath.Join(t.TempDir(), "none.yaml")}, args...), strings.NewReader(""), &out, &errb)
	return st, out.String(), errb.String()
}

// Every command answers --help with its synopsis and description on standard output, exit 0, and reads no
// configuration; the ones with flags list them.
func TestHelpOfEveryCommand(t *testing.T) {
	for _, v := range verbs {
		args := append(strings.Fields(v.name), "--help")
		st, out, errs := runNoConfig(t, args...)
		if st != 0 || errs != "" {
			t.Errorf("%s --help: exit %d, stderr %q", v.name, st, errs)
		}
		if !strings.Contains(out, "usage: jailops-backupd [-c FILE] [--json] "+v.syn) || !strings.Contains(out, v.desc) {
			t.Errorf("%s --help:\n%s", v.name, out)
		}
		for _, f := range v.flags {
			name, _, _ := flagName(f)
			if !strings.Contains(out, "  -"+strings.TrimPrefix(name, "--")) {
				t.Errorf("%s --help does not list %s:\n%s", v.name, name, out)
			}
		}
	}
	// the forms without a row of their own answer with their command
	for _, args := range [][]string{{"hosts", "list", "-h"}, {"delete", "cancel", "--help"}, {"verify", "all", "--help"}} {
		if st, out, _ := runNoConfig(t, args...); st != 0 || !strings.Contains(out, "usage: jailops-backupd") {
			t.Errorf("%v: exit %d\n%s", args, st, out)
		}
	}
}

func TestGlobalOptionsAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-version"}, {"version"}, {"--json", "version"}, {"version", "--json"}} {
		if st, out, _ := runNoConfig(t, args...); st != 0 || !strings.HasPrefix(out, "jailops-backupd ") || !strings.Contains(out, "schema") {
			t.Errorf("%v: exit %d %q", args, st, out)
		}
	}
	// a bad flag of a command names the command and the way to its help, exit 1
	if st, _, errs := runNoConfig(t, "gc", "--bogus"); st != 1 || !strings.Contains(errs, "gc --help") || !strings.Contains(errs, "bogus") {
		t.Errorf("gc --bogus: exit %d %q", st, errs)
	}
	if st, _, errs := runNoConfig(t, "--bogus", "status"); st != 2 || !strings.Contains(errs, "unknown option") {
		t.Errorf("--bogus: exit %d %q", st, errs)
	}
	// --config, in both forms, after the command too; the file does not exist, so the error names it
	for _, args := range [][]string{{"status", "--config", "/nonexistent/b.yaml"}, {"--config=/nonexistent/b.yaml", "status"}, {"status", "-c=/nonexistent/b.yaml"}} {
		if st, _, errs := runNoConfig(t, args...); st != 1 || !strings.Contains(errs, "/nonexistent/b.yaml") {
			t.Errorf("%v: exit %d %q", args, st, errs)
		}
	}
	if st, _, errs := runNoConfig(t, "init", "--admin", "x", "--password-prompt", "--password-file", "f"); st != 1 || !strings.Contains(errs, "exclude each other") {
		t.Errorf("both password sources: exit %d %q", st, errs)
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "dsn")
	_ = os.WriteFile(dsn, []byte("postgres://x@localhost/db\n"), 0o600)
	cfg := filepath.Join(dir, "b.yaml")
	_ = os.WriteFile(cfg, []byte("hostname: n\ndatabase: {dsn_file: "+dsn+"}\ndatastore: {kind: directory, directory: "+filepath.Join(dir, "repo")+"}\n"+
		"tls: {cert_file: "+filepath.Join(dir, "n.crt")+", key_file: "+filepath.Join(dir, "n.key")+"}\n"), 0o600)
	var out, errb bytes.Buffer
	if st := RunCLI(context.Background(), []string{"check-config", "-c", cfg}, strings.NewReader(""), &out, &errb); st != 0 || !strings.Contains(out.String(), "configuration is valid") || !strings.Contains(out.String(), "missing (init creates it)") {
		t.Fatalf("exit %d\n%s%s", st, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "repo")); err == nil {
		t.Fatal("check-config touched the datastore")
	}
	// the secret file rule applies here too
	_ = os.Chmod(dsn, 0o644)
	out.Reset()
	errb.Reset()
	if st := RunCLI(context.Background(), []string{"-c", cfg, "check-config"}, strings.NewReader(""), &out, &errb); st != 1 || !strings.Contains(errb.String(), "readable by group or others") {
		t.Fatalf("exit %d %s", st, errb.String())
	}
	_ = os.WriteFile(cfg, []byte("nope: 1\n"), 0o600)
	if st := RunCLI(context.Background(), []string{"-c", cfg, "check-config"}, strings.NewReader(""), &out, &errb); st != 1 {
		t.Fatalf("an invalid configuration passed: %d", st)
	}
}

// The completion scripts name every command and parse in their shells (checked with the shells that are installed).
func TestCompletionScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		st, out, errs := runNoConfig(t, "completion", sh)
		if st != 0 || errs != "" {
			t.Fatalf("completion %s: exit %d %s", sh, st, errs)
		}
		// fish writes a flag as "-l name" and a subcommand as a condition on its words
		for _, v := range verbs {
			for _, word := range strings.Fields(v.name) {
				if !strings.Contains(out, word) {
					t.Errorf("%s: %q is missing", sh, word)
				}
			}
			for _, f := range v.flags {
				if n, _, _ := flagName(f); !strings.Contains(out, strings.TrimPrefix(n, "--")) {
					t.Errorf("%s: %s is missing", sh, n)
				}
			}
		}
		path := filepath.Join(t.TempDir(), "jailops-backupd."+sh)
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.LookPath(sh); err != nil {
			t.Logf("%s is not installed: syntax not checked", sh)
			continue
		}
		if b, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, b)
		}
	}
	if st, _, errs := runNoConfig(t, "completion", "csh"); st != 1 || !strings.Contains(errs, "unknown shell") {
		t.Errorf("csh: exit %d %q", st, errs)
	}
}
