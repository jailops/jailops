package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestServiceStartArgv(t *testing.T) {
	got, err := serviceStartArgv("nginx")
	if err != nil || !slices.Equal(got, []string{"/bin/sh", "-c", serviceStartScript, "jailops-start", "nginx"}) {
		t.Fatalf("%q %v", got, err)
	}
	for _, n := range []string{"php-fpm", "postgresql", "valkey", "forgejo", "a.b_c", "X11"} {
		if a, err := serviceStartArgv(n); err != nil || a[4] != n || a[2] != serviceStartScript {
			t.Errorf("%s: %q %v", n, a, err)
		}
	}
	for _, n := range []string{"", "-f", "a b", "a;id", "$(id)", "`id`", "a\nb", "../x", "a/b", "\"x\"", strings.Repeat("a", 65)} {
		if a, err := serviceStartArgv(n); err == nil {
			t.Errorf("%q accepted: %q", n, a)
		}
	}
}

// The wrapper's script, run by a real sh with the in-jail programs replaced:
// a daemon the rc script leaves behind does not hold the caller's output, a
// failure prints what the script said and keeps its status, and the name is
// an argument, never shell text.
func TestServiceStartScript(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "service")
	// the stand-in for service(8): records its arguments, starts a "daemon" that keeps its descriptors, fails for "bad"
	if err := os.WriteFile(svc, []byte("#!/bin/sh\nprintf '%s|' \"$@\" > \"$REC\"\nsleep 5 &\necho \"starting $1\"\n[ \"$1\" != bad ] || { echo \"cannot start $1\" >&2; exit 3; }\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("/usr/sbin/service", svc, "/usr/bin/mktemp -t jailops", "mktemp").Replace(serviceStartScript)
	if script == serviceStartScript {
		t.Fatal("nothing replaced")
	}
	run := func(name string) (string, int, time.Duration) {
		t.Helper()
		rec := filepath.Join(dir, "rec")
		cmd := exec.Command("/bin/sh", "-c", script, serviceStartName, name)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "REC=" + rec, "TMPDIR=" + dir}
		start := time.Now()
		out, err := cmd.CombinedOutput() // waits for the end of the output, as the in-jail exec does
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(rec); string(b) != name+"|start|" {
			t.Errorf("service got %q", b)
		}
		return string(out), code, time.Since(start)
	}
	out, code, took := run("nginx")
	if out != "" || code != 0 || took >= 4*time.Second {
		t.Errorf("success: %q %d after %s", out, code, took)
	}
	out, code, took = run("bad")
	if !strings.Contains(out, "starting bad") || !strings.Contains(out, "cannot start bad") || code != 3 || took >= 4*time.Second {
		t.Errorf("failure: %q %d after %s", out, code, took)
	}
	hostile := "x; touch " + filepath.Join(dir, "pwned") + " $(touch " + filepath.Join(dir, "pwned2") + ")"
	if _, code, _ = run(hostile); code != 0 {
		t.Errorf("hostile name: %d", code)
	}
	for _, f := range []string{"pwned", "pwned2"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("the name was run as shell text (%s)", f)
		}
	}
	// the temporary files are gone
	if left, _ := filepath.Glob(filepath.Join(dir, "tmp.*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
