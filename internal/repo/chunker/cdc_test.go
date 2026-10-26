package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "write testdata/vectors.json if it does not exist")

// corpus is a deterministic pseudo-random byte stream: splitmix64 from a fixed
// seed, 8 little-endian bytes per step. It does not depend on any library.
func corpus(n int, seed uint64) []byte {
	b := make([]byte, 0, n+8)
	x := seed
	for len(b) < n {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		for i := 0; i < 8; i++ {
			b = append(b, byte(z>>(8*i)))
		}
	}
	return b[:n]
}

func split(t testing.TB, data []byte, p Params) [][]byte {
	t.Helper()
	s, err := NewSplitter(bytes.NewReader(data), p)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for {
		c, err := s.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
}

func lens(cs [][]byte) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = len(c)
	}
	return out
}

type vectors struct {
	GearFirst  []string          `json:"gear_first_hex"`
	GearLast   string            `json:"gear_255_hex"`
	GearSHA256 string            `json:"gear_table_sha256"`
	CorpusSeed uint64            `json:"corpus_seed"`
	CorpusLen  int               `json:"corpus_len"`
	CorpusSHA  string            `json:"corpus_sha256"`
	Cuts       []int             `json:"cut_lengths"`
	SmallSeed  uint64            `json:"small_corpus_seed"`
	SmallLen   int               `json:"small_corpus_len"`
	SmallMin   int               `json:"small_min"`
	SmallAvg   int               `json:"small_avg"`
	SmallMax   int               `json:"small_max"`
	SmallCuts  []int             `json:"small_cut_lengths"`
	NameHashes map[string]uint32 `json:"fnv32a"`
}

func gearDigest() string {
	g := GearTable()
	var b []byte
	for _, v := range g {
		for i := 0; i < 8; i++ {
			b = append(b, byte(v>>(8*i)))
		}
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func buildVectors(t *testing.T) vectors {
	g := GearTable()
	v := vectors{
		GearFirst:  []string{hex8(g[0]), hex8(g[1]), hex8(g[2])},
		GearLast:   hex8(g[255]),
		GearSHA256: gearDigest(),
		CorpusSeed: 1, CorpusLen: 16 << 20,
		SmallSeed: 2, SmallLen: 3 << 20, SmallMin: 4 << 10, SmallAvg: 16 << 10, SmallMax: 64 << 10,
		NameHashes: map[string]uint32{"": NameHash(""), "a": NameHash("a"), "etc/rc.conf": NameHash("etc/rc.conf")},
	}
	c := corpus(v.CorpusLen, v.CorpusSeed)
	s := sha256.Sum256(c)
	v.CorpusSHA = hex.EncodeToString(s[:])
	v.Cuts = lens(split(t, c, Default))
	v.SmallCuts = lens(split(t, corpus(v.SmallLen, v.SmallSeed), Params{Min: v.SmallMin, Avg: v.SmallAvg, Max: v.SmallMax}))
	return v
}

func hex8(v uint64) string {
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (56 - 8*i))
	}
	return hex.EncodeToString(b[:])
}

// The gear table, the masks and the normalisation are part of the format. The
// vectors are never regenerated; a change is a format change.
func TestGoldenBoundaries(t *testing.T) {
	got := buildVectors(t)
	const path = "testdata/vectors.json"
	if *update {
		if _, err := os.Stat(path); err == nil {
			t.Fatal("vectors exist: they are never regenerated, bump the format version instead")
		}
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want vectors
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if !bytes.Equal(gj, wj) {
		t.Fatalf("chunk boundaries or gear table changed: this breaks deduplication against existing backups\n got: %s\nwant: %s", gj, wj)
	}
}

func TestReassemblyBoundsAndDeterminism(t *testing.T) {
	p := Params{Min: 16 << 10, Avg: 64 << 10, Max: 256 << 10}
	data := corpus(20<<20, 3)
	a := split(t, data, p)
	var re []byte
	for i, c := range a {
		re = append(re, c...)
		if len(c) > p.Max || (len(c) < p.Min && i != len(a)-1) {
			t.Fatalf("chunk %d has size %d outside [%d,%d]", i, len(c), p.Min, p.Max)
		}
	}
	if !bytes.Equal(re, data) {
		t.Fatal("chunks do not reassemble to the input")
	}
	b := split(t, data, p)
	if len(a) != len(b) {
		t.Fatal("not deterministic")
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatal("not deterministic")
		}
	}
	if avg := len(data) / len(a); avg < p.Avg/2 || avg > p.Avg*2 {
		t.Fatalf("average chunk %d is far from the target %d", avg, p.Avg)
	}
}

// Cutting must not depend on how the reader delivers the bytes.
func TestReaderGranularityDoesNotMatter(t *testing.T) {
	p := Params{Min: 8 << 10, Avg: 32 << 10, Max: 128 << 10}
	data := corpus(3<<20, 4)
	want := lens(split(t, data, p))
	s, _ := NewSplitter(&oneByteReader{r: bytes.NewReader(data), n: 997}, p)
	var got []int
	for {
		c, err := s.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, len(c))
	}
	if len(got) != len(want) {
		t.Fatal("cut count depends on read granularity")
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatal("cuts depend on read granularity")
		}
	}
}

type oneByteReader struct {
	r io.Reader
	n int
}

func (o *oneByteReader) Read(p []byte) (int, error) {
	if len(p) > o.n {
		p = p[:o.n]
	}
	return o.r.Read(p)
}

// Property: an insertion shifts only boundaries near it. After the insertion
// point the cut points of the shifted stream are the old ones moved by the
// inserted length, apart from a bounded number of chunks.
func TestInsertionShiftsOnlyNearbyBoundaries(t *testing.T) {
	p := Params{Min: 16 << 10, Avg: 64 << 10, Max: 256 << 10}
	data := corpus(16<<20, 5)
	ends := func(cs [][]byte) map[int]bool {
		m := map[int]bool{}
		pos := 0
		for _, c := range cs {
			pos += len(c)
			m[pos] = true
		}
		return m
	}
	for _, at := range []int{0, 1000, 5 << 20, 12<<20 + 77} {
		ins := []byte("inserted-bytes!")
		shifted := append(append(append([]byte(nil), data[:at]...), ins...), data[at:]...)
		a := ends(split(t, data, p))
		bcs := split(t, shifted, p)
		b := ends(bcs)
		moved, diff := 0, 0
		for e := range a {
			if e <= at {
				continue
			}
			moved++
			if !b[e+len(ins)] {
				diff++
			}
		}
		// only boundaries between the insertion and the next resynchronisation may differ
		if diff > 8 {
			t.Fatalf("insertion at %d: %d of %d later boundaries changed", at, diff, moved)
		}
		// everything before the insertion point is untouched
		for e := range a {
			if e < at-p.Max && !b[e] {
				t.Fatalf("insertion at %d changed an earlier boundary at %d", at, e)
			}
		}
	}
}

func TestEdgeCases(t *testing.T) {
	p := Default
	if len(split(t, nil, p)) != 0 {
		t.Fatal("empty input must give no chunks")
	}
	if c := split(t, []byte("x"), p); len(c) != 1 || string(c[0]) != "x" {
		t.Fatal("tiny input")
	}
	zeros := make([]byte, 40<<20)
	cs := split(t, zeros, p)
	for _, c := range cs[:len(cs)-1] {
		if len(c) != p.Max {
			t.Fatalf("all-zero input must cut at Max, got %d", len(c))
		}
	}
	if _, err := NewSplitter(bytes.NewReader(nil), Params{Min: 10, Avg: 5, Max: 20}); err == nil {
		t.Fatal("bad parameters accepted")
	}
	if _, err := NewSplitter(bytes.NewReader(nil), Params{Min: 100, Avg: 200, Max: 300}); err == nil {
		t.Fatal("tiny average accepted")
	}
}

type errReader struct{ n int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, io.ErrClosedPipe
	}
	n := min(len(p), e.n)
	e.n -= n
	return n, nil
}

func TestReaderErrorPropagates(t *testing.T) {
	s, _ := NewSplitter(&errReader{n: 1 << 20}, Params{Min: 1 << 10, Avg: 4 << 10, Max: 16 << 10})
	for {
		_, err := s.Next()
		if err == io.ErrClosedPipe {
			return
		}
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
	}
}

func TestIsZero(t *testing.T) {
	for _, n := range []int{0, 1, 7, 8, 9, 100, 4096} {
		z := make([]byte, n)
		if !IsZero(z) {
			t.Fatalf("zeros of %d", n)
		}
		for i := 0; i < n; i += 1 + n/7 {
			z[i] = 1
			if IsZero(z) {
				t.Fatalf("nonzero byte %d of %d missed", i, n)
			}
			z[i] = 0
		}
	}
}

func TestPackPolicy(t *testing.T) {
	p := PackPolicy{}.Norm()
	if p.SmallFile != 256<<10 || p.PackMin != 512<<10 || p.PackMax != 2<<20 || p.PackDiv != 8 {
		t.Fatalf("%+v", p)
	}
	if p.Large(256<<10-1) || !p.Large(256<<10) {
		t.Fatal("Large threshold")
	}
	if !p.EndsPack(2<<20, "x") || p.EndsPack(2<<20-1, "name-with-nonzero-hash-0") && NameHash("name-with-nonzero-hash-0")%8 != 0 {
		t.Fatal("PackMax rule")
	}
	if p.EndsPack(512<<10-1, zeroHashName(t)) {
		t.Fatal("pack ended below PackMin")
	}
	if !p.EndsPack(512<<10, zeroHashName(t)) {
		t.Fatal("pack did not end at a name boundary")
	}
	if p.EndsPack(512<<10, nonZeroHashName(t)) {
		t.Fatal("pack ended at a non-boundary name")
	}
}

func zeroHashName(t *testing.T) string {
	for i := 0; ; i++ {
		n := "f" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
		if NameHash(n)%8 == 0 {
			return n
		}
	}
}

func nonZeroHashName(t *testing.T) string {
	for i := 0; ; i++ {
		n := "g" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
		if NameHash(n)%8 != 0 {
			return n
		}
	}
}

func FuzzSplitter(f *testing.F) {
	f.Add([]byte("hello"), uint8(3))
	f.Add(corpus(100000, 9), uint8(200))
	f.Fuzz(func(t *testing.T, data []byte, k uint8) {
		p := Params{Min: 256 + int(k), Avg: 2048 + 2*int(k), Max: 8192 + 4*int(k)}
		s, err := NewSplitter(bytes.NewReader(data), p)
		if err != nil {
			t.Skip("fuzzed parameters outside the splitter's accepted range")
		}
		var re []byte
		for {
			c, err := s.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(c) == 0 || len(c) > p.Max {
				t.Fatalf("bad chunk size %d", len(c))
			}
			re = append(re, c...)
		}
		if !bytes.Equal(re, data) {
			t.Fatal("reassembly")
		}
	})
}
