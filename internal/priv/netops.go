package priv

import (
	"context"
	"net/netip"
	"strconv"

	"jailops.net/jailops/internal/sys/ifconfig"
)

// Bounds for a bridge or epair MTU. 576 is the IPv4 minimum, 9216 the
// largest jumbo frame common drivers take.
const (
	MinMTU = 576
	MaxMTU = 9216
)

// mtuSetter is implemented by an ifconfig.Client that can change an MTU.
// PROPOSED interface change (internal/sys/ifconfig is not edited here):
//
//	SetMTU(ctx context.Context, iface string, mtu int) error
//
// until ifconfig.Client has it, Local runs ifconfig(8) itself with a fixed
// argument vector after validating the interface and the number.
type mtuSetter interface {
	SetMTU(ctx context.Context, iface string, mtu int) error
}

// SetMTU sets the MTU of an interface JailOps owns. It replaces any use of
// the command Runner for "ifconfig <bridge> mtu <n>".
// UNVERIFIED on FreeBSD.
func (l *Local) SetMTU(ctx context.Context, iface string, mtu int) error {
	if err := l.cfg.ifOwned(iface); err != nil {
		return err
	}
	if mtu < MinMTU || mtu > MaxMTU {
		return invalid("mtu %d is outside %d..%d", mtu, MinMTU, MaxMTU)
	}
	if s, ok := l.d.Net.(mtuSetter); ok {
		return s.SetMTU(ctx, iface, mtu)
	}
	return l.ifconfigRun(ctx, iface, "mtu", strconv.Itoa(mtu))
}

// checkNDPRoute accepts only a global unicast IPv6 address (unique-local
// included): a /128 host route for an address JailOps answers neighbor
// discovery for in proxy mode.
func checkNDPRoute(addr netip.Addr) error {
	switch {
	case !addr.IsValid() || !addr.Is6() || addr.Is4In6() || addr.Zone() != "":
		return invalid("%q is not a plain IPv6 address", addr)
	case !addr.IsGlobalUnicast() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsLoopback() || addr.IsUnspecified():
		return forbidden("%s is not a global unicast address", addr)
	}
	return nil
}

// NDPRouteAdd adds the /128 host route addr through iface (a bridge or other
// interface JailOps owns). Nothing else: no gateway, no prefix, no default.
// UNVERIFIED on FreeBSD.
func (l *Local) NDPRouteAdd(ctx context.Context, addr netip.Addr, iface string) error {
	r, err := l.ndpRoute(addr, iface)
	if err != nil {
		return err
	}
	return l.Net().RouteAdd(ctx, r)
}

// NDPRouteDelete removes a route NDPRouteAdd made.
func (l *Local) NDPRouteDelete(ctx context.Context, addr netip.Addr, iface string) error {
	r, err := l.ndpRoute(addr, iface)
	if err != nil {
		return err
	}
	return l.Net().RouteDelete(ctx, r)
}

func (l *Local) ndpRoute(addr netip.Addr, iface string) (ifconfig.Route, error) {
	if err := checkNDPRoute(addr); err != nil {
		return ifconfig.Route{}, err
	}
	if err := l.cfg.ifOwned(iface); err != nil {
		return ifconfig.Route{}, err
	}
	return ifconfig.Route{Dest: netip.PrefixFrom(addr, 128).String(), Interface: iface}, nil
}
