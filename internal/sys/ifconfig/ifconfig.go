// Package ifconfig manages bridges, epair interfaces, addresses and routes
// through ifconfig(8) and route(8), and reads interface state. By default it
// only changes interfaces whose names carry a JailOps prefix.
package ifconfig

import (
	"context"
	"net/netip"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// DefaultPrefixes are the name prefixes JailOps owns: bridges are jo<n> and
// epairs are named after the jail with the same prefix.
var DefaultPrefixes = []string{"jo"}

// Options configure a Client.
type Options struct {
	// Prefixes of interface names that may be changed. Nil means
	// DefaultPrefixes.
	Prefixes []string
	// AllowForeign lifts the prefix restriction. It must be an explicit
	// decision of the caller.
	AllowForeign bool
}

func (o Options) prefixes() []string {
	if len(o.Prefixes) == 0 {
		return DefaultPrefixes
	}
	return o.Prefixes
}

// owned checks the name format and, unless AllowForeign, the prefix.
func (o Options) owned(name string) error {
	if err := ValidateIfName(name); err != nil {
		return err
	}
	if o.AllowForeign {
		return nil
	}
	for _, p := range o.prefixes() {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return nil
		}
	}
	return api.Errorf(api.CodeForbidden, "interface %q is not managed by JailOps (prefix %s)", name, strings.Join(o.prefixes(), ","))
}

// ValidateIfName accepts FreeBSD interface names: a letter, then letters,
// digits, '_' and '.', at most 15 bytes (IFNAMSIZ is 16 with the NUL).
func ValidateIfName(n string) error {
	if n == "" || len(n) > 15 {
		return api.Errorf(api.CodeInvalid, "invalid interface name %q", n)
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !(letter || i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '.')) {
			return api.Errorf(api.CodeInvalid, "invalid interface name %q", n)
		}
	}
	return nil
}

// ValidateJailRef accepts a jail name or numeric JID for `ifconfig vnet`.
func ValidateJailRef(j string) error {
	if j == "" || len(j) > 64 || j[0] == '-' {
		return api.Errorf(api.CodeInvalid, "invalid jail %q", j)
	}
	for i := 0; i < len(j); i++ {
		c := j[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return api.Errorf(api.CodeInvalid, "invalid jail %q", j)
		}
	}
	return nil
}

// ValidateDescription rejects control characters in an interface description.
func ValidateDescription(d string) error {
	if len(d) > 255 {
		return api.Errorf(api.CodeInvalid, "description too long")
	}
	for _, r := range d {
		if r < 0x20 || r == 0x7f {
			return api.Errorf(api.CodeInvalid, "invalid description")
		}
	}
	return nil
}

// Addr is an interface address.
type Addr struct {
	Prefix netip.Prefix // address with prefix length
}

// Member is a bridge member port.
type Member struct {
	Name    string
	Flags   []string
	Private bool
}

// Iface is one entry of the interface list.
type Iface struct {
	Name        string
	Flags       []string // symbolic, e.g. UP, RUNNING
	MTU         int
	Description string
	MAC         string
	Inet        []netip.Prefix
	Inet6       []netip.Prefix
	Groups      []string
	Status      string
	Members     []Member
	// VlanParent and VlanTag are set on a vlan(4) interface: "vlan: 77 vlanproto: 802.1q vlanpcp: 0 parent interface: em0".
	VlanParent string
	VlanTag    int
}

// Up reports whether the UP flag is set.
func (i Iface) Up() bool {
	for _, f := range i.Flags {
		if f == "UP" {
			return true
		}
	}
	return false
}

// Neighbor is a permanent IPv6 neighbor entry (ndp -an).
type Neighbor struct {
	Addr  netip.Addr
	Iface string
	MAC   string
}

// Route describes a route for RouteAdd and RouteDelete.
type Route struct {
	// Dest is "default" or an address or prefix; its family selects -inet
	// or -inet6.
	Dest string
	// Gateway is a next hop. Empty with Interface set means a link route
	// (-interface).
	Gateway   string
	Interface string
}

// Client is the interface layer. Names given to mutating calls must carry a
// managed prefix (see Options).
type Client interface {
	List(ctx context.Context) ([]Iface, error)

	BridgeCreate(ctx context.Context, name string) error
	BridgeDestroy(ctx context.Context, name string) error
	BridgeAddMember(ctx context.Context, bridge, member string, private bool) error
	BridgeDeleteMember(ctx context.Context, bridge, member string) error
	SetDescription(ctx context.Context, iface, desc string) error

	// EpairCreate creates a pair named <name>a and <name>b and returns both.
	EpairCreate(ctx context.Context, name string) (a, b string, err error)
	// EpairDestroy destroys the pair that iface (either end) belongs to.
	EpairDestroy(ctx context.Context, iface string) error
	// MoveToJail moves iface into the vnet of jail (name or JID).
	MoveToJail(ctx context.Context, iface, jail string) error
	// MoveFromJail takes iface back from the jail to the host.
	MoveFromJail(ctx context.Context, iface, jail string) error

	SetLink(ctx context.Context, iface string, up bool) error
	AddrAdd(ctx context.Context, iface string, prefix netip.Prefix) error
	AddrRemove(ctx context.Context, iface string, prefix netip.Prefix) error

	RouteAdd(ctx context.Context, r Route) error
	RouteDelete(ctx context.Context, r Route) error

	// NeighborSet installs a permanent IPv6 neighbor entry on iface: addr is at mac. An entry of the same address is
	// replaced (a static entry that outlived its jail would send the next owner's traffic to a dead address).
	NeighborSet(ctx context.Context, iface string, addr netip.Addr, mac string) error
	// NeighborDelete removes the entry of addr on iface.
	NeighborDelete(ctx context.Context, iface string, addr netip.Addr) error

	// IPv6Forwarding reads net.inet6.ip6.forwarding.
	IPv6Forwarding(ctx context.Context) (bool, error)
}

// routeArgs validates r and renders `-inet|-inet6 <dest> <gw>|-interface <if>`.
func routeArgs(o Options, r Route) ([]string, error) {
	var fam string
	dest := r.Dest
	switch {
	case dest == "default":
		// the family follows the gateway
		if g, err := netip.ParseAddr(r.Gateway); err == nil {
			fam = famOf(g)
		} else if r.Interface != "" && r.Gateway == "" {
			return nil, api.Errorf(api.CodeInvalid, "default route over an interface needs an explicit family; give a gateway")
		}
	default:
		if p, err := netip.ParsePrefix(dest); err == nil {
			fam, dest = famOf(p.Addr()), p.Masked().String()
		} else if a, err := netip.ParseAddr(dest); err == nil && a.Zone() == "" {
			fam, dest = famOf(a), a.String()
		}
	}
	if fam == "" {
		return nil, api.Errorf(api.CodeInvalid, "invalid route %q via %q", r.Dest, r.Gateway)
	}
	args := []string{fam, dest}
	switch {
	case r.Gateway != "":
		g, err := netip.ParseAddr(r.Gateway)
		if err != nil || famOf(g) != fam {
			return nil, api.Errorf(api.CodeInvalid, "invalid gateway %q", r.Gateway)
		}
		args = append(args, g.String())
	case r.Interface != "":
		if err := o.owned(r.Interface); err != nil {
			return nil, err
		}
		args = append(args, "-interface", r.Interface)
	default:
		return nil, api.Errorf(api.CodeInvalid, "route needs a gateway or an interface")
	}
	return args, nil
}

func famOf(a netip.Addr) string {
	if a.Is4() || a.Is4In6() {
		return "-inet"
	}
	return "-inet6"
}
