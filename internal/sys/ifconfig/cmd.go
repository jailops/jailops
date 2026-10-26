package ifconfig

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Command implements Client over run.Runner.
type Command struct {
	R run.Runner
	O Options
}

var _ Client = (*Command)(nil)

// NewCommand returns a Client with the given options.
func NewCommand(r run.Runner, o Options) *Command { return &Command{R: r, O: o} }

func (c *Command) run(ctx context.Context, name string, args ...string) (string, error) {
	res, err := c.R.Run(ctx, run.Cmd{Name: name, Args: args, Timeout: 20 * time.Second})
	if err != nil {
		return "", mapErr(name, err)
	}
	return string(res.Stdout), nil
}

func mapErr(tool string, err error) error {
	ee, ok := run.IsExit(err)
	if !ok {
		return api.Errorf(api.CodeInternal, "%s: %v", tool, err)
	}
	msg := strings.TrimSpace(ee.Stderr)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "does not exist"), strings.Contains(low, "interface not found"), strings.Contains(low, "no such"), strings.Contains(low, "not in table"):
		return api.Errorf(api.CodeNotFound, "%s: %s", tool, msg)
	case strings.Contains(low, "file exists"), strings.Contains(low, "already exists"):
		return api.Errorf(api.CodeAlreadyExists, "%s: %s", tool, msg)
	case strings.Contains(low, "busy"):
		return api.Errorf(api.CodeBusy, "%s: %s", tool, msg)
	}
	return api.Errorf(api.CodeInternal, "%s: %s", tool, msg)
}

// List implements Client with `ifconfig -a`.
func (c *Command) List(ctx context.Context) ([]Iface, error) {
	out, err := c.run(ctx, "ifconfig", "-a")
	if err != nil {
		return nil, err
	}
	return ParseIfconfig(out), nil
}

// BridgeCreate creates a bridge: `ifconfig bridge create` returns a
// kernel-chosen name, which is then renamed to name. If the rename fails the
// new bridge is destroyed again. UNVERIFIED on FreeBSD.
func (c *Command) BridgeCreate(ctx context.Context, name string) error {
	if err := c.O.owned(name); err != nil {
		return err
	}
	out, err := c.run(ctx, "ifconfig", "bridge", "create")
	if err != nil {
		return err
	}
	tmp := strings.TrimSpace(out)
	if ValidateIfName(tmp) != nil {
		return api.Errorf(api.CodeInternal, "ifconfig bridge create returned %q", tmp)
	}
	if _, err := c.run(ctx, "ifconfig", tmp, "name", name); err != nil {
		c.run(ctx, "ifconfig", tmp, "destroy") // best effort cleanup
		return err
	}
	return nil
}

// BridgeDestroy implements Client.
func (c *Command) BridgeDestroy(ctx context.Context, name string) error {
	if err := c.O.owned(name); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", name, "destroy")
	return err
}

// BridgeAddMember implements Client: `addm member [private member]`.
func (c *Command) BridgeAddMember(ctx context.Context, bridge, member string, private bool) error {
	if err := c.O.owned(bridge); err != nil {
		return err
	}
	if err := ValidateIfName(member); err != nil {
		return err
	}
	args := []string{bridge, "addm", member}
	if private {
		args = append(args, "private", member)
	}
	_, err := c.run(ctx, "ifconfig", args...)
	return err
}

// BridgeDeleteMember implements Client.
func (c *Command) BridgeDeleteMember(ctx context.Context, bridge, member string) error {
	if err := c.O.owned(bridge); err != nil {
		return err
	}
	if err := ValidateIfName(member); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", bridge, "deletem", member)
	return err
}

// SetDescription implements Client.
func (c *Command) SetDescription(ctx context.Context, iface, desc string) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	if err := ValidateDescription(desc); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", iface, "description", desc)
	return err
}

// EpairCreate implements Client: `ifconfig epair create` prints "epairNa";
// both ends are renamed to <name>a and <name>b. On failure the pair is
// destroyed. UNVERIFIED on FreeBSD (renaming an epair end).
func (c *Command) EpairCreate(ctx context.Context, name string) (string, string, error) {
	a, b := name+"a", name+"b"
	if err := c.O.owned(a); err != nil {
		return "", "", err
	}
	out, err := c.run(ctx, "ifconfig", "epair", "create")
	if err != nil {
		return "", "", err
	}
	ta := strings.TrimSpace(out)
	if !strings.HasPrefix(ta, "epair") || !strings.HasSuffix(ta, "a") || ValidateIfName(ta) != nil {
		return "", "", api.Errorf(api.CodeInternal, "ifconfig epair create returned %q", ta)
	}
	tb := strings.TrimSuffix(ta, "a") + "b"
	cleanup := func(n string) { c.run(ctx, "ifconfig", n, "destroy") }
	if _, err := c.run(ctx, "ifconfig", tb, "name", b); err != nil {
		cleanup(ta)
		return "", "", err
	}
	if _, err := c.run(ctx, "ifconfig", ta, "name", a); err != nil {
		cleanup(b)
		return "", "", err
	}
	return a, b, nil
}

// EpairDestroy implements Client. Destroying one end removes both.
func (c *Command) EpairDestroy(ctx context.Context, iface string) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", iface, "destroy")
	return err
}

// MoveToJail implements Client. UNVERIFIED on FreeBSD: whether addresses
// survive the move (design section 6).
func (c *Command) MoveToJail(ctx context.Context, iface, jail string) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	if err := ValidateJailRef(jail); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", iface, "vnet", jail)
	return err
}

// MoveFromJail implements Client. UNVERIFIED: `ifconfig <if> -vnet <jail>`
// run from the host.
func (c *Command) MoveFromJail(ctx context.Context, iface, jail string) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	if err := ValidateJailRef(jail); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", iface, "-vnet", jail)
	return err
}

// SetLink implements Client.
func (c *Command) SetLink(ctx context.Context, iface string, up bool) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	state := "down"
	if up {
		state = "up"
	}
	_, err := c.run(ctx, "ifconfig", iface, state)
	return err
}

func addrArgs(iface string, p netip.Prefix, verb string) []string {
	return append([]string{iface}, AddrWords(p, verb)...)
}

// AddrWords returns the ifconfig words that add (verb "alias") or remove
// ("-alias") an address. IPv6 takes "<addr> prefixlen <n>": the /n form
// was rejected on FreeBSD 15.1.
func AddrWords(p netip.Prefix, verb string) []string {
	if p.Addr().Is6() {
		return []string{"inet6", p.Addr().String(), "prefixlen", strconv.Itoa(p.Bits()), verb}
	}
	return []string{"inet", p.String(), verb}
}

func validPrefix(p netip.Prefix) error {
	if !p.IsValid() || p.Addr().Zone() != "" {
		return api.Errorf(api.CodeInvalid, "invalid address %q", p)
	}
	return nil
}

// AddrAdd implements Client.
func (c *Command) AddrAdd(ctx context.Context, iface string, p netip.Prefix) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	if err := validPrefix(p); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", addrArgs(iface, p, "alias")...)
	return err
}

// AddrRemove implements Client.
func (c *Command) AddrRemove(ctx context.Context, iface string, p netip.Prefix) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	if err := validPrefix(p); err != nil {
		return err
	}
	_, err := c.run(ctx, "ifconfig", addrArgs(iface, p, "-alias")...)
	return err
}

// RouteAdd implements Client.
func (c *Command) RouteAdd(ctx context.Context, r Route) error {
	a, err := routeArgs(c.O, r)
	if err != nil {
		return err
	}
	c.forgetNeighbor(ctx, r)
	_, err = c.run(ctx, "route", append([]string{"-n", "add"}, a...)...)
	return err
}

// forgetNeighbor drops the neighbor cache entry of an IPv6 host route's address. The kernel keeps the entry of a
// proxied jail address (static, a day long) after its route is gone; the next jail that is given the same address
// then cannot be reached from the host because the old hardware address is still cached. Best effort: an entry
// that is not there is the normal case.
func (c *Command) forgetNeighbor(ctx context.Context, r Route) {
	p, err := netip.ParsePrefix(r.Dest)
	if err != nil || !p.Addr().Is6() || p.Bits() != 128 {
		return
	}
	_, _ = c.run(ctx, "ndp", "-d", p.Addr().String())
}

// RouteDelete implements Client.
func (c *Command) RouteDelete(ctx context.Context, r Route) error {
	a, err := routeArgs(c.O, r)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "route", append([]string{"-n", "delete"}, a...)...)
	c.forgetNeighbor(ctx, r)
	return err
}

// neighborArg is the address as ndp(8) wants it: a link-local address carries its interface.
func neighborArg(iface string, a netip.Addr) string {
	if a.IsLinkLocalUnicast() {
		return a.String() + "%" + iface
	}
	return a.String()
}

func (c *Command) neighborCheck(iface string, a netip.Addr) error {
	if err := c.O.owned(iface); err != nil {
		return err
	}
	if !a.IsValid() || a.Is4In6() || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
		return api.Errorf(api.CodeInvalid, "%s is not a unicast address", a)
	}
	return nil
}

// neighborCmd returns the tool, its delete arguments and its set arguments for an address: arp(8) for IPv4, ndp(8)
// for IPv6.
func neighborCmd(iface string, a netip.Addr, mac string) (tool string, del, set []string) {
	if a.Is4() {
		return "arp", []string{"-d", a.String()}, []string{"-s", a.String(), mac}
	}
	return "ndp", []string{"-d", neighborArg(iface, a)}, []string{"-s", neighborArg(iface, a), mac}
}

// NeighborSet implements Client with `arp -s` or `ndp -s`, after a delete: an entry that is there is replaced, never kept.
func (c *Command) NeighborSet(ctx context.Context, iface string, a netip.Addr, mac string) error {
	if err := c.neighborCheck(iface, a); err != nil {
		return err
	}
	if hw, err := net.ParseMAC(mac); err != nil || len(hw) != 6 {
		return api.Errorf(api.CodeInvalid, "%q is not an Ethernet address", mac)
	}
	tool, del, set := neighborCmd(iface, a, mac)
	_, _ = c.run(ctx, tool, del...)
	_, err := c.run(ctx, tool, set...)
	return err
}

// NeighborDelete implements Client with `arp -d` or `ndp -d`.
func (c *Command) NeighborDelete(ctx context.Context, iface string, a netip.Addr) error {
	if err := c.neighborCheck(iface, a); err != nil {
		return err
	}
	tool, del, _ := neighborCmd(iface, a, "")
	_, err := c.run(ctx, tool, del...)
	return err
}

// IPv6Forwarding implements Client.
func (c *Command) IPv6Forwarding(ctx context.Context) (bool, error) {
	out, err := c.run(ctx, "sysctl", "-n", "net.inet6.ip6.forwarding")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(out) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	}
	return false, api.Errorf(api.CodeInternal, "unexpected sysctl value %q", strings.TrimSpace(out))
}
