package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// JoinPrefix starts every join token.
const JoinPrefix = "joj_"

// JoinClaims is the signed content of a join token.
type JoinClaims struct {
	Version int `json:"v"`
	// ID is a random nonce; consuming it makes the token single use.
	ID string `json:"id"`
	// Member is the name the joining member must use.
	Member string `json:"member"`
	// ClusterFingerprint is the SHA-256 fingerprint of the cluster
	// certificate, which the joiner pins before sending anything secret.
	ClusterFingerprint string `json:"fp"`
	// Addresses are cluster member addresses to try.
	Addresses []string  `json:"addrs,omitempty"`
	Expires   time.Time `json:"exp"`
}

// UsedSet records consumed join token ids. Consume must be atomic and report
// false if id was already consumed. Production code persists it; MemoryUsedSet
// serves tests and single-process use.
type UsedSet interface {
	Consume(id string, expires time.Time) (bool, error)
}

// MemoryUsedSet is an in-memory UsedSet that forgets tokens after they expire.
type MemoryUsedSet struct {
	mu  sync.Mutex
	m   map[string]time.Time
	Now func() time.Time
}

// Consume implements UsedSet.
func (s *MemoryUsedSet) Consume(id string, expires time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if s.m == nil {
		s.m = map[string]time.Time{}
	}
	for k, e := range s.m {
		if now().After(e) {
			delete(s.m, k)
		}
	}
	if _, used := s.m[id]; used {
		return false, nil
	}
	s.m[id] = expires
	return true, nil
}

// NewJoinToken creates a token bound to member, valid for ttl, protected by an
// HMAC under key (the cluster secret). The token embeds the cluster
// certificate fingerprint.
func NewJoinToken(key []byte, member, clusterFingerprint string, addrs []string, ttl time.Duration, now time.Time) (string, JoinClaims, error) {
	if len(key) < 16 {
		return "", JoinClaims{}, errors.New("auth: join key too short")
	}
	if member == "" || strings.ContainsAny(member, "/ \t\n") {
		return "", JoinClaims{}, api.Errorf(api.CodeInvalid, "invalid member name")
	}
	fp, err := NormalizeFingerprint(clusterFingerprint)
	if err != nil {
		return "", JoinClaims{}, api.Errorf(api.CodeInvalid, "%v", err)
	}
	if ttl <= 0 {
		return "", JoinClaims{}, api.Errorf(api.CodeInvalid, "join token lifetime must be positive")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", JoinClaims{}, err
	}
	c := JoinClaims{Version: 1, ID: urlEnc.EncodeToString(nonce[:]), Member: member, ClusterFingerprint: fp,
		Addresses: addrs, Expires: now.Add(ttl).UTC().Truncate(time.Second)}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", JoinClaims{}, err
	}
	p := urlEnc.EncodeToString(payload)
	return JoinPrefix + p + "." + urlEnc.EncodeToString(joinMAC(key, p)), c, nil
}

func joinMAC(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("jailops-join:"))
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// ParseJoinToken checks the HMAC (constant time) and the structure, without
// consuming the token. Use it on the joiner side to read the fingerprint when
// the key is known, and VerifyJoinToken on the accepting side.
func ParseJoinToken(key []byte, token string) (JoinClaims, error) {
	var c JoinClaims
	bad := api.Errorf(api.CodeUnauthorized, "invalid join token")
	rest, ok := strings.CutPrefix(token, JoinPrefix)
	if !ok {
		return c, bad
	}
	p, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return c, bad
	}
	got, err := urlEnc.DecodeString(sig)
	if err != nil || !hmac.Equal(got, joinMAC(key, p)) {
		return c, bad
	}
	raw, err := urlEnc.DecodeString(p)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.ID == "" {
		return JoinClaims{}, bad
	}
	return c, nil
}

// VerifyJoinToken authenticates the token, requires it to be unexpired and
// issued for member, and consumes it. A token verifies exactly once.
func VerifyJoinToken(key []byte, token, member string, now time.Time, used UsedSet) (JoinClaims, error) {
	c, err := ParseJoinToken(key, token)
	if err != nil {
		return c, err
	}
	if !now.Before(c.Expires) {
		return JoinClaims{}, api.Errorf(api.CodeUnauthorized, "join token expired")
	}
	if c.Member != member {
		return JoinClaims{}, api.Errorf(api.CodeForbidden, "join token was issued for a different member name")
	}
	fresh, err := used.Consume(c.ID, c.Expires)
	if err != nil {
		return JoinClaims{}, fmt.Errorf("auth: record join token: %w", err)
	}
	if !fresh {
		return JoinClaims{}, api.Errorf(api.CodeUnauthorized, "join token already used")
	}
	return c, nil
}
