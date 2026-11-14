package validate

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func proxySpec(subnet string) api.NetworkSpec {
	return api.NetworkSpec{Uplink: "vtnet0", IPv6: &api.IPv6Net{Mode: api.IPv6Proxy, Subnet: subnet,
		Range: "2001:db8:1:2:100::10-2001:db8:1:2:100::20"}}
}

// What the proxy cannot serve is refused when the network is created: the helper accepts /48 to /120.
func TestProxySubnetsMustBeServableByTheHelper(t *testing.T) {
	if err := NetworkSpec(proxySpec("2001:db8:1:2:100::/80")); err != nil {
		t.Fatal(err)
	}
	small := proxySpec("2001:db8:1:2:100::/120")
	small.IPv6.Range = "2001:db8:1:2:100::2-2001:db8:1:2:100::20"
	if err := NetworkSpec(small); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"2001:db8::/32", "2001:db8::/47"} {
		s := proxySpec(p)
		s.IPv6.Range = "2001:db8::10-2001:db8::20"
		err := NetworkSpec(s)
		if err == nil || !strings.Contains(err.Error(), "48") {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := NetworkSpec(proxySpec("2001:db8:1:2:100::/121")); err == nil {
		t.Error("a /121 was accepted")
	}
}

func TestRoutedSubnetsAreBetween32And126(t *testing.T) {
	r := func(subnet, rng string) api.NetworkSpec {
		return api.NetworkSpec{IPv6: &api.IPv6Net{Mode: api.IPv6Routed, Subnet: subnet, Range: rng}}
	}
	if err := NetworkSpec(r("2001:db8::/32", "2001:db8::10-2001:db8::20")); err != nil {
		t.Fatal(err)
	}
	s := r("2000::/16", "2000::10-2000::20")
	if err := NetworkSpec(s); err == nil {
		t.Fatal("a routed /16 was accepted")
	}
}
