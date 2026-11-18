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
