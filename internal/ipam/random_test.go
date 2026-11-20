package ipam

import (
	"net/netip"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// the range is the one `network create` gives a prefix: the first 65534 addresses
func v6net(assign string) *api.IPv6Net {
	return &api.IPv6Net{Subnet: "2001:db8:77:1:2::/80", Range: "2001:db8:77:1:2::1-2001:db8:77:1:2::fffe", Assignment: assign}
}

// A random network gives each jail a free address anywhere in its prefix, and never an address twice.
func TestRandomAssignmentDrawsFreeAddressesOfTheRange(t *testing.T) {
	al, err := NewV6(v6net(api.AssignRandom), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[netip.Addr]bool{}
	low := 0
	for i := 0; i < 200; i++ {
		a, err := al.Allocate(netip.Addr{}, "jail")
		if err != nil {
			t.Fatal(err)
		}
		if seen[a] {
			t.Fatalf("%s given twice", a)
		}
		seen[a] = true
		if !al.prefix.Contains(a) {
			t.Fatalf("%s is outside the prefix", a)
		}
		if al.reserved[a] {
			t.Fatalf("%s is reserved", a)
		}
		if al.rng.Contains(a) {
			low++ // the default range is 65534 of the 2^48 addresses of the /80: a draw lands there once in 4 billion
		}
	}
	if low > 2 {
		t.Fatalf("%d of 200 addresses are inside the small default range: they are not drawn over the whole /80", low)
	}
}

// The default stays the lowest free address, and a preferred address still wins.
func TestSequentialAssignmentIsTheDefault(t *testing.T) {
	for _, assign := range []string{"", api.AssignSequential} {
		al, err := NewV6(v6net(assign), nil)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := al.Allocate(netip.Addr{}, "x")
		b, _ := al.Allocate(netip.Addr{}, "y")
		if a.String() != "2001:db8:77:1:2::2" || b.String() != "2001:db8:77:1:2::3" {
			t.Fatalf("assignment %q gave %s and %s, want ::2 and ::3", assign, a, b)
		}
	}
	al, _ := NewV6(v6net(api.AssignRandom), nil)
	want := netip.MustParseAddr("2001:db8:77:1:2::99")
	if got, err := al.Allocate(want, "x"); err != nil || got != want {
		t.Fatalf("a preferred address wins on a random network: %v %v", got, err)
	}
}

// A prefix with a few addresses is filled up, each address once, and then refused as exhausted: the draws that miss are taken over
// by the walk of the range.
func TestRandomAssignmentOnASmallPrefix(t *testing.T) {
	n := &api.IPv6Net{Subnet: "2001:db8:77:1:2::/124", Range: "2001:db8:77:1:2::1-2001:db8:77:1:2::e", Assignment: api.AssignRandom}
	al, err := NewV6(n, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[netip.Addr]bool{}
	for {
		a, err := al.Allocate(netip.Addr{}, "j")
		if err != nil {
			if !api.IsCode(err, api.CodePoolExhausted) {
				t.Fatalf("the end is exhaustion, got %v", err)
			}
			break
		}
		if seen[a] {
			t.Fatalf("%s given twice", a)
		}
		seen[a] = true
	}
	if len(seen) < 10 || len(seen) > 14 {
		t.Fatalf("a /124 holds about 13 jails, got %d", len(seen))
	}
}

// A jail can be given an address of its own whatever the network does, and an address that is taken is refused.
func TestAStaticAddressOnARandomNetworkIsCheckedAsEverywhere(t *testing.T) {
	al, _ := NewV6(v6net(api.AssignRandom), nil)
	want := netip.MustParseAddr("2001:db8:77:1:2:abcd:1234:5678") // outside the default range, inside the prefix
	if got, err := al.Allocate(want, "a"); err != nil || got != want {
		t.Fatalf("an address of the prefix is given: %v %v", got, err)
	}
	if _, err := al.Allocate(want, "b"); !api.IsCode(err, api.CodeIPConflict) {
		t.Fatalf("the same address twice is refused as in use, got %v", err)
	}
	if _, err := al.Allocate(netip.MustParseAddr("2001:db8:ffff::1"), "c"); err == nil {
		t.Fatal("an address outside the prefix is refused")
	}
}
