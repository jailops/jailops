package service_test

import (
	"slices"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// A policy says which jails use it: their own list, a profile, the project's mandatory list. A policy that is used is
// not deleted, whichever way it is used.
func TestPolicyStatusSaysWhoUsesIt(t *testing.T) {
	e, s := newEnv(t)
	projectFixture(t, e, s) // policy web, named by profile small, which jail web has
	alpha := as(globalAdmin, dtest.SeedProject)
	for _, n := range []string{"own", "lone", "floor"} {
		if _, err := s.CreatePolicy(alpha, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: n}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := s.GetJail(alpha, "db")
	if err != nil {
		t.Fatal(err)
	}
	db.Spec.Firewall.Policies = []string{"own"}
	if _, err := s.UpdateJail(alpha, "db", db, service.AnyGeneration); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProject(as(globalAdmin, ""), dtest.SeedProject)
	p.Spec.FirewallPolicies = []string{"floor"}
	if _, err := s.UpdateProject(as(globalAdmin, ""), dtest.SeedProject, p, service.AnyGeneration); err != nil {
		t.Fatal(err)
	}
	want := map[string]api.FirewallPolicyStatus{
		"web":   {UsedBy: []string{"web"}},
		"own":   {UsedBy: []string{"db"}},
		"lone":  {UsedBy: []string{}},
		"floor": {UsedBy: []string{}, Mandatory: true},
	}
	for name, w := range want {
		got, err := s.GetPolicy(alpha, name)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Status.UsedBy, w.UsedBy) || got.Status.Mandatory != w.Mandatory || got.Status.UsedBy == nil {
			t.Errorf("%s: %+v, want %+v", name, got.Status, w)
		}
	}
	list, err := s.ListPolicies(alpha, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range list {
		if w := want[got.Metadata.Name]; !slices.Equal(got.Status.UsedBy, w.UsedBy) || got.Status.Mandatory != w.Mandatory {
			t.Errorf("list %s: %+v, want %+v", got.Metadata.Name, got.Status, w)
		}
	}
	wantCode(t, s.DeletePolicy(alpha, "web", service.AnyGeneration), api.CodeBusy) // through a profile
	wantCode(t, s.DeletePolicy(alpha, "own", service.AnyGeneration), api.CodeBusy)
	wantCode(t, s.DeletePolicy(alpha, "floor", service.AnyGeneration), api.CodeBusy)
	if err := s.DeletePolicy(alpha, "lone", service.AnyGeneration); err != nil {
		t.Fatal(err)
	}
}
