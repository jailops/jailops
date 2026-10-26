package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"net/url"
	"strings"
	"time"
)

// TOTPConfig selects the RFC 6238 parameters. The zero value means SHA-1,
// 6 digits, 30 seconds: what authenticator apps expect.
type TOTPConfig struct {
	Digits int // 6 to 9
	Period time.Duration
	Hash   func() hash.Hash
}

func (c TOTPConfig) norm() TOTPConfig {
	if c.Digits == 0 {
		c.Digits = 6
	}
	if c.Period == 0 {
		c.Period = 30 * time.Second
	}
	if c.Hash == nil {
		c.Hash = sha1.New
	}
	return c
}

// Hash constructors for TOTPConfig.
var (
	HashSHA1   = sha1.New
	HashSHA256 = sha256.New
	HashSHA512 = sha512.New
)

// hotp is RFC 4226 dynamic truncation.
func hotp(secret []byte, counter uint64, digits int, h func() hash.Hash) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(h, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint64(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, uint64(bin)%mod)
}

// TOTPCode computes the code valid at t.
func TOTPCode(secret []byte, t time.Time, cfg TOTPConfig) string {
	cfg = cfg.norm()
	return hotp(secret, uint64(t.Unix())/uint64(cfg.Period/time.Second), cfg.Digits, cfg.Hash)
}

// TOTPValidate checks code against the steps within skew steps of now. It
// returns the matched time-step counter so the caller can reject replays by
// remembering the highest counter accepted (pass it as lastUsed; counters
// less than or equal to it are refused).
func TOTPValidate(secret []byte, code string, now time.Time, skew int, lastUsed int64, cfg TOTPConfig) (counter int64, ok bool) {
	cfg = cfg.norm()
	if len(code) != cfg.Digits {
		return 0, false
	}
	cur := now.Unix() / int64(cfg.Period/time.Second)
	for d := -skew; d <= skew; d++ {
		c := cur + int64(d)
		if c < 0 || c <= lastUsed {
			continue
		}
		want := hotp(secret, uint64(c), cfg.Digits, cfg.Hash)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			ok = true
			counter = c // keep scanning so timing does not depend on the match position
		}
	}
	return counter, ok
}

// GenerateTOTPSecret returns 20 random bytes (160 bits).
func GenerateTOTPSecret() ([]byte, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("auth: entropy: %w", err)
	}
	return b, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeTOTPSecret renders the secret for manual entry.
func EncodeTOTPSecret(secret []byte) string { return b32.EncodeToString(secret) }

// TOTPProvisioningURI returns the otpauth:// URI shown as a QR code.
func TOTPProvisioningURI(issuer, account string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", issuer)
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + q.Encode()
}

// Recovery codes.

// GenerateRecoveryCodes returns n codes (formatted xxxxx-xxxxx, 50 bits each)
// and their storage hashes. Show the codes once; store only the hashes.
func GenerateRecoveryCodes(n int) (codes, hashes []string, err error) {
	for range n {
		var b [7]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, nil, fmt.Errorf("auth: entropy: %w", err)
		}
		s := strings.ToLower(b32.EncodeToString(b[:])) // 56 bits -> 12 chars
		code := s[:5] + "-" + s[5:10]
		codes = append(codes, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return codes, hashes, nil
}

// HashRecoveryCode normalises (case, dashes, spaces) and hashes a code.
func HashRecoveryCode(code string) string {
	n := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))
	h := sha256.Sum256([]byte("jailops-recovery:" + n))
	return hex.EncodeToString(h[:])
}
