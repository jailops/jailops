package validate

import (
	"net/netip"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/sdk/api"
)

// NetworkSpec validates a network definition.
func NetworkSpec(s api.NetworkSpec) error {
	if s.Bridge != "" {
		if err := IfName("bridge", s.Bridge); err != nil {
			return err
		}
		if len(s.Bridge) > ipam.MaxBridgeName {
			return Invalid("bridge", "bridge name %q is longer than %d characters (the gateway interface is derived from it)", s.Bridge, ipam.MaxBridgeName)
		}
	}
	if s.Uplink != "" {
		if err := IfName("uplink", s.Uplink); err != nil {
			return err
		}
		if s.Uplink == s.Bridge {
			return Invalid("uplink", "uplink must not be the bridge itself")
		}
		if ipam.IsEpairName(s.Uplink) {
			return Invalid("uplink", "uplink %q looks like a jail interface (a bridge member)", s.Uplink)
		}
	}
	if s.MTU != 0 && (s.MTU < 576 || s.MTU > 9216) {
		return Invalid("mtu", "mtu must be 0 or between 576 and 9216")
	}
	if len(s.DNS) > 3 {
		return Invalid("dns", "at most 3 DNS servers are used by resolvers")
	}
	for _, d := range s.DNS {
		a, err := addr("dns", d)
		if err != nil {
			return err
		}
		if a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() {
			return Invalid("dns", "dns %q is not a usable unicast address", d)
		}
	}
	if s.IPv4 == nil && (s.IPv6 == nil || s.IPv6.Mode == api.IPv6None) {
		return Invalid("ipv4", "a network needs at least one address family")
	}
	if s.IPv4 != nil {
		if err := ipv4(s); err != nil {
			return err
		}
	}
	if s.IPv6 != nil {
		if err := ipv6(s); err != nil {
			return err
		}
		if s.IPv6.Mode != api.IPv6None && s.MTU != 0 && s.MTU < 1280 {
			return Invalid("mtu", "IPv6 needs an MTU of at least 1280")
		}
	}
	return nil
}

// NetworkMembers validates the spec and additionally checks the observed
// member list of the bridge: the uplink must not be one of them.
func NetworkMembers(s api.NetworkSpec, members []string) error {
	if err := NetworkSpec(s); err != nil {
		return err
	}
	for _, m := range members {
		if s.Uplink != "" && m == s.Uplink {
			return Invalid("uplink", "uplink %q is a member of bridge %q", s.Uplink, s.Bridge).
				WithHint("remove the uplink from the bridge; the uplink is never bridged")
		}
	}
	return nil
}

func ipv4(s api.NetworkSpec) error {
	n := s.IPv4
	p, err := netip.ParsePrefix(n.Subnet)
	if err != nil || !p.Addr().Is4() || p.Addr().Zone() != "" {
		return Invalid("ipv4.subnet", "ipv4.subnet %q is not an IPv4 CIDR", n.Subnet)
	}
	if p != p.Masked() {
		return Invalid("ipv4.subnet", "ipv4.subnet %q has host bits set (want %s)", n.Subnet, p.Masked())
	}
	if p.Bits() < 8 || p.Bits() > 30 {
		return Invalid("ipv4.subnet", "ipv4.subnet prefix length must be between 8 and 30")
	}
	if p.Addr().IsMulticast() || p.Addr().IsLoopback() {
		return Invalid("ipv4.subnet", "ipv4.subnet %q is not a unicast network", n.Subnet)
	}
	gw, err := addr("ipv4.gateway", n.Gateway)
	if err != nil || !gw.Is4() {
		return Invalid("ipv4.gateway", "ipv4.gateway %q is not an IPv4 address", n.Gateway)
	}
	if !p.Contains(gw) {
		return Invalid("ipv4.gateway", "ipv4.gateway %s is outside %s", gw, p)
	}
	if gw == p.Addr() || gw == lastAddr(p) {
		return Invalid("ipv4.gateway", "ipv4.gateway %s is the network or broadcast address", gw)
	}
	r, err := ipam.ParseRange(n.Range)
	if err != nil {
		return Invalid("ipv4.range", "ipv4.range %q must be \"first-last\" with IPv4 addresses", n.Range)
	}
	if !r.First.Is4() || !p.Contains(r.First) || !p.Contains(r.Last) {
		return Invalid("ipv4.range", "ipv4.range %q is outside %s", n.Range, p)
	}
	if r.Contains(gw) {
		return Invalid("ipv4.range", "ipv4.range %q contains the gateway %s", n.Range, gw)
	}
	if err := exclusions("ipv4.exclude", n.Exclude, p); err != nil {
		return err
	}
	if n.NAT && s.Uplink == "" {
		return Invalid("ipv4.nat", "ipv4.nat needs an uplink")
	}
	return nil
}

func exclusions(field string, list []string, p netip.Prefix) error {
	ex, err := ipam.ParseExclusions(list)
	if err != nil {
		return Invalid(field, "%v", err.(*api.Error).Message)
	}
	for _, e := range ex {
		if e.First.BitLen() != p.Addr().BitLen() || !p.Contains(e.First) || !p.Contains(e.Last) {
			return Invalid(field, "exclusion %s-%s is outside %s", e.First, e.Last, p)
		}
	}
	return nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

func ipv6(s api.NetworkSpec) error {
	n := s.IPv6
	switch n.Mode {
	case api.IPv6None:
		return nil
	case api.IPv6Routed, api.IPv6Proxy:
	default:
		return Invalid("ipv6.mode", "ipv6.mode %q must be routed, proxy or none", n.Mode)
	}
	p, err := netip.ParsePrefix(n.Subnet)
	if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() || p.Addr().Zone() != "" {
		return Invalid("ipv6.subnet", "ipv6.subnet %q is not an IPv6 CIDR", n.Subnet)
	}
	if p != p.Masked() {
		return Invalid("ipv6.subnet", "ipv6.subnet %q has host bits set (want %s)", n.Subnet, p.Masked())
	}
	// What the proxy cannot serve is refused here, not when the first jail starts: the helper approves prefixes of
	// /48 to /120, and a proxy-mode subnet lies inside one.
	lo := ipam.RoutedSubnetMin6
	if n.Mode == api.IPv6Proxy {
		lo = ipam.ProxySubnetMin6
	}
	if p.Bits() < lo || p.Bits() > ipam.SubnetMax6 {
		msg := "ipv6.subnet prefix length must be between %d and %d"
		if n.Mode == api.IPv6Proxy {
			msg += " in proxy mode: the helper approves proxied prefixes of /%d to /%d only (ndp-proxy in priv.conf), and the subnet must lie inside one"
			return Invalid("ipv6.subnet", msg, lo, ipam.SubnetMax6, ipam.ProxyApproveMin6, ipam.ProxyApproveMax6)
		}
		return Invalid("ipv6.subnet", msg, lo, ipam.SubnetMax6)
	}
	if p.Addr().IsLinkLocalUnicast() || p.Addr().IsMulticast() || p.Addr().IsLoopback() {
		return Invalid("ipv6.subnet", "ipv6.subnet %q is not a global or unique-local network", n.Subnet)
	}
	gw, err := addr("ipv6.gateway", n.Gateway)
	if err != nil || !gw.Is6() {
		return Invalid("ipv6.gateway", "ipv6.gateway %q is not an IPv6 address", n.Gateway)
	}
	linkLocal := gw.IsLinkLocalUnicast()
	if !linkLocal {
		if !p.Contains(gw) {
			return Invalid("ipv6.gateway", "ipv6.gateway %s is outside %s (only a link-local gateway may be)", gw, p)
		}
		if gw == p.Addr() {
			return Invalid("ipv6.gateway", "ipv6.gateway %s is the network address", gw)
		}
	}
	r, err := ipam.ParseRange(n.Range)
	if err != nil || r.First.Is4() {
		return Invalid("ipv6.range", "ipv6.range %q must be \"first-last\" with IPv6 addresses", n.Range)
	}
	if !p.Contains(r.First) || !p.Contains(r.Last) {
		return Invalid("ipv6.range", "ipv6.range %q is outside %s", n.Range, p)
	}
	if r.Contains(gw) {
		return Invalid("ipv6.range", "ipv6.range %q contains the gateway %s", n.Range, gw)
	}
	if err := exclusions("ipv6.exclude", n.Exclude, p); err != nil {
		return err
	}
	if n.Mode == api.IPv6Proxy {
		if !linkLocal {
			return Invalid("ipv6.gateway", "in proxy mode the gateway must be the bridge's link-local address: the bridge must not hold an on-link copy of the upstream prefix %s", p).
				WithHint("use a gateway such as fe80::1")
		}
		if s.Uplink == "" {
			return Invalid("uplink", "ipv6 proxy mode needs an uplink to answer neighbor solicitations on")
		}
		if s.Uplink == s.Bridge || ipam.IsEpairName(s.Uplink) || strings.HasPrefix(s.Uplink, "bridge") {
			return Invalid("uplink", "in proxy mode the uplink %q must not be a bridge or a bridge member", s.Uplink)
		}
	}
	return nil
}
