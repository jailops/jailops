package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// exposeRoute is how the Internet reaches one address of a jail: directly on the uplink (an address the upstream routes
// to the host) or through a port forward (an IPv4 address behind NAT), or not at all (a network without an uplink).
type exposeRoute struct {
	Family   string // "4" or "6"
	Addr     netip.Addr
	Network  string
	Uplink   string
	NAT      bool // an IPv4 address behind NAT: reached through a forward
	Routed   bool // reached directly on the uplink
	NDPProxy bool // the network has the NDP proxy switched on, for the warnings
}

// addressed are the routes that have an address already: what a jail that exists can be reached on.
func addressed(routes []exposeRoute) []exposeRoute {
	return slices.DeleteFunc(slices.Clone(routes), func(r exposeRoute) bool { return !r.Addr.IsValid() })
}

// exposeRoutes lists the addresses of a jail with how the Internet reaches each. An address that is still "auto" (a jail
// being made) counts, with no address yet: it will have one of the family.
func (s *Service) exposeRoutes(spec api.JailSpec) []exposeRoute {
	var out []exposeRoute
	for _, a := range spec.Network {
		n, err := s.netKind.get(a.Network)
		if err != nil {
			continue
		}
		if ad, _ := parseAddr(a.IPv4); a.IPv4 != "none" && n.Spec.IPv4 != nil {
			up := n.Spec.Uplink != ""
			out = append(out, exposeRoute{Family: "4", Addr: ad, Network: a.Network, Uplink: n.Spec.Uplink, NAT: up && n.Spec.IPv4.NAT, Routed: up && !n.Spec.IPv4.NAT})
		}
		if ad, _ := parseAddr(a.IPv6); a.IPv6 != "none" && n.Spec.IPv6 != nil {
			up := n.Spec.Uplink != ""
			out = append(out, exposeRoute{Family: "6", Addr: ad, Network: a.Network, Uplink: n.Spec.Uplink, Routed: up, NDPProxy: n.Spec.IPv6.NDPProxy})
		}
	}
	return out
}

// natIPv4 says whether a jail has an IPv4 address behind NAT: its exposed IPv4 ports are forwarded.
func natIPv4(routes []exposeRoute) bool {
	return slices.ContainsFunc(routes, func(r exposeRoute) bool { return r.NAT })
}

// exposesFamily says whether the entry covers a family: an entry without families covers every family.
func exposesFamily(e api.ExposeEntry, fam string) bool {
	return len(e.Families) == 0 || slices.Contains(e.Families, fam)
}

// exposeName names the rules made from an entry.
func exposeName(e api.ExposeEntry) string {
	return "expose " + e.Protocol() + " " + strings.Join(e.Ports, ",")
}

// exposeKey identifies an entry: the same protocol and ports are the same exposure.
func exposeKey(e api.ExposeEntry) string { return e.Protocol() + "/" + strings.Join(e.Ports, ",") }

// normalPorts parses the ports of a request: each item may hold a comma list; the result is sorted, without duplicates.
func normalPorts(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range in {
		for _, p := range strings.Split(item, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			lo, hi, err := validate.ParsePortSpec(p)
			if err != nil {
				return nil, invalid("%q is not a port or a range of ports (80, 80,443, 8000-8100)", p)
			}
			if lo == hi {
				p = strconv.Itoa(lo)
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sortPorts(out)
	return out, nil
}

// sortPorts orders ports and ranges by their first port.
func sortPorts(out []string) {
	sort.Slice(out, func(i, j int) bool {
		a, _, _ := validate.ParsePortSpec(out[i])
		b, _, _ := validate.ParsePortSpec(out[j])
		return a < b
	})
}

// normalProtos turns the proto of a request (tcp, udp or a comma list of them) into the protocols it names, sorted and
// without duplicates.
func normalProtos(p string) ([]string, error) {
	if strings.TrimSpace(p) == "" {
		return []string{"tcp"}, nil
	}
	var out []string
	for _, x := range strings.Split(p, ",") {
		x = strings.TrimSpace(x)
		if x != "tcp" && x != "udp" {
			return nil, invalid("proto %q must be tcp, udp or tcp,udp", p)
		}
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ExposeJail lets the Internet reach ports of a jail: an allow rule on the jail's link and, for an address the upstream
// routes to the host, on its uplink; for an IPv4 address behind NAT, a forward of the same ports of the host. The entry
// of the same protocol and ports is replaced. The IPv4 forward takes ports of the host, so it is a global administrator's:
// a request that needs one from anyone else is refused as a whole.
func (s *Service) ExposeJail(ctx context.Context, name string, req api.ExposeRequest) (api.ExposeResult, error) {
	res, err := s.exposeOnce(ctx, name, req)
	if isConflict(err) { // another change of the jail came in between the read and the write: read it again once
		res, err = s.exposeOnce(ctx, name, req)
	}
	return res, err
}

// isConflict says whether err is a stale generation.
func isConflict(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Code == api.CodePreconditionFailed
}

func (s *Service) exposeOnce(ctx context.Context, name string, req api.ExposeRequest) (api.ExposeResult, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.ExposeResult{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.ExposeResult{}, err
	}
	protos, err := normalProtos(req.Proto)
	if err != nil {
		return api.ExposeResult{}, err
	}
	ports, err := normalPorts(req.Ports)
	if err != nil {
		return api.ExposeResult{}, err
	}
	if len(ports) == 0 {
		return api.ExposeResult{}, invalid("name the ports to expose: jailops expose %s 80,443", name)
	}
	old, err := s.jailSnapshot(key)
	if err != nil {
		return api.ExposeResult{}, notFound("jail", name)
	}
	eff := s.effective(old)
	routes := addressed(s.exposeRoutes(eff))
	if len(routes) == 0 {
		return api.ExposeResult{}, invalid("jail %q has no address: attach it to a network first", name)
	}
	fams, err := exposeFamilies(name, routes, req.Families)
	if err != nil {
		return api.ExposeResult{}, err
	}
	forwards := slices.Contains(fams, "4") && natIPv4(routes)
	if req.HostPort > 0 && !forwards {
		return api.ExposeResult{}, invalid("--host-port applies to an IPv4 forward, and %s", noForwardWhy(name, routes, fams))
	}
	if req.Listen != "" && !forwards {
		return api.ExposeResult{}, invalid("--listen applies to an IPv4 forward, and %s", noForwardWhy(name, routes, fams))
	}
	if forwards && !isGlobalAdmin(ActorFrom(ctx)) {
		return api.ExposeResult{}, forwardNeedsGlobal(name, routes, "expose", req.Ports)
	}
	if forwards {
		for _, p := range ports {
			if strings.Contains(p, "-") {
				return api.ExposeResult{}, rangeForwardError("jail "+name, p)
			}
		}
	}
	if req.HostPort > 0 && len(ports) > 1 {
		return api.ExposeResult{}, invalid("--host-port takes one port: jailops expose %s %s --host-port %d, then jailops expose %s %s", name, ports[0], req.HostPort, name, strings.Join(ports[1:], ","))
	}
	var entries []api.ExposeEntry
	for _, proto := range protos {
		entries = append(entries, api.ExposeEntry{Proto: proto, Ports: ports, Families: fams, From: slices.Clone(req.From), HostPort: req.HostPort,
			Listen: req.Listen, Comment: req.Comment})
	}
	for i := range entries {
		if err := validate.ExposeEntry("expose", entries[i]); err != nil {
			return api.ExposeResult{}, err
		}
	}
	next := slices.Clone(old.Spec.Expose)
	changed := false
	for _, e := range entries {
		if exposureHolds(next, e) {
			continue
		}
		// a port is exposed once per protocol: the new entry takes its ports from the entries that name them
		next = takePorts(next, e.Protocol(), e.Ports)
		next = append(next, e)
		changed = true
	}
	if changed {
		// replacing an entry that had a forward (or changing its sources or jail port) gives or takes a port of the host
		after := old
		after.Spec.Expose = next
		if s.forwardsChange(key, eff, s.effective(after)) && !isGlobalAdmin(ActorFrom(ctx)) {
			return api.ExposeResult{}, forwardNeedsGlobal(name, routes, "expose", req.Ports)
		}
	}
	res := s.exposeResult(name, next, entries, routes, fams)
	res.Notes = s.openedByPolicy(ctx, old, protos, ports, routes, fams)
	if !changed || req.DryRun {
		res.Changed = changed
		return res, nil
	}
	in := old
	in.Spec.Expose = next
	if _, err := s.updateJail(ctx, p, key, name, in, old.Metadata.Generation, false); err != nil {
		return api.ExposeResult{}, err
	}
	res.Changed = true
	s.auditExpose(ctx, p.Meta.Name, name, "EXPOSE", fmt.Sprintf("proto=%s ports=%s families=%s from=%s host_port=%d", req.Proto, strings.Join(ports, ","), strings.Join(fams, ","), fromText(req.From), req.HostPort))
	s.emitIn(ctx, p.Meta.Name, "jail-exposed", "/api/v1/jails/"+name)
	s.kick()
	return res, nil
}

// UnexposeJail takes ports out of a jail's exposure. Removing a forward takes the host's port back, so it is a global
// administrator's too; removing only the IPv6 side is not.
func (s *Service) UnexposeJail(ctx context.Context, name string, req api.UnexposeRequest) (api.ExposeResult, error) {
	res, err := s.unexposeOnce(ctx, name, req)
	if isConflict(err) {
		res, err = s.unexposeOnce(ctx, name, req)
	}
	return res, err
}

func (s *Service) unexposeOnce(ctx context.Context, name string, req api.UnexposeRequest) (api.ExposeResult, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.ExposeResult{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.ExposeResult{}, err
	}
	old, err := s.jailSnapshot(key)
	if err != nil {
		return api.ExposeResult{}, notFound("jail", name)
	}
	protos := []string{"tcp", "udp"}
	if req.Proto != "" {
		if protos, err = normalProtos(req.Proto); err != nil {
			return api.ExposeResult{}, err
		}
	}
	ports, err := normalPorts(req.Ports)
	if err != nil {
		return api.ExposeResult{}, err
	}
	for _, f := range req.Families {
		if f != "4" && f != "6" {
			return api.ExposeResult{}, invalid("family %q must be 4 or 6", f)
		}
	}
	eff := s.effective(old)
	routes := s.exposeRoutes(eff)
	var next, gone []api.ExposeEntry
	for _, e := range old.Spec.Expose {
		if !slices.Contains(protos, e.Protocol()) {
			next = append(next, e)
			continue
		}
		// the ports of the entry this request is about: the named ones, or all of them
		var hit, rest []string
		for _, p := range e.Ports {
			if len(ports) == 0 || slices.Contains(ports, p) {
				hit = append(hit, p)
			} else {
				rest = append(rest, p)
			}
		}
		if len(hit) == 0 {
			next = append(next, e)
			continue
		}
		fams := e.Families
		if len(fams) == 0 {
			fams = []string{"4", "6"}
		}
		taken := fams // the families that stop being exposed on those ports
		if len(req.Families) > 0 {
			taken = slices.DeleteFunc(slices.Clone(fams), func(f string) bool { return !slices.Contains(req.Families, f) })
		}
		if len(taken) == 0 {
			next = append(next, e)
			continue
		}
		if len(rest) > 0 {
			keep := e
			keep.Ports = rest
			next = append(next, keep)
		}
		if left := slices.DeleteFunc(slices.Clone(fams), func(f string) bool { return slices.Contains(taken, f) }); len(left) > 0 {
			stay := e
			stay.Ports, stay.Families = hit, left
			next = append(next, stay)
		}
		out := e
		out.Ports, out.Families = hit, taken
		gone = append(gone, out)
	}
	if len(next) == 0 {
		next = nil
	}
	res := s.exposeResult(name, next, gone, routes, nil)
	if len(gone) == 0 {
		res.Notes = insideRange(old.Spec.Expose, protos, ports)
		return res, nil
	}
	after := old
	after.Spec.Expose = next
	res.Notes = s.stillOpenedByPolicy(ctx, after, gone, routes)
	if natIPv4(routes) {
		before, after := s.forwardsOf(key, eff), eff
		after.Expose = next
		if len(before) != len(s.forwardsOf(key, after)) && !isGlobalAdmin(ActorFrom(ctx)) {
			return api.ExposeResult{}, forwardNeedsGlobal(name, routes, "unexpose", req.Ports)
		}
	}
	if req.DryRun {
		res.Changed = true
		return res, nil
	}
	in := old
	in.Spec.Expose = next
	if _, err := s.updateJail(ctx, p, key, name, in, old.Metadata.Generation, false); err != nil {
		return api.ExposeResult{}, err
	}
	res.Changed = true
	s.auditExpose(ctx, p.Meta.Name, name, "UNEXPOSE", fmt.Sprintf("proto=%s ports=%s families=%s", req.Proto, strings.Join(ports, ","), strings.Join(req.Families, ",")))
	s.emitIn(ctx, p.Meta.Name, "jail-unexposed", "/api/v1/jails/"+name)
	s.kick()
	return res, nil
}

// sameSettings compares two entries without their ports, who and when.
func sameSettings(a, b api.ExposeEntry) bool {
	return a.Protocol() == b.Protocol() && slices.Equal(a.Families, b.Families) && slices.Equal(a.From, b.From) &&
		a.HostPort == b.HostPort && a.Listen == b.Listen && a.Comment == b.Comment
}

// exposureHolds says whether an entry of the jail already exposes all the ports of e with the same settings.
func exposureHolds(entries []api.ExposeEntry, e api.ExposeEntry) bool {
	return slices.ContainsFunc(entries, func(x api.ExposeEntry) bool {
		return sameSettings(x, e) && !slices.ContainsFunc(e.Ports, func(p string) bool { return !slices.Contains(x.Ports, p) })
	})
}

// takePorts takes the ports out of the entries of a protocol: an entry keeps the ports that are not taken, with everything
// else it says, and goes when none is left.
func takePorts(entries []api.ExposeEntry, proto string, ports []string) []api.ExposeEntry {
	var out []api.ExposeEntry
	for _, e := range entries {
		if e.Protocol() == proto {
			e.Ports = slices.DeleteFunc(slices.Clone(e.Ports), func(p string) bool { return slices.Contains(ports, p) })
			if len(e.Ports) == 0 {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// exposureText names what a request exposes the way the EXPOSED column does: "80,443/tcp", "53/tcp,udp".
func exposureText(ports, protos []string) string {
	return strings.Join(ports, ",") + "/" + strings.Join(protos, ",")
}

func fromText(from []string) string {
	if len(from) == 0 {
		return "any"
	}
	return strings.Join(from, ",")
}

func (s *Service) auditExpose(ctx context.Context, project, name, method, detail string) {
	s.Audit(ctx, store.AuditEntry{Actor: ActorFrom(ctx).Name, ActorKind: ActorFrom(ctx).Kind, Method: method,
		Route: "/api/v1/jails/{name}/" + strings.ToLower(method), Object: "/api/v1/jails/" + name, Project: project, Result: "ok", Detail: detail})
}

// exposeFamilies is the families an exposure covers: those the request names, else every family the jail has.
func exposeFamilies(name string, routes []exposeRoute, want []string) ([]string, error) {
	has := map[string]bool{}
	for _, r := range routes {
		has[r.Family] = true
	}
	if len(want) == 0 {
		var out []string
		for _, f := range []string{"4", "6"} {
			if has[f] {
				out = append(out, f)
			}
		}
		return out, nil
	}
	var out []string
	for _, f := range want {
		switch {
		case f != "4" && f != "6":
			return nil, invalid("family %q must be 4 or 6", f)
		case !has[f] && f == "6":
			return nil, invalid("jail %q has no IPv6 address: attach it to a network with IPv6, or expose IPv4 only (--family 4)", name)
		case !has[f]:
			return nil, invalid("jail %q has no IPv4 address", name)
		case !slices.Contains(out, f):
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

// noForwardWhy says why an exposure has no IPv4 forward, for the message about --host-port.
func noForwardWhy(name string, routes []exposeRoute, fams []string) string {
	for _, r := range routes {
		if r.Family == "4" && slices.Contains(fams, "4") {
			return fmt.Sprintf("%s's IPv4 address %s (network %s) has no NAT: the address is reached directly", name, r.Addr, r.Network)
		}
	}
	return fmt.Sprintf("%s's exposure has no IPv4 address", name)
}

// forwardNeedsGlobal is the refusal of a request that changes an IPv4 forward from anyone but a global administrator.
func forwardNeedsGlobal(name string, routes []exposeRoute, verb string, ports []string) error {
	var addr, network string
	for _, r := range routes {
		if r.NAT {
			addr, network = r.Addr.String(), r.Network
			break
		}
	}
	list := strings.Join(ports, ",")
	if list == "" {
		list = "PORTS"
	}
	return api.Errorf(api.CodeForbidden, "an IPv4 port forward is made by a global administrator only: %s's IPv4 address %s is behind NAT on network %s, so a change of its exposure changes ports of the host", name, addr, network).
		WithHint(fmt.Sprintf("IPv6 only: jailops %s %s %s --family 6; a global administrator changes the IPv4 side with --family 4", verb, name, list))
}

// exposeResult describes an exposure for the answer of expose and unexpose: for every address of the jail, how the
// Internet reaches the ports of the entries named, and the warnings.
func (s *Service) exposeResult(name string, all, named []api.ExposeEntry, routes []exposeRoute, fams []string) api.ExposeResult {
	res := api.ExposeResult{Jail: name, Entries: all}
	if res.Entries == nil {
		res.Entries = []api.ExposeEntry{}
	}
	var ports, protos []string
	var hostPorts []string
	var listen string
	for _, e := range named {
		listen = e.Listen
		if !slices.Contains(protos, e.Protocol()) {
			protos = append(protos, e.Protocol())
		}
		for _, p := range e.Ports {
			if !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
			if strings.Contains(p, "-") {
				continue
			}
			h := p
			if e.HostPort > 0 {
				h = strconv.Itoa(e.HostPort)
			}
			if pair := h + "->" + p; !slices.Contains(hostPorts, pair) {
				hostPorts = append(hostPorts, pair)
			}
		}
	}
	sortPorts(ports)
	sort.Strings(protos)
	res.Exposed = exposureText(ports, protos)
	text := strings.Join(ports, ", ")
	for _, r := range routes {
		if fams != nil && !slices.Contains(fams, r.Family) {
			continue
		}
		f := api.ExposeFamily{Family: r.Family, Address: r.Addr.String(), Network: r.Network, Interface: r.Uplink, Ports: text}
		switch {
		case r.NAT:
			f.Via, f.Forwards, f.Listen = "forward", hostPorts, listen
		case r.Routed:
			f.Via = "uplink"
		default:
			f.Via, f.Note = "none", "network "+r.Network+" has no uplink: nothing arrives from outside"
		}
		res.Families = append(res.Families, f)
		if r.Family == "6" && !r.NDPProxy && r.Routed {
			res.Warnings = append(res.Warnings, fmt.Sprintf("network %s: the Internet reaches %s only if your provider routes the prefix to this host (on a shared segment: jailops network ndp-proxy enable %s)", r.Network, r.Addr, r.Network))
		}
	}
	if fams != nil && !slices.Contains(fams, "6") {
		hasV6 := slices.ContainsFunc(routes, func(r exposeRoute) bool { return r.Family == "6" })
		if !hasV6 {
			res.Families = append(res.Families, api.ExposeFamily{Family: "6", Via: "none", Ports: text, Note: "the jail has no IPv6 address"})
		}
	}
	sort.SliceStable(res.Families, func(i, j int) bool { return res.Families[i].Family > res.Families[j].Family })
	return res
}

// exposureRules is what the renderer takes of a jail's exposure: an allow rule in for each entry (on the link, and on the
// uplink of the routed addresses), and, for an IPv4 address behind NAT, a forward of each single port of the entries that
// cover IPv4 (the host port is the jail's port unless the entry names one).
func (s *Service) exposureRules(spec api.JailSpec) ([]render.Exposure, []api.FirewallRule) {
	if len(spec.Expose) == 0 {
		return nil, nil
	}
	nat := natIPv4(s.exposeRoutes(spec))
	var ex []render.Exposure
	var fw []api.FirewallRule
	for _, e := range spec.Expose {
		ex = append(ex, render.Exposure{Name: exposeName(e), Proto: e.Protocol(), Ports: e.Ports, From: e.From, Families: e.Families, Comment: e.Comment})
		if !nat || !exposesFamily(e, "4") {
			continue
		}
		for _, p := range e.Ports {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			host := n
			if e.HostPort > 0 {
				host = e.HostPort
			}
			fw = append(fw, api.FirewallRule{Name: exposeName(e) + " forward", Comment: e.Comment, Action: "allow", Direction: "in", Proto: e.Protocol(),
				From: slices.Clone(e.From), Redirect: &api.Redirect{Listen: e.Listen, HostPort: host, JailPort: n}})
		}
	}
	return ex, fw
}
