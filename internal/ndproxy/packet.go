package ndproxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// MAC is an Ethernet hardware address.
type MAC [6]byte

// IsZero reports whether m is all zero.
func (m MAC) IsZero() bool { return m == MAC{} }

// IsMulticast reports whether the group bit is set (this includes broadcast).
func (m MAC) IsMulticast() bool { return m[0]&1 != 0 }

// String formats m as colon separated hex.
func (m MAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

// Wire constants (RFC 4861 section 4, RFC 8200 section 3, RFC 4443).
const (
	etherHdrLen = 14
	ipv6HdrLen  = 40
	etherTypeV6 = 0x86DD
	protoICMPv6 = 58

	// ICMPv6 message types.
	TypeNeighborSolicitation  = 135
	TypeNeighborAdvertisement = 136

	// ND option types (RFC 4861 section 4.6).
	optSourceLLA = 1
	optTargetLLA = 2

	// NA flags (RFC 4861 section 4.4).
	FlagRouter    = 0x80
	FlagSolicited = 0x40
	FlagOverride  = 0x20

	ndHdrLen = 24 // ICMPv6 header (4) + reserved/flags (4) + target (16)

	// MaxFrame is the largest frame Parse looks at; ND messages are tiny.
	MaxFrame = 1500
)

// Errors returned by Parse. ErrNotND means the frame is well formed but is
// not a neighbor solicitation or advertisement; everything else wrapping
// ErrMalformed is a protocol violation.
var (
	ErrNotND     = errors.New("ndproxy: not a neighbor discovery packet")
	ErrMalformed = errors.New("ndproxy: malformed neighbor discovery packet")
)

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// Packet is a parsed Neighbor Solicitation or Advertisement.
//
// Only untagged Ethernet II frames are handled. An 802.1Q tagged frame has
// ethertype 0x8100 and is reported as ErrNotND. The uplink is expected to
// deliver untagged traffic (a VLAN uplink is its own vlan(4) interface).
type Packet struct {
	EthSrc, EthDst MAC
	Src, Dst       netip.Addr
	HopLimit       uint8
	Type           uint8 // TypeNeighborSolicitation or TypeNeighborAdvertisement
	Target         netip.Addr
	Flags          uint8 // NA only: FlagRouter|FlagSolicited|FlagOverride
	SourceLLA      *MAC  // NS source link-layer address option, if present
	TargetLLA      *MAC  // NA target link-layer address option, if present
}

// IsDAD reports whether p is a Duplicate Address Detection probe: a
// solicitation from the unspecified address.
func (p *Packet) IsDAD() bool {
	return p.Type == TypeNeighborSolicitation && p.Src.IsUnspecified()
}

// allNodes is ff02::1, where an unsolicited advertisement goes.
var allNodes = netip.MustParseAddr("ff02::1")

// SolicitedNode returns the solicited-node multicast address of a
// (RFC 4291 section 2.7.1): ff02::1:ffXX:XXXX with the low 24 bits of a.
func SolicitedNode(a netip.Addr) netip.Addr {
	b := a.As16()
	return netip.AddrFrom16([16]byte{0xff, 0x02, 11: 0x01, 12: 0xff, 13: b[13], 14: b[14], 15: b[15]})
}

// MulticastMAC returns the Ethernet multicast address for an IPv6
// multicast address (RFC 2464 section 7): 33:33 plus the low 32 bits.
func MulticastMAC(a netip.Addr) MAC {
	b := a.As16()
	return MAC{0x33, 0x33, b[12], b[13], b[14], b[15]}
}

// checksum computes the ICMPv6 checksum (RFC 4443 section 2.3) over the
// pseudo-header (RFC 8200 section 8.1) and msg. The checksum field in msg
// must be zero when computing, or the result is zero when msg is valid.
func checksum(src, dst netip.Addr, msg []byte) uint16 {
	var sum uint32
	add := func(b []byte) {
		for len(b) >= 2 {
			sum += uint32(b[0])<<8 | uint32(b[1])
			b = b[2:]
		}
		if len(b) == 1 {
			sum += uint32(b[0]) << 8
		}
	}
	s, d := src.As16(), dst.As16()
	add(s[:])
	add(d[:])
	var tail [8]byte
	binary.BigEndian.PutUint32(tail[0:4], uint32(len(msg)))
	tail[7] = protoICMPv6
	add(tail[:])
	add(msg)
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// Parse decodes and validates a frame. It never panics and never retains
// frame. Validation follows RFC 4861 sections 7.1.1 and 7.1.2: hop limit 255,
// ICMPv6 code 0, a correct checksum, a non-multicast target, options that
// have non-zero length within the message, and for solicitations the
// destination/source/option consistency rules.
func Parse(frame []byte) (*Packet, error) {
	if len(frame) < etherHdrLen || binary.BigEndian.Uint16(frame[12:14]) != etherTypeV6 {
		return nil, ErrNotND
	}
	if len(frame) < etherHdrLen+ipv6HdrLen {
		return nil, ErrNotND
	}
	ip := frame[etherHdrLen:]
	if ip[0]>>4 != 6 || ip[6] != protoICMPv6 {
		// Extension headers are not handled: ND packets never carry them
		// in practice and a hop-by-hop header would make validation
		// ambiguous. Such packets are simply not for us.
		return nil, ErrNotND
	}
	if len(ip) < ipv6HdrLen+4 {
		return nil, ErrNotND
	}
	if t := ip[ipv6HdrLen]; t != TypeNeighborSolicitation && t != TypeNeighborAdvertisement {
		return nil, ErrNotND
	}
	plen := int(binary.BigEndian.Uint16(ip[4:6]))
	if plen < ndHdrLen || plen > len(ip)-ipv6HdrLen {
		return nil, malformed("payload length %d with %d bytes present", plen, len(ip)-ipv6HdrLen)
	}
	msg := ip[ipv6HdrLen : ipv6HdrLen+plen] // trailing Ethernet padding is ignored
	p := &Packet{Type: msg[0], HopLimit: ip[7]}
	copy(p.EthDst[:], frame[0:6])
	copy(p.EthSrc[:], frame[6:12])
	p.Src = netip.AddrFrom16([16]byte(ip[8:24]))
	p.Dst = netip.AddrFrom16([16]byte(ip[24:40]))
	if p.HopLimit != 255 {
		return nil, malformed("hop limit %d, want 255", p.HopLimit)
	}
	if msg[1] != 0 {
		return nil, malformed("icmpv6 code %d, want 0", msg[1])
	}
	if checksum(p.Src, p.Dst, msg) != 0 {
		return nil, malformed("bad icmpv6 checksum")
	}
	if p.Src.IsMulticast() {
		return nil, malformed("multicast source address")
	}
	p.Target = netip.AddrFrom16([16]byte(msg[8:24]))
	if p.Target.IsMulticast() {
		return nil, malformed("multicast target %s", p.Target)
	}
	if p.Type == TypeNeighborAdvertisement {
		p.Flags = msg[4] & (FlagRouter | FlagSolicited | FlagOverride)
		if p.Dst.IsMulticast() && p.Flags&FlagSolicited != 0 {
			return nil, malformed("solicited advertisement to multicast")
		}
	}
	// NS reserved bits are ignored by receivers (RFC 4861 section 4.3).
	if err := parseOptions(p, msg[ndHdrLen:]); err != nil {
		return nil, err
	}
	if p.Type == TypeNeighborSolicitation {
		if p.Src.IsUnspecified() {
			if !p.Dst.IsMulticast() || p.Dst != SolicitedNode(p.Target) {
				return nil, malformed("DAD solicitation not sent to the solicited-node address")
			}
			if p.SourceLLA != nil {
				return nil, malformed("source link-layer option with unspecified source")
			}
		} else if p.Dst.IsMulticast() && p.Dst != SolicitedNode(p.Target) {
			return nil, malformed("solicitation sent to multicast %s that is not the solicited-node address", p.Dst)
		}
	}
	return p, nil
}

func parseOptions(p *Packet, opts []byte) error {
	for len(opts) > 0 {
		if len(opts) < 2 {
			return malformed("truncated option")
		}
		l := int(opts[1]) * 8
		if l == 0 {
			return malformed("option of length 0")
		}
		if l > len(opts) {
			return malformed("option overruns message")
		}
		typ := opts[0]
		if (typ == optSourceLLA || typ == optTargetLLA) && l == 8 {
			var m MAC
			copy(m[:], opts[2:8])
			if typ == optSourceLLA && p.Type == TypeNeighborSolicitation {
				if p.SourceLLA != nil {
					return malformed("duplicate source link-layer option")
				}
				p.SourceLLA = &m
			}
			if typ == optTargetLLA && p.Type == TypeNeighborAdvertisement {
				if p.TargetLLA != nil {
					return malformed("duplicate target link-layer option")
				}
				p.TargetLLA = &m
			}
		} else if typ == optSourceLLA || typ == optTargetLLA {
			// An Ethernet link-layer option is exactly 8 bytes.
			return malformed("link-layer option of length %d", l)
		}
		opts = opts[l:]
	}
	return nil
}

// build assembles Ethernet + IPv6 + ICMPv6 with a valid checksum.
func build(ethSrc, ethDst MAC, src, dst netip.Addr, icmp []byte) []byte {
	f := make([]byte, etherHdrLen+ipv6HdrLen+len(icmp))
	copy(f[0:6], ethDst[:])
	copy(f[6:12], ethSrc[:])
	binary.BigEndian.PutUint16(f[12:14], etherTypeV6)
	ip := f[etherHdrLen:]
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:6], uint16(len(icmp)))
	ip[6] = protoICMPv6
	ip[7] = 255
	s, d := src.As16(), dst.As16()
	copy(ip[8:24], s[:])
	copy(ip[24:40], d[:])
	copy(ip[ipv6HdrLen:], icmp)
	binary.BigEndian.PutUint16(ip[ipv6HdrLen+2:], checksum(src, dst, ip[ipv6HdrLen:]))
	return f
}

func lladOption(typ byte, m MAC) []byte {
	return append([]byte{typ, 1}, m[:]...)
}

// BuildNA builds a Neighbor Advertisement for target. flags is a combination
// of FlagRouter, FlagSolicited and FlagOverride. If tlla is non-nil a target
// link-layer address option is included. The frame is sent from ethSrc to
// ethDst, with IPv6 source src (normally target) and destination dst.
func BuildNA(ethSrc, ethDst MAC, src, dst, target netip.Addr, flags uint8, tlla *MAC) []byte {
	icmp := make([]byte, ndHdrLen, ndHdrLen+8)
	icmp[0] = TypeNeighborAdvertisement
	icmp[4] = flags & (FlagRouter | FlagSolicited | FlagOverride)
	t := target.As16()
	copy(icmp[8:], t[:])
	if tlla != nil {
		icmp = append(icmp, lladOption(optTargetLLA, *tlla)...)
	}
	return build(ethSrc, ethDst, src, dst, icmp)
}

// BuildNS builds a Neighbor Solicitation for target. If slla is non-nil a
// source link-layer address option is included (it must be nil when src is
// the unspecified address).
func BuildNS(ethSrc, ethDst MAC, src, dst, target netip.Addr, slla *MAC) []byte {
	icmp := make([]byte, ndHdrLen, ndHdrLen+8)
	icmp[0] = TypeNeighborSolicitation
	t := target.As16()
	copy(icmp[8:], t[:])
	if slla != nil && !src.IsUnspecified() {
		icmp = append(icmp, lladOption(optSourceLLA, *slla)...)
	}
	return build(ethSrc, ethDst, src, dst, icmp)
}
