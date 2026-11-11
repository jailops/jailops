package service

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"jailops.net/jailops/internal/fwsim"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// testEnd is one end of a firewall test: a jail of the project with its addresses, a single address, or the host.
type testEnd struct {
	label string
	jail  *api.Jail // nil for an address that belongs to no jail of the project, and for the host
	addrs []netip.Addr
	host  bool
}

// maxTestCases bounds the address pairs one test evaluates.
const maxTestCases = 16

// FirewallTest says whether a new connection from one end to the other would pass the firewall, and which rule
// decides at each anchor it meets. It evaluates the anchors the renderer builds (render.Anchor, through buildJail,
// the code that makes what is loaded), so the answer is about the rules as rendered.
func (s *Service) FirewallTest(ctx context.Context, req api.FirewallTestRequest) (api.FirewallTestResult, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.FirewallTestResult{}, err
	}
	project := p.Meta.Name
	proto := req.Proto
	if proto == "" {
		proto = "tcp"
	}
	if !slices.Contains([]string{"tcp", "udp", "icmp", "icmp6"}, proto) {
		return api.FirewallTestResult{}, invalid("proto %q must be tcp, udp, icmp or icmp6", proto)
	}
	var caveats []string
	icmpType := req.ICMPType
	switch proto {
	case "tcp", "udp":
		if req.Port < 1 || req.Port > 65535 {
			return api.FirewallTestResult{}, invalid("a %s test needs a port from 1 to 65535", proto)
		}
		if icmpType != "" {
			return api.FirewallTestResult{}, invalid("an ICMP type goes with proto icmp or icmp6")
		}
	default:
		if req.Port != 0 {
			return api.FirewallTestResult{}, invalid("a port goes with proto tcp or udp")
		}
		if icmpType == "" {
			icmpType = "echoreq"
			caveats = append(caveats, "no ICMP type was given: the test is an echo request (ping); other types may meet other rules")
		}
	}
	if req.Family != "" && req.Family != "4" && req.Family != "6" {
		return api.FirewallTestResult{}, invalid("family must be 4 or 6")
	}
	src, err := s.testEnd(project, req.From)
	if err != nil {
		return api.FirewallTestResult{}, err
	}
	dst, err := s.testEnd(project, req.To)
	if err != nil {
		return api.FirewallTestResult{}, err
	}
	if src.host && dst.host {
		return api.FirewallTestResult{}, invalid("from and to are both the host: name a jail or an address on one side")
	}
	if src.jail == nil && dst.jail == nil {
		return api.FirewallTestResult{}, invalid("neither end is a jail of this project: there is no anchor of this project to test")
	}
	// the host's own address is the gateway of the other end's networks
	if src.host {
		src.addrs = gatewayAddrs(s, dst.jail)
	}
	if dst.host {
		dst.addrs = gatewayAddrs(s, src.jail)
	}
	if len(src.addrs) == 0 || len(dst.addrs) == 0 {
		return api.FirewallTestResult{}, invalid("%s has no address to test with", map[bool]string{true: src.label, false: dst.label}[len(src.addrs) == 0])
	}
	sets, err := s.allSetsWithJails(project)
	if err != nil {
		return api.FirewallTestResult{}, err
	}
	anchors := map[string]*render.Anchor{}
	loaded := map[string]string{}
	anchorOf := func(j api.Jail) (*render.Anchor, error) {
		key := jailKey(j)
		if a, ok := anchors[key]; ok {
			return a, nil
		}
		b, err := s.buildJail(ctx, j, sets)
		if err != nil {
			return nil, err
		}
		if b.desired.Anchor == nil {
			return nil, invalid("jail %q has no firewall anchor (it has no address or no network)", j.Metadata.Name)
		}
		anchors[key] = b.desired.Anchor
		loaded[key] = s.loadedState(ctx, b.desired.AnchorName, b.desired.Ruleset)
		return b.desired.Anchor, nil
	}

	res := api.FirewallTestResult{Verdict: api.TestPass}
	for _, fam := range []string{"inet", "inet6"} {
		if req.Family == "4" && fam != "inet" || req.Family == "6" && fam != "inet6" {
			continue
		}
		// icmp is the IPv4 protocol and icmp6 the IPv6 one, as in a rule
		if proto == "icmp" && fam != "inet" || proto == "icmp6" && fam != "inet6" {
			continue
		}
		for _, sa := range familyAddrs(src.addrs, fam) {
			for _, da := range familyAddrs(dst.addrs, fam) {
				if len(res.Cases) >= maxTestCases {
					caveats = append(caveats, fmt.Sprintf("only the first %d address pairs were tested", maxTestCases))
					break
				}
				c := api.FirewallTestCase{Family: fam, From: sa.String(), To: da.String(), Proto: proto, Port: req.Port, ICMPType: icmpType, Verdict: api.TestPass}
				pkt := fwsim.Packet{Family: fam, Proto: proto, Src: sa, Dst: da, DstPort: req.Port, ICMPType: icmpType}
				walk := func(side string, j *api.Jail, h fwsim.Hook) (bool, error) {
					an, err := anchorOf(*j)
					if err != nil {
						return false, err
					}
					r := fwsim.Evaluate(an, pkt, h)
					hop := api.FirewallTestHop{Side: side, Jail: j.Metadata.Name, Anchor: an.Name, Interface: h.Class, Direction: h.Dir,
						Verdict: string(r.Verdict), Why: r.Why, LoadedMatchesRendered: loaded[jailKey(*j)]}
					if r.Rule != nil {
						hop.DecidedBy = &api.FirewallTestRule{Stage: r.Rule.Origin.Stage, Policy: r.Rule.Origin.Policy, ProjectPolicy: r.Rule.Origin.ProjectPolicy,
							Managed: r.Rule.Origin.Managed, Rule: r.Rule.Origin.RuleNo, Name: r.Rule.Origin.RuleName, Comment: r.Rule.Origin.Comment, PF: r.Text}
					}
					for _, sk := range r.Skipped {
						hop.Skipped = append(hop.Skipped, api.FirewallTestSkipped{Why: sk.Why, Rule: api.FirewallTestRule{Stage: sk.Rule.Origin.Stage,
							Policy: sk.Rule.Origin.Policy, ProjectPolicy: sk.Rule.Origin.ProjectPolicy, Managed: sk.Rule.Origin.Managed, Rule: sk.Rule.Origin.RuleNo,
							Name: sk.Rule.Origin.RuleName, Comment: sk.Rule.Origin.Comment, PF: an.Lines[sk.Rule.Line]}})
					}
					c.Hops = append(c.Hops, hop)
					if r.Verdict != fwsim.Pass {
						c.Verdict = string(r.Verdict)
						return false, nil
					}
					return true, nil
				}
				// What the source jail sends is judged on its own port. What reaches the destination jail leaves the
				// host on the gateway interface of its network (every jail port is private, so nothing goes jail to
				// jail on the bridge; docs/design.md 18.9 and 18.13).
				cont := true
				if src.jail != nil {
					if cont, err = walk("source", src.jail, fwsim.Hook{Class: render.IfEpair, Dir: "in"}); err != nil {
						return api.FirewallTestResult{}, err
					}
				}
				if cont && dst.jail != nil {
					if _, err = walk("destination", dst.jail, fwsim.Hook{Class: render.IfGateway, Dir: "out"}); err != nil {
						return api.FirewallTestResult{}, err
					}
				}
				res.Cases = append(res.Cases, c)
			}
		}
	}
	if len(res.Cases) == 0 {
		switch {
		case proto == "icmp" && req.Family == "6":
			return api.FirewallTestResult{}, invalid("icmp is the IPv4 protocol").WithHint("test IPv6 with --proto icmp6")
		case proto == "icmp6" && req.Family == "4":
			return api.FirewallTestResult{}, invalid("icmp6 is the IPv6 protocol").WithHint("test IPv4 with --proto icmp")
		}
		return api.FirewallTestResult{}, invalid("%s and %s have no address %s in common", src.label, dst.label, map[bool]string{true: "family", false: "family (icmp is IPv4, icmp6 is IPv6)"}[proto != "icmp" && proto != "icmp6"])
	}
	if proto == "icmp" || proto == "icmp6" {
		other := map[string]string{"icmp": "icmp6 for IPv6", "icmp6": "icmp for IPv4"}[proto]
		caveats = append(caveats, fmt.Sprintf("%s is tested for one family only; %s is a separate test", proto, other))
	}
	for _, c := range res.Cases {
		switch {
		case c.Verdict == api.TestBlock:
			res.Verdict = api.TestBlock
		case c.Verdict == api.TestUnknown && res.Verdict != api.TestBlock:
			res.Verdict = api.TestUnknown
		}
	}
	res.Caveats = append(caveats, testCaveats(src, dst)...)
	return res, nil
}

// testCaveats say what the answer does not cover.
func testCaveats(src, dst testEnd) []string {
	c := []string{
		"the answer is for a new connection: a connection that is already open (pf keeps its state) is not asked about",
		"the rules of the host's own pf.conf are not evaluated; JailOps's anchors hang under it and the defaults of an anchor are final",
	}
	if src.jail != nil && dst.jail == nil && !dst.host {
		c = append(c, "the destination is not a jail of this project: only the source's anchor is evaluated; the host's rules and the uplink decide the rest")
	}
	if src.jail != nil && dst.host {
		c = append(c, "the destination is the host: only the source's anchor is evaluated; the host's own rules decide the rest")
	}
	if dst.jail != nil {
		c = append(c, "on the gateway interface only the destination jail's anchor is evaluated, not the anchors of other jails of the network")
	}
	if src.jail != nil {
		c = append(c, "the source address is the jail's own, as it is on the jail's port, before any translation (NAT)")
	}
	return c
}

// testEnd reads one end of a test.
func (s *Service) testEnd(project, text string) (testEnd, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return testEnd{}, invalid("name both ends: a jail (@name), an address, or host")
	}
	if text == "host" {
		return testEnd{label: "host", host: true}, nil
	}
	if name, ok := strings.CutPrefix(text, "@"); ok {
		key, err := validate.QualifiedName(project, name)
		if err != nil {
			return testEnd{}, notFound("jail", name)
		}
		j, err := s.jailSnapshot(key)
		if err != nil {
			if sets, serr := s.allSets(project); serr == nil {
				if _, isSet := sets[name]; isSet {
					return testEnd{}, invalid("@%s is an address set, not one end of a connection", name).WithHint("name a jail of the project or one address")
				}
			}
			return testEnd{}, notFound("jail", name)
		}
		return testEnd{label: "@" + name, jail: &j, addrs: jailAddrs(s.effective(j))}, nil
	}
	if strings.Contains(text, "/") {
		return testEnd{}, invalid("%q is a prefix: give one address", text)
	}
	a, err := netip.ParseAddr(text)
	if err != nil {
		return testEnd{}, invalid("%q is not a jail (@name), an address or host", text)
	}
	a = a.Unmap()
	// an address of a jail of the project is that jail
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range sortedKeys(s.jails) {
		j := s.jails[n].jail
		if j.Metadata.Project != project {
			continue
		}
		if slices.Contains(jailAddrs(s.effective(j)), a) {
			jj := j
			return testEnd{label: text, jail: &jj, addrs: []netip.Addr{a}}, nil
		}
	}
	return testEnd{label: text, addrs: []netip.Addr{a}}, nil
}

func familyAddrs(addrs []netip.Addr, fam string) []netip.Addr {
	var out []netip.Addr
	for _, a := range addrs {
		if a.Is4() == (fam == "inet") {
			out = append(out, a)
		}
	}
	return out
}

// gatewayAddrs are the addresses the host connects from or listens on for a jail: the gateways of its networks (IPv4,
// and IPv6 of a routed network; a proxy-mode network has a link-local gateway and is left out).
func gatewayAddrs(s *Service, j *api.Jail) []netip.Addr {
	if j == nil {
		return nil
	}
	var out []netip.Addr
	for _, na := range s.effective(*j).Network {
		n, err := s.netKind.get(na.Network)
		if err != nil {
			continue
		}
		if n.Spec.IPv4 != nil {
			if a, ok := parseAddr(n.Spec.IPv4.Gateway); ok {
				out = append(out, a)
			}
		}
		if v6 := n.Spec.IPv6; v6 != nil && v6.Mode == api.IPv6Routed {
			if a, ok := parseAddr(v6.Gateway); ok && !a.IsLinkLocalUnicast() {
				out = append(out, a)
			}
		}
	}
	return out
}

// loadedState says whether the rules loaded in pf for an anchor are the rendered ones: "true", "false" or "unknown".
func (s *Service) loadedState(ctx context.Context, anchor, ruleset string) string {
	if s.d.PF == nil {
		return "unknown"
	}
	norm, err := s.d.PF.Validate(ctx, anchor, ruleset)
	if err != nil {
		return "unknown"
	}
	live, err := s.d.PF.Read(ctx, anchor)
	if err != nil {
		return "unknown"
	}
	if slices.Equal(pfctl.RuleLines(norm), pfctl.RuleLines(live)) {
		return "true"
	}
	return "false"
}
