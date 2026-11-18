package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk"
)

// The name of a remote is a file name in the certs directory: only the names every other object has are accepted.
func TestRemoteNameIsAName(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"../x", "a/b", "a\\b", "Up", "9a", "a.b", "local", "x-", strings.Repeat("a", 64)} {
		out, errs, code := h.run("", "remote", "add", name, "127.0.0.1:1", "--token", "api-token")
		if code != 2 || !strings.Contains(errs, "remote name") && !strings.Contains(errs, "socket of this host") {
			t.Fatalf("%q: %d %q %q", name, code, out, errs)
		}
	}
	if _, err := os.Stat(filepath.Join(h.dir, "certs")); err == nil {
		t.Fatal("a refused name made the certs directory")
	}
}

// A key file of the remote's name that exists is never written through or replaced (a symbolic link in the certs
// directory would otherwise let the command overwrite another file); a removed remote takes its key pair with it.
func TestRemoteKeyFilesAreNeverOverwritten(t *testing.T) {
	srv, ts, fp := tlsFake(t)
	dir := t.TempDir()
	admin := &harness{t: t, srv: srv, ts: ts, dir: dir, env: map[string]string{}, cfgFile: filepath.Join(dir, "admin.yaml")}
	admin.env["JAILOPS_CONFIG"] = admin.cfgFile
	admin.writeConfig(fmt.Sprintf("current: adm\nremotes:\n  adm:\n    url: %s\n    fingerprint: %s\n    token: admin-token\n", ts.URL, fp))
	if _, errs, code := admin.run("", "project", "create", "web"); code != 0 {
		t.Fatal(errs)
	}
	n := 0
	enroll := func() string {
		n++
		out, errs, code := admin.run("", "cert", "enroll", fmt.Sprintf("laptop%d", n), "--role", "operator", "--projects", "web")
		tok := strings.TrimSpace(out)
		if code != 0 || !strings.HasPrefix(tok, sdk.TrustTokenPrefix) {
			t.Fatalf("cert enroll: %d %q %s", code, out, errs)
		}
		return tok
	}
	client := &harness{t: t, srv: srv, ts: ts, dir: filepath.Join(dir, "client"), env: map[string]string{}, cfgFile: filepath.Join(dir, "client", "config.yaml")}
	_ = os.MkdirAll(filepath.Join(client.dir, "certs"), 0o700)
	client.env["JAILOPS_CONFIG"] = client.cfgFile
	host := strings.TrimPrefix(ts.URL, "https://")

	victim := writeFile(t, dir, "victim", "keep me", 0o600)
	if err := os.Symlink(victim, filepath.Join(client.dir, "certs", "laptop.key")); err != nil {
		t.Fatal(err)
	}
	_, errs, code := client.run("", "remote", "add", "laptop", host, "--token", enroll())
	if code == 0 || !strings.Contains(errs, "exists already") {
		t.Fatalf("a symbolic link in place of the key: %d %q", code, errs)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep me" {
		t.Fatalf("the file behind the link was written: %q", b)
	}
	_ = os.Remove(filepath.Join(client.dir, "certs", "laptop.key"))

	if _, errs, code := client.run("", "remote", "add", "laptop", host, "--token", enroll()); code != 0 {
		t.Fatalf("add: %d %s", code, errs)
	}
	for _, f := range []string{"laptop.key", "laptop.crt"} {
		if _, err := os.Stat(filepath.Join(client.dir, "certs", f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, errs, code := client.run("", "remote", "remove", "laptop"); code != 0 {
		t.Fatalf("remove: %d %s", code, errs)
	}
	for _, f := range []string{"laptop.key", "laptop.crt"} {
		if _, err := os.Stat(filepath.Join(client.dir, "certs", f)); err == nil {
			t.Fatalf("%s stayed after the remote was removed", f)
		}
	}
	// the same name can be added again
	if _, errs, code := client.run("", "remote", "add", "laptop", host, "--token", enroll()); code != 0 {
		t.Fatalf("add again: %d %s", code, errs)
	}
}

// --dry-run answers before the network is touched: no connection to the host, no question about its certificate.
func TestRemoteAddDryRunDoesNotConnect(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("", "remote", "add", "lab", "https://127.0.0.1:1", "--dry-run")
	if code != 0 || !strings.Contains(out, "dry-run: would add remote lab") || strings.Contains(errs, "Pin this certificate") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}
