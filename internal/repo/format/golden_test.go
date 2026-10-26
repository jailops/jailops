package format

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"
)

// The vectors pin the byte-level format. They are never regenerated to make a
// test pass: a change of any construction is a format change and bumps the
// format version. -update writes the file only when it does not exist yet.
var update = flag.Bool("update", false, "write testdata/vectors.json if it does not exist")

const vectorsPath = "testdata/vectors.json"

func fixedMaster() []byte {
	m := make([]byte, 32)
	for i := range m {
		m[i] = byte(i)
	}
	return m
}

func fixedNonce(b byte) []byte { return bytes.Repeat([]byte{b}, NonceSize) }

type vectors struct {
	Master          string `json:"master_hex"`
	Enc             string `json:"k_enc_hex"`
	ID              string `json:"k_id_hex"`
	Manifest        string `json:"k_manifest_hex"`
	Domain          string `json:"domain_id"`
	ChunkPlain      string `json:"chunk_plaintext_hex"`
	ChunkNonce      string `json:"chunk_nonce_hex"`
	ChunkID         string `json:"chunk_id_hex"`
	ChunkBlob       string `json:"chunk_blob_hex"`
	ZChunkPlain     string `json:"zchunk_plaintext_hex"`
	ZChunkNonce     string `json:"zchunk_nonce_hex"`
	ZChunkID        string `json:"zchunk_id_hex"`
	ZChunkBlob      string `json:"zchunk_blob_hex"`
	GroupHost       string `json:"group_host"`
	GroupJail       string `json:"group_jail"`
	Group           string `json:"group_id"`
	IndexFile       string `json:"index_file_hex"`
	ManifestNonce   string `json:"manifest_nonce_hex"`
	ManifestJSON    string `json:"manifest_json"`
	ManifestBlob    string `json:"manifest_blob_hex"`
	CatalogBody     string `json:"catalog_body_hex"`
	CatalogFrame    string `json:"catalog_frame_hex"`
	ReceiptJSON     string `json:"receipt_json"`
	Recovery        string `json:"recovery_key"`
	SnapshotID      string `json:"snapshot_id"`
	ManifestSnapGrp string `json:"manifest_group"`
}

func buildVectors(t *testing.T) vectors {
	t.Helper()
	master := fixedMaster()
	k, err := DeriveKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("JailOps backup test vector chunk\n")
	id, blob, err := k.EncodeChunk(plain, EncodeOpts{NoCompress: true, Nonce: fixedNonce(0x11)})
	if err != nil {
		t.Fatal(err)
	}
	zplain := bytes.Repeat([]byte("JailOps compressible vector\n"), 40)
	zid, zblob, err := k.EncodeChunk(zplain, EncodeOpts{Nonce: fixedNonce(0x33)})
	if err != nil {
		t.Fatal(err)
	}
	if zblob[5] != flagZstd {
		t.Fatal("the compressible vector was not compressed")
	}
	idx := &Index{Size: uint64(len(plain)) + 100 + uint64(len(zplain)), Entries: []Entry{
		{End: uint64(len(plain)), ID: id},
		{End: uint64(len(plain)) + 100, ID: ZeroID},
		{End: uint64(len(plain)) + 100 + uint64(len(zplain)), ID: zid},
	}}
	idxFile, err := idx.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	const host, jail = "0f8b1c6e-0000-4000-8000-000000000001", "5d2a9b44-0000-4000-8000-000000000002"
	group := k.GroupID(host, jail)
	snap := "20261007230000-0a1b2c3d"
	m := &Manifest{
		SnapshotID: snap,
		Created:    time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC),
		Host:       host, Jail: jail, JailName: "web1", Policy: "nightly",
		IndexDigest: FileDigest(idxFile),
		Streams: []Stream{{Name: "tank/jails/web1/data", Kind: StreamFiles, Offset: 0,
			PayloadSize: idx.Size - 50, CatalogSize: 50, Entries: 3, Mode: "full",
			LastFull: time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC), TrustCtime: true}},
	}
	mb, err := k.SealManifest(m, group, fixedNonce(0x22))
	if err != nil {
		t.Fatal(err)
	}
	mj, _ := json.Marshal(func() *Manifest { c := *m; c.Format = ManifestFormat; return &c }())

	body := []byte("catalog entry bytes are defined by the catalog package")
	var cf bytes.Buffer
	w, _ := NewCatalogFrameWriter(&cf)
	_, _ = w.Write(body)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rc := &Receipt{Committed: time.Date(2026, 10, 7, 23, 0, 5, 0, time.UTC), ChunkCount: 2, IndexBytes: len(idxFile),
		IndexDigest: FileDigest(idxFile), ManifestDigest: FileDigest(mb), Source: "client"}
	rj, _ := rc.Marshal()
	rk, _ := EncodeRecoveryKey(master)
	return vectors{
		Master: hex.EncodeToString(master), Enc: hex.EncodeToString(k.Enc[:]), ID: hex.EncodeToString(k.ID[:]),
		Manifest: hex.EncodeToString(k.Manifest[:]), Domain: k.DomainID,
		ChunkPlain: hex.EncodeToString(plain), ChunkNonce: hex.EncodeToString(fixedNonce(0x11)), ChunkID: id.String(),
		ChunkBlob:   hex.EncodeToString(blob),
		ZChunkPlain: hex.EncodeToString(zplain), ZChunkNonce: hex.EncodeToString(fixedNonce(0x33)), ZChunkID: zid.String(),
		ZChunkBlob: hex.EncodeToString(zblob),
		GroupHost:  host, GroupJail: jail, Group: group,
		IndexFile:     hex.EncodeToString(idxFile),
		ManifestNonce: hex.EncodeToString(fixedNonce(0x22)), ManifestJSON: string(mj), ManifestBlob: hex.EncodeToString(mb),
		CatalogBody: hex.EncodeToString(body), CatalogFrame: hex.EncodeToString(cf.Bytes()),
		ReceiptJSON: string(rj), Recovery: rk, SnapshotID: snap, ManifestSnapGrp: group,
	}
}

func TestVectors(t *testing.T) {
	got := buildVectors(t)
	if *update {
		if _, err := os.Stat(vectorsPath); err == nil {
			t.Fatal("testdata/vectors.json exists: vectors are never regenerated, bump the format version instead")
		}
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(vectorsPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var want vectors
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	// Everything except the random-free parts must be byte identical.
	if got != want {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("the format changed: vectors differ\n got: %s\nwant: %s", g, w)
	}
}

// The recorded blobs decode with the recorded keys (the other direction of the pin).
func TestVectorsDecode(t *testing.T) {
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	master, _ := hex.DecodeString(v.Master)
	k, _ := DeriveKeys(master)
	for _, c := range [][3]string{{v.ChunkBlob, v.ChunkID, v.ChunkPlain}, {v.ZChunkBlob, v.ZChunkID, v.ZChunkPlain}} {
		blob, _ := hex.DecodeString(c[0])
		id, err := ParseID(c[1])
		if err != nil {
			t.Fatal(err)
		}
		pt, err := k.DecodeChunk(blob, id)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(pt) != c[2] {
			t.Fatal("plaintext differs")
		}
	}
	mb, _ := hex.DecodeString(v.ManifestBlob)
	m, err := k.OpenManifest(mb, v.Group, v.SnapshotID)
	if err != nil || m.JailName != "web1" {
		t.Fatalf("manifest: %v %+v", err, m)
	}
	if _, err := k.OpenManifest(mb, v.Group, "20261007230001-0a1b2c3d"); err == nil {
		t.Fatal("manifest opened for another snapshot")
	}
	ix, _ := hex.DecodeString(v.IndexFile)
	if _, err := ParseIndex(ix); err != nil {
		t.Fatal(err)
	}
	cf, _ := hex.DecodeString(v.CatalogFrame)
	body, err := OpenCatalog(cf)
	if err != nil || hex.EncodeToString(body) != v.CatalogBody {
		t.Fatalf("catalog frame: %v", err)
	}
	if _, err := ParseReceipt([]byte(v.ReceiptJSON)); err != nil {
		t.Fatal(err)
	}
	if mk, err := DecodeRecoveryKey(v.Recovery); err != nil || !bytes.Equal(mk, master) {
		t.Fatalf("recovery key: %v", err)
	}
}
