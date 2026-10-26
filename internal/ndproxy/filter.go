package ndproxy

import (
	"encoding/binary"
	"errors"
)

// BPFInsn is one classic BPF instruction (struct bpf_insn).
type BPFInsn struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// Classic BPF opcodes used by the filter.
const (
	bpfLdHAbs = 0x28 // BPF_LD|BPF_H|BPF_ABS
	bpfLdBAbs = 0x30 // BPF_LD|BPF_B|BPF_ABS
	bpfJeqK   = 0x15 // BPF_JMP|BPF_JEQ|BPF_K
	bpfRetK   = 0x06 // BPF_RET|BPF_K
)

// NDFilter returns a classic BPF program that accepts untagged Ethernet
// frames carrying IPv6 with next header ICMPv6, hop limit 255 and an ICMPv6
// type of 135 (NS) or 136 (NA), and rejects everything else. The hop limit
// test is only a pre-filter; Parse repeats every check.
func NDFilter() []BPFInsn {
	const reject = 10
	jf := func(i int) uint8 { return uint8(reject - (i + 1)) }
	return []BPFInsn{
		0:  {Code: bpfLdHAbs, K: 12},
		1:  {Code: bpfJeqK, Jf: jf(1), K: etherTypeV6},
		2:  {Code: bpfLdBAbs, K: etherHdrLen + 6},
		3:  {Code: bpfJeqK, Jf: jf(3), K: protoICMPv6},
		4:  {Code: bpfLdBAbs, K: etherHdrLen + 7},
		5:  {Code: bpfJeqK, Jf: jf(5), K: 255},
		6:  {Code: bpfLdBAbs, K: etherHdrLen + ipv6HdrLen},
		7:  {Code: bpfJeqK, Jt: 1, K: TypeNeighborSolicitation},
		8:  {Code: bpfJeqK, Jf: jf(8), K: TypeNeighborAdvertisement},
		9:  {Code: bpfRetK, K: MaxFrame},
		10: {Code: bpfRetK, K: 0},
	}
}

// runBPF is a minimal interpreter for the opcodes NDFilter uses. It exists
// so the filter can be tested without a kernel. It returns the accepted
// length, or 0 for reject.
func runBPF(prog []BPFInsn, pkt []byte) (uint32, error) {
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case bpfLdHAbs:
			if int(in.K)+2 > len(pkt) {
				return 0, nil
			}
			a = uint32(binary.BigEndian.Uint16(pkt[in.K:]))
		case bpfLdBAbs:
			if int(in.K)+1 > len(pkt) {
				return 0, nil
			}
			a = uint32(pkt[in.K])
		case bpfJeqK:
			if a == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case bpfRetK:
			return in.K, nil
		default:
			return 0, errors.New("ndproxy: unsupported bpf opcode")
		}
	}
	return 0, errors.New("ndproxy: bpf program fell off the end")
}

// splitBPFRecords walks a buffer returned by read(2) on a BPF descriptor and
// calls fn with each captured frame. tsLen is the size of the timestamp
// field of struct bpf_hdr (a struct timeval) and align is BPF_ALIGNMENT.
// Layout: tstamp[tsLen] caplen u32, datalen u32, hdrlen u16. Records are
// padded so the next one starts at a multiple of align. Truncated or
// inconsistent records end the walk; the number of bytes skipped is
// reported as bad so a caller can count it.
func splitBPFRecords(buf []byte, tsLen, align int, fn func(frame []byte)) (bad int) {
	minHdr := tsLen + 10
	for len(buf) > 0 {
		if len(buf) < minHdr {
			return len(buf)
		}
		caplen := int(binary.NativeEndian.Uint32(buf[tsLen:]))
		hdrlen := int(binary.NativeEndian.Uint16(buf[tsLen+8:]))
		if hdrlen < minHdr || hdrlen > len(buf) || caplen < 0 || caplen > len(buf)-hdrlen {
			return len(buf)
		}
		fn(buf[hdrlen : hdrlen+caplen])
		step := (hdrlen + caplen + align - 1) &^ (align - 1)
		if step >= len(buf) {
			return 0
		}
		buf = buf[step:]
	}
	return 0
}
