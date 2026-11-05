package validate

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

func TestABridgeNameMustLeaveRoomForTheGatewayInterface(t *testing.T) {
	if err := NetworkSpec(api.NetworkSpec{Bridge: "palminnbridge", IPv4: &api.IPv4Net{Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", Range: "10.1.0.2-10.1.0.9"}}); err != nil { // 13 characters
		t.Fatal(err)
	}
	if err := NetworkSpec(api.NetworkSpec{Bridge: "palminnbridge1", IPv4: &api.IPv4Net{Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", Range: "10.1.0.2-10.1.0.9"}}); err == nil { // 14: the gateway ends would not fit
		t.Fatal("a bridge name that leaves no room for the gateway interface was accepted")
	}
}

// A bridge may have any name an interface can have, but not a driver's, and not one whose gateway pair looks like a
// jail's epair.
func TestBridgeNames(t *testing.T) {
	for name, want := range map[string]string{
		"palminn": "", "jo0": "", "jo42": "", "lan": "", "dmz2": "", "bridgeway": "", "vmxnet": "", "lonet": "", "em1x": "",
		"vmx0": "driver \"vmx\"", "em1": "driver \"em\"", "lo0": "driver \"lo\"", "bridge0": "driver \"bridge\"", "bridge": "driver \"bridge\"",
		"epair0a": "driver \"epair\"", "tap3": "driver \"tap\"", "wg0": "driver \"wg\"", "pflog0": "driver \"pflog\"",
		"igb12": "driver \"igb\"", "all": "driver \"all\"", "VMX0": "not a valid interface name", "dmz_2": "not a valid interface name",
		"jo1234567890a":  "form of a jail's interface", // jo1234567890aga is an epair end's shape
		"m":              `gateway interface mgb, which has the form of the interfaces of the driver "mgb"`,
		"palminnbridge1": "longer than 13", "1net": "not a valid interface name", "pal-minn": "not a valid interface name", "": "",
	} {
		err := NetworkSpec(api.NetworkSpec{Bridge: name, IPv4: &api.IPv4Net{Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", Range: "10.1.0.2-10.1.0.9"}})
		if (err == nil) != (want == "") || err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (want %q)", name, err, want)
		}
	}
}

func proxySpec(subnet string) api.NetworkSpec {
	return api.NetworkSpec{Bridge: "jo5", Uplink: "vtnet0", IPv6: &api.IPv6Net{Mode: api.IPv6Proxy, Subnet: subnet, Gateway: "fe80::1",
		Range: "2001:db8:1:2:100::10-2001:db8:1:2:100::20"}}
}

// What the proxy cannot serve is refused when the network is created: the helper approves /48 to /120.
func TestProxySubnetsMustBeServableByTheHelper(t *testing.T) {
	if err := NetworkSpec(proxySpec("2001:db8:1:2:100::/80")); err != nil {
		t.Fatal(err)
	}
	small := proxySpec("2001:db8:1:2:100::/126") // inside an approved /120
	small.IPv6.Range = "2001:db8:1:2:100::2-2001:db8:1:2:100::3"
	if err := NetworkSpec(small); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"2001:db8::/32", "2001:db8::/47"} {
		s := proxySpec(p)
		s.IPv6.Range = "2001:db8::10-2001:db8::20"
		err := NetworkSpec(s)
		if err == nil || !strings.Contains(err.Error(), "/48") || !strings.Contains(err.Error(), "priv.conf") {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := NetworkSpec(proxySpec("2001:db8:1:2:100::/127")); err == nil {
		t.Error("a /127 was accepted")
	}
}

func TestRoutedSubnetsAreBetween32And126(t *testing.T) {
	r := func(subnet, rng string) api.NetworkSpec {
		return api.NetworkSpec{Bridge: "jo5", IPv6: &api.IPv6Net{Mode: api.IPv6Routed, Subnet: subnet, Gateway: strings.TrimSuffix(subnet, "/32") + "1", Range: rng}}
	}
	if err := NetworkSpec(r("2001:db8::/32", "2001:db8::10-2001:db8::20")); err != nil {
		t.Fatal(err)
	}
	s := r("2000::/16", "2000::10-2000::20")
	s.IPv6.Gateway = "2000::1"
	if err := NetworkSpec(s); err == nil {
		t.Fatal("a routed /16 was accepted")
	}
}

// Every bridge name the service accepts is one the helper creates, with its gateway pair: valid interface names of at
// most 15 characters, none of a driver's form.
func FuzzBridgeName(f *testing.F) {
	for _, s := range []string{"palminn", "jo0", "lan", "vmx0", "bridge", "abcdefghijklm", "jo1234567890a", "epair0", "vlan0.10", "x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b string) {
		if BridgeName(b) != nil {
			return
		}
		for _, n := range []string{b, b + "ga", b + "gb"} {
			if err := ifconfig.CheckNewName(n); err != nil {
				t.Fatalf("bridge %q is accepted but the helper refuses %q: %v", b, n, err)
			}
		}
	})
}
