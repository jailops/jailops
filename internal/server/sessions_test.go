package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

type sess struct {
	hc   *http.Client
	csrf string
	e    *dtest.Env
}

func newSess(t *testing.T, e *dtest.Env, user string) *sess {
	t.Helper()
	hc := jarClient(t)
	if code, out := login(t, e, hc, user, goodPassword, ""); code != 200 {
		t.Fatalf("login %s: %d %v", user, code, out)
	}
	u, _ := url.Parse(e.HTTP.URL)
	s := &sess{hc: hc, e: e}
	for _, ck := range hc.Jar.Cookies(u) {
		if ck.Name == "jailops_csrf" {
			s.csrf = ck.Value
		}
	}
	return s
}

func (s *sess) do(method, path string) (int, []byte) {
	req, _ := http.NewRequest(method, s.e.HTTP.URL+path, strings.NewReader("{}"))
	req.Header.Set("X-CSRF-Token", s.csrf)
	req.Header.Set("Origin", s.e.HTTP.URL)
	resp, err := s.hc.Do(req)
	if err != nil {
		s.e.T.Fatal(err)
	}
	defer resp.Body.Close()
	var env api.Response
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env.Metadata
}

func TestUserScopeAndSessions(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	c := ctxT(t)
	for _, p := range []string{"alpha", "beta"} {
		if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: p}}); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name string, global bool, projects ...string) {
		if _, err := e.Admin.Users().Create(c, api.UserCreate{Metadata: api.ObjectMeta{Name: name},
			Spec: api.UserSpec{Permission: api.PermAdmin, Global: global, Projects: projects}, Password: goodPassword}); err != nil {
			t.Fatal(err)
		}
	}
	mk("root", true)
	mk("ann", false, "alpha")
	mk("bob", false, "beta")
	ann, bob, root := newSess(t, e, "ann"), newSess(t, e, "bob"), newSess(t, e, "root")
	// The session knows who it is and what it may reach.
	code, meta := ann.do("GET", "/1.0/auth/session")
	var id api.Identity
	_ = json.Unmarshal(meta, &id)
	if code != 200 || id.User != "ann" || id.Global || strings.Join(id.Projects, ",") != "alpha" || id.Kind != "user" {
		t.Fatalf("%d %+v", code, id)
	}
	if code, _ := ann.do("GET", "/1.0/jails?project=alpha"); code != 200 {
		t.Fatalf("own project: %d", code)
	}
	if code, _ := ann.do("GET", "/1.0/jails?project=beta"); code != 404 {
		t.Fatalf("another project: %d", code)
	}
	if code, _ := ann.do("GET", "/1.0/users"); code != 403 {
		t.Fatalf("host-level read: %d", code)
	}
	// Sessions: each user lists their own, a global admin lists them all.
	list := func(s *sess) []api.Session {
		_, meta := s.do("GET", "/1.0/sessions")
		var l []api.Session
		_ = json.Unmarshal(meta, &l)
		return l
	}
	al, rl := list(ann), list(root)
	if len(al) != 1 || al[0].User != "ann" || !al[0].Current || al[0].ID == "" || len(rl) != 3 {
		t.Fatalf("%+v\n%+v", al, rl)
	}
	for _, s := range rl {
		if strings.Contains(s.ID, "jailops") || len(s.ID) != 16 {
			t.Fatalf("a session reference should be a short opaque id: %q", s.ID)
		}
	}
	bobRef := ""
	for _, s := range rl {
		if s.User == "bob" {
			bobRef = s.ID
		}
	}
	// ann cannot see or end bob's session; she learns nothing about it.
	if code, _ := ann.do("DELETE", "/1.0/sessions/"+bobRef); code != 404 {
		t.Fatalf("revoking another user's session: %d", code)
	}
	if code, _ := bob.do("GET", "/1.0/auth/session"); code != 200 {
		t.Fatalf("bob's session was ended by ann: %d", code)
	}
	// A global admin revokes it, and it ends at the next request.
	if code, _ := root.do("DELETE", "/1.0/sessions/"+bobRef); code != 200 {
		t.Fatalf("%d", code)
	}
	if code, _ := bob.do("GET", "/1.0/auth/session"); code != 401 {
		t.Fatalf("a revoked session still works: %d", code)
	}
	// Users end their own.
	if code, _ := ann.do("DELETE", "/1.0/sessions/"+al[0].ID); code != 200 {
		t.Fatalf("%d", code)
	}
	if code, _ := ann.do("GET", "/1.0/jails?project=alpha"); code != 401 {
		t.Fatalf("%d", code)
	}
	// Changing a user's scope applies to the next request of a new session.
	u, _ := e.Admin.Users().Get(c, "bob")
	u.Spec.Projects, u.Spec.Global = []string{"alpha"}, false
	if _, err := e.Admin.Users().Update(c, "bob", u, ""); err != nil {
		t.Fatal(err)
	}
	bob = newSess(t, e, "bob")
	if code, _ := bob.do("GET", "/1.0/jails?project=alpha"); code != 200 {
		t.Fatalf("%d", code)
	}
	if code, _ := bob.do("GET", "/1.0/jails?project=beta"); code != 404 {
		t.Fatalf("%d", code)
	}
	// Disabling a user ends the session on the next request.
	u, _ = e.Admin.Users().Get(c, "bob")
	u.Spec.Disabled = true
	if _, err := e.Admin.Users().Update(c, "bob", u, ""); err != nil {
		t.Fatal(err)
	}
	if code, _ := bob.do("GET", "/1.0/auth/session"); code != 401 {
		t.Fatalf("%d", code)
	}
}

var _ = auth.RequestorHeader
