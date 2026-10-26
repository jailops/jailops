package ifconfig

import (
	"context"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

var ctx = context.Background()

func TestParseIfconfigGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/ifconfig_a.txt")
	if err != nil {
		t.Fatal(err)
	}
	ifs := ParseIfconfig(string(b))
	byName := map[string]Iface{}
	for _, i := range ifs {
		byName[i.Name] = i
	}
	for _, n := range []string{"em0", "lo0", "jo0", "joweba"} {
		if _, ok := byName[n]; !ok {
			t.Fatalf("missing %s in %v", n, ifs)
		}
	}
	if len(ifs) != 4 {
		t.Errorf("want 4 interfaces, got %d (the bad header must be dropped)", len(ifs))
	}
	em := byName["em0"]
	if em.MTU != 1500 || !em.Up() || em.MAC != "52:54:00:12:34:56" || em.Status != "active" {
		t.Errorf("em0: %+v", em)
	}
	if !reflect.DeepEqual(em.Inet, []netip.Prefix{netip.MustParsePrefix("192.168.1.10/24")}) {
		t.Errorf("inet %v", em.Inet)
	}
	if !reflect.DeepEqual(em.Inet6, []netip.Prefix{netip.MustParsePrefix("fe80::5054:ff:fe12:3456/64"), netip.MustParsePrefix("2001:db8:1::10/64")}) {
		t.Errorf("inet6 %v", em.Inet6)
	}
	if lo := byName["lo0"]; lo.MTU != 16384 || lo.Inet[0] != netip.MustParsePrefix("127.0.0.1/8") || lo.Inet6[0].Bits() != 128 {
		t.Errorf("lo0: %+v", lo)
	}
	jo := byName["jo0"]
	if jo.Description != "jailops:net-lan office bridge" || !reflect.DeepEqual(jo.Groups, []string{"bridge"}) {
		t.Errorf("jo0: %+v", jo)
	}
	if len(jo.Members) != 2 || jo.Members[0].Name != "joweba" || !jo.Members[0].Private || jo.Members[1].Private {
		t.Errorf("members: %+v", jo.Members)
	}
}

func FuzzParseIfconfig(f *testing.F) {
	b, _ := os.ReadFile("testdata/ifconfig_a.txt")
	f.Add(string(b))
	f.Add("x: flags=<\n\tinet 1.2.3.4 netmask\n\tmember:\n")
	f.Fuzz(func(t *testing.T, s string) {
		for _, i := range ParseIfconfig(s) {
			if ValidateIfName(i.Name) != nil {
				t.Fatalf("bad name %q", i.Name)
			}
		}
	})
}

func TestMaskBits(t *testing.T) {
	for in, want := range map[string]int{"0xffffff00": 24, "0xffffffff": 32, "0x0": 0, "255.255.0.0": 16, "0xff00ff00": -1, "zz": -1, "0x1ffffffff": -1} {
		got, ok := maskBits(in)
		if want < 0 && ok || want >= 0 && (!ok || got != want) {
			t.Errorf("%s: %d %v", in, got, ok)
		}
	}
}

func TestValidateNames(t *testing.T) {
	for in, ok := range map[string]bool{"jo0": true, "em0": true, "epair0a": true, "vlan.10": true, "": false, "0jb": false,
		"-jo": false, "jo 0": false, "jo;0": false, strings.Repeat("a", 16): false, strings.Repeat("a", 15): true} {
		if err := ValidateIfName(in); (err == nil) != ok {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func newCmd() (*Command, *run.Fake) {
	f := run.NewFake()
	return NewCommand(f, Options{}), f
}

func TestCommandBridgeAndEpair(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"bridge", "create"}, "bridge0\n")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	if err := c.BridgeCreate(ctx, "jo0"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDescription(ctx, "jo0", "jailops:abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.BridgeAddMember(ctx, "jo0", "joweba", true); err != nil {
		t.Fatal(err)
	}
	if err := c.BridgeAddMember(ctx, "jo0", "jodbea", false); err != nil {
		t.Fatal(err)
	}
	if err := c.BridgeDeleteMember(ctx, "jo0", "joweba"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddrAdd(ctx, "jo0", netip.MustParsePrefix("10.77.0.1/24")); err != nil {
		t.Fatal(err)
	}
	if err := c.AddrRemove(ctx, "jo0", netip.MustParsePrefix("fd00::1/64")); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLink(ctx, "jo0", true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLink(ctx, "jo0", false); err != nil {
		t.Fatal(err)
	}
	if err := c.BridgeDestroy(ctx, "jo0"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ifconfig bridge create",
		"ifconfig bridge0 name jo0",
		"ifconfig jo0 description jailops:abc",
		"ifconfig jo0 addm joweba private joweba",
		"ifconfig jo0 addm jodbea",
		"ifconfig jo0 deletem joweba",
		"ifconfig jo0 inet 10.77.0.1/24 alias",
		"ifconfig jo0 inet6 fd00::1 prefixlen 64 -alias",
		"ifconfig jo0 up",
		"ifconfig jo0 down",
		"ifconfig jo0 destroy",
	}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("got  %q\nwant %q", f.Argv(), want)
	}
}

func TestCommandEpair(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"epair", "create"}, "epair3a\n")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	a, b, err := c.EpairCreate(ctx, "joweb")
	if err != nil || a != "joweba" || b != "jowebb" {
		t.Fatal(a, b, err)
	}
	if err := c.MoveToJail(ctx, a, "web"); err != nil {
		t.Fatal(err)
	}
	if err := c.EpairDestroy(ctx, a); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ifconfig epair create", "ifconfig epair3b name jowebb", "ifconfig epair3a name joweba",
		"ifconfig joweba vnet web", "ifconfig joweba destroy",
	}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("%q", f.Argv())
	}
}

func TestEpairRenameFailureCleansUp(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"epair", "create"}, "epair3a\n")
	f.OnFail("ifconfig", []string{"epair3b", "name", "jowebb"}, 1, "ifconfig: SIOCSIFNAME: File exists")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"epair3a", "destroy"}})
	if _, _, err := c.EpairCreate(ctx, "joweb"); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatal(err)
	}
	if last := f.Argv()[len(f.Argv())-1]; last != "ifconfig epair3a destroy" {
		t.Errorf("no cleanup: %q", f.Argv())
	}
}

func TestPrefixEnforcement(t *testing.T) {
	c, f := newCmd()
	for i, err := range []error{
		c.BridgeDestroy(ctx, "bridge0"),
		c.BridgeCreate(ctx, "em0"),
		c.BridgeAddMember(ctx, "bridge0", "joweba", false),
		c.SetLink(ctx, "em0", false),
		c.AddrAdd(ctx, "em0", netip.MustParsePrefix("10.0.0.1/24")),
		c.EpairDestroy(ctx, "epair0a"),
		c.MoveToJail(ctx, "em0", "web"),
		c.BridgeDestroy(ctx, "jo"), // prefix alone is not a name
	} {
		if !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if len(f.Calls()) != 0 {
		t.Error("foreign interface touched")
	}
	// explicit opt-in
	f2 := run.NewFake()
	f2.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	if err := NewCommand(f2, Options{AllowForeign: true}).SetLink(ctx, "em0", true); err != nil {
		t.Fatal(err)
	}
	// custom prefix
	if err := NewCommand(f2, Options{Prefixes: []string{"jail"}}).SetLink(ctx, "jail0", true); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidArguments(t *testing.T) {
	c, f := newCmd()
	for i, err := range []error{
		c.SetLink(ctx, "jo0;reboot", true),
		c.BridgeAddMember(ctx, "jo0", "-x", false),
		c.MoveToJail(ctx, "joweba", "-j"),
		c.MoveToJail(ctx, "joweba", "a b"),
		c.SetDescription(ctx, "jo0", "line\nbreak"),
		c.AddrAdd(ctx, "jo0", netip.Prefix{}),
		c.RouteAdd(ctx, Route{Dest: "10.0.0.0/8; reboot", Gateway: "10.0.0.1"}),
		c.RouteAdd(ctx, Route{Dest: "10.0.0.0/8", Gateway: "fe80::1"}),
		c.RouteAdd(ctx, Route{Dest: "10.0.0.0/8"}),
		c.RouteAdd(ctx, Route{Dest: "default", Interface: "jo0"}),
	} {
		if !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if len(f.Calls()) != 0 {
		t.Errorf("ran %q", f.Argv())
	}
}

func TestRoutes(t *testing.T) {
	c, f := newCmd()
	f.Add(run.Rule{Name: "route", Args: []string{run.RestArgs}})
	for _, err := range []error{
		c.RouteAdd(ctx, Route{Dest: "10.1.2.3/24", Gateway: "10.0.0.1"}),
		c.RouteAdd(ctx, Route{Dest: "default", Gateway: "fe80::1"}),
		c.RouteAdd(ctx, Route{Dest: "2001:db8::5", Interface: "jo0"}),
		c.RouteDelete(ctx, Route{Dest: "2001:db8:1::/64", Gateway: "fd00::1"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"route -n add -inet 10.1.2.0/24 10.0.0.1",
		"route -n add -inet6 default fe80::1",
		"route -n add -inet6 2001:db8::5 -interface jo0",
		"route -n delete -inet6 2001:db8:1::/64 fd00::1",
	}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("%q", f.Argv())
	}
}

func TestForwardingAndList(t *testing.T) {
	c, f := newCmd()
	f.On("sysctl", []string{"-n", "net.inet6.ip6.forwarding"}, "1\n")
	if on, err := c.IPv6Forwarding(ctx); err != nil || !on {
		t.Fatal(on, err)
	}
	f.Reset()
	f2 := run.NewFake()
	f2.On("sysctl", []string{run.RestArgs}, "banana\n")
	if _, err := NewCommand(f2, Options{}).IPv6Forwarding(ctx); err == nil {
		t.Fatal("garbage accepted")
	}
	b, _ := os.ReadFile("testdata/ifconfig_a.txt")
	f.On("ifconfig", []string{"-a"}, string(b))
	ifs, err := c.List(ctx)
	if err != nil || len(ifs) != 4 {
		t.Fatal(len(ifs), err)
	}
}

func TestFake(t *testing.T) {
	f := NewFake(Options{})
	f.AddForeign("em0", true)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.BridgeCreate(ctx, "jo0"))
	if !api.IsCode(f.BridgeCreate(ctx, "jo0"), api.CodeAlreadyExists) {
		t.Fatal("dup bridge")
	}
	a, b, err := f.EpairCreate(ctx, "joweb")
	must(err)
	must(f.BridgeAddMember(ctx, "jo0", a, true))
	if !api.IsCode(f.BridgeAddMember(ctx, "jo0", a, false), api.CodeAlreadyExists) {
		t.Fatal("dup member")
	}
	must(f.AddrAdd(ctx, "jo0", netip.MustParsePrefix("10.0.0.1/24")))
	must(f.SetLink(ctx, "jo0", true))
	must(f.MoveToJail(ctx, b, "web"))
	ifs, _ := f.List(ctx)
	names := []string{}
	for _, i := range ifs {
		names = append(names, i.Name)
	}
	if !reflect.DeepEqual(names, []string{"em0", "jo0", "joweba", "lo0"}) {
		t.Fatalf("%v", names)
	}
	if !ifs[1].Up() || len(ifs[1].Members) != 1 || !ifs[1].Members[0].Private {
		t.Fatalf("%+v", ifs[1])
	}
	if !api.IsCode(f.SetLink(ctx, "em0", false), api.CodeForbidden) {
		t.Fatal("foreign")
	}
	must(f.RouteAdd(ctx, Route{Dest: "default", Gateway: "10.0.0.254"}))
	if !api.IsCode(f.RouteAdd(ctx, Route{Dest: "default", Gateway: "10.0.0.254"}), api.CodeAlreadyExists) {
		t.Fatal("dup route")
	}
	must(f.RouteDelete(ctx, Route{Dest: "default", Gateway: "10.0.0.254"}))
	must(f.EpairDestroy(ctx, a))
	ifs, _ = f.List(ctx)
	if len(ifs) != 3 || len(ifs[1].Members) != 0 {
		t.Fatalf("%+v", ifs)
	}
	f.Forwarding = true
	if on, _ := f.IPv6Forwarding(ctx); !on {
		t.Fatal("forwarding")
	}
}

// A proxied jail address is reused by the next jail with another hardware address; the kernel's cached entry of the
// old one must go when the host route does, and before the route of the next jail is made.
func TestHostRoutesForgetTheNeighborEntry(t *testing.T) {
	c, f := newCmd()
	f.Add(run.Rule{Name: "route", Args: []string{run.RestArgs}})
	f.Add(run.Rule{Name: "ndp", Args: []string{run.RestArgs}})
	r := Route{Dest: "2001:db8:1:2:100::10/128", Interface: "jo0"}
	if err := c.RouteDelete(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := c.RouteAdd(ctx, r); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route -n delete -inet6 2001:db8:1:2:100::10/128 -interface jo0",
		"ndp -d 2001:db8:1:2:100::10",
		"ndp -d 2001:db8:1:2:100::10",
		"route -n add -inet6 2001:db8:1:2:100::10/128 -interface jo0",
	}
	got := f.Argv()
	if len(got) != len(want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
	// IPv4 and prefixes are not neighbors of this kind
	c2, f2 := newCmd()
	f2.Add(run.Rule{Name: "route", Args: []string{run.RestArgs}})
	if err := c2.RouteAdd(ctx, Route{Dest: "10.9.0.0/24", Gateway: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range f2.Argv() {
		if len(a) >= 3 && a[:3] == "ndp" {
			t.Errorf("ndp ran for an IPv4 route: %q", a)
		}
	}
}

// The line is copied from FreeBSD 15.1 (ifconfig vlan77 create vlan 77 vlandev epair5a).
func TestParseVlan(t *testing.T) {
	out := "vlan77: flags=1008842<BROADCAST,RUNNING,SIMPLEX,MULTICAST,LOWER_UP> metric 0 mtu 1500\n" +
		"\toptions=0\n\tether 58:9c:fc:10:1f:35\n\tgroups: vlan\n" +
		"\tvlan: 77 vlanproto: 802.1q vlanpcp: 0 parent interface: epair5a\n\tstatus: active\n"
	ifs := ParseIfconfig(out)
	if len(ifs) != 1 || ifs[0].VlanTag != 77 || ifs[0].VlanParent != "epair5a" || ifs[0].Status != "active" {
		t.Fatalf("%+v", ifs)
	}
}

// A static neighbor entry replaces whatever is there (delete first), and a link-local address carries its interface.
func TestCommandNeighbor(t *testing.T) {
	c, f := newCmd()
	f.Add(run.Rule{Name: "ndp", Args: []string{run.RestArgs}})
	f.Add(run.Rule{Name: "arp", Args: []string{run.RestArgs}})
	mac := "ca:64:60:d0:bb:57"
	if err := c.NeighborSet(ctx, "jo0gb", netip.MustParseAddr("2001:db8:2::100"), mac); err != nil {
		t.Fatal(err)
	}
	if err := c.NeighborSet(ctx, "jo0gb", netip.MustParseAddr("fe80::c864:60ff:fed0:bb57"), mac); err != nil {
		t.Fatal(err)
	}
	if err := c.NeighborDelete(ctx, "jo0gb", netip.MustParseAddr("fe80::c864:60ff:fed0:bb57")); err != nil {
		t.Fatal(err)
	}
	if err := c.NeighborSet(ctx, "jo0gb", netip.MustParseAddr("10.20.0.12"), mac); err != nil { // IPv4 is ARP
		t.Fatal(err)
	}
	if err := c.NeighborDelete(ctx, "jo0gb", netip.MustParseAddr("10.20.0.12")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ndp -d 2001:db8:2::100", "ndp -s 2001:db8:2::100 " + mac,
		"ndp -d fe80::c864:60ff:fed0:bb57%jo0gb", "ndp -s fe80::c864:60ff:fed0:bb57%jo0gb " + mac,
		"ndp -d fe80::c864:60ff:fed0:bb57%jo0gb",
		"arp -d 10.20.0.12", "arp -s 10.20.0.12 " + mac, "arp -d 10.20.0.12",
	}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("got  %q\nwant %q", f.Argv(), want)
	}
	for _, bad := range []func() error{
		func() error { return c.NeighborSet(ctx, "em0", netip.MustParseAddr("2001:db8::1"), mac) },          // not ours
		func() error { return c.NeighborSet(ctx, "jo0gb", netip.MustParseAddr("2001:db8::1"), "nonsense") }, // not a MAC
		func() error { return c.NeighborSet(ctx, "jo0gb", netip.MustParseAddr("ff02::1"), mac) },            // multicast
	} {
		if bad() == nil {
			t.Error("accepted")
		}
	}
	if len(f.Argv()) != len(want) {
		t.Error("a refused entry ran a command")
	}
}
