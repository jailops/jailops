package service

import (
	"net/netip"
	"strings"
	"testing"
)

func TestQuickPassesBeforeTheAnchorAreFound(t *testing.T) {
	rules := strings.Join([]string{
		"block drop log all",
		"pass in quick on vtnet0 proto tcp from any to (vtnet0) port = 22 flags S/SA keep state",
		"pass out quick on vtnet0 all flags S/SA keep state",
		"pass quick inet proto icmp all keep state",
		"pass quick inet6 proto ipv6-icmp all keep state",
		`anchor "jailops/*" all`,
		"pass quick all",
	}, "\n")
	got := quickPassesBeforeAnchor(rules)
	if len(got) != 2 || !strings.Contains(got[0], "icmp") || !strings.Contains(got[1], "ipv6-icmp") {
		t.Fatalf("%q", got)
	}
	if got := quickPassesBeforeAnchor("pass all\n" + `anchor "jailops/*" all`); len(got) != 0 {
		t.Fatalf("a rule that is not quick was reported: %q", got)
	}
}

func TestDefaultBlockAndStatePolicyLine(t *testing.T) {
	for rules, want := range map[string]bool{
		"block drop log all\npass out all flags S/SA keep state": true,
		"block drop in on vtnet0 all":                            true,
		"block drop in quick on vtnet0 all":                      false,
		"block drop in log quick inet6 from fe80::/10 to any":    false,
		"pass all": false,
	} {
		if got := defaultBlock(rules); got != want {
			t.Errorf("%q: %v", rules, got)
		}
	}
	for conf, want := range map[string]bool{
		"set skip on lo0\nset state-policy if-bound\nblock log all": true,
		"set state-policy \\\n  if-bound":                           true,
		"# set state-policy if-bound\nblock log all":                false,
		"set state-policy floating":                                 false,
	} {
		if got := reStatePolicy.MatchString(stripPFComments(conf)); got != want {
			t.Errorf("%q: %v", conf, got)
		}
	}
}

// The administrator's rules before the anchor that stand in the way of what the policies admit from outside: a quick block
// of every source, and a pass rule for a jail prefix. Rules of the host's own, with a source list, a table or a port, and
// the default block that is not quick, are not.
func TestInboundRulesInTheWayOfTheAnchor(t *testing.T) {
	nets := []inboundNet{
		{name: "pub6", uplink: "vtnet0", subnet: netip.MustParsePrefix("2001:db8:1:2:100::/80")},
		{name: "pub4", uplink: "vtnet0", subnet: netip.MustParsePrefix("198.51.100.0/28")},
	}
	rules := strings.Join([]string{
		"block drop log all", // not quick
		"block drop in quick on vtnet0 inet from 10.0.0.0/8 to any",
		"block drop in quick on vtnet0 inet6 from <bridges_v6> to any",
		"pass in quick on vtnet0 proto tcp from <office> to (vtnet0) port = 2282 flags S/SA keep state (if-bound)",
		"block drop in quick on vtnet0 inet6 all",
		"block drop in quick on em9 all", // another interface
		"block drop out quick on vtnet0 all",
		"block drop in quick on vtnet0 inet6 proto tcp from any to any port = 25",
		"pass in quick on vtnet0 inet6 proto tcp from <office> to 2001:db8:1:2:100::/80 port = 2282 flags S/SA keep state (if-bound)",
		`anchor "jailops/*" all`,
		"block drop in quick on vtnet0 all", // after the anchor: not in the way
	}, "\n")
	blocks, passes := inboundRulesInTheWay(rules, nets)
	if len(blocks) != 1 || !strings.Contains(blocks[0], "block drop in quick on vtnet0 inet6 all") || !strings.Contains(blocks[0], "pub6") {
		t.Errorf("blocks: %q", blocks)
	}
	if len(passes) != 1 || !strings.Contains(passes[0], "2001:db8:1:2:100::/80") {
		t.Errorf("passes: %q", passes)
	}
	// the same host with the anchor above the block: nothing in the way
	b2, p2 := inboundRulesInTheWay(`anchor "jailops/*" all`+"\n"+rules, nets)
	if len(b2) != 0 || len(p2) != 0 {
		t.Errorf("rules after the anchor: %q %q", b2, p2)
	}
}

// The host's pf.conf is the door for what the anchors do not open: a default block of everything inbound on the uplink
// is ok, quick or not, on the uplink, on no interface or on all but another; no block, a block for one family or source,
// or a pass in all after the block is open.
func TestHostDefaultDenyOnTheUplink(t *testing.T) {
	anchor := `anchor "jailops/*" all`
	for rules, open := range map[string]bool{
		"block drop all\npass out all flags S/SA keep state\n" + anchor: false,
		"block return in log all\n" + anchor:                            false,
		"block drop in on vtnet0 all\n" + anchor:                        false,
		"block drop in on ! lo0 all\n" + anchor:                         false,
		"block drop in quick on egress all\n" + anchor:                  false,
		anchor + "\nblock drop in all":                                  false,
		anchor:                                                          true,
		"pass in quick on vtnet0 proto tcp from any to (vtnet0) port = 22 flags S/SA keep state\n" + anchor: true,
		"block drop in on em1 all\n" + anchor:                          true,
		"block drop in on ! vtnet0 all\n" + anchor:                     true,
		"block drop in inet6 from fe80::/10 to any\n" + anchor:         true,
		"block drop in proto tcp all\n" + anchor:                       true,
		"block drop all\npass in all flags S/SA keep state\n" + anchor: true,
		"pass in quick all\nblock drop all\n" + anchor:                 true,
		"block drop out all\n" + anchor:                                true,
		// each address family on its own: an IPv4-only block leaves the routed IPv6 open
		"block drop in inet all\n" + anchor:                               true,
		"block drop in inet6 all\nblock drop in inet all\n" + anchor:      false,
		"block in all\npass in inet all\n" + anchor:                       true,
		"block in all\npass in inet6 proto tcp to any port 22\n" + anchor: false,
	} {
		got := openUplinks(rules, []string{"vtnet0"})
		if (len(got) == 1) != open {
			t.Errorf("%q: open %v, want %v", rules, got, open)
		}
	}
	if got := openUplinks("block drop in inet all\n", []string{"vtnet0"}); len(got) != 1 || got[0] != "vtnet0 (IPv6)" {
		t.Errorf("the family that is open is named: %q", got)
	}
}

func TestPFStatesLimitIsRead(t *testing.T) {
	out := "states        hard limit   100000\nsrc-nodes     hard limit    10000\nframes hard limit 5000\n"
	if n, ok := pfStatesLimit(out); !ok || n != 100000 {
		t.Fatalf("%d %v", n, ok)
	}
	for _, bad := range []string{"", "states hard limit x", "No ALTQ support in kernel\n"} {
		if _, ok := pfStatesLimit(bad); ok {
			t.Errorf("%q read as a limit", bad)
		}
	}
}
