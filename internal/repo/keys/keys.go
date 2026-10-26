// Package keys manages the repository master key on a host: the key file
// (mode 0600, optionally wrapped with Argon2id), the recovery key that is shown
// once, escrow of the master key to offline recovery recipients, and the key
// status the engine checks before a backup.
//
// The master key never leaves the host in the clear. The backup node holds no
// key. Rotating the master key means a new repository.
package keys

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"jailops.net/jailops/internal/repo/format"
)

// Key is a loaded master key with the sub-keys derived from it.
type Key struct {
	master []byte
	// Keys are the derived sub-keys the engine uses.
	Keys *format.Keys
}

// NewKey wraps a master key.
func NewKey(master []byte) (*Key, error) {
	k, err := format.DeriveKeys(master)
	if err != nil {
		return nil, err
	}
	return &Key{master: append([]byte(nil), master...), Keys: k}, nil
}

// Generate returns a fresh random key.
func Generate() (*Key, error) {
	m, err := format.GenerateMaster()
	if err != nil {
		return nil, err
	}
	return NewKey(m)
}

// DomainID is the non-secret domain id of the key.
func (k *Key) DomainID() string { return k.Keys.DomainID }

// Master returns a copy of the master key bytes (for escrow and key files).
func (k *Key) Master() []byte { return append([]byte(nil), k.master...) }

// RecoveryKey renders the master key as the printable recovery key. It is shown
// once, when the key is created; nothing stores it.
func (k *Key) RecoveryKey() string {
	s, _ := format.EncodeRecoveryKey(k.master)
	return s
}

// FromRecoveryKey parses a recovery key (a new host restoring needs only this).
func FromRecoveryKey(s string) (*Key, error) {
	m, err := format.DecodeRecoveryKey(s)
	if err != nil {
		return nil, err
	}
	return NewKey(m)
}

// ---- key file --------------------------------------------------------------------

// KeyFile is the on-disk representation of the master key.
type KeyFile struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"` // "passphrase" or "none"
	DomainID   string `json:"domain_id"`
	// passphrase protection
	Argon2 *Argon2Params `json:"argon2,omitempty"`
	Nonce  string        `json:"nonce,omitempty"`   // base64
	Wrap   string        `json:"wrapped,omitempty"` // base64, AEAD(master)
	// no protection (file permissions only)
	Master string `json:"master,omitempty"` // base64
}

// Argon2Params are the Argon2id parameters stored in the key file.
type Argon2Params struct {
	Time    uint32 `json:"time"`
	MemKiB  uint32 `json:"memory_kib"`
	Threads uint8  `json:"threads"`
	Salt    string `json:"salt"` // base64
}

// DefaultArgon2 returns the production parameters (64 MiB, 3 passes, 4 lanes).
func DefaultArgon2() Argon2Params { return Argon2Params{Time: 3, MemKiB: 64 * 1024, Threads: 4} }

// Bounds for stored Argon2id parameters: a damaged or hostile key file must
// produce an error, never a panic or a multi-gigabyte allocation.
const (
	maxArgon2Time    = 64
	maxArgon2MemKiB  = 1 << 20 // 1 GiB
	maxArgon2Threads = 64
	maxKeyFileBytes  = 64 << 10
)

func (p Argon2Params) validate() error {
	if p.Time < 1 || p.Time > maxArgon2Time {
		return fmt.Errorf("key file argon2 time %d out of range 1-%d", p.Time, maxArgon2Time)
	}
	if p.Threads < 1 || p.Threads > maxArgon2Threads {
		return fmt.Errorf("key file argon2 threads %d out of range 1-%d", p.Threads, maxArgon2Threads)
	}
	if p.MemKiB < 8*uint32(p.Threads) || p.MemKiB > maxArgon2MemKiB {
		return fmt.Errorf("key file argon2 memory %d KiB out of range %d-%d", p.MemKiB, 8*uint32(p.Threads), maxArgon2MemKiB)
	}
	return nil
}

func keyFileAAD(domain string) []byte { return []byte("JailOps backup keyfile v1|" + domain) }

// WrapOptions control NewKeyFile. Salt and Nonce are random when nil (tests
// inject fixed ones).
type WrapOptions struct {
	Passphrase string
	Argon2     Argon2Params
	Salt       []byte
	Nonce      []byte
}

// NewKeyFile wraps the key. An empty passphrase produces an unprotected file
// that relies on file permissions (needed for unattended schedulers).
func NewKeyFile(k *Key, o WrapOptions) (*KeyFile, error) {
	kf := &KeyFile{Version: 1, DomainID: k.DomainID()}
	if o.Passphrase == "" {
		kf.Protection = "none"
		kf.Master = base64.StdEncoding.EncodeToString(k.master)
		return kf, nil
	}
	p := o.Argon2
	if p == (Argon2Params{}) {
		p = DefaultArgon2()
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	salt, nonce := o.Salt, o.Nonce
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return nil, err
		}
	}
	if nonce == nil {
		nonce = make([]byte, format.NonceSize)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, err
		}
	}
	if len(nonce) != format.NonceSize {
		return nil, errors.New("bad nonce size")
	}
	p.Salt = base64.StdEncoding.EncodeToString(salt)
	kek := argon2.IDKey([]byte(o.Passphrase), salt, p.Time, p.MemKiB, p.Threads, format.KeySize)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, err
	}
	kf.Protection = "passphrase"
	kf.Argon2 = &p
	kf.Nonce = base64.StdEncoding.EncodeToString(nonce)
	kf.Wrap = base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, k.master, keyFileAAD(kf.DomainID)))
	return kf, nil
}

// ParseKeyFile parses a key file.
func ParseKeyFile(b []byte) (*KeyFile, error) {
	if len(b) > maxKeyFileBytes {
		return nil, errors.New("key file is too large")
	}
	kf := &KeyFile{}
	if err := json.Unmarshal(b, kf); err != nil {
		return nil, fmt.Errorf("parsing key file: %w", err)
	}
	if kf.Version != 1 {
		return nil, fmt.Errorf("unsupported key file version %d", kf.Version)
	}
	return kf, nil
}

// ErrPassphrase is returned for a missing or wrong passphrase.
var ErrPassphrase = errors.New("keys: wrong passphrase or damaged key file")

// ErrPassphraseRequired is returned when the key file is wrapped and no passphrase was given.
var ErrPassphraseRequired = errors.New("keys: the key file is passphrase protected: a passphrase is required")

// Unwrap returns the key. It verifies that the key matches the file's domain id.
func (kf *KeyFile) Unwrap(passphrase string) (*Key, error) {
	var master []byte
	switch kf.Protection {
	case "none":
		m, err := base64.StdEncoding.DecodeString(kf.Master)
		if err != nil {
			return nil, err
		}
		master = m
	case "passphrase":
		if kf.Argon2 == nil {
			return nil, errors.New("key file is missing argon2 parameters")
		}
		if err := kf.Argon2.validate(); err != nil {
			return nil, err
		}
		if passphrase == "" {
			return nil, ErrPassphraseRequired
		}
		salt, err := base64.StdEncoding.DecodeString(kf.Argon2.Salt)
		if err != nil {
			return nil, err
		}
		nonce, err := base64.StdEncoding.DecodeString(kf.Nonce)
		if err != nil {
			return nil, err
		}
		if len(nonce) != format.NonceSize { // aead.Open panics on any other length
			return nil, fmt.Errorf("key file nonce is %d bytes, want %d", len(nonce), format.NonceSize)
		}
		ct, err := base64.StdEncoding.DecodeString(kf.Wrap)
		if err != nil {
			return nil, err
		}
		kek := argon2.IDKey([]byte(passphrase), salt, kf.Argon2.Time, kf.Argon2.MemKiB, kf.Argon2.Threads, format.KeySize)
		aead, err := chacha20poly1305.NewX(kek)
		if err != nil {
			return nil, err
		}
		master, err = aead.Open(nil, nonce, ct, keyFileAAD(kf.DomainID))
		if err != nil {
			return nil, ErrPassphrase
		}
	default:
		return nil, fmt.Errorf("unknown key file protection %q", kf.Protection)
	}
	k, err := NewKey(master)
	if err != nil {
		return nil, err
	}
	if k.DomainID() != kf.DomainID {
		return nil, errors.New("key file domain id does not match its key")
	}
	return k, nil
}

// ErrKeyFileExists is returned by Create when the file is already there.
var ErrKeyFileExists = errors.New("keys: the key file already exists")

// ErrNoKeyFile is returned by Load when there is no key file.
var ErrNoKeyFile = errors.New("keys: no key file")

// Create generates a key and writes it to path, which must not exist, with
// mode 0600 (parent directories 0700). It returns the key and the recovery key,
// which the caller shows once and does not store.
func Create(path string, o WrapOptions) (*Key, string, error) {
	k, err := Generate()
	if err != nil {
		return nil, "", err
	}
	if err := WriteKeyFile(path, k, o); err != nil {
		return nil, "", err
	}
	return k, k.RecoveryKey(), nil
}

// WriteKeyFile writes key to path (create-only, mode 0600, fsynced).
func WriteKeyFile(path string, k *Key, o WrapOptions) error {
	kf, err := NewKeyFile(k, o)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrKeyFileExists
		}
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// Load reads the key file at path. An unprotected file must not be readable by
// group or others: the key is the whole secret.
func Load(path, passphrase string) (*Key, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoKeyFile
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("keys: %s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	kf, err := ParseKeyFile(b)
	if err != nil {
		return nil, err
	}
	if kf.Protection == "none" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("keys: %s is readable by other users (mode %04o): the unprotected key file must be mode 0600", path, fi.Mode().Perm())
	}
	return kf.Unwrap(passphrase)
}
