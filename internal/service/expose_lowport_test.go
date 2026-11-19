package service_test

import (
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The start hook loads the forwards of a jail's stored exposure without asking for an assertion again, so the only
// gate on a privileged host port is the one that stores the exposure: a project administrator cannot store one, neither
// through the exposure itself nor by writing the entry into the jail's spec.
func TestAProjectAdministratorCannotStoreAForwardOfAPrivilegedPort(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	seedJail(t, s, "web")
	project := as(alphaOnly, dtest.SeedProject)
	admin := as(globalAdmin, dtest.SeedProject)

	for _, req := range []api.ExposeRequest{
		{Ports: []string{"80"}},
		{Ports: []string{"8080"}, HostPort: 80},
		{Ports: []string{"22"}},
	} {
		_, err := s.ExposeJail(project, "web", req)
		wantCode(t, err, api.CodeForbidden)
	}

	j, _ := s.GetJail(project, "web")
	j.Spec.Expose = []api.ExposeEntry{{Ports: []string{"80"}}}
	_, err := s.UpdateJail(project, "web", j, j.Metadata.Generation)
	wantCode(t, err, api.CodeForbidden)
	j.Spec.Expose = []api.ExposeEntry{{Ports: []string{"8080"}, HostPort: 443}}
	_, err = s.UpdateJail(project, "web", j, j.Metadata.Generation)
	wantCode(t, err, api.CodeForbidden)
	if got, _ := s.GetJail(admin, "web"); len(got.Spec.Expose) != 0 {
		t.Fatalf("a refused request stored an exposure: %+v", got.Spec.Expose)
	}

	// the global administrator stores the same forward, and that is what the hook then loads
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80"}}); err != nil {
		t.Fatal(err)
	}
}
