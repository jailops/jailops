package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// fleetWithPolicy makes a repository directory whose .sops.yaml encrypts to the
// given recipients, and returns the identity of the first one.
func fleetWithPolicy(t *testing.T, n int) (dir string, ids []*age.X25519Identity) {
	t.Helper()
	dir = t.TempDir()
	var keys, refs strings.Builder
	for i := 0; i < n; i++ {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		keys.WriteString("  - &k" + string(rune('a'+i)) + " " + id.Recipient().String() + "\n")
		refs.WriteString("          - *k" + string(rune('a'+i)) + "\n")
	}
	policy := "keys:\n" + keys.String() + `creation_rules:
  - path_regex: hosts/.*/remote\.sops\.yaml$
    encrypted_regex: '^(client_key|client_cert)$'
    key_groups:
      - age:
` + refs.String() + `  - path_regex: \.sops\.ya?ml$
    key_groups:
      - age:
` + refs.String()
	if err := os.WriteFile(filepath.Join(dir, SopsPolicyFilename), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, ids
}

func useKey(t *testing.T, id *age.X25519Identity) {
	t.Helper()
	t.Setenv(EnvKeyFile, "x") // registers the restore; then really unset
	t.Setenv(EnvKeyCmd, "x")
	os.Unsetenv(EnvKeyFile)
	os.Unsetenv(EnvKeyCmd)
	t.Setenv(EnvKey, id.String())
}

func TestEncryptAndDecryptSecrets(t *testing.T) {
	dir, ids := fleetWithPolicy(t, 1)
	useKey(t, ids[0])
	enc, err := EncryptYAML(dir, SecretsRel, []byte("db:\n  password: hunter2-secret\nplain: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "hunter2-secret") || !IsEncrypted(enc) {
		t.Fatalf("not encrypted:\n%s", enc)
	}
	if err := WriteFileAtomic(SecretsPath(dir), enc, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSecrets(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := Lookup(m, "db.password"); err != nil || v != "hunter2-secret" {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := Lookup(m, "db"); err == nil {
		t.Error("a subtree is not a secret")
	}
	if _, err := Lookup(m, "db.nope"); err == nil {
		t.Error("missing path")
	}
	// the structure is readable without a key
	t.Setenv(EnvKey, "")
	st, err := SecretsStructure(dir)
	if err != nil || !PathExists(st, "db.password") || PathExists(st, "db.other") || PathExists(st, "sops") {
		t.Fatalf("%v %v", st, err)
	}
}

func TestDecryptNeedsAKeyAndRefusesPlaintext(t *testing.T) {
	dir, ids := fleetWithPolicy(t, 1)
	useKey(t, ids[0])
	enc, err := EncryptYAML(dir, SecretsRel, []byte("a:\n  b: secretvalue\n"))
	if err != nil {
		t.Fatal(err)
	}
	WriteFileAtomic(SecretsPath(dir), enc, 0o644)
	t.Setenv(EnvKey, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadSecrets(dir, nil); err == nil || !strings.Contains(err.Error(), "SOPS_AGE_KEY") {
		t.Fatalf("no key configured: %v", err)
	}
	other, _ := age.GenerateX25519Identity()
	useKey(t, other)
	if _, err := LoadSecrets(dir, nil); err == nil {
		t.Fatal("decrypted with a key that is not a recipient")
	}
	os.WriteFile(SecretsPath(dir), []byte("a:\n  b: plain\n"), 0o644)
	if _, err := LoadSecrets(dir, nil); err == nil || !strings.Contains(err.Error(), "not SOPS-encrypted") {
		t.Fatalf("a plaintext secrets file must be refused: %v", err)
	}
}

func TestBackendResolution(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if b := Resolve(get(nil)); b.Name != "" || b.Describe() != "none configured" {
		t.Fatal(b)
	}
	if err := EnsureUnlocked(get(nil)); err == nil || !strings.Contains(err.Error(), EnvKeyFile) {
		t.Fatal(err)
	}
	if b := Resolve(get(map[string]string{EnvKey: "k", EnvKeyFile: "f", EnvKeyCmd: "c"})); b.Name != EnvKey {
		t.Fatalf("SOPS tries the key text first: %v", b)
	}
	f := filepath.Join(t.TempDir(), "id")
	os.WriteFile(f, []byte("AGE-PLUGIN-YUBIKEY-1ABC\n"), 0o600)
	b := Resolve(get(map[string]string{EnvKeyFile: f, EnvKeyCmd: "c"}))
	if b.Name != EnvKeyFile || !b.Plugin || !strings.Contains(b.Describe(), "age-plugin") {
		t.Fatal(b)
	}
	if err := EnsureUnlocked(get(map[string]string{EnvKeyFile: filepath.Join(t.TempDir(), "missing")})); err == nil {
		t.Fatal("a missing identity file")
	}
	if err := EnsureUnlocked(get(map[string]string{EnvKeyCmd: "true"})); err == nil {
		t.Fatal("a command that prints nothing")
	}
	if err := EnsureUnlocked(get(map[string]string{EnvKeyCmd: "echo AGE-SECRET-KEY-1X"})); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteRoundTrip(t *testing.T) {
	dir, ids := fleetWithPolicy(t, 1)
	useKey(t, ids[0])
	certPEM, keyPEM, fp, err := NewClientIdentity("sync@web1", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := Remote{URL: "https://web1.example.com:8443", ServerFingerprint: strings.Repeat("ab", 32), ClientCert: string(certPEM), ClientKey: string(keyPEM)}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ClientFingerprint(); err != nil || got != fp {
		t.Fatalf("%q %q %v", got, fp, err)
	}
	plain := "url: " + r.URL + "\nserver_fingerprint: " + r.ServerFingerprint + "\nclient_cert: |\n" + indent(r.ClientCert) + "client_key: |\n" + indent(r.ClientKey)
	enc, err := EncryptYAML(dir, RemoteRel("web1"), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "BEGIN PRIVATE KEY") || strings.Contains(string(enc), "BEGIN CERTIFICATE") {
		t.Fatal("key or certificate in clear")
	}
	if !strings.Contains(string(enc), "url: "+r.URL) || !strings.Contains(string(enc), r.ServerFingerprint) {
		t.Fatalf("url and fingerprint must stay readable:\n%s", enc)
	}
	WriteFileAtomic(RemotePath(dir, "web1"), enc, 0o600)
	got, err := LoadRemote(dir, "web1", nil)
	if err != nil || got.URL != r.URL || got.ClientKey != r.ClientKey {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := got.TLSCertificate(); err != nil {
		t.Fatal(err)
	}
	if r2, err := LoadRemote(dir, "nohost", nil); err != nil || r2 != nil {
		t.Fatalf("a host without a file: %v %v", r2, err)
	}
	if hs, _ := ListRemotes(dir); len(hs) != 0 {
		// hosts/web1 exists only as a directory with the file: the listing walks hosts/
		t.Logf("remotes: %v", hs)
	}
	bad := r
	bad.ServerFingerprint = ""
	if bad.Check() == nil {
		t.Error("no pin must be refused")
	}
	bad = r
	bad.URL = "http://web1"
	if bad.Check() == nil {
		t.Error("http must be refused")
	}
	bad = r
	bad.ClientKey = string(keyPEM[:40])
	if bad.Check() == nil {
		t.Error("a broken key")
	}
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

func TestPolicyAddAndRemoveRecipients(t *testing.T) {
	dir, ids := fleetWithPolicy(t, 2)
	useKey(t, ids[0])
	enc, err := EncryptYAML(dir, SecretsRel, []byte("a:\n  b: value-one\n"))
	if err != nil {
		t.Fatal(err)
	}
	WriteFileAtomic(SecretsPath(dir), enc, 0o644)

	p, err := LoadSopsPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs := p.ListRecipients(); len(rs) != 2 || len(rs[0].Rules) != 2 {
		t.Fatalf("%+v", rs)
	}
	third, _ := age.GenerateX25519Identity()
	if err := p.AddRecipient("ci_runner", third.Recipient().String(), "ci", AddRecipientOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p.AddRecipient("ci_runner", third.Recipient().String(), "", AddRecipientOptions{}); err == nil {
		t.Error("a duplicate")
	}
	if err := p.AddRecipient("bad", "ssh-ed25519 AAAA", "", AddRecipientOptions{}); err == nil {
		t.Error("not an age key")
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	files, err := AffectedFiles(dir, []string{`\.sops\.ya?ml$`})
	if err != nil || len(files) != 1 || files[0] != SecretsRel {
		t.Fatalf("%v %v", files, err)
	}
	done, failed, err := UpdateKeys(dir, files)
	if err != nil || len(done) != 1 || failed != "" {
		t.Fatalf("%v %q %v", done, failed, err)
	}
	// the new recipient can open it now
	useKey(t, third)
	if m, err := LoadSecrets(dir, nil); err != nil {
		t.Fatalf("new recipient: %v", err)
	} else if v, _ := Lookup(m, "a.b"); v != "value-one" {
		t.Fatal(v)
	}

	// remove it again: real revocation needs the re-wrap
	useKey(t, ids[0])
	p, _ = LoadSopsPolicy(dir)
	if _, err := p.RemoveRecipient("ci_runner"); err != nil {
		t.Fatal(err)
	}
	p.Save()
	if _, _, err := UpdateKeys(dir, files); err != nil {
		t.Fatal(err)
	}
	useKey(t, third)
	if _, err := LoadSecrets(dir, nil); err == nil {
		t.Fatal("a removed recipient can still decrypt")
	}
	useKey(t, ids[1])
	if _, err := LoadSecrets(dir, nil); err != nil {
		t.Fatalf("the other recipient lost access: %v", err)
	}
}

func TestRemoveRefusesToLeaveARuleWithoutRecipients(t *testing.T) {
	dir, ids := fleetWithPolicy(t, 1)
	_ = ids
	p, err := LoadSopsPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := p.ListRecipients()
	if _, err := p.RemoveRecipient("ka"); err == nil || !strings.Contains(err.Error(), "zero recipients") {
		t.Fatalf("%v", err)
	}
	if got := p.ListRecipients(); len(got) != len(before) || len(got[0].Rules) != len(before[0].Rules) {
		t.Fatal("a refused removal changed the policy")
	}
	if _, err := p.RemoveRecipient("nobody"); err == nil {
		t.Fatal("unknown recipient")
	}
}
