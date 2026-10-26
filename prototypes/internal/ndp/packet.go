// Package ndp holds the pure, Linux-testable logic of prototype 5 (NDP proxy experiments):
// Ethernet/IPv6/ICMPv6 neighbor discovery packets, BPF framing, output parsers, the watchdog
// state machine and the hypothesis evaluators. Nothing here was run on FreeBSD by its author;
// the FreeBSD-only parts (BPF ioctls) live in bpf_freebsd.go.
package ndp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// MAC is an Ethernet address.
type MAC [6]byte

func (m MAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

// IsZero reports the all-zero address.
func (m MAC) IsZero() bool { return m == MAC{} }

// ParseMAC parses aa:bb:cc:dd:ee:ff.
func ParseMAC(s string) (MAC, error) {
	var m MAC
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 6 {
		return m, fmt.Errorf("bad MAC %q", s)
	}
	for i, p := range parts {
		var v uint8
		if _, err := fmt.Sscanf(p, "%x", &v); err != nil || len(p) > 2 || len(p) == 0 {
			return m, fmt.Errorf("bad MAC %q", s)
		}
		m[i] = v
	}
	return m, nil
}

// ICMPv6 types used here.
const (
	TypeEchoRequest = 128
	TypeEchoReply   = 129
	TypeNS          = 135
	TypeNA          = 136
)

// SolicitedNode returns ff02::1:ffXX:XXXX for a.
func SolicitedNode(a netip.Addr) netip.Addr {
	b := a.As16()
	return netip.AddrFrom16([16]byte{0xff, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0xff, b[13], b[14], b[15]})
}

// SolicitedNodeMAC returns 33:33:ff:XX:XX:XX.
func SolicitedNodeMAC(a netip.Addr) MAC {
	b := a.As16()
	return MAC{0x33, 0x33, 0xff, b[13], b[14], b[15]}
}

// Frame is a parsed Ethernet + IPv6 + ICMPv6 frame. Only ICMPv6 with next header 58 directly
// after the IPv6 header is parsed (no extension headers).
type Frame struct {
	EthDst, EthSrc MAC
	Src, Dst       netip.Addr
	HopLimit       uint8
	ICMPType       uint8
	ICMPCode       uint8
	Target         netip.Addr // NS, NA
	SLLA, TLLA     MAC
	HasSLLA        bool
	HasTLLA        bool
	Router         bool
	Solicited      bool
	Override       bool
	ChecksumOK     bool
}

// Errors from ParseFrame.
var (
	ErrShort     = errors.New("frame too short")
	ErrNotIPv6   = errors.New("not IPv6")
	ErrNotICMPv6 = errors.New("not ICMPv6 (or has extension headers)")
)

// ParseFrame parses an Ethernet frame.
func ParseFrame(b []byte) (*Frame, error) {
	if len(b) < 14+40+4 {
		return nil, ErrShort
	}
	if binary.BigEndian.Uint16(b[12:14]) != 0x86dd {
		return nil, ErrNotIPv6
	}
	f := &Frame{}
	copy(f.EthDst[:], b[0:6])
	copy(f.EthSrc[:], b[6:12])
	ip := b[14:]
	if ip[0]>>4 != 6 {
		return nil, ErrNotIPv6
	}
	plen := int(binary.BigEndian.Uint16(ip[4:6]))
	if ip[6] != 58 {
		return nil, ErrNotICMPv6
	}
	f.HopLimit = ip[7]
	f.Src = netip.AddrFrom16([16]byte(ip[8:24]))
	f.Dst = netip.AddrFrom16([16]byte(ip[24:40]))
	icmp := ip[40:]
	if plen < len(icmp) {
		icmp = icmp[:plen]
	}
	if len(icmp) < 4 {
		return nil, ErrShort
	}
	f.ICMPType, f.ICMPCode = icmp[0], icmp[1]
	f.ChecksumOK = len(icmp) == plen && Checksum(f.Src, f.Dst, icmp) == 0
	switch f.ICMPType {
	case TypeNS, TypeNA:
		if len(icmp) < 24 {
			return nil, ErrShort
		}
		if f.ICMPType == TypeNA {
			f.Router = icmp[4]&0x80 != 0
			f.Solicited = icmp[4]&0x40 != 0
			f.Override = icmp[4]&0x20 != 0
		}
		f.Target = netip.AddrFrom16([16]byte(icmp[8:24]))
		for o := icmp[24:]; len(o) >= 8; {
			l := int(o[1]) * 8
			if l == 0 || l > len(o) {
				break
			}
			switch o[0] {
			case 1:
				if l >= 8 {
					copy(f.SLLA[:], o[2:8])
					f.HasSLLA = true
				}
			case 2:
				if l >= 8 {
					copy(f.TLLA[:], o[2:8])
					f.HasTLLA = true
				}
			}
			o = o[l:]
		}
	}
	return f, nil
}

// IsNS and IsNA report the ICMPv6 type.
func (f *Frame) IsNS() bool { return f.ICMPType == TypeNS }
func (f *Frame) IsNA() bool { return f.ICMPType == TypeNA }

// MulticastNS: a neighbor solicitation sent to a multicast IPv6 destination.
func (f *Frame) MulticastNS() bool { return f.IsNS() && f.Dst.IsMulticast() }

// UnicastNS: a neighbor solicitation sent to a unicast IPv6 destination (NUD probe).
func (f *Frame) UnicastNS() bool { return f.IsNS() && !f.Dst.IsMulticast() }

// DAD: duplicate address detection solicitation (unspecified source).
func (f *Frame) DAD() bool { return f.IsNS() && f.Src.IsUnspecified() }

// Checksum computes the ICMPv6 checksum over the pseudo header and msg; over a message that
// already carries a valid checksum the result is 0.
func Checksum(src, dst netip.Addr, msg []byte) uint16 {
	var sum uint32
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(b[i])<<8 | uint32(b[i+1])
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	s, d := src.As16(), dst.As16()
	add(s[:])
	add(d[:])
	var tail [8]byte
	binary.BigEndian.PutUint32(tail[0:4], uint32(len(msg)))
	tail[7] = 58
	add(tail[:])
	add(msg)
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func buildEth(dst, src MAC, ipsrc, ipdst netip.Addr, icmp []byte) []byte {
	b := make([]byte, 14+40+len(icmp))
	copy(b[0:6], dst[:])
	copy(b[6:12], src[:])
	binary.BigEndian.PutUint16(b[12:14], 0x86dd)
	ip := b[14:]
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:6], uint16(len(icmp)))
	ip[6] = 58
	ip[7] = 255
	s, d := ipsrc.As16(), ipdst.As16()
	copy(ip[8:24], s[:])
	copy(ip[24:40], d[:])
	copy(ip[40:], icmp)
	binary.BigEndian.PutUint16(ip[42:44], Checksum(ipsrc, ipdst, ip[40:]))
	return b
}

// NAParams describes a neighbor advertisement.
type NAParams struct {
	EthSrc, EthDst MAC
	Src, Dst       netip.Addr
	Target         netip.Addr
	TLLA           MAC
	Solicited      bool
	Override       bool
	Router         bool
}

// BuildNA builds a complete Ethernet frame carrying a neighbor advertisement with a
// target link-layer address option. Hop limit is 255 (RFC 4861 section 7.1.2).
func BuildNA(p NAParams) []byte {
	icmp := make([]byte, 24+8)
	icmp[0] = TypeNA
	if p.Router {
		icmp[4] |= 0x80
	}
	if p.Solicited {
		icmp[4] |= 0x40
	}
	if p.Override {
		icmp[4] |= 0x20
	}
	t := p.Target.As16()
	copy(icmp[8:24], t[:])
	icmp[24], icmp[25] = 2, 1
	copy(icmp[26:32], p.TLLA[:])
	return buildEth(p.EthDst, p.EthSrc, p.Src, p.Dst, icmp)
}

// NSParams describes a neighbor solicitation.
type NSParams struct {
	EthSrc, EthDst MAC
	Src, Dst       netip.Addr
	Target         netip.Addr
	SLLA           MAC
	NoSLLA         bool
}

// BuildNS builds a complete Ethernet frame carrying a neighbor solicitation.
func BuildNS(p NSParams) []byte {
	n := 32
	if p.NoSLLA {
		n = 24
	}
	icmp := make([]byte, n)
	icmp[0] = TypeNS
	t := p.Target.As16()
	copy(icmp[8:24], t[:])
	if !p.NoSLLA {
		icmp[24], icmp[25] = 1, 1
		copy(icmp[26:32], p.SLLA[:])
	}
	return buildEth(p.EthDst, p.EthSrc, p.Src, p.Dst, icmp)
}

// AnswerFor decides whether the proxy should answer frame f for one of the addresses in
// mine, and builds the NA. hostMAC is advertised as the target link-layer address. It never
// answers DAD solicitations (RFC 4861 7.2.4 would need an all-nodes reply; the prototype keeps
// out of DAD so that it cannot hide a real duplicate).
func AnswerFor(f *Frame, mine map[netip.Addr]bool, hostMAC MAC) ([]byte, bool) {
	if f == nil || !f.IsNS() || f.DAD() || !mine[f.Target] || !f.ChecksumOK || f.HopLimit != 255 {
		return nil, false
	}
	dstMAC := f.EthSrc
	if f.HasSLLA {
		dstMAC = f.SLLA
	}
	return BuildNA(NAParams{
		EthSrc: hostMAC, EthDst: dstMAC,
		Src: f.Target, Dst: f.Src, Target: f.Target, TLLA: hostMAC,
		Solicited: true, Override: true,
	}), true
}
