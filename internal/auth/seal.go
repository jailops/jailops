package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// SealKeySize is the AES-256 key size for Seal.
const SealKeySize = 32

// Seal encrypts plaintext with AES-256-GCM. aad binds the ciphertext to its
// owner (for example the user id) so rows cannot be swapped. The nonce is
// prepended. Where the key lives (a root-only file, never the database it
// protects) is the daemon's decision.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("auth: entropy: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// Unseal reverses Seal.
func Unseal(key, sealed, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("auth: sealed value too short")
	}
	pt, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], aad)
	if err != nil {
		return nil, errors.New("auth: cannot unseal value")
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != SealKeySize {
		return nil, fmt.Errorf("auth: seal key must be %d bytes", SealKeySize)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// NewSealKey returns a random key for Seal.
func NewSealKey() ([]byte, error) {
	k := make([]byte, SealKeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("auth: entropy: %w", err)
	}
	return k, nil
}
