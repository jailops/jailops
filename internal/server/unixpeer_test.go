package server_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/server"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// serveAs sends one request through the daemon's handler as if it arrived on the unix socket from the given peer.
func serveAs(e *dtest.Env, cred auth.Cred, method, path, token string, body string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "@"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.D.Handler().ServeHTTP(rec, req.WithContext(server.WithPeerCred(req.Context(), cred)))
	return rec.Code, rec.Body.String()
}

// Peers that look like anything but root: no uid, gid or group membership gives them a role.
var ordinaryPeers = map[string]auth.Cred{
	"ordinary user":            {UID: 1001, GID: 1001},
	"member of group 0":        {UID: 1002, GID: 0, Groups: []uint32{0}},
	"member of wheel and more": {UID: 1003, GID: 1003, Groups: []uint32{0, 5, 1001}},
	"nobody":                   {UID: 65534, GID: 65534},
}

// Root on the socket is a global administrator: the host's recovery access.
func TestRootOnTheSocketIsAGlobalAdministrator(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	root := auth.Cred{UID: 0, GID: 0}
	code, body := serveAs(e, root, "GET", "/1.0/auth/session", "", "")
	if code != 200 {
		t.Fatalf("session: %d %s", code, body)
	}
	var env api.Response
	_ = json.Unmarshal([]byte(body), &env)
	var id api.Identity
	if err := json.Unmarshal(env.Metadata, &id); err != nil {
		t.Fatal(err)
	}
	if id.Kind != "unix" || id.Permission != api.PermAdmin || !id.Global {
		t.Fatalf("root is %+v", id)
	}
	// Everything the global administrator token reads, root reads too, with the same answer.
	for _, p := range []string{"/1.0/users", "/1.0/tokens", "/1.0/certificates", "/1.0/audit", "/1.0/projects", "/1.0/networks",
		"/1.0/storage", "/1.0/images", "/1.0/doctor", "/1.0/resources", "/1.0/remotes", "/1.0/backup-targets", "/1.0/jails?project=alpha"} {
		want, _ := e.Raw("GET", p, e.Token(api.PermAdmin), "")
		got, b := serveAs(e, root, "GET", p, "", "")
		if got != want || got != 200 && got != 501 {
			t.Errorf("GET %s: root got %d (%s), admin token got %d", p, got, strings.TrimSpace(b), want)
		}
	}
	// And writes: a host-level object, and the identity management a recovery needs.
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/1.0/users", `{"metadata":{"name":"recovered"},"spec":{"permission":"admin","global":true},"password":"correct horse battery staple 9"}`},
		{"POST", "/1.0/tokens", `{"metadata":{"name":"rt"},"spec":{"permission":"readonly","global":true}}`},
		{"DELETE", "/1.0/users/recovered", ""},
	} {
		if code, b := serveAs(e, root, tc.method, tc.path, "", tc.body); code/100 != 2 {
			t.Errorf("root %s %s: %d %s", tc.method, tc.path, code, b)
		}
	}
	// The audit trail names who acted.
	entries, err := e.Global().Audit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range entries {
		if a.Action == "POST /1.0/users" && a.Requestor != "" && a.Result == "ok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("root's change is not in the audit: %+v", entries)
	}
}

// A non-root peer is anonymous and reaches only what an unauthenticated remote client reaches.
func TestNonRootPeerIsAnonymous(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	for name, cred := range ordinaryPeers {
		t.Run(name, func(t *testing.T) {
			for _, p := range []string{"/1.0/auth/session", "/1.0/users", "/1.0/jails?project=alpha", "/1.0/projects", "/1.0/audit"} {
				if code, b := serveAs(e, cred, "GET", p, "", ""); code != 401 {
					t.Errorf("GET %s: %d %s, want 401", p, code, b)
				}
			}
			if code, b := serveAs(e, cred, "POST", "/1.0/users", "", `{}`); code != 401 {
				t.Errorf("POST /1.0/users: %d %s", code, b)
			}
			// What an anonymous remote client may do works.
			for _, p := range []string{"/1.0", "/healthz"} {
				if code, b := serveAs(e, cred, "GET", p, "", ""); code != 200 {
					t.Errorf("GET %s: %d %s", p, code, b)
				}
			}
			code, b := serveAs(e, cred, "GET", "/1.0", "", "")
			if code != 200 || !strings.Contains(b, `"untrusted"`) {
				t.Errorf("GET /1.0 reports %s", b)
			}
		})
	}
}

// A token works over the socket for every uid, and the identity is the token's, not the peer's.
func TestTokenOverTheSocketWorksForAnyUID(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	for name, cred := range ordinaryPeers {
		t.Run(name, func(t *testing.T) {
			code, b := serveAs(e, cred, "GET", "/1.0/auth/session", e.Token(api.PermReadonly), "")
			if code != 200 || !strings.Contains(b, `"token"`) || strings.Contains(b, `"unix"`) {
				t.Fatalf("session with a token: %d %s", code, b)
			}
			// The token's role applies: readonly reads, and cannot write.
			if code, _ := serveAs(e, cred, "GET", "/1.0/projects", e.Token(api.PermReadonly), ""); code != 200 {
				t.Errorf("readonly token read: %d", code)
			}
			if code, _ := serveAs(e, cred, "POST", "/1.0/users", e.Token(api.PermReadonly), `{}`); code != 403 {
				t.Errorf("readonly token write: %d", code)
			}
			if code, _ := serveAs(e, cred, "GET", "/1.0/users", e.Token(api.PermAdmin), ""); code != 200 {
				t.Errorf("admin token over the socket: %d", code)
			}
			// A bad token is refused, it does not fall back to anonymous.
			if code, _ := serveAs(e, cred, "GET", "/1.0/projects", "jot_"+strings.Repeat("A", 52), ""); code != 401 {
				t.Errorf("bad token: %d", code)
			}
		})
	}
}

// On the real socket, as the user running the tests (not root): anonymous, and a token or a login works.
func TestRealSocketPeerIsAnonymousUntilItAuthenticates(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the real-socket peer is root here; the fabricated-peer tests cover the same code")
	}
	e := dtest.New(t, dtest.Options{Listen: true})
	ctx := ctxT(t)
	c := unixClient(t, e)
	if info, err := c.Info(ctx); err != nil || info.Auth != "untrusted" {
		t.Fatalf("%v %+v", err, info)
	}
	_, err := c.Whoami(ctx)
	wantCode(t, err, api.CodeUnauthorized)
	for _, err := range c.Projects().List(ctx) {
		wantCode(t, err, api.CodeUnauthorized)
		break
	}
	tc := unixClient(t, e, sdk.WithToken(e.Token(api.PermAdmin)))
	if id, err := tc.Whoami(ctx); err != nil || id.Kind != "token" || !id.Global {
		t.Fatalf("%v %+v", err, id)
	}
	// Password login over the socket gives a session cookie, same as a remote client.
	if _, err := e.Admin.Users().Create(ctx, api.UserCreate{Metadata: api.ObjectMeta{Name: "ann"}, Password: "correct horse battery staple 9",
		Spec: api.UserSpec{Permission: api.PermReadonly, Global: true}}); err != nil {
		t.Fatal(err)
	}
	hc := jarClient(t)
	hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.D.UnixPath())
	}}
	hc.Timeout = 5 * time.Second
	resp, err := hc.Post("http://unix/1.0/auth/login", "application/json", strings.NewReader(`{"username":"ann","password":"correct horse battery staple 9"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login over the socket: %d", resp.StatusCode)
	}
	resp, err = hc.Get("http://unix/1.0/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session cookie over the socket: %d", resp.StatusCode)
	}
}
