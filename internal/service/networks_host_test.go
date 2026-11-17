package service

import (
	"net/netip"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// A host on a shared segment holds its address as a /128; the provider's router answers neighbor discovery for the
// whole /64. A proxy network inside that /64 is accepted, one outside it is not.
func TestProxySubnetInsideTheSegmentOfAHostAddressOf128(t *testing.T) {
	s := newSvc(t)
	f := ifconfig.NewFake(ifconfig.Options{})
	f.AddIface(ifconfig.Iface{Name: "vtnet0", Flags: []string{"UP"}, Inet6: []netip.Prefix{netip.MustParsePrefix("2001:db8:103:211::1/128")}})
	s.d.Net = f
	spec := func(subnet string) api.NetworkSpec {
		return api.NetworkSpec{Uplink: "vtnet0", IPv6: &api.IPv6Net{Mode: api.IPv6Proxy, Subnet: subnet}}
	}
	if err := s.checkNetworkHost(bg, spec("2001:db8:103:211:100::/80")); err != nil {
		t.Fatalf("a subnet inside the /64 of the host address: %v", err)
	}
	err := s.checkNetworkHost(bg, spec("2001:db8:103:212:100::/80"))
	if err == nil || !strings.Contains(err.Error(), "2001:db8:103:211::/64") {
		t.Fatalf("a subnet outside the /64 must be refused and name the segment: %v", err)
	}
}
