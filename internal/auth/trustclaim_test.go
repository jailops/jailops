package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

var trustKey = []byte("0123456789abcdef0123")

const trustFP = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

func TestTrustTokenLifecycle(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, c, err := NewTrustToken(trustKey, "laptop", strings.ToUpper(trustFP), time.Hour, now)
	if err != nil || !strings.HasPrefix(tok, TrustPrefix) || c.Name != "laptop" || c.ServerFingerprint != trustFP {
		t.Fatalf("%v %+v", err, c)
	}
	got, err := VerifyTrustToken(trustKey, tok, trustFP, now.Add(time.Minute))
	if err != nil || got.ID != c.ID {
		t.Fatalf("%v", err)
	}
	if fp, err := TrustTokenFingerprint(tok); err != nil || fp != trustFP {
		t.Fatalf("%v %s", err, fp)
	}
	// Expired.
	if _, err := VerifyTrustToken(trustKey, tok, trustFP, now.Add(time.Hour)); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("expired: %v", err)
	}
	// Wrong server fingerprint.
	if _, err := VerifyTrustToken(trustKey, tok, strings.Repeat("11", 32), now); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	// Wrong key.
	if _, err := ParseTrustToken([]byte("another-key-0123456"), tok); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestTrustTokenForgery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, c, _ := NewTrustToken(trustKey, "laptop", trustFP, time.Hour, now)
	p, sig, _ := strings.Cut(strings.TrimPrefix(tok, TrustPrefix), ".")
	// A payload with a later expiry and the old signature.
	c.Expires += 100000
	raw, _ := json.Marshal(c)
	forged := TrustPrefix + urlEnc.EncodeToString(raw) + "." + sig
	if _, err := ParseTrustToken(trustKey, forged); err == nil {
		t.Fatal("forged payload accepted")
	}
	for _, bad := range []string{"", "joc_", "joc_x", "joc_" + p, "joc_" + p + ".!!", "jot_" + p + "." + sig, tok + "x"} {
		if _, err := ParseTrustToken(trustKey, bad); !api.IsCode(err, api.CodeUnauthorized) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A join token is not a trust token, and the reverse.
	jt, _, err := NewJoinToken(trustKey, "m1", trustFP, nil, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustToken(trustKey, jt); err == nil {
		t.Fatal("join token accepted as trust token")
	}
	if _, err := ParseTrustToken(trustKey, strings.Replace(jt, JoinPrefix, TrustPrefix, 1)); err == nil {
		t.Fatal("re-prefixed join token accepted as trust token")
	}
	if _, err := ParseJoinToken(trustKey, strings.Replace(tok, TrustPrefix, JoinPrefix, 1)); err == nil {
		t.Fatal("trust token accepted as join token")
	}
}

func TestNewTrustTokenValidation(t *testing.T) {
	now := time.Now()
	for name, f := range map[string]func() error{
		"short key": func() error { _, _, e := NewTrustToken([]byte("x"), "a", trustFP, time.Hour, now); return e },
		"bad name":  func() error { _, _, e := NewTrustToken(trustKey, "A b", trustFP, time.Hour, now); return e },
		"bad fp":    func() error { _, _, e := NewTrustToken(trustKey, "a", "zz", time.Hour, now); return e },
		"zero ttl":  func() error { _, _, e := NewTrustToken(trustKey, "a", trustFP, 0, now); return e },
		"long ttl":  func() error { _, _, e := NewTrustToken(trustKey, "a", trustFP, MaxTrustTTL+time.Second, now); return e },
	} {
		if f() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func claimFor(now time.Time) RequestorClaim {
	return RequestorClaim{Version: 1, Name: "ann", Kind: "user", Role: api.PermOperator, Projects: []string{"web"},
		IssuedAt: now.Unix(), Expires: now.Add(30 * time.Second).Unix(), Audience: "host-1", Method: "GET", Path: "/1.0/jails"}
}

func TestRequestorClaim(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	hub, hubLeaf := mustCert(t, CertOptions{CommonName: "hub"})
	_, otherLeaf := mustCert(t, CertOptions{CommonName: "other"})
	hdr, err := SignRequestor(hub, claimFor(now))
	if err != nil {
		t.Fatal(err)
	}
	want := RequestorExpect{Audience: "host-1", Method: "GET", Path: "/1.0/jails", Now: now.Add(10 * time.Second)}
	c, err := VerifyRequestor(hubLeaf, hdr, want)
	if err != nil || c.Name != "ann" || c.Projects[0] != "web" {
		t.Fatalf("%v %+v", err, c)
	}
	cases := []struct {
		name string
		hdr  string
		w    RequestorExpect
		leaf bool
	}{
		{"wrong key", hdr, want, false},
		{"expired", hdr, RequestorExpect{Audience: "host-1", Method: "GET", Path: "/1.0/jails", Now: now.Add(31 * time.Second)}, true},
		{"replay long after", hdr, RequestorExpect{Audience: "host-1", Method: "GET", Path: "/1.0/jails", Now: now.Add(time.Hour)}, true},
		{"wrong audience", hdr, RequestorExpect{Audience: "host-2", Method: "GET", Path: "/1.0/jails", Now: now}, true},
		{"wrong method", hdr, RequestorExpect{Audience: "host-1", Method: "DELETE", Path: "/1.0/jails", Now: now}, true},
		{"wrong path", hdr, RequestorExpect{Audience: "host-1", Method: "GET", Path: "/1.0/users", Now: now}, true},
		{"garbage", "abc", want, true},
		{"empty", "", want, true},
		{"bad sig encoding", strings.Split(hdr, ".")[0] + ".!!", want, true},
	}
	for _, tc := range cases {
		peer := hubLeaf
		if !tc.leaf {
			peer = otherLeaf
		}
		if _, err := VerifyRequestor(peer, tc.hdr, tc.w); !api.IsCode(err, api.CodeUnauthorized) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// Future-dated claim.
	fut, _ := SignRequestor(hub, claimFor(now.Add(time.Minute)))
	if _, err := VerifyRequestor(hubLeaf, fut, RequestorExpect{Audience: "host-1", Method: "GET", Path: "/1.0/jails", Now: now}); err == nil {
		t.Error("future-dated claim accepted")
	}
	// Too long a lifetime.
	long := claimFor(now)
	long.Expires = now.Add(time.Hour).Unix()
	lh, _ := SignRequestor(hub, long)
	if _, err := VerifyRequestor(hubLeaf, lh, want); err == nil {
		t.Error("long-lived claim accepted")
	}
	// Altered projects with the original signature.
	p, sig, _ := strings.Cut(hdr, ".")
	raw, _ := urlEnc.DecodeString(p)
	var alt RequestorClaim
	_ = json.Unmarshal(raw, &alt)
	alt.Projects = []string{"web", "db"}
	alt.Global = true
	araw, _ := json.Marshal(alt)
	if _, err := VerifyRequestor(hubLeaf, urlEnc.EncodeToString(araw)+"."+sig, want); err == nil {
		t.Error("altered claim accepted")
	}
	// No audience in the claim: refused, a claim is always addressed to one host.
	noAud := claimFor(now)
	noAud.Audience = ""
	nh, _ := SignRequestor(hub, noAud)
	if _, err := VerifyRequestor(hubLeaf, nh, want); err == nil {
		t.Errorf("claim without audience accepted")
	}
}

func TestClaimClamp(t *testing.T) {
	cl := func(role api.Permission, global bool, projects ...string) RequestorClaim {
		return RequestorClaim{Role: role, Global: global, Projects: projects}
	}
	tests := []struct {
		name     string
		c        RequestorClaim
		perm     api.Permission
		scope    Scope
		wantRole api.Permission
		bad      bool
	}{
		{"lower role wins (cert)", cl(api.PermAdmin, false, "web"), api.PermOperator, Scope{Projects: []string{"web"}}, api.PermOperator, false},
		{"lower role wins (claim)", cl(api.PermReadonly, false, "web"), api.PermAdmin, Scope{Global: true}, api.PermReadonly, false},
		{"global cert, any projects", cl(api.PermOperator, false, "a", "b"), api.PermAdmin, Scope{Global: true}, api.PermOperator, false},
		{"global cert, global claim", cl(api.PermAdmin, true), api.PermAdmin, Scope{Global: true}, api.PermAdmin, false},
		{"restricted cert, global claim", cl(api.PermAdmin, true), api.PermAdmin, Scope{Projects: []string{"web"}}, "", true},
		{"project outside cert", cl(api.PermOperator, false, "web", "db"), api.PermAdmin, Scope{Projects: []string{"web"}}, "", true},
		{"empty claim", cl(api.PermOperator, false), api.PermAdmin, Scope{Global: true}, "", true},
		{"cert without scope", cl(api.PermOperator, false, "web"), api.PermAdmin, Scope{}, "", true},
		{"narrow role equal", cl(api.PermBackupPull, false, "web"), api.PermBackupPull, Scope{Global: true}, api.PermBackupPull, false},
		{"narrow role mismatch", cl(api.PermBackupPull, false, "web"), api.PermBackupPush, Scope{Global: true}, "", true},
		{"admin covers restore", cl(api.PermRestore, false, "web"), api.PermAdmin, Scope{Global: true}, api.PermRestore, false},
		{"readonly cert, narrow claim", cl(api.PermRestore, false, "web"), api.PermReadonly, Scope{Global: true}, "", true},
	}
	for _, tc := range tests {
		role, sc, err := tc.c.Clamp(tc.perm, tc.scope)
		if tc.bad {
			if !api.IsCode(err, api.CodeForbidden) {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err != nil || role != tc.wantRole || sc.Global != tc.c.Global {
			t.Errorf("%s: %v %s %+v", tc.name, err, role, sc)
		}
	}
}

func TestScope(t *testing.T) {
	if (Scope{}).Has() || (Scope{}).Allows("default") || (Scope{}).Validate() == nil {
		t.Fatal("an empty scope must grant nothing")
	}
	s, err := Scope{Projects: []string{" web", "db", "web"}}.Normalize()
	if err != nil || len(s.Projects) != 2 || s.Projects[0] != "db" || !s.Allows("web") || s.Allows("x") || s.Validate() != nil {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := (Scope{Projects: []string{"Bad_Name"}}).Normalize(); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("%v", err)
	}
	if g := (Scope{Global: true}); !g.Has() || !g.Allows("anything") {
		t.Fatal("global")
	}
	if err := (Scope{}).Validate(); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
}
