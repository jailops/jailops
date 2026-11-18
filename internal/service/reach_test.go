package service

import (
	"net/netip"
	"strings"
	"testing"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

func rip(s string) netip.Addr { return netip.MustParseAddr(s) }

// reachInput is a dual-stack jail of project alpha: IPv6 routed on em0, IPv4 behind NAT on em0, the host on both
// networks, one network of another project.
func reachInput(tunnel bool, rules ...api.FirewallRule) (render.PFInput, reachCtx) {
	v6, v4 := rip("2001:db8:1::100"), rip("10.20.0.10")
	u6 := render.Uplink{Iface: "em0", Routed: true}
	if tunnel {
		u6.Tunnels = []string{"wg-office"}
	}
	in := render.PFInput{
		Jail: "alpha_web", Interfaces: []string{"jo0123456789a0a"}, Addresses: []netip.Addr{v6, v4}, Uplink: "em0",
		UplinkOf:  map[netip.Addr]render.Uplink{v6: u6, v4: {Iface: "em0"}},
		OwnNets:   []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64"), netip.MustParsePrefix("10.20.0.0/24")},
		Foreign:   []netip.Prefix{netip.MustParsePrefix("10.31.0.0/24"), netip.MustParsePrefix("2001:db8:2::/64")},
		Host:      []netip.Addr{rip("10.20.0.1"), rip("2001:db8:1::1")},
		Antispoof: true, DefaultIn: "deny", DefaultOut: "allow",
		Sets:     map[string]api.AddressSetSpec{"office": {Addresses: api.Entries("203.0.113.0/24", "2001:db8:ffff::/48")}},
		Policies: []render.PFPolicy{{Name: "web", Policy: api.FirewallPolicySpec{Rules: rules}}},
	}
	rc := reachCtx{host: in.Host, foreign: in.Foreign, own: in.OwnNets, addrs: in.Addresses}
	return in, rc
}

func reachOfInput(t *testing.T, in render.PFInput, rc reachCtx) api.JailReach {
	t.Helper()
	an, err := render.BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	return computeReach(an, rc, map[netip.Addr]string{rip("2001:db8:1::100"): "pub", rip("10.20.0.10"): "pub"}, api.JailReach{Project: "alpha", Jail: "web"})
}

func rowsOf(r api.JailReach, source string) []api.ReachRow {
	var out []api.ReachRow
	for _, row := range r.Rows {
		if row.Source == source {
			out = append(out, row)
		}
	}
	return out
}

func exposedText(r api.JailReach) string {
	var parts []string
	for _, e := range r.Exposed {
		s := e.Family + " " + e.Proto + " " + e.Ports + " via " + e.Via
		if e.HostPort != 0 {
			s += " host-port " + itoa(e.HostPort)
		}
		if e.Restricted {
			s += " from " + strings.Join(e.From, ",")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// A web jail with 80 and 443 open to everybody over IPv6 and a forward over IPv4: the Internet reaches it by the uplink
// pass over IPv6 and by the forward over IPv4, and nothing else.
func TestReachWebJailOnBothFamilies(t *testing.T) {
	in, rc := reachInput(false,
		api.FirewallRule{Name: "site", Comment: "the site", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80", "443"}, From: []string{"any"}},
	)
	r := reachOfInput(t, in, rc)
	if got, want := exposedText(r), "6 tcp 80,443 via uplink"; got != want {
		t.Errorf("exposed = %q, want %q", got, want)
	}
	if e := r.Exposed[0]; e.Address != "2001:db8:1::100" || e.Network != "pub" || e.Interface != "em0" || !strings.Contains(e.By, "policy web rule 1 site") {
		t.Errorf("entry = %+v", e)
	}
	rows := rowsOf(r, api.ReachInternet)
	if len(rows) != 1 || rows[0].Decision != "pass" || rows[0].Ports != "80,443" || rows[0].Family != "6" {
		t.Errorf("internet rows = %+v", rows)
	}
}

// A forward of an IPv4 address behind NAT: the entry carries the host port and the jail port, and the pass rule that
// follows the translation is not a second entry.
func TestReachForwardIPv4Only(t *testing.T) {
	in, rc := reachInput(false, api.FirewallRule{Name: "http4", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"any"}, Redirect: &api.Redirect{HostPort: 8080, JailPort: 80}})
	in.Addresses = []netip.Addr{rip("10.20.0.10")}
	rc.addrs = in.Addresses
	r := reachOfInput(t, in, rc)
	if got, want := exposedText(r), "4 tcp 80 via forward host-port 8080"; got != want {
		t.Errorf("exposed = %q, want %q", got, want)
	}
	rows := rowsOf(r, api.ReachInternet)
	if len(rows) != 1 || rows[0].Decision != "forward" || rows[0].HostPort != 8080 || rows[0].Ports != "80" {
		t.Errorf("internet rows = %+v", rows)
	}
	// the forward is in the rules, ahead of the filter rules, with the host port
	if r.Rules[0].Decision != "forward" || r.Rules[0].Ports != "8080->80" {
		t.Errorf("first rule = %+v", r.Rules[0])
	}
}

// A rule with a source restricts the entry: the set shows by name and the entry is marked.
func TestReachSourceRestrictedRule(t *testing.T) {
	in, rc := reachInput(false, api.FirewallRule{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"set:office"}})
	r := reachOfInput(t, in, rc)
	if len(r.Exposed) != 1 {
		t.Fatalf("exposed = %v", exposedText(r))
	}
	e := r.Exposed[0]
	if !e.Restricted || len(e.From) != 1 || e.From[0] != "set:office" || e.Family != "6" || e.Ports != "22" {
		t.Errorf("entry = %+v", e)
	}
}

// Reach from a WireGuard tunnel is a row of its own and is not "exposed to the Internet".
func TestReachTunnelIsNotTheInternet(t *testing.T) {
	in, rc := reachInput(true, api.FirewallRule{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"22"}, From: []string{"2001:db8:99::/64"}})
	r := reachOfInput(t, in, rc)
	// the rule names a source and stands on the uplink as well as on the tunnel: the Internet entry is the restricted one
	if got := exposedText(r); got != "6 tcp 22 via uplink from 2001:db8:99::/64" {
		t.Errorf("exposed = %q", got)
	}
	tun := rowsOf(r, api.ReachTunnel)
	if len(tun) != 1 || tun[0].SourceName != "wg-office" || tun[0].Ports != "22" || !tun[0].Restricted {
		t.Errorf("tunnel rows = %+v", tun)
	}
}

// With a default of deny the jail's link is closed to everything that no rule names: the project's jails, the host and the
// other projects reach nothing, and the rows say what stops them.
func TestReachDefaultDeny(t *testing.T) {
	in, rc := reachInput(false)
	in.DefaultInBy = []string{"web", "default [project]"}
	r := reachOfInput(t, in, rc)
	if len(r.Exposed) != 0 {
		t.Errorf("exposed = %s", exposedText(r))
	}
	if strings.Join(r.DefaultInBy, ",") != "web,default [project]" || len(r.DefaultOutBy) != 0 {
		t.Errorf("the policies that set the defaults: %v / %v", r.DefaultInBy, r.DefaultOutBy)
	}
	for _, src := range []string{api.ReachJails, api.ReachHost, api.ReachOther} {
		rows := rowsOf(r, src)
		if len(rows) != 1 || rows[0].Decision != "none" || rows[0].By == "" {
			t.Errorf("%s rows = %+v", src, rows)
		}
	}
	if r.DefaultIn != "deny" || r.DefaultOut != "allow" {
		t.Errorf("defaults = %s/%s", r.DefaultIn, r.DefaultOut)
	}
	if rows := rowsOf(r, api.ReachOther); !strings.Contains(rows[0].By, "no allow in rule with a source") {
		t.Errorf("other projects: %q", rows[0].By)
	}
	if rows := rowsOf(r, api.ReachHost); !strings.Contains(rows[0].By, "the inbound default is deny (set by web, default [project])") {
		t.Errorf("host: %q", rows[0].By)
	}
}

// With a default of allow (a jail without policy) the project's jails reach every port; the host and the other projects
// are still closed; and the defaults never open the uplink.
func TestReachDefaultAllowOnTheLink(t *testing.T) {
	in, rc := reachInput(false)
	in.DefaultIn, in.Policies = "allow", nil
	r := reachOfInput(t, in, rc)
	jails := rowsOf(r, api.ReachJails)
	if len(jails) == 0 || jails[0].Decision != "pass" || jails[0].Ports != "all" {
		t.Errorf("jails rows = %+v", jails)
	}
	if rows := rowsOf(r, api.ReachHost); rows[0].Decision != "pass" || rows[0].Ports != "all" {
		t.Errorf("host rows = %+v", rows)
	}
	if rows := rowsOf(r, api.ReachOther); rows[0].Decision != "none" {
		t.Errorf("other projects rows = %+v", rows)
	}
	if len(r.Exposed) != 0 || len(rowsOf(r, api.ReachInternet)) != 0 {
		t.Errorf("a default opened the uplink: %s", exposedText(r))
	}
}

// What the jail reaches: everything but the host and the other projects with a default-out of allow.
func TestReachOut(t *testing.T) {
	in, rc := reachInput(false)
	r := reachOfInput(t, in, rc)
	if !strings.HasPrefix(r.Out.Internet, "tcp all ports") {
		t.Errorf("internet = %q", r.Out.Internet)
	}
	// what a jail sends is not closed on its side, also towards other projects (the jail that is reached decides); the host is
	if !strings.HasPrefix(r.Out.OtherProjects, "tcp all ports") || !strings.HasPrefix(r.Out.Host, "nothing") {
		t.Errorf("out = %+v", r.Out)
	}
}

// Two jails of two projects: the exposed entries carry the address of their own jail only.
func TestReachRulesAreInEvaluationOrder(t *testing.T) {
	in, rc := reachInput(false, api.FirewallRule{Name: "site", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"443"}, From: []string{"any"}})
	r := reachOfInput(t, in, rc)
	var uplink, def int = -1, -1
	for i, x := range r.Rules {
		if strings.HasPrefix(x.Where, "uplink") && x.Decision == "pass" && uplink < 0 {
			uplink = i
		}
		if x.Stage == render.StageDefault && x.Direction == "to" {
			def = i
		}
	}
	if uplink < 0 || def < 0 || uplink > def {
		t.Errorf("uplink pass at %d, default at %d", uplink, def)
	}
}

// A deny rule of the uplink that drops everything an allow rule lets in leaves nothing the Internet reaches.
func TestReachUplinkDenyClosesTheEntry(t *testing.T) {
	in, rc := reachInput(false,
		api.FirewallRule{Name: "open", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80", "443"}, From: []string{"any"}},
		api.FirewallRule{Name: "closed", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}},
	)
	r := reachOfInput(t, in, rc)
	if got := exposedText(r); got != "6 tcp 80,443 via uplink" && got != "6 tcp 443 via uplink" {
		t.Fatalf("exposed = %q", got)
	}
	in2, rc2 := reachInput(false,
		api.FirewallRule{Name: "open", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}},
		api.FirewallRule{Name: "closed", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}},
	)
	if got := exposedText(reachOfInput(t, in2, rc2)); got != "" {
		t.Errorf("a port closed by a deny rule is listed: %q", got)
	}
}

// The other projects reach a jail like the Internet does: the row lists each allow in rule with a source (the private IPv4
// address included, there is no NAT exception) with what it is limited to, and a closed row says why.
func TestReachOtherProjectsAreReachedLikeTheInternet(t *testing.T) {
	rule := func(name, action string, from ...string) api.FirewallRule {
		return api.FirewallRule{Name: name, Action: action, Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: from}
	}
	other := func(rules ...api.FirewallRule) []api.ReachRow {
		in, rc := reachInput(false, rules...)
		return rowsOf(reachOfInput(t, in, rc), api.ReachOther)
	}
	// any: every address of the jail, with the rule that decides
	rows := other(rule("world", "allow", "any"))
	fams := ""
	for _, r := range rows {
		if r.Decision != "pass" || r.Ports != "80" || strings.Join(r.From, ",") != "any" || r.Policy != "web" || r.Rule != 1 {
			t.Errorf("from any: %+v", r)
		}
		fams += r.Family
	}
	if len(rows) != 2 || fams != "46" {
		t.Errorf("from any: one row for IPv4 (the private address too) and one for IPv6: %+v", rows)
	}
	// an address inside another project's network, named: the row carries it
	rows = other(rule("one", "allow", "10.31.0.5"))
	if len(rows) != 1 || rows[0].Decision != "pass" || strings.Join(rows[0].From, ",") != "10.31.0.5/32" && strings.Join(rows[0].From, ",") != "10.31.0.5" {
		t.Errorf("a named address: %+v", rows)
	}
	// a rule without a source is for the project's own jails and the host, not for the other projects
	if rows = other(rule("own", "allow")); len(rows) != 1 || rows[0].Decision != "none" {
		t.Errorf("a rule without a source: %+v", rows)
	}
	// a deny rule wins
	rows = other(rule("world", "allow", "any"), rule("no", "deny", "10.31.0.0/24", "2001:db8:2::/64"))
	if len(rows) != 1 || rows[0].Decision != "none" || !strings.Contains(rows[0].By, "policy web") {
		t.Errorf("a deny rule: %+v", rows)
	}
}
