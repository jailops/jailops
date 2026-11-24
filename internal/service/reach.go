package service

import (
	"context"
	"fmt"
	"jailops.net/jailops/internal/sys/ifconfig"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/fwsim"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// reachCtx is what the reach computation needs besides the anchor: the addresses the classes of source are probed with.
// buildJail fills it from the same inputs it renders the anchor from.
type reachCtx struct {
	host    []netip.Addr
	foreign []netip.Prefix
	own     []netip.Prefix
	addrs   []netip.Addr
	// policies are the policies of the jail in the order they are rendered
	policies []api.ReachPolicy
}

// The addresses of "the Internet" in a probe: no network, set or prefix of JailOps holds them (they are in the ranges of
// RFC 2544 and RFC 5180), so only a rule whose source is any matches them.
var (
	internetV4 = netip.MustParseAddr("198.18.255.254")
	internetV6 = netip.MustParseAddr("2001:2:ffff::fffe")
)

// reachTTL is how long a computed reach is reused: jailops list asks for every jail of a project.
const reachTTL = 5 * time.Second

type reachEntry struct {
	at    time.Time
	reach api.JailReach
}

type reachCache struct {
	mu sync.Mutex
	m  map[string]reachEntry
	// the interfaces of the host, listed once for a while: every jail with a forward asks for them
	ifaces   []ifconfig.Iface
	ifacesAt time.Time
}

func (c *reachCache) get(key string, now time.Time) (api.JailReach, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) > reachTTL {
		return api.JailReach{}, false
	}
	return e.reach, true
}

func (c *reachCache) put(key string, r api.JailReach, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]reachEntry{}
	}
	if len(c.m) > 4096 {
		c.m = map[string]reachEntry{}
	}
	c.m[key] = reachEntry{at: now, reach: r}
}

// JailReach says what reaches a jail of the request's project, what it reaches, and the rules of its anchor in the order
// pf decides.
func (s *Service) JailReach(ctx context.Context, name string) (api.JailReach, error) {
	_, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.JailReach{}, err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return api.JailReach{}, notFound("jail", name)
	}
	refs, err := s.allRefs(j.Metadata.Project)
	if err != nil {
		return api.JailReach{}, err
	}
	return s.reachOf(ctx, j, refs, false), nil
}

// reachOf computes the reach of a jail from its rendered anchor. A jail whose anchor cannot be built has Error set.
func (s *Service) reachOf(ctx context.Context, j api.Jail, refs fwRefs, cached bool) api.JailReach {
	key := jailKey(j)
	if cached {
		if r, ok := s.reach.get(key, s.now()); ok {
			return r
		}
	}
	out := api.JailReach{Project: j.Metadata.Project, Jail: j.Metadata.Name}
	b, err := s.buildJail(ctx, j, refs)
	switch {
	case err != nil:
		out.Error = err.Error()
	case b.desired.Anchor == nil:
		out.Error = "the jail has no firewall anchor (it has no address or no network)"
	default:
		out = computeReach(b.desired.Anchor, b.reach, s.reachNames(j), out)
		out.RulesID = render.RulesID(b.desired.Ruleset)
		s.addHostAddresses(ctx, &out, b.desired.Anchor)
	}
	s.reach.put(key, out, s.now())
	return out
}

// reachNames maps a jail's addresses to the networks that hold them.
func (s *Service) reachNames(j api.Jail) map[netip.Addr]string {
	m := map[netip.Addr]string{}
	for _, a := range s.effective(j).Network {
		for _, t := range []string{a.IPv4, a.IPv6} {
			if ad, err := netip.ParseAddr(t); err == nil {
				m[ad.Unmap()] = a.Network
			}
		}
	}
	return m
}

// hostInterfaces lists the interfaces of the host, at most once in reachTTL.
func (s *Service) hostInterfaces(ctx context.Context) ([]ifconfig.Iface, error) {
	c := &s.reach
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ifaces != nil && s.now().Sub(c.ifacesAt) <= reachTTL {
		return c.ifaces, nil
	}
	list, err := s.d.Net.List(ctx)
	if err != nil {
		return nil, err
	}
	c.ifaces, c.ifacesAt = list, s.now()
	return list, nil
}

// addHostAddresses fills the host address of the forwards that listen on the address of the uplink.
func (s *Service) addHostAddresses(ctx context.Context, r *api.JailReach, an *render.Anchor) {
	if len(an.Forwards) == 0 || s.d.Net == nil {
		return
	}
	list, err := s.hostInterfaces(ctx)
	if err != nil {
		return
	}
	upAddr := func(iface string) string {
		for _, i := range list {
			if i.Name != iface {
				continue
			}
			for _, p := range i.Inet {
				if !p.Addr().IsLoopback() {
					return p.Addr().String()
				}
			}
		}
		return ""
	}
	fix := func(iface string, host string) string {
		if host != "" {
			return host
		}
		return upAddr(iface)
	}
	for i := range r.Rows {
		if r.Rows[i].Decision == "forward" && r.Rows[i].HostAddress == "" {
			r.Rows[i].HostAddress = fix(r.Rows[i].Interface, "")
		}
	}
	for i := range r.Exposed {
		if r.Exposed[i].Via == "forward" && r.Exposed[i].HostAddress == "" {
			r.Exposed[i].HostAddress = fix(r.Exposed[i].Interface, "")
		}
	}
}

// exposedOf lists what the Internet reaches on a jail (the list uses it for every jail).
func (s *Service) exposedOf(ctx context.Context, j api.Jail, refs fwRefs) []api.ExposedEntry {
	return s.reachOf(ctx, j, refs, true).Exposed
}

// Exposed lists everything the Internet reaches, across the jails the caller can see.
func (s *Service) Exposed(ctx context.Context, tunnels bool) (api.Exposed, error) {
	a := ActorFrom(ctx)
	if !a.HasScope() {
		return api.Exposed{}, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	only := ProjectFrom(ctx)
	if only != "" && !a.CanProject(only) {
		return api.Exposed{}, notFound("project", only)
	}
	s.mu.RLock()
	var jails []api.Jail
	for _, k := range sortedKeys(s.jails) {
		j := s.jails[k].jail
		p := j.Metadata.Project
		if only != "" && p != only || !a.CanProject(p) {
			continue
		}
		jails = append(jails, j)
	}
	s.mu.RUnlock()
	var out api.Exposed
	refsOf := map[string]fwRefs{}
	for _, j := range jails {
		refs, ok := refsOf[j.Metadata.Project]
		if !ok {
			var err error
			if refs, err = s.allRefs(j.Metadata.Project); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("%s/%s: %v", j.Metadata.Project, j.Metadata.Name, err))
				continue
			}
			refsOf[j.Metadata.Project] = refs
		}
		r := s.reachOf(ctx, j, refs, true)
		if r.Error != "" {
			out.Errors = append(out.Errors, fmt.Sprintf("%s/%s: %s", r.Project, r.Jail, r.Error))
			continue
		}
		for _, e := range r.Exposed {
			out.Entries = append(out.Entries, api.ExposedJail{Project: r.Project, Jail: r.Jail, ExposedEntry: e})
		}
		if tunnels {
			for _, row := range r.Rows {
				if row.Source != api.ReachTunnel || row.Decision != "pass" {
					continue
				}
				for _, fam := range strings.Split(row.Family, ",") {
					out.Tunnels = append(out.Tunnels, api.ExposedJail{Project: r.Project, Jail: r.Jail, ExposedEntry: api.ExposedEntry{
						Proto: row.Proto, Ports: portsOfRow(row.Ports), Family: fam, From: row.From, Restricted: row.Restricted, Via: "tunnel",
						Interface: row.SourceName, By: row.By}})
				}
			}
		}
	}
	return out, nil
}

func portsOfRow(p string) string {
	if p == "all" {
		return ""
	}
	return p
}

// ---- the computation ----

type segment struct {
	lo, hi int
	pass   bool
	rule   *render.Rule
	unk    bool
}

// segments walks the anchor for a packet at a hook at every port where the verdict can change, and returns the verdict of
// each stretch of ports.
func segments(an *render.Anchor, base fwsim.Packet, h fwsim.Hook) []segment {
	pts := map[int]bool{1: true}
	for _, r := range an.Rules {
		if r.Dir != h.Dir || r.Iface != h.Iface {
			continue
		}
		for _, pr := range r.Ports {
			pts[int(pr.Lo)] = true
			if pr.Hi < 65535 {
				pts[int(pr.Hi)+1] = true
			}
		}
	}
	var list []int
	for p := range pts {
		list = append(list, p)
	}
	sort.Ints(list)
	var out []segment
	for i, p := range list {
		pkt := base
		pkt.DstPort = p
		res := fwsim.Evaluate(an, pkt, h)
		hi := 65535
		if i+1 < len(list) {
			hi = list[i+1] - 1
		}
		out = append(out, segment{lo: p, hi: hi, pass: res.Verdict == fwsim.Pass, rule: res.Rule, unk: res.Verdict == fwsim.Unknown})
	}
	return out
}

// groupPass collects the passing stretches by deciding rule and returns each group's ports; the first deciding rule of
// the blocked stretches comes back as why, so that "nothing" can say what stops it.
type passGroup struct {
	rule  *render.Rule
	ports []render.PortRange
}

func groupPass(segs []segment) (groups []passGroup, blocker *render.Rule, unknown bool) {
	idx := map[*render.Rule]int{}
	for _, sg := range segs {
		if sg.unk {
			unknown = true
			continue
		}
		if !sg.pass {
			// a deny rule of a policy says more than the closing of the anchor that stands before it in the ports
			if blocker == nil || sg.rule != nil && sg.rule.Origin.Stage == render.StagePolicy && blocker.Origin.Stage != render.StagePolicy {
				blocker = sg.rule
			}
			continue
		}
		i, ok := idx[sg.rule]
		if !ok {
			i = len(groups)
			idx[sg.rule] = i
			groups = append(groups, passGroup{rule: sg.rule})
		}
		g := &groups[i]
		if n := len(g.ports); n > 0 && int(g.ports[n-1].Hi)+1 == sg.lo {
			g.ports[n-1].Hi = uint16(sg.hi)
		} else {
			g.ports = append(g.ports, render.PortRange{Lo: uint16(sg.lo), Hi: uint16(sg.hi)})
		}
	}
	return groups, blocker, unknown
}

func famText(kw string) string {
	switch kw {
	case "inet":
		return "4"
	case "inet6":
		return "6"
	}
	return "4,6"
}

func portsText(rs []render.PortRange) string {
	if len(rs) == 0 {
		return ""
	}
	var parts []string
	for _, r := range rs {
		switch {
		case r.Lo == r.Hi:
			parts = append(parts, strconv.Itoa(int(r.Lo)))
		default:
			parts = append(parts, fmt.Sprintf("%d-%d", r.Lo, r.Hi))
		}
	}
	return strings.Join(parts, ",")
}

// allPorts says whether a list of ranges covers every port.
func allPorts(rs []render.PortRange) bool {
	return len(rs) == 1 && rs[0].Lo <= 1 && rs[0].Hi == 65535
}

// matchText says what a Match names, for a reader: "any", set:NAME, or the addresses.
func matchText(m render.Match) []string {
	if m.Any {
		return nil
	}
	var out []string
	for _, t := range m.Tables {
		out = append(out, tableName(t))
	}
	if len(out) == 0 {
		for _, p := range m.Prefixes {
			out = append(out, p.String())
		}
	}
	if m.Negate {
		for i := range out {
			out[i] = "not " + out[i]
		}
	}
	return out
}

func tableName(t string) string {
	switch {
	case t == render.HostTable:
		return "host"
	case t == render.ForeignTable:
		return "other projects"
	case t == render.UplinkTable:
		return "other networks"
	case t == "urpf-failed":
		return "forged sources"
	case strings.HasPrefix(t, "s_"):
		return "set:" + strings.ReplaceAll(strings.TrimPrefix(t, "s_"), "_", "-")
	case strings.HasPrefix(t, "j_"):
		return "jail:" + strings.ReplaceAll(strings.TrimPrefix(t, "j_"), "_", "-")
	case strings.HasPrefix(t, "t_"):
		return "tag:" + strings.ReplaceAll(strings.TrimPrefix(t, "t_"), "_", "-")
	}
	return t
}

// byText says where a rule comes from.
func byText(o render.Origin) string {
	switch o.Stage {
	case render.StagePolicy, render.StageUplink:
		if o.Policy == render.ExposeOrigin {
			s := o.RuleName
			if s == "" {
				s = "expose"
			}
			if o.Comment != "" {
				s += fmt.Sprintf(" %q", o.Comment)
			}
			return s
		}
		if o.Policy == "" {
			if o.Stage == render.StageUplink {
				return "uplink protection"
			}
			return "policy"
		}
		kind := ""
		switch {
		case o.ProjectPolicy:
			kind = " (project policy)"
		}
		s := fmt.Sprintf("policy %s%s rule %d", o.Policy, kind, o.RuleNo)
		if o.RuleName != "" {
			s += " " + o.RuleName
		}
		if o.Comment != "" {
			s += fmt.Sprintf(" %q", o.Comment)
		}
		return s
	case render.StageRaw:
		return "raw PF fragment of policy " + o.Policy
	case render.StageIsolation:
		return "other projects: no allow in rule with a source admits it"
	case render.StageHost:
		return "the host is closed to jails (DNS and ping to the gateway excepted)"
	case render.StageDefault:
		return "the default of the jail's policies" + setByText(splitSetBy(o.SetBy))
	case render.StageClosed:
		return "the jail has a link and no address"
	case render.StageAntispoof:
		return "protection: only the jail's own source addresses"
	case render.StageOwnAddrs:
		return "protection: only the jail's own addresses reach it"
	case render.StageNeighbour, render.StageIPv6Control:
		return "protection of the link"
	}
	return o.Stage
}

// ruleBy is byText of a rule; the defaults say which way they go.
func ruleBy(r *render.Rule) string {
	if r != nil && r.Origin.Stage == render.StageDefault {
		v, dir := "deny", "inbound"
		if r.Action == "pass" {
			v = "allow"
		}
		if r.Dir == "in" { // on the jail's link, what comes in is what the jail sends
			dir = "outbound"
		}
		return fmt.Sprintf("the %s default is %s%s", dir, v, setByText(splitSetBy(r.Origin.SetBy)))
	}
	if r == nil {
		return "nothing of this anchor decides"
	}
	return byText(r.Origin)
}

func isTunnel(iface string) bool { return strings.HasPrefix(iface, "wg") }

func where(r render.Rule) string {
	switch {
	case r.Iface == "":
		return "link"
	case isTunnel(r.Iface):
		return "tunnel " + r.Iface
	}
	return "uplink " + r.Iface
}

func direction(r render.Rule) string {
	if r.Iface == "" && r.Dir == "in" {
		return "from"
	}
	return "to"
}

func ruleDecision(r render.Rule) api.ReachRuleDecision {
	switch r.Kind {
	case render.RuleDisabled:
		return api.RuleDisabled
	case render.RuleRaw:
		return api.RuleRaw
	}
	return api.ReachRuleDecision(r.Action)
}

func actionText(a string) string {
	if a == "pass" {
		return "allow"
	}
	return "deny"
}

// computeReach turns an anchor into rows, rules and the entries the Internet reaches.
func computeReach(an *render.Anchor, rc reachCtx, nets map[netip.Addr]string, out api.JailReach) api.JailReach {
	out.Policies = rc.policies
	out.Rows = []api.ReachRow{}
	// the rules in evaluation order; the forwards (translation rules) come first
	out.Rules = []api.ReachRule{}
	for _, f := range an.Forwards {
		to := "the uplink address"
		if f.Listen.IsValid() {
			to = f.Listen.String()
		}
		out.Rules = append(out.Rules, api.ReachRule{Where: "uplink " + f.Iface, Direction: "to", Family: famText(f.Family),
			From: strings.Join(orAny(matchText(f.From)), ", "), To: fmt.Sprintf("%s:%d -> %s:%d", to, f.HostPort, f.JailAddr, f.JailPort),
			Proto: f.Proto, Ports: fmt.Sprintf("%d->%d", f.HostPort, f.JailPort), Decision: "forward", Stage: render.StagePolicy, By: byText(f.Origin)})
	}
	raw := ""
	for _, r := range an.Rules {
		line := ""
		if r.Line >= 0 && r.Line < len(an.Lines) {
			line = an.Lines[r.Line]
		}
		rr := api.ReachRule{Where: where(r), Direction: direction(r), Family: famText(r.Family), From: strings.Join(orAny(matchText(r.From)), ", "),
			To: strings.Join(orAny(matchText(r.To)), ", "), Proto: r.Proto, Ports: portsText(r.Ports), Decision: ruleDecision(r), Stage: r.Origin.Stage,
			By: ruleBy(&r), PF: line}
		out.Rules = append(out.Rules, rr)
		if r.Kind == render.RuleRaw && raw == "" {
			raw = r.Origin.Policy
		}
	}
	// the defaults of the anchor
	for _, r := range an.Rules {
		if r.Origin.Stage != render.StageDefault || r.Iface != "" {
			continue
		}
		v := "deny"
		if r.Action == "pass" {
			v = "allow"
		}
		if r.Dir == "out" {
			out.DefaultIn, out.DefaultInBy = v, splitSetBy(r.Origin.SetBy)
		} else {
			out.DefaultOut, out.DefaultOutBy = v, splitSetBy(r.Origin.SetBy)
		}
	}
	// the rows of the Internet and of the tunnels: the pass rules on an interface that is not the jail's link
	type rowKey struct{ source, name, proto, ports, from, by string }
	merged := map[rowKey]int{}
	addRow := func(row api.ReachRow) {
		k := rowKey{row.Source, row.SourceName, row.Proto, row.Ports, strings.Join(row.From, ","), row.By}
		if i, ok := merged[k]; ok && row.Decision == "pass" {
			if !strings.Contains(out.Rows[i].Family, row.Family) {
				out.Rows[i].Family = "4,6"
			}
			return
		}
		merged[k] = len(out.Rows)
		out.Rows = append(out.Rows, row)
	}
	for _, f := range an.Forwards {
		row := api.ReachRow{Source: api.ReachInternet, Family: famText(f.Family), Proto: f.Proto, Ports: strconv.Itoa(f.JailPort), From: matchText(f.From),
			Restricted: !f.From.Any, Decision: "forward", By: byText(f.Origin), Policy: f.Origin.Policy, Rule: f.Origin.RuleNo, RuleName: f.Origin.RuleName,
			Stage: f.Origin.Stage, Interface: f.Iface, HostPort: f.HostPort}
		if f.Listen.IsValid() {
			row.HostAddress = f.Listen.String()
		}
		addRow(row)
		out.Exposed = append(out.Exposed, api.ExposedEntry{Proto: f.Proto, Ports: strconv.Itoa(f.JailPort), Family: famText(f.Family), Address: f.JailAddr.String(),
			HostAddress: row.HostAddress, HostPort: f.HostPort, From: orAny(matchText(f.From)), Restricted: !f.From.Any, Via: "forward", Interface: f.Iface,
			Network: nets[f.JailAddr], By: byText(f.Origin)})
	}
	for _, r := range an.Rules {
		if r.Iface == "" || r.Kind != render.RuleFilter || r.Action != "pass" || r.Dir != "in" || forwardPass(an, r) || uplinkBlocked(an, r) {
			continue
		}
		src := api.ReachInternet
		name := ""
		if isTunnel(r.Iface) {
			src, name = api.ReachTunnel, r.Iface
		}
		ports := portsText(r.Ports)
		if ports == "" && (r.Proto == "tcp" || r.Proto == "udp" || r.Proto == "") {
			ports = "all"
		}
		proto := r.Proto
		if proto == "" {
			proto = "all"
		}
		addRow(api.ReachRow{Source: src, SourceName: name, Family: famText(r.Family), Proto: proto, Ports: ports, From: matchText(r.From), Restricted: !r.From.Any,
			Decision: "pass", By: byText(r.Origin), Policy: r.Origin.Policy, Rule: r.Origin.RuleNo, RuleName: r.Origin.RuleName, Stage: r.Origin.Stage, Interface: r.Iface})
		if src != api.ReachInternet {
			continue
		}
		for _, p := range r.To.Prefixes {
			if !p.IsSingleIP() {
				continue
			}
			out.Exposed = append(out.Exposed, api.ExposedEntry{Proto: proto, Ports: portsOfRow(ports), Family: famText(r.Family), Address: p.Addr().String(),
				From: orAny(matchText(r.From)), Restricted: !r.From.Any, Via: "uplink", Interface: r.Iface, Network: nets[p.Addr()], By: byText(r.Origin)})
		}
	}
	// the link: what the project's jails, the host and the other projects reach, by walking the anchor
	for _, class := range []struct{ source string }{{api.ReachJails}, {api.ReachHost}, {api.ReachOther}} {
		out.Rows = append(out.Rows, linkRows(an, rc, class.source)...)
	}
	out.Out = linkOut(an, rc)
	if raw != "" {
		out.Notes = append(out.Notes, "the raw PF fragment of policy "+raw+" is not evaluated: what it passes or blocks is not in these rows")
	}
	if len(out.Exposed) > 0 {
		out.Notes = append(out.Notes, "what the host's own pf.conf drops before the jailops anchor is not shown (jailops doctor, pf.inbound)")
	}
	if len(an.Uplinks) > 0 || len(an.Forwards) > 0 {
		out.Notes = append(out.Notes, "what the Internet reaches is what these rows say only if the host's pf.conf blocks inbound traffic by default: JailOps opens the uplink for what the policies admit and never closes it (jailops doctor: pf.host_default_deny)")
	}
	sortExposed(out.Exposed)
	return out
}

// uplinkBlocked says whether a deny rule on the same uplink drops everything the pass rule lets in: the deny rules of the
// policies come before the allow rules, so such a pass is dead and is not listed as reaching the jail.
func uplinkBlocked(an *render.Anchor, pass render.Rule) bool {
	for _, b := range an.Rules {
		if b.Iface != pass.Iface || b.Kind != render.RuleFilter || b.Action != "block" || b.Dir != "in" || b.Family != pass.Family {
			continue
		}
		if b.Proto != "" && b.Proto != pass.Proto || !b.From.Any {
			continue
		}
		if len(b.Ports) > 0 {
			covered := len(pass.Ports) > 0
			for _, p := range pass.Ports {
				ok := false
				for _, q := range b.Ports {
					if q.Lo <= p.Lo && p.Hi <= q.Hi {
						ok = true
					}
				}
				covered = covered && ok
			}
			if !covered {
				continue
			}
		}
		if !b.To.Any && len(pass.To.Prefixes) > 0 && !slices.ContainsFunc(pass.To.Prefixes, func(p netip.Prefix) bool { return b.To.Contains(p.Addr()) }) {
			continue
		}
		return true
	}
	return false
}

// forwardPass says whether a pass rule on an uplink is the one that follows a forward's translation: it belongs to the
// forward and is not a rule of its own.
func forwardPass(an *render.Anchor, r render.Rule) bool {
	for _, f := range an.Forwards {
		if f.Iface != r.Iface || f.Family != r.Family || f.Proto != r.Proto || f.Origin.Policy != r.Origin.Policy || f.Origin.RuleNo != r.Origin.RuleNo {
			continue
		}
		if len(r.Ports) == 1 && int(r.Ports[0].Lo) == f.JailPort && int(r.Ports[0].Hi) == f.JailPort && r.To.Contains(f.JailAddr) {
			return true
		}
	}
	return false
}

func orAny(s []string) []string {
	if len(s) == 0 {
		return []string{"any"}
	}
	return s
}

func sortExposed(e []api.ExposedEntry) {
	sort.SliceStable(e, func(i, j int) bool {
		if e[i].Family != e[j].Family {
			return e[i].Family > e[j].Family // 6 before 4, as the design lists them
		}
		if e[i].Proto != e[j].Proto {
			return e[i].Proto < e[j].Proto
		}
		return e[i].Ports < e[j].Ports
	})
}

func (rc reachCtx) jailAddr(fam string) (netip.Addr, bool) {
	for _, a := range rc.addrs {
		if fam == "inet" && a.Is4() || fam == "inet6" && a.Is6() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// probe is the address a class of source is probed with, in a family.
func (rc reachCtx) probe(source, fam string, jail netip.Addr) (netip.Addr, bool) {
	switch source {
	case api.ReachHost:
		for _, a := range rc.host {
			a = a.Unmap()
			if (fam == "inet") == a.Is4() && !a.IsLinkLocalUnicast() {
				return a, true
			}
		}
	case api.ReachJails:
		for _, p := range rc.own {
			if (fam == "inet") == p.Addr().Is4() {
				return lastOther(p, jail), true
			}
		}
	case api.ReachOther:
		for _, p := range rc.foreign {
			if (fam == "inet") == p.Addr().Is4() {
				return p.Addr().Next(), true
			}
		}
	case api.ReachInternet:
		if fam == "inet" {
			return internetV4, true
		}
		return internetV6, true
	}
	return netip.Addr{}, false
}

// probes are the addresses a class of source is probed with, in a family. The other projects reach the jail like the
// Internet does, through the allow in rules that have a source, so each source an anchor rule names inside their networks
// is probed too, not only the first address of a network.
func (rc reachCtx) probes(an *render.Anchor, source, fam string, jail netip.Addr) []netip.Addr {
	first, ok := rc.probe(source, fam, jail)
	if !ok {
		return nil
	}
	out := []netip.Addr{first}
	if source != api.ReachOther {
		return out
	}
	add := func(a netip.Addr) {
		if (fam == "inet") == a.Is4() && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	for _, r := range an.Rules {
		if r.Kind != render.RuleFilter || r.Iface != "" || r.Dir != "out" || r.Action != "pass" || r.From.Negate {
			continue
		}
		for _, p := range r.From.Prefixes {
			for _, fp := range rc.foreign {
				if fp.Masked().Contains(p.Addr()) && p.Bits() >= fp.Bits() {
					add(p.Addr())
				}
			}
		}
	}
	return out
}

// lastOther is the highest address of a prefix that is not the jail's own.
func lastOther(p netip.Prefix, jail netip.Addr) netip.Addr {
	a := p.Masked().Addr()
	b := a.AsSlice()
	for i := range b {
		for bit := 0; bit < 8; bit++ {
			idx := i*8 + bit
			if idx >= p.Bits() {
				b[i] |= 1 << (7 - bit)
			}
		}
	}
	last, _ := netip.AddrFromSlice(b)
	if last == jail {
		last = last.Prev()
	}
	return last
}

var sourceTitle = map[string]string{
	api.ReachJails: "the jails of the project", api.ReachHost: "the host", api.ReachOther: "other projects"}

// linkRows walks the anchor for a probe of a class of source at the jail's own link (what leaves the host for the jail) and
// returns the rows: what passes, or why nothing does.
func linkRows(an *render.Anchor, rc reachCtx, source string) []api.ReachRow {
	type key struct {
		proto, ports, by, from string
		rule                   *render.Rule
	}
	type acc struct {
		row api.ReachRow
		fam map[string]bool
	}
	var order []key
	rows := map[key]*acc{}
	var blocker *render.Rule
	unknown := false
	for _, fam := range []string{"inet", "inet6"} {
		dst, ok := rc.jailAddr(fam)
		if !ok {
			continue
		}
		for _, src := range rc.probes(an, source, fam, dst) {
			for _, proto := range []string{"tcp", "udp"} {
				base := fwsim.Packet{Family: fam, Proto: proto, Src: src, Dst: dst}
				groups, b, unk := groupPass(segments(an, base, fwsim.Hook{Dir: "out"}))
				if blocker == nil {
					blocker = b
				}
				unknown = unknown || unk
				for _, g := range groups {
					ports := portsText(g.ports)
					if allPorts(g.ports) {
						ports = "all"
					}
					var from []string
					if source == api.ReachOther {
						from = orAny(matchText(g.rule.From)) // who it is limited to: any, a prefix or a set
					}
					k := key{proto, ports, ruleBy(g.rule), strings.Join(from, ","), g.rule}
					a, ok := rows[k]
					if !ok {
						a = &acc{row: api.ReachRow{Source: source, Proto: proto, Ports: ports, From: from, Restricted: len(from) > 0 && from[0] != "any", Decision: "pass", By: ruleBy(g.rule),
							Policy: g.rule.Origin.Policy, Rule: g.rule.Origin.RuleNo, RuleName: g.rule.Origin.RuleName, Stage: g.rule.Origin.Stage}, fam: map[string]bool{}}
						rows[k] = a
						order = append(order, k)
					}
					a.fam[famText(fam)] = true
				}
			}
		}
	}
	var out []api.ReachRow
	for _, k := range order {
		a := rows[k]
		a.row.Family = famJoin(a.fam)
		out = append(out, a.row)
	}
	if len(out) == 0 {
		by, stage := ruleBy(blocker), ""
		if blocker != nil {
			stage = blocker.Origin.Stage
		}
		if unknown {
			by += " (a raw PF fragment is not evaluated)"
		}
		out = append(out, api.ReachRow{Source: source, Family: "4,6", Proto: "tcp,udp", Decision: "none", By: by, Stage: stage})
	}
	return out
}

func famJoin(m map[string]bool) string {
	switch {
	case m["4"] && m["6"]:
		return "4,6"
	case m["4"]:
		return "4"
	}
	return "6"
}

// linkOut says what the jail reaches: probes from the jail's address to the Internet, the host, the project's jails and
// the other projects, at the jail's own link (what it sends).
func linkOut(an *render.Anchor, rc reachCtx) api.ReachOut {
	one := func(source string) string {
		var parts []string
		var blocker *render.Rule
		for _, fam := range []string{"inet", "inet6"} {
			src, ok := rc.jailAddr(fam)
			if !ok {
				continue
			}
			dst, ok := rc.probe(source, fam, src)
			if !ok {
				continue
			}
			for _, proto := range []string{"tcp", "udp"} {
				groups, b, _ := groupPass(segments(an, fwsim.Packet{Family: fam, Proto: proto, Src: src, Dst: dst}, fwsim.Hook{Dir: "in"}))
				if blocker == nil {
					blocker = b
				}
				for _, g := range groups {
					t := proto + " " + portsText(g.ports)
					if allPorts(g.ports) {
						t = proto + " all ports"
					}
					if !slices.Contains(parts, t) {
						parts = append(parts, t)
					}
				}
			}
		}
		if len(parts) == 0 {
			if blocker != nil {
				return "nothing (" + ruleBy(blocker) + ")"
			}
			return "nothing"
		}
		return strings.Join(parts, ", ")
	}
	return api.ReachOut{Internet: one(api.ReachInternet), Host: one(api.ReachHost), Jails: one(api.ReachJails), OtherProjects: one(api.ReachOther)}
}

// splitSetBy is the list in Origin.SetBy.
func splitSetBy(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ", ")
}

// setByText says which policies state deny: " (set by a, b [project])", or " (no policy says deny)".
func setByText(by []string) string {
	if len(by) == 0 {
		return " (no policy says deny)"
	}
	return " (set by " + strings.Join(by, ", ") + ")"
}
