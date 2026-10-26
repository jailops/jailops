package render

import (
	"fmt"
	"net/netip"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Op is the kind of a plan step.
type Op string

// Plan operations. Names follow the ifconfig, route and pfctl concepts they
// map to; the executor lives in internal/sys.
const (
	OpRequireSysctl  Op = "sysctl.require"
	OpBridgeCreate   Op = "bridge.create"
	OpBridgeDestroy  Op = "bridge.destroy"
	OpSetDescription Op = "iface.description"
	OpSetMTU         Op = "iface.mtu"
	OpSetMAC         Op = "iface.mac"
	OpAddAddr        Op = "iface.addr.add"
	OpRemoveAddr     Op = "iface.addr.remove"
	OpIfaceUp        Op = "iface.up"
	OpIfaceDown      Op = "iface.down"
	OpEpairCreate    Op = "epair.create"
	OpEpairDestroy   Op = "epair.destroy"
	OpAddMember      Op = "bridge.member.add"
	OpRemoveMember   Op = "bridge.member.remove"
	OpSetPrivate     Op = "bridge.member.private"
	OpClearPrivate   Op = "bridge.member.unprivate"
	OpAddHostRoute   Op = "route.host.add"
	OpDelHostRoute   Op = "route.host.delete"
	OpSetStaticARP   Op = "iface.staticarp"
	OpAddBridgeStat  Op = "bridge.static.add"
	OpDelBridgeStat  Op = "bridge.static.delete"
	OpSetNeighbor    Op = "neighbor.set"
	OpDelNeighbor    Op = "neighbor.delete"
	OpNDPProxyAdd    Op = "ndproxy.add"
	OpNDPProxyDel    Op = "ndproxy.delete"
	OpLoadAnchor     Op = "pf.anchor.load"
	OpFlushAnchor    Op = "pf.anchor.flush"
	OpAddGatewayLink Op = "route.gateway.add"
	OpDelGatewayLink Op = "route.gateway.delete"
	OpAddDefaultRte  Op = "route.default.add"
	OpDelDefaultRte  Op = "route.default.delete"
)

// Phase says where a step runs.
type Phase string

const (
	// PhaseHost steps run in the host network stack.
	PhaseHost Phase = "host"
	// PhaseJail steps run inside the jail's vnet after the jail started.
	PhaseJail Phase = "jail"
)

// Step is one declarative action. It carries no state about the system.
type Step struct {
	Op    Op
	Phase Phase
	// Iface is the interface the step acts on (bridge, epair end, uplink).
	Iface string
	// Arg is the main argument: an address with prefix, a description, a
	// MAC, a member name, an anchor name or a "sysctl=value" pair.
	Arg string
	// Arg2 is a second argument: the jail-side name of an epair, or the
	// bridge of a host route.
	Arg2 string
	// Inverse undoes the step. It is nil when the step is undone implicitly
	// by the inverse of an earlier step (for example, destroying an epair
	// removes its MAC) or cannot be undone (sysctl requirements).
	Inverse *Step
	Note    string
}

func step(op Op, iface, arg string) Step {
	return Step{Op: op, Phase: PhaseHost, Iface: iface, Arg: arg}
}

func with(s Step, inv Step) Step { s.Inverse = &inv; return s }

// Rollback returns the inverses of steps, in reverse order, skipping steps
// without one.
func Rollback(steps []Step) []Step {
	var out []Step
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Inverse != nil {
			out = append(out, *steps[i].Inverse)
		}
	}
	return out
}

// Describe returns a human readable approximation of the command a step
// maps to. It is documentation and golden-test output, not an argv.
// UNVERIFIED: syntax on FreeBSD 15.
func (s Step) Describe() string {
	pre := ""
	if s.Phase == PhaseJail {
		return s.describeJail()
	}
	switch s.Op {
	case OpRequireSysctl:
		return pre + "require sysctl " + s.Arg
	case OpBridgeCreate:
		return pre + "ifconfig bridge create name " + s.Iface
	case OpBridgeDestroy:
		return pre + "ifconfig " + s.Iface + " destroy"
	case OpSetDescription:
		if s.Arg == "" {
			return pre + "ifconfig " + s.Iface + " -description"
		}
		return pre + "ifconfig " + s.Iface + " description " + s.Arg
	case OpSetMTU:
		return pre + "ifconfig " + s.Iface + " mtu " + s.Arg
	case OpSetMAC:
		return pre + "ifconfig " + s.Iface + " ether " + s.Arg
	case OpAddAddr:
		return pre + "ifconfig " + s.Iface + " " + ifFamily(s.Arg) + " " + s.Arg + " alias"
	case OpRemoveAddr:
		return pre + "ifconfig " + s.Iface + " " + ifFamily(s.Arg) + " " + s.Arg + " -alias"
	case OpIfaceUp:
		return pre + "ifconfig " + s.Iface + " up"
	case OpIfaceDown:
		return pre + "ifconfig " + s.Iface + " down"
	case OpEpairCreate:
		return pre + "ifconfig epair create; rename ends to " + s.Iface + " and " + s.Arg
	case OpEpairDestroy:
		return pre + "ifconfig " + s.Iface + " destroy"
	case OpAddMember:
		return pre + "ifconfig " + s.Iface + " addm " + s.Arg
	case OpRemoveMember:
		return pre + "ifconfig " + s.Iface + " deletem " + s.Arg
	case OpSetPrivate:
		return pre + "ifconfig " + s.Iface + " private " + s.Arg
	case OpClearPrivate:
		return pre + "ifconfig " + s.Iface + " -private " + s.Arg
	case OpAddHostRoute:
		return pre + "route add " + afFlag(s.Arg) + " " + s.Arg + " -interface " + s.Iface
	case OpDelHostRoute:
		return pre + "route delete " + afFlag(s.Arg) + " " + s.Arg
	case OpSetStaticARP:
		return pre + "ifconfig " + s.Iface + " staticarp"
	case OpAddBridgeStat:
		return pre + "ifconfig " + s.Iface + " static " + s.Arg2 + " " + s.Arg
	case OpDelBridgeStat:
		return pre + "ifconfig " + s.Iface + " deladdr " + s.Arg
	case OpSetNeighbor:
		return pre + "ndp -s " + s.Arg + " " + s.Arg2 + "   (on " + s.Iface + ")"
	case OpDelNeighbor:
		return pre + "ndp -d " + s.Arg + "   (on " + s.Iface + ")"
	case OpNDPProxyAdd:
		return pre + "ndproxy answer " + s.Arg + " on " + s.Iface
	case OpNDPProxyDel:
		return pre + "ndproxy withdraw " + s.Arg + " on " + s.Iface
	case OpLoadAnchor:
		return pre + "pfctl -a " + s.Arg + " -f <rendered>"
	case OpFlushAnchor:
		return pre + "pfctl -a " + s.Arg + " -F all"
	case OpAddGatewayLink:
		return pre + "route add -host " + s.Arg + " -interface " + s.Iface
	case OpDelGatewayLink:
		return pre + "route delete -host " + s.Arg
	case OpAddDefaultRte:
		return pre + "route add " + afFlag(s.Arg) + " default " + s.Arg
	case OpDelDefaultRte:
		return pre + "route delete " + afFlag(s.Arg) + " default"
	}
	return string(s.Op)
}

func ifFamily(arg string) string {
	if strings.Contains(arg, ":") {
		return "inet6"
	}
	return "inet"
}

func afFlag(arg string) string {
	if strings.Contains(arg, ":") {
		return "-inet6"
	}
	return "-inet"
}

// DescribePlan renders a plan, one line per step, for golden tests and logs.
func DescribePlan(steps []Step) string {
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "%02d %s\n", i+1, s.Describe())
	}
	return b.String()
}

// NetworkInput is the input of NetworkPlan.
type NetworkInput struct {
	// ID is the network's UUID, written into the bridge description.
	ID   string
	Name string
	// Bridge is the bridge name; spec.Bridge is used when empty.
	Bridge string
	Spec   api.NetworkSpec
}

// BridgeDescription is the ownership marker written on bridges and epairs.
func BridgeDescription(id string) string { return "jailops:" + id }

// NetworkPlan returns the ordered steps that create a network's bridge and
// host-side configuration. It does not look at the system; reconcile diffs
// against what it observes and must never touch a bridge whose description
// is not BridgeDescription(ID).
func NetworkPlan(in NetworkInput) ([]Step, error) {
	if err := validate.Name("network", in.Name); err != nil {
		return nil, err
	}
	if err := validate.NetworkSpec(in.Spec); err != nil {
		return nil, err
	}
	if err := validateID(in.ID); err != nil {
		return nil, err
	}
	br := in.Bridge
	if br == "" {
		br = in.Spec.Bridge
	}
	if err := validate.IfName("bridge", br); err != nil {
		return nil, err
	}
	s := in.Spec
	var out []Step
	req := func(kv, note string) {
		st := step(OpRequireSysctl, "", kv)
		st.Note = note
		out = append(out, st)
	}
	// The bridge pfil sysctls are asserted by the host pass of the reconcile
	// loop (reconcile.HostDesired), which also loads if_bridge first.
	v6 := s.IPv6 != nil && s.IPv6.Mode != api.IPv6None
	if (s.IPv4 != nil && s.IPv4.NAT) || v6 || s.Uplink != "" {
		if s.IPv4 != nil {
			req("net.inet.ip.forwarding=1", "jails reach the uplink through the host")
		}
		if v6 {
			req("net.inet6.ip6.forwarding=1", "jails reach the uplink through the host")
		}
	}
	// The bridge carries only layer 2. The host's gateway addresses live on the l3 end of a gateway epair whose
	// member end is a port of the bridge (see ipam.GatewayNames for why).
	gm, gl3, err := ipam.GatewayNames(br)
	if err != nil {
		return nil, err
	}
	gwep := step(OpEpairCreate, gm, gl3)
	gwep.Arg2 = gl3
	out = append(out,
		with(step(OpBridgeCreate, br, ""), step(OpBridgeDestroy, br, "")),
		with(step(OpSetDescription, br, BridgeDescription(in.ID)), step(OpSetDescription, br, "")),
		with(gwep, step(OpEpairDestroy, gm, "")),
		with(step(OpSetDescription, gm, BridgeDescription(in.ID)), step(OpSetDescription, gm, "")),
		with(step(OpSetDescription, gl3, BridgeDescription(in.ID)), step(OpSetDescription, gl3, "")),
	)
	if s.MTU != 0 {
		for _, i := range []string{br, gm, gl3} {
			out = append(out, with(step(OpSetMTU, i, fmt.Sprint(s.MTU)), step(OpSetMTU, i, "1500")))
		}
	}
	// the member end is never private: the jails must be able to reach the gateway through it
	out = append(out, with(step(OpAddMember, br, gm), step(OpRemoveMember, br, gm)))
	addr := func(a string) {
		out = append(out, with(step(OpAddAddr, gl3, a), step(OpRemoveAddr, gl3, a)))
	}
	if s.IPv4 != nil {
		p, _ := netip.ParsePrefix(s.IPv4.Subnet)
		gw, _ := netip.ParseAddr(s.IPv4.Gateway)
		addr(netip.PrefixFrom(gw, p.Bits()).String())
	}
	if v6 {
		p, _ := netip.ParsePrefix(s.IPv6.Subnet)
		gw, _ := netip.ParseAddr(s.IPv6.Gateway)
		bits := p.Bits()
		if gw.IsLinkLocalUnicast() {
			bits = 64
		}
		addr(netip.PrefixFrom(gw, bits).String())
		if !gw.IsLinkLocalUnicast() {
			// the jails' default gateway (they hold /128 addresses and have no on-link prefix)
			addr(netip.PrefixFrom(ipam.GatewayLinkLocal6, 64).String())
		}
	}
	out = append(out,
		with(step(OpIfaceUp, br, ""), step(OpIfaceDown, br, "")),
		with(step(OpIfaceUp, gm, ""), step(OpIfaceDown, gm, "")),
		with(step(OpIfaceUp, gl3, ""), step(OpIfaceDown, gl3, "")),
	)
	a := NetAnchor(in.Name)
	out = append(out, with(step(OpLoadAnchor, "", a), step(OpFlushAnchor, "", a)))
	return out, nil
}

func validateID(id string) error {
	if id == "" || len(id) > 64 {
		return validate.Invalid("id", "id must be 1-64 characters")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return validate.Invalid("id", "id %q has unsupported characters", id)
		}
	}
	return nil
}

// AttachInput is the input of AttachPlan: one attachment of one jail.
type AttachInput struct {
	JailID   string // used for epair names and the ownership marker
	JailUUID string // used to derive MAC addresses
	Index    int    // attachment index within the jail
	Bridge   string
	// MAC of the jail end; derived from JailUUID when empty.
	MAC     string
	MTU     int
	Private bool
	// IPv4 and IPv6 are the jail's address with the prefix length it
	// configures (ipam.GuestPrefixLen6 for IPv6); zero means none.
	IPv4, IPv6 netip.Prefix
	Gateway4   netip.Addr
	Gateway6   netip.Addr
	IPv6Mode   api.IPv6Mode
	Uplink     string
	// HostConfigured asks the plan to configure addresses and default
	// routes from the host after the jail started (Linux jails; FreeBSD
	// jails configure themselves through rc.conf). UNVERIFIED: whether
	// addresses set on the epair survive its move into the vnet.
	HostConfigured bool
}

// AttachPlan returns the steps that connect one jail interface to a bridge.
// Host-phase steps run before the jail starts (the jail-side end is moved
// into the vnet by jail.conf's vnet.interface); jail-phase steps run after.
func AttachPlan(in AttachInput) ([]Step, error) {
	if err := validateID(in.JailID); err != nil {
		return nil, err
	}
	if err := validate.IfName("bridge", in.Bridge); err != nil {
		return nil, err
	}
	host, jail, err := ipam.EpairNames(in.JailID, in.Index)
	if err != nil {
		return nil, err
	}
	hmac, err := ipam.DeriveMACN(in.JailUUID, in.Index, true)
	if err != nil {
		return nil, err
	}
	jmac := in.MAC
	if jmac == "" {
		if jmac, err = ipam.DeriveMACN(in.JailUUID, in.Index, false); err != nil {
			return nil, err
		}
	} else if err := validate.MAC("mac", jmac); err != nil {
		return nil, err
	}
	if in.MTU != 0 && (in.MTU < 576 || in.MTU > 9216) {
		return nil, validate.Invalid("mtu", "mtu out of range")
	}
	if in.IPv4.IsValid() && !in.IPv4.Addr().Is4() || in.IPv6.IsValid() && !in.IPv6.Addr().Is6() {
		return nil, validate.Invalid("address", "address family mismatch")
	}
	if in.Gateway4.IsValid() && !in.Gateway4.Is4() || in.Gateway6.IsValid() && !in.Gateway6.Is6() {
		return nil, validate.Invalid("gateway", "gateway family mismatch")
	}
	proxy := in.IPv6Mode == api.IPv6Proxy && in.IPv6.IsValid()
	if proxy {
		if err := validate.IfName("uplink", in.Uplink); err != nil {
			return nil, err
		}
		if in.Uplink == in.Bridge {
			return nil, validate.Invalid("uplink", "uplink must not be the bridge")
		}
	}

	var out []Step
	ep := step(OpEpairCreate, host, jail)
	ep.Arg2 = jail
	out = append(out, with(ep, step(OpEpairDestroy, host, "")))
	out = append(out, step(OpSetMAC, host, hmac), step(OpSetMAC, jail, jmac))
	out = append(out, with(step(OpSetDescription, host, BridgeDescription(in.JailID)), step(OpSetDescription, host, "")))
	if in.MTU != 0 {
		out = append(out, step(OpSetMTU, host, fmt.Sprint(in.MTU)), step(OpSetMTU, jail, fmt.Sprint(in.MTU)))
	}
	out = append(out, with(step(OpAddMember, in.Bridge, host), step(OpRemoveMember, in.Bridge, host)))
	// The jail's MAC address is fixed on its port: a jail that sets another jail's MAC address on its own interface
	// cannot take that jail's frames (a learned entry would move, a static one does not).
	stat := step(OpAddBridgeStat, in.Bridge, jmac)
	stat.Arg2 = host
	out = append(out, with(stat, step(OpDelBridgeStat, in.Bridge, jmac)))
	// Every jail port is private: frames never pass from a jail to another jail. A jail holds /32 and /128 addresses
	// and reaches everything, the other jails included, through the host (in.Private is no longer consulted).
	out = append(out, with(step(OpSetPrivate, in.Bridge, host), step(OpClearPrivate, in.Bridge, host)))
	out = append(out, with(step(OpIfaceUp, host, ""), step(OpIfaceDown, host, "")))
	if proxy {
		a := netip.PrefixFrom(in.IPv6.Addr(), 128).String()
		gw := ipam.GatewayIface(in.Bridge)
		r := step(OpAddHostRoute, gw, a)
		out = append(out,
			with(r, step(OpDelHostRoute, gw, a)),
			with(step(OpNDPProxyAdd, in.Uplink, in.IPv6.Addr().String()), step(OpNDPProxyDel, in.Uplink, in.IPv6.Addr().String())))
	}
	if in.IPv4.IsValid() || in.IPv6.IsValid() {
		// (After the host route of a proxied address, which the entry needs to find the interface.) The host never
		// asks a jail for its address: a permanent entry on the gateway interface says where the jail's addresses
		// are (ARP for IPv4; neighbor discovery for the global and link-local IPv6 addresses), so that nothing a jail
		// sends can change where the host sends the traffic of another jail.
		gw := ipam.GatewayIface(in.Bridge)
		// The host asks nobody on this interface who has an address: only the entries below count, so an address
		// a jail gives itself is never resolved (the entry is shared by the jails of the network and stays).
		out = append(out, step(OpSetStaticARP, gw, ""))
		var addrs []netip.Addr
		if in.IPv4.IsValid() {
			addrs = append(addrs, in.IPv4.Addr())
		}
		if in.IPv6.IsValid() {
			ll, err := ipam.LinkLocalFromMAC(jmac)
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, in.IPv6.Addr(), ll)
		}
		for _, a := range addrs {
			set, del := step(OpSetNeighbor, gw, a.String()), step(OpDelNeighbor, gw, a.String())
			set.Arg2 = jmac
			out = append(out, with(set, del))
		}
	}
	if in.HostConfigured {
		j := func(op Op, iface, arg string) Step { s := step(op, iface, arg); s.Phase = PhaseJail; return s }
		for _, p := range []netip.Prefix{in.IPv4, in.IPv6} {
			if p.IsValid() {
				out = append(out, with(j(OpAddAddr, jail, p.String()), j(OpRemoveAddr, jail, p.String())))
			}
		}
		out = append(out, with(j(OpIfaceUp, jail, ""), j(OpIfaceDown, jail, "")))
		for _, g := range []netip.Addr{in.Gateway4, in.Gateway6} {
			if !g.IsValid() {
				continue
			}
			if g.Is4() && in.IPv4.Bits() == 32 {
				// a /32 has no on-link gateway: the route to the gateway itself comes first
				out = append(out, with(j(OpAddGatewayLink, jail, g.String()), j(OpDelGatewayLink, jail, g.String())))
			}
			arg := g.String()
			if g.IsLinkLocalUnicast() {
				arg += "%" + jail
			}
			out = append(out, with(j(OpAddDefaultRte, jail, arg), j(OpDelDefaultRte, jail, arg)))
		}
	}
	return out, nil
}

// describeJail renders a jail-phase step as the host command that configures
// the jail's vnet from outside (the Linux userland has no usable tools).
func (s Step) describeJail() string {
	pre := "[host, jail] "
	switch s.Op {
	case OpAddAddr, OpRemoveAddr:
		p, err := netip.ParsePrefix(s.Arg)
		if err != nil {
			return pre + string(s.Op)
		}
		verb := "alias"
		if s.Op == OpRemoveAddr {
			verb = "-alias"
		}
		switch {
		case p.Addr().Is6():
			return fmt.Sprintf("%sifconfig -j <jail> %s inet6 %s prefixlen %d %s", pre, s.Iface, p.Addr(), p.Bits(), verb)
		case s.Op == OpAddAddr:
			return fmt.Sprintf("%sifconfig -j <jail> %s inet %s up", pre, s.Iface, p)
		}
		return fmt.Sprintf("%sifconfig -j <jail> %s inet %s %s", pre, s.Iface, p, verb)
	case OpIfaceUp:
		return pre + "ifconfig -j <jail> " + s.Iface + " up"
	case OpIfaceDown:
		return pre + "ifconfig -j <jail> " + s.Iface + " down"
	case OpAddGatewayLink:
		return pre + "route -j <jail> add -host " + s.Arg + " -interface " + s.Iface
	case OpDelGatewayLink:
		return pre + "route -j <jail> delete -host " + s.Arg
	case OpAddDefaultRte:
		if strings.Contains(s.Arg, ":") {
			return pre + "route -j <jail> add -inet6 default " + s.Arg
		}
		return pre + "route -j <jail> add default " + s.Arg
	case OpDelDefaultRte:
		if strings.Contains(s.Arg, ":") {
			return pre + "route -j <jail> delete -inet6 default"
		}
		return pre + "route -j <jail> delete default"
	}
	return pre + string(s.Op)
}
