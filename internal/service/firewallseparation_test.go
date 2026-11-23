package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// A raw PF fragment is pf text of the administrator of the host: the administrator of a project, who could pass or
// block the traffic of other projects' jails with it, may not write one.
func TestRawPFNeedsAGlobalAdministrator(t *testing.T) {
	_, s := newEnv(t)
	spec := api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow", Raw: "block drop quick on lo0 all\n"}
	_, err := s.CreatePolicy(as(alphaOnly, dtest.SeedProject), api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "raw"}, Spec: spec})
	if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "global administrator") {
		t.Fatalf("the administrator of a project wrote raw pf: %v", err)
	}
	if _, err := s.CreatePolicy(as(globalAdmin, dtest.SeedProject), api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "raw"}, Spec: spec}); api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a global administrator was refused: %v", err)
	}
}

// A jail does not reach the host through a policy: JailOps' firewall governs the jails and /etc/pf.conf governs the
// host. An allow rule to the host is refused; a deny rule to it is a rule that closes and stays valid.
func TestAnAllowRuleToTheHostIsRefused(t *testing.T) {
	_, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	rule := func(action string) api.FirewallPolicy {
		return api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "host-" + action}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow",
			Rules: []api.FirewallRule{{Name: "r", Action: action, Direction: "out", Proto: "tcp", To: []string{"host"}, Ports: []string{"22"}}}}}
	}
	_, err := s.CreatePolicy(ctx, rule("allow"))
	if !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "does not reach the host") {
		t.Fatalf("allow out to the host: %v", err)
	}
	if _, err := s.CreatePolicy(ctx, rule("deny")); err != nil {
		t.Fatalf("deny out to the host: %v", err)
	}
	// the host may still be a source of an allow in rule: the host connects to the jail
	in := api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "from-host"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow",
		Rules: []api.FirewallRule{{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"host"}, Ports: []string{"22"}}}}}
	if _, err := s.CreatePolicy(ctx, in); err != nil {
		t.Fatalf("allow in from the host: %v", err)
	}
}

// A profile is another way to the same jail: a project administrator who edits a profile so that its jails get a policy
// with a raw PF fragment is refused, as attaching that policy to the jail itself is.
func TestProfileCannotGiveAJailARawPolicy(t *testing.T) {
	_, s := newEnv(t)
	gctx, pctx := as(globalAdmin, dtest.SeedProject), as(alphaOnly, dtest.SeedProject)
	if _, err := s.CreatePolicy(gctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "raw"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow", Raw: "block drop quick on lo0 all\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProfile(pctx, api.Profile{Metadata: api.ObjectMeta{Name: "p"}, Spec: api.ProfileSpec{Config: map[string]string{"resources.pids": "100"}}}); err != nil {
		t.Fatal(err)
	}
	j := dtest.WebJail("web")
	j.Spec.Profiles = []string{"p"}
	op, err := s.CreateJail(pctx, j)
	mustOp(t, s, op, err)
	p, _ := s.GetProfile(pctx, "p")
	p.Spec.Config = map[string]string{"resources.pids": "100", "firewall.policies": "raw"}
	_, err = s.UpdateProfile(pctx, "p", p, p.Metadata.Generation)
	if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "raw PF fragment") {
		t.Fatalf("a project administrator gave a jail a raw policy through a profile: %v", err)
	}
	p, _ = s.GetProfile(pctx, "p")
	p.Spec.Config = map[string]string{"resources.pids": "100", "firewall.policies": "raw"}
	if _, err := s.UpdateProfile(gctx, "p", p, p.Metadata.Generation); err != nil {
		t.Fatalf("a global administrator was refused: %v", err)
	}
}
