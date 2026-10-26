package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Anchor names. Project names are lowercase letters, digits and "-", so a
// component with a dot is reserved for JailOps itself (verified on FreeBSD:
// pfctl refuses to modify anchors whose name begins with an underscore).
const (
	AnchorRoot   = "jailops"
	AnchorShared = "jailops/shared.pf"
)

// JailAnchor returns the anchor path of a jail given its project-qualified
// key: "web_nginx" is "jailops/web/nginx". A key without a project belongs to
// no project and gets "jailops/<key>", an anchor no jail uses (a name to test
// rules under).
func JailAnchor(key string) string {
	p, n, ok := strings.Cut(key, "_")
	if !ok || p == "" || n == "" {
		return AnchorRoot + "/" + key
	}
	return JailAnchorFor(p, n)
}

// JailAnchorFor returns the anchor path jailops/<project>/<jail>.
func JailAnchorFor(project, name string) string { return ProjectAnchor(project) + "/" + name }

// ProjectAnchor returns the parent anchor of a project's jail anchors.
func ProjectAnchor(project string) string { return AnchorRoot + "/" + project }

// ProjectAnchorRules returns the ruleset of a project's parent anchor: it
// makes PF evaluate the jail anchors below it. UNVERIFIED on FreeBSD: that a
// wildcard anchor rule inside "jailops/<project>" evaluates "jailops/<project>/*"
// when the top level wildcard ("jailops/*") evaluates the project anchor.
func ProjectAnchorRules() string {
	return "nat-anchor \"*\"\nrdr-anchor \"*\"\nanchor \"*\"\n"
}

// NetAnchor returns the anchor path holding the NAT rules of a network.
func NetAnchor(network string) string { return AnchorRoot + "/net." + network }

// AnchorLines returns the three lines the administrator adds to pf.conf once.
// JailOps never edits pf.conf. UNVERIFIED: wildcard anchors ("jailops/*")
// evaluating anchors that are loaded later (prototype 3).
func AnchorLines() string {
	return "nat-anchor \"jailops/*\"\nrdr-anchor \"jailops/*\"\nanchor \"jailops/*\"\n"
}

// AllowedSourceTable is the per-jail table of source addresses the jail may
// use (its own and fe80::/10). Every anchor defines the tables it uses.
const AllowedSourceTable = "jail_addrs"

// PFInput is the input of PFAnchor.
type PFInput struct {
	Jail string
	// Interfaces are the host-side epair ends of the jail. Rules are
	// attached to them: direction "in" of a rule means traffic into the
	// jail (PF "out" on the host-side end), "out" means traffic from the
	// jail (PF "in"). Needs net.link.bridge.pfil_member=1 (verified on FreeBSD 15.1, design 18.9; the host pass
	// asserts it).
	Interfaces []string
	// Gateways are the gateway interfaces (the l3 end of the gateway pair) of the jail's networks. What the host
	// sends to the jail, and what it routes to the jail from other networks, leaves the host on them, and on the
	// jail's own port the pf hook never sees it: the rules for traffic into the jail stand on the gateway
	// interface too, restricted to the jail's addresses. Verified on FreeBSD 15.1 (design section 5).
	Gateways []string
	// Addresses are the jail's own addresses. It must not be empty.
	Addresses []netip.Addr
	// LinkLocal are the jail's own link-local addresses (one per interface, formed by the kernel from the MAC
	// JailOps sets: ipam.LinkLocalFromMAC). With them the jail may use only its own link-local addresses as the source
	// of neighbour discovery and receive only its own; without them the whole of fe80::/10 is allowed.
	LinkLocal []netip.Addr
	// NoAdvertise forbids every neighbour advertisement from the jail. Every jail with an IPv6 address sets it: a jail
	// holds its address as /128 in both IPv6 modes (design 18.13), reaches everything through the gateway, and the
	// host holds a permanent neighbor entry for it (render.OpSetNeighbor), so nobody ever asks the jail for its
	// address and it never has to answer. (There is no longer a jail that needs to advertise.)
	NoAdvertise bool
	// Tags are the jail's own tags (rendered as a comment only).
	Tags []string
	// TagMembers maps a tag to the addresses of every jail that has it.
	TagMembers map[string][]netip.Addr
	Policy     api.FirewallPolicySpec
	Sets       map[string]api.AddressSetSpec
	// Uplink is the interface redirects are applied on; required when a
	// rule has a redirect.
	Uplink string
	// Antispoof adds an antispoof rule for the jail interfaces.
	// UNVERIFIED: antispoof on bridge members, and its interaction with
	// IPv6 proxy mode (acceptance test 4 of design section 5).
	Antispoof bool
	// AllowRaw must be set by the caller after checking the admin role; a
	// raw fragment is otherwise refused.
	AllowRaw bool
	// Foreign are the subnets of the networks that belong to other projects. Nothing from them reaches the jail, and
	// the jail reaches nothing in them, unless a rule of a policy says so first (projects are isolated from each
	// other by default; a shared service lives in a project of its own and is opened by rules).
	Foreign []netip.Prefix
	// Mandatory is the project's mandatory policy. It is rendered before the
	// jail's own rules, so a jail's policy cannot override it.
	Mandatory *PFMandatory
}

// PFMandatory is a project's mandatory firewall policy. Its own defaults are
// ignored: the caller copies them into PFInput.Policy when the jail has no
// policy of its own.
type PFMandatory struct {
	Name     string
	Policy   api.FirewallPolicySpec
	AllowRaw bool
}

type family struct {
	kw    string // inet, inet6
	addrs []netip.Addr
}

// PFAnchor renders the ruleset of anchor jailops/<jail>. Rules are
// first-match (every typed rule is "quick"); defaults come last. Tables for
// address sets and tags are defined inside the anchor so that one load is
// atomic. UNVERIFIED: acceptance by pfctl -n, mixed-family tables in
// family-restricted rules.
func PFAnchor(in PFInput) (string, error) {
	if err := validate.JailKey("jail", in.Jail); err != nil {
		return "", err
	}
	if len(in.Interfaces) == 0 {
		return "", fmt.Errorf("render: pf anchor needs at least one interface")
	}
	for _, i := range append(append([]string(nil), in.Interfaces...), in.Gateways...) {
		if err := validate.IfName("interface", i); err != nil {
			return "", err
		}
	}
	if _, err := validate.FirewallPolicySpec(in.Policy); err != nil {
		return "", err
	}
	var mand []api.FirewallRule
	if m := in.Mandatory; m != nil {
		if err := validate.Name("mandatory policy", m.Name); err != nil {
			return "", err
		}
		if _, err := validate.FirewallPolicySpec(m.Policy); err != nil {
			return "", err
		}
		if m.Policy.Raw != "" && !m.AllowRaw {
			return "", api.Errorf(api.CodeForbidden, "raw PF fragments require the admin role")
		}
		mand = m.Policy.Rules
	}
	for _, t := range in.Tags {
		if err := validate.Tag("tag", t); err != nil {
			return "", err
		}
	}
	if in.Policy.Raw != "" && !in.AllowRaw {
		return "", api.Errorf(api.CodeForbidden, "raw PF fragments require the admin role")
	}
	addrs := uniqAddrs(in.Addresses)
	if len(addrs) == 0 {
		return "", fmt.Errorf("render: jail %s has no addresses", in.Jail)
	}
	var fams []family
	for _, kw := range []string{"inet", "inet6"} {
		f := family{kw: kw}
		for _, a := range addrs {
			if a.Is4() == (kw == "inet") && a.IsValid() && a.Zone() == "" {
				f.addrs = append(f.addrs, a)
			}
		}
		if len(f.addrs) > 0 {
			fams = append(fams, f)
		}
	}
	ifs := list(in.Interfaces)

	// Collect referenced tables.
	setRefs, tagRefs := map[string]bool{}, map[string]bool{}
	for _, r := range append(append([]api.FirewallRule(nil), mand...), in.Policy.Rules...) {
		for _, e := range append(append([]string(nil), r.From...), r.To...) {
			k, v, err := validate.ParseEndpoint(e)
			if err != nil {
				return "", err
			}
			switch k {
			case validate.EndpointSet:
				setRefs[v] = true
			case validate.EndpointTag:
				tagRefs[v] = true
			}
		}
	}

	var b builder
	b.line("%s", Header)
	b.line("# anchor %s", JailAnchor(in.Jail))
	if len(in.Tags) > 0 {
		tags := append([]string(nil), in.Tags...)
		sort.Strings(tags)
		b.line("# tags: %s", strings.Join(tags, " "))
	}

	for _, n := range sortedKeys(setRefs) {
		members, err := validate.FlattenSet(n, in.Sets)
		if err != nil {
			return "", err
		}
		b.line("%s", tableLine("s_"+n, members))
	}
	for _, n := range sortedKeys(tagRefs) {
		var members []string
		for _, a := range uniqAddrs(in.TagMembers[n]) {
			members = append(members, a.String())
		}
		b.line("%s", tableLine("t_"+strings.ReplaceAll(n, "-", "_"), members))
	}

	// Translation rules come before filter rules.
	rdrRules := func(rules []api.FirewallRule) error {
		for i, r := range rules {
			if r.Redirect == nil {
				continue
			}
			if err := validate.IfName("uplink", in.Uplink); err != nil {
				return fmt.Errorf("render: rules[%d] has a redirect and needs a valid uplink: %w", i, err)
			}
			b.line("# rdr %s", ruleLabel(i, r))
			for _, f := range fams {
				from, ok := endpoints(r.From, f, nil)
				if !ok {
					continue
				}
				to := "(" + in.Uplink + ")"
				if l := r.Redirect.Listen; l != "" {
					a, err := netip.ParseAddr(l)
					if err != nil {
						return fmt.Errorf("render: rules[%d]: listen address %q: %w", i, l, err)
					}
					if a.Is4() != (f.kw == "inet") {
						continue
					}
					to = a.String()
				}
				b.line("rdr on %s %s proto %s from %s to %s port %d -> %s port %d",
					in.Uplink, f.kw, r.Proto, from, to, r.Redirect.HostPort, f.addrs[0], r.Redirect.JailPort)
			}
		}
		return nil
	}
	if err := rdrRules(mand); err != nil {
		return "", err
	}
	if err := rdrRules(in.Policy.Rules); err != nil {
		return "", err
	}

	own6 := famAddrs(fams, "inet6")
	ll := linkLocals(in.LinkLocal)
	// The jail's own sources for IPv6 control traffic: its addresses and its own link-local addresses (the whole of
	// fe80::/10 when the planner could not derive them).
	src6 := append(addrStrings(own6), llStrings(ll)...)
	if len(ll) == 0 {
		src6 = append(src6, "fe80::/10")
	}
	// The table of the jail's own addresses: the only sources it may use, and the only destinations that reach it. It
	// holds its link-local address (fe80::/10 when unknown), or IPv6 neighbour discovery breaks. A negated address
	// list ("from ! { a b }") is a pf syntax error; a negated table is not, and the anchor defines the table it names
	// (verified on FreeBSD 15.1).
	{
		members := append(addrStrings(addrs), llStrings(ll)...)
		if len(ll) == 0 {
			members = append(members, "fe80::/10")
		}
		b.line("%s", tableLine(AllowedSourceTable, members))
	}
	if len(own6) > 0 {
		// IPv6 control traffic. A jail is never a router: no router advertisement or redirect leaves it. The unspecified
		// address :: cannot be a member of a table (pf accepts it and stores nothing), so duplicate address detection
		// (a neighbour solicitation to a solicited-node group) has a rule of its own before the antispoof block.
		b.line("block in quick on %s inet6 proto icmp6 from any to any icmp6-type { routeradv, redir }", ifs)
		b.line("pass in quick on %s inet6 proto icmp6 from :: to ff02::1:ff00:0/104 icmp6-type neighbrsol", ifs)
	}
	if in.Antispoof {
		b.line("block in quick on %s from ! <%s> to any", ifs, AllowedSourceTable)
	}
	if len(own6) > 0 {
		// pf cannot read the target of a neighbour message, so a jail's advertisement for another jail's address cannot
		// be told from its own. What can be said is said: only its own addresses are the source, an advertisement never
		// goes to a multicast group (an unsolicited one would reach every jail and the host), and only messages
		// addressed to the jail's own addresses and solicited-node groups reach it. Unicast messages between jails of
		// one network stay possible: see docs/networking-ipv6.md.
		from6 := listStr(src6)
		if in.NoAdvertise {
			b.line("block in quick on %s inet6 proto icmp6 from any to any icmp6-type neighbradv", ifs)
			b.line("pass in quick on %s inet6 proto icmp6 from %s to any icmp6-type neighbrsol", ifs, from6)
		} else {
			b.line("block in quick on %s inet6 proto icmp6 from any to ff02::/16 icmp6-type neighbradv", ifs)
			b.line("pass in quick on %s inet6 proto icmp6 from %s to any icmp6-type { neighbrsol, neighbradv }", ifs, from6)
		}
		b.line("pass in quick on %s inet6 proto icmp6 from %s to ff02::/16 icmp6-type { listenrep, listendone, 143 }", ifs, from6)
		b.line("pass in quick on %s inet6 proto icmp6 from %s to any icmp6-type { unreach, toobig, timex, paramprob }", ifs, from6)
		// the host forwarding from a jail to a neighbour on the same interface would send a redirect; a jail has no
		// on-link neighbour and must not learn one
		b.line("block out quick on %s inet6 proto icmp6 from any to any icmp6-type { redir, routeradv }", ifs)
		b.line("pass out quick on %s inet6 proto icmp6 from any to %s icmp6-type { neighbrsol, neighbradv }", ifs, listStr(ndDests(own6, ll)))
		b.line("pass out quick on %s inet6 proto icmp6 from any to any icmp6-type { unreach, toobig, timex, paramprob }", ifs)
	}
	// Only the jail's own addresses reach it: an address it adds for itself (an alias) receives nothing, and no
	// broadcast (DHCP) or multicast other than neighbour discovery gets in.
	b.line("block drop out quick on %s from any to ! <%s>", ifs, AllowedSourceTable)
	if len(famAddrs(fams, "inet")) > 0 {
		b.line("block drop out quick on %s inet proto icmp from any to any icmp-type redir", ifs)
	}

	if in.Mandatory != nil {
		b.line("# project policy %s", in.Mandatory.Name)
		filterRules(&b, mand, fams, ifs, false)
		if raw := in.Mandatory.Policy.Raw; raw != "" {
			b.line("# raw fragment (admin)")
			b.line("%s", strings.TrimRight(strings.ReplaceAll(raw, "\r\n", "\n"), "\n"))
		}
		b.line("# jail policy")
	}
	filterRules(&b, in.Policy.Rules, fams, ifs, false)

	if in.Policy.Raw != "" {
		b.line("# raw fragment (admin)")
		raw := strings.ReplaceAll(in.Policy.Raw, "\r\n", "\n")
		b.line("%s", strings.TrimRight(raw, "\n"))
	}

	if len(in.Foreign) > 0 {
		fp := make([]string, 0, len(in.Foreign))
		for _, p := range in.Foreign {
			fp = append(fp, p.Masked().String())
		}
		sort.Strings(fp)
		b.line("%s", tableLine("foreign", fp))
		b.line("# other projects: closed unless a rule above opened it")
		b.line("block drop out quick on %s from <foreign> to any", ifs)
		b.line("block drop in quick on %s from any to <foreign>", ifs)
	}
	b.line("# defaults: final (quick), so that no rule of the host's own pf.conf after the anchor line changes them")
	inAct, outAct := "block drop", "pass"
	if in.Policy.DefaultIn == "allow" {
		inAct = "pass"
	}
	if in.Policy.DefaultOut == "deny" {
		outAct = "block drop"
	}
	b.line("%s out quick on %s all", inAct, ifs)
	b.line("%s in quick on %s all", outAct, ifs)
	// What the host itself sends to the jail, or routes to it from another network or from the internet, leaves the
	// host on the gateway interface and is never seen on the jail's own port: the rules for traffic into the jail
	// stand there too, restricted to the jail's own addresses. (Verified on FreeBSD 15.1: without them a deny-in
	// policy let the host and routed traffic through.)
	if len(in.Gateways) > 0 {
		gifs := listStr(in.Gateways)
		b.line("# into the jail from the host and from routed networks")
		if len(own6) > 0 {
			// No router advertisement or redirect leaves the host on the gateway interface (no SLAAC, ever; what a jail
			// is sent on its own port is not filtered by pf, so this is where it is stopped).
			b.line("block drop out quick on %s inet6 proto icmp6 from any to any icmp6-type { routeradv, redir }", gifs)
			// neighbour discovery towards the jail's own addresses, or the host could not reach it; and the ICMPv6
			// errors (destination unreachable, packet too big, time exceeded, parameter problem) that must never be
			// blocked, whatever the policy says: path MTU discovery of a flow into the jail depends on them.
			b.line("pass out quick on %s inet6 proto icmp6 from any to %s icmp6-type { neighbrsol, neighbradv }", gifs, listStr(ndDests(own6, ll)))
			b.line("pass out quick on %s inet6 proto icmp6 from any to %s icmp6-type { unreach, toobig, timex, paramprob }", gifs, listStr(addrStrings(own6)))
		}
		if in.Mandatory != nil {
			filterRules(&b, mand, fams, gifs, true)
		}
		filterRules(&b, in.Policy.Rules, fams, gifs, true)
		if len(in.Foreign) > 0 {
			for _, f := range fams {
				b.line("block drop out quick on %s %s from <foreign> to %s", gifs, f.kw, listStr(addrStrings(f.addrs)))
			}
		}
		for _, f := range fams {
			b.line("%s out quick on %s %s from any to %s", inAct, gifs, f.kw, listStr(addrStrings(f.addrs)))
		}
	}
	out, err := b.result()
	if err != nil {
		return "", err
	}
	return labelPassRules(JailAnchor(in.Jail), out), nil
}

var (
	tableDefRE = regexp.MustCompile(`(?m)^table\s+<([A-Za-z][A-Za-z0-9_]*)>[^{\n]*\{([^}]*)\}`)
	tableUseRE = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9_]*)>`)
)

// labelPassRules puts a label on every stateful pass rule of an anchor. The label is a hash of the anchor's name, the
// rule's text, the members of the tables the rule names, and everything that comes before the rule in the anchor
// (every rule above it, with the members of their tables). It is the same for as long as the rule means the same
// thing at the same place, and different as soon as it does not: a rule removed, a port changed, an address taken out
// of a set, or a deny put in front of it, which changes what the rule lets through. The reconciler kills the states of
// labels that are gone after it loads an anchor: pf keeps the states of a rule that was removed, and a flow that is no
// longer allowed would go on until it ended. The price is that a rule inserted in the middle of an anchor ends the
// flows of the pass rules below it, which then have to reconnect.
func labelPassRules(anchor, text string) string {
	tables := map[string]string{}
	for _, m := range tableDefRE.FindAllStringSubmatch(text, -1) {
		tables[m[1]] = strings.Join(strings.Fields(m[2]), " ")
	}
	var out strings.Builder
	raw := false                           // the administrator's raw fragment is passed on untouched
	above := sha256.Sum256([]byte(anchor)) // what comes before: a running hash of the rules seen so far
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "# ") {
			raw = ln == "# raw fragment (admin)"
		}
		rule := strings.TrimSpace(ln) != "" && !strings.HasPrefix(ln, "#") && !strings.HasPrefix(ln, "table ")
		if rule {
			uses := ""
			for _, m := range tableUseRE.FindAllStringSubmatch(ln, -1) {
				if body, ok := tables[m[1]]; ok {
					uses += m[1] + "=" + body + "\n"
				}
			}
			if !raw && strings.HasPrefix(ln, "pass ") && !strings.Contains(ln, " no state") && !strings.Contains(ln, " label ") {
				h := sha256.New()
				h.Write(above[:])
				h.Write([]byte(ln + "\n" + uses))
				ln += ` label "` + pfctl.LabelPrefix + hex.EncodeToString(h.Sum(nil))[:12] + `"`
			}
			next := sha256.New()
			next.Write(above[:])
			next.Write([]byte(ln + "\n" + uses))
			copy(above[:], next.Sum(nil))
		}
		out.WriteString(ln)
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func ruleLabel(i int, r api.FirewallRule) string {
	if r.Name != "" {
		return fmt.Sprintf("rule %d: %s", i+1, r.Name)
	}
	return fmt.Sprintf("rule %d", i+1)
}

func tableLine(name string, members []string) string {
	if len(members) == 0 {
		return fmt.Sprintf("table <%s> persist", name)
	}
	return fmt.Sprintf("table <%s> persist { %s }", name, strings.Join(members, ", "))
}

// endpoints renders a from/to list for one address family. def is used when
// the list is empty (nil means "any"). The bool is false when the user listed
// entries but none applies to this family.
func endpoints(entries []string, f family, def []netip.Addr) (string, bool) {
	if len(entries) == 0 {
		if def == nil {
			return "any", true
		}
		parts := make([]string, len(def))
		for i, a := range def {
			parts[i] = a.String()
		}
		return listStr(parts), true
	}
	var parts []string
	for _, e := range entries {
		k, v, err := validate.ParseEndpoint(e)
		if err != nil {
			return "", false // validated earlier; defensive
		}
		switch k {
		case validate.EndpointAny:
			return "any", true
		case validate.EndpointSet:
			parts = append(parts, "<s_"+v+">")
		case validate.EndpointTag:
			parts = append(parts, "<t_"+strings.ReplaceAll(v, "-", "_")+">")
		case validate.EndpointAddr:
			if isV4Text(v) == (f.kw == "inet") {
				parts = append(parts, v)
			}
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return listStr(parts), true
}

func isV4Text(s string) bool { return !strings.Contains(s, ":") }

func listStr(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func list(items []string) string { return listStr(items) }

func portList(ports []string) string {
	out := make([]string, len(ports))
	for i, p := range ports {
		lo, hi, _ := validate.ParsePortSpec(p)
		if lo == hi {
			out[i] = fmt.Sprint(lo)
		} else {
			out[i] = fmt.Sprintf("%d:%d", lo, hi)
		}
	}
	return listStr(out)
}

func uniqAddrs(in []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(in))
	for _, a := range in {
		if a.IsValid() {
			out = append(out, a.WithZone("").Unmap())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Compare(out[j]) < 0 })
	u := out[:0]
	for i, a := range out {
		if i == 0 || a != out[i-1] {
			u = append(u, a)
		}
	}
	return u
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PFNet renders the anchor of a network (NetAnchor(name)): the NAT rule when the network has one, and the pass of
// the network's own gateway pair. The pair is how the host and routed traffic reach the jails, and a packet that
// enters the bridge there has already been judged by the jail's anchor on its way out of the gateway, but pf keeps no
// state across the two hooks (the direction is the opposite), so on a host whose ruleset blocks by default the packet
// would be dropped at the bridge. The pass is stateless and covers only the pair. (Found on a host with
// "block log all": the host could not reach any jail.)
func PFNet(name string, spec api.NetworkSpec) (string, error) {
	if err := validate.Name("network", name); err != nil {
		return "", err
	}
	if err := validate.NetworkSpec(spec); err != nil {
		return "", err
	}
	member, _, err := ipam.GatewayNames(spec.Bridge)
	if err != nil {
		return "", err
	}
	var b builder
	b.line("%s", Header)
	b.line("# anchor %s", NetAnchor(name))
	if spec.IPv4 != nil && spec.IPv4.NAT {
		p, err := netip.ParsePrefix(spec.IPv4.Subnet)
		if err != nil {
			return "", err
		}
		b.line("nat on %s inet from %s to ! %s -> (%s)", spec.Uplink, p.Masked(), p.Masked(), spec.Uplink)
	}
	b.line("# the gateway pair of the network: judged by the anchors of the jails, not here")
	b.line("pass quick on %s all no state", member)
	return b.result()
}

// filterRules renders the typed filter rules of one policy.
func filterRules(b *builder, rules []api.FirewallRule, fams []family, ifs string, onlyIn bool) {
	for i, r := range rules {
		if onlyIn && r.Direction == "out" {
			continue
		}
		b.line("# %s", ruleLabel(i, r))
		act := "pass"
		if r.Action == "deny" {
			act = "block drop"
		}
		dir, own := "out", "to" // traffic into the jail leaves the host-side end
		if r.Direction == "out" {
			dir, own = "in", "from"
		}
		for _, f := range fams {
			if r.Proto == "icmp" && f.kw != "inet" || r.Proto == "icmp6" && f.kw != "inet6" {
				continue
			}
			var from, to string
			var ok1, ok2 bool
			if own == "to" {
				from, ok1 = endpoints(r.From, f, nil)
				to, ok2 = endpoints(r.To, f, f.addrs)
			} else {
				from, ok1 = endpoints(r.From, f, f.addrs)
				to, ok2 = endpoints(r.To, f, nil)
			}
			if !ok1 || !ok2 {
				continue
			}
			var sb strings.Builder
			sb.WriteString(act + " " + dir)
			if r.Log {
				sb.WriteString(" log")
			}
			fmt.Fprintf(&sb, " quick on %s %s", ifs, f.kw)
			if r.Proto != "" {
				sb.WriteString(" proto " + r.Proto)
			}
			fmt.Fprintf(&sb, " from %s to %s", from, to)
			if r.Redirect != nil {
				// the filter sees the translated destination
				to2 := f.addrs[0].String()
				sb.Reset()
				fmt.Fprintf(&sb, "%s %s", act, dir)
				if r.Log {
					sb.WriteString(" log")
				}
				fmt.Fprintf(&sb, " quick on %s %s proto %s from %s to %s port %d", ifs, f.kw, r.Proto, from, to2, r.Redirect.JailPort)
			} else if len(r.Ports) > 0 {
				sb.WriteString(" port " + portList(r.Ports))
			}
			b.line("%s", sb.String())
		}
	}
}

func llStrings(in []netip.Addr) []string { return addrStrings(in) }

// linkLocals keeps the link-local addresses of a list.
func linkLocals(in []netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range uniqAddrs(in) {
		if a.Is6() && a.IsLinkLocalUnicast() {
			out = append(out, a)
		}
	}
	return out
}

// solicitedNode returns the solicited-node multicast address of a unicast address (RFC 4291, 2.7.1).
func solicitedNode(a netip.Addr) netip.Addr {
	b := a.As16()
	return netip.AddrFrom16([16]byte{0xff, 0x02, 11: 0x01, 12: 0xff, 13: b[13], 14: b[14], 15: b[15]})
}

// ndDests are the destinations neighbour discovery messages may have when they reach the jail: its own addresses and
// link-local addresses and their solicited-node groups (the whole solicited-node range and fe80::/10 when the
// link-local addresses are unknown).
func ndDests(own, ll []netip.Addr) []string {
	var out []string
	all := append(append([]netip.Addr(nil), own...), ll...)
	out = append(out, addrStrings(all)...)
	if len(ll) == 0 {
		out = append(out, "fe80::/10", "ff02::1:ff00:0/104")
		return out
	}
	seen := map[netip.Addr]bool{}
	for _, a := range all {
		if g := solicitedNode(a); !seen[g] {
			seen[g] = true
			out = append(out, g.String())
		}
	}
	return out
}

func addrStrings(in []netip.Addr) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = a.String()
	}
	return out
}

func famAddrs(fams []family, kw string) []netip.Addr {
	for _, f := range fams {
		if f.kw == kw {
			return f.addrs
		}
	}
	return nil
}

// tagName turns a jail key into a PF tag.
func tagName(key string) string {
	var sb strings.Builder
	sb.WriteString("jo_")
	for _, c := range key {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			sb.WriteRune(c)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}
