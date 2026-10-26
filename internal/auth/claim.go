package auth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// RequestorHeader carries a signed requestor claim from a hub to a host.
const RequestorHeader = "X-JailOps-Requestor"

// MaxClaimLifetime bounds exp - iat of a claim.
const MaxClaimLifetime = 60 * time.Second

const claimSkew = 5 * time.Second

const claimDomain = "jailops-requestor-v1\n"

// RequestorClaim is what a hub vouches for: who calls, with which role and
// scope, for one request. It is signed with the hub certificate key.
type RequestorClaim struct {
	Version  int            `json:"v"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Role     api.Permission `json:"role"`
	Projects []string       `json:"projects,omitempty"`
	Global   bool           `json:"global,omitempty"`
	IssuedAt int64          `json:"iat"`
	Expires  int64          `json:"exp"`
	Audience string         `json:"aud,omitempty"`
	Method   string         `json:"method"`
	Path     string         `json:"path"`
}

var errClaim = api.Errorf(api.CodeUnauthorized, "invalid requestor claim")

func claimDigest(payload []byte) []byte {
	h := sha256.New()
	h.Write([]byte(claimDomain))
	h.Write(payload)
	return h.Sum(nil)
}

// SignRequestor signs the claim with the certificate's ECDSA P-256 key and
// returns the header value.
func SignRequestor(cert tls.Certificate, c RequestorClaim) (string, error) {
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		return "", errors.New("auth: requestor claims need an ECDSA P-256 certificate key")
	}
	if c.Version == 0 {
		c.Version = 1
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sig, err := ecdsa.SignASN1(rand.Reader, key, claimDigest(payload))
	if err != nil {
		return "", err
	}
	return urlEnc.EncodeToString(payload) + "." + urlEnc.EncodeToString(sig), nil
}

// RequestorExpect is what the receiving host knows about the request.
type RequestorExpect struct {
	Audience string
	Method   string
	Path     string
	Now      time.Time
}

// VerifyRequestor checks the header against the TLS peer certificate (which
// the caller has already matched to a pinned hub entry) and the request.
func VerifyRequestor(peer *x509.Certificate, header string, want RequestorExpect) (RequestorClaim, error) {
	var c RequestorClaim
	if peer == nil {
		return c, errClaim
	}
	pub, ok := peer.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve.Params().Name != "P-256" {
		return c, errClaim
	}
	p, sig, ok := strings.Cut(header, ".")
	if !ok {
		return c, errClaim
	}
	payload, err := urlEnc.DecodeString(p)
	if err != nil {
		return c, errClaim
	}
	rawSig, err := urlEnc.DecodeString(sig)
	if err != nil || !ecdsa.VerifyASN1(pub, claimDigest(payload), rawSig) {
		return c, errClaim
	}
	if json.Unmarshal(payload, &c) != nil || c.Version != 1 {
		return RequestorClaim{}, errClaim
	}
	now := want.Now
	if now.IsZero() {
		now = time.Now()
	}
	iat, exp := time.Unix(c.IssuedAt, 0), time.Unix(c.Expires, 0)
	switch {
	case iat.After(now.Add(claimSkew)),
		!exp.After(now),
		exp.Sub(iat) > MaxClaimLifetime,
		c.Audience == "" || c.Audience != want.Audience,
		c.Method != want.Method || c.Path != want.Path:
		return RequestorClaim{}, errClaim
	}
	return c, nil
}

// Clamp limits a claim by the rights of the hub certificate entry that
// carried it: the effective role is the lower of the two, and the claimed
// scope must lie inside the certificate's scope. A claim with neither
// projects nor global is forbidden.
func (c RequestorClaim) Clamp(certPerm api.Permission, certScope Scope) (api.Permission, Scope, error) {
	claimed := Scope{Projects: c.Projects, Global: c.Global}
	if !claimed.Has() {
		return "", Scope{}, api.Errorf(api.CodeForbidden, "the requestor claim has no projects and is not global")
	}
	role := c.Role
	switch {
	case role == certPerm:
	case role.Rank() >= 0 && certPerm.Rank() >= 0:
		if certPerm.Rank() < role.Rank() {
			role = certPerm
		}
	case certPerm.Satisfies(role):
	default:
		return "", Scope{}, api.Errorf(api.CodeForbidden, "the hub certificate may not claim the %s role", c.Role)
	}
	if !certScope.Global {
		if c.Global {
			return "", Scope{}, api.Errorf(api.CodeForbidden, "the hub certificate may not claim global access")
		}
		for _, p := range c.Projects {
			if !slices.Contains(certScope.Projects, p) {
				return "", Scope{}, api.Errorf(api.CodeForbidden, "the hub certificate may not act in project %q", p)
			}
		}
	}
	return role, claimed, nil
}
