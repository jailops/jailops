package fwsim

import (
	"net/netip"
	"testing"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

func uplinkInput(rules ...api.FirewallRule) render.PFInput {
	return render.PFInput{
		Jail: "alpha_web", Interfaces: []string{"jo0123456789a0a"},
		Addresses: []netip.Addr{netip.MustParseAddr("2001:db8:1::100")},
		Uplink:    "em0",
		UplinkOf:  map[netip.Addr]render.Uplink{netip.MustParseAddr("2001:db8:1::100"): {Iface: "em0", Routed: true}},
		OwnNets:   []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64")},
		Antispoof: true, DefaultIn: "allow", DefaultOut: "allow",
		Policies: []render.PFPolicy{{Name: "web", Policy: api.FirewallPolicySpec{Rules: rules}}},
	}
}

// What the simulator sees on the uplink agrees with the text: a flow the policies admit passes there, anything else is
// not matched (the hop is a block for the caller).
func TestUplinkHook(t *testing.T) {
	in := uplinkInput(api.FirewallRule{Name: "ssh", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"2282"}, From: []string{"2001:db8:aaaa::/48"}})
	an, err := render.BuildAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	hook := Hook{Dir: "in", Iface: "em0"}
	pkt := func(src string, port int) Packet {
		return Packet{Family: "inet6", Proto: "tcp", Src: netip.MustParseAddr(src), Dst: netip.MustParseAddr("2001:db8:1::100"), DstPort: port}
	}
	if r := Evaluate(an, pkt("2001:db8:aaaa::1", 2282), hook); r.Verdict != Pass {
		t.Errorf("the allowed source: %+v", r)
	}
	if r := Evaluate(an, pkt("2001:db8:aaaa::1", 2283), hook); !r.NoMatch {
		t.Errorf("another port: %+v", r)
	}
	if r := Evaluate(an, pkt("2001:db8:bbbb::1", 2282), hook); !r.NoMatch {
		t.Errorf("another source: %+v", r)
	}
	if r := Evaluate(an, pkt("2001:db8:1::50", 2282), hook); r.Verdict != Block {
		t.Errorf("a forged source from the jail's own network: %+v", r)
	}
	// the epair hook does not meet the uplink rules
	if r := Evaluate(an, pkt("2001:db8:aaaa::1", 2282), Hook{Dir: "out"}); r.Rule != nil && r.Rule.Iface != "" {
		t.Errorf("the epair hook decided by an uplink rule: %+v", r.Rule)
	}
}
