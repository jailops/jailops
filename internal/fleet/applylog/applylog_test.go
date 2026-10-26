package applylog

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/fleet/secrets"
)

func TestLogIsJSONLRedactedAndPrivate(t *testing.T) {
	red := secrets.NewRedactor()
	red.Add([]byte("plaintext-token-123"))
	dir := t.TempDir()
	l, err := Open(dir, "f", red)
	if err != nil {
		t.Fatal(err)
	}
	l.Write(KindApplyStart, map[string]any{"fleet": "f"})
	l.Write(KindChange, map[string]any{"error": "boom plaintext-token-123 boom"})
	l.Close()
	st, _ := os.Stat(l.Path())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	b, _ := os.ReadFile(l.Path())
	if strings.Contains(string(b), "plaintext-token-123") {
		t.Fatalf("log leaked: %s", b)
	}
	ev, err := Read(l.Path())
	if err != nil || len(ev) != 2 || ev[0].Kind != KindApplyStart || ev[1].Kind != KindChange {
		t.Fatalf("%v %v", ev, err)
	}
	var nilLog *Log
	nilLog.Write("x", nil) // a nil log discards
	nilLog.Close()
}

func TestLockExcludesAndReleases(t *testing.T) {
	dir := t.TempDir()
	a, err := Acquire(dir, "f", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(dir, "f", 100*time.Millisecond)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire: %v", err)
	}
	// Another fleet is independent.
	b, err := Acquire(dir, "g", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
	a.Release()
	c, err := Acquire(dir, "f", time.Second)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	c.Release()
}

func TestStateDirPrecedence(t *testing.T) {
	t.Setenv("JAILOPS_SYNC_STATE_DIR", "/env")
	t.Setenv("XDG_STATE_HOME", "/xdg")
	if StateDir("/flag") != "/flag" || StateDir("") != "/env" {
		t.Fatal("explicit and env precedence")
	}
	t.Setenv("JAILOPS_SYNC_STATE_DIR", "")
	if StateDir("") != "/xdg/jailops-sync" {
		t.Fatalf("%s", StateDir(""))
	}
}
