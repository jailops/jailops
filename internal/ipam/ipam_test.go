package ipam

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func ap(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestParseRange(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		size string
	}{
		{"10.0.0.2-10.0.0.9", true, "8"},
		{"10.0.0.2", true, "1"},
		{"10.0.0.9-10.0.0.2", false, ""},
		{"10.0.0.1-fd00::1", false, ""},
		{"", false, ""},
		{"fd00::10-fd00::ffff:ffff:ffff:ffff", true, "18446744073709551600"},
		{"10.0.0.1-", false, ""},
		{"fe80::1%em0", false, ""},
	}
	for _, tc := range tests {
		r, err := ParseRange(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v", tc.in, err)
			continue
		}
		if err != nil {
			if !api.IsCode(err, api.CodeInvalid) {
				t.Errorf("%q: wrong code %v", tc.in, err)
			}
			continue
		}
		if r.Size().String() != tc.size {
			t.Errorf("%q: size %s want %s", tc.in, r.Size(), tc.size)
		}
	}
}

func v4(t *testing.T, rng string, excl []string, used ...string) *Allocator {
	t.Helper()
	var u []netip.Addr
	for _, s := range used {
		u = append(u, ap(s))
	}
	a, err := NewV4(&api.IPv4Net{Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Range: rng, Exclude: excl}, u)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAllocateLowestFirst(t *testing.T) {
	a := v4(t, "10.0.0.1-10.0.0.10", []string{"10.0.0.3-10.0.0.4", "10.0.0.6"}, "10.0.0.2")
	var got []string
	for {
		x, err := a.Allocate(netip.Addr{}, "j")
		if err != nil {
			if !api.IsCode(err, api.CodePoolExhausted) {
				t.Fatalf("code: %v", err)
			}
			break
		}
		got = append(got, x.String())
	}
	want := "10.0.0.5 10.0.0.7 10.0.0.8 10.0.0.9 10.0.0.10"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v", got)
	}
}

func TestNeverAllocateSpecial(t *testing.T) {
	a := v4(t, "10.0.0.0-10.0.0.255", nil)
	first, _ := a.Allocate(netip.Addr{}, "")
	if first != ap("10.0.0.2") {
		t.Fatalf("first=%s", first)
	}
	for _, s := range []string{"10.0.0.0", "10.0.0.1", "10.0.0.255"} {
		_, err := a.Allocate(ap(s), "")
		if !api.IsCode(err, api.CodeIPConflict) {
			t.Errorf("%s: %v", s, err)
		}
	}
	// exhaust: 253 usable (.2-.254)
	n := 1
	for {
		x, err := a.Allocate(netip.Addr{}, "")
		if err != nil {
			break
		}
		if x == ap("10.0.0.255") || x == ap("10.0.0.0") || x == ap("10.0.0.1") {
			t.Fatalf("allocated %s", x)
		}
		n++
	}
	if n != 253 {
		t.Fatalf("n=%d", n)
	}
}

func TestPreferred(t *testing.T) {
	a := v4(t, "10.0.0.10-10.0.0.20", []string{"10.0.0.15"}, "10.0.0.12")
	if x, err := a.Allocate(ap("10.0.0.50"), "a"); err != nil || x != ap("10.0.0.50") {
		t.Fatalf("fixed outside range: %v %v", x, err)
	}
	_, err := a.Allocate(ap("10.0.0.50"), "b")
	var e *api.Error
	if !api.IsCode(err, api.CodeIPConflict) {
		t.Fatal(err)
	}
	e = err.(*api.Error)
	if e.Detail["address"] != "10.0.0.50" || e.Detail["owner"] != "a" {
		t.Fatalf("detail %v", e.Detail)
	}
	for _, s := range []string{"10.0.0.12", "10.0.0.15"} {
		if _, err := a.Allocate(ap(s), ""); !api.IsCode(err, api.CodeIPConflict) {
			t.Errorf("%s: %v", s, err)
		}
	}
	if _, err := a.Allocate(ap("192.168.0.1"), ""); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("outside subnet: %v", err)
	}
	a.Release(ap("10.0.0.50"))
	if _, err := a.Allocate(ap("10.0.0.50"), ""); err != nil {
		t.Errorf("after release: %v", err)
	}
}

func TestCounts(t *testing.T) {
	a := v4(t, "10.0.0.1-10.0.0.10", []string{"10.0.0.9-10.0.0.20"}, "10.0.0.2", "10.0.0.9", "10.0.0.200")
	al, fr := a.Stats()
	// range 10 addresses, minus gateway(.1), minus excluded .9,.10 => 7 allocatable; .2 used
	if al != 1 || fr != 6 {
		t.Fatalf("allocated=%d free=%d", al, fr)
	}
}

func TestIPv6Huge(t *testing.T) {
	a, err := NewV6(&api.IPv6Net{Mode: api.IPv6Routed, Subnet: "2001:db8::/32", Gateway: "2001:db8::1", Range: "2001:db8::1-2001:db8:ffff::"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	x, err := a.Allocate(netip.Addr{}, "")
	if err != nil || x != ap("2001:db8::2") {
		t.Fatalf("%v %v", x, err)
	}
	_, fr := a.Counts()
	if fr.BitLen() < 90 {
		t.Fatalf("free %s", fr)
	}
	// saturating status counters
	_, f := a.Stats()
	if f <= 0 {
		t.Fatal(f)
	}
}

func TestIPv6LinkLocalGatewayAndNetworkAddr(t *testing.T) {
	a, err := NewV6(&api.IPv6Net{Mode: api.IPv6Proxy, Subnet: "2001:db8:1:2::/64", Gateway: "fe80::1", Range: "2001:db8:1:2::-2001:db8:1:2::10"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := a.Allocate(netip.Addr{}, "")
	if x != ap("2001:db8:1:2::1") {
		t.Fatalf("got %s (network address must be skipped)", x)
	}
}

func TestExhaustedAndSmallSubnets(t *testing.T) {
	a, err := New(netip.MustParsePrefix("10.0.0.0/30"), ap("10.0.0.1"), Range{ap("10.0.0.1"), ap("10.0.0.2")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allocate(netip.Addr{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allocate(netip.Addr{}, ""); !api.IsCode(err, api.CodePoolExhausted) {
		t.Fatal(err)
	}
	// range ending at the top of the address space must not loop
	b, _ := New(netip.MustParsePrefix("255.255.255.252/30"), netip.Addr{}, Range{ap("255.255.255.253"), ap("255.255.255.255")}, nil, nil)
	for i := 0; i < 3; i++ {
		if _, err := b.Allocate(netip.Addr{}, ""); err != nil {
			if i != 2 { // .255 is broadcast
				t.Fatal(i, err)
			}
		}
	}
	if _, err := b.Allocate(netip.Addr{}, ""); !api.IsCode(err, api.CodePoolExhausted) {
		t.Fatal(err)
	}
}

func TestRangeOutsideSubnet(t *testing.T) {
	_, err := NewV4(&api.IPv4Net{Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Range: "10.0.1.1-10.0.1.5"}, nil)
	if !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
}

func TestExclusionsCIDR(t *testing.T) {
	ex, err := ParseExclusions([]string{"10.0.0.0/30", "10.0.0.9"})
	if err != nil || len(ex) != 2 || ex[0].Last != ap("10.0.0.3") {
		t.Fatalf("%v %v", ex, err)
	}
	if _, err := ParseExclusions([]string{"junk"}); err == nil {
		t.Fatal("want error")
	}
}

func TestMAC(t *testing.T) {
	m1, err := DeriveMAC("0b0e7f3e-5c4b-4f6e-9d3e-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := DeriveMAC("0B0E7F3E-5C4B-4F6E-9D3E-111111111111")
	if m1 != m2 {
		t.Fatal("not stable/case-insensitive")
	}
	hw, err := netHW(m1)
	if err != nil || hw[0]&0x02 == 0 || hw[0]&0x01 != 0 {
		t.Fatalf("%s %v", m1, err)
	}
	h, _ := DeriveMACN("0b0e7f3e-5c4b-4f6e-9d3e-111111111111", 0, true)
	n, _ := DeriveMACN("0b0e7f3e-5c4b-4f6e-9d3e-111111111111", 1, false)
	if h == m1 || n == m1 || h == n {
		t.Fatal("collision between ends/indices")
	}
	if _, err := DeriveMAC(""); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
}

func TestEpair(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("jail-%d", i)
		for idx := 0; idx < 3; idx++ {
			h, j, err := EpairNames(id, idx)
			if err != nil {
				t.Fatal(err)
			}
			if len(h) > 15 || len(j) > 15 || !IsEpairName(h) || !IsEpairName(j) || !strings.HasPrefix(h, "jo") {
				t.Fatalf("bad names %q %q", h, j)
			}
			if seen[h] || seen[j] {
				t.Fatalf("collision %s", h)
			}
			seen[h], seen[j] = true, true
		}
	}
	if EpairName("x", 0) != EpairName("x", 0) {
		t.Fatal("not deterministic")
	}
	if EpairName("x", -1) != "" || EpairName("", 0) != "" || EpairName("x", MaxEpairIndex+1) != "" {
		t.Fatal("expected empty on invalid")
	}
	if len(EpairName("x", MaxEpairIndex)) != 15 {
		t.Fatal("max index length")
	}
	if IsEpairName("epair0a") || IsEpairName("jo0") {
		t.Fatal("false positive")
	}
	if BridgeName(3) != "jo3" {
		t.Fatal()
	}
}

func netHW(s string) ([]byte, error) {
	var b [6]byte
	_, err := fmt.Sscanf(s, "%02x:%02x:%02x:%02x:%02x:%02x", &b[0], &b[1], &b[2], &b[3], &b[4], &b[5])
	return b[:], err
}

func FuzzParseRange(f *testing.F) {
	for _, s := range []string{"10.0.0.1-10.0.0.2", "::1-::2", "a-b", "-", "1.2.3.4"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := ParseRange(s)
		if err == nil && (r.Last.Less(r.First) || r.Size().Sign() <= 0) {
			t.Fatalf("bad range %v", r)
		}
		_, _ = ParseExclusions([]string{s})
	})
}

func v4net() *api.IPv4Net {
	return &api.IPv4Net{Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Range: "10.0.0.10-10.0.0.60"}
}

func TestProjectsShareABridgeWithDisjointAddresses(t *testing.T) {
	var inUse []netip.Addr // the in-use set spans all projects
	subA, subB := "10.0.0.10-10.0.0.19", "10.0.0.20-10.0.0.29"
	got := map[netip.Addr]string{}
	for i := 0; i < 10; i++ {
		for _, p := range []struct{ name, sub, other string }{{"a", subA, subB}, {"b", subB, subA}} {
			al, err := NewV4In(v4net(), Scope{Sub: p.sub}, inUse)
			if err != nil {
				t.Fatal(err)
			}
			ad, err := al.Allocate(netip.Addr{}, p.name)
			if err != nil {
				t.Fatalf("%s %d: %v", p.name, i, err)
			}
			if o, dup := got[ad]; dup {
				t.Fatalf("%s got %s already given to %s", p.name, ad, o)
			}
			r, _ := ParseRange(p.sub)
			if !r.Contains(ad) {
				t.Fatalf("%s: %s outside %s", p.name, ad, p.sub)
			}
			got[ad] = p.name
			inUse = append(inUse, ad)
		}
	}
	al, _ := NewV4In(v4net(), Scope{Sub: subA}, inUse)
	if _, err := al.Allocate(netip.Addr{}, "a"); !api.IsCode(err, api.CodePoolExhausted) {
		t.Fatalf("a should be exhausted: %v", err)
	}
	if a, f := al.Stats(); a != 10 || f != 0 {
		t.Fatalf("stats %d %d", a, f)
	}
}

func TestScopeWithoutSubExcludesOthers(t *testing.T) {
	al, err := NewV4In(v4net(), Scope{Exclude: []string{"10.0.0.10-10.0.0.19", "10.0.0.20-10.0.0.29"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ad, err := al.Allocate(netip.Addr{}, "c")
	if err != nil || ad.String() != "10.0.0.30" {
		t.Fatalf("%v %v", ad, err)
	}
	_, err = al.Allocate(netip.MustParseAddr("10.0.0.15"), "c")
	if !api.IsCode(err, api.CodeForbidden) || err.(*api.Error).Detail["address"] != "10.0.0.15" {
		t.Fatalf("%v", err)
	}
	if _, err := al.Allocate(netip.MustParseAddr("10.0.0.40"), "c"); err != nil {
		t.Fatal(err)
	}
}

func TestFixedAddressMustBeInSub(t *testing.T) {
	al, _ := NewV4In(v4net(), Scope{Sub: "10.0.0.20-10.0.0.29"}, nil)
	for _, a := range []string{"10.0.0.19", "10.0.0.30", "10.0.0.200"} {
		if _, err := al.Allocate(netip.MustParseAddr(a), "x"); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s: %v", a, err)
		}
	}
	if _, err := al.Allocate(netip.MustParseAddr("10.0.0.25"), "x"); err != nil {
		t.Fatal(err)
	}
	// Sub is clipped to the network range.
	al, err := NewV4In(v4net(), Scope{Sub: "10.0.0.5-10.0.0.12"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ad, _ := al.Allocate(netip.Addr{}, "x"); ad.String() != "10.0.0.10" {
		t.Fatalf("%v", ad)
	}
	for _, sub := range []string{"10.0.0.70-10.0.0.80", "10.0.0.1", "fd00::1"} {
		if _, err := NewV4In(v4net(), Scope{Sub: sub}, nil); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: %v", sub, err)
		}
	}
}

func TestScopedIPv6(t *testing.T) {
	n := &api.IPv6Net{Mode: api.IPv6Routed, Subnet: "fd50::/64", Gateway: "fd50::1", Range: "fd50::10-fd50::ff"}
	a, _ := NewV6In(n, Scope{Sub: "fd50::20-fd50::2f"}, nil)
	b, _ := NewV6In(n, Scope{Sub: "fd50::30-fd50::3f"}, []netip.Addr{netip.MustParseAddr("fd50::20")})
	x, err1 := a.Allocate(netip.Addr{}, "a")
	y, err2 := b.Allocate(netip.Addr{}, "b")
	if err1 != nil || err2 != nil || x.String() != "fd50::20" || y.String() != "fd50::30" {
		t.Fatalf("%v %v %v %v", x, y, err1, err2)
	}
	if _, err := a.Allocate(netip.MustParseAddr("fd50::30"), "a"); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("%v", err)
	}
	c, _ := NewV6In(n, Scope{Exclude: []string{"fd50::20-fd50::3f"}}, nil)
	if z, _ := c.Allocate(netip.Addr{}, "c"); z.String() != "fd50::10" {
		t.Fatalf("%v", z)
	}
}

func TestParseSubAndOverlap(t *testing.T) {
	if _, err := ParseSub("10.0.0.20-10.0.0.30", "10.0.0.10-10.0.0.60"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"10.0.0.5-10.0.0.30", "10.0.0.50-10.0.0.61", "fd00::1", "x"} {
		if _, err := ParseSub(s, "10.0.0.10-10.0.0.60"); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	r := func(s string) Range { x, _ := ParseRange(s); return x }
	if !Overlap(r("10.0.0.1-10.0.0.5"), r("10.0.0.5-10.0.0.9")) || Overlap(r("10.0.0.1-10.0.0.4"), r("10.0.0.5-10.0.0.9")) || Overlap(r("10.0.0.1"), r("fd00::1")) {
		t.Fatal("overlap")
	}
}

func v6alloc(t *testing.T, subnet, gw, rng string) *Allocator {
	t.Helper()
	a, err := NewV6(&api.IPv6Net{Mode: api.IPv6Routed, Subnet: subnet, Gateway: gw, Range: rng}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The subnet-router anycast address, the all-ones end and (in a /64) the RFC 2526 anycast identifiers are never handed
// out, even when the range reaches the end of the subnet; the addresses below them are, and a released one is reused.
func TestIPv64ReservesAnycastAndTheAllOnesEnd(t *testing.T) {
	a := v6alloc(t, "2001:db8:1:2::/64", "2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ff00-2001:db8:1:2:ffff:ffff:ffff:ffff")
	got, err := a.Allocate(netip.Addr{}, "x")
	if err != nil || got != ap("2001:db8:1:2:ffff:ffff:ffff:ff00") {
		t.Fatalf("%v %v", got, err)
	}
	// the range holds 256 addresses, the top 128 of which are reserved
	for i := 1; i < 128; i++ {
		if _, err := a.Allocate(netip.Addr{}, "x"); err != nil {
			t.Fatalf("allocation %d: %v", i, err)
		}
	}
	if x, err := a.Allocate(netip.Addr{}, "x"); !api.IsCode(err, api.CodePoolExhausted) {
		t.Fatalf("handed out %v from the reserved end: %v", x, err)
	}
	for _, r := range []string{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2:ffff:ffff:ffff:ff80", "2001:db8:1:2:fdff:ffff:ffff:ffa0", "2001:db8:1:2::"} {
		if err := a.Reserve(ap(r), "y"); !api.IsCode(err, api.CodeIPConflict) {
			t.Errorf("%s: %v", r, err)
		}
	}
	if err := a.Reserve(ap("2001:db8:1:2:ffff:ffff:ffff:ff7f"), "y"); !api.IsCode(err, api.CodeIPConflict) {
		t.Errorf("already allocated: %v", err) // the last one the allocator handed out
	}
	// release and reuse
	a.Release(ap("2001:db8:1:2:ffff:ffff:ffff:ff10"))
	if got, err := a.Allocate(netip.Addr{}, "z"); err != nil || got != ap("2001:db8:1:2:ffff:ffff:ffff:ff10") {
		t.Fatalf("reuse: %v %v", got, err)
	}
	if _, free := a.Stats(); free != 0 {
		t.Errorf("free %d", free)
	}
}

// Prefixes other than /64 have no RFC 2526 range: only the all-ones end is kept back.
func TestIPv6OtherPrefixesReserveOnlyTheAllOnesEnd(t *testing.T) {
	a := v6alloc(t, "2001:db8:1:2:100::/80", "2001:db8:1:2:100::1", "2001:db8:1:2:100:ffff:ffff:fff0-2001:db8:1:2:100:ffff:ffff:ffff")
	n := 0
	for {
		got, err := a.Allocate(netip.Addr{}, "x")
		if err != nil {
			break
		}
		if got == ap("2001:db8:1:2:100:ffff:ffff:ffff") {
			t.Fatal("the all-ones address was handed out")
		}
		n++
	}
	if n != 15 {
		t.Fatalf("%d addresses, want 15 (the last of 16 is reserved)", n)
	}
	// a small subnet: the range may not end at its all-ones address
	b := v6alloc(t, "2001:db8::/126", "2001:db8::1", "2001:db8::2-2001:db8::3")
	if got, err := b.Allocate(netip.Addr{}, "x"); err != nil || got != ap("2001:db8::2") {
		t.Fatal(got, err)
	}
	if _, err := b.Allocate(netip.Addr{}, "x"); !api.IsCode(err, api.CodePoolExhausted) {
		t.Fatalf("%v", err)
	}
	// the range's own last address at the very end of the address space is not an infinite loop
	c := v6alloc(t, "ffff::/16", "ffff::1", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
	if _, err := c.Allocate(netip.Addr{}, "x"); err != nil {
		t.Logf("%v", err)
	}
}
