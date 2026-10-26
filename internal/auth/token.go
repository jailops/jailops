package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// TokenPrefix starts every API token.
const TokenPrefix = "jot_"

var tokenEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// tokenLen is the length of a well-formed token: prefix plus 32 bytes of base32.
var tokenLen = len(TokenPrefix) + tokenEnc.EncodedLen(32)

// GenerateToken creates a token from 32 random bytes. The secret is returned
// once; only Hash is to be stored.
func GenerateToken() (secret string, hash []byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("auth: entropy: %w", err)
	}
	secret = TokenPrefix + strings.ToLower(tokenEnc.EncodeToString(b[:]))
	return secret, HashToken(secret), nil
}

// TokenWellFormed is a cheap shape check done before hashing, so that
// garbage never reaches the database.
func TokenWellFormed(s string) bool {
	if len(s) != tokenLen || !strings.HasPrefix(s, TokenPrefix) {
		return false
	}
	_, err := tokenEnc.DecodeString(strings.ToUpper(s[len(TokenPrefix):]))
	return err == nil
}

// HashToken returns the SHA-256 of the token string.
func HashToken(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// VerifyToken compares a presented token with a stored hash in constant time.
func VerifyToken(presented string, storedHash []byte) bool {
	if len(storedHash) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(HashToken(presented), storedHash) == 1
}

// CheckTokenUse applies expiry and the allow_from restriction. Entries of
// allowFrom are IP addresses or CIDR prefixes. An empty list allows any source.
func CheckTokenUse(expiresAt time.Time, allowFrom []string, now time.Time, remote netip.Addr) error {
	if !expiresAt.IsZero() && !now.Before(expiresAt) {
		return api.Errorf(api.CodeUnauthorized, "token expired")
	}
	if len(allowFrom) == 0 {
		return nil
	}
	remote = remote.Unmap()
	for _, a := range allowFrom {
		if p, err := netip.ParsePrefix(a); err == nil {
			if p.Contains(remote) {
				return nil
			}
			continue
		}
		if ip, err := netip.ParseAddr(a); err == nil && ip.Unmap() == remote {
			return nil
		}
	}
	return api.Errorf(api.CodeForbidden, "token is not valid from this address")
}

// ValidateAllowFrom checks that every entry parses, for token creation.
func ValidateAllowFrom(allowFrom []string) error {
	for _, a := range allowFrom {
		if _, err := netip.ParsePrefix(a); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(a); err == nil {
			continue
		}
		return api.Errorf(api.CodeInvalid, "allow_from entry %q is neither an address nor a CIDR prefix", a)
	}
	return nil
}
