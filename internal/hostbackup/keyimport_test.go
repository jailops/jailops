package hostbackup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/sdk/api"
)

// keyFileOf returns the content of a key file of the key a rig holds, wrapped when a passphrase is given.
func keyFileOf(t *testing.T, r *rig, pass string) []byte {
	t.Helper()
	kf, err := keys.NewKeyFile(r.svc.currentKey(), keys.WrapOptions{Passphrase: pass, Argon2: keys.Argon2Params{Time: 1, MemKiB: 64, Threads: 1}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(kf)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImportRecoveryKeyOnANewHostThenRestore(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc})
	j := r.jail("web1", "uuid-web1", nil)
	bk := r.backup(j).Targets[0].Backup

	h := newRig(t, rigOpts{node: &nc, noKey: true})
	res, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{RecoveryKey: r.svc.currentKey().RecoveryKey()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || res.Replaced || res.Domain != r.svc.currentKey().DomainID() || res.Existing != "" {
		t.Fatalf("import = %+v", res)
	}
	if !res.Verified || res.Repository != res.Domain {
		t.Fatalf("the repository was not asked or did not agree: %+v", res)
	}
	fi, err := os.Stat(h.keyPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file = %v, %v", fi, err)
	}
	// the key file loads on the next start of the daemon
	if err := h.svc.LoadKey(); err != nil {
		t.Fatal(err)
	}
	// the import rebuilt the catalog from the repository: the backup is listed without any other step
	if res.Backups != 1 || res.Warning != "" {
		t.Fatalf("the catalog was not rebuilt: %+v", res)
	}
	if l, err := h.svc.List(bg); err != nil || len(l) != 1 || l[0].Metadata.Name != bk.Metadata.Name {
		t.Fatalf("list after the import = %v, %v", l, err)
	}
	if _, err := h.svc.Restore(bg, api.RestoreRequest{Backup: bk.Metadata.Name}); err != nil {
		t.Fatal(err)
	}
}

func TestImportRefusesAWrongKeyBeforeAnythingIsWritten(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc})
	r.backup(r.jail("web1", "uuid-web1", nil)) // the node now records the key domain

	wrong, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	h := newRig(t, rigOpts{node: &nc, noKey: true})
	_, err = h.svc.ImportKeyChecked(bg, ImportKeyOptions{RecoveryKey: wrong.RecoveryKey()})
	if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "key domain") {
		t.Fatalf("wrong key = %v", err)
	}
	if _, err := os.Stat(h.keyPath); !os.IsNotExist(err) {
		t.Fatalf("a refused import wrote a key file: %v", err)
	}
	if h.svc.currentKey() != nil {
		t.Fatal("a refused import set a key")
	}
}

func TestImportFromAKeyFile(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc})
	r.backup(r.jail("web1", "uuid-web1", nil))

	plain := keyFileOf(t, r, "")
	h := newRig(t, rigOpts{node: &nc, noKey: true})
	if _, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{KeyFile: plain}); err != nil {
		t.Fatal(err)
	}

	wrapped := keyFileOf(t, r, "correct horse")
	h2 := newRig(t, rigOpts{node: &nc, noKey: true})
	if _, err := h2.svc.ImportKeyChecked(bg, ImportKeyOptions{KeyFile: wrapped}); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("no passphrase = %v", err)
	}
	if _, err := h2.svc.ImportKeyChecked(bg, ImportKeyOptions{KeyFile: wrapped, Passphrase: "wrong"}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("wrong passphrase = %v", err)
	}
	if _, err := os.Stat(h2.keyPath); !os.IsNotExist(err) {
		t.Fatalf("a refused import wrote a key file: %v", err)
	}
	if _, err := h2.svc.ImportKeyChecked(bg, ImportKeyOptions{KeyFile: wrapped, Passphrase: "correct horse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.svc.ImportKeyChecked(bg, ImportKeyOptions{KeyFile: []byte("not json")}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("garbage = %v", err)
	}
}

func TestImportFromTheEscrowBlob(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc})
	r.backup(r.jail("web1", "uuid-web1", nil))

	h := newRig(t, rigOpts{node: &nc, noKey: true})
	res, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{Identity: r.identity})
	if err != nil || !res.Installed || !res.Verified {
		t.Fatalf("escrow import = %+v, %v", res, err)
	}
	other, _, _ := keys.GenerateRecoveryIdentity()
	h2 := newRig(t, rigOpts{node: &nc, noKey: true})
	if _, err := h2.svc.ImportKeyChecked(bg, ImportKeyOptions{Identity: other}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("wrong identity = %v", err)
	}
}

func TestImportNeedsExactlyOneSource(t *testing.T) {
	h := newRig(t, rigOpts{noKey: true})
	if _, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("no source = %v", err)
	}
	k, _ := keys.Generate()
	if _, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true, RecoveryKey: k.RecoveryKey(), Identity: "AGE-SECRET-KEY-1X"}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("two sources = %v", err)
	}
	if _, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true, RecoveryKey: "JOR1-nonsense"}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("bad recovery key = %v", err)
	}
}

func TestImportOverAnExistingKey(t *testing.T) {
	h := newRig(t, rigOpts{noEscrow: true})
	have := h.svc.currentKey()
	before, err := os.ReadFile(h.keyPath)
	if err != nil {
		t.Fatal(err)
	}

	// the key it already holds: a success that changes nothing
	res, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true, RecoveryKey: have.RecoveryKey()})
	if err != nil || res.Installed || res.Replaced || res.Existing != have.DomainID() {
		t.Fatalf("same key = %+v, %v", res, err)
	}

	// another key: refused, and nothing is touched
	other, _ := keys.Generate()
	_, err = h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true, RecoveryKey: other.RecoveryKey()})
	if !api.IsCode(err, api.CodeAlreadyExists) || !strings.Contains(err.Error(), have.DomainID()) {
		t.Fatalf("different key = %v", err)
	}
	if after, _ := os.ReadFile(h.keyPath); string(after) != string(before) || h.svc.currentKey() != have {
		t.Fatal("a refused import changed the key")
	}

	// Check reports and writes nothing, also for a different key
	res, err = h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true, Check: true, RecoveryKey: other.RecoveryKey()})
	if err != nil || res.Installed || res.Existing != have.DomainID() || res.Domain != other.DomainID() {
		t.Fatalf("check = %+v, %v", res, err)
	}

	// Replace moves the old key file aside and installs the new one
	res, err = h.svc.ImportKeyChecked(bg, ImportKeyOptions{SkipNodeCheck: true, Replace: true, RecoveryKey: other.RecoveryKey()})
	if err != nil || !res.Installed || !res.Replaced || res.Aside == "" {
		t.Fatalf("replace = %+v, %v", res, err)
	}
	if aside, err := os.ReadFile(res.Aside); err != nil || string(aside) != string(before) {
		t.Fatalf("the old key file was not kept: %v", err)
	}
	if fi, err := os.Stat(res.Aside); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("aside mode = %v, %v", fi, err)
	}
	if h.svc.currentKey().DomainID() != other.DomainID() {
		t.Fatal("the new key is not in use")
	}
	if err := h.svc.LoadKey(); err != nil || h.svc.currentKey().DomainID() != other.DomainID() {
		t.Fatalf("the new key file does not load: %v", err)
	}
}

func TestImportWithoutTheNodeNeedsTheFlag(t *testing.T) {
	h := newRig(t, rigOpts{noKey: true}) // no node configured
	k, _ := keys.Generate()
	if _, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{RecoveryKey: k.RecoveryKey()}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("no node = %v", err)
	}
	if _, err := os.Stat(h.keyPath); !os.IsNotExist(err) {
		t.Fatalf("a refused import wrote a key file: %v", err)
	}
	res, err := h.svc.ImportKeyChecked(bg, ImportKeyOptions{RecoveryKey: k.RecoveryKey(), SkipNodeCheck: true})
	if err != nil || !res.Installed || res.Verified {
		t.Fatalf("skip = %+v, %v", res, err)
	}
}
