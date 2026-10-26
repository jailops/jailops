package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// virtualGroups are interface groups that cannot be the uplink of the proxy: it needs a real Ethernet segment (a
// physical interface or a vlan(4) on one).
var virtualGroups = []string{"lo", "epair", "bridge", "tun", "tap", "gif", "gre", "wg", "pflog", "pfsync", "enc", "ipsec", "lagg"}

// uplinkProblem says why an interface cannot be the uplink of an NDP proxy, or "" when it can. The proxy captures
// and sends untagged Ethernet frames on the interface it is given, so it must be the interface the upstream segment
// is on: a physical interface that carries no vlan(4) children, or a vlan(4) interface itself. The physical parent
// of a vlan is not: it would see and send the frames of the untagged segment only.
func uplinkProblem(uplink string, ifs []ifconfig.Iface) string {
	var up *ifconfig.Iface
	var children []string
	for i := range ifs {
		if ifs[i].Name == uplink {
			up = &ifs[i]
		}
		if ifs[i].VlanParent == uplink {
			children = append(children, ifs[i].Name)
		}
		for _, m := range ifs[i].Members {
			if m.Name == uplink {
				return fmt.Sprintf("%s is a member of bridge %s", uplink, ifs[i].Name)
			}
		}
	}
	if up == nil {
		return uplink + " does not exist"
	}
	for _, g := range up.Groups {
		if slices.Contains(virtualGroups, g) {
			return fmt.Sprintf("%s is a %s interface, not an Ethernet segment", uplink, g)
		}
	}
	if len(children) > 0 {
		slices.Sort(children)
		return fmt.Sprintf("%s is the parent of vlan interface(s) %s; use the vlan interface of the segment the addresses are on as the uplink", uplink, strings.Join(children, ", "))
	}
	return ""
}

// doctorNDPUplink checks the uplink of every proxy-mode network.
func (s *Service) doctorNDPUplink(ctx context.Context, c *checkSet) {
	if s.d.Net == nil {
		return
	}
	nets, _ := s.netKind.list()
	var uplinks []string
	for _, o := range nets {
		if o.Spec.IPv6 != nil && o.Spec.IPv6.Mode == api.IPv6Proxy && !slices.Contains(uplinks, o.Spec.Uplink) {
			uplinks = append(uplinks, o.Spec.Uplink)
		}
	}
	if len(uplinks) == 0 {
		return
	}
	ifs, err := s.d.Net.List(ctx)
	if err != nil {
		c.add("network.uplink", api.DoctorUnknown, "the interfaces cannot be listed: "+err.Error(), "")
		return
	}
	var bad []string
	for _, u := range uplinks {
		if p := uplinkProblem(u, ifs); p != "" {
			bad = append(bad, p)
		}
	}
	if len(bad) > 0 {
		c.add("network.uplink", api.DoctorFail, "the uplink of an IPv6 proxy network is unsuitable: "+strings.Join(bad, "; "),
			"the proxy needs a physical interface without vlan children, or a vlan(4) interface (docs/networking-ipv6.md)")
		return
	}
	c.add("network.uplink", api.DoctorOK, "the uplink of every IPv6 proxy network is a physical or vlan interface", "")
}

// hasScrub reports whether the loaded main ruleset (pfctl -s rules) has a scrub rule: "scrub in all fragment
// reassemble" as printed on FreeBSD 15.1, or a match rule with scrub options. Without it packets are not normalised
// (fragments are not reassembled and MSS is not clamped), which matters for jails behind a tunnel or a smaller MTU.
func hasScrub(rules string) bool {
	for _, ln := range strings.Split(rules, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "scrub ") || strings.HasPrefix(ln, "match ") && strings.Contains(ln, "scrub") {
			return true
		}
	}
	return false
}

// gatewaysWithoutStaticARP lists the gateway interfaces of networks with an IPv4 subnet that exist but do not have
// the staticarp flag: the host would ask jails for addresses they gave themselves. The flag is set when the first jail
// of the network starts after this version was installed.
func gatewaysWithoutStaticARP(ifs []ifconfig.Iface, bridges []string) []string {
	var out []string
	for _, br := range bridges {
		gw := ipam.GatewayIface(br)
		for _, i := range ifs {
			if i.Name == gw && !slices.Contains(i.Flags, "STATICARP") {
				out = append(out, gw)
			}
		}
	}
	return out
}

// doctorStaticARP warns about gateway interfaces that still resolve addresses with ARP while a jail runs on them.
func (s *Service) doctorStaticARP(ctx context.Context, c *checkSet) {
	if s.d.Net == nil {
		return
	}
	nets, _ := s.netKind.list()
	used := map[string]bool{}
	s.mu.RLock()
	for _, e := range s.jails {
		for _, a := range e.jail.Spec.Network {
			used[a.Network] = true
		}
	}
	s.mu.RUnlock()
	var bridges []string
	for _, o := range nets {
		if o.Spec.IPv4 != nil && used[o.Meta.Name] {
			bridges = append(bridges, o.Spec.Bridge)
		}
	}
	if len(bridges) == 0 {
		return
	}
	ifs, err := s.d.Net.List(ctx)
	if err != nil {
		return
	}
	if bad := gatewaysWithoutStaticARP(ifs, bridges); len(bad) > 0 {
		c.add("network.staticarp", api.DoctorWarn, "the gateway interface(s) "+strings.Join(bad, ", ")+" still resolve addresses with ARP: a jail started before this version has its old addressing",
			"restart the jails of those networks (jailops restart) so they get /32 addresses, pinned MAC addresses and the host's static ARP entries")
		return
	}
	c.add("network.staticarp", api.DoctorOK, "the gateway interfaces of networks in use only use static ARP entries", "")
}

// networksWithoutDNS names the networks that have no nameserver although jails use them: the jails' resolv.conf is
// empty and they cannot resolve names (the flag form of `network create` has no dns option; use a file or `network edit`).
func networksWithoutDNS(nets map[string][]string, used map[string]bool) []string {
	var out []string
	for name, dns := range nets {
		if len(dns) == 0 && used[name] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Service) doctorDNS(c *checkSet) {
	nets, _ := s.netKind.list()
	used := map[string]bool{}
	s.mu.RLock()
	for _, e := range s.jails {
		for _, a := range e.jail.Spec.Network {
			used[a.Network] = true
		}
	}
	s.mu.RUnlock()
	m := map[string][]string{}
	for _, o := range nets {
		m[o.Meta.Name] = o.Spec.DNS
	}
	if bad := networksWithoutDNS(m, used); len(bad) > 0 {
		c.add("network.dns", api.DoctorWarn, "no nameserver on network(s) "+strings.Join(bad, ", ")+": jails there have an empty resolv.conf and cannot resolve names",
			"add `dns: [1.1.1.1, 2606:4700:4700::1111]` to the network (`jailops network edit NAME`)")
	}
}
