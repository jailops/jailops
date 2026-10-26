package format

import "testing"

func FuzzParseIndex(f *testing.F) {
	x := &Index{Size: 30, Entries: []Entry{{10, ID{1}}, {20, ZeroID}, {30, ID{2}}}}
	b, _ := x.Marshal()
	f.Add(b)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		if x, err := ParseIndex(b); err == nil {
			if m, err := x.Marshal(); err != nil || string(m) != string(b) {
				t.Fatalf("parsed index does not re-marshal identically: %v", err)
			}
		}
	})
}

func FuzzVerifyAndDecodeChunk(f *testing.F) {
	k, _ := DeriveKeys(fixedMaster())
	id, blob, _ := k.EncodeChunk([]byte("fuzz seed chunk fuzz seed chunk fuzz seed chunk"), EncodeOpts{})
	f.Add(blob, id[:])
	f.Add([]byte{}, id[:])
	f.Fuzz(func(t *testing.T, b, idb []byte) {
		var id ID
		copy(id[:], idb)
		_ = VerifyBlob(b)
		_, _ = k.DecodeChunk(b, id)
	})
}

func FuzzOpenManifest(f *testing.F) {
	k, _ := DeriveKeys(fixedMaster())
	g := k.GroupID("h", "j")
	blob, _ := k.SealManifest(&Manifest{SnapshotID: "20260101000000-00000001"}, g, nil)
	f.Add(blob)
	f.Fuzz(func(t *testing.T, b []byte) {
		_ = VerifyManifestBlob(b)
		_, _ = k.OpenManifest(b, g, "20260101000000-00000001")
	})
}

func FuzzParseReceipt(f *testing.F) {
	d := FileDigest(nil)
	r := &Receipt{ChunkCount: 1, IndexDigest: d, ManifestDigest: d, Source: "client"}
	b, _ := r.Marshal()
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = ParseReceipt(b) })
}

func FuzzOpenCatalog(f *testing.F) {
	f.Add([]byte("JOCT\x01\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = OpenCatalog(b) })
}

func FuzzDecodeRecoveryKey(f *testing.F) {
	s, _ := EncodeRecoveryKey(fixedMaster())
	f.Add(s)
	f.Fuzz(func(t *testing.T, s string) { _, _ = DecodeRecoveryKey(s) })
}

func FuzzParseIDAndSnapshot(f *testing.F) {
	f.Add("20260101000000-00000001")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseID(s)
		if ValidSnapshotID(s) {
			if _, err := SnapshotIDTime(s); err != nil {
				// a syntactically valid id may carry an impossible date, but must not panic
				return
			}
		}
	})
}
