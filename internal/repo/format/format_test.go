package format

import (
	"bytes"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func testKeys(t testing.TB) *Keys {
	t.Helper()
	k, err := DeriveKeys(fixedMaster())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeyDerivationDomainSeparation(t *testing.T) {
	k := testKeys(t)
	if k.Enc == k.ID || k.ID == k.Manifest || k.Enc == k.Manifest {
		t.Fatal("sub-keys must differ")
	}
	if !ValidDomainID(k.DomainID) {
		t.Fatalf("bad domain id %q", k.DomainID)
	}
	other := make([]byte, 32)
	k2, _ := DeriveKeys(other)
	if k2.DomainID == k.DomainID {
		t.Fatal("different masters share a domain id")
	}
	if _, err := DeriveKeys(make([]byte, 31)); err == nil {
		t.Fatal("short master accepted")
	}
}

func TestChunkRoundTrip(t *testing.T) {
	k := testKeys(t)
	for _, n := range []int{0, 1, 31, 4096, 1 << 20} {
		for _, compressible := range []bool{false, true} {
			data := make([]byte, n)
			if compressible {
				copy(data, bytes.Repeat([]byte("abc"), n/3))
			} else {
				_, _ = rand.Read(data)
			}
			id, blob, err := k.EncodeChunk(data, EncodeOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyBlob(blob); err != nil {
				t.Fatal(err)
			}
			if compressible && n > 100 && blob[5] != flagZstd {
				t.Fatalf("size %d should compress", n)
			}
			if !compressible && n > 100 && blob[5] != 0 {
				t.Fatalf("size %d incompressible but flagged compressed", n)
			}
			pt, err := k.DecodeChunk(blob, id)
			if err != nil || !bytes.Equal(pt, data) {
				t.Fatalf("size %d: %v", n, err)
			}
		}
	}
}

func TestChunkTamperAndBinding(t *testing.T) {
	k := testKeys(t)
	id, blob, _ := k.EncodeChunk([]byte("some chunk content"), EncodeOpts{})
	// every single-byte change is caught (by the trailer or the AEAD)
	for i := range blob {
		c := append([]byte(nil), blob...)
		c[i] ^= 1
		if _, err := k.DecodeChunk(c, id); err == nil {
			t.Fatalf("byte %d flipped, still decoded", i)
		}
	}
	// the id is bound: a valid blob under another id does not decode
	other := k.ChunkID([]byte("other"))
	if _, err := k.DecodeChunk(blob, other); err == nil {
		t.Fatal("blob decoded under a different id")
	}
	// a different key domain cannot decode
	k2, _ := DeriveKeys(bytes.Repeat([]byte{7}, 32))
	if _, err := k2.DecodeChunk(blob, id); err == nil {
		t.Fatal("blob decoded with another key")
	}
	// poisoning: a self-consistent blob whose content does not hash to its id
	plant := k.ChunkID([]byte("victim plaintext"))
	forged, err := k.SealChunk(plant, []byte("attacker plaintext"), EncodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlob(forged); err != nil {
		t.Fatal("keyless verification must accept a self-consistent forged blob")
	}
	if _, err := k.DecodeChunk(forged, plant); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("poisoned chunk not detected: %v", err)
	}
	if _, err := k.DecodeChunk(blob, ZeroID); err == nil {
		t.Fatal("zero id accepted")
	}
}

func TestDomainBoundInAAD(t *testing.T) {
	// Same enc key, different domain: forge a Keys with a different domain id
	// and the AEAD must refuse.
	k := testKeys(t)
	id, blob, _ := k.EncodeChunk([]byte("x"), EncodeOpts{NoCompress: true})
	k2 := *k
	k2.domain[0] ^= 1
	if _, err := k2.DecodeChunk(blob, id); err == nil {
		t.Fatal("domain is not bound into the chunk AAD")
	}
}

func TestZstdBombRefused(t *testing.T) {
	k := testKeys(t)
	big := make([]byte, MaxChunkSize+1)
	if _, err := k.SealChunk(k.ChunkID(big), big, EncodeOpts{}); err == nil {
		t.Fatal("oversized chunk sealed")
	}
	// craft a blob whose zstd body inflates beyond the cap
	e, _ := zstd.NewWriter(nil)
	frame := e.EncodeAll(make([]byte, MaxChunkSize+4096), nil)
	id := k.ChunkID([]byte("anything"))
	blob := k.sealRaw(id, frame, flagZstd)
	if _, err := k.DecodeChunk(blob, id); err == nil {
		t.Fatal("decompression bomb accepted")
	}
}

// sealRaw builds a blob around an arbitrary body (tests).
func (k *Keys) sealRaw(id ID, body []byte, flags byte) []byte {
	nonce := fixedNonce(9)
	aeadBlob := []byte(blobMagic)
	aeadBlob = append(aeadBlob, blobVersion, flags, 0, 0)
	aeadBlob = append(aeadBlob, nonce...)
	a, _ := newAEAD(k.Enc[:])
	aeadBlob = a.Seal(aeadBlob, nonce, body, k.blobAAD(flags, id))
	return appendTrailer(aeadBlob)
}

func TestPooledZstdMatchesReference(t *testing.T) {
	ref, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	data := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 100000)
	for _, n := range []int{0, 1, 100, 4095, 65536, 128<<10 + 1, 1 << 20, len(data)} {
		if !bytes.Equal(compress(data[:n]), ref.EncodeAll(data[:n], nil)) {
			t.Fatalf("size %d: pooled encoder output differs", n)
		}
	}
}

func TestConcurrentEncodeIsDeterministic(t *testing.T) {
	k := testKeys(t)
	data := bytes.Repeat([]byte("deterministic "), 50000)
	_, want, _ := k.EncodeChunk(data, EncodeOpts{Nonce: fixedNonce(1)})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < 5; r++ {
				_, b, err := k.EncodeChunk(data, EncodeOpts{Nonce: fixedNonce(1)})
				if err != nil || !bytes.Equal(b, want) {
					t.Error("concurrent encode differs")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestIndex(t *testing.T) {
	a, b := ID{1}, ID{2}
	x := &Index{Size: 30, Entries: []Entry{{10, a}, {20, ZeroID}, {30, b}}}
	f, err := x.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	y, err := ParseIndex(f)
	if err != nil || y.Size != 30 || len(y.Entries) != 3 {
		t.Fatalf("%v %+v", err, y)
	}
	if ids := y.ChunkIDs(); len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("ids %v", ids)
	}
	if s, err := y.Sub(10, 30); err != nil || len(s.Entries) != 2 || s.Entries[0].End != 10 || s.Size != 20 {
		t.Fatalf("sub %v %+v", err, s)
	}
	if _, err := y.Sub(5, 30); err == nil {
		t.Fatal("non-boundary start accepted")
	}
	if _, err := y.Sub(10, 25); err == nil {
		t.Fatal("non-boundary end accepted")
	}
	if s, err := y.Sub(30, 30); err != nil || len(s.Entries) != 0 {
		t.Fatal("empty sub at end")
	}
	if y.Find(0) != 0 || y.Find(10) != 1 || y.Find(29) != 2 || y.Find(30) != -1 {
		t.Fatal("Find")
	}
	for i := range f {
		c := append([]byte(nil), f...)
		c[i] ^= 1
		if _, err := ParseIndex(c); err == nil {
			t.Fatalf("byte %d flipped, index still parses", i)
		}
	}
	for _, bad := range []*Index{
		{Size: 10, Entries: []Entry{{5, a}, {5, b}}},
		{Size: 10, Entries: []Entry{{5, a}}},
		{Size: 1, Entries: nil},
		{Size: MaxChunkSize + 1, Entries: []Entry{{MaxChunkSize + 1, a}}},
	} {
		if _, err := bad.Marshal(); err == nil {
			t.Fatalf("invalid index %+v marshalled", bad)
		}
	}
	empty := &Index{}
	ef, err := empty.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if e, err := ParseIndex(ef); err != nil || len(e.Entries) != 0 {
		t.Fatal(err)
	}
}

func TestManifest(t *testing.T) {
	k := testKeys(t)
	group := k.GroupID("h", "j")
	if len(group) != 32 || !ValidGroupID(group) {
		t.Fatal(group)
	}
	if k.GroupID("h", "j2") == group || k.GroupID("h2", "j") == group {
		t.Fatal("group ids collide")
	}
	idx := &Index{Size: 100, Entries: []Entry{{60, ID{1}}, {100, ID{2}}}}
	f, _ := idx.Marshal()
	m := &Manifest{SnapshotID: "20260101000000-00000001", Created: time.Unix(1, 0).UTC(), IndexDigest: FileDigest(f),
		Streams: []Stream{{Name: "a", Kind: StreamFiles, PayloadSize: 60, CatalogSize: 10}, {Name: "b", Kind: StreamFiles, Offset: 70, PayloadSize: 30}}}
	blob, err := k.SealManifest(m, group, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.OpenManifest(blob, group, m.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.CheckIndex(f, idx); err != nil {
		t.Fatal(err)
	}
	if _, err := k.OpenManifest(blob, k.GroupID("h", "other"), m.SnapshotID); err == nil {
		t.Fatal("manifest opened for another group")
	}
	if _, err := k.OpenManifest(blob, group, "20260101000000-00000002"); err == nil {
		t.Fatal("manifest opened for another snapshot")
	}
	k2, _ := DeriveKeys(bytes.Repeat([]byte{3}, 32))
	if _, err := k2.OpenManifest(blob, group, m.SnapshotID); err == nil {
		t.Fatal("manifest opened with another key")
	}
	for i := range blob {
		c := append([]byte(nil), blob...)
		c[i] ^= 1
		if _, err := k.OpenManifest(c, group, m.SnapshotID); err == nil {
			t.Fatalf("byte %d flipped, manifest opens", i)
		}
	}
	bad := *got
	bad.Streams = got.Streams[:1]
	if bad.CheckIndex(f, idx) == nil {
		t.Fatal("short stream list accepted")
	}
	if got.CheckIndex(append([]byte("x"), f...), idx) == nil {
		t.Fatal("wrong index digest accepted")
	}
}

func TestSnapshotIDs(t *testing.T) {
	t0 := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	a, err := NewSnapshotID(t0, nil)
	if err != nil || !ValidSnapshotID(a) {
		t.Fatal(a, err)
	}
	b, _ := NewSnapshotID(t0.Add(time.Microsecond), nil)
	if !(a < b) {
		t.Fatalf("ids do not sort by time: %s %s", a, b)
	}
	if tm, err := SnapshotIDTime(a); err != nil || !tm.Equal(t0.Truncate(time.Second)) {
		t.Fatal(tm, err)
	}
	for _, bad := range []string{"", "20260304050607-1234567", "20260304050607_12345678", "2026030405060x-12345678", "20261304050607-12345678", "20260304050607-1234567G", "../../etc/passwd/aaaaaaaa"} {
		if ValidSnapshotID(bad) {
			if _, err := SnapshotIDTime(bad); err == nil {
				t.Fatalf("%q accepted", bad)
			}
		}
	}
}

func TestRecoveryKey(t *testing.T) {
	m := fixedMaster()
	s, err := EncodeRecoveryKey(m)
	if err != nil || !strings.HasPrefix(s, "JOR1-") {
		t.Fatal(s, err)
	}
	got, err := DecodeRecoveryKey(strings.ToLower(strings.ReplaceAll(s, "-", " ")))
	if err != nil || !bytes.Equal(got, m) {
		t.Fatal(err)
	}
	bad := []byte(s)
	if bad[8] == 'A' {
		bad[8] = 'B'
	} else {
		bad[8] = 'A'
	}
	if _, err := DecodeRecoveryKey(string(bad)); err == nil {
		t.Fatal("typo accepted")
	}
	if _, err := DecodeRecoveryKey("XXXX-AAAA"); err == nil {
		t.Fatal("wrong prefix")
	}
}

func TestReceipt(t *testing.T) {
	d := FileDigest([]byte("x"))
	r := &Receipt{Committed: time.Unix(5, 0), ChunkCount: 1, IndexBytes: 2, IndexDigest: d, ManifestDigest: d, Source: "client"}
	b, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := r.Marshal()
	if !bytes.Equal(b, b2) {
		t.Fatal("receipt encoding is not deterministic")
	}
	if _, err := ParseReceipt(b); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"format":2}`, strings.Replace(string(b), d, "zz", 1), string(b) + "x", `{"format":1,"unknown":1}`} {
		if _, err := ParseReceipt([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCatalogFrame(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCatalogFrameWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("hello"))
	_ = w.Close()
	if w.Len() != int64(buf.Len()) {
		t.Fatal("length")
	}
	body, err := OpenCatalog(buf.Bytes())
	if err != nil || string(body) != "hello" {
		t.Fatal(err)
	}
	for i := range buf.Bytes() {
		c := append([]byte(nil), buf.Bytes()...)
		c[i] ^= 1
		if _, err := OpenCatalog(c); err == nil {
			t.Fatalf("byte %d flipped, catalog opens", i)
		}
	}
	if _, err := OpenCatalog(buf.Bytes()[:buf.Len()-1]); err == nil {
		t.Fatal("truncated catalog opens")
	}
}

func TestParseID(t *testing.T) {
	id := ID{0xab, 0xcd, 3}
	got, err := ParseID(id.String())
	if err != nil || got != id {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "zz", strings.ToUpper(id.String()), id.String() + "00", "../" + id.String()[3:]} {
		if _, err := ParseID(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
