package daemon_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// A policy attached while another change of the jail runs is refused as busy (two operations on one jail do not run
// together) and the others are kept: the jail ends with exactly the policies whose attach answered, and one attach
// after another keeps them all.
func TestAttachedPoliciesAreKept(t *testing.T) {
	shellT = t
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := context.Background()
	mustOp(e.Admin.Jails().Create(ctx, dtest.WebJail("web")))
	names := []string{"p1", "p2", "p3", "p4", "p5", "p6"}
	for _, n := range names {
		if _, err := e.Admin.Firewall().Policies().Create(ctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: n}}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = opErr(e.Admin.Jails().ChangeConfig(ctx, "web", api.ConfigChange{Add: map[string]string{"firewall.policies": n}}, false))
		}()
	}
	wg.Wait()
	var want []string
	for i, err := range errs {
		switch {
		case err == nil:
			want = append(want, names[i])
		case !api.IsCode(err, api.CodeBusy):
			t.Fatalf("attach %s: %v", names[i], err)
		}
	}
	got, _ := e.Admin.Jails().Get(ctx, "web")
	have := slices.Clone(got.Spec.Firewall.Policies)
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) || len(want) == 0 {
		t.Fatalf("the jail has %v, the attaches that answered are %v", have, want)
	}
	// one after another they are all kept
	for _, n := range names {
		mustOp(e.Admin.Jails().ChangeConfig(ctx, "web", api.ConfigChange{Add: map[string]string{"firewall.policies": n}}, false))
	}
	got, _ = e.Admin.Jails().Get(ctx, "web")
	have = slices.Clone(got.Spec.Firewall.Policies)
	slices.Sort(have)
	if !slices.Equal(have, names) {
		t.Fatalf("the jail has %v, want all of %v", have, names)
	}
	// and a detach takes out one of them only
	mustOp(e.Admin.Jails().ChangeConfig(ctx, "web", api.ConfigChange{Remove: map[string]string{"firewall.policies": "p3"}}, false))
	got, _ = e.Admin.Jails().Get(ctx, "web")
	if slices.Contains(got.Spec.Firewall.Policies, "p3") || len(got.Spec.Firewall.Policies) != len(names)-1 {
		t.Fatalf("after the detach: %v", got.Spec.Firewall.Policies)
	}
}

// The reach of a jail lists its policies in the order they are rendered, with the rules each has and the default it states.
func TestReachListsThePoliciesInTheirOrder(t *testing.T) {
	shellT = t
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := context.Background()
	mustOp(e.Admin.Jails().Create(ctx, dtest.WebJail("web")))
	mk := func(n string, spec api.FirewallPolicySpec) {
		t.Helper()
		if _, err := e.Admin.Firewall().Policies().Create(ctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: n}, Spec: spec}); err != nil {
			t.Fatal(err)
		}
	}
	mk("closed", api.FirewallPolicySpec{DefaultIn: "deny", Rules: []api.FirewallRule{{Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}}}})
	mk("plain", api.FirewallPolicySpec{})
	mustOp(e.Admin.Jails().ChangeConfig(ctx, "web", api.ConfigChange{Add: map[string]string{"firewall.policies": "closed"}}, false))
	mustOp(e.Admin.Jails().ChangeConfig(ctx, "web", api.ConfigChange{Add: map[string]string{"firewall.policies": "plain"}}, false))
	mustOp(e.Admin.Jails().Start(ctx, "web"))
	r, err := e.Admin.Jails().Reach(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if r.Error != "" || len(r.Policies) != 2 {
		t.Fatalf("reach: %q %+v", r.Error, r.Policies)
	}
	if p := r.Policies[0]; p.Name != "closed" || p.Source != "jail" || p.Rules != 1 || p.DefaultIn != "deny" || p.DefaultOut != "" {
		t.Errorf("the first policy: %+v", p)
	}
	if p := r.Policies[1]; p.Name != "plain" || p.Rules != 0 || p.DefaultIn != "" {
		t.Errorf("the second policy: %+v", p)
	}
}
