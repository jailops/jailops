package server_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

var allRoles = []api.Permission{api.PermReadonly, api.PermOperator, api.PermAdmin,
	api.PermBackupPush, api.PermBackupPull, api.PermRestore, api.PermBackupAdmin}

func concrete(pattern string) string {
	r := strings.NewReplacer("{path...}", "a/b")
	p := r.Replace(pattern)
	for {
		i := strings.Index(p, "{")
		if i < 0 {
			return p
		}
		j := strings.Index(p[i:], "}")
		p = p[:i] + "x" + p[i+j+1:]
	}
}

// Every route is denied to every role below its entry and to anonymous
// callers, and allowed to the roles that satisfy it. The expectation comes
// from the permission table, the behaviour from the running server.
func TestAuthMatrix(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	tokens := map[api.Permission]string{}
	for _, r := range allRoles {
		tokens[r] = e.Token(r)
	}
	reg := auth.DefaultRegistry()
	client := &http.Client{Timeout: 3 * time.Second}
	do := func(method, path, token string) int {
		req, _ := http.NewRequest(method, e.HTTP.URL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	keys := e.D.Server().RouteKeys()
	if len(keys) < 100 {
		t.Fatalf("only %d routes", len(keys))
	}
	for _, key := range keys {
		method, pattern, _ := strings.Cut(key, " ")
		ent, ok := reg.Lookup(method, pattern)
		if !ok {
			t.Fatalf("%s has no permission entry", key)
		}
		path := concrete(pattern)
		anon := do(method, path, "")
		if !ent.Public && anon != 401 {
			t.Errorf("%s: anonymous got %d, want 401", key, anon)
		}
		if ent.Public && anon == 403 {
			t.Errorf("%s: public route answered 403 to an anonymous caller", key)
		}
		// An anonymous local caller (any non-root peer of the unix socket) is the same as an anonymous remote one.
		for peer, cred := range ordinaryPeers {
			if lc, _ := serveAs(e, cred, method, path, "", "{}"); lc != anon {
				t.Errorf("%s: anonymous local (%s) got %d, anonymous remote got %d", key, peer, lc, anon)
			}
		}
		for _, role := range allRoles {
			code := do(method, path, tokens[role])
			allowed := ent.Public || role.Satisfies(ent.Perm)
			switch {
			case !allowed && code != 403:
				t.Errorf("%s as %s: got %d, want 403 (needs %s)", key, role, code, ent.Perm)
			case allowed && !ent.Public && (code == 401 || code == 403):
				t.Errorf("%s as %s: got %d, want it allowed (needs %s)", key, role, code, ent.Perm)
			}
		}
	}
}

// Every entry of the permission table is a route of the server, so a
// permission without a handler cannot hide.
func TestEveryPermissionEntryHasARoute(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	routes := e.D.Server().RouteKeys()
	for _, ent := range auth.Perms {
		if !slices.Contains(routes, ent.Key()) {
			t.Errorf("permission entry %s has no route", ent.Key())
		}
	}
}

func TestEveryRouteIsVerified(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	if err := auth.VerifyComplete(e.D.Server().RouteKeys()); err != nil {
		t.Fatal(err)
	}
}
