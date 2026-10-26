package ndp

import (
	"encoding/binary"
	"errors"
)

// Insn is a classic BPF instruction (same layout as struct bpf_insn).
type Insn struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// Classic BPF opcode bits (identical on FreeBSD; see bpf(4)).
const (
	opLD   = 0x00
	opJMP  = 0x05
	opRET  = 0x06
	szH    = 0x08
	szB    = 0x10
	modABS = 0x20
	jEQ    = 0x10
	srcK   = 0x00
)

// FilterICMPv6 builds a program that accepts Ethernet/IPv6 frames whose next header is ICMPv6
// (no extension headers) and whose ICMPv6 type is in types, and returns snap bytes for them.
func FilterICMPv6(types []uint8, snap uint32) []Insn {
	n := len(types)
	p := []Insn{
		{opLD | szH | modABS, 0, 0, 12},
		{opJMP | jEQ | srcK, 0, uint8(2 + n), 0x86dd}, // jf -> ret 0
		{opLD | szB | modABS, 0, 0, 14 + 6},
		{opJMP | jEQ | srcK, 0, uint8(n), 58},
		{opLD | szB | modABS, 0, 0, 14 + 40},
	}
	for i, t := range types {
		p = append(p, Insn{opJMP | jEQ | srcK, uint8(n - 1 - i + 1), 0, uint32(t)})
	}
	p = append(p, Insn{opRET | srcK, 0, 0, 0}, Insn{opRET | srcK, 0, 0, snap})
	return p
}

// RunFilter is a minimal interpreter for the subset FilterICMPv6 emits (tests only, but exported
// so the main program can self-check its program before loading it).
func RunFilter(prog []Insn, pkt []byte) uint32 {
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code & 0x07 {
		case opLD:
			off := int(in.K)
			switch in.Code & 0x18 {
			case szH:
				if off+2 > len(pkt) {
					return 0
				}
				a = uint32(binary.BigEndian.Uint16(pkt[off:]))
			case szB:
				if off+1 > len(pkt) {
					return 0
				}
				a = uint32(pkt[off])
			default:
				return 0
			}
		case opJMP:
			if a == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case opRET:
			return in.K
		}
	}
	return 0
}

// BPFAlign is BPF_ALIGNMENT (sizeof(long)). UNVERIFIED: 8 on amd64 and arm64 FreeBSD.
const BPFAlign = 8

// TimevalLen is sizeof(struct timeval) inside struct bpf_hdr on LP64 FreeBSD.
// UNVERIFIED: 16 (checked against x/sys BpfHdr layout, not against a running kernel).
const TimevalLen = 16

// ErrBadBPF is returned for a malformed read buffer.
var ErrBadBPF = errors.New("malformed bpf buffer")

// SplitBPF splits one read(2) buffer from a BPF descriptor into captured packets. Each record
// is a struct bpf_hdr {timeval; u32 caplen; u32 datalen; u16 hdrlen} followed by caplen bytes,
// the next record starting at BPF_WORDALIGN(hdrlen+caplen). The returned slices alias buf.
func SplitBPF(buf []byte, tvLen int) ([][]byte, error) {
	var out [][]byte
	for len(buf) > 0 {
		if len(buf) < tvLen+10 {
			return out, ErrBadBPF
		}
		caplen := int(binary.LittleEndian.Uint32(buf[tvLen:]))
		hdrlen := int(binary.LittleEndian.Uint16(buf[tvLen+8:]))
		if hdrlen < tvLen+10 || hdrlen > len(buf) || caplen < 0 || hdrlen+caplen > len(buf) {
			return out, ErrBadBPF
		}
		out = append(out, buf[hdrlen:hdrlen+caplen])
		next := (hdrlen + caplen + BPFAlign - 1) &^ (BPFAlign - 1)
		if next >= len(buf) {
			break
		}
		buf = buf[next:]
	}
	return out, nil
}
