package ndproxy

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

var (
	macA   = MAC{2, 0, 0, 0, 0, 1}
	macB   = MAC{2, 0, 0, 0, 0, 2}
	macH   = MAC{2, 0, 0, 0, 0, 0xaa}
	jailA  = netip.MustParseAddr("2001:db8::1:2")
	router = netip.MustParseAddr("fe80::1")
)

func mkNS(src, target netip.Addr, dst netip.Addr, ethSrc MAC, slla *MAC) []byte {
	return BuildNS(ethSrc, MulticastMAC(dst), src, dst, target, slla)
}

func csumAt(f []byte) uint16 { return binary.BigEndian.Uint16(f[etherHdrLen+ipv6HdrLen+2:]) }

func TestChecksumVectors(t *testing.T) {
	// Vectors computed with an independent implementation (RFC 1071 sum
	// over the RFC 8200 section 8.1 pseudo-header).
	dst := netip.MustParseAddr("ff02::1:ff01:2")
	ns := BuildNS(macA, MulticastMAC(dst), router, dst, jailA, &macA)
	if got := csumAt(ns); got != 0x4b5d {
		t.Fatalf("NS checksum %#x", got)
	}
	na := BuildNA(macB, macA, jailA, router, jailA, FlagSolicited|FlagOverride, &macB)
	if got := csumAt(na); got != 0xb9a7 {
		t.Fatalf("NA checksum %#x", got)
	}
}

func TestParseNSMulticast(t *testing.T) {
	dst := SolicitedNode(jailA)
	if dst != netip.MustParseAddr("ff02::1:ff01:2") {
		t.Fatalf("solicited node %s", dst)
	}
	p, err := Parse(mkNS(router, jailA, dst, macA, &macA))
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != TypeNeighborSolicitation || p.Target != jailA || p.Src != router || p.SourceLLA == nil || *p.SourceLLA != macA || p.IsDAD() {
		t.Fatalf("%+v", p)
	}
	if p.EthDst != (MAC{0x33, 0x33, 0xff, 1, 0, 2}) {
		t.Fatalf("eth dst %s", p.EthDst)
	}
}

func TestParseNSUnicastAndNA(t *testing.T) {
	if _, err := Parse(mkNS(router, jailA, jailA, macA, nil)); err != nil {
		t.Fatal(err)
	}
	na := BuildNA(macB, macA, jailA, router, jailA, FlagRouter|FlagSolicited|FlagOverride, &macB)
	p, err := Parse(na)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != TypeNeighborAdvertisement || p.Flags != 0xe0 || p.TargetLLA == nil || *p.TargetLLA != macB {
		t.Fatalf("%+v", p)
	}
}

func TestParseDAD(t *testing.T) {
	un := netip.IPv6Unspecified()
	p, err := Parse(mkNS(un, jailA, SolicitedNode(jailA), macA, nil))
	if err != nil || !p.IsDAD() {
		t.Fatalf("%v %+v", err, p)
	}
	// DAD must go to the solicited-node address and carry no SLLA.
	if _, err := Parse(mkNS(un, jailA, jailA, macA, nil)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unicast DAD: %v", err)
	}
	f := BuildNS(macA, MulticastMAC(SolicitedNode(jailA)), un, SolicitedNode(jailA), jailA, nil)
	f = appendOpt(t, f, optSourceLLA, macA)
	if _, err := Parse(f); !errors.Is(err, ErrMalformed) {
		t.Fatalf("DAD with SLLA: %v", err)
	}
}

// appendOpt appends a link-layer option and fixes length and checksum.
func appendOpt(t *testing.T, f []byte, typ byte, m MAC) []byte {
	t.Helper()
	f = append(append([]byte(nil), f...), lladOption(typ, m)...)
	return refix(f)
}

func refix(f []byte) []byte {
	ip := f[etherHdrLen:]
	binary.BigEndian.PutUint16(ip[4:], uint16(len(ip)-ipv6HdrLen))
	binary.BigEndian.PutUint16(ip[ipv6HdrLen+2:], 0)
	src := netip.AddrFrom16([16]byte(ip[8:24]))
	dst := netip.AddrFrom16([16]byte(ip[24:40]))
	binary.BigEndian.PutUint16(ip[ipv6HdrLen+2:], checksum(src, dst, ip[ipv6HdrLen:]))
	return f
}

func TestParseRejects(t *testing.T) {
	good := mkNS(router, jailA, SolicitedNode(jailA), macA, &macA)
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	cases := map[string][]byte{
		"hop limit":      mut(func(b []byte) []byte { b[etherHdrLen+7] = 254; return b }),
		"checksum":       mut(func(b []byte) []byte { b[len(b)-1] ^= 1; return b }),
		"code":           mut(func(b []byte) []byte { b[etherHdrLen+ipv6HdrLen+1] = 1; return refix(b) }),
		"multicast tgt":  mkNS(router, SolicitedNode(jailA), SolicitedNode(jailA), macA, nil),
		"mcast src":      mkNS(SolicitedNode(jailA), jailA, SolicitedNode(jailA), macA, nil),
		"wrong mcast":    mkNS(router, jailA, netip.MustParseAddr("ff02::1"), macA, nil),
		"short payload":  mut(func(b []byte) []byte { return b[:len(b)-4] }),
		"zero len opt":   mut(func(b []byte) []byte { b[etherHdrLen+ipv6HdrLen+25] = 0; return refix(b) }),
		"option overrun": mut(func(b []byte) []byte { b[etherHdrLen+ipv6HdrLen+25] = 9; return refix(b) }),
		"dup slla":       appendOpt(t, good, optSourceLLA, macB),
		"plen too small": mut(func(b []byte) []byte { binary.BigEndian.PutUint16(b[etherHdrLen+4:], 8); return b }),
	}
	for name, f := range cases {
		if _, err := Parse(f); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// SNA solicited to multicast
	na := BuildNA(macB, macA, jailA, SolicitedNode(jailA), jailA, FlagSolicited, nil)
	if _, err := Parse(na); !errors.Is(err, ErrMalformed) {
		t.Errorf("solicited NA to multicast: %v", err)
	}
}

func TestParseNotND(t *testing.T) {
	good := mkNS(router, jailA, SolicitedNode(jailA), macA, nil)
	vlan := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(vlan[12:], 0x8100)
	echo := append([]byte(nil), good...)
	echo[etherHdrLen+ipv6HdrLen] = 128
	v4 := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(v4[12:], 0x0800)
	for name, f := range map[string][]byte{"vlan": vlan, "echo": echo, "ipv4": v4, "empty": nil, "short": good[:20]} {
		if _, err := Parse(f); !errors.Is(err, ErrNotND) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseIgnoresEthernetPadding(t *testing.T) {
	f := append(mkNS(router, jailA, jailA, macA, nil), make([]byte, 10)...)
	if _, err := Parse(f); err != nil {
		t.Fatal(err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	f.Add(BuildNA(macB, macA, jailA, router, jailA, 0x60, &macB))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Parse(b)
		if err != nil {
			if p != nil {
				t.Fatal("packet with error")
			}
			return
		}
		if p.HopLimit != 255 || p.Target.IsMulticast() || p.Src.IsMulticast() {
			t.Fatalf("accepted invalid %+v", p)
		}
		if p.Type != TypeNeighborSolicitation && p.Type != TypeNeighborAdvertisement {
			t.Fatal("type")
		}
		// An accepted packet's checksum is valid by construction.
		if p.IsDAD() && p.SourceLLA != nil {
			t.Fatal("DAD with SLLA accepted")
		}
	})
}
