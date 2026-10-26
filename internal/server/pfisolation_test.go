package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func startJailIn(t *testing.T, c *sdk.Client, j api.Jail) {
	t.Helper()
	ctx := ctxT(t)
	op, err := c.Jails().Create(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	so, err := c.Jails().Start(ctx, j.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := so.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

// Projects are isolated from each other without anyone writing a policy, and every jail has an anchor.
func TestProjectsAreIsolatedWithoutAnyPolicy(t *testing.T) {
	e := seeded(t) // project alpha on lan 10.20.0.0/24
	net := e.MakeNetwork("b")
	if _, err := e.Admin.Projects().Create(ctxT(t), api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	beta := e.Scoped(api.PermAdmin, "beta")
	startJailIn(t, e.Admin, dtest.WebJailOn("a1", "lan"))
	startJailIn(t, beta, dtest.WebJailOn("b1", net))

	ra := string(e.Files.Data[e.Cfg.StateDir+"/rendered/pf/alpha_a1.conf"])
	rb := string(e.Files.Data[e.Cfg.StateDir+"/rendered/pf/beta_b1.conf"])
	if ra == "" || rb == "" {
		t.Fatalf("a jail without a policy has no anchor:\nalpha=%q\nbeta=%q", ra, rb)
	}
	if !strings.Contains(ra, "table <foreign> persist { 10.31.0.0/24 }") || strings.Contains(ra, "10.20.0.0/24") {
		t.Errorf("alpha's jail must treat beta's network, and only that, as foreign:\n%s", ra)
	}
	if !strings.Contains(rb, "table <foreign> persist { 10.20.0.0/24 }") || strings.Contains(rb, "10.31.0.0/24") {
		t.Errorf("beta's jail must treat alpha's network, and only that, as foreign:\n%s", rb)
	}
	for _, rs := range []string{ra, rb} {
		if !strings.Contains(rs, "pass in quick on") || strings.Contains(rs, "block drop out quick on jo") && !strings.Contains(rs, "pass out quick on") {
			t.Errorf("a jail without a policy must be open apart from the isolation:\n%s", rs)
		}
	}
}

// The project's mandatory defaults are a floor: a jail's own policy cannot turn a default deny into an allow.
func TestAProjectsDefaultDenyIsAFloor(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	if _, err := e.Admin.Firewall().Policies().Create(c, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "base"},
		Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin.Firewall().Policies().Create(c, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "open"},
		Spec: api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "allow"}}); err != nil {
		t.Fatal(err)
	}
	p, err := e.Admin.Projects().Get(c, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	p.Spec.Firewall = "base"
	if _, err := e.Admin.Projects().Update(c, "alpha", p, ""); err != nil {
		t.Fatal(err)
	}
	j := dtest.WebJailOn("w1", "lan")
	j.Spec.Firewall.Policy = "open"
	startJailIn(t, e.Admin, j)
	rs := string(e.Files.Data[e.Cfg.StateDir+"/rendered/pf/alpha_w1.conf"])
	i := strings.Index(rs, "# defaults")
	if i < 0 || !strings.Contains(rs[i:], "block drop out quick on") || strings.Contains(rs[i:], "pass out quick on") && strings.Index(rs[i:], "pass out quick on") < strings.Index(rs[i:], "block drop out quick on") {
		t.Fatalf("the jail's own default allow overrode the project's default deny:\n%s", rs)
	}
}
