package server_test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// hubPair makes a host that trusts the hub's certificate with the given
// scope, and a hub with the host registered as "h1".
func hubPair(t *testing.T, hubScope api.CertificateSpec) (host, hub *dtest.Env) {
	t.Helper()
	host = dtest.New(t, dtest.Options{Listen: true, Hostname: "host1", Mutate: func(c *daemon.Config) { c.Limits.AuthFailureThreshold = 1000 }})
	hub = dtest.New(t, dtest.Options{Listen: true, Hub: true, Hostname: "hub"})
	host.Seed()
	ctx := ctxT(t)
	for _, p := range []string{"web", "db"} { // the hub knows the projects its identities are scoped to
		if _, err := hub.Admin.Projects().Create(ctx, api.Project{Metadata: api.ObjectMeta{Name: p}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"web", "db"} {
		if _, err := host.Admin.Projects().Create(ctx, api.Project{Metadata: api.ObjectMeta{Name: p}, Spec: api.ProjectSpec{
			Networks: []api.ProjectNetwork{{Network: host.MakeNetwork(p)}},
			Storage:  []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	sctx := service.WithActor(ctx, service.Actor{Name: "t", Perm: api.PermAdmin, Global: true})
	hubScope.Fingerprint, hubScope.Kind = hub.D.Fingerprint(), "hub"
	if _, err := host.D.Service().CreateCertificate(sctx, api.Certificate{Metadata: api.ObjectMeta{Name: "hub"}, Spec: hubScope}); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Admin.Remotes().Create(ctx, api.Remote{Metadata: api.ObjectMeta{Name: "h1"},
		Spec: api.RemoteSpec{Address: host.D.HTTPSAddr(), Fingerprint: host.D.Fingerprint()}}); err != nil {
		t.Fatal(err)
	}
	return host, hub
}

// The host applies its own project scoping to what the hub claims, and the
// hub certificate limits what the hub may claim.
func TestHubForwardsTheCallersScope(t *testing.T) {
	host, hub := hubPair(t, api.CertificateSpec{Permission: api.PermAdmin, Global: true})
	ctx := ctxT(t)
	// A caller restricted to "web" on the hub is restricted to "web" on the host.
	ws := hub.Scoped(api.PermAdmin, "web")
	h1 := ws.Host("h1")
	wop, err := h1.Project("web").Jails().Create(ctx, dtest.WebJailOn("w", "lan-web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wop.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"db", "default", "ghost"} {
		if _, err := h1.Project(p).Jails().Get(ctx, "w"); !api.IsCode(err, api.CodeNotFound) {
			t.Fatalf("project %s: %v", p, err)
		}
	}
	wantCode(t, func() error { _, err := h1.Networks().Create(ctx, dtest.LAN()); return err }(), api.CodeForbidden)
	// What the host saw is the caller, not the hub: the audit names the requestor.
	entries, _ := host.Global().Audit(ctx, 100)
	found := false
	for _, a := range entries {
		if strings.HasPrefix(a.Action, "POST /api/v1/jails") && a.Project == "web" && strings.HasPrefix(a.Requestor, "i-admin") && strings.Contains(a.SourceIP, "via hub:hub") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the host's audit does not name the forwarded requestor: %+v", entries)
	}
	// A global caller on the hub is global on the host (the hub certificate is global there).
	if _, err := hub.Admin.Host("h1").Project("web").Jails().Get(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Admin.Host("h1").Networks().Get(ctx, "lan"); err != nil {
		t.Fatal(err)
	}
	// A caller with neither is stopped at the hub.
	tok := hub.ScopelessToken(api.PermAdmin)
	if code, _ := hub.Raw("GET", "/api/v1/jails?host=h1&project=alpha", tok, ""); code != 403 {
		t.Fatalf("%d", code)
	}
}

func TestHubCertificateLimitsTheClaim(t *testing.T) {
	// The hub is trusted for project web only, as an operator.
	host, hub := hubPair(t, api.CertificateSpec{Permission: api.PermOperator, Projects: []string{"web"}})
	ctx := ctxT(t)
	// A global admin on the hub cannot be a global admin on this host.
	_, err := hub.Admin.Host("h1").Networks().Get(ctx, "lan")
	if !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a restricted hub certificate forwarded a global caller: %v", err)
	}
	// A caller restricted to web may act there, as an operator at most.
	ws := hub.Scoped(api.PermAdmin, "web").Host("h1")
	if _, err := ws.Project("web").Jails().Start(ctx, "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if _, err := ws.Project("web").Jails().Create(ctx, dtest.WebJailOn("w", "lan-web")); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("the claimed admin role exceeded the certificate's operator role: %v", err)
	}
	// A caller restricted to db is outside the certificate's scope.
	_, err = hub.Scoped(api.PermOperator, "db").Host("h1").Project("db").Jails().Get(ctx, "x")
	if !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("%v", err)
	}
	// Without a claim the hub certificate acts with its own entry only.
	hubTLS := dialHost(t, host, hub)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/projects", 200}, {"GET", "/api/v1/networks", 200}, {"POST", "/api/v1/networks", 403}, {"GET", "/api/v1/users", 403},
		{"GET", "/api/v1/jails?project=db", 404}, {"GET", "/api/v1/jails?project=web", 200},
	} {
		code, body := hubTLS(tc.method, tc.path, "", nil)
		if code != tc.want {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, code, body)
		}
	}
}

// dialHost returns a function that sends a raw request to host with hub's
// certificate over pinned TLS, and an optional claim header.
func dialHost(t *testing.T, host, hub *dtest.Env) func(method, path, claim string, hdr map[string]string) (int, string) {
	t.Helper()
	cert := hub.D.Certificate()
	return dialHostAs(t, host, &cert)
}

func dialHostAs(t *testing.T, host *dtest.Env, cert *tls.Certificate) func(method, path, claim string, hdr map[string]string) (int, string) {
	t.Helper()
	tc, err := auth.ClientConfigPinned(context.Background(), cert, host.D.HTTPSAddr(), host.D.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 10 * time.Second}
	t.Cleanup(cl.CloseIdleConnections)
	return func(method, path, claim string, hdr map[string]string) (int, string) {
		req, _ := http.NewRequest(method, "https://"+host.D.HTTPSAddr()+path, strings.NewReader("{}"))
		if claim != "" {
			req.Header.Set(auth.RequestorHeader, claim)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
}

func TestHubClaimForgeryIsRejected(t *testing.T) {
	host, hub := hubPair(t, api.CertificateSpec{Permission: api.PermAdmin, Global: true})
	ctx := ctxT(t)
	_ = ctx
	hubCert := hub.D.Certificate()
	send := dialHost(t, host, hub)
	now := time.Now()
	good := auth.RequestorClaim{Version: 1, Name: "alice", Kind: "user", Role: api.PermAdmin, Projects: []string{"web"},
		IssuedAt: now.Unix(), Expires: now.Add(30 * time.Second).Unix(), Audience: host.D.Service().HostUUID(), Method: "GET", Path: "/api/v1/jails"}
	sign := func(c auth.RequestorClaim, cert tls.Certificate) string {
		h, err := auth.SignRequestor(cert, c)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	// The genuine claim works, in its project and not outside it.
	if code, body := send("GET", "/api/v1/jails?project=web", sign(good, hubCert), nil); code != 200 {
		t.Fatalf("control: %d %s", code, body)
	}
	if code, _ := send("GET", "/api/v1/jails?project=db", sign(good, hubCert), nil); code != 404 {
		t.Fatalf("a claim for web read db: %d", code)
	}
	// Altered projects: the payload changed, the signature did not.
	hdr := sign(good, hubCert)
	p, sig, _ := strings.Cut(hdr, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(p)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["projects"] = []string{"web", "db"}
	m["global"] = true
	altered, _ := json.Marshal(m)
	if code, _ := send("GET", "/api/v1/jails?project=db", base64.RawURLEncoding.EncodeToString(altered)+"."+sig, nil); code != 401 {
		t.Fatalf("altered projects: %d", code)
	}
	// Expired.
	old := good
	old.IssuedAt, old.Expires = now.Add(-2*time.Minute).Unix(), now.Add(-time.Minute).Unix()
	if code, _ := send("GET", "/api/v1/jails?project=alpha", sign(old, hubCert), nil); code != 401 {
		t.Fatalf("expired: %d", code)
	}
	// Valid for too long.
	long := good
	long.Expires = now.Add(time.Hour).Unix()
	if code, _ := send("GET", "/api/v1/jails?project=alpha", sign(long, hubCert), nil); code != 401 {
		t.Fatalf("long lived: %d", code)
	}
	// Signed by another key.
	other := newClientCert(t, "mallory")
	if code, _ := send("GET", "/api/v1/jails?project=alpha", sign(good, other), nil); code != 401 {
		t.Fatalf("wrong signature: %d", code)
	}
	// Bound to another request, and to another host.
	if code, _ := send("DELETE", "/api/v1/jails/x?project=alpha", sign(good, hubCert), nil); code != 401 {
		t.Fatalf("method and path replay: %d", code)
	}
	wrongHost := good
	wrongHost.Audience = "00000000-0000-4000-8000-000000000000"
	if code, _ := send("GET", "/api/v1/jails?project=alpha", sign(wrongHost, hubCert), nil); code != 401 {
		t.Fatalf("audience: %d", code)
	}
	// Garbage.
	for _, g := range []string{"x", "a.b", strings.Repeat("A", 40) + "." + strings.Repeat("B", 40)} {
		if code, _ := send("GET", "/api/v1/jails?project=alpha", g, nil); code != 401 {
			t.Fatalf("garbage %q: %d", g, code)
		}
	}
	// A claim from a certificate that is not a hub certificate.
	cl := newClientCert(t, "client")
	ctxs := service.WithActor(context.Background(), service.Actor{Name: "t", Perm: api.PermAdmin, Global: true})
	if _, err := host.D.Service().CreateCertificate(ctxs, api.Certificate{Metadata: api.ObjectMeta{Name: "plain"},
		Spec: api.CertificateSpec{Fingerprint: auth.Fingerprint(cl.Certificate[0]), Permission: api.PermAdmin, Global: true, Kind: "client"}}); err != nil {
		t.Fatal(err)
	}
	plain := dialHostAs(t, host, &cl)
	if code, _ := plain("GET", "/api/v1/jails?project=alpha", sign(good, cl), nil); code != 403 {
		t.Fatalf("a client certificate carried a claim: %d", code)
	}
	// A claim next to a token, and on the unix socket.
	tok := host.Token(api.PermAdmin)
	if code, _ := send("GET", "/api/v1/jails?project=alpha", sign(good, hubCert), map[string]string{"Authorization": "Bearer " + tok}); code == 200 {
		// the hub certificate wins over the token; the claim is honoured as a claim
		t.Log("claim and token together: the certificate decides")
	}
	if code, _ := host.Raw("GET", "/api/v1/jails?project=alpha", tok, ""); code != 200 {
		t.Fatal(code)
	}
	if code, _ := host.RawHdr("GET", "/api/v1/jails?project=alpha", tok, "", map[string]string{auth.RequestorHeader: sign(good, hubCert)}); code != 403 {
		t.Fatalf("a claim with a token: %d", code)
	}
	uc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", host.D.UnixPath())
	}}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", "http://unix/api/v1/jails", nil)
	req.Header.Set(auth.RequestorHeader, sign(good, hubCert))
	resp, err := uc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("a claim on the unix socket: %d", resp.StatusCode)
	}
	// The failures are audited and counted against the source.
	entries, _ := host.Global().Audit(ctxT(t), 500)
	n := 0
	for _, a := range entries {
		if a.Result == "denied" && strings.Contains(a.Detail, "authentication failed") {
			n++
		}
	}
	if n < 5 {
		t.Fatalf("%d failed authentications audited", n)
	}
}

func TestAHubCannotReachAuthenticationEndpoints(t *testing.T) {
	host, hub := hubPair(t, api.CertificateSpec{Permission: api.PermAdmin, Global: true})
	send := dialHost(t, host, hub)
	now := time.Now()
	for _, tc := range [][2]string{{"GET", "/api/v1/auth/session"}, {"POST", "/api/v1/auth/password"}, {"GET", "/api/v1/sessions"}} {
		method, path := tc[0], tc[1]
		h, err := auth.SignRequestor(hub.D.Certificate(), auth.RequestorClaim{Version: 1, Name: "alice", Kind: "user", Role: api.PermAdmin, Global: true,
			IssuedAt: now.Unix(), Expires: now.Add(30 * time.Second).Unix(), Audience: host.D.Service().HostUUID(), Method: method, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if code, body := send(method, path, h, nil); code != 403 {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}
