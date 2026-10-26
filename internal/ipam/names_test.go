package ipam

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestGatewayNames(t *testing.T) {
	m, l3, err := GatewayNames("jo21")
	if err != nil || m != "jo21ga" || l3 != "jo21gb" || GatewayIface("jo21") != "jo21gb" {
		t.Fatalf("%q %q %v", m, l3, err)
	}
	long := strings.Repeat("b", MaxBridgeName)
	if _, l3, err := GatewayNames(long); err != nil || len(l3) != 15 {
		t.Fatalf("the longest bridge name must give a 15 character interface: %q %v", l3, err)
	}
	if _, _, err := GatewayNames(long + "x"); err == nil || GatewayIface(long+"x") != "" {
		t.Fatal("a bridge name that is too long was accepted")
	}
	if _, _, err := GatewayNames(""); err == nil {
		t.Fatal("empty")
	}
}

func TestLinkLocalFromMAC(t *testing.T) {
	got, err := LinkLocalFromMAC("2a:58:8a:88:fc:d2")
	if err != nil || got.String() != "fe80::2858:8aff:fe88:fcd2" { // observed on FreeBSD 15.1
		t.Fatal(got, err)
	}
	if _, err := LinkLocalFromMAC("nonsense"); err == nil {
		t.Fatal("accepted")
	}
}

// In both modes a jail holds a /128 and reaches everything through the host's link-local gateway.
func TestGuestAddressingIsTheSameInBothModes(t *testing.T) {
	for _, m := range []api.IPv6Mode{api.IPv6Routed, api.IPv6Proxy} {
		if GuestPrefixLen6(m, 80) != 128 {
			t.Errorf("%s: jail prefix is not /128", m)
		}
	}
	if g := JailGateway6(&api.IPv6Net{Mode: api.IPv6Routed, Gateway: "2001:db8::1"}); g.String() != "fe80::1" {
		t.Errorf("routed gateway %s", g)
	}
	if g := JailGateway6(&api.IPv6Net{Mode: api.IPv6Proxy, Gateway: "fe80::5"}); g.String() != "fe80::5" {
		t.Errorf("proxy gateway %s", g)
	}
}
