package ipam

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestProxyAddrs6(t *testing.T) {
	v6 := &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80", Range: "2001:db8:1:2:100::10-2001:db8:1:2:100::ff"}
	got, more, err := ProxyAddrs6(v6)
	if err != nil || more || len(got) != 240 || got[0].String() != "2001:db8:1:2:100::10" || got[239].String() != "2001:db8:1:2:100::ff" {
		t.Fatalf("%d addresses, more %v, err %v", len(got), more, err)
	}
	v6.Exclude = []string{"2001:db8:1:2:100::20-2001:db8:1:2:100::2f"}
	got, _, _ = ProxyAddrs6(v6)
	if len(got) != 224 {
		t.Fatalf("an excluded span stays out: %d addresses", len(got))
	}
	// the resolver address is the second address of the subnet: a range that reaches it leaves it out
	v6 = &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80", Range: "2001:db8:1:2:100::1-2001:db8:1:2:100::4"}
	got, _, _ = ProxyAddrs6(v6)
	for _, a := range got {
		if a == netip.MustParseAddr("2001:db8:1:2:100::1") {
			t.Fatal("the resolver address is answered for")
		}
	}
	if len(got) != 3 {
		t.Fatalf("%v", got)
	}
}

func TestProxyAddrs6StopsAtTheLimit(t *testing.T) {
	v6 := &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80", Range: "2001:db8:1:2:100::10-2001:db8:1:2:100::ffff"}
	got, more, err := ProxyAddrs6(v6)
	if err != nil || !more || len(got) != AnswerLimit6 {
		t.Fatalf("%d addresses, more %v, err %v", len(got), more, err)
	}
}

func TestInProxyRange6(t *testing.T) {
	v6 := &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80", Range: "2001:db8:1:2:100::10-2001:db8:1:2:100::ffff", Exclude: []string{"2001:db8:1:2:100::40"}}
	for a, want := range map[string]bool{
		"2001:db8:1:2:100::10":   true,
		"2001:db8:1:2:100::1000": false, // beyond the answer limit: the range does not answer for it, the jail that holds it does
		"2001:db8:1:2:100::40":   false,
		"2001:db8:1:2:100::1":    false, // the resolver address
		"2001:db8:1:2:100::f":    false, // below the range
		"2001:db8:1:2:200::10":   false,
	} {
		if got := InProxyRange6(v6, netip.MustParseAddr(a)); got != want {
			t.Errorf("%s: %v, want %v", a, got, want)
		}
	}
}

// A range of a /80 with an exclusion of a /96 is 2^32 addresses wide: the listing jumps over the span instead of
// stepping through it, so the reconcile loop (which holds a lock while it lists) and the status endpoint finish at once.
func TestProxyAddrs6JumpsOverALargeExclusion(t *testing.T) {
	v6 := &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80",
		Range:   "2001:db8:1:2:100::1-2001:db8:1:2:100:ffff:ffff:ffff",
		Exclude: []string{"2001:db8:1:2:100::2-2001:db8:1:2:100:0:ffff:ffff"}}
	done := make(chan struct{})
	var got []netip.Addr
	go func() {
		got, _, _ = ProxyAddrs6(v6)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ProxyAddrs6 steps through the excluded span")
	}
	// ::1 is the resolver address, ::2 to :0:ffff:ffff is excluded: the first address is the one after the span
	if len(got) != AnswerLimit6 || got[0].String() != "2001:db8:1:2:100:1::" {
		t.Fatalf("%d addresses, the first is %v", len(got), got[:min(len(got), 1)])
	}
}

// The base address of the prefix (the subnet-router anycast address) is never listed, and an address beyond the limit
// is not in the range the helper answers for.
func TestProxyRangeLeavesOutTheBaseAndStopsAtTheLimit(t *testing.T) {
	v6 := &api.IPv6Net{NDPProxy: true, Subnet: "2001:db8:1:2:100::/80", Range: "2001:db8:1:2:100::-2001:db8:1:2:100::ffff"}
	got, more, err := ProxyAddrs6(v6)
	if err != nil || !more || len(got) != AnswerLimit6 {
		t.Fatalf("%d addresses, more %v, err %v", len(got), more, err)
	}
	if base := netip.MustParseAddr("2001:db8:1:2:100::"); InProxyRange6(v6, base) || slices.Contains(got, base) {
		t.Error("the base address of the prefix is answered for")
	}
	if !InProxyRange6(v6, netip.MustParseAddr("2001:db8:1:2:100::10")) {
		t.Error("an address of the range is not")
	}
	if InProxyRange6(v6, netip.MustParseAddr("2001:db8:1:2:100::fff0")) {
		t.Error("an address beyond the limit is in the range")
	}
}
