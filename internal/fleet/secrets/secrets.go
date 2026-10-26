// Package secrets reads and writes the fleet's age-encrypted secrets
// (secrets/<name>.age).
//
// The repository holds ciphertext only. Plaintext is produced in memory,
// registered with a Redactor so that logs and terminal output can never carry
// it, and is never written to disk. Most secrets are never decrypted by sync
// at all: a jail environment value "secret:<name>" is sent to the API as the
// sealed ciphertext (see Sealed), so the host that holds the key opens it.
package secrets

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"filippo.io/age"
	"filippo.io/age/armor"

	"jailops.net/jailops/internal/fleet/vault"
)

// Environment variables that locate the decryption identity.
const (
	// EnvIdentityFile names a file with one or more age identities.
	EnvIdentityFile = "JAILOPS_SYNC_AGE_IDENTITY"
	// EnvKey holds identity text itself (AGE-SECRET-KEY-1...). Use it where a
	// file is awkward, for example in CI; the process environment is the
	// only place it lives.
	EnvKey = "JAILOPS_SYNC_AGE_KEY"
)

// SealedPrefix starts a sealed value sent to the API.
const SealedPrefix = "sealed:age:"

// ExtSealedSecrets is the api_extensions name a server advertises when it
// accepts sealed values. Sealed values are sent only to servers that do.
const ExtSealedSecrets = "sealed_secrets"

const maxSecretSize = 1 << 20

// ErrNoIdentity is returned when no decryption identity is configured.
var ErrNoIdentity = errors.New("no age identity configured: set --identity, " + EnvIdentityFile + " or " + EnvKey)

// Source says where the identity comes from.
type Source struct {
	// File is an identity file path (the --identity flag).
	File string
	// Getenv reads the environment; nil means os.Getenv.
	Getenv func(string) string
}

func (s Source) getenv(k string) string {
	if s.Getenv != nil {
		return s.Getenv(k)
	}
	return os.Getenv(k)
}

// Describe names the configured source without revealing it.
func (s Source) Describe() string {
	switch {
	case s.File != "":
		return "--identity " + s.File
	case s.getenv(EnvIdentityFile) != "":
		return "$" + EnvIdentityFile + " " + s.getenv(EnvIdentityFile)
	case s.getenv(EnvKey) != "":
		return "$" + EnvKey
	}
	return ""
}

// Load parses the configured identities.
func (s Source) Load() ([]age.Identity, error) {
	var r io.Reader
	switch {
	case s.File != "" || s.getenv(EnvIdentityFile) != "":
		p := s.File
		if p == "" {
			p = s.getenv(EnvIdentityFile)
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("identity file: %w", err)
		}
		if st.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("identity file %s is accessible by group or others (mode %04o); chmod 600 it", p, st.Mode().Perm())
		}
		if st.Size() > maxSecretSize {
			return nil, fmt.Errorf("identity file %s is too large", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("identity file: %w", err)
		}
		r = bytes.NewReader(b)
	case s.getenv(EnvKey) != "":
		r = strings.NewReader(s.getenv(EnvKey))
	default:
		return nil, ErrNoIdentity
	}
	ids, err := age.ParseIdentities(r)
	if err != nil {
		return nil, fmt.Errorf("parse identity: %w", err)
	}
	return ids, nil
}

// Store is the secrets directory of a fleet.
type Store struct {
	dir string
	src Source
	red *Redactor

	mu  sync.Mutex
	ids []age.Identity

	// sops serves values of shared/secrets.sops.yaml (see SopsSealed).
	sopsPath   string
	sopsEnv    func(string) string
	sopsOnce   sync.Once
	sopsValues map[string]any
	sopsErr    error
}

// NewStore opens the secrets directory. The identity is loaded on first use.
func NewStore(dir string, src Source, red *Redactor) *Store {
	if red == nil {
		red = NewRedactor()
	}
	return &Store{dir: dir, src: src, red: red}
}

// Redactor returns the redactor that learns every decrypted value.
func (s *Store) Redactor() *Redactor { return s.red }

func (s *Store) path(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("invalid secret name %q", name)
	}
	return filepath.Join(s.dir, name+".age"), nil
}

// Names lists the secrets present, sorted.
func (s *Store) Names() ([]string, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".age"); ok && !e.IsDir() {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) identities() ([]age.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		ids, err := s.src.Load()
		if err != nil {
			return nil, err
		}
		s.ids = ids
	}
	return s.ids, nil
}

func (s *Store) raw(name string) ([]byte, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("secret %q: no %s", name, p)
		}
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxSecretSize {
		return nil, fmt.Errorf("secret %q: not a regular file of reasonable size", name)
	}
	return os.ReadFile(p)
}

// Decrypt returns the plaintext of a secret. The value is registered with the
// redactor before it is returned. Callers must not log or persist it.
func (s *Store) Decrypt(name string) ([]byte, error) {
	data, err := s.raw(name)
	if err != nil {
		return nil, err
	}
	ids, err := s.identities()
	if err != nil {
		return nil, err
	}
	var r io.Reader = bytes.NewReader(data)
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte(armor.Header)) {
		r = armor.NewReader(r)
	}
	pr, err := age.Decrypt(r, ids...)
	if err != nil {
		return nil, fmt.Errorf("secret %q: cannot decrypt with the configured identity: %w", name, err)
	}
	plain, err := io.ReadAll(io.LimitReader(pr, maxSecretSize))
	if err != nil {
		return nil, fmt.Errorf("secret %q: %w", name, err)
	}
	s.red.Add(plain)
	return plain, nil
}

// Token decrypts a secret that holds a single-line token and trims whitespace.
func (s *Store) Token(name string) (string, error) {
	b, err := s.Decrypt(name)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("secret %q is empty", name)
	}
	s.red.Add([]byte(tok))
	return tok, nil
}

// Sealed returns the value to send to the API for a secret: the ciphertext
// as stored, base64 encoded, behind SealedPrefix. It needs no identity and
// reveals nothing the repository does not already hold.
func (s *Store) Sealed(name string) (string, error) {
	data, err := s.raw(name)
	if err != nil {
		return "", err
	}
	t := bytes.TrimSpace(data)
	if !bytes.HasPrefix(t, []byte(armor.Header)) && !bytes.HasPrefix(t, []byte("age-encryption.org/")) {
		return "", fmt.Errorf("secret %q is not an age file", name)
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(data), nil
}

// RecipientsFile is the file with the public keys secrets are encrypted to.
const RecipientsFile = "recipients.txt"

// Recipients reads secrets/recipients.txt (age public keys, one per line).
func (s *Store) Recipients() ([]age.Recipient, error) {
	f, err := os.Open(filepath.Join(s.dir, RecipientsFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no %s in %s: list the age public keys to encrypt to", RecipientsFile, s.dir)
		}
		return nil, err
	}
	defer f.Close()
	rs, err := age.ParseRecipients(io.LimitReader(f, maxSecretSize))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", RecipientsFile, err)
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("%s lists no recipients", RecipientsFile)
	}
	return rs, nil
}

// Set encrypts plaintext to the recipients and writes secrets/<name>.age
// atomically. The plaintext never touches the disk.
func (s *Store) Set(name string, plaintext []byte, recipients []age.Recipient) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return errors.New("no recipients")
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, recipients...)
	if err != nil {
		return err
	}
	if _, err := w.Write(plaintext); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := aw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// GenerateIdentity creates a new X25519 identity. The caller decides where
// the private part goes; sync never stores it.
func GenerateIdentity() (*age.X25519Identity, error) { return age.GenerateX25519Identity() }

// Redactor removes known secret values from text.
type Redactor struct {
	mu   sync.RWMutex
	vals [][]byte
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Mask replaces every redacted value.
const Mask = "[redacted]"

// Add registers a value. Values shorter than 4 bytes are ignored because
// masking them would destroy unrelated text.
func (r *Redactor) Add(v []byte) {
	if r == nil || len(v) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.vals {
		if bytes.Equal(x, v) {
			return
		}
	}
	r.vals = append(r.vals, bytes.Clone(v))
	// Longest first so a value that contains another is masked whole.
	sort.Slice(r.vals, func(i, j int) bool { return len(r.vals[i]) > len(r.vals[j]) })
}

// Bytes returns b with every registered value masked.
func (r *Redactor) Bytes(b []byte) []byte {
	if r == nil {
		return b
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vals {
		b = bytes.ReplaceAll(b, v, []byte(Mask))
	}
	return b
}

// String returns s with every registered value masked.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	return string(r.Bytes([]byte(s)))
}

// Writer wraps w so every Write is masked first. Output is written in whole
// lines by this program, so a value is never split across writes.
func (r *Redactor) Writer(w io.Writer) io.Writer { return redactWriter{r, w} }

type redactWriter struct {
	r *Redactor
	w io.Writer
}

func (rw redactWriter) Write(p []byte) (int, error) {
	if _, err := rw.w.Write(rw.r.Bytes(bytes.Clone(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WithSops lets the store serve the values of the repository's SOPS secrets
// file. getenv reads the SOPS key variables (nil means the process
// environment). It returns the store.
func (s *Store) WithSops(fleetPath string, getenv func(string) string) *Store {
	s.sopsPath, s.sopsEnv = fleetPath, getenv
	return s
}

// SopsStable returns a stand-in for a SOPS secret that changes exactly when the
// stored value changes and reveals nothing: a digest of the leaf as the
// repository holds it (its ciphertext). It needs no key. Sync hashes
// declarations with it, so a plan never decrypts to find out that nothing
// changed, and never compares the random ciphertext of a fresh seal.
func (s *Store) SopsStable(path string) (string, error) {
	if s.sopsPath == "" {
		return "", errors.New("this store has no SOPS secrets file")
	}
	st, err := vault.SecretsStructure(s.sopsPath)
	if err != nil {
		return "", err
	}
	v, err := vault.Lookup(st, path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:8]), nil
}

func (s *Store) sops() (map[string]any, error) {
	s.sopsOnce.Do(func() {
		s.sopsValues, s.sopsErr = vault.LoadSecrets(s.sopsPath, s.sopsEnv)
	})
	return s.sopsValues, s.sopsErr
}

// SopsValue decrypts one value of the SOPS secrets file. The plaintext is
// registered with the redactor before it is returned.
func (s *Store) SopsValue(path string) (string, error) {
	if s.sopsPath == "" {
		return "", errors.New("this store has no SOPS secrets file")
	}
	m, err := s.sops()
	if err != nil {
		return "", err
	}
	v, err := vault.Lookup(m, path)
	if err != nil {
		return "", err
	}
	s.red.Add([]byte(v))
	return v, nil
}

// SopsSealed returns the value to send to the API for a SOPS secret: the
// plaintext, in memory only, encrypted to the age recipients of
// secrets/recipients.txt (the keys the hosts hold) behind SealedPrefix. It is
// different on every call, because age encryption is randomised; use SopsStable
// to compare.
func (s *Store) SopsSealed(path string) (string, error) {
	v, err := s.SopsValue(path)
	if err != nil {
		return "", err
	}
	rs, err := s.Recipients()
	if err != nil {
		return "", fmt.Errorf("secret %q: %w (the sealed value is encrypted to them)", path, err)
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, rs...)
	if err != nil {
		return "", err
	}
	if _, err := w.Write([]byte(v)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := aw.Close(); err != nil {
		return "", err
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
