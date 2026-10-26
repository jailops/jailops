package server_test

import (
	"sort"
	"strings"
	"testing"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk/fake"
)

// The in-memory server that CLI and UI tests use enforces the same roles as
// the daemon: every route it serves has the role of the daemon's table.
func TestFakeRolesMatchTheDaemonTable(t *testing.T) {
	reg := auth.DefaultRegistry()
	perms := fake.RoutePerms()
	if len(perms) < 60 {
		t.Fatalf("the fake serves only %d routes", len(perms))
	}
	var keys []string
	for k := range perms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		method, pattern, _ := strings.Cut(k, " ")
		ent, ok := reg.Lookup(method, pattern)
		if !ok {
			// The fake names path parameters its own way; compare by shape.
			ent, ok = reg.Match(method, concreteShape(pattern))
		}
		if !ok {
			t.Errorf("the fake serves %s, which the daemon does not", k)
			continue
		}
		if ent.Perm != perms[k] {
			t.Errorf("%s: the fake needs %s, the daemon %s", k, perms[k], ent.Perm)
		}
	}
}

func concreteShape(p string) string {
	for {
		i := strings.Index(p, "{")
		if i < 0 {
			return p
		}
		j := strings.Index(p[i:], "}")
		p = p[:i] + "x" + p[i+j+1:]
	}
}
