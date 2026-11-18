package render

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Uplink is the interface through which the upstream reaches an address of a jail, and whether it does so by routing.
type Uplink struct {
	Iface string
	// Routed is true for an address the upstream routes to the host (IPv6 routed and proxy, IPv4 without NAT): it
	// can receive a connection from the Internet. A NATed IPv4 address cannot: only its port forwards are reached.
	Routed bool
	// Tunnels are the interfaces of the WireGuard servers that serve the network that holds the address: what comes in
	// on them is an external source like what comes in on the uplink, whether the address is NATed or not.
	Tunnels []string
}

// uplinkFor is the uplink of an address of the jail: its entry of UplinkOf, else the single Uplink of the input.
func (in *PFInput) uplinkFor(a netip.Addr) string {
	if u, ok := in.UplinkOf[a.Unmap()]; ok && u.Iface != "" {
		return u.Iface
	}
	return in.Uplink
}

// UplinkStateLimit is the number of states one pass rule on the uplink may create when neither the jail nor the host has a
// limit (limits.states, jails.max_states: 0 is none): a flood of connection attempts from the Internet fills the rule's states and not the host's state table.
const UplinkStateLimit = 5000

// routedUplinks are the jail's addresses that the upstream reaches by routing, with their uplink: where a connection from
// outside to the jail arrives on the host.
func routedUplinks(in *PFInput, addrs []netip.Addr) map[netip.Addr]string {
	var out map[netip.Addr]string
	for _, a := range addrs {
		if u, ok := in.UplinkOf[a]; ok && u.Routed && u.Iface != "" {
			if out == nil {
				out = map[netip.Addr]string{}
			}
			out[a] = u.Iface
		}
	}
	return out
}

// tunnelIfaces are the jail's addresses with the interfaces of the WireGuard servers that serve their network.
func tunnelIfaces(in *PFInput, addrs []netip.Addr) map[netip.Addr][]string {
	var out map[netip.Addr][]string
	for _, a := range addrs {
		if u, ok := in.UplinkOf[a]; ok && len(u.Tunnels) > 0 {
			if out == nil {
				out = map[netip.Addr][]string{}
			}
			out[a] = append([]string(nil), u.Tunnels...)
		}
	}
	return out
}

// uplinkSection is the rules of one policy with where they come from, for uplinkRules.
type uplinkSection struct {
	org   Origin
	rules []api.FirewallRule
}

// externalEndpoints is endpoints for the uplink: what a rule names that can arrive from outside. "any" (or no source at
// all) is any; an address and an address set stay; the host, a jail and a tag never arrive on the uplink, so they are
// left out, and a rule that names nothing else has no uplink rule. The Match is the structured form.
func externalEndpoints(entries []string, f family, in *PFInput) (string, Match, bool) {
	if len(entries) == 0 {
		return "any", anyMatch(), true
	}
	var parts []string
	var m Match
	for _, e := range entries {
		k, v, err := validate.ParseEndpoint(e)
		if err != nil {
			return "", Match{}, false
		}
		switch k {
		case validate.EndpointAny:
			return "any", anyMatch(), true
		case validate.EndpointSet:
			parts = append(parts, "<"+validate.SetTable(v)+">")
			m.Tables = append(m.Tables, validate.SetTable(v))
			if members, err := validate.FlattenSet(v, in.Sets, in.Jails); err == nil {
				m.Prefixes = append(m.Prefixes, prefixesOf(members)...)
			}
		case validate.EndpointAddr:
			if isV4Text(v) == (f.kw == "inet") {
				parts = append(parts, v)
				if p, ok := prefixOf(v); ok {
					m.Prefixes = append(m.Prefixes, p)
				}
			}
		}
	}
	if len(parts) == 0 {
		return "", Match{}, false
	}
	return listStr(parts), m, true
}

// UplinkTable is the table of the subnets that must never arrive on the uplink: the jail's own networks and those of
// the other projects (a packet from them that comes in from outside is forged).
const UplinkTable = "uplink_nets"

// uplinkRules renders, for the jail's routed addresses, what its policies admit from outside on the uplink of each
// address: an explicit allow rule in with an external source becomes a pass in quick on the uplink to the jail's own
// addresses, so that the jail's firewall decides and the host's own pf.conf needs nothing for the jail. The defaults
// never open the uplink, and neither does a rule whose source is the host, a jail or a tag (those never arrive
// there). A deny rule is rendered first (deny wins at the door as on the jail's link), then the forged-source block.
func uplinkRules(b *builder, in *PFInput, secs []uplinkSection, fams []family) error {
	type key struct{ iface, kw string }
	groups := map[key][]netip.Addr{}
	var keys []key
	for _, f := range fams {
		for _, a := range f.addrs {
			u, ok := in.UplinkOf[a]
			if !ok {
				continue
			}
			// the interfaces an external source comes in on: the uplink of a routed address, and the tunnels that serve the network
			var ingress []string
			if u.Routed && u.Iface != "" {
				ingress = append(ingress, u.Iface)
			}
			ingress = append(ingress, u.Tunnels...)
			for _, ifc := range ingress {
				if err := validate.IfName("uplink", ifc); err != nil && !wgIfaceRE.MatchString(ifc) { // a tunnel is wg-<name>
					return fmt.Errorf("render: ingress interface of %s: %w", a, err)
				}
				k := key{ifc, f.kw}
				if _, seen := groups[k]; !seen {
					keys = append(keys, k)
				}
				if !slices.Contains(groups[k], a) {
					groups[k] = append(groups[k], a)
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].iface != keys[j].iface {
			return keys[i].iface < keys[j].iface
		}
		return keys[i].kw < keys[j].kw
	})
	type cand struct {
		org  Origin
		i    int
		rule api.FirewallRule
	}
	var denies, allows []cand
	for _, sec := range secs {
		for i, r := range sec.rules {
			// Only an explicit, enabled rule for traffic into the jail, to the jail's own addresses, from outside.
			if r.Disabled || r.Redirect != nil || r.Direction == "out" || len(r.To) > 0 {
				continue
			}
			if _, _, ok := externalEndpoints(r.From, family{kw: "inet"}, in); !ok {
				if _, _, ok6 := externalEndpoints(r.From, family{kw: "inet6"}, in); !ok6 {
					continue
				}
			}
			c := cand{org: sec.org, i: i, rule: r}
			if r.Action == "deny" {
				denies = append(denies, c)
			} else {
				allows = append(allows, c)
			}
		}
	}
	if len(denies)+len(allows) == 0 {
		return nil
	}
	b.line("# the uplink: what the policies admit from outside to the jail's own addresses; the defaults never open it")
	var nets []string
	for _, p := range in.OwnNets {
		nets = append(nets, p.Masked().String())
	}
	for _, p := range in.Foreign {
		nets = append(nets, p.Masked().String())
	}
	sort.Strings(nets)
	nets = slices.Compact(nets)
	if len(nets) > 0 {
		b.line("%s", tableLine(UplinkTable, nets))
	}
	for _, k := range keys {
		addrs := groups[k]
		to := listStr(addrStrings(addrs))
		f := family{kw: k.kw, addrs: addrs}
		// A source that pf's reverse path check refuses on this interface (an address of the host, of a tunnel or of the LAN
		// that arrives from outside) is forged; the check depends on the host's routes, so the simulator does not model it
		// (a match that holds no address).
		b.rule(Rule{Action: "block", Dir: "in", Iface: k.iface, Family: k.kw, From: Match{Tables: []string{"urpf-failed"}}, To: addrMatch(addrs),
			Origin: Origin{Stage: StageUplink}},
			"block drop in quick on %s %s from urpf-failed to %s", k.iface, k.kw, to)
		if len(nets) > 0 {
			b.rule(Rule{Action: "block", Dir: "in", Iface: k.iface, Family: k.kw, From: Match{Prefixes: prefixesOf(nets), Tables: []string{UplinkTable}}, To: addrMatch(addrs),
				Origin: Origin{Stage: StageUplink}},
				"block drop in quick on %s %s from <%s> to %s", k.iface, k.kw, UplinkTable, to)
		}
		emit := func(c cand, act, pfAct string) {
			r := c.rule
			if r.Proto == "icmp" && k.kw != "inet" || r.Proto == "icmp6" && k.kw != "inet6" {
				return
			}
			from, mFrom, ok := externalEndpoints(r.From, f, in)
			if !ok {
				return
			}
			sr := Rule{Action: pfAct, Dir: "in", Iface: k.iface, Family: k.kw, Proto: r.Proto, From: mFrom, To: addrMatch(addrs), Origin: c.org}
			sr.Origin.RuleNo, sr.Origin.RuleName, sr.Origin.Comment = c.i+1, r.Name, r.Comment
			var sb strings.Builder
			sb.WriteString(act + " in")
			if r.Log {
				sb.WriteString(" log")
			}
			fmt.Fprintf(&sb, " quick on %s %s", k.iface, k.kw)
			if r.Proto != "" {
				sb.WriteString(" proto " + r.Proto)
			}
			fmt.Fprintf(&sb, " from %s to %s", from, to)
			if len(r.Ports) > 0 {
				sb.WriteString(" port " + portList(r.Ports))
				sr.Ports = portRanges(r.Ports)
			}
			if pfAct == "pass" {
				// a limit of states on every rule that lets the Internet in (the jail's own limit when it has one)
				limit := UplinkStateLimit
				if in.MaxStates > 0 {
					limit = in.MaxStates
				}
				fmt.Fprintf(&sb, " keep state (max %d, if-bound)", limit)
			}
			b.line("# %s, on the uplink", ruleLabel(c.i, r))
			b.rule(sr, "%s", sb.String())
		}
		for _, c := range denies {
			emit(c, "block drop", "block")
		}
		for _, c := range allows {
			emit(c, "pass", "pass")
		}
	}
	return nil
}
