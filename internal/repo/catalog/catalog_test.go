package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "write testdata/vectors.json if it does not exist")

func sum(b []byte) [32]byte { return sha256.Sum256(b) }

// sampleTree builds a catalog by hand with fixed values (no file system).
func sampleTree() (*Tree, map[string][]byte) {
	files := map[string][]byte{
		"etc/rc.conf":       []byte("hostname=\"web1\"\n"),
		"usr/local/bin/app": []byte("#!/bin/sh\necho app\n"),
	}
	rc := files["etc/rc.conf"]
	app := files["usr/local/bin/app"]
	root := &Entry{Kind: KDir, Mode: 0o755, UID: 0, GID: 0, MtimeNs: 1_700_000_000_000_000_001, CtimeNs: 1_700_000_000_000_000_002, Inode: 4,
		Children: []*Entry{
			{Kind: KDir, Name: "etc", Mode: 0o755, MtimeNs: 1_700_000_001_000_000_000, CtimeNs: 1_700_000_001_000_000_005, Inode: 5, Flags: 0,
				Children: []*Entry{
					{Kind: KFile, Name: "rc.conf", Mode: 0o644, UID: 0, GID: 0, MtimeNs: 1_700_000_002_000_000_000, CtimeNs: 1_700_000_002_000_000_007, Inode: 6,
						Size: uint64(len(rc)), PayOff: 0, Sum: sum(rc),
						Attrs: []Attr{{Name: "freebsd:extattr:user:note", Value: []byte("hi")}, {Name: "linux:xattr:user.comment", Value: []byte{0, 1, 2, 255}}}},
					{Kind: KHardlink, Name: "rc.conf.link", Mode: 0o644, MtimeNs: 1_700_000_002_000_000_000, CtimeNs: 1_700_000_002_000_000_007, Inode: 6, Link: "etc/rc.conf"},
				}},
			{Kind: KFifo, Name: "pipe", Mode: 0o600, UID: 1001, GID: 1001, MtimeNs: -5, Inode: 9},
			{Kind: KSymlink, Name: "sym", Mode: 0o777, MtimeNs: 1_700_000_003_000_000_000, Inode: 8, Link: "etc/rc.conf"},
			{Kind: KDir, Name: "usr", Mode: 0o755, Inode: 10, Flags: 0x20000,
				Children: []*Entry{{Kind: KDir, Name: "local", Mode: 0o755, Inode: 11,
					Children: []*Entry{{Kind: KDir, Name: "bin", Mode: 0o755, Inode: 12,
						Children: []*Entry{{Kind: KFile, Name: "app", Mode: 0o4755, UID: 5, GID: 6, Inode: 13, Size: uint64(len(app)), PayOff: uint64(len(rc)), Sum: sum(app)}}}}}}},
			{Kind: KChar, Name: "zero", Mode: 0o666, Inode: 14, Major: 13, Minor: 12},
			{Kind: KBlock, Name: "disk", Mode: 0o660, Inode: 15, Major: 8, Minor: 1},
			{Kind: KFile, Name: "empty", Mode: 0o600, Inode: 16},
		}}
	sort.Slice(root.Children, func(i, j int) bool { return root.Children[i].Name < root.Children[j].Name })
	return &Tree{Root: root, Count: 13}, files
}

type vectors struct {
	CatalogHex string                 `json:"catalog_hex"`
	Paths      []string               `json:"paths"`
	FileSums   map[string]string      `json:"file_sums"`
	Contents   map[string]string      `json:"file_content_hex"`
	Attrs      map[string][][2]string `json:"attrs"`
	Hardlink   struct {
		Path   string `json:"path"`
		Target string `json:"target"`
	} `json:"hardlink"`
}

func buildVectors(t *testing.T) vectors {
	tree, files := sampleTree()
	b, err := tree.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	v := vectors{CatalogHex: hex.EncodeToString(b), FileSums: map[string]string{}, Contents: map[string]string{}, Attrs: map[string][][2]string{}}
	tree.Walk(func(p string, e *Entry) bool { v.Paths = append(v.Paths, p); return true })
	for p, c := range files {
		s := sha256.Sum256(c)
		v.FileSums[p] = hex.EncodeToString(s[:])
		v.Contents[p] = hex.EncodeToString(c)
	}
	e, _ := tree.Lookup("etc/rc.conf")
	for _, a := range e.Attrs {
		v.Attrs["etc/rc.conf"] = append(v.Attrs["etc/rc.conf"], [2]string{a.Name, hex.EncodeToString(a.Value)})
	}
	v.Hardlink.Path, v.Hardlink.Target = "etc/rc.conf.link", "etc/rc.conf"
	return v
}

func TestGoldenCatalog(t *testing.T) {
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
		t.Fatalf("the catalog encoding changed: bump the format version instead of updating the vectors\n got %s\nwant %s", gj, wj)
	}
	// and the recorded bytes decode
	cb, _ := hex.DecodeString(want.CatalogHex)
	tr, err := Decode(cb)
	if err != nil || tr.Count != 13 {
		t.Fatalf("%v %+v", err, tr)
	}
}

func TestRoundTripAndDeterminism(t *testing.T) {
	tree, _ := sampleTree()
	a, err := tree.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var streamed bytes.Buffer
	if err := tree.Encode(&streamed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, streamed.Bytes()) {
		t.Fatal("Encode and Bytes differ")
	}
	got, err := Decode(a)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := got.Bytes()
	if !bytes.Equal(a, b) {
		t.Fatal("decode then encode changed the bytes")
	}
	e, err := got.Lookup("usr/local/bin/app")
	if err != nil || e.Mode != 0o4755 || e.UID != 5 || e.PayOff != 16 {
		t.Fatalf("%v %+v", err, e)
	}
	if st := got.Stats(); st.Files != 3 || st.Dirs != 5 || st.Symlinks != 1 || st.Hardlinks != 1 || st.Special != 3 {
		t.Fatalf("%+v", st)
	}
	if _, err := got.Lookup("etc/../../x"); err == nil {
		t.Fatal("escape accepted")
	}
	if _, err := got.Lookup("etc/rc.conf/x"); err == nil {
		t.Fatal("lookup through a file")
	}
}

func TestDecodeRejectsDamage(t *testing.T) {
	tree, _ := sampleTree()
	good, _ := tree.Bytes()
	for i := range good {
		c := append([]byte(nil), good...)
		c[i] ^= 1
		if _, err := Decode(c); err == nil {
			t.Fatalf("flipping byte %d went unnoticed", i)
		}
	}
	if _, err := Decode(good[:len(good)-1]); err == nil {
		t.Fatal("truncated catalog accepted")
	}
	if _, err := Decode(nil); err == nil {
		t.Fatal("empty catalog accepted")
	}
}

// reframe encodes a hostile tree. The framing is valid; the content is not.
func reframe(t *testing.T, root *Entry) []byte {
	t.Helper()
	var buf byteSink
	if err := (&Tree{Root: root, Count: 1}).encode(&buf, true); err != nil {
		t.Fatal(err)
	}
	return buf.b
}

func TestDecodeRejectsHostileContent(t *testing.T) {
	dir := func(name string, kids ...*Entry) *Entry {
		return &Entry{Kind: KDir, Name: name, Mode: 0o755, Children: kids}
	}
	file := func(name string) *Entry { return &Entry{Kind: KFile, Name: name, Mode: 0o644} }
	cases := map[string]*Entry{
		"dot dot":        dir("", file("..")),
		"dot":            dir("", file(".")),
		"slash in name":  dir("", file("a/b")),
		"nul in name":    dir("", file("a\x00b")),
		"empty name":     dir("", file("")),
		"unsorted":       dir("", file("b"), file("a")),
		"duplicate":      dir("", file("a"), file("a")),
		"unknown kind":   dir("", &Entry{Kind: 99, Name: "x"}),
		"root not dir":   file(""),
		"attrs unsorted": dir("", &Entry{Kind: KFile, Name: "a", Attrs: []Attr{{Name: "b"}, {Name: "a"}}}),
		"attr empty":     dir("", &Entry{Kind: KFile, Name: "a", Attrs: []Attr{{Name: ""}}}),
		"hardlink empty": dir("", &Entry{Kind: KHardlink, Name: "h"}),
	}
	for name, root := range cases {
		if _, err := Decode(reframe(t, root)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// deep nesting
	deep := &Entry{Kind: KDir, Name: "d"}
	root := &Entry{Kind: KDir}
	cur := root
	for i := 0; i < maxDepth+10; i++ {
		n := &Entry{Kind: KDir, Name: "d"}
		cur.Children = []*Entry{n}
		cur = n
	}
	_ = deep
	if _, err := Decode(reframe(t, root)); err == nil || !strings.Contains(err.Error(), "deeply") {
		t.Fatalf("deep nesting: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	tree, _ := sampleTree()
	good, _ := tree.Bytes()
	f.Add(good)
	f.Add([]byte("JOCT\x01\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		tr, err := Decode(b)
		if err != nil {
			return
		}
		// anything that decodes must re-encode to the same bytes and walk without panicking
		again, err := tr.Bytes()
		if err != nil || !bytes.Equal(again, b) {
			t.Fatalf("re-encoding differs: %v", err)
		}
		tr.Walk(func(string, *Entry) bool { return true })
	})
}

// FuzzDecodeBody fuzzes the entry decoder behind a valid frame, so the fuzzer
// does not have to guess the SHA-256 trailer.
func FuzzDecodeBody(f *testing.F) {
	tree, _ := sampleTree()
	good, _ := tree.Bytes()
	f.Add(good[8 : len(good)-32])
	f.Fuzz(func(t *testing.T, body []byte) {
		var buf bytes.Buffer
		buf.WriteString("JOCT\x01\x00\x00\x00")
		buf.Write(body)
		h := sha256.Sum256(buf.Bytes())
		buf.Write(h[:])
		if tr, err := Decode(buf.Bytes()); err == nil {
			again, err := tr.Bytes()
			if err != nil || !bytes.Equal(again, buf.Bytes()) {
				t.Fatalf("re-encoding differs: %v", err)
			}
		}
	})
}

func TestBrowse(t *testing.T) {
	tree, _ := sampleTree()
	l, err := tree.List("etc")
	if err != nil || len(l) != 2 {
		t.Fatal(err, l)
	}
	if got, _ := tree.Find("", "*.conf"); len(got) != 1 || got[0] != "etc/rc.conf" {
		t.Fatal(got)
	}
	if got, _ := tree.Find("usr", "app"); len(got) != 1 || got[0] != "usr/local/bin/app" {
		t.Fatal(got)
	}
	n, b, err := tree.Subtree("etc")
	if err != nil || n != 3 || b != 16 {
		t.Fatal(n, b, err)
	}
	e, _ := tree.Lookup("usr/local/bin/app")
	if !strings.HasPrefix(e.ModeString(), "-rwsr-xr-x") {
		t.Fatal(e.ModeString())
	}
	if !strings.Contains(e.Line("app"), "app") {
		t.Fatal(e.Line("app"))
	}
	if Base("a/b/c") != "c" || Base("c") != "c" {
		t.Fatal("Base")
	}
	_ = fmt.Sprint()
}
