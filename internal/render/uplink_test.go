package render

import (
	"net/netip"
	"regexp"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// uplinkBase is a jail with an IPv6 address on a routed network and an IPv4 address on a NATed one, both behind em0.
func uplinkBase(rules ...api.FirewallRule) PFInput {
	return PFInput{
		Jail: "alpha_web", Interfaces: []string{"jo0123456789a0a"},
		Addresses: []netip.Addr{ip("2001:db8:1::100"), ip("10.20.0.10")},
		Uplink:    "em0",
		UplinkOf: map[netip.Addr]Uplink{
			ip("2001:db8:1::100"): {Iface: "em0", Routed: true},
			ip("10.20.0.10"):      {Iface: "em0", Routed: false}, // NAT: only a redirect reaches it
		},
		OwnNets:   []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64"), netip.MustParsePrefix("10.20.0.0/24")},
		Foreign:   []netip.Prefix{netip.MustParsePrefix("10.31.0.0/24")},
		Antispoof: true, DefaultIn: "allow", DefaultOut: "allow",
		Sets:     map[string]api.AddressSetSpec{"office": {Addresses: api.Entries("203.0.113.0/24", "2001:db8:ffff::/48")}},
		Jails:    map[string][]string{"db": {"2001:db8:1::200"}},
		Policies: []PFPolicy{{Name: "web", Policy: api.FirewallPolicySpec{Rules: rules}}},
	}
}

// uplinkLines are the rules of an anchor that stand on em0.
func uplinkLines(t *testing.T, in PFInput) []string {
	t.Helper()
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ln := range an.Lines {
		if (strings.HasPrefix(ln, "pass ") || strings.HasPrefix(ln, "block ")) && strings.Contains(ln, " on em0 ") {
			out = append(out, ln)
		}
	}
	return out
}

// An allow rule in that names an external source (any, an address, a prefix, a set) opens the uplink for the jail's routed address, with the same
// source, protocol and ports, and `log` when the rule logs. The NATed IPv4 address is not opened.
func TestUplinkAnAllowRuleOpensTheRoutedAddress(t *testing.T) {
	got := strings.Join(uplinkLines(t, uplinkBase(
		api.FirewallRule{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"2282"}, From: []string{"set:office", "198.51.100.9"}, Log: true},
		api.FirewallRule{Name: "web", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"any"}, Ports: []string{"80", "8000-8100"}},
		api.FirewallRule{Name: "internal", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"9000"}},
	)), "\n")
	for _, want := range []string{
		"block drop in quick on em0 inet6 from <uplink_nets> to 2001:db8:1::100",
		"pass in log quick on em0 inet6 proto tcp from <s_office> to 2001:db8:1::100 port 2282",
		"pass in quick on em0 inet6 proto tcp from any to 2001:db8:1::100 port { 80, 8000:8100 }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "10.20.0.10") {
		t.Errorf("a NATed IPv4 address is opened on the uplink:\n%s", got)
	}
	if strings.Contains(got, "9000") {
		t.Errorf("a rule without a source (for the link) opened the uplink:\n%s", got)
	}
	// the antispoof block stands before the passes
	if strings.Index(got, "block drop in quick on em0") > strings.Index(got, "pass in") {
		t.Errorf("the forged-source block comes after a pass:\n%s", got)
	}
}

// The default never opens the uplink, whatever it is, and neither do the rules that name no external source, the rules
// of the other direction, disabled rules and rules that name a destination of their own.
func TestUplinkOnlyAnExplicitExternalAllowRuleOpensIt(t *testing.T) {
	cases := map[string][]api.FirewallRule{
		"no rule at all (default in allow)": nil,
		"from a jail":                       {{Name: "db", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"5432"}, From: []string{"jail:db"}}},
		"from the host":                     {{Name: "h", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"host"}}},
		"from a tag":                        {{Name: "t", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"tag:ops"}}},
		"disabled":                          {{Name: "off", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, Disabled: true}},
		"out":                               {{Name: "o", Action: "allow", Direction: "out", Proto: "tcp", Ports: []string{"443"}}},
		"deny only":                         {{Name: "d", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"jail:db"}}},
	}
	for name, rules := range cases {
		in := uplinkBase(rules...)
		for _, def := range []string{"allow", "deny"} {
			in.DefaultIn = def
			if got := uplinkLines(t, in); len(got) != 0 {
				t.Errorf("%s (default_in %s): the uplink has rules:\n%s", name, def, strings.Join(got, "\n"))
			}
		}
	}
}

// A mixed list keeps only what can arrive from outside; a deny rule is rendered on the uplink before the passes, so that
// it wins at the door too.
func TestUplinkDenyComesFirstAndAMixedListKeepsTheExternalPart(t *testing.T) {
	got := uplinkLines(t, uplinkBase(
		api.FirewallRule{Name: "ok", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"jail:db", "198.51.100.9", "2001:db8:aaaa::/48"}},
		api.FirewallRule{Name: "bad", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"2001:db8:aaaa::bad"}},
	))
	text := strings.Join(got, "\n")
	deny := strings.Index(text, "block drop in quick on em0 inet6 proto tcp from 2001:db8:aaaa::bad")
	pass := strings.Index(text, "pass in quick on em0 inet6 proto tcp from 2001:db8:aaaa::/48 to 2001:db8:1::100 port 22")
	if deny < 0 || pass < 0 || deny > pass {
		t.Errorf("deny %d, pass %d in:\n%s", deny, pass, text)
	}
	if strings.Contains(text, "j_db") || strings.Contains(text, "198.51.100.9") {
		t.Errorf("a jail or an IPv4 address in the IPv6 rule:\n%s", text)
	}
}

// Each address is opened on the uplink of its own network, and an address of a network without an uplink on none.
func TestUplinkPerAddress(t *testing.T) {
	in := uplinkBase(api.FirewallRule{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"any"}, Ports: []string{"22"}})
	in.Addresses = []netip.Addr{ip("2001:db8:1::100"), ip("203.0.113.10")}
	in.UplinkOf = map[netip.Addr]Uplink{
		ip("2001:db8:1::100"): {Iface: "em0", Routed: true},
		ip("203.0.113.10"):    {Iface: "em1", Routed: true}, // a public IPv4 subnet without NAT on the second network
	}
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	text := an.Text()
	for _, want := range []string{
		"pass in quick on em0 inet6 proto tcp from any to 2001:db8:1::100 port 22",
		"pass in quick on em1 inet proto tcp from any to 203.0.113.10 port 22",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if an.Uplinks[ip("203.0.113.10")] != "em1" || an.Uplinks[ip("2001:db8:1::100")] != "em0" {
		t.Errorf("Uplinks = %v", an.Uplinks)
	}
}

// A port forward is applied on the uplink of the address it leads to.
func TestRedirectIsAppliedOnTheUplinkOfItsAddress(t *testing.T) {
	in := uplinkBase()
	in.Forwards = []api.FirewallRule{{Name: "fwd", Action: "allow", Direction: "in", Proto: "tcp", Redirect: &api.Redirect{HostPort: 8080, JailPort: 80}}}
	in.Uplink = "em9" // the first network's, which holds none of the jail's addresses
	text, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "match in on em0 inet proto tcp from any to (em0) port 8080 rdr-to 10.20.0.10 port 80") || strings.Contains(text, "em9") {
		t.Errorf("the redirect is not on em0:\n%s", text)
	}
}

// The destination of every rule on the uplink is the jail's own addresses and nothing else: never `any`, never an address of
// the host or a gateway, whatever the rules and the host's addresses are. The passes carry a limit of states, and the
// forged sources of the reverse path check are dropped first.
func TestUplinkRulesNameOnlyTheJailsOwnAddresses(t *testing.T) {
	in := uplinkBase(
		api.FirewallRule{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}},
		api.FirewallRule{Name: "dns", Action: "allow", Direction: "in", Proto: "udp", Ports: []string{"53"}, From: []string{"198.51.100.0/24", "2001:db8:aaaa::/48"}},
		api.FirewallRule{Name: "bad", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"192.0.2.66"}},
	)
	in.UplinkOf[ip("10.20.0.10")] = Uplink{Iface: "em0", Routed: true} // a routed IPv4 address too
	in.Host = []netip.Addr{ip("203.0.113.2"), ip("10.20.0.1"), ip("2001:db8:1::1"), ip("fe80::1"), ip("2001:db8:1:2::3")}
	an, err := BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	reTo := regexp.MustCompile(`\bto\s+(\{[^}]*\}|\S+)`)
	own := map[netip.Addr]bool{}
	for _, a := range in.Addresses {
		own[a] = true
	}
	host := map[netip.Addr]bool{}
	for _, a := range in.Host {
		host[a] = true
	}
	n := 0
	for _, ln := range an.Lines {
		if !(strings.HasPrefix(ln, "pass ") || strings.HasPrefix(ln, "block ")) || !strings.Contains(ln, " on em0 ") {
			continue
		}
		n++
		m := reTo.FindStringSubmatch(ln)
		if m == nil || m[1] == "any" {
			t.Errorf("a rule on the uplink with no destination of the jail: %q", ln)
			continue
		}
		for _, f := range strings.FieldsFunc(strings.Trim(m[1], "{}"), func(r rune) bool { return r == ',' || r == ' ' }) {
			a, err := netip.ParseAddr(f)
			if err != nil || !own[a] || host[a] {
				t.Errorf("the destination %q of %q is not an address of the jail", f, ln)
			}
		}
		if strings.HasPrefix(ln, "pass ") && !strings.Contains(ln, "keep state (max 5000, if-bound)") {
			t.Errorf("a pass on the uplink without a limit of states: %q", ln)
		}
	}
	if n == 0 {
		t.Fatal("no rule on the uplink")
	}
	text := an.Text()
	for _, want := range []string{
		"block drop in quick on em0 inet from urpf-failed to 10.20.0.10",
		"block drop in quick on em0 inet6 from urpf-failed to 2001:db8:1::100",
		"block drop in quick on em0 inet proto tcp from 192.0.2.66 to 10.20.0.10 port 22",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// the jail's own limit of states replaces the default
	in.MaxStates = 77
	text, _ = PFAnchor(in)
	if !strings.Contains(text, "keep state (max 77, if-bound)") || strings.Contains(text, "max 5000") {
		t.Errorf("the jail's own limit is not used on the uplink:\n%s", text)
	}
}

// The peers of a WireGuard server are an external source like the Internet: a jail with no allow in rule has no pass on the
// tunnel's interface, one with `allow in --from <tunnel prefix>` has, and the address does not have to be routed (a NATed
// IPv4 address is reached through the tunnel by routing).
func TestTunnelIsAnExternalSource(t *testing.T) {
	tunnel := func(in PFInput) PFInput {
		in.UplinkOf[ip("10.20.0.10")] = Uplink{Iface: "em0", Routed: false, Tunnels: []string{"wg-office"}}
		return in
	}
	onTunnel := func(in PFInput) []string {
		var out []string
		for _, ln := range uplinkLines(t, in) {
			out = append(out, ln)
		}
		var res []string
		an, _ := BuildAnchor(in)
		for _, ln := range an.Lines {
			if (strings.HasPrefix(ln, "pass ") || strings.HasPrefix(ln, "block ")) && strings.Contains(ln, " on wg-office ") {
				res = append(res, ln)
			}
		}
		return res
	}
	// no allow in rule, default_in allow: nothing on the tunnel
	if got := onTunnel(tunnel(uplinkBase())); len(got) != 0 {
		t.Errorf("a jail with only defaults has rules on the tunnel:\n%s", strings.Join(got, "\n"))
	}
	// a rule whose source is the tunnel's prefix opens the tunnel for the NATed address, and nothing on the uplink for it
	in := tunnel(uplinkBase(api.FirewallRule{Name: "office", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"10.99.0.0/24"}}))
	got := strings.Join(onTunnel(in), "\n")
	if !strings.Contains(got, "pass in quick on wg-office inet proto tcp from 10.99.0.0/24 to 10.20.0.10 port 22") || !strings.Contains(got, "keep state (max 5000, if-bound)") ||
		!strings.Contains(got, "block drop in quick on wg-office inet from urpf-failed to 10.20.0.10") {
		t.Errorf("no pass on the tunnel for the tunnel prefix:\n%s", got)
	}
	if up := strings.Join(uplinkLines(t, in), "\n"); strings.Contains(up, "10.20.0.10 port 22") {
		t.Errorf("the NATed address is opened on the uplink:\n%s", up)
	}
	// a rule from another prefix does not open the tunnel for the peers' addresses by accident: the rule says who
	in = tunnel(uplinkBase(api.FirewallRule{Name: "elsewhere", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"192.0.2.0/24"}}))
	if got := strings.Join(onTunnel(in), "\n"); !strings.Contains(got, "from 192.0.2.0/24 to 10.20.0.10") {
		t.Errorf("the rule is rendered with its own source:\n%s", got)
	}
}

// An exposure is an allow rule from anyone (or from the sources it names) that stands on the jail's link and on the uplink of
// every routed address of the families it names; a family it leaves out has no rule at all.
func TestExposureOpensTheUplinkPerFamily(t *testing.T) {
	both := uplinkBase()
	both.Expose = []Exposure{{Name: "expose tcp 80,443", Proto: "tcp", Ports: []string{"80", "443"}}}
	got := strings.Join(uplinkLines(t, both), "\n")
	if !strings.Contains(got, "pass in quick on em0 inet6 proto tcp from any to 2001:db8:1::100 port { 80, 443 }") {
		t.Errorf("an exposure without sources is not open to anyone on the uplink:\n%s", got)
	}
	v4only := uplinkBase()
	v4only.Expose = []Exposure{{Name: "expose tcp 80", Proto: "tcp", Ports: []string{"80"}, Families: []string{"4"}}}
	if got := strings.Join(uplinkLines(t, v4only), "\n"); strings.Contains(got, "port 80") {
		t.Errorf("an IPv4 exposure opened the IPv6 address:\n%s", got)
	}
	from := uplinkBase()
	from.Expose = []Exposure{{Name: "expose tcp 22", Proto: "tcp", Ports: []string{"22"}, From: []string{"set:office"}}}
	if got := strings.Join(uplinkLines(t, from), "\n"); !strings.Contains(got, "pass in quick on em0 inet6 proto tcp from <s_office> to 2001:db8:1::100 port 22") {
		t.Errorf("the sources of an exposure are not kept:\n%s", got)
	}
}

// A forward is for an IPv4 address behind NAT only: an IPv6 address and a routed IPv4 address are reached directly.
func TestForwardsAreForNATedIPv4Only(t *testing.T) {
	in := uplinkBase()
	in.Forwards = []api.FirewallRule{{Name: "expose tcp 80 forward", Action: "allow", Direction: "in", Proto: "tcp", Redirect: &api.Redirect{HostPort: 80, JailPort: 80}}}
	text, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(text, "rdr-to") != 1 || !strings.Contains(text, "rdr-to 10.20.0.10 port 80") {
		t.Errorf("the forward is not for the IPv4 address alone:\n%s", text)
	}
	in.UplinkOf[ip("10.20.0.10")] = Uplink{Iface: "em0", Routed: true}
	text, err = PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "rdr-to") {
		t.Errorf("a routed IPv4 address is forwarded to:\n%s", text)
	}
}
