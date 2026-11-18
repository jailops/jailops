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

// The other projects are closed before the allow rules: a wide prefix (0.0.0.0/0, a /8) and `allow in --from any` do
// not reach or admit a foreign network, so the isolation of a project does not depend on the policies of another. A
// rule that names a foreign address with a prefix of its own opens it.
func TestForeignProjectsAreClosedBeforeTheAllowRulesExceptWhatIsNamed(t *testing.T) {
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
	wide := build(
		api.FirewallRule{Name: "all", Action: "allow", Direction: "out", To: []string{"0.0.0.0/0", "10.0.0.0/8"}},
		api.FirewallRule{Name: "world", Action: "allow", Direction: "in", From: []string{"any"}},
	)
	closed, firstAllow := line(wide, "block drop out quick on jo0123456789a0a from <foreign_closed> to any"), line(wide, "pass ")
	toClosed := line(wide, "block drop in quick on jo0123456789a0a from any to <foreign_closed>")
	if closed < 0 || toClosed < 0 {
		t.Fatalf("no early isolation block:\n%s", strings.Join(wide.Lines, "\n"))
	}
	for _, l := range wide.Lines {
		if strings.HasPrefix(l, "table <foreign_closed>") && (!strings.Contains(l, "10.31.0.0/24") || !strings.Contains(l, "10.32.0.0/24")) {
			t.Errorf("a wide prefix named a foreign network: %s", l)
		}
	}
	// the blocks stand before the first policy rule that passes something (the host stage has its own passes: ICMP, DNS)
	policy := line(wide, "# policy ")
	if policy < 0 || closed > policy+60 {
		t.Fatalf("the isolation stands at %d, the policy at %d", closed, policy)
	}
	for i, l := range wide.Lines {
		if strings.HasPrefix(l, "pass ") && strings.Contains(l, "from any to ! <not_any>") && i < closed {
			t.Errorf("an allow rule stands before the isolation: %s", l)
		}
	}
	_ = firstAllow
	// the old block after the allow rules stays as the last word
	if line(wide, "block drop out quick on jo0123456789a0a from <foreign> to any") < 0 {
		t.Error("the block after the allow rules is gone")
	}
	// a rule that names an address inside a foreign network opens that network: it is left out of the early block
	named := build(api.FirewallRule{Name: "db", Action: "allow", Direction: "out", To: []string{"10.31.0.5"}})
	for _, l := range named.Lines {
		if strings.HasPrefix(l, "table <foreign_closed>") && (strings.Contains(l, "10.31.0.0/24") || !strings.Contains(l, "10.32.0.0/24")) {
			t.Errorf("a named foreign network is closed early, or an unnamed one is not: %s", l)
		}
	}
	// an anchor with no foreign network has no early block
	in := uplinkBase(api.FirewallRule{Name: "all", Action: "allow", Direction: "out", To: []string{"0.0.0.0/0"}})
	in.Foreign = nil
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	if line(an, "table <foreign_closed>") >= 0 {
		t.Error("an early block for no foreign network")
	}
}

// A raw fragment stored by a project administrator (before raw fragments needed a global one) stands after the host
// stage and the isolation of other projects, both quick: a "pass quick" in it cannot open the host or another project.
func TestRawFragmentStandsAfterTheHostAndIsolationBlocks(t *testing.T) {
	in := uplinkBase(api.FirewallRule{Name: "web", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}})
	in.Host = []netip.Addr{ip("198.51.100.7")}
	in.Policies = append(in.Policies, PFPolicy{Name: "legacy", Project: true, AllowRaw: true,
		Policy: api.FirewallPolicySpec{Raw: "pass quick on jo0123456789a0a from any to any"}})
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	host, iso, raw := -1, -1, -1
	for i, l := range an.Lines {
		switch {
		case l == "block drop in quick on jo0123456789a0a from any to <host>":
			host = i
		case strings.HasPrefix(l, "block drop in quick on jo0123456789a0a from any to <foreign_closed>"):
			iso = i
		case strings.HasPrefix(l, "pass quick on jo0123456789a0a from any to any"):
			raw = i
		}
	}
	if host < 0 || iso < 0 || raw < 0 || raw < host || raw < iso {
		t.Errorf("host block at %d, isolation at %d, raw at %d:\n%s", host, iso, raw, strings.Join(an.Lines, "\n"))
	}
}
