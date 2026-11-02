package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The passphrase flow of encrypted volumes: asked twice on a terminal (a fake one here), refused on a pipe, read
// from a file for scripts; unlock, lock and the change of the passphrase. No passphrase is ever printed or sent
// anywhere but in the request bodies, which the fake records with the passphrase replaced.
func TestJailEncryptedVolumes(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.env["TERM"], h.env["NO_COLOR"] = "dumb", "1"
	tr := h.transcript()
	const pass, other = "marker-cli-correct-horse", "marker-cli-new-passphrase"
	create := []string{"jail", "create", "vault", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default", "--volume", "db:/var/db/app:encrypted:5G"}

	// no terminal and no file: refused before anything is sent
	tr.do("", create...)
	h.tty = true
	h.secrets = []string{pass, pass + "x"}
	tr.do("", create...)
	h.secrets = []string{"too-short", "too-short"}
	tr.do("", create...)
	h.secrets = []string{pass, pass}
	tr.do("", create...)
	h.tty = false

	// a script: the passphrase in a file, warned about when others can read it
	file := filepath.Join(h.dir, "vault.pass")
	if err := os.WriteFile(file, []byte(pass+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr.do("", "jail", "create", "vault2", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default", "--volume", "db:/srv/db:encrypted", "--passphrase-file", file)
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	tr.do("", "jail", "create", "plain", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default", "--passphrase-file", file, "--volume", "db:/srv/db")

	// a host boot locks both; a locked jail does not start
	h.srv.RebootHost()
	tr.ok("jail", "list")
	tr.do("", "jail", "start", "vault")
	tr.ok("explain", "locked")
	h.tty = true
	h.secrets = []string{"marker-cli-wrong-passphrase"}
	tr.do("", "jail", "unlock", "vault")
	h.secrets = []string{pass}
	tr.do("", "jail", "unlock", "vault", "--start")
	tr.do("", "jail", "unlock", "vault2", "--passphrase-file", file)
	tr.ok("jail", "list", "--state", "locked")
	tr.ok("jail", "list")
	tr.do("", "jail", "lock", "vault")
	tr.ok("jail", "list", "--state", "locked")
	// the passphrase changes: the current one once, the new one twice
	h.secrets = []string{pass}
	tr.do("", "jail", "unlock", "vault")
	h.secrets = []string{pass, other, other}
	tr.do("", "jail", "passphrase", "vault")
	h.secrets = []string{pass}
	tr.do("", "jail", "lock", "vault")
	tr.do("", "jail", "unlock", "vault")
	h.secrets = []string{other}
	tr.do("", "jail", "unlock", "vault")
	h.tty = false
	tr.do("", "jail", "unlock", "vault")
	tr.golden("jail_encrypted")

	if strings.Contains(tr.buf.String(), "marker-cli") {
		t.Fatal("a passphrase was printed")
	}
	sent := 0
	for _, r := range h.srv.Requests() {
		if strings.Contains(string(r.Body), "marker-cli") || strings.Contains(r.Path+r.Query, "marker-cli") {
			t.Fatalf("the fake recorded a passphrase: %s %s %s", r.Method, r.Path, r.Body)
		}
		if strings.Contains(string(r.Body), `"redacted"`) {
			sent++
		}
	}
	if sent < 6 {
		t.Fatalf("only %d requests carried a passphrase", sent)
	}
}
