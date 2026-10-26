package server_test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// testClock is a clock tests can move.
type testClock struct{ ns atomic.Int64 }

func newClock() *testClock {
	c := &testClock{}
	c.ns.Store(time.Now().UnixNano())
	return c
}
func (c *testClock) Now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *testClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

func newClientCert(t *testing.T, name string) tls.Certificate {
	t.Helper()
	cert, _, _, err := auth.GenerateCertificate(auth.CertOptions{CommonName: name})
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func tlsClient(t *testing.T, e *dtest.Env, cert tls.Certificate) *sdk.Client {
	t.Helper()
	c, err := sdk.Connect(context.Background(), sdk.Remote("https://"+e.D.HTTPSAddr(),
		sdk.WithPinnedFingerprint(e.D.Fingerprint()), sdk.WithClientCert(cert), sdk.WithRetries(0)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func trustEnv(t *testing.T) (*dtest.Env, *testClock) {
	t.Helper()
	clk := newClock()
	e := dtest.New(t, dtest.Options{Listen: true, Now: clk.Now})
	if _, err := e.Admin.Projects().Create(ctxT(t), api.Project{Metadata: api.ObjectMeta{Name: "web"}, Spec: api.ProjectSpec{
		Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	return e, clk
}

// The trust workflow end to end over pinned TLS: token, redemption, use, revocation.
func TestTrustWorkflow(t *testing.T) {
	e, clk := trustEnv(t)
	c := ctxT(t)
	tok, err := e.Admin.Trust().CreateToken(c, api.TrustTokenRequest{Name: "laptop", Permission: api.PermOperator, Projects: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token, "joc_") || tok.Name != "laptop" || !tok.ExpiresAt.Equal(clk.Now().Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("%+v", tok)
	}
	// The token carries the server's fingerprint; the client pins it before it sends anything.
	if fp, err := sdk.TrustTokenFingerprint(tok.Token); err != nil || fp != e.D.Fingerprint() {
		t.Fatalf("%v %s", err, fp)
	}
	cert := newClientCert(t, "laptop")
	// Before redemption the certificate is a stranger.
	if i, _ := tlsClient(t, e, cert).Info(c); i.Auth != "untrusted" {
		t.Fatalf("%+v", i)
	}
	res, err := sdk.RedeemTrustToken(c, "https://"+e.D.HTTPSAddr(), tok.Token, cert)
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerFingerprint != e.D.Fingerprint() || res.Certificate.Metadata.Name != "laptop" ||
		res.Certificate.Spec.Permission != api.PermOperator || res.Certificate.Spec.Global ||
		strings.Join(res.Certificate.Spec.Projects, ",") != "web" || res.Certificate.Spec.Kind != "client" {
		t.Fatalf("%+v", res)
	}
	tc := tlsClient(t, e, cert)
	id, err := tc.Whoami(c)
	if err != nil || id.Permission != api.PermOperator || id.Global || strings.Join(id.Projects, ",") != "web" || id.Kind != "certificate" {
		t.Fatalf("%v %+v", err, id)
	}
	// It works in its project, and only there.
	if _, err := tc.Project("web").Jails().Get(c, "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if _, err := tc.Project("default").Jails().Get(c, "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if _, err := tc.Project("default").Jails().Start(c, "x"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := tc.Project("web").Jails().Create(c, sdk.NewJail("x", webSpec())); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("an operator created a jail: %v", err) // the role is checked first
	}
	if _, err := tc.Networks().Create(c, dtest.LAN()); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("%v", err)
	}
	// The single-use token is spent: a replay is unauthorized, with this or another certificate.
	for _, cc := range []tls.Certificate{cert, newClientCert(t, "other")} {
		_, err = sdk.RedeemTrustToken(c, "https://"+e.D.HTTPSAddr(), tok.Token, cc)
		wantCode(t, err, api.CodeUnauthorized)
	}
	// Revocation is effective on the next request.
	if err := e.Admin.Certificates().Delete(c, "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Whoami(c); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("a revoked certificate still works: %v", err)
	}
	if i, _ := tc.Info(c); i.Auth != "untrusted" {
		t.Fatalf("%+v", i)
	}
	// The token is shown once: not in the log, not in the audit, not in what lists tokens.
	if strings.Contains(e.Log(), tok.Token) || strings.Contains(e.Log(), tok.Token[len("joc_"):len("joc_")+24]) {
		t.Fatal("the trust token reached the log")
	}
	// The issuing and the use are audited.
	entries, _ := e.Admin.Audit(c, 500)
	for _, a := range entries {
		if strings.Contains(a.Resource+a.Detail+a.Action, tok.Token[len("joc_"):len("joc_")+24]) {
			t.Fatalf("the trust token reached the audit: %+v", a)
		}
	}
	var issued, redeemed, denied bool
	for _, a := range entries {
		switch {
		case strings.Contains(a.Action, "POST /1.0/trust/tokens") && a.Result == "ok":
			issued = true
		case strings.Contains(a.Action, "POST /1.0/trust/clients") && a.Result == "ok":
			redeemed = true
		case strings.Contains(a.Action, "POST /1.0/trust/clients") && a.Result == "denied":
			denied = true
		}
	}
	if !issued || !redeemed || !denied {
		t.Fatalf("audit: issued %v redeemed %v denied %v\n%+v", issued, redeemed, denied, entries)
	}
}

func TestTrustTokenExpiryAndExpiringCertificate(t *testing.T) {
	e, clk := trustEnv(t)
	c := ctxT(t)
	addr := "https://" + e.D.HTTPSAddr()
	tok, err := e.Admin.Trust().CreateToken(c, api.TrustTokenRequest{Name: "late", Permission: api.PermReadonly, Global: true, TTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(61 * time.Second)
	_, err = sdk.RedeemTrustToken(c, addr, tok.Token, newClientCert(t, "late"))
	wantCode(t, err, api.CodeUnauthorized)
	// A certificate that was given an expiry stops working at that time.
	tok, err = e.Admin.Trust().CreateToken(c, api.TrustTokenRequest{Name: "temp", Permission: api.PermReadonly, Global: true,
		ExpiresAt: clk.Now().Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cert := newClientCert(t, "temp")
	if _, err := sdk.RedeemTrustToken(c, addr, tok.Token, cert); err != nil {
		t.Fatal(err)
	}
	tc := tlsClient(t, e, cert)
	if _, err := tc.Whoami(c); err != nil {
		t.Fatal(err)
	}
	clk.Advance(3 * time.Hour)
	// The service's rebuild of its trust cache happens on the next change; expiry is checked on use.
	if _, err := tc.Whoami(c); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("an expired certificate still works: %v", err)
	}
}

func TestTrustTokenForgeryAndWrongServer(t *testing.T) {
	e, _ := trustEnv(t)
	other, _ := trustEnv(t)
	c := ctxT(t)
	addr := "https://" + e.D.HTTPSAddr()
	// A token of another daemon is not a token of this one (different key and fingerprint).
	ot, err := other.Admin.Trust().CreateToken(c, api.TrustTokenRequest{Name: "x", Permission: api.PermReadonly, Global: true})
	if err != nil {
		t.Fatal(err)
	}
	var body = func(token string) api.TrustClientRequest { return api.TrustClientRequest{Token: token} }
	_ = body
	// Presenting it to the wrong server: the client pins the fingerprint in the token and the wrong
	// server fails the TLS pin, so the token never leaves the client.
	_, err = sdk.RedeemTrustToken(c, addr, ot.Token, newClientCert(t, "x"))
	if err == nil || api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("the token reached the wrong server: %v", err)
	}
	// A forged token that names this server's fingerprint is refused by the HMAC.
	good, err := e.Admin.Trust().CreateToken(c, api.TrustTokenRequest{Name: "real", Permission: api.PermReadonly, Global: true})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.TrimPrefix(good.Token, "joc_"), ".", 2)
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	claims["name"] = "evil"
	forged, _ := json.Marshal(claims)
	ftok := "joc_" + base64.RawURLEncoding.EncodeToString(forged) + "." + parts[1]
	_, err = sdk.RedeemTrustToken(c, addr, ftok, newClientCert(t, "evil"))
	wantCode(t, err, api.CodeUnauthorized)
	// A token with the right shape and the wrong key.
	_, err = sdk.RedeemTrustToken(c, addr, "joc_"+parts[0]+"."+base64.RawURLEncoding.EncodeToString(make([]byte, 32)), newClientCert(t, "evil"))
	wantCode(t, err, api.CodeUnauthorized)
	// The genuine token still works afterwards: failures do not spend it.
	if _, err := sdk.RedeemTrustToken(c, addr, good.Token, newClientCert(t, "real")); err != nil {
		t.Fatal(err)
	}
	// A join token is not a trust token.
	jt, _, err := auth.NewJoinToken(make([]byte, 32), "m", e.D.Fingerprint(), nil, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = sdk.RedeemTrustToken(c, addr, jt, newClientCert(t, "j"))
	if err == nil {
		t.Fatal("a join token was accepted as a trust token")
	}
}

func TestTrustTokenRulesAtCreation(t *testing.T) {
	e, _ := trustEnv(t)
	c := ctxT(t)
	tr := e.Admin.Trust()
	create := func(r api.TrustTokenRequest) error { _, err := tr.CreateToken(c, r); return err }
	wantCode(t, create(api.TrustTokenRequest{Name: "n", Permission: api.PermOperator}), api.CodeInvalid) // neither projects nor global
	wantCode(t, create(api.TrustTokenRequest{Name: "n", Permission: api.PermOperator, Projects: []string{"ghost"}}), api.CodeInvalid)
	wantCode(t, create(api.TrustTokenRequest{Name: "", Permission: api.PermOperator, Global: true}), api.CodeInvalid)
	wantCode(t, create(api.TrustTokenRequest{Name: "n", Permission: "root", Global: true}), api.CodeInvalid)
	wantCode(t, create(api.TrustTokenRequest{Name: "n", Permission: api.PermOperator, Global: true, TTLSeconds: -1}), api.CodeInvalid)
	// Only a global admin issues trust tokens.
	ws := e.Scoped(api.PermAdmin, "web")
	wantCode(t, func() error {
		_, err := ws.Trust().CreateToken(c, api.TrustTokenRequest{Name: "n", Permission: api.PermReadonly, Projects: []string{"web"}})
		return err
	}(), api.CodeForbidden)
	// A name in use as a certificate is refused.
	tok, _ := tr.CreateToken(c, api.TrustTokenRequest{Name: "dup", Permission: api.PermReadonly, Global: true})
	if _, err := sdk.RedeemTrustToken(c, "https://"+e.D.HTTPSAddr(), tok.Token, newClientCert(t, "dup")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, create(api.TrustTokenRequest{Name: "dup", Permission: api.PermReadonly, Global: true}), api.CodeAlreadyExists)
}

// Failed redemptions are rate limited per source and audited.
func TestTrustRedemptionIsRateLimited(t *testing.T) {
	e, _ := trustEnv(t)
	c := ctxT(t)
	addr := "https://" + e.D.HTTPSAddr()
	// A real server fingerprint but a token that is garbage: it reaches the server.
	fake := "joc_" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"v":1,"id":"x","name":"n","fp":"%s","exp":%d,"sec":"s"}`, e.D.Fingerprint(), time.Now().Add(time.Hour).Unix()))) + ".AAAA"
	var last error
	for i := 0; i < 8; i++ {
		_, last = sdk.RedeemTrustToken(c, addr, fake, newClientCert(t, "x"))
	}
	wantCode(t, last, api.CodeRateLimited)
	// Even a genuine token is refused while the source is locked out.
	good, _ := e.Admin.Trust().CreateToken(c, api.TrustTokenRequest{Name: "g", Permission: api.PermReadonly, Global: true})
	_, err := sdk.RedeemTrustToken(c, addr, good.Token, newClientCert(t, "g"))
	wantCode(t, err, api.CodeRateLimited)
	entries, _ := e.Admin.Audit(c, 500)
	n := 0
	for _, a := range entries {
		if strings.Contains(a.Action, "/1.0/trust/clients") && a.Result == "denied" {
			n++
		}
	}
	if n < 5 {
		t.Fatalf("%d denied redemptions audited", n)
	}
}

// An identity that has neither projects nor global can do nothing.
func TestIdentityWithNeitherProjectsNorGlobalCanDoNothing(t *testing.T) {
	e, _ := trustEnv(t)
	c := ctxT(t)
	// The API refuses to create one.
	_, err := e.Admin.Tokens().Create(c, api.Token{Metadata: api.ObjectMeta{Name: "n"}, Spec: api.TokenSpec{Permission: api.PermAdmin}})
	wantCode(t, err, api.CodeInvalid)
	_, err = e.Admin.Users().Create(c, api.UserCreate{Metadata: api.ObjectMeta{Name: "n"}, Spec: api.UserSpec{Permission: api.PermAdmin}})
	wantCode(t, err, api.CodeInvalid)
	_, err = e.Admin.Certificates().Create(c, api.Certificate{Spec: api.CertificateSpec{Fingerprint: strings.Repeat("ab", 32), Permission: api.PermAdmin}})
	wantCode(t, err, api.CodeInvalid)
	_, err = e.Admin.Tokens().Create(c, api.Token{Metadata: api.ObjectMeta{Name: "n"}, Spec: api.TokenSpec{Permission: api.PermAdmin, Projects: []string{}}})
	wantCode(t, err, api.CodeInvalid) // an empty list never means all
	// One that is stored anyway gets nothing: not even reads.
	tok := e.ScopelessToken(api.PermAdmin)
	for _, rt := range []struct{ method, path string }{
		{"GET", "/1.0/jails"}, {"GET", "/1.0/networks"}, {"GET", "/1.0/projects"}, {"GET", "/1.0/images"}, {"GET", "/1.0/operations"},
		{"GET", "/1.0/audit"}, {"GET", "/1.0/storage"}, {"POST", "/1.0/jails"}, {"GET", "/1.0/users"}, {"GET", "/1.0/events"},
	} {
		code, body := e.Raw(rt.method, rt.path, tok, "{}")
		if code != 403 {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, code, body)
		}
	}
	// Its own session information is still readable, and says what it is.
	code, body := e.Raw("GET", "/1.0/auth/session", tok, "")
	if code != 200 || !strings.Contains(body, `"permission":"admin"`) || strings.Contains(body, `"global":true`) {
		t.Fatalf("%d %s", code, body)
	}
}
