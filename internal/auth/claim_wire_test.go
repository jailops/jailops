package auth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk/api"
)

func hubCert(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hub"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

// The members of a requestor claim are the wire contract between a hub and a host of another release: a host
// refuses a claim whose version it does not know, and a claim with a member it does not know is accepted (the members
// it needs are signed with it), so a hub of a later release that adds a member keeps working against this host.
func TestRequestorClaimWireIsFrozen(t *testing.T) {
	now := time.Now()
	c := auth.RequestorClaim{Version: 1, Name: "alice", Kind: "user", Role: api.PermAdmin, Projects: []string{"web"}, Global: true,
		IssuedAt: now.Unix(), Expires: now.Add(30 * time.Second).Unix(), Audience: "host-uuid", Method: "GET", Path: "/api/v1/jails"}
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	var got []string
	for k := range m {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := "aud,exp,global,iat,kind,method,name,path,projects,role,v"; strings.Join(got, ",") != want {
		t.Fatalf("the members of a claim changed (a hub and a host of different releases must still agree): %s", strings.Join(got, ","))
	}
	cert, leaf := hubCert(t)
	want := auth.RequestorExpect{Audience: "host-uuid", Method: "GET", Path: "/api/v1/jails", Now: now}

	// a claim of a later hub: one more member, the same version: accepted
	m["future_member"] = []string{"x"}
	future, _ := json.Marshal(m)
	if _, err := auth.VerifyRequestor(leaf, signRaw(t, cert, future), want); err != nil {
		t.Fatalf("a claim with a member this host does not know was refused: %v", err)
	}
	// a claim of a version this host does not know: refused, never read as v1
	m["v"] = 2
	v2, _ := json.Marshal(m)
	if _, err := auth.VerifyRequestor(leaf, signRaw(t, cert, v2), want); err == nil {
		t.Fatal("a claim of version 2 was accepted by a host that reads version 1")
	}
}

// signRaw signs a payload the way SignRequestor does (the digest is domain-separated) so that a payload this release
// would not produce can be tested.
func signRaw(t *testing.T, cert tls.Certificate, payload []byte) string {
	t.Helper()
	h := sha256.New()
	h.Write([]byte("jailops-requestor-v1\n"))
	h.Write(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, cert.PrivateKey.(*ecdsa.PrivateKey), h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sig)
}
