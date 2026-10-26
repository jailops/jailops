package validate

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestABridgeNameMustLeaveRoomForTheGatewayInterface(t *testing.T) {
	if err := NetworkSpec(api.NetworkSpec{Bridge: "jo1234567890a", IPv4: &api.IPv4Net{Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", Range: "10.1.0.2-10.1.0.9"}}); err != nil { // 13 characters
		t.Fatal(err)
	}
	if err := NetworkSpec(api.NetworkSpec{Bridge: "jo1234567890ab", IPv4: &api.IPv4Net{Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", Range: "10.1.0.2-10.1.0.9"}}); err == nil { // 14: the gateway ends would not fit
		t.Fatal("a bridge name that leaves no room for the gateway interface was accepted")
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
