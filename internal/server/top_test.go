package server_test

import (
	"net/http"
	"slices"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// The live usage over the real daemon: any role may read it, an identity sees the running jails of its projects, a
// global identity also the host, and a caller that is nobody gets nothing.
func TestTopRoute(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	startJailIn(t, e.Admin, dtest.WebJail("web"))
	e.Run.On("rctl", []string{"-u", "jail:alpha_web"}, "maxproc=4\nmemoryuse=2048\npcpu=3\n")

	f, err := e.Global().Top(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Jails) != 1 || f.Jails[0].Project != dtest.SeedProject || f.Jails[0].Name != "web" || f.Jails[0].Processes == nil || *f.Jails[0].Processes != 4 {
		t.Fatalf("%+v", f.Jails)
	}
	if f.MeasuredAt.IsZero() {
		t.Fatal("a frame says when it was measured")
	}

	// a read-only token of the project reads it, and a label that matches nothing narrows it away
	ro := e.Scoped(api.PermReadonly, dtest.SeedProject)
	if f, err := ro.Top(c); err != nil || len(f.Jails) != 1 || f.Host != nil {
		t.Fatalf("a project identity: %+v %v", f, err)
	}
	if f, err := ro.Top(c, sdk.Label("role", "none")); err != nil || len(f.Jails) != 0 {
		t.Fatalf("a label: %+v %v", f, err)
	}

	// no credentials, and a credential with no scope
	if _, err := e.Anonymous().Top(c); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("anonymous: %v", err)
	}
	if code, _ := e.RawHdr(http.MethodGet, "/api/v1/top", e.ScopelessToken(api.PermReadonly), "", nil); code != http.StatusForbidden {
		t.Fatalf("a token with no scope: %d", code)
	}
	// a project the identity has no scope in
	if _, err := ro.Project("beta").Top(c); err == nil {
		t.Fatal("another project")
	}
	if !slices.Contains(api.Extensions, "top") {
		t.Fatal("the server does not announce the extension")
	}
}
