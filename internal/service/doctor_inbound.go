package service

import (
	"context"
	"fmt"
	"jailops.net/jailops/internal/validate"
	"net/netip"
	"slices"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// inboundNet is a network whose jails the Internet can reach through the uplink: an IPv6 network the upstream routes to
// the host (routed or proxy) or an IPv4 network without NAT. Its subnet and uplink are what a rule of pf.conf is held
// against.
type inboundNet struct {
	name   string
	uplink string
	subnet netip.Prefix
}

// inboundNets are the networks of the jails that exist (a network nobody uses opens nothing).
func (s *Service) inboundNets() []inboundNet {
	used := map[string]bool{}
	s.mu.RLock()
	for _, e := range s.jails {
		for _, a := range e.jail.Spec.Network {
			used[a.Network] = true
		}
	}
	s.mu.RUnlock()
	var out []inboundNet
	nets, _ := s.netKind.list()
	for _, o := range nets {
		if !used[o.Meta.Name] || o.Spec.Uplink == "" {
			continue
		}
		if v4 := o.Spec.IPv4; v4 != nil && !v4.NAT {
			if p, err := netip.ParsePrefix(v4.Subnet); err == nil {
				out = append(out, inboundNet{name: o.Meta.Name, uplink: o.Spec.Uplink, subnet: p.Masked()})
			}
		}
		if v6 := o.Spec.IPv6; v6 != nil && (v6.Mode == api.IPv6Routed || v6.Mode == api.IPv6Proxy) {
			if p, err := netip.ParsePrefix(v6.Subnet); err == nil {
				out = append(out, inboundNet{name: o.Meta.Name, uplink: o.Spec.Uplink, subnet: p.Masked()})
			}
		}
	}
	return out
}

// pfRule is the part of a rule of `pfctl -s rules` that decides whether it can stand in the way of inbound traffic to a
// jail. A field the parser does not understand makes the rule "not certain" (ok false): the check then says nothing
// about it, because a doctor that fails on a guess is worse than none.
type pfRule struct {
	text           string
	action         string // pass, block
	quick          bool
	dir            string // in, out, ""
	iface          string
	family         string // inet, inet6, ""
	proto          string
	from, to       string
	hasPort, other bool
}

var pfRuleEnd = map[string]bool{"port": true, "flags": true, "keep": true, "no": true, "modulate": true, "synproxy": true, "label": true,
	"tag": true, "tagged": true, "queue": true, "probability": true, "received-on": true, "user": true, "group": true, "icmp-type": true,
	"icmp6-type": true, "allow-opts": true, "(if-bound)": true, "(floating)": true, "scrub": true, "set": true, "rtable": true,
	"route-to": true, "reply-to": true, "dup-to": true, "fastroute": true, "state": true, "ttl": true, "os": true, "(max": true}

// parsePFRule reads one line of the loaded ruleset.
func parsePFRule(ln string) (pfRule, bool) {
	ln = strings.TrimSpace(ln)
	f := strings.Fields(ln)
	if len(f) < 2 || (f[0] != "pass" && f[0] != "block") {
		return pfRule{}, false
	}
	r := pfRule{text: ln, action: f[0]}
	for i := 1; i < len(f); i++ {
		switch f[i] {
		case "in", "out":
			r.dir = f[i]
		case "quick":
			r.quick = true
		case "on":
			if i+1 < len(f) {
				i++
				if f[i] == "!" || strings.HasPrefix(f[i], "!") || f[i] == "{" {
					r.other = true
				}
				r.iface = f[i]
			}
		case "inet", "inet6":
			r.family = f[i]
		case "proto":
			if i+1 < len(f) {
				i++
				r.proto = f[i]
			}
		case "all":
			r.from, r.to = "any", "any"
		case "from", "to":
			j := i + 1
			var parts []string
			for ; j < len(f) && f[j] != "to" && !pfRuleEnd[f[j]]; j++ {
				parts = append(parts, f[j])
			}
			v := strings.Join(parts, " ")
			if f[i] == "from" {
				r.from = v
			} else {
				r.to = v
			}
			i = j - 1
		case "port":
			r.hasPort = true
		}
	}
	return r, true
}

// inboundRulesInTheWay reads the rules of the loaded main ruleset that come before the jailops anchor and returns the
// block rules that drop every inbound packet to a jail network (a `block ... quick` for all sources, no port, on no
// interface or on the uplink, to any address or to a prefix that holds the network), and the pass rules that open a
// jail network for good (`pass ... quick` to an address or a prefix of the network: the hand-written rule this check
// replaces). Rules with a source list, a table, a port or an interface that is not an uplink of a network are not
// understood as being in the way.
func inboundRulesInTheWay(rules string, nets []inboundNet) (blocks, passes []string) {
	uplinks := map[string]bool{}
	for _, n := range nets {
		uplinks[n.uplink] = true
	}
	touches := func(r pfRule, to string) (inboundNet, bool) {
		for _, n := range nets {
			if r.iface != "" && r.iface != n.uplink {
				continue
			}
			if r.family == "inet" && !n.subnet.Addr().Is4() || r.family == "inet6" && !n.subnet.Addr().Is6() {
				continue
			}
			if to == "any" {
				return n, true
			}
			if p, err := netip.ParsePrefix(to); err == nil && p.Overlaps(n.subnet) {
				return n, true
			}
			if a, err := netip.ParseAddr(to); err == nil && n.subnet.Contains(a) {
				return n, true
			}
		}
		return inboundNet{}, false
	}
	for _, ln := range strings.Split(rules, "\n") {
		if strings.Contains(ln, `anchor "jailops/*"`) {
			break
		}
		r, ok := parsePFRule(ln)
		if !ok || !r.quick || r.dir == "out" || r.other || r.hasPort && r.action == "block" {
			continue
		}
		if r.iface != "" && !uplinks[r.iface] {
			continue
		}
		switch r.action {
		case "block":
			if r.from != "any" || r.hasPort || (r.proto != "" && r.proto != "tcp" && r.proto != "udp") {
				continue
			}
			if n, ok := touches(r, r.to); ok {
				blocks = append(blocks, fmt.Sprintf("%s (network %s)", r.text, n.name))
			}
		case "pass":
			if r.to == "" || r.to == "any" || strings.HasPrefix(r.to, "(") || strings.HasPrefix(r.to, "<") {
				continue
			}
			if n, ok := touches(r, r.to); ok {
				passes = append(passes, fmt.Sprintf("%s (network %s)", r.text, n.name))
			}
		}
	}
	slices.Sort(blocks)
	slices.Sort(passes)
	return slices.Compact(blocks), slices.Compact(passes)
}

// doctorPFInbound checks what stands in the way of the jail's own firewall opening the uplink. The anchors decide what
// reaches a jail from outside; the administrator's pf.conf only keeps its meaning for the host. A quick block of
// everything before the anchor line drops the traffic before the anchor is asked, and a hand-written pass rule for a
// jail prefix keeps a jail without an allow rule reachable.
func (s *Service) doctorPFInbound(c *checkSet, rules string) {
	const name = "pf.inbound"
	nets := s.inboundNets()
	if len(nets) == 0 {
		return // nothing of a jail can be reached from outside: there is nothing to check
	}
	blocks, passes := inboundRulesInTheWay(rules, nets)
	switch {
	case len(blocks) > 0:
		c.add(name, api.DoctorFail, "a quick block rule of pf.conf for every source comes before the jailops anchor, so inbound traffic to the jails is dropped before their policies are asked, whatever the policies say: "+strings.Join(blocks, "; "),
			`decide what that rule is for. To keep the Internet away from the jails, leave it. For the policies of the jails to decide, move anchor "jailops/*" above it (a rule that must also apply to the jails, such as a blocklist of sources, stays above the anchor line; rules for a single source are not flagged here), then pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf. JailOps never edits pf.conf`)
	case len(passes) > 0:
		c.add(name, api.DoctorWarn, "a pass rule of pf.conf before the jailops anchor opens a jail network for good, so a jail without an allow rule is reachable from outside as well: "+strings.Join(passes, "; "),
			"delete it: the jail's own firewall opens the uplink now, for the ports you name (jailops expose JAIL PORTS)")
	default:
		c.add(name, api.DoctorOK, "no rule of pf.conf before the jailops anchor stands in the way of what the policies admit from outside", "")
	}
}

// doctorExposedJails says which jails the Internet can reach through an uplink: the jails whose anchor has a pass rule on
// an uplink (a port forward, or an allow in rule with an external source on a routed address). An administrator of a
// project exposes ports of the jails of the project; this line is where the others see it.
func (s *Service) doctorExposedJails(ctx context.Context, c *checkSet) {
	const name = "jails.exposed"
	var out []string
	s.mu.RLock()
	var jails []api.Jail
	for _, k := range sortedKeys(s.jails) {
		jails = append(jails, s.jails[k].jail)
	}
	s.mu.RUnlock()
	refsOf := map[string]fwRefs{}
	for _, j := range jails {
		project := j.Metadata.Project
		refs, ok := refsOf[project]
		if !ok {
			var err error
			if refs, err = s.allRefs(project); err != nil {
				continue
			}
			refsOf[project] = refs
		}
		if es := s.exposedOf(ctx, j, refs); len(es) > 0 {
			out = append(out, j.Metadata.Project+"/"+j.Metadata.Name+" ("+exposedSummary(es)+")")
		}
	}
	if len(out) == 0 {
		c.add(name, api.DoctorOK, "no jail has a rule that lets the Internet in on an uplink", "")
		return
	}
	c.add(name, api.DoctorInfo, fmt.Sprintf("%d jail(s) can be reached from the Internet: %s", len(out), strings.Join(out, "; ")),
		"jailops expose opens a jail to the Internet, an allow in rule with an external source does too; jailops firewall exposed lists them and jailops firewall show JAIL says why")
}

// doctorInboundWithoutSource lists the jails on a routed address whose policies have an allow in rule without a source. Before
// 0.8.4 such a rule opened the uplink to the Internet; now it is for the jail's link (the project's jails and the host), so
// what worked before may have stopped. The operator sees which rules, and what to do.
func (s *Service) doctorInboundWithoutSource(c *checkSet) {
	const name = "jails.inbound-without-source"
	s.mu.RLock()
	var jails []api.Jail
	for _, k := range sortedKeys(s.jails) {
		jails = append(jails, s.jails[k].jail)
	}
	s.mu.RUnlock()
	var out []string
	for _, j := range jails {
		eff := s.effective(j)
		if !slices.ContainsFunc(s.exposeRoutes(eff), func(r exposeRoute) bool { return r.Routed }) {
			continue
		}
		names := slices.Clone(eff.Firewall.Policies)
		if pr, err := s.projKind.get(j.Metadata.Project); err == nil {
			names = append(names, pr.Spec.FirewallPolicies...)
		}
		for _, pn := range names {
			pol, err := s.polKind.p(j.Metadata.Project).get(pn)
			if err != nil {
				continue
			}
			for i, r := range pol.Spec.Rules {
				if r.Action == "allow" && r.Direction == "in" && !r.Disabled && len(r.From) == 0 {
					out = append(out, fmt.Sprintf("%s/%s: policy %s rule %d", j.Metadata.Project, j.Metadata.Name, pn, i+1))
				}
			}
		}
	}
	if len(out) == 0 {
		return
	}
	c.add(name, api.DoctorInfo, "an allow in rule without a source is for the jail's link only and does not open the uplink; these were open to the Internet before 0.8.4: "+strings.Join(out, "; "),
		"to keep one open to the Internet, give the rule --from any, or use jailops expose JAIL PORTS")
}

// exposedSummary says what the Internet reaches of a jail in one phrase: "tcp 80,443 over IPv4 and IPv6, tcp 22 over
// IPv6 from 2001:db8::/32". A forward names the port of the host.
func exposedSummary(es []api.ExposedEntry) string {
	type key struct{ proto, ports, from string }
	var order []key
	fams := map[key][]string{}
	for _, e := range es {
		ports := e.Ports
		if e.Via == "forward" && e.HostPort > 0 {
			ports = fmt.Sprint(e.HostPort)
		}
		if ports == "" {
			ports = "all ports"
		}
		from := ""
		if e.Restricted {
			from = strings.Join(e.From, ",")
		}
		k := key{e.Proto, ports, from}
		if _, ok := fams[k]; !ok {
			order = append(order, k)
		}
		f := "IPv" + e.Family
		if !slices.Contains(fams[k], f) {
			fams[k] = append(fams[k], f)
		}
	}
	var parts []string
	for _, k := range order {
		slices.Sort(fams[k])
		p := k.proto + " " + k.ports + " over " + strings.Join(fams[k], " and ")
		if k.from != "" {
			p += " from " + k.from
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// doctorInPlaceThinJails warns about thin FreeBSD jails stored with the update strategy inplace (a strategy that was
// accepted for them before and is refused now): they stay as they are and updatable, and a thin jail moves to a newer
// generation of its image.
func (s *Service) doctorInPlaceThinJails(c *checkSet) {
	const name = "jails.update"
	var out []string
	s.mu.RLock()
	for _, k := range sortedKeys(s.jails) {
		j := s.jails[k].jail
		if validate.UpdateStrategyFits(j.Spec) != nil {
			out = append(out, j.Metadata.Project+"/"+j.Metadata.Name)
		}
	}
	s.mu.RUnlock()
	if len(out) == 0 {
		c.add(name, api.DoctorOK, "no thin jail has the update strategy inplace", "")
		return
	}
	c.add(name, api.DoctorWarn, fmt.Sprintf("%d thin jail(s) have the update strategy inplace, which a thin jail does not support: %s", len(out), strings.Join(out, ", ")),
		"set rebase: jailops jail set JAIL --update-strategy rebase (a thin jail moves to a newer generation of its image)")
}
