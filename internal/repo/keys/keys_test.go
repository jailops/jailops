package keys

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/store"
)

var fast = Argon2Params{Time: 1, MemKiB: 64, Threads: 1}

func TestCreateLoadUnprotected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "master.key")
	k, rec, err := Create(path, WrapOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", di.Mode())
	}
	if !strings.HasPrefix(rec, "JOR1-") {
		t.Fatal(rec)
	}
	got, err := Load(path, "")
	if err != nil || !bytes.Equal(got.Master(), k.Master()) || got.DomainID() != k.DomainID() {
		t.Fatalf("load: %v", err)
	}
	if _, _, err := Create(path, WrapOptions{}); !errors.Is(err, ErrKeyFileExists) {
		t.Fatalf("second create: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "none"), ""); !errors.Is(err, ErrNoKeyFile) {
		t.Fatal(err)
	}
	// readable by others: refused
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, ""); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("world readable key file accepted: %v", err)
	}
	// the recovery key restores the same key on another host
	r, err := FromRecoveryKey(rec)
	if err != nil || !bytes.Equal(r.Master(), k.Master()) {
		t.Fatal(err)
	}
}

func TestPassphraseWrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	k, _, err := Create(path, WrapOptions{Passphrase: "correct horse", Argon2: fast})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(k.Master()))) {
		t.Fatal("the master key is in the file in the clear")
	}
	if _, err := Load(path, ""); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("no passphrase: %v", err)
	}
	if _, err := Load(path, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	got, err := Load(path, "correct horse")
	if err != nil || !bytes.Equal(got.Master(), k.Master()) {
		t.Fatal(err)
	}
	// a wrapped file is allowed to be group readable: it is encrypted
	_ = os.Chmod(path, 0o640)
	if _, err := Load(path, "correct horse"); err != nil {
		t.Fatal(err)
	}
	// tampering with the domain id or the ciphertext
	var kf KeyFile
	_ = json.Unmarshal(raw, &kf)
	bad := kf
	bad.DomainID = "0000000000000000"
	if _, err := bad.Unwrap("correct horse"); err == nil {
		t.Fatal("domain id swap accepted")
	}
}

func TestHostileKeyFiles(t *testing.T) {
	good, _ := Generate()
	kf, _ := NewKeyFile(good, WrapOptions{Passphrase: "p", Argon2: fast})
	for name, mut := range map[string]func(*KeyFile){
		"time 0":       func(k *KeyFile) { k.Argon2.Time = 0 },
		"time huge":    func(k *KeyFile) { k.Argon2.Time = 1 << 30 },
		"threads 0":    func(k *KeyFile) { k.Argon2.Threads = 0 },
		"memory huge":  func(k *KeyFile) { k.Argon2.MemKiB = 1 << 31 },
		"memory tiny":  func(k *KeyFile) { k.Argon2.MemKiB = 1 },
		"short nonce":  func(k *KeyFile) { k.Nonce = base64.StdEncoding.EncodeToString([]byte("x")) },
		"no argon2":    func(k *KeyFile) { k.Argon2 = nil },
		"bad base64":   func(k *KeyFile) { k.Wrap = "!!!" },
		"unknown prot": func(k *KeyFile) { k.Protection = "rot13" },
	} {
		c := *kf
		a := *kf.Argon2
		c.Argon2 = &a
		mut(&c)
		if _, err := c.Unwrap("p"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseKeyFile([]byte(`{"version":2}`)); err == nil {
		t.Fatal("version 2 accepted")
	}
	if _, err := ParseKeyFile(make([]byte, maxKeyFileBytes+1)); err == nil {
		t.Fatal("huge file accepted")
	}
	if _, err := NewKeyFile(good, WrapOptions{Passphrase: "p", Argon2: Argon2Params{Time: 1 << 20, MemKiB: 64, Threads: 1}}); err == nil {
		t.Fatal("hostile params accepted when writing")
	}
}

func FuzzKeyFile(f *testing.F) {
	k, _ := Generate()
	kf, _ := NewKeyFile(k, WrapOptions{Passphrase: "p", Argon2: fast})
	b, _ := json.Marshal(kf)
	f.Add(b)
	nf, _ := NewKeyFile(k, WrapOptions{})
	nb, _ := json.Marshal(nf)
	f.Add(nb)
	f.Fuzz(func(t *testing.T, b []byte) {
		kf, err := ParseKeyFile(b)
		if err != nil {
			return
		}
		if kf.Argon2 != nil && kf.Argon2.MemKiB > 1<<12 {
			return // do not spend the fuzzer's time in Argon2
		}
		_, _ = kf.Unwrap("p")
	})
}

func TestEscrow(t *testing.T) {
	k, _ := Generate()
	id1, r1, err := GenerateRecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	id2, r2, _ := GenerateRecoveryIdentity()
	idOther, _, _ := GenerateRecoveryIdentity()
	blob, err := WrapEscrow(k, []string{r1, r2})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, k.Master()) {
		t.Fatal("master key in the clear in the escrow blob")
	}
	for _, id := range []string{id1, id2} {
		got, err := OpenEscrow(blob, id)
		if err != nil || !bytes.Equal(got.Master(), k.Master()) {
			t.Fatalf("recipient cannot open its escrow: %v", err)
		}
	}
	if _, err := OpenEscrow(blob, idOther); err == nil {
		t.Fatal("a stranger opened the escrow blob")
	}
	for i := range blob {
		c := append([]byte(nil), blob...)
		c[i] ^= 1
		if got, err := OpenEscrow(c, id1); err == nil && !bytes.Equal(got.Master(), k.Master()) {
			t.Fatalf("a damaged blob opened with a different key (byte %d)", i)
		}
	}
	if _, err := WrapEscrow(k, nil); err == nil {
		t.Fatal("escrow without recipients accepted")
	}
	if _, err := WrapEscrow(k, []string{"age1notakey"}); err == nil {
		t.Fatal("bad recipient accepted")
	}
	if _, err := OpenEscrow(blob, "nonsense"); err == nil {
		t.Fatal("bad identity accepted")
	}
}

func TestStatusAndEscrowInRepository(t *testing.T) {
	ctx := context.Background()
	st, err := store.Init(ctx, datastore.NewMemBackend(), store.InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := Generate()
	if s, _ := StatusOf(ctx, nil, st); s != StatusNone || s.String() != "none" {
		t.Fatal(s)
	}
	if s, _ := StatusOf(ctx, k, nil); s != StatusPresent {
		t.Fatal(s)
	}
	if s, err := StatusOf(ctx, k, st); err != nil || s != StatusPresent || s.String() != "present" {
		t.Fatal(s, err)
	}
	_, rec, _ := GenerateRecoveryIdentity()
	if err := Escrow(ctx, k, []string{rec}, st); err != nil {
		t.Fatal(err)
	}
	if s, _ := StatusOf(ctx, k, st); s != StatusEscrowed || s.String() != "escrowed" {
		t.Fatal(s)
	}
	// create-only: a second escrow cannot replace the first
	if err := Escrow(ctx, k, []string{rec}, st); !errors.Is(err, store.ErrExists) {
		t.Fatalf("escrow replaced: %v", err)
	}
	// another key domain is not escrowed
	k2, _ := Generate()
	if s, _ := StatusOf(ctx, k2, st); s != StatusPresent {
		t.Fatal(s)
	}
}
