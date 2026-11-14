package priv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// Bounds for an epair MTU. 576 is the IPv4 minimum, 9216 the
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
// the command Runner for "ifconfig <epair> mtu <n>".
// UNVERIFIED on FreeBSD.
func (l *Local) SetMTU(ctx context.Context, iface string, mtu int) error {
	if mtu < MinMTU || mtu > MaxMTU {
		return invalid("mtu %d is outside %d..%d", mtu, MinMTU, MaxMTU)
	}
	if err := l.ifOwned(ctx, iface); err != nil {
		return err
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

// NDPRouteAdd adds the /128 host route addr through iface (an epair of JailOps; netGuard.RouteAdd reads it in the kernel). Nothing else: no gateway, no prefix, no default.
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
	if err := ifconfig.ValidateIfName(iface); err != nil {
		return ifconfig.Route{}, err
	}
	return ifconfig.Route{Dest: netip.PrefixFrom(addr, 128).String(), Interface: iface}, nil
}

// checkRejectRoute accepts a reject route only for the subnet of a network JailOps has stored (its IPv4 subnet, or the
// IPv6 prefix of a network in routed mode): the helper never makes a route that answers with an error for any other
// prefix, which would cut the host off from it.
func (l *Local) checkRejectRoute(ctx context.Context, r ifconfig.Route) error {
	p, err := netip.ParsePrefix(r.Dest)
	if err != nil || r.Gateway != "" || r.Interface != "" {
		return invalid("invalid reject route %q", r.Dest)
	}
	dir := filepath.Join(l.cfg.StateDir, "objects", string(api.KindNetwork))
	names, err := l.listState(dir)
	if err != nil {
		return fmt.Errorf("network objects: %w", err)
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := l.readState(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var d networkDoc
		if json.Unmarshal(b, &d) != nil {
			continue
		}
		if v4 := d.Spec.IPv4; v4 != nil {
			if q, err := netip.ParsePrefix(v4.Subnet); err == nil && q.Masked() == p.Masked() {
				return nil
			}
		}
		if v6 := d.Spec.IPv6; v6 != nil && v6.Mode == api.IPv6Routed {
			if q, err := netip.ParsePrefix(v6.Subnet); err == nil && q.Masked() == p.Masked() {
				return nil
			}
		}
	}
	return forbidden("%s is not the subnet of a network of JailOps", p)
}

// storedNetworks are the networks JailOps has stored.
func (l *Local) storedNetworks() ([]networkDoc, error) {
	dir := filepath.Join(l.cfg.StateDir, "objects", string(api.KindNetwork))
	names, err := l.listState(dir)
	if err != nil {
		return nil, fmt.Errorf("network objects: %w", err)
	}
	var out []networkDoc
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := l.readState(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var d networkDoc
		if json.Unmarshal(b, &d) == nil {
			out = append(out, d)
		}
	}
	return out, nil
}
