package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupKeyImport covers the sources of the key (a recovery key from a file, standard input and the prompt, a key
// file with and without its passphrase, an age identity), the refusals (a wrong key, a second source, a different key
// without --replace) and --dry-run, which writes nothing.
func TestBackupKeyImport(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()

	rec := filepath.Join(h.dir, "recovery.txt")
	if err := os.WriteFile(rec, []byte("JOR1-dom-aaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	two := filepath.Join(h.dir, "two-lines.txt")
	_ = os.WriteFile(two, []byte("JOR1-dom-aaaa\nJOR1-dom-bbbb\n"), 0o600)
	kf := filepath.Join(h.dir, "backup.key")
	_ = os.WriteFile(kf, []byte(`{"version":1,"protection":"none","domain_id":"x","master":"eA=="}`), 0o600)
	wrapped := filepath.Join(h.dir, "wrapped.key")
	_ = os.WriteFile(wrapped, []byte(`{"version":1,"protection":"passphrase","domain_id":"x"}`), 0o600)
	pass := filepath.Join(h.dir, "pass.txt")
	_ = os.WriteFile(pass, []byte("correct horse\n"), 0o600)
	id := filepath.Join(h.dir, "identity.txt")
	_ = os.WriteFile(id, []byte("# created: 2026-01-01T00:00:00Z\n# public key: age1qqq\nAGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ\n"), 0o600)
	empty := filepath.Join(h.dir, "empty.txt")
	_ = os.WriteFile(empty, nil, 0o600)

	// nothing given and no terminal: the prompt cannot be answered
	tr.do("", "backup", "key", "import")
	// the sources that are wrong
	tr.do("", "backup", "key", "import", "--file", rec, "--identity-file", id)
	tr.do("", "backup", "key", "import", "--file", two)
	tr.do("", "backup", "key", "import", "--file", empty)
	tr.do("", "backup", "key", "import", "--file", filepath.Join(h.dir, "missing"))
	tr.do("", "backup", "key", "import", "--file", rec, "--passphrase-file", pass)
	tr.do("", "backup", "key", "import", "--identity-file", empty)
	tr.do("", "backup", "key", "import", "--file", wrapped, "--passphrase-file", filepath.Join(h.dir, "nopass"))
	tr.do("JOR1-x\n", "backup", "key", "import", "--file", "-")
	// a dry run writes nothing
	tr.ok("--dry-run", "backup", "key", "import", "--file", rec)
	tr.ok("backup", "key", "status")
	// the recovery key from a file, then again (the key it already holds), then another key
	tr.ok("backup", "key", "import", "--file", rec)
	tr.ok("backup", "key", "status")
	tr.ok("backup", "key", "import", "--file", rec)
	tr.do("JOR1-dom-bbbb\n", "backup", "key", "import", "--file", "-")
	// a key of another domain is refused by the node check, also with --replace and --dry-run
	tr.do("", "--dry-run", "backup", "key", "import", "--file", kf)
	tr.do("JOR1-dom-bbbb\n", "backup", "key", "import", "--file", "-", "--replace")
	// without the check it goes through: a key file, a protected key file and an identity
	tr.ok("--dry-run", "backup", "key", "import", "--file", kf, "--no-verify")
	tr.ok("backup", "key", "import", "--file", kf, "--replace", "--no-verify")
	tr.ok("backup", "key", "import", "--file", wrapped, "--replace", "--no-verify", "--passphrase-file", pass)
	tr.ok("backup", "key", "import", "--identity-file", id, "--replace", "--no-verify", "-o", "json")
	tr.golden("backup_key_import")

	// the secrets are not in the output of a later command
	out, _, _ := h.run("", "backup", "key", "status", "-o", "json")
	if strings.Contains(out, "JOR1-") || strings.Contains(out, "AGE-SECRET-KEY") {
		t.Fatalf("a secret came back: %s", out)
	}
}

// TestBackupKeyImportPromptsForTheRecoveryKey reads the recovery key from the terminal prompt.
func TestBackupKeyImportPromptsForTheRecoveryKey(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.secrets = []string{"JOR1-dom-cccc"}
	out, errs, code := h.run("", "backup", "key", "import")
	if code != 0 || !strings.Contains(out, "dom-cccc") || strings.Contains(out+errs, "JOR1-") {
		t.Fatalf("prompt import: %d %q %q", code, out, errs)
	}
}
