package service

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// checkNetworkHost checks a network against the host's interfaces when it is created or changed: the uplink must be
// an interface that can carry the network's traffic, and the subnets must not be in use on the host already. Both
// would be found only when the first jail started, or never: a typo in the uplink renders a NAT rule on a missing
// interface, and a subnet of the host's own LAN breaks the host's routing. When the interfaces cannot be listed (a
// host without the network client) nothing is refused here; the doctor reports what it can.
func (s *Service) checkNetworkHost(ctx context.Context, spec api.NetworkSpec) error {
	if s.d.Net == nil {
		return nil
	}
	ifs, err := s.d.Net.List(ctx)
	if err != nil {
		return nil
	}
	proxy := spec.IPv6 != nil && spec.IPv6.NDPProxy
	if spec.Uplink != "" {
		p := uplinkProblem(spec.Uplink, ifs)
		if !proxy {
			p = routedUplinkProblem(spec.Uplink, ifs)
		}
		if p != "" {
			hint := "name the host interface the network's traffic leaves through with --uplink"
			if c := uplinkCandidates(ifs, proxy); len(c) > 0 {
				hint += "; candidates on this host: " + strings.Join(c, ", ")
			}
			return api.Errorf(api.CodeInvalid, "uplink %s cannot be used: %s", spec.Uplink, p).WithDetail("field", "uplink").WithHint(hint)
		}
	}
	var subnets []netip.Prefix
	if spec.IPv4 != nil {
		if p, err := netip.ParsePrefix(spec.IPv4.Subnet); err == nil {
			subnets = append(subnets, p.Masked())
		}
	}
	var v6 netip.Prefix
	if spec.IPv6 != nil {
		if p, err := netip.ParsePrefix(spec.IPv6.Subnet); err == nil {
			v6 = p.Masked()
			if !proxy {
				subnets = append(subnets, v6)
			}
		}
	}
	for _, i := range ifs {
		if hostIfaceIsNetwork(i) {
			continue
		}
		for _, have := range slices.Concat(i.Inet, i.Inet6) {
			if have.Addr().IsLinkLocalUnicast() {
				continue
			}
			for _, sub := range subnets {
				if sub.Overlaps(have.Masked()) {
					return api.Errorf(api.CodePreconditionFailed, "subnet %s overlaps %s on the host interface %s", sub, have, i.Name).
						WithDetail("field", subnetField(sub)).
						WithHint("the host routes that prefix through " + i.Name + " already; choose a subnet the host does not use (ifconfig lists the host's addresses)")
				}
			}
		}
	}
	if proxy && v6.IsValid() {
		// the proxy never answers for an address of the host itself, and the helper refuses such a prefix when the first
		// jail starts: say it here, where the network is made
		for _, i := range ifs {
			if hostIfaceIsNetwork(i) {
				continue
			}
			for _, h := range i.Inet6 {
				// the resolver address of the network, which the host holds on its loopback for the jails
				if h.Addr() == ipam.Resolver6(v6) && slices.Contains(i.Groups, "lo") {
					continue
				}
				if !h.Addr().IsLinkLocalUnicast() && v6.Contains(h.Addr()) {
					return api.Errorf(api.CodeInvalid, "with the NDP proxy the subnet %s holds the host's own address %s on %s", v6, h.Addr(), i.Name).
						WithDetail("field", "ipv6.subnet").
						WithHint("take a part of the segment's prefix that does not hold the host's own address, such as a /80 elsewhere in the /64")
				}
			}
		}
		var up *ifconfig.Iface
		for k := range ifs {
			if ifs[k].Name == spec.Uplink {
				up = &ifs[k]
			}
		}
		if up != nil && !slices.ContainsFunc(up.Inet6, func(p netip.Prefix) bool {
			if p.Addr().IsLinkLocalUnicast() {
				return false
			}
			seg := segmentPrefix(p)
			return seg.Bits() <= v6.Bits() && seg.Contains(v6.Addr())
		}) {
			var have []string
			for _, p := range up.Inet6 {
				if !p.Addr().IsLinkLocalUnicast() {
					have = append(have, segmentPrefix(p).String())
				}
			}
			on := "no global IPv6 prefix"
			if len(have) > 0 {
				on = "only " + strings.Join(have, ", ")
			}
			return api.Errorf(api.CodeInvalid, "with the NDP proxy the subnet %s must lie inside the prefix of the uplink's segment, and %s has %s", v6, spec.Uplink, on).
				WithDetail("field", "ipv6.subnet").
				WithHint("the NDP proxy answers for addresses of the segment the uplink is on: take a part of that prefix (a host address of /128 stands for the /64 around it); or switch the NDP proxy off (jailops network ndp-proxy disable) for a prefix routed to this host")
		}
	}
	return nil
}

// segmentPrefix is the prefix of the segment an address of an interface sits on. A host on a shared segment usually
// holds its address as a /128 (the provider's router answers neighbor discovery for the whole /64), so an address
// with a prefix longer than /64 stands for the /64 around it; a prefix of /64 or shorter is the segment's own.
func segmentPrefix(p netip.Prefix) netip.Prefix {
	if p.Bits() > 64 {
		return netip.PrefixFrom(p.Addr(), 64).Masked()
	}
	return p.Masked()
}

func subnetField(p netip.Prefix) string {
	if p.Addr().Is4() {
		return "ipv4.subnet"
	}
	return "ipv6.subnet"
}

// hostIfaceIsNetwork reports whether an interface is JailOps's, such as the host end of a jail's epair: its addresses
// are a network's, which the comparison of network specs covers. A WireGuard interface of
// JailOps is not: its addresses are routed by the host like any other interface's.
func hostIfaceIsNetwork(i ifconfig.Iface) bool {
	return render.IsJailOpsDesc(i.Description) && !strings.HasPrefix(i.Name, "wg")
}

// routedUplinkProblem says why an interface cannot be the uplink of a network that routes (NAT, a routed IPv6
// prefix, port forwards), or "" when it can: it must exist and be no loopback, bridge, bridge member or interface of
// JailOps (the end of a jail's epair). A vlan(4), a lagg(4), a tunnel or an epair of the administrator's own
// that leads into another vnet is fine; the proxy's stricter rule is uplinkProblem.
func routedUplinkProblem(uplink string, ifs []ifconfig.Iface) string {
	var up *ifconfig.Iface
	for i := range ifs {
		if ifs[i].Name == uplink {
			up = &ifs[i]
		}
		for _, m := range ifs[i].Members {
			if m.Name == uplink {
				return fmt.Sprintf("%s is a member of bridge %s", uplink, ifs[i].Name)
			}
		}
	}
	if up == nil {
		return uplink + " does not exist on this host"
	}
	for _, g := range []string{"lo", "bridge"} {
		if slices.Contains(up.Groups, g) {
			return fmt.Sprintf("%s is a %s interface", uplink, g)
		}
	}
	if slices.Contains(up.Flags, "LOOPBACK") {
		return uplink + " is a loopback interface"
	}
	if ipam.IsEpairName(uplink) || render.IsJailOpsDesc(up.Description) {
		return uplink + " is an interface of JailOps (" + orNone(up.Description) + ")"
	}
	return ""
}

// uplinkCandidates lists the interfaces that could be an uplink, with their addresses, for a hint.
func uplinkCandidates(ifs []ifconfig.Iface, proxy bool) []string {
	var out []string
	for _, i := range ifs {
		p := routedUplinkProblem(i.Name, ifs)
		if proxy {
			p = uplinkProblem(i.Name, ifs)
		}
		if p != "" || render.IsJailOpsDesc(i.Description) {
			continue
		}
		var a []string
		for _, x := range slices.Concat(i.Inet, i.Inet6) {
			if !x.Addr().IsLinkLocalUnicast() {
				a = append(a, x.String())
			}
		}
		if len(a) > 0 {
			out = append(out, i.Name+" ("+strings.Join(a, ", ")+")")
		} else {
			out = append(out, i.Name)
		}
	}
	return out
}

// forwardingWarnings says which forwarding sysctl a network needs that is off. A network that routes (NAT, an uplink,
// IPv6) is created and shown as not ready with the reason Precondition until it is on; this says so at once.
func (s *Service) forwardingWarnings(ctx context.Context, spec api.NetworkSpec) []string {
	v6 := spec.IPv6 != nil
	if !(spec.IPv4 != nil && spec.IPv4.NAT) && !v6 && spec.Uplink == "" {
		return nil
	}
	var out []string
	if spec.IPv4 != nil {
		if v, err := s.sysctl(ctx, "net.inet.ip.forwarding"); err == nil && v == "0" {
			out = append(out, "IPv4 forwarding is off (net.inet.ip.forwarding is 0): the network stays not ready and its jails reach nothing beyond the host; run `sysctl net.inet.ip.forwarding=1` and `sysrc gateway_enable=YES`")
		}
	}
	if v6 {
		if v, err := s.sysctl(ctx, "net.inet6.ip6.forwarding"); err == nil && v == "0" {
			out = append(out, "IPv6 forwarding is off (net.inet6.ip6.forwarding is 0): the network stays not ready and its jails reach nothing beyond the host; run `sysctl net.inet6.ip6.forwarding=1` and `sysrc ipv6_gateway_enable=YES`")
		}
	}
	return out
}

// hostPrefixes lists the IPv4 prefixes on the host's own interfaces (not those of JailOps networks), for the choice of
// a free subnet.
func (s *Service) hostPrefixes(ctx context.Context) []netip.Prefix {
	if s.d.Net == nil {
		return nil
	}
	ifs, err := s.d.Net.List(ctx)
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, i := range ifs {
		if !hostIfaceIsNetwork(i) {
			for _, p := range i.Inet {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "no description"
	}
	return s
}
