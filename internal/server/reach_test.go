package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The review views of what reaches a jail, over the real daemon: a jail with an allow rule that names the Internet shows
// in its status, in its reach and in the host-wide list, and `from internet` tests both families, the IPv4 one through
// the forward of a NATed network.
func TestReachViewsAndTheInternetSource(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	fw := e.Admin.Firewall()
	mkNet := func(name, v4, gw, rng string, nat bool, v6 *api.IPv6Net) {
		t.Helper()
		if _, err := e.Admin.Networks().Create(c, api.Network{Metadata: api.ObjectMeta{Name: name}, Spec: api.NetworkSpec{Uplink: "em0",
			IPv4: &api.IPv4Net{Subnet: v4, Gateway: gw, Range: rng, NAT: nat}, IPv6: v6}}); err != nil {
			t.Fatal(err)
		}
	}
	mkNet("pub", "10.99.0.0/24", "10.99.0.1", "10.99.0.10-10.99.0.50", false,
		&api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80", Range: "2001:db8:1:2:100::10-2001:db8:1:2:100::ff"})
	mkNet("natnet", "10.98.0.0/24", "10.98.0.1", "10.98.0.10-10.98.0.50", true, nil)
	p, err := e.Admin.Projects().Get(c, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	p.Spec.Networks = append(p.Spec.Networks, api.ProjectNetwork{Network: "pub"}, api.ProjectNetwork{Network: "natnet"})
	if _, err := e.Admin.Projects().Update(c, "alpha", p, ""); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, rules ...api.FirewallRule) {
		t.Helper()
		if _, err := fw.Policies().Create(c, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: name}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow", Rules: rules}}); err != nil {
			t.Fatal(err)
		}
	}
	mk("site", api.FirewallRule{Name: "site", Comment: "the site", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80", "443"}, From: []string{"any"}})
	web := dtest.WebJailOn("web", "pub")
	web.Spec.Network[0].IPv6 = "auto"
	web.Spec.Firewall.Policies = []string{"site"}
	startJailIn(t, e.Admin, web)
	natjail := dtest.WebJailOn("nat", "natnet")
	startJailIn(t, e.Admin, natjail)
	if _, err := e.Admin.Jails().Expose(c, "nat", api.ExposeRequest{Ports: []string{"80"}, HostPort: 8080}); err != nil {
		t.Fatal(err)
	}
	startJailIn(t, e.Admin, dtest.WebJailOn("quiet", "natnet"))

	// the jail's status carries the entries the list shows
	got, err := e.Admin.Jails().Get(c, "web")
	if err != nil {
		t.Fatal(err)
	}
	var fams []string
	for _, x := range got.Status.Exposed {
		fams = append(fams, x.Family+"/"+x.Proto+"/"+x.Ports+"/"+x.Via)
	}
	if strings.Join(fams, " ") != "6/tcp/80,443/uplink 4/tcp/80,443/uplink" {
		t.Fatalf("web exposed = %v", fams)
	}
	reach, err := e.Admin.Jails().Reach(c, "web")
	if err != nil || reach.Error != "" {
		t.Fatalf("reach: %+v %v", reach, err)
	}
	if reach.DefaultIn != "deny" || len(reach.Rules) == 0 {
		t.Fatalf("reach defaults/rules: %+v", reach)
	}
	// the forward of the NATed jail
	nat, err := e.Admin.Jails().Reach(c, "nat")
	if err != nil || len(nat.Exposed) != 1 || nat.Exposed[0].Via != "forward" || nat.Exposed[0].HostPort != 8080 || nat.Exposed[0].Ports != "80" || nat.Exposed[0].Family != "4" {
		t.Fatalf("nat reach: %+v %v", nat.Exposed, err)
	}
	if q, err := e.Admin.Jails().Reach(c, "quiet"); err != nil || len(q.Exposed) != 0 {
		t.Fatalf("a jail with only defaults is not exposed: %+v %v", q.Exposed, err)
	}
	// the host-wide list names the jails and not the quiet one
	all, err := fw.Exposed(c, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, x := range all.Entries {
		seen[x.Jail] = true
	}
	if !seen["web"] || !seen["nat"] || seen["quiet"] {
		t.Fatalf("exposed jails = %v", seen)
	}
	// from internet: both families of web pass on 443; port 22 is blocked on both
	res, err := fw.Test(c, api.FirewallTestRequest{From: "internet", To: "jail:web", Proto: "tcp", Port: 443})
	if err != nil || res.Verdict != api.TestPass || len(res.Cases) != 2 {
		t.Fatalf("internet -> web:443: %+v %v", res, err)
	}
	res, err = fw.Test(c, api.FirewallTestRequest{From: "internet", To: "jail:web", Proto: "tcp", Port: 22})
	if err != nil || res.Verdict != api.TestBlock {
		t.Fatalf("internet -> web:22: %+v %v", res, err)
	}
	// the NATed jail is reached at the host port, through the forward, to the jail's port
	res, err = fw.Test(c, api.FirewallTestRequest{From: "internet", To: "jail:nat", Proto: "tcp", Port: 8080})
	if err != nil || res.Verdict != api.TestPass {
		t.Fatalf("internet -> nat:8080: %+v %v", res, err)
	}
	hops := res.Cases[0].Hops
	if len(hops) < 2 || hops[0].Side != "forward" || !strings.Contains(hops[0].Why, "port 8080 of the host is forwarded to") {
		t.Fatalf("the forward hop: %+v", hops)
	}
	// a port that is not forwarded does not reach a NATed address
	res, err = fw.Test(c, api.FirewallTestRequest{From: "internet", To: "jail:nat", Proto: "tcp", Port: 80})
	if err != nil || res.Verdict != api.TestBlock || !strings.Contains(res.Cases[0].Hops[0].Why, "behind NAT") {
		t.Fatalf("internet -> nat:80: %+v %v", res, err)
	}
}
