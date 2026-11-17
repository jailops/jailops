package render

import (
	"net/netip"
	"strings"
	"testing"
)

func TestUnboundConf(t *testing.T) {
	a := netip.MustParseAddr
	p := netip.MustParsePrefix
	got := UnboundConf("corp.example",
		[]DNSJail{
			{Project: "vendor", Name: "web", Addrs: []netip.Addr{a("10.77.0.4")}},
			{Project: "vendor", Name: "postgres", Addrs: []netip.Addr{a("2001:db8:1::2"), a("10.77.0.2")}},
			{Project: "shop", Name: "db", Addrs: []netip.Addr{a("10.20.0.10")}},
			{Project: "shop", Name: "cache", Addrs: []netip.Addr{a("2001:db8:2::5")}}, // IPv6 only
		},
		[]DNSNet{
			{Project: "vendor", Subnets: []netip.Prefix{p("10.77.0.0/24"), p("2001:db8:1::/64")}, Gateway: a("10.77.0.1")},
			{Project: "shop", Subnets: []netip.Prefix{p("10.20.0.0/24")}, Gateway: a("10.20.0.1")},
			{Project: "idle", Subnets: []netip.Prefix{p("10.30.0.0/24")}, Gateway: a("10.30.0.1")}, // no jail runs: no view, but the resolver listens and admits the subnet
		},
		[]string{"127.0.0.1", "::1", "10.20.0.1"}) // the administrator's own interface lines: not written twice
	want := Header + `
# The names of the running jails inside their project, for the unbound of the host (docs/dns.md).
view:
	name: "jailops-shop"
	view-first: yes
	local-zone: "shop.corp.example." static
	local-data: "cache.shop.corp.example. AAAA 2001:db8:2::5"
	local-data: "cache.v6.shop.corp.example. AAAA 2001:db8:2::5"
	local-data: "db.shop.corp.example. A 10.20.0.10"
	local-data: "db.v4.shop.corp.example. A 10.20.0.10"
	local-data: "host.shop.corp.example. A 10.20.0.1"
	local-data: "host.v4.shop.corp.example. A 10.20.0.1"
view:
	name: "jailops-vendor"
	view-first: yes
	local-zone: "vendor.corp.example." static
	local-data: "host.v4.vendor.corp.example. A 10.77.0.1"
	local-data: "host.vendor.corp.example. A 10.77.0.1"
	local-data: "postgres.v4.vendor.corp.example. A 10.77.0.2"
	local-data: "postgres.v6.vendor.corp.example. AAAA 2001:db8:1::2"
	local-data: "postgres.vendor.corp.example. A 10.77.0.2"
	local-data: "postgres.vendor.corp.example. AAAA 2001:db8:1::2"
	local-data: "web.v4.vendor.corp.example. A 10.77.0.4"
	local-data: "web.vendor.corp.example. A 10.77.0.4"
server:
	interface: 10.30.0.1
	interface: 10.77.0.1
	ip-transparent: yes
	access-control: 10.20.0.0/24 allow
	access-control-view: 10.20.0.0/24 "jailops-shop"
	access-control: 10.77.0.0/24 allow
	access-control-view: 10.77.0.0/24 "jailops-vendor"
	access-control: 2001:db8:1::/64 allow
	access-control-view: 2001:db8:1::/64 "jailops-vendor"
	access-control: 10.30.0.0/24 allow
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The host's own resolver listens on the loopback. Unbound does that by default only while no `interface:` line exists, and
// the generated file has such lines, so it names the loopback too, unless the administrator's configuration covers it.
func TestUnboundConfKeepsTheLoopback(t *testing.T) {
	a := netip.MustParseAddr
	p := netip.MustParsePrefix
	nets := []DNSNet{{Project: "shop", Subnets: []netip.Prefix{p("10.20.0.0/24")}, Gateway: a("10.20.0.1")}}
	for name, listening := range map[string][]string{"none": nil, "other addresses": {"192.0.2.9"}} {
		got := UnboundConf("internal", nil, nets, listening)
		for _, line := range []string{"\tinterface: 127.0.0.1\n", "\tinterface: ::1\n", "\tinterface: 10.20.0.1\n"} {
			if !strings.Contains(got, line) {
				t.Errorf("%s: missing %q in:\n%s", name, line, got)
			}
		}
	}
	for name, listening := range map[string][]string{"both": {"127.0.0.1", "::1"}, "wildcards": {"0.0.0.0", "::"}} {
		if got := UnboundConf("internal", nil, nets, listening); strings.Contains(got, "interface: 127.0.0.1") || strings.Contains(got, "interface: ::1") {
			t.Errorf("%s: the loopback is named twice:\n%s", name, got)
		}
	}
	if got := UnboundConf("internal", nil, nil, nil); strings.Contains(got, "interface:") {
		t.Errorf("no network: the file leaves unbound as it is:\n%s", got)
	}
}
