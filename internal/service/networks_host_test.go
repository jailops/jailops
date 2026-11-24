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
		return api.NetworkSpec{Uplink: "vtnet0", IPv6: &api.IPv6Net{NDPProxy: true, Subnet: subnet}}
	}
	if err := s.checkNetworkHost(bg, spec("2001:db8:103:211:100::/80")); err != nil {
		t.Fatalf("a subnet inside the /64 of the host address: %v", err)
	}
	err := s.checkNetworkHost(bg, spec("2001:db8:103:212:100::/80"))
	if err == nil || !strings.Contains(err.Error(), "2001:db8:103:211::/64") {
		t.Fatalf("a subnet outside the /64 must be refused and name the segment: %v", err)
	}
	err = s.checkNetworkHost(bg, spec("2001:db8:103:211::/64"))
	if err == nil || !strings.Contains(err.Error(), "holds the host's own address 2001:db8:103:211::1") {
		t.Fatalf("a subnet that holds the host's own address must be refused at once: %v", err)
	}
}

// The resolver address of the subnet, which the host holds on its loopback for the jails, is not the host's own
// address in the sense of the NDP proxy; any other address of the host inside the subnet still is.
func TestProxySubnetMayHoldTheResolverAddressOnTheLoopback(t *testing.T) {
	s := newSvc(t)
	f := ifconfig.NewFake(ifconfig.Options{})
	f.AddIface(ifconfig.Iface{Name: "vtnet0", Flags: []string{"UP"}, Inet6: []netip.Prefix{netip.MustParsePrefix("2001:db8:103:211::1/128")}})
	f.AddIface(ifconfig.Iface{Name: "lo1", Groups: []string{"lo"}, Flags: []string{"UP", "LOOPBACK"}, Inet6: []netip.Prefix{netip.MustParsePrefix("2001:db8:103:211:100::1/128")}})
	s.d.Net = f
	spec := api.NetworkSpec{Uplink: "vtnet0", IPv6: &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:103:211:100::/80"}}
	if err := s.checkNetworkHost(bg, spec); err != nil {
		t.Fatalf("the resolver address on the loopback must not refuse the subnet: %v", err)
	}
	f.AddIface(ifconfig.Iface{Name: "lo2", Groups: []string{"lo"}, Flags: []string{"UP", "LOOPBACK"}, Inet6: []netip.Prefix{netip.MustParsePrefix("2001:db8:103:211:100::7/128")}})
	err := s.checkNetworkHost(bg, spec)
	if err == nil || !strings.Contains(err.Error(), "holds the host's own address 2001:db8:103:211:100::7") {
		t.Fatalf("another address of the host inside the subnet must be refused: %v", err)
	}
}

// The resolver address a deleted network with the same subnet left on the loopback does not refuse a routed network:
// it is the address the new network holds there itself. Another address of the subnet on the host still does.
func TestRoutedSubnetMayFindItsResolverAddressOnTheLoopback(t *testing.T) {
	s := newSvc(t)
	f := ifconfig.NewFake(ifconfig.Options{})
	f.AddIface(ifconfig.Iface{Name: "lo0", Groups: []string{"lo"}, Flags: []string{"UP", "LOOPBACK"}, Inet6: []netip.Prefix{netip.MustParsePrefix("fd91:1::1/128")}})
	s.d.Net = f
	spec := api.NetworkSpec{IPv6: &api.IPv6Net{Subnet: "fd91:1::/64"}}
	if err := s.checkNetworkHost(bg, spec); err != nil {
		t.Fatalf("the left resolver address must not refuse the subnet: %v", err)
	}
	f.AddIface(ifconfig.Iface{Name: "lo1", Groups: []string{"lo"}, Flags: []string{"UP", "LOOPBACK"}, Inet6: []netip.Prefix{netip.MustParsePrefix("fd91:1::5/128")}})
	if err := s.checkNetworkHost(bg, spec); err == nil || !strings.Contains(err.Error(), "overlaps fd91:1::5/128") {
		t.Fatalf("another address of the subnet must be refused: %v", err)
	}
}
