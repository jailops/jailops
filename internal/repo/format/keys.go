package format

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	// KeySize is the size of every key in bytes.
	KeySize = 32
	// NonceSize is the XChaCha20-Poly1305 nonce size.
	NonceSize = chacha20poly1305.NonceSizeX
	// IDSize is the size of a chunk id (HMAC-SHA256 output).
	IDSize = sha256.Size
	// DomainSize is the size of the raw (binary) domain id.
	DomainSize = 8

	hkdfSalt = "JailOps/backup/v1"
)

// ID is a keyed chunk identifier.
type ID [IDSize]byte

// ZeroID marks an all-zero range that has no blob.
var ZeroID ID

// String returns the lowercase hex form.
func (i ID) String() string { return hex.EncodeToString(i[:]) }

// IsZero reports whether the id is the all-zero-range marker.
func (i ID) IsZero() bool { return i == ZeroID }

// ParseID parses a 64-character lowercase hex id.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != 2*IDSize || s != strings.ToLower(s) {
		return id, fmt.Errorf("invalid chunk id %q", s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, fmt.Errorf("invalid chunk id %q", s)
	}
	copy(id[:], b)
	return id, nil
}

// Keys holds the sub-keys of one repository key domain.
type Keys struct {
	Enc      [KeySize]byte // chunk blob encryption
	ID       [KeySize]byte // keyed chunk identity (HMAC)
	Manifest [KeySize]byte // manifest encryption
	// DomainID is the non-secret identifier of the key domain: 16 lowercase
	// hex characters. A repository records it so that a host holding a
	// different master key is rejected early.
	DomainID string
	domain   [DomainSize]byte
}

// GenerateMaster returns a fresh random master key.
func GenerateMaster() ([]byte, error) {
	m := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, m); err != nil {
		return nil, err
	}
	return m, nil
}

func derive(master []byte, info string) ([KeySize]byte, error) {
	var out [KeySize]byte
	r := hkdf.New(sha256.New, master, []byte(hkdfSalt), []byte(info))
	if _, err := io.ReadFull(r, out[:]); err != nil {
		return out, err
	}
	return out, nil
}

// DeriveKeys derives all sub-keys from the 32-byte master key.
func DeriveKeys(master []byte) (*Keys, error) {
	if len(master) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(master))
	}
	k := &Keys{}
	var err error
	if k.Enc, err = derive(master, "chunk-encryption"); err != nil {
		return nil, err
	}
	if k.ID, err = derive(master, "chunk-id"); err != nil {
		return nil, err
	}
	if k.Manifest, err = derive(master, "manifest-encryption"); err != nil {
		return nil, err
	}
	d, err := derive(master, "domain-id")
	if err != nil {
		return nil, err
	}
	copy(k.domain[:], d[:DomainSize])
	k.DomainID = hex.EncodeToString(k.domain[:])
	return k, nil
}

// ChunkID returns the keyed id of a plaintext chunk: HMAC-SHA256(k_id, plaintext).
func (k *Keys) ChunkID(plaintext []byte) ID {
	m := hmac.New(sha256.New, k.ID[:])
	m.Write(plaintext)
	var id ID
	m.Sum(id[:0])
	return id
}

// GroupID returns the opaque, key-dependent identifier of a backup group.
// host and jail are identities (uuids), not display names, so renaming a jail
// keeps its group. The repository tree carries no names.
func (k *Keys) GroupID(host, jail string) string {
	m := hmac.New(sha256.New, k.ID[:])
	m.Write([]byte("group\x00" + host + "\x00" + jail))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// ValidDomainID reports whether s is a well-formed domain id.
func ValidDomainID(s string) bool {
	if len(s) != 2*DomainSize {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ---- recovery key ---------------------------------------------------------

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

const recoveryPrefix = "JOR1"

func recoveryChecksum(master []byte) []byte {
	h := sha256.Sum256(append([]byte("JailOps backup recovery key v1"), master...))
	return h[:4]
}

// EncodeRecoveryKey renders the master key as a printable string with a
// checksum: "JOR1-XXXXX-XXXXX-...". Groups are 5 characters.
func EncodeRecoveryKey(master []byte) (string, error) {
	if len(master) != KeySize {
		return "", errors.New("master key has wrong size")
	}
	raw := append(append([]byte{}, master...), recoveryChecksum(master)...)
	s := b32.EncodeToString(raw)
	parts := []string{recoveryPrefix}
	for len(s) > 0 {
		n := min(5, len(s))
		parts = append(parts, s[:n])
		s = s[n:]
	}
	return strings.Join(parts, "-"), nil
}

// DecodeRecoveryKey parses and checksum-verifies a recovery key string.
func DecodeRecoveryKey(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\n", "", "\t", "", "\r", "").Replace(s))
	if !strings.HasPrefix(s, recoveryPrefix) {
		return nil, errors.New("recovery key must start with JOR1")
	}
	raw, err := b32.DecodeString(strings.TrimPrefix(s, recoveryPrefix))
	if err != nil {
		return nil, fmt.Errorf("recovery key is not valid: %w", err)
	}
	if len(raw) != KeySize+4 {
		return nil, errors.New("recovery key has the wrong length")
	}
	master, sum := raw[:KeySize], raw[KeySize:]
	if !hmac.Equal(sum, recoveryChecksum(master)) {
		return nil, errors.New("recovery key checksum mismatch (typo?)")
	}
	return append([]byte{}, master...), nil
}
