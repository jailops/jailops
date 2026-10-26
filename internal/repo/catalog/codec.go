package catalog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strings"

	"jailops.net/jailops/internal/repo/format"
)

// Encoding limits enforced when decoding.
const (
	maxDepth     = 4096
	maxAttrName  = 4096
	maxAttrValue = 16 << 20
	maxNameLen   = 4096
)

// ---- encoding ---------------------------------------------------------------------

// encoder appends the encoding to one buffer and flushes it to its writer
// whenever it holds flushAt bytes, so streaming a catalog needs one small
// buffer instead of the whole encoding.
type encoder struct {
	b   []byte
	w   io.Writer
	err error
	// unchecked disables the sort check (tests build hostile catalogs with it)
	unchecked bool
}

const flushAt = 1 << 20

func (e *encoder) flush() {
	if e.err != nil || len(e.b) == 0 {
		return
	}
	_, e.err = e.w.Write(e.b)
	e.b = e.b[:0]
}

func (e *encoder) uvarint(v uint64) { e.b = binary.AppendUvarint(e.b, v) }
func (e *encoder) varint(v int64)   { e.b = binary.AppendVarint(e.b, v) }

func (e *encoder) str(s string) {
	e.uvarint(uint64(len(s)))
	e.b = append(e.b, s...)
}

func (e *encoder) attrs(a []Attr) {
	e.uvarint(uint64(len(a)))
	for _, x := range a {
		e.str(x.Name)
		e.uvarint(uint64(len(x.Value)))
		e.b = append(e.b, x.Value...)
	}
}

func (e *encoder) entry(x *Entry) {
	e.b = append(e.b, byte(x.Kind))
	e.str(x.Name)
	e.uvarint(uint64(x.Mode))
	e.uvarint(uint64(x.UID))
	e.uvarint(uint64(x.GID))
	e.varint(x.MtimeNs)
	e.varint(x.CtimeNs)
	e.uvarint(x.Inode)
	e.uvarint(uint64(x.Flags))
	e.attrs(x.Attrs)
	switch x.Kind {
	case KDir:
		for i, c := range x.Children {
			if i > 0 && x.Children[i-1].Name >= c.Name && e.err == nil && !e.unchecked {
				e.err = fmt.Errorf("catalog directory entries are not sorted or repeat (%q)", c.Name)
			}
			e.entry(c)
		}
		e.b = append(e.b, 0)
	case KFile:
		e.uvarint(x.Size)
		if x.Size > 0 {
			e.uvarint(x.PayOff)
			e.b = append(e.b, x.Sum[:]...)
		}
	case KSymlink, KHardlink:
		e.str(x.Link)
	case KChar, KBlock:
		e.uvarint(uint64(x.Major))
		e.uvarint(uint64(x.Minor))
	}
	if e.w != nil && len(e.b) >= flushAt {
		e.flush()
	}
}

// Encode writes the framed catalog (header, entries, SHA-256 trailer) to w in
// pieces of about flushAt bytes; it never holds the whole encoding.
func (t *Tree) Encode(w io.Writer) error { return t.encode(w, false) }

func (t *Tree) encode(w io.Writer, unchecked bool) error {
	fw, err := format.NewCatalogFrameWriter(w)
	if err != nil {
		return err
	}
	e := &encoder{b: make([]byte, 0, flushAt+64<<10), w: fw, unchecked: unchecked}
	e.entry(t.Root)
	e.flush()
	if e.err != nil {
		return e.err
	}
	return fw.Close()
}

// Bytes returns the encoded catalog (byte-identical to Encode).
func (t *Tree) Bytes() ([]byte, error) {
	var buf byteSink
	buf.b = make([]byte, 0, 64+96*max(t.Count, 1))
	if err := t.Encode(&buf); err != nil {
		return nil, err
	}
	return buf.b, nil
}

type byteSink struct{ b []byte }

func (s *byteSink) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }

// ---- decoding ---------------------------------------------------------------------

type decoder struct {
	b     []byte
	pos   int
	depth int
	count int
	// slab hands out entries from blocks; stack collects the children of the
	// directories being decoded, copied out once at an exact size.
	slab  []Entry
	stack []*Entry
}

func (d *decoder) newEntry() *Entry {
	if len(d.slab) == 0 {
		d.slab = make([]Entry, 512)
	}
	x := &d.slab[0]
	d.slab = d.slab[1:]
	return x
}

var errShort = errors.New("catalog is truncated")

func (d *decoder) byte() (byte, error) {
	if d.pos >= len(d.b) {
		return 0, errShort
	}
	c := d.b[d.pos]
	d.pos++
	return c, nil
}

// minimalLen is the length of the shortest uvarint encoding of v.
func minimalLen(v uint64) int { return max(1, (bits.Len64(v)+6)/7) }

var errNonCanonical = errors.New("catalog holds a non-canonical varint")

// uvarint reads a minimal-length uvarint: an overlong encoding of the same
// value is refused, so that every catalog has exactly one encoding.
func (d *decoder) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.b[d.pos:])
	if n <= 0 {
		return 0, errShort
	}
	if n != minimalLen(v) {
		return 0, errNonCanonical
	}
	d.pos += n
	return v, nil
}

func (d *decoder) varint() (int64, error) {
	u, n := binary.Uvarint(d.b[d.pos:])
	if n <= 0 {
		return 0, errShort
	}
	if n != minimalLen(u) {
		return 0, errNonCanonical
	}
	d.pos += n
	v := int64(u >> 1)
	if u&1 != 0 {
		v = ^v
	}
	return v, nil
}

func (d *decoder) bytes(n uint64) ([]byte, error) {
	if n > uint64(len(d.b)-d.pos) {
		return nil, errShort
	}
	out := d.b[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return out, nil
}

func (d *decoder) str(limit uint64) (string, error) {
	n, err := d.uvarint()
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", errors.New("catalog string is too long")
	}
	b, err := d.bytes(n)
	return string(b), err
}

func (d *decoder) u32() (uint32, error) {
	v, err := d.uvarint()
	if err != nil {
		return 0, err
	}
	if v > 0xffffffff {
		return 0, errors.New("catalog value out of range")
	}
	return uint32(v), nil
}

// entry reads one entry whose kind byte has already been read.
func (d *decoder) entry(kind Kind) (*Entry, error) {
	if d.depth > maxDepth {
		return nil, errors.New("catalog nests too deeply")
	}
	d.count++
	x := d.newEntry()
	x.Kind = kind
	var err error
	if x.Name, err = d.str(maxNameLen); err != nil {
		return nil, err
	}
	// a name is one path component: no separator, no NUL, never "", "." or ".."
	// below the root (a hostile catalog must not steer extraction)
	if strings.ContainsAny(x.Name, "/\x00") || x.Name == "." || x.Name == ".." || (x.Name == "" && d.depth > 0) {
		return nil, fmt.Errorf("catalog holds an unsafe name %q", x.Name)
	}
	if x.Mode, err = d.u32(); err != nil {
		return nil, err
	}
	if x.UID, err = d.u32(); err != nil {
		return nil, err
	}
	if x.GID, err = d.u32(); err != nil {
		return nil, err
	}
	if x.MtimeNs, err = d.varint(); err != nil {
		return nil, err
	}
	if x.CtimeNs, err = d.varint(); err != nil {
		return nil, err
	}
	if x.Inode, err = d.uvarint(); err != nil {
		return nil, err
	}
	if x.Flags, err = d.u32(); err != nil {
		return nil, err
	}
	na, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	if na > uint64(len(d.b)) {
		return nil, errShort
	}
	for i := uint64(0); i < na; i++ {
		name, err := d.str(maxAttrName)
		if err != nil {
			return nil, err
		}
		vl, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		if vl > maxAttrValue {
			return nil, errors.New("catalog attribute is too large")
		}
		val, err := d.bytes(vl)
		if err != nil {
			return nil, err
		}
		if name == "" || strings.IndexByte(name, 0) >= 0 {
			return nil, errors.New("catalog holds an unsafe attribute name")
		}
		if i > 0 && x.Attrs[i-1].Name >= name {
			return nil, fmt.Errorf("catalog attributes are not sorted or repeat (%q)", name)
		}
		x.Attrs = append(x.Attrs, Attr{Name: name, Value: append([]byte(nil), val...)})
	}
	switch kind {
	case KDir:
		d.depth++
		mark := len(d.stack)
		for {
			k, err := d.byte()
			if err != nil {
				return nil, err
			}
			if k == 0 {
				break
			}
			c, err := d.entry(Kind(k))
			if err != nil {
				return nil, err
			}
			d.stack = append(d.stack, c)
		}
		x.Children = append([]*Entry(nil), d.stack[mark:]...)
		clear(d.stack[mark:])
		d.stack = d.stack[:mark]
		d.depth--
		for i := 1; i < len(x.Children); i++ {
			if x.Children[i-1].Name >= x.Children[i].Name {
				return nil, fmt.Errorf("catalog directory entries are not sorted or repeat (%q)", x.Children[i].Name)
			}
		}
	case KFile:
		if x.Size, err = d.uvarint(); err != nil {
			return nil, err
		}
		if x.Size > 0 {
			if x.PayOff, err = d.uvarint(); err != nil {
				return nil, err
			}
			if x.PayOff > 1<<62 || x.Size > 1<<62 {
				return nil, errors.New("catalog offset out of range")
			}
			b, err := d.bytes(32)
			if err != nil {
				return nil, err
			}
			copy(x.Sum[:], b)
		}
	case KSymlink, KHardlink:
		if x.Link, err = d.str(1 << 16); err != nil {
			return nil, err
		}
		if kind == KHardlink && (x.Link == "" || strings.ContainsRune(x.Link, 0)) {
			return nil, errors.New("catalog holds an unsafe hard link target")
		}
	case KChar, KBlock:
		if x.Major, err = d.u32(); err != nil {
			return nil, err
		}
		if x.Minor, err = d.u32(); err != nil {
			return nil, err
		}
	case KFifo:
	default:
		return nil, fmt.Errorf("catalog holds an unknown entry kind %d", kind)
	}
	return x, nil
}

// Decode parses an encoded catalog and checks its framing and digest.
func Decode(b []byte) (*Tree, error) {
	body, err := format.OpenCatalog(b)
	if err != nil {
		return nil, err
	}
	d := &decoder{b: body}
	k, err := d.byte()
	if err != nil {
		return nil, err
	}
	if Kind(k) != KDir {
		return nil, errors.New("catalog root is not a directory")
	}
	root, err := d.entry(KDir)
	if err != nil {
		return nil, err
	}
	if d.pos != len(body) {
		return nil, errors.New("catalog has trailing data")
	}
	return &Tree{Root: root, Count: d.count}, nil
}
