package priv

import (
	"context"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// The interfaces of a host as the kernel shows them, for the ownership matrix: what JailOps made, what an older
// release made, and everything that is someone else's.
func ownershipHost(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	if err := e.net.BridgeCreate(ctx, "palminn"); err != nil { // a network's bridge with a free name
		t.Fatal(err)
	}
	if _, _, err := e.net.EpairCreate(ctx, "palminng"); err != nil {
		t.Fatal(err)
	}
	for _, i := range []ifconfig.Iface{
		{Name: "jo7", Groups: []string{"bridge"}},                                   // an older release's bridge, no description
		{Name: "jo8", Groups: []string{"bridge"}, Description: "customer bridge"},   // a legacy name with someone's description
		{Name: "jo9", Groups: []string{"vmx"}},                                      // a physical interface renamed to a legacy name
		{Name: "vmx0", Groups: []string{"vmx"}, Description: "uplink"},              // the uplink
		{Name: "vmx1", Groups: []string{"vmx"}, Description: ifconfig.Marker},       // an uplink an administrator marked by hand
		{Name: "bridge0", Groups: []string{"bridge"}},                               // a bridge of the kernel's name
		{Name: "tap0", Groups: []string{"tap"}, Description: "jailops wireguard=x"}, // another kind, whatever the description
	} {
		e.net.AddIface(i)
	}
}

// Every refusal of the helper's ownership rule, through the guard and over the socket: the matrix of the threat model
// in docs/security-model.md ("Interfaces").
func TestInterfaceOwnershipMatrix(t *testing.T) {
	f := newFixtureWith(t, func(e *env) { ownershipHost(t, e); e.conf(t, "shop_web") })
	ctx := context.Background()
	for _, via := range []struct {
		name string
		n    ifconfig.Client
	}{{"guard", f.env.local.Net()}, {"socket", f.client.Net()}} {
		n := via.n
		foreign := []string{"mybr", "em0", "vmx0", "vmx1", "jo8", "jo9", "bridge0", "tap0", "lo0"}
		for _, name := range foreign {
			for k, err := range []error{
				n.BridgeDestroy(ctx, name),
				n.BridgeAddMember(ctx, name, "palminnga", false),
				n.BridgeAddMember(ctx, "palminn", name, false), // never a port of a JailOps bridge
				n.BridgeDeleteMember(ctx, name, "palminnga"),
				n.SetDescription(ctx, name, ifconfig.Marker), // the marker is never written on what exists
				n.SetDescription(ctx, name, ""),
				n.EpairDestroy(ctx, name),
				n.SetLink(ctx, name, false),
				n.AddrAdd(ctx, name, netip.MustParsePrefix("10.9.0.1/24")),
				n.AddrRemove(ctx, name, netip.MustParsePrefix("10.9.0.1/24")),
				n.RouteAdd(ctx, ifconfig.Route{Dest: "2001:db8::7/128", Interface: name}),
				n.NeighborSet(ctx, name, netip.MustParseAddr("2001:db8::7"), "02:00:00:00:00:07"),
				n.NeighborDelete(ctx, name, netip.MustParseAddr("2001:db8::7")),
				n.BridgeCreate(ctx, name), // a name that exists is never adopted (or is a driver's)
				n.MoveToJail(ctx, name, "shop_web"),
				f.client.SetMTU(ctx, name, 1400),
			} {
				if err == nil || !(api.IsCode(err, api.CodeForbidden) || api.IsCode(err, api.CodeInvalid)) {
					t.Errorf("%s %s case %d: %v", via.name, name, k, err)
				}
			}
		}
		if err := n.BridgeCreate(ctx, "mybr"); !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "never takes over an interface by its name") {
			t.Errorf("%s: create over a hand-made bridge: %v", via.name, err)
		}
		// names of drivers, made or not
		for _, name := range []string{"vmx0", "em0", "lo0", "bridge0", "bridge", "epair0a", "tap0", "wg0", "pflog0", "vlan0.10", "igb3", "all"} {
			if err := n.BridgeCreate(ctx, name); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "form of the interfaces of the driver") {
				t.Errorf("%s: create %s: %v", via.name, name, err)
			}
		}
		if _, _, err := n.EpairCreate(ctx, "epair5"); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: epair under the kernel's names: %v", via.name, err)
		}
		// a description that would take the marker off is refused; clearing keeps it
		if err := n.SetDescription(ctx, "palminn", "my text"); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: marker removable: %v", via.name, err)
		}
		if err := n.SetDescription(ctx, "palminn", ""); err != nil {
			t.Fatal(err)
		}
		if d := desc(t, f.env, "palminn"); d != ifconfig.Marker {
			t.Errorf("%s: cleared to %q", via.name, d)
		}
		// creating what JailOps already owns is idempotent for the caller
		if err := n.BridgeCreate(ctx, "palminn"); !api.IsCode(err, api.CodeAlreadyExists) {
			t.Errorf("%s: create of an owned bridge: %v", via.name, err)
		}
	}
	// reading stays open: the uplink and the rest are listed
	ifs, err := f.client.Net().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, i := range ifs {
		seen[i.Name] = true
	}
	for _, n := range []string{"vmx0", "em0", "mybr", "lo0", "palminn"} {
		if !seen[n] {
			t.Errorf("%s not listed", n)
		}
	}
	// nothing foreign changed
	for name, want := range map[string]string{"vmx0": "uplink", "jo8": "customer bridge", "mybr": "", "bridge0": "", "jo9": ""} {
		if d := desc(t, f.env, name); d != want {
			t.Errorf("%s: description %q, want %q", name, d, want)
		}
	}
}

func desc(t *testing.T, e *env, name string) string {
	t.Helper()
	i, err := e.net.Show(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return i.Description
}

// A JailOps network can have any free name; everything a network and its jails do on it works.
func TestInterfaceOwnershipFreeName(t *testing.T) {
	f := newFixtureWith(t, func(e *env) {
		e.conf(t, "shop_web")
		e.runner.On("ifconfig", []string{"palminn", "mtu", "1400"}, "")
	})
	ctx := context.Background()
	n := f.client.Net()
	if err := n.BridgeCreate(ctx, "palminn"); err != nil {
		t.Fatal(err)
	}
	m, l3, err := n.EpairCreate(ctx, "palminng")
	if err != nil || m != "palminnga" || l3 != "palminngb" {
		t.Fatal(m, l3, err)
	}
	a, b, err := n.EpairCreate(ctx, "jo0123456789a0")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		n.SetDescription(ctx, "palminn", "jailops net=palminn bridge"),
		n.BridgeAddMember(ctx, "palminn", m, false),
		n.BridgeAddMember(ctx, "palminn", a, true),
		n.AddrAdd(ctx, l3, netip.MustParsePrefix("172.17.32.1/24")),
		n.SetLink(ctx, "palminn", true),
		n.RouteAdd(ctx, ifconfig.Route{Dest: "2001:db8::7/128", Interface: l3}),
		n.NeighborSet(ctx, l3, netip.MustParseAddr("2001:db8::7"), "02:00:00:00:00:07"),
		f.client.SetMTU(ctx, "palminn", 1400),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	// the jail end keeps its marker in the jail's vnet, and comes back
	if err := n.MoveToJail(ctx, b, "shop_web"); err != nil {
		t.Fatal(err)
	}
	if i, err := f.env.net.ShowInJail(ctx, "shop_web", b); err != nil || i.Description != ifconfig.Marker {
		t.Fatalf("%+v %v", i, err)
	}
	if err := n.MoveFromJail(ctx, b, "shop_web"); err != nil {
		t.Fatal(err)
	}
	if err := n.EpairDestroy(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := n.EpairDestroy(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := n.BridgeDestroy(ctx, "palminn"); err != nil {
		t.Fatal(err)
	}
}

// An interface of an earlier release (legacy prefix, no description) is accepted once and marked; one with someone
// else's description, or of another kind, is not.
func TestInterfaceOwnershipLegacy(t *testing.T) {
	f := newFixtureWith(t, func(e *env) { ownershipHost(t, e) })
	ctx := context.Background()
	n := f.client.Net()
	if err := n.SetLink(ctx, "jo7", true); err != nil {
		t.Fatal(err)
	}
	if d := desc(t, f.env, "jo7"); d != ifconfig.Marker {
		t.Fatalf("jo7 not marked: %q", d)
	}
	if err := n.BridgeDestroy(ctx, "jo7"); err != nil {
		t.Fatal(err)
	}
	if err := n.SetLink(ctx, "jo8", true); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("legacy name with a foreign description: %v", err)
	}
	if err := n.SetLink(ctx, "jo9", true); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("legacy name on a physical interface: %v", err)
	}
	// priv.conf's if-prefix names the legacy prefixes; without jo, a bare jo7 is foreign
	e := newEnv(t)
	cfg := e.cfg
	cfg.IfPrefixes = []string{"old"}
	net := ifconfig.NewFake(ifconfig.Options{Prefixes: cfg.IfPrefixes}) // the helper builds both from priv.conf
	l, err := NewLocal(cfg, Deps{ZFS: e.zfs, Jails: e.jails, PF: e.pf, Net: net, Runner: e.runner}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	net.AddIface(ifconfig.Iface{Name: "jo7", Groups: []string{"bridge"}})
	net.AddIface(ifconfig.Iface{Name: "old3", Groups: []string{"bridge"}})
	if err := l.Net().SetLink(ctx, "jo7", true); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("jo7 without the prefix: %v", err)
	}
	if err := l.Net().SetLink(ctx, "old3", true); err != nil {
		t.Errorf("old3: %v", err)
	}
}

// jail(8) moves vnet.interface into the jail itself: one that exists and is not JailOps's stops the jail's creation.
func TestJailCreateRefusesAForeignVnetInterface(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conf := filepath.Join(e.cfg.ConfDir, "alpha_web.conf")
	e.conf(t, "alpha_web")
	e.net.AddIface(ifconfig.Iface{Name: "jo0123456789a0b", Groups: []string{"epair"}, Description: "someone's"})
	if err := e.local.Jails().Create(ctx, conf, "alpha_web"); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("foreign vnet.interface: %v", err)
	}
	// a JailOps epair, or none yet (the prestart hook makes it), is fine
	for _, made := range []bool{false, true} {
		e := newEnv(t)
		e.conf(t, "alpha_web")
		if made {
			if _, _, err := e.net.EpairCreate(ctx, "jo0123456789a0"); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.local.Jails().Create(ctx, filepath.Join(e.cfg.ConfDir, "alpha_web.conf"), "alpha_web"); err != nil {
			t.Errorf("made %v: %v", made, err)
		}
	}
}

// twinNet shows a host interface of every name, as the host does when a name is taken (test helper).
type twinNet struct{ *ifconfig.Fake }

func (t twinNet) Show(_ context.Context, name string) (ifconfig.Iface, error) {
	return ifconfig.Iface{Name: name, Groups: []string{"vmx"}}, nil
}

// The kernel brings an interface back from a jail's vnet although the host has one of that name; the helper does not.
func TestMoveFromJailNeverDoublesAHostName(t *testing.T) {
	e := newEnv(t)
	e.conf(t, "shop_web")
	e.net.AddToJail("shop_web", ifconfig.Iface{Name: "vtnet0", Groups: []string{"epair"}, Description: ifconfig.Marker}) // forged in the jail
	l, err := NewLocal(e.cfg, Deps{ZFS: e.zfs, Jails: e.jails, PF: e.pf, Net: twinNet{e.net}, Runner: e.runner}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Net().MoveFromJail(context.Background(), "vtnet0", "shop_web"); !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "exists on the host") {
		t.Fatalf("%v", err)
	}
}
