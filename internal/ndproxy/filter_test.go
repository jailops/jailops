package ndproxy

import (
	"encoding/binary"
	"testing"
)

func TestNDFilter(t *testing.T) {
	prog := NDFilter()
	ns := mkNS(router, jailA, SolicitedNode(jailA), macA, &macA)
	na := BuildNA(macB, macA, jailA, router, jailA, 0x60, &macB)
	with := func(f []byte, off int, v byte) []byte {
		c := append([]byte(nil), f...)
		c[off] = v
		return c
	}
	cases := []struct {
		name string
		pkt  []byte
		want bool
	}{
		{"ns", ns, true},
		{"na", na, true},
		{"echo", with(ns, 54, 128), false},
		{"hop", with(ns, 21, 64), false},
		{"tcp", with(ns, 20, 6), false},
		{"ipv4", func() []byte { c := append([]byte(nil), ns...); binary.BigEndian.PutUint16(c[12:], 0x800); return c }(), false},
		{"short", ns[:30], false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		n, err := runBPF(prog, c.pkt)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if (n > 0) != c.want {
			t.Errorf("%s: got %d want accept=%v", c.name, n, c.want)
		}
	}
}

func rec(tsLen, align int, payload []byte, hdrExtra int) []byte {
	hdrlen := (tsLen + 10 + align - 1) &^ (align - 1)
	hdrlen += hdrExtra
	b := make([]byte, hdrlen+len(payload))
	binary.NativeEndian.PutUint32(b[tsLen:], uint32(len(payload)))
	binary.NativeEndian.PutUint32(b[tsLen+4:], uint32(len(payload)))
	binary.NativeEndian.PutUint16(b[tsLen+8:], uint16(hdrlen))
	copy(b[hdrlen:], payload)
	for len(b)%align != 0 {
		b = append(b, 0)
	}
	return b
}

func TestSplitBPFRecords(t *testing.T) {
	buf := append(rec(16, 8, []byte("hello"), 0), rec(16, 8, []byte("worlds!!!"), 0)...)
	var got []string
	if bad := splitBPFRecords(buf, 16, 8, func(f []byte) { got = append(got, string(f)) }); bad != 0 {
		t.Fatal(bad)
	}
	if len(got) != 2 || got[0] != "hello" || got[1] != "worlds!!!" {
		t.Fatalf("%q", got)
	}
	// Truncated capture length must not read out of bounds.
	bad := append([]byte(nil), buf[:len(rec(16, 8, []byte("hello"), 0))]...)
	binary.NativeEndian.PutUint32(bad[16:], 1000)
	n := 0
	if b := splitBPFRecords(bad, 16, 8, func([]byte) { n++ }); b == 0 || n != 0 {
		t.Fatalf("bad=%d n=%d", b, n)
	}
	if b := splitBPFRecords([]byte{1, 2, 3}, 16, 8, func([]byte) { t.Fatal("called") }); b != 3 {
		t.Fatal(b)
	}
}

func FuzzSplitBPFRecords(f *testing.F) {
	f.Add(rec(16, 8, []byte("hello"), 0))
	f.Fuzz(func(t *testing.T, b []byte) {
		splitBPFRecords(b, 16, 8, func(fr []byte) {
			if len(fr) > len(b) {
				t.Fatal("frame larger than buffer")
			}
		})
	})
}
