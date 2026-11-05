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

// show is what `ifconfig <name>` prints for an interface of kind ("bridge", "epair", "vmx", "lo") with desc.
func show(name, kind, desc string) string {
	out := name + ": flags=1008843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST,LOWER_UP> metric 0 mtu 1500\n"
	if desc != "" {
		out += "\tdescription: " + desc + "\n"
	}
	return out + "\tether 58:9c:fc:10:ff:01\n\tgroups: " + kind + "\n"
}

// mine answers `ifconfig <name>` with an interface of JailOps (the marker) for every name.
func mine(f *run.Fake, kind string, names ...string) {
	for _, n := range names {
		f.On("ifconfig", []string{n}, show(n, kind, Marker))
	}
}

// writes returns the calls that are not reads of one interface (ifconfig NAME, ifconfig -j JAIL NAME).
func writes(argv []string) []string {
	var out []string
	for _, a := range argv {
		f := strings.Fields(a)
		if len(f) == 4 && f[1] == "-j" {
			continue
		}
		if len(f) > 2 || a == "ifconfig -a" {
			out = append(out, a)
		}
	}
	return out
}

func TestCommandBridgeAndEpair(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"bridge", "create"}, "bridge0\n")
	mine(f, "bridge", "jo0", "palminn")
	mine(f, "epair", "joweba", "jodbea")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	if err := c.BridgeCreate(ctx, "palminn"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDescription(ctx, "jo0", "jailops:abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDescription(ctx, "jo0", ""); err != nil { // clearing keeps the marker
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
	if err := c.BridgeDestroy(ctx, "palminn"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ifconfig bridge create",
		"ifconfig bridge0 description " + Marker, // marked before it has its name
		"ifconfig bridge0 name palminn",
		"ifconfig jo0", "ifconfig jo0 description jailops:abc",
		"ifconfig jo0", "ifconfig jo0 description " + Marker,
		"ifconfig jo0", "ifconfig joweba", "ifconfig jo0 addm joweba private joweba",
		"ifconfig jo0", "ifconfig jodbea", "ifconfig jo0 addm jodbea",
		"ifconfig jo0", "ifconfig jo0 deletem joweba",
		"ifconfig jo0", "ifconfig jo0 inet 10.77.0.1/24 alias",
		"ifconfig jo0", "ifconfig jo0 inet6 fd00::1 prefixlen 64 -alias",
		"ifconfig jo0", "ifconfig jo0 up",
		"ifconfig jo0", "ifconfig jo0 down",
		"ifconfig palminn", "ifconfig palminn destroy",
	}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("got  %q\nwant %q", f.Argv(), want)
	}
}

func TestCommandEpair(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"epair", "create"}, "epair3a\n")
	mine(f, "epair", "joweba")
	f.On("ifconfig", []string{"-j", "web", "joweba"}, show("joweba", "epair", "jailops jail=p/web net=lan (jail side)"))
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	a, b, err := c.EpairCreate(ctx, "joweb")
	if err != nil || a != "joweba" || b != "jowebb" {
		t.Fatal(a, b, err)
	}
	if err := c.MoveToJail(ctx, a, "web"); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveFromJail(ctx, a, "web"); err != nil {
		t.Fatal(err)
	}
	if err := c.EpairDestroy(ctx, a); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ifconfig epair create", "ifconfig epair3a description " + Marker, "ifconfig epair3b description " + Marker,
		"ifconfig epair3b name jowebb", "ifconfig epair3a name joweba",
		"ifconfig joweba", "ifconfig joweba vnet web",
		"ifconfig -j web joweba", "ifconfig joweba -vnet web",
		"ifconfig joweba", "ifconfig joweba destroy",
	}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("%q", f.Argv())
	}
}

func TestEpairRenameFailureCleansUp(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"epair", "create"}, "epair3a\n")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"epair3a", "description", Marker}})
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"epair3b", "description", Marker}})
	f.OnFail("ifconfig", []string{"epair3b", "name", "jowebb"}, 1, "ifconfig: SIOCSIFNAME: File exists")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"epair3a", "destroy"}})
	if _, _, err := c.EpairCreate(ctx, "joweb"); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatal(err)
	}
	if last := f.Argv()[len(f.Argv())-1]; last != "ifconfig epair3a destroy" {
		t.Errorf("no cleanup: %q", f.Argv())
	}
}

// A bridge whose name is taken is never adopted: the kernel refuses the rename and the new bridge goes again.
func TestBridgeCreateNeverAdopts(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"bridge", "create"}, "bridge4\n")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"bridge4", "description", Marker}})
	f.OnFail("ifconfig", []string{"bridge4", "name", "mybr"}, 1, "ifconfig: ioctl SIOCSIFNAME (set name): File exists")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"bridge4", "destroy"}})
	if err := c.BridgeCreate(ctx, "mybr"); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatal(err)
	}
	want := []string{"ifconfig bridge create", "ifconfig bridge4 description " + Marker, "ifconfig bridge4 name mybr", "ifconfig bridge4 destroy"}
	if !reflect.DeepEqual(f.Argv(), want) {
		t.Errorf("%q", f.Argv())
	}
	// a failed description destroys the bridge before it ever has the name
	c, f = newCmd()
	f.On("ifconfig", []string{"bridge", "create"}, "bridge4\n")
	f.OnFail("ifconfig", []string{"bridge4", "description", Marker}, 1, "ifconfig: boom")
	f.Add(run.Rule{Name: "ifconfig", Args: []string{"bridge4", "destroy"}})
	if err := c.BridgeCreate(ctx, "mybr"); err == nil {
		t.Fatal("accepted")
	}
	if got := f.Argv(); got[len(got)-1] != "ifconfig bridge4 destroy" || strings.Contains(strings.Join(got, "|"), " name ") {
		t.Errorf("%q", got)
	}
}

// Ownership is the kernel's description and kind, never the name: every refusal, and what is accepted.
func TestOwnership(t *testing.T) {
	type cas struct {
		name, kind, desc string
		want             Ownership
		msg              string
	}
	for _, k := range []cas{
		{"palminn", "bridge", Marker, OwnedByMarker, ""},
		{"palminn", "bridge", "jailops net=palminn bridge", OwnedByMarker, ""},
		{"jo0", "bridge", "jailops:0b0e7f3e", OwnedByMarker, ""},
		{"palmingb", "epair", "jailops net=palminn gateway (host side)", OwnedByMarker, ""},
		{"jo7", "bridge", "", OwnedLegacy, ""},                                             // an older release, or a crash before the description
		{"jo1234567890aab", "epair", "", OwnedLegacy, ""},                                  // an epair of an older release
		{"mybr", "bridge", "", NotOwned, "has no JailOps description"},                     // a bridge made by hand
		{"bridge0", "bridge", "", NotOwned, "has no JailOps description"},                  // the kernel's name
		{"jo7", "bridge", "customer uplink bridge", NotOwned, "is not one JailOps writes"}, // a legacy name with someone's description
		{"mybr", "bridge", "jailopsx", NotOwned, "is not one JailOps writes"},
		{"mybr", "bridge", "Jailops managed", NotOwned, "is not one JailOps writes"},
		{"vmx0", "vmx", "", NotOwned, "only bridges and epairs"},
		{"vmx0", "vmx", Marker, NotOwned, "only bridges and epairs"}, // a forged description on the uplink
		{"jo7", "vmx", "", NotOwned, "only bridges and epairs"},      // a renamed uplink with a legacy name
		{"lo0", "lo", Marker, NotOwned, "only bridges and epairs"},
		{"wg0", "wg", "jailops wireguard=office", NotOwned, "only bridges and epairs"},
		{"tap0", "tap", Marker, NotOwned, "only bridges and epairs"},
	} {
		i := ParseIfconfig(show(k.name, k.kind, k.desc))[0]
		got, err := Options{}.Owns(i)
		if got != k.want || (err == nil) != (k.msg == "") || err != nil && (!strings.Contains(err.Error(), k.msg) || !api.IsCode(err, api.CodeForbidden)) {
			t.Errorf("%s/%s/%q: %v %v", k.name, k.kind, k.desc, got, err)
		}
	}
	// the legacy prefixes are priv.conf's
	if got, _ := (Options{Prefixes: []string{"old"}}).Owns(Iface{Name: "jo7", Groups: []string{"bridge"}}); got != NotOwned {
		t.Error("jo is legacy only by default")
	}
	if got, _ := (Options{Prefixes: []string{"old"}}).Owns(Iface{Name: "old3", Groups: []string{"bridge"}}); got != OwnedLegacy {
		t.Error("custom legacy prefix")
	}
	if got, _ := (Options{}).Owns(Iface{Name: "jo", Groups: []string{"bridge"}}); got != NotOwned {
		t.Error("the prefix alone is not a legacy name")
	}
}

// Every mutating call reads the interface from the kernel first and touches nothing that is not JailOps's.
func TestCommandRefusesForeign(t *testing.T) {
	c, f := newCmd()
	f.On("ifconfig", []string{"vmx0"}, show("vmx0", "vmx", ""))
	f.On("ifconfig", []string{"lo0"}, show("lo0", "lo", ""))
	f.On("ifconfig", []string{"mybr"}, show("mybr", "bridge", "")) // ifconfig bridge create; ifconfig bridge0 name mybr
	f.On("ifconfig", []string{"forged"}, show("forged", "bridge", "my jailops bridge"))
	f.On("ifconfig", []string{"jo0"}, show("jo0", "bridge", Marker))
	f.On("ifconfig", []string{"-j", "web", "lo0"}, show("lo0", "lo", ""))
	f.OnFail("ifconfig", []string{"ghost"}, 1, "ifconfig: interface ghost does not exist")
	for _, name := range []string{"vmx0", "lo0", "mybr", "forged"} {
		for i, err := range []error{
			c.BridgeDestroy(ctx, name),
			c.BridgeAddMember(ctx, name, "jo0", false),
			c.BridgeAddMember(ctx, "jo0", name, false), // the uplink is never made a port
			c.BridgeDeleteMember(ctx, name, "jo0"),
			c.SetDescription(ctx, name, Marker), // the daemon cannot have the marker written on a foreign interface
			c.SetLink(ctx, name, false),
			c.AddrAdd(ctx, name, netip.MustParsePrefix("10.0.0.1/24")),
			c.AddrRemove(ctx, name, netip.MustParsePrefix("10.0.0.1/24")),
			c.EpairDestroy(ctx, name),
			c.MoveToJail(ctx, name, "web"),
			c.RouteAdd(ctx, Route{Dest: "2001:db8::5/128", Interface: name}),
			c.NeighborSet(ctx, name, netip.MustParseAddr("2001:db8::5"), "02:00:00:00:00:01"),
			c.NeighborDelete(ctx, name, netip.MustParseAddr("2001:db8::5")),
		} {
			if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "not managed by JailOps") {
				t.Errorf("%s case %d: %v", name, i, err)
			}
		}
	}
	if !api.IsCode(c.MoveFromJail(ctx, "lo0", "web"), api.CodeForbidden) {
		t.Error("the loopback of a jail was moved")
	}
	if !api.IsCode(c.SetLink(ctx, "ghost", true), api.CodeNotFound) {
		t.Error("a missing interface is not_found")
	}
	if w := writes(f.Argv()); len(w) != 0 {
		t.Errorf("foreign interface touched: %q", w)
	}
	// a create under a driver's name runs nothing at all
	f.Reset()
	for _, n := range []string{"vmx0", "em0", "lo0", "bridge0", "bridge", "tap0", "wg0", "pflog0", "vlan0.10", "all"} {
		if err := c.BridgeCreate(ctx, n); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "form of the interfaces of the driver") {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, _, err := c.EpairCreate(ctx, "epair0"); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("epair0a: %v", err)
	}
	if err := c.SetDescription(ctx, "jo0", "my text"); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("the marker was removable: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("ran %q", f.Argv())
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
	mine(f, "bridge", "jo0")
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
		"ifconfig jo0",
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
	mine(f, "bridge", "jo0")
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
		"ifconfig jo0",
		"route -n delete -inet6 2001:db8:1:2:100::10/128 -interface jo0",
		"ndp -d 2001:db8:1:2:100::10",
		"ifconfig jo0",
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
	mine(f, "epair", "jo0gb")
	f.On("ifconfig", []string{"em0"}, show("em0", "em", ""))
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
	if got := writes(f.Argv()); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
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
	if len(writes(f.Argv())) != len(want) {
		t.Error("a refused entry ran a command")
	}
}

// The fake stores descriptions and reads them back as the kernel does, with the same ownership rule as Command.
func TestFakeOwnership(t *testing.T) {
	f := NewFake(Options{})
	f.AddForeign("vmx0", true)
	f.AddIface(Iface{Name: "mybr", Groups: []string{"bridge"}})
	f.AddIface(Iface{Name: "jo7", Groups: []string{"bridge"}})
	if err := f.BridgeCreate(ctx, "palminn"); err != nil {
		t.Fatal(err)
	}
	if i, _ := f.Show(ctx, "palminn"); i.Description != Marker {
		t.Fatalf("created without the marker: %+v", i)
	}
	if err := f.SetDescription(ctx, "palminn", "jailops net=palminn bridge"); err != nil {
		t.Fatal(err)
	}
	if i, _ := f.Show(ctx, "palminn"); i.Description != "jailops net=palminn bridge" {
		t.Fatalf("%+v", i)
	}
	for _, err := range []error{f.SetLink(ctx, "vmx0", false), f.SetLink(ctx, "mybr", true), f.SetDescription(ctx, "mybr", Marker),
		f.BridgeAddMember(ctx, "palminn", "vmx0", false), f.BridgeAddMember(ctx, "palminn", "mybr", false)} {
		if !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("foreign: %v", err)
		}
	}
	if err := f.BridgeCreate(ctx, "mybr"); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Errorf("a taken name: %v", err) // the helper's guard turns this into forbidden for what is not JailOps's
	}
	if err := f.BridgeCreate(ctx, "bridge7"); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("a driver's name: %v", err)
	}
	// legacy: accepted (marking it is the helper's)
	if err := f.SetLink(ctx, "jo7", true); err != nil {
		t.Errorf("legacy: %v", err)
	}
	// the marker travels into the jail's vnet and back
	_, b, err := f.EpairCreate(ctx, "jox")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.MoveToJail(ctx, b, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Show(ctx, b); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("a jail's interface is visible on the host: %v", err)
	}
	if i, err := f.ShowInJail(ctx, "web", b); err != nil || i.Description != Marker {
		t.Errorf("%+v %v", i, err)
	}
	f.AddToJail("web", Iface{Name: "mine", Groups: []string{"epair"}, Description: "the jail's"})
	if err := f.MoveFromJail(ctx, "mine", "web"); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an interface the jail made: %v", err)
	}
	if err := f.MoveFromJail(ctx, b, "web"); err != nil {
		t.Fatal(err)
	}
}
