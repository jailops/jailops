package keys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"

	"jailops.net/jailops/internal/repo/format"
)

// Escrow wraps the master key to one or more offline recovery recipients with
// age (X25519). The private halves live offline (a safe, a hardware token, a
// sealed envelope); the blob is not secret and is stored in the repository under
// escrow/<domain>.age, create-only, so a compromised host cannot replace it.
// Disaster recovery then needs only a recovery identity and read access to the
// repository.
//
// The age plaintext is "JOEK" | version 1 | domain id (16 ascii) | master key (32).

const (
	escrowMagic   = "JOEK"
	escrowVersion = 1
	escrowLen     = 4 + 1 + 2*format.DomainSize + format.KeySize
)

// GenerateRecoveryIdentity returns a new age identity (secret, keep offline) and
// the matching recipient (public).
func GenerateRecoveryIdentity() (identity, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// WrapEscrow encrypts the key to the recipients (age1... strings). At least one
// recipient is required.
func WrapEscrow(k *Key, recipients []string) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("escrow needs at least one recovery recipient")
	}
	var rs []age.Recipient
	for _, s := range recipients {
		r, err := age.ParseX25519Recipient(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("escrow recipient %q: %w", s, err)
		}
		rs = append(rs, r)
	}
	plain := append([]byte(escrowMagic), escrowVersion)
	plain = append(plain, k.DomainID()...)
	plain = append(plain, k.master...)
	var out bytes.Buffer
	w, err := age.Encrypt(&out, rs...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// OpenEscrow decrypts an escrow blob with an age identity (AGE-SECRET-KEY-1...)
// and checks that the key it holds belongs to the domain it names.
func OpenEscrow(blob []byte, identity string) (*Key, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(identity))
	if err != nil {
		return nil, fmt.Errorf("escrow identity: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(blob), id)
	if err != nil {
		return nil, fmt.Errorf("this recovery identity does not open the escrow blob: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, escrowLen+1))
	if err != nil {
		return nil, err
	}
	if len(plain) != escrowLen || string(plain[:4]) != escrowMagic || plain[4] != escrowVersion {
		return nil, errors.New("escrow blob has an unknown layout")
	}
	domain := string(plain[5 : 5+2*format.DomainSize])
	k, err := NewKey(plain[5+2*format.DomainSize:])
	if err != nil {
		return nil, err
	}
	if k.DomainID() != domain {
		return nil, errors.New("the unwrapped key does not match the key domain the blob names")
	}
	return k, nil
}

// ---- key status ----------------------------------------------------------------------

// Status is what a host knows about the key of a repository.
type Status int

const (
	// StatusNone: there is no key; nothing can be backed up or restored.
	StatusNone Status = iota
	// StatusPresent: the host holds the key, but nothing recoverable exists
	// outside it: losing the host loses every backup unless the recovery key
	// was kept.
	StatusPresent
	// StatusEscrowed: the host holds the key and the repository holds an
	// escrow blob for its domain.
	StatusEscrowed
)

func (s Status) String() string {
	switch s {
	case StatusPresent:
		return "present"
	case StatusEscrowed:
		return "escrowed"
	}
	return "none"
}

// EscrowChecker answers whether the repository holds an escrow blob for a key
// domain. *store.Store implements it; so does the node client.
type EscrowChecker interface {
	HasEscrow(ctx context.Context, domain string) (bool, error)
}

// StatusOf reports the key status. A nil key is StatusNone. A nil checker means
// the repository cannot be asked: the status is StatusPresent.
func StatusOf(ctx context.Context, k *Key, ec EscrowChecker) (Status, error) {
	if k == nil {
		return StatusNone, nil
	}
	if ec == nil {
		return StatusPresent, nil
	}
	ok, err := ec.HasEscrow(ctx, k.DomainID())
	if err != nil {
		return StatusPresent, err
	}
	if ok {
		return StatusEscrowed, nil
	}
	return StatusPresent, nil
}

// EscrowStore is what Escrow needs from the repository.
type EscrowStore interface {
	PutEscrow(ctx context.Context, domain string, blob []byte) error
}

// Escrow wraps the key to the recipients and stores the blob in the repository.
func Escrow(ctx context.Context, k *Key, recipients []string, st EscrowStore) error {
	blob, err := WrapEscrow(k, recipients)
	if err != nil {
		return err
	}
	return st.PutEscrow(ctx, k.DomainID(), blob)
}
