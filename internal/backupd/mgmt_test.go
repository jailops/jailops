package backupd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jailops.net/jailops/internal/backupd"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/nodetest"
	"jailops.net/jailops/sdk/api"
)

// web talks to the management API handler in process.
type web struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
	csrf   string
	token  string
}

func (r *rig) web() *web { return &web{t: r.t, h: r.Srv.WebHandler()} }

func (w *web) do(method, path string, body any) (int, api.Response, string) {
	w.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "https://node.test"+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "192.0.2.1:5555"
	if w.cookie != nil {
		req.AddCookie(w.cookie)
	}
	if w.csrf != "" && method != "GET" {
		req.Header.Set("X-CSRF-Token", w.csrf)
		req.Header.Set("Origin", "https://node.test")
	}
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	var env api.Response
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-jailops_session" && c.MaxAge > 0 {
			w.cookie = c
		}
	}
	return rec.Code, env, rec.Body.String()
}

func (w *web) login(user, pw string) int {
	w.t.Helper()
	st, env, _ := w.do("POST", "/1.0/auth/login", map[string]string{"username": user, "password": pw})
	if st == 200 {
		var s backupd.SessionBody
		_ = json.Unmarshal(env.Metadata, &s)
		w.csrf = s.CSRF
	}
	return st
}

func TestManagementAPI(t *testing.T) {
	r := startRig(t, memKind, nil)
	const pw = "correct horse battery staple"
	for _, u := range []struct{ name, role string }{{"root", "admin"}, {"ops", "backup-admin"}, {"eye", "readonly"}} {
		if err := backupd.CreateUser(ctx, r.DB, u.name, pw, u.role, testParams); err != nil {
			t.Fatal(err)
		}
	}
	if err := backupd.CreateUser(ctx, r.DB, "weak", "short", "admin", testParams); err == nil {
		t.Fatal("a weak password was accepted")
	}

	// unauthenticated: public routes only
	anon := r.web()
	if st, env, _ := anon.do("GET", "/1.0", nil); st != 200 {
		t.Fatalf("server info: %d", st)
	} else {
		var si api.ServerInfo
		_ = json.Unmarshal(env.Metadata, &si)
		if si.Role != api.RoleBackup || si.Auth != "untrusted" || si.APIVersion != "1.0" {
			t.Fatalf("%+v", si)
		}
	}
	for _, p := range []string{"/1.0/repository", "/1.0/hosts", "/1.0/jobs", "/1.0/audit", "/1.0/users", "/1.0/groups"} {
		if st, _, _ := anon.do("GET", p, nil); st != 401 {
			t.Errorf("GET %s without credentials: %d", p, st)
		}
	}
	if st, _, _ := anon.do("GET", "/1.0/nonexistent", nil); st != 404 {
		t.Errorf("unknown route: %d", st)
	}
	// login failures
	if st := anon.login("root", "wrong password here"); st != 401 {
		t.Fatalf("wrong password: %d", st)
	}
	if st := anon.login("nobody", "whatever password"); st != 401 {
		t.Fatalf("unknown user: %d", st)
	}
	// lockout after repeated failures
	locked := false
	for range 8 {
		if st := anon.login("root", "wrong password again"); st == 429 {
			locked = true
		}
	}
	if !locked {
		t.Fatal("no lockout after repeated failures")
	}

	root := &web{t: t, h: r.Srv.WebHandler()}
	root.do("GET", "/1.0", nil)
	// log in from another address to avoid the lockout of 192.0.2.1
	ops, eye := r.web2("192.0.2.2"), r.web2("192.0.2.3")
	rootW := r.web2("192.0.2.4")
	for w, u := range map[*web]string{rootW: "root", ops: "ops", eye: "eye"} {
		if st := w.login(u, pw); st != 200 {
			t.Fatalf("login %s: %d", u, st)
		}
	}
	// roles
	check := func(w *web, method, path string, body any, want int) {
		t.Helper()
		if st, _, b := w.do(method, path, body); st != want {
			t.Errorf("%s %s: %d, want %d: %s", method, path, st, want, b)
		}
	}
	check(eye, "GET", "/1.0/repository", nil, 200)
	check(eye, "GET", "/1.0/groups", nil, 200)
	check(eye, "GET", "/1.0/hosts", nil, 200)
	check(eye, "GET", "/1.0/audit", nil, 403)
	check(eye, "GET", "/1.0/users", nil, 403)
	check(eye, "POST", "/1.0/jobs/gc", node.GCRequest{DryRun: true}, 403)
	check(eye, "PUT", "/1.0/maintenance", node.ModeBody{Mode: "read-only"}, 403)
	check(ops, "POST", "/1.0/jobs/gc", node.GCRequest{DryRun: true}, 202)
	check(ops, "GET", "/1.0/audit", nil, 403)
	check(ops, "POST", "/1.0/hosts", backupd.HostBody{Name: "x", Fingerprint: strings.Repeat("a", 64)}, 403)
	check(rootW, "GET", "/1.0/audit", nil, 200)
	check(rootW, "GET", "/1.0/users", nil, 200)
	// a response never carries a password hash
	if _, _, b := rootW.do("GET", "/1.0/users", nil); strings.Contains(b, "argon2") || strings.Contains(b, "password_hash") {
		t.Fatalf("the user list leaks hashes: %s", b)
	}
	// CSRF: a session without the header cannot change anything
	rootW2 := &web{t: t, h: rootW.h, cookie: rootW.cookie}
	check(rootW2, "POST", "/1.0/hosts", backupd.HostBody{Name: "csrf", Fingerprint: strings.Repeat("b", 64)}, 403)
	rootW3 := &web{t: t, h: rootW.h, cookie: rootW.cookie, csrf: "wrong"}
	check(rootW3, "POST", "/1.0/hosts", backupd.HostBody{Name: "csrf", Fingerprint: strings.Repeat("b", 64)}, 403)

	// hosts
	check(rootW, "POST", "/1.0/hosts", backupd.HostBody{Name: "newhost", Fingerprint: strings.Repeat("c", 64), Scopes: []string{"backup-push"}}, 201)
	check(rootW, "POST", "/1.0/hosts", backupd.HostBody{Name: "newhost", Fingerprint: strings.Repeat("d", 64)}, 409)
	check(rootW, "POST", "/1.0/hosts", backupd.HostBody{Name: "bad", Fingerprint: strings.Repeat("e", 64), Scopes: []string{"backup-admin"}}, 400) // administration is not a host scope
	check(rootW, "PUT", "/1.0/hosts/newhost", map[string]any{"scopes": []string{"backup-pull"}}, 200)
	check(rootW, "DELETE", "/1.0/hosts/newhost", nil, 200)
	check(rootW, "DELETE", "/1.0/hosts/nohost", nil, 404)

	// tokens: the secret is shown once and works as a bearer credential
	st, env, b := rootW.do("POST", "/1.0/tokens", backupd.TokenBody{Name: "ci", Role: "readonly"})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	var tk struct{ Secret string }
	_ = json.Unmarshal(env.Metadata, &tk)
	if !strings.HasPrefix(tk.Secret, "jot_") {
		t.Fatalf("%s", b)
	}
	if _, _, b := rootW.do("GET", "/1.0/tokens", nil); strings.Contains(b, tk.Secret) {
		t.Fatal("the token list shows a secret")
	}
	tw := &web{t: t, h: rootW.h, token: tk.Secret}
	check(tw, "GET", "/1.0/repository", nil, 200)
	check(tw, "POST", "/1.0/jobs/gc", node.GCRequest{DryRun: true}, 403)
	if st, _, _ := (&web{t: t, h: rootW.h, token: "jot_" + strings.Repeat("a", 52)}).do("GET", "/1.0/repository", nil); st != 401 {
		t.Fatalf("an unknown token: %d", st)
	}
	check(rootW, "DELETE", "/1.0/tokens/ci", nil, 200)
	check(tw, "GET", "/1.0/repository", nil, 401)

	// users: the last administrator is protected
	check(rootW, "PUT", "/1.0/users/root", backupd.UserBody{Role: "readonly"}, 409)
	check(rootW, "DELETE", "/1.0/users/root", nil, 409)
	check(rootW, "PUT", "/1.0/users/eye", backupd.UserBody{Role: "readonly", Disabled: true}, 200)
	check(eye, "GET", "/1.0/repository", nil, 401) // a disabled account loses its session at once

	// maintenance through the management API stops hosts
	h := r.enroll("h")
	check(ops, "PUT", "/1.0/maintenance", node.ModeBody{Mode: "read-only"}, 200)
	g := r.Fix.Group("h", "j")
	if _, err := h.C.Begin(ctx, g, nodetest.SnapshotID(r.Clock.Now(), 1), ""); code(err) != api.CodeMaintenance {
		t.Fatalf("a host during maintenance: %v", err)
	}
	check(ops, "PUT", "/1.0/maintenance", node.ModeBody{Mode: "online"}, 200)
	if s, err := h.C.Begin(ctx, g, nodetest.SnapshotID(r.Clock.Now(), 1), ""); err != nil {
		t.Fatal(err)
	} else {
		_ = s.Abort(ctx)
	}
	// logout ends the session
	check(rootW, "POST", "/1.0/auth/logout", nil, 200)
	check(rootW, "GET", "/1.0/repository", nil, 401)
	// the audit log recorded the administrative actions
	rw := r.web2("192.0.2.9")
	rw.login("root", pw)
	_, env, _ = rw.do("GET", "/1.0/audit?limit=500", nil)
	var entries []struct{ Action, Actor string }
	_ = json.Unmarshal(env.Metadata, &entries)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
	}
	for _, want := range []string{"host.enroll", "host.revoke", "token.create", "token.delete", "user.update", "maintenance.set", "auth.login", "auth.login.failed", "gc.start"} {
		if !seen[want] {
			t.Errorf("no audit entry %q (have %v)", want, seen)
		}
	}
}

func (r *rig) web2(remote string) *web {
	w := r.web()
	w.t = r.t
	w.h = remoteHandler{r.Srv.WebHandler(), remote}
	return w
}

// remoteHandler sets the source address of every request.
type remoteHandler struct {
	h      http.Handler
	remote string
}

func (r remoteHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	req.RemoteAddr = r.remote + ":4000"
	r.h.ServeHTTP(w, req)
}

func TestManagementRouteTableIsCompleteAndDocumented(t *testing.T) {
	reg, err := backupd.Registry()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.VerifyComplete(backupd.MgmtRoutes()); err != nil {
		t.Fatal(err)
	}
	// the node's own routes (host listener and admin socket) have unique keys and local routes are all admin
	seen := map[string]bool{}
	for _, rt := range node.Routes() {
		if seen[rt.Key()] {
			t.Errorf("duplicate route %s", rt.Key())
		}
		seen[rt.Key()] = true
		if rt.Local && !strings.Contains(rt.Pattern, "/admin/") {
			t.Errorf("local route %s outside /admin/", rt.Key())
		}
		if !rt.Local && strings.Contains(rt.Pattern, "/admin/") {
			t.Errorf("host route %s under /admin/", rt.Key())
		}
		if rt.Method == "DELETE" && !rt.Local && !strings.Contains(rt.Pattern, "/sessions/{sid}") {
			t.Errorf("a host route deletes something other than its own session: %s", rt.Key())
		}
	}
}

func TestLoginHardening(t *testing.T) {
	r := startRig(t, memKind, nil)
	const pw = "correct horse battery staple"
	for _, n := range []string{"root", "a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		if err := backupd.CreateUser(ctx, r.DB, n, pw, "admin", testParams); err != nil {
			t.Fatal(err)
		}
	}
	// A spray over many names from one source is stopped by the source key, not only the pair keys.
	spray := r.web2("192.0.2.50")
	locked := false
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "root"} {
		if spray.login(n, "wrong password here") == 429 {
			locked = true
		}
	}
	if !locked {
		t.Fatal("a spray over many user names was not limited")
	}
	if st := spray.login("root", pw); st != 429 {
		t.Fatalf("the right password from a locked source: %d", st)
	}

	// A browser login from another site is refused.
	w := r.web2("192.0.2.60")
	b, _ := json.Marshal(map[string]string{"username": "root", "password": pw})
	req := httptest.NewRequest("POST", "https://node.test/1.0/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.test")
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("cross-origin login: %d", rec.Code)
	}
	// A body that is not declared as JSON is refused.
	req = httptest.NewRequest("POST", "https://node.test/1.0/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("text/plain login: %d", rec.Code)
	}

	// Changing the password ends the user's other sessions but not this one.
	one, two := r.web2("192.0.2.61"), r.web2("192.0.2.62")
	if one.login("root", pw) != 200 || two.login("root", pw) != 200 {
		t.Fatal("login")
	}
	if st, _, body := one.do("POST", "/1.0/auth/password", map[string]string{"current": pw, "new": "another long passphrase 42"}); st != 200 {
		t.Fatalf("%d %s", st, body)
	}
	if st, _, _ := two.do("GET", "/1.0/auth/session", nil); st != 401 {
		t.Fatalf("the other session survived a password change: %d", st)
	}
	if st, _, _ := one.do("GET", "/1.0/auth/session", nil); st != 200 {
		t.Fatalf("the session that changed the password ended: %d", st)
	}
	// Guessing the current password is limited.
	limited := false
	for range 12 {
		if st, _, _ := one.do("POST", "/1.0/auth/password", map[string]string{"current": "wrong guess number one", "new": "yet another passphrase 43"}); st == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("password guesses through the change endpoint are not limited")
	}

	// A bad bearer token is limited per source.
	bad := r.web2("192.0.2.70")
	bad.token = "jot_" + strings.Repeat("a", 52)
	got429 := false
	for range 15 {
		if st, _, _ := bad.do("GET", "/1.0/repository", nil); st == 429 {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("bad bearer tokens are not limited")
	}
}
