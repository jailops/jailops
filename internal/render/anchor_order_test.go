package render

import (
	"net/netip"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The uplink rules and the closing of the host are rendered by different code and must agree: with an allow rule that
// reaches the host's addresses (a prefix that holds them) and an allow rule from the Internet, the anchor closes the host
// before any allow rule, and every rule on the uplink names only the jail's own addresses.
func TestAnchorClosesTheHostBeforeAllowRulesAndOpensTheUplinkOnlyToTheJail(t *testing.T) {
	in := uplinkBase(
		api.FirewallRule{Name: "web", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}},
		api.FirewallRule{Name: "everything", Action: "allow", Direction: "out", To: []string{"0.0.0.0/0", "::/0"}},
	)
	in.Host = []netip.Addr{ip("10.20.0.1"), ip("198.51.100.7"), ip("2001:db8:1::1")}
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	hostBlock, firstPolicyPass := -1, -1
	for i, l := range an.Lines {
		if l == "block drop in quick on jo0123456789a0a from any to <host>" {
			hostBlock = i
		}
		if firstPolicyPass < 0 && strings.HasPrefix(l, "# policy ") {
			firstPolicyPass = i
		}
		if strings.Contains(l, " on em0 ") && strings.HasPrefix(l, "pass ") {
			for _, bad := range []string{" to any", "<host>", "10.20.0.1", "198.51.100.7", "2001:db8:1::1 "} {
				if strings.Contains(l, bad) {
					t.Errorf("a rule on the uplink reaches %q: %s", bad, l)
				}
			}
			if !strings.Contains(l, " to 2001:db8:1::100 ") {
				t.Errorf("a rule on the uplink does not name the jail's address: %s", l)
			}
		}
	}
	if hostBlock < 0 || firstPolicyPass < 0 || hostBlock > firstPolicyPass {
		t.Errorf("the host is closed at line %d, the first policy rule stands at line %d:\n%s", hostBlock, firstPolicyPass, strings.Join(an.Lines, "\n"))
	}
}

// The jails of other projects reach a jail like the Internet does: through an enabled allow in rule whose source covers
// them (any, an address, a prefix, a set), never through a default, and a rule without a source is not for them. What the
// jail sends is not closed on its side. The closing block stands after the allow rules and before the defaults.
func TestForeignProjectsReachTheJailLikeTheInternet(t *testing.T) {
	line := func(an *Anchor, prefix string) int {
		for i, l := range an.Lines {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	build := func(rules ...api.FirewallRule) *Anchor {
		in := uplinkBase(rules...)
		in.Foreign = []netip.Prefix{netip.MustParsePrefix("10.31.0.0/24"), netip.MustParsePrefix("10.32.0.0/24")}
		an, err := BuildAnchor(in)
		if err != nil {
			t.Fatal(err)
		}
		return an
	}
	an := build(
		api.FirewallRule{Name: "world", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}},
		api.FirewallRule{Name: "db", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"5432"}},
		api.FirewallRule{Name: "all", Action: "allow", Direction: "out", To: []string{"0.0.0.0/0", "10.0.0.0/8"}},
	)
	table, closing, world, def := line(an, "table <foreign> persist { 10.31.0.0/24, 10.32.0.0/24 }"), line(an, "block drop out quick on jo0123456789a0a from <foreign> to any"), line(an, "pass out quick on jo0123456789a0a inet proto tcp from any to 10.20.0.10 port 80"), line(an, "# defaults")
	if table < 0 || closing < 0 || world < 0 || def < 0 {
		t.Fatalf("table %d, closing block %d, the world rule %d, defaults %d:\n%s", table, closing, world, def, strings.Join(an.Lines, "\n"))
	}
	// the table is defined before any rule uses it; the block comes after the allow rules and before the defaults
	if first := line(an, "# policy "); table > first || world > closing || closing > def {
		t.Errorf("table at %d, policy at %d, world rule at %d, closing block at %d, defaults at %d", table, first, world, closing, def)
	}
	// an allow in rule without a source is not for the other projects; a rule with a source is for whoever it covers
	var db string
	for _, l := range an.Lines {
		if strings.Contains(l, "port 5432") && strings.HasPrefix(l, "pass out quick") {
			db = l
			break
		}
	}
	if !strings.Contains(db, "from ! <foreign> to ") {
		t.Errorf("a rule without a source is open to the other projects: %q", db)
	}
	// nothing closes what the jail sends, and nothing is closed before the allow rules
	for _, l := range an.Lines {
		if strings.HasPrefix(l, "block drop in quick on jo0123456789a0a from any to <foreign") || strings.Contains(l, "foreign_closed") {
			t.Errorf("an isolation rule of the old kind: %s", l)
		}
	}
	// an allow out rule that names a foreign address changes nothing in the closing of what comes in
	sent := build(api.FirewallRule{Name: "db", Action: "allow", Direction: "out", To: []string{"10.31.0.5"}})
	if line(sent, "block drop out quick on jo0123456789a0a from <foreign> to any") < 0 {
		t.Error("an allow out rule opened what comes in from another project")
	}
	// a deny rule of the jail comes first and wins over an allow rule that covers the same source
	denied := build(
		api.FirewallRule{Name: "world", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}},
		api.FirewallRule{Name: "no-31", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"10.31.0.0/24"}},
	)
	if d, a := line(denied, "block drop out quick on jo0123456789a0a inet proto tcp from 10.31.0.0/24 to 10.20.0.10 port 80"), line(denied, "pass out quick on jo0123456789a0a inet proto tcp from any to 10.20.0.10 port 80"); d < 0 || a < 0 || d > a {
		t.Errorf("the deny rule at %d, the allow rule at %d:\n%s", d, a, strings.Join(denied.Lines, "\n"))
	}
	// an anchor with no foreign network has no table and no closing block
	in := uplinkBase(api.FirewallRule{Name: "all", Action: "allow", Direction: "out", To: []string{"0.0.0.0/0"}})
	in.Foreign = nil
	none, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	if line(none, "table <foreign>") >= 0 {
		t.Error("a table for no foreign network")
	}
}

// A raw fragment stands after the host stage, which is quick: a "pass quick" in it cannot open the host. (A raw fragment is
// written by a global administrator and is the rest of the anchor's business: it can pass what it names, other projects
// included.)
func TestRawFragmentStandsAfterTheHostBlock(t *testing.T) {
	in := uplinkBase(api.FirewallRule{Name: "web", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}})
	in.Host = []netip.Addr{ip("198.51.100.7")}
	in.Policies = append(in.Policies, PFPolicy{Name: "legacy", Project: true, AllowRaw: true,
		Policy: api.FirewallPolicySpec{Raw: "pass quick on jo0123456789a0a from any to any"}})
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	host, raw := -1, -1
	for i, l := range an.Lines {
		switch {
		case l == "block drop in quick on jo0123456789a0a from any to <host>":
			host = i
		case strings.HasPrefix(l, "pass quick on jo0123456789a0a from any to any"):
			raw = i
		}
	}
	if host < 0 || raw < 0 || raw < host {
		t.Errorf("host block at %d, raw at %d:\n%s", host, raw, strings.Join(an.Lines, "\n"))
	}
}
