package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// A jail's own policy is neither a policy that something else manages (app-JAIL) nor the project's mandatory policy,
// which is rendered for every jail already.
func TestJailMayNotNameAManagedPolicyOrTheProjectFloor(t *testing.T) {
	_, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	spec := api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}
	if err := service.SetManagedPolicy(s, dtest.SeedProject, "app-db", api.AppAccessManagedBy, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePolicy(ctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "floor"}, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.GetProject(as(globalAdmin, ""), dtest.SeedProject)
	if err != nil {
		t.Fatal(err)
	}
	pr.Spec.FirewallPolicies = []string{"floor"}
	if _, err := s.UpdateProject(as(globalAdmin, ""), dtest.SeedProject, pr, pr.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"app-db": "is managed by " + api.AppAccessManagedBy, "floor": "is a mandatory policy of project"} {
		j := dtest.WebJail("web")
		j.Spec.Firewall.Policies = []string{name}
		_, err := s.CreateJail(ctx, j)
		wantCode(t, err, api.CodeInvalid)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A rule-only policy that allows traffic in from named sources leaves the jail open unless some policy says deny: the
// jail says so in its warnings, and a project policy that says deny takes the warning away.
func TestOpenInboundWarning(t *testing.T) {
	_, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	if _, err := s.CreatePolicy(ctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "ssh-office"}, Spec: api.FirewallPolicySpec{
		Rules: []api.FirewallRule{{Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"2282"}, From: []string{"192.0.2.0/24"}}}}}); err != nil {
		t.Fatal(err)
	}
	j := dtest.WebJail("web")
	j.Spec.Firewall.Policies = []string{"ssh-office"}
	op, err := s.CreateJail(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	got, err := s.GetJail(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	warned := func(j api.Jail) bool {
		for _, w := range j.Status.Warnings {
			if strings.Contains(w, "no policy says deny") && strings.Contains(w, "ssh-office allows tcp 2282 from 192.0.2.0/24") {
				return true
			}
		}
		return false
	}
	if !warned(got) {
		t.Fatalf("no warning: %v", got.Status.Warnings)
	}
	if _, err := s.CreatePolicy(ctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "floor"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny"}}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.GetProject(as(globalAdmin, ""), dtest.SeedProject)
	if err != nil {
		t.Fatal(err)
	}
	pr.Spec.FirewallPolicies = []string{"floor"}
	if _, err := s.UpdateProject(as(globalAdmin, ""), dtest.SeedProject, pr, pr.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetJail(ctx, "web"); err != nil || warned(got) {
		t.Fatalf("the project's deny does not take the warning away: %v %v", err, got.Status.Warnings)
	}
}
