package reconcile_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
)

// routeWorld is a world whose host routing table the test controls.
func routeWorld(t *testing.T, routes map[netip.Addr]string, readErr error) *world {
	w := newWorld(t, reconcile.Config{})
	w.R.Close()
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Sysctl: sysctl, Clock: w.clock,
		HostRoutes: func(context.Context) (map[netip.Addr]string, error) { return routes, readErr }})
	t.Cleanup(w.R.Close)
	return w
}

func addRoute(t *testing.T, w *world, addr, iface string) {
	t.Helper()
	if err := w.Net.RouteAdd(bg, ifconfig.Route{Dest: addr + "/128", Interface: iface}); err != nil {
		t.Fatal(err)
	}
}

// After a crash, the /128 route of a jail that does not exist is removed; the routes of running jails, of jails being
// started and of other interfaces stay.
func TestStaleHostRoutesAreRemoved(t *testing.T) {
	gw := ipam.GatewayIface("jo0")
	live, stale, other, starting := "2001:db8:1::10", "2001:db8:1::77", "2001:db8:1::88", "2001:db8:1::11"
	routes := map[netip.Addr]string{
		netip.MustParseAddr(live): gw, netip.MustParseAddr(stale): gw, netip.MustParseAddr(starting): gw,
		netip.MustParseAddr(other): "jo99gb", // the gateway of a network that is not in this table
	}
	w := routeWorld(t, routes, nil)
	for a, i := range routes {
		addRoute(t, w, a.String(), i)
	}
	running, booting := jailDesired("web"), jailDesired("db")
	running.Proxy = []reconcile.ProxyAddr{{Addr: live, Bridge: gw, Uplink: "em0"}}
	booting.ID = "b"
	booting.Proxy = []reconcile.ProxyAddr{{Addr: starting, Bridge: gw, Uplink: "em0"}}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}, Jails: []reconcile.JailDesired{running, booting}}
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	w.mu.Lock()
	w.locks["jail/db"] = true // its start is in progress
	w.mu.Unlock()
	rep := w.R.Pass(bg)
	if len(rep.StaleRoutes) != 1 || rep.StaleRoutes[0] != stale {
		t.Fatalf("stale routes %v, failures %v", rep.StaleRoutes, rep.Failures)
	}
	left := w.Net.Routes()
	if len(left) != 3 {
		t.Fatalf("routes left: %v", left)
	}
	// the jail stopped and nothing is in progress: its route goes too
	w.mu.Lock()
	delete(w.locks, "jail/db")
	w.mu.Unlock()
	routes[netip.MustParseAddr(stale)] = "none"
	rep = w.R.Pass(bg)
	if len(rep.StaleRoutes) != 1 || rep.StaleRoutes[0] != starting {
		t.Fatalf("stale routes %v", rep.StaleRoutes)
	}
}

// If a jail could not be rendered its addresses are unknown: nothing is removed.
func TestStaleHostRoutesNeedEveryJailKnown(t *testing.T) {
	gw := ipam.GatewayIface("jo0")
	routes := map[netip.Addr]string{netip.MustParseAddr("2001:db8:1::77"): gw}
	w := routeWorld(t, routes, nil)
	addRoute(t, w, "2001:db8:1::77", gw)
	broken := jailDesired("web")
	broken.Problem = "cannot render"
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}, Jails: []reconcile.JailDesired{broken}}
	if rep := w.R.Pass(bg); len(rep.StaleRoutes) != 0 || len(w.Net.Routes()) != 1 {
		t.Fatalf("%v", rep.StaleRoutes)
	}
}

func TestStaleHostRoutesReadFailureIsReported(t *testing.T) {
	w := routeWorld(t, nil, errors.New("netstat failed"))
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}}
	if rep := w.R.Pass(bg); rep.Failures["routes"] == "" {
		t.Fatalf("%v", rep.Failures)
	}
}

// Networks that are not in proxy mode have no host routes of this kind: nothing is read or removed.
func TestStaleHostRoutesOnlyForProxyNetworks(t *testing.T) {
	read := false
	w := newWorld(t, reconcile.Config{})
	w.R.Close()
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Sysctl: sysctl, Clock: w.clock,
		HostRoutes: func(context.Context) (map[netip.Addr]string, error) { read = true; return nil, nil }})
	t.Cleanup(w.R.Close)
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}}
	w.R.Pass(bg)
	if read {
		t.Fatal("the routing table was read for a network without a proxy")
	}
}

// Link-local static entries of a jail that is gone are removed; those of a running jail, the host's own entry and
// entries on other interfaces stay.
func TestStaleLinkLocalNeighborsAreRemoved(t *testing.T) {
	gw := ipam.GatewayIface("jo0")
	macGone, macLive := "02:aa:00:00:00:01", "02:aa:00:00:00:02"
	llGone, _ := ipam.LinkLocalFromMAC(macGone)
	llLive, _ := ipam.LinkLocalFromMAC(macLive)
	host := netip.MustParseAddr("fe80::1")
	other := netip.MustParseAddr("fe80::1234")
	w := newWorld(t, reconcile.Config{})
	w.R.Close()
	list := []ifconfig.Neighbor{
		{Addr: llGone, Iface: gw, MAC: macGone},
		{Addr: llLive, Iface: gw, MAC: macLive},
		{Addr: host, Iface: gw, MAC: "58:9c:fc:10:5e:2c"},                             // the host's own address: not formed from its MAC
		{Addr: llGone, Iface: "jo99gb", MAC: macGone},                                 // the gateway of a network that is not ours here
		{Addr: other, Iface: gw, MAC: macGone},                                        // does not match the MAC it points at
		{Addr: netip.MustParseAddr("10.20.0.30"), Iface: gw, MAC: macGone},            // IPv4 of a jail that is gone
		{Addr: netip.MustParseAddr("10.20.0.31"), Iface: gw, MAC: macLive},            // IPv4 of the running jail
		{Addr: netip.MustParseAddr("10.20.0.1"), Iface: gw, MAC: "58:9c:fc:10:5e:2c"}, // the host's own gateway address
		{Addr: netip.MustParseAddr("192.0.2.9"), Iface: gw, MAC: macGone},             // outside the network's subnet
	}
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Sysctl: sysctl, Clock: w.clock,
		HostNeighbors: func(context.Context) ([]ifconfig.Neighbor, error) { return list, nil }})
	t.Cleanup(w.R.Close)
	for _, n := range list {
		if n.Iface == gw {
			if err := w.Net.NeighborSet(bg, gw, n.Addr, n.MAC); err != nil {
				t.Fatal(err)
			}
		}
	}
	live := jailDesired("web")
	live.Attach = [][]render.Step{{{Op: render.OpSetNeighbor, Phase: render.PhaseHost, Iface: gw, Arg: llLive.String(), Arg2: macLive}, {Op: render.OpSetNeighbor, Phase: render.PhaseHost, Iface: gw, Arg: "10.20.0.31", Arg2: macLive}}}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{live}}
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	rep := w.R.Pass(bg)
	if len(rep.StaleNeighbors) != 2 || rep.StaleNeighbors[0] != llGone.String() && rep.StaleNeighbors[1] != llGone.String() {
		t.Fatalf("stale %v failures %v", rep.StaleNeighbors, rep.Failures)
	}
	left := w.Net.Neighbors()
	if len(left) != 6 || left[gw+" "+llGone.String()] != "" || left[gw+" "+llLive.String()] == "" || left[gw+" 10.20.0.30"] != "" || left[gw+" 10.20.0.31"] == "" || left[gw+" 10.20.0.1"] == "" {
		t.Fatalf("entries left: %v", left)
	}
}
