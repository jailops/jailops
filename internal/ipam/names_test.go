package ipam

import (
	"testing"
)

func TestLinkLocalFromMAC(t *testing.T) {
	got, err := LinkLocalFromMAC("2a:58:8a:88:fc:d2")
	if err != nil || got.String() != "fe80::2858:8aff:fe88:fcd2" { // observed on FreeBSD 15.1
		t.Fatal(got, err)
	}
	if _, err := LinkLocalFromMAC("nonsense"); err == nil {
		t.Fatal("accepted")
	}
}

// A jail holds a /128 and reaches everything through the host's link-local gateway.
func TestGuestAddressingIsOneHostPrefix(t *testing.T) {
	if GuestPrefixLen6() != 128 {
		t.Error("jail prefix is not /128")
	}
	if GatewayLinkLocal6.String() != "fe80::1" {
		t.Errorf("gateway %s", GatewayLinkLocal6)
	}
}
