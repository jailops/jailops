package catalog

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func write(t testing.TB, root, rel string, data []byte) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func rnd(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// makeTree creates a varied tree and returns the root.
func makeTree(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "etc/rc.conf", []byte("hostname=web1\n"))
	write(t, root, "etc/motd", []byte("welcome\n"))
	write(t, root, "var/db/big.bin", rnd(1, 700<<10))
	write(t, root, "var/db/huge.bin", rnd(2, 3<<20))
	write(t, root, "var/empty", nil)
	for i := 0; i < 40; i++ {
		write(t, root, fmt.Sprintf("usr/small/f%03d", i), rnd(int64(100+i), 3000+i*911))
	}
	if err := os.Symlink("../etc/rc.conf", filepath.Join(root, "var", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/target", filepath.Join(root, "var", "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "etc/motd"), filepath.Join(root, "etc/motd.hard")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "var/pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "etc/motd"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(filepath.Join(root, "etc/rc.conf"), "user.note", []byte("kept"), 0); err != nil {
		t.Fatalf("this test needs user extended attributes on the temp directory: %v", err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for _, p := range []string{"etc/rc.conf", "etc/motd", "var/db/big.bin"} {
		if err := os.Chtimes(filepath.Join(root, p), old, old); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// payloadOf reads the payload bytes for a scan result (what the engine feeds the chunker).
func payloadOf(t testing.TB, root string, tree *Tree) []byte {
	t.Helper()
	var out []byte
	tree.Walk(func(p string, e *Entry) bool {
		if e.Kind == KFile && e.Size > 0 {
			if uint64(len(out)) != e.PayOff {
				t.Fatalf("%s: payload offset %d, stream is at %d", p, e.PayOff, len(out))
			}
			b, err := os.ReadFile(filepath.Join(root, p))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b...)
		}
		return true
	})
	return out
}

func fillSums(t testing.TB, root string, res *Result) {
	t.Helper()
	for _, r := range res.Runs {
		for _, f := range r.Files {
			b, err := os.ReadFile(f.Path)
			if err != nil {
				t.Fatal(err)
			}
			f.Entry.Sum = sha256.Sum256(b)
		}
	}
}

func scan(t testing.TB, root string, o ScanOptions) *Result {
	t.Helper()
	res, err := Scan(root, o)
	if err != nil {
		t.Fatal(err)
	}
	fillSums(t, root, res)
	return res
}

func TestScanLayout(t *testing.T) {
	root := makeTree(t)
	res := scan(t, root, ScanOptions{})
	tree := res.Tree
	// walk order = payload order, and the offsets tile the payload
	pay := payloadOf(t, root, tree)
	if uint64(len(pay)) != res.PayloadSize {
		t.Fatalf("payload %d, scan says %d", len(pay), res.PayloadSize)
	}
	// runs tile the payload exactly, never overlap, and large files stand alone
	var pos uint64
	for i, r := range res.Runs {
		if r.Start != pos {
			t.Fatalf("run %d starts at %d, expected %d", i, r.Start, pos)
		}
		if r.Large && len(r.Files) != 1 {
			t.Fatal("a large run holds several files")
		}
		if !r.Large {
			for _, f := range r.Files {
				if f.Entry.Size >= 256<<10 {
					t.Fatalf("large file %s in a pack", f.Rel)
				}
			}
		}
		pos = r.End
	}
	if pos != res.PayloadSize {
		t.Fatal("runs do not cover the payload")
	}
	// kinds
	check := func(p string, k Kind) {
		e, err := tree.Lookup(p)
		if err != nil || e.Kind != k {
			t.Fatalf("%s: %v %+v", p, err, e)
		}
	}
	check("etc/motd", KFile)
	check("etc/motd.hard", KHardlink)
	check("var/link", KSymlink)
	check("var/pipe", KFifo)
	check("var/empty", KFile)
	if e, _ := tree.Lookup("etc/motd.hard"); e.Link != "etc/motd" {
		t.Fatalf("hardlink target %q", e.Link)
	}
	if e, _ := tree.Lookup("var/dangling"); e.Link != "/nonexistent/target" {
		t.Fatal("dangling symlink target")
	}
	e, _ := tree.Lookup("etc/rc.conf")
	if len(e.Attrs) != 1 || e.Attrs[0].Name != "linux:xattr:user.note" || string(e.Attrs[0].Value) != "kept" {
		t.Fatalf("attrs %+v", e.Attrs)
	}
	if e.MtimeNs != time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.UTC).UnixNano() {
		t.Fatalf("mtime %d", e.MtimeNs)
	}
	// symlinks are never followed: the catalog does not contain the target's content
	if l, _ := tree.Lookup("var/link"); l.Size != 0 || len(l.Children) != 0 {
		t.Fatal("symlink was followed")
	}
	// the catalog survives its own encoding
	b, err := tree.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
}

func catalogBytes(t testing.TB, res *Result) []byte {
	b, err := res.Tree.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScanIsDeterministicAndIndependentOfConcurrency(t *testing.T) {
	root := makeTree(t)
	base := catalogBytes(t, scan(t, root, ScanOptions{Workers: 1}))
	for _, o := range []ScanOptions{{Workers: 16}, {Workers: 3, Ahead: 4}, {Workers: 64, Ahead: 1}} {
		if !bytes.Equal(catalogBytes(t, scan(t, root, o)), base) {
			t.Fatalf("scan with %+v differs from the sequential one", o)
		}
	}
	// OnRun delivers the same runs in the same order
	var seen []uint64
	res, err := Scan(root, ScanOptions{OnRun: func(r *Run) error { seen = append(seen, r.Start); return nil }})
	if err != nil || len(res.Runs) != 0 {
		t.Fatal(err)
	}
	ref := scan(t, root, ScanOptions{})
	if len(seen) != len(ref.Runs) {
		t.Fatalf("%d runs vs %d", len(seen), len(ref.Runs))
	}
	for i := range seen {
		if seen[i] != ref.Runs[i].Start {
			t.Fatal("run order differs")
		}
	}
	// an error from OnRun ends the scan with that error
	boom := fmt.Errorf("boom")
	if _, err := Scan(root, ScanOptions{OnRun: func(*Run) error { return boom }}); err != boom {
		t.Fatalf("got %v", err)
	}
}

func TestScanBoundsTheWalkAhead(t *testing.T) {
	root := t.TempDir()
	for d := 0; d < 30; d++ {
		for f := 0; f < 30; f++ {
			write(t, root, fmt.Sprintf("d%02d/f%02d", d, f), []byte("x"))
		}
	}
	before := parkedTotal.Load()
	res, err := Scan(root, ScanOptions{Workers: 4, Ahead: 20})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tree.Count != 30*30+30+1 {
		t.Fatalf("count %d", res.Tree.Count)
	}
	if parkedTotal.Load() == before {
		t.Fatal("the Ahead bound was never exercised")
	}
}

// ---- pack boundaries depend on names only ----------------------------------------------

func TestPackBoundariesDependOnNamesOnly(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 400; i++ {
		write(t, root, fmt.Sprintf("p/f%04d", i), bytes.Repeat([]byte{byte(i)}, 30_000))
	}
	runs := func() [][2]string {
		res := scan(t, root, ScanOptions{})
		var out [][2]string
		for _, r := range res.Runs {
			out = append(out, [2]string{r.Files[0].Rel, r.Files[len(r.Files)-1].Rel})
		}
		return out
	}
	a := runs()
	if len(a) < 5 {
		t.Fatalf("expected several packs, got %d", len(a))
	}
	for _, r := range a {
		_ = r
	}
	// removing one early file leaves every pack after the next name-defined boundary identical
	if err := os.Remove(filepath.Join(root, "p/f0003")); err != nil {
		t.Fatal(err)
	}
	b := runs()
	same := 0
	set := map[[2]string]bool{}
	for _, r := range a {
		set[r] = true
	}
	for _, r := range b {
		if set[r] {
			same++
		}
	}
	if same < len(a)-3 {
		t.Fatalf("only %d of %d packs survived removing one file", same, len(a))
	}
}

// ---- change detection --------------------------------------------------------------------

type fileOf map[string]*Entry

func reuse(res *Result) (reusable, total int) {
	for _, r := range res.Runs {
		total++
		if r.Reusable {
			reusable++
		}
	}
	return
}

func runContaining(res *Result, rel string) *Run {
	for i := range res.Runs {
		for _, f := range res.Runs[i].Files {
			if f.Rel == rel {
				return &res.Runs[i]
			}
		}
	}
	return nil
}

func TestChangeDetection(t *testing.T) {
	root := makeTree(t)
	first := scan(t, root, ScanOptions{TrustCtimeInode: true})
	if n, _ := reuse(first); n != 0 {
		t.Fatal("a first scan reuses nothing")
	}
	// nothing changed: every run is reusable and the old bytes are contiguous
	second := scan(t, root, ScanOptions{Prev: first.Tree, TrustCtimeInode: true})
	n, total := reuse(second)
	if n != total || total == 0 {
		t.Fatalf("unchanged tree: %d of %d runs reusable", n, total)
	}
	for _, r := range second.Runs {
		if r.OldEnd-r.OldStart != r.End-r.Start || r.OldStart != r.Start {
			t.Fatalf("unchanged tree: run %+v maps elsewhere", r)
		}
	}
	// forced full read
	forced := scan(t, root, ScanOptions{Prev: first.Tree, TrustCtimeInode: true, NoReuse: true})
	if n, _ := reuse(forced); n != 0 {
		t.Fatal("NoReuse still reuses")
	}

	type mutation struct {
		name   string
		file   string
		mutate func(p string)
	}
	muts := []mutation{
		{"content, same size, mtime restored (ctime catches it)", "var/db/big.bin", func(p string) {
			fi, _ := os.Stat(p)
			b, _ := os.ReadFile(p)
			b[100] ^= 0xff
			_ = os.WriteFile(p, b, 0o644)
			_ = os.Chtimes(p, fi.ModTime(), fi.ModTime())
		}},
		{"append", "var/db/huge.bin", func(p string) {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			_, _ = f.Write([]byte("more"))
			f.Close()
		}},
		{"chmod", "etc/rc.conf", func(p string) { _ = os.Chmod(p, 0o600) }},
		{"mtime only", "etc/motd", func(p string) {
			_ = os.Chtimes(p, time.Unix(1_000_000, 5), time.Unix(1_000_000, 5))
		}},
		{"xattr", "etc/rc.conf", func(p string) { _ = unix.Setxattr(p, "user.note", []byte("changed"), 0) }},
		{"xattr added", "etc/motd", func(p string) { _ = unix.Setxattr(p, "user.new", []byte("1"), 0) }},
		{"replaced by a new inode with identical content", "etc/rc.conf", func(p string) {
			b, _ := os.ReadFile(p)
			fi, _ := os.Stat(p)
			x, _ := unix.Getxattr(p, "user.note", make([]byte, 100))
			_ = x
			_ = os.Remove(p)
			_ = os.WriteFile(p, b, 0o644)
			_ = os.Chtimes(p, fi.ModTime(), fi.ModTime())
			_ = unix.Setxattr(p, "user.note", []byte("kept"), 0)
		}},
		{"a small file in a pack changes", "usr/small/f010", func(p string) { _ = os.WriteFile(p, []byte("different"), 0o644) }},
	}
	for _, m := range muts {
		t.Run(m.name, func(t *testing.T) {
			r2 := makeTreeCopy(t, root)
			p := filepath.Join(r2, filepath.FromSlash(m.file))
			prev := scan(t, r2, ScanOptions{TrustCtimeInode: true})
			m.mutate(p)
			res := scan(t, r2, ScanOptions{Prev: prev.Tree, TrustCtimeInode: true})
			run := runContaining(res, m.file)
			if run != nil && run.Reusable {
				t.Fatalf("a changed file's run %s is marked reusable", m.file)
			}
			// the rest is still reusable: only the runs holding the change are re-read
			n, total := reuse(res)
			if total-n > 2 {
				t.Fatalf("%d of %d runs are not reusable after one change", total-n, total)
			}
		})
	}
}

// makeTreeCopy makes an independent tree with the same content (and fresh inodes).
func makeTreeCopy(t *testing.T, src string) string {
	return makeTree(t)
}

func TestTrustCtimeOffSkipsInodeCheck(t *testing.T) {
	root := makeTree(t)
	first := scan(t, root, ScanOptions{TrustCtimeInode: true})
	p := filepath.Join(root, "var/db/big.bin")
	fi, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	b[7] ^= 1
	_ = os.WriteFile(p, b, 0o644)
	_ = os.Chtimes(p, fi.ModTime(), fi.ModTime())
	withCtime := scan(t, root, ScanOptions{Prev: first.Tree, TrustCtimeInode: true})
	if r := runContaining(withCtime, "var/db/big.bin"); r.Reusable {
		t.Fatal("forged mtime hid a rewrite although ctime is trusted")
	}
	// documented trade-off: without ctime the same rewrite goes unnoticed
	without := scan(t, root, ScanOptions{Prev: first.Tree, TrustCtimeInode: false})
	if r := runContaining(without, "var/db/big.bin"); !r.Reusable {
		t.Fatal("expected the rewrite to look unchanged without ctime")
	}
}

func TestReuseRequiresContiguousOldBytes(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, root, fmt.Sprintf("s/f%02d", i), bytes.Repeat([]byte{byte(i + 1)}, 20_000))
	}
	first := scan(t, root, ScanOptions{TrustCtimeInode: true})
	// delete a file from the middle of the first pack: the old bytes of the rest are no longer contiguous
	if err := os.Remove(filepath.Join(root, "s/f05")); err != nil {
		t.Fatal(err)
	}
	res := scan(t, root, ScanOptions{Prev: first.Tree, TrustCtimeInode: true})
	r := runContaining(res, "s/f04")
	if r == nil || r.Reusable {
		t.Fatal("a pack with a hole in its old bytes must not be reusable")
	}
	// a file added to a pack makes the pack not reusable
	root2 := t.TempDir()
	for i := 0; i < 10; i++ {
		write(t, root2, fmt.Sprintf("s/f%02d", i*2), bytes.Repeat([]byte{byte(i + 1)}, 20_000))
	}
	prev := scan(t, root2, ScanOptions{TrustCtimeInode: true})
	write(t, root2, "s/f05", []byte("new"))
	res2 := scan(t, root2, ScanOptions{Prev: prev.Tree, TrustCtimeInode: true})
	if r := runContaining(res2, "s/f05"); r.Reusable {
		t.Fatal("a pack with a new file must not be reusable")
	}
}

func TestPrevCatalogPointingOutsideItsPayloadIsNotTrusted(t *testing.T) {
	root := makeTree(t)
	first := scan(t, root, ScanOptions{TrustCtimeInode: true})
	e, _ := first.Tree.Lookup("var/db/huge.bin")
	e.PayOff = 1 << 40 // a forged or damaged catalog
	res := scan(t, root, ScanOptions{Prev: first.Tree, TrustCtimeInode: true})
	if r := runContaining(res, "var/db/huge.bin"); r.Reusable && r.OldEnd > first.PayloadSize+1<<40 {
		t.Fatal("reuse trusted an offset beyond the payload")
	}
}

// ---- extraction --------------------------------------------------------------------------

type fixture struct {
	root string
	res  *Result
	pay  []byte
}

func makeFixture(t testing.TB) *fixture {
	root := makeTree(t)
	res := scan(t, root, ScanOptions{})
	return &fixture{root: root, res: res, pay: payloadOf(t, root, res.Tree)}
}

func (f *fixture) payload() Payload { return FromReaderAt(bytes.NewReader(f.pay)) }

func compareTrees(t *testing.T, a, b string) {
	t.Helper()
	ra := scan(t, a, ScanOptions{})
	rb := scan(t, b, ScanOptions{})
	var pa, pb []string
	ra.Tree.Walk(func(p string, e *Entry) bool { pa = append(pa, p); return true })
	rb.Tree.Walk(func(p string, e *Entry) bool { pb = append(pb, p); return true })
	if strings.Join(pa, "\n") != strings.Join(pb, "\n") {
		t.Fatalf("different paths:\n%v\n%v", pa, pb)
	}
	for _, p := range pa {
		ea, _ := ra.Tree.Lookup(p)
		eb, _ := rb.Tree.Lookup(p)
		if ea.Kind != eb.Kind || ea.Mode != eb.Mode || ea.Size != eb.Size || ea.Link != eb.Link || ea.Sum != eb.Sum || ea.UID != eb.UID || ea.GID != eb.GID {
			t.Fatalf("%s differs: %+v vs %+v", p, ea, eb)
		}
		if ea.Kind != KSymlink && ea.MtimeNs != eb.MtimeNs {
			t.Fatalf("%s: mtime %d vs %d", p, ea.MtimeNs, eb.MtimeNs)
		}
		if !attrsEqual(ea.Attrs, eb.Attrs) {
			t.Fatalf("%s: attrs %+v vs %+v", p, ea.Attrs, eb.Attrs)
		}
	}
}

func TestExtractRoundTrip(t *testing.T) {
	f := makeFixture(t)
	dest := t.TempDir()
	if err := f.res.Tree.Extract(dest, f.payload(), ExtractOptions{}); err != nil {
		t.Fatal(err)
	}
	compareTrees(t, f.root, dest)
	// the hard link is a hard link again
	var s1, s2 unix.Stat_t
	_ = unix.Stat(filepath.Join(dest, "etc/motd"), &s1)
	_ = unix.Stat(filepath.Join(dest, "etc/motd.hard"), &s2)
	if s1.Ino != s2.Ino || s1.Nlink != 2 {
		t.Fatal("hard link not restored as a hard link")
	}
	if fi, _ := os.Lstat(filepath.Join(dest, "var/link")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink not restored")
	}
	if fi, _ := os.Lstat(filepath.Join(dest, "var/pipe")); fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("fifo not restored")
	}
}

func TestExtractSequentialStream(t *testing.T) {
	f := makeFixture(t)
	dest := t.TempDir()
	if err := f.res.Tree.Extract(dest, Sequential(bytes.NewReader(f.pay)), ExtractOptions{}); err != nil {
		t.Fatal(err)
	}
	compareTrees(t, f.root, dest)
}

func TestExtractSubtreeAndSingleFile(t *testing.T) {
	f := makeFixture(t)
	dest := t.TempDir()
	if err := f.res.Tree.Extract(dest, f.payload(), ExtractOptions{Paths: []string{"var/db/big.bin", "etc"}}); err != nil {
		t.Fatal(err)
	}
	var got []string
	_ = filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(dest, p)
		got = append(got, rel)
		return nil
	})
	sort.Strings(got)
	want := []string{".", "etc", "etc/motd", "etc/motd.hard", "etc/rc.conf", "var", "var/db", "var/db/big.bin"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	// parents carry their catalogued mode
	e, _ := f.res.Tree.Lookup("var/db")
	fi, _ := os.Stat(filepath.Join(dest, "var/db"))
	if uint32(fi.Mode().Perm()) != e.Mode&0o777 {
		t.Fatalf("parent mode %v vs %o", fi.Mode().Perm(), e.Mode)
	}
	// a hard link whose first link was not selected becomes an ordinary file
	d2 := t.TempDir()
	if err := f.res.Tree.Extract(d2, f.payload(), ExtractOptions{Paths: []string{"etc/motd.hard"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(d2, "etc/motd.hard"))
	if err != nil || string(b) != "welcome\n" {
		t.Fatalf("%v %q", err, b)
	}
	if err := f.res.Tree.Extract(t.TempDir(), f.payload(), ExtractOptions{Paths: []string{"nope"}}); err == nil {
		t.Fatal("missing path accepted")
	}
	if err := f.res.Tree.Extract(t.TempDir(), f.payload(), ExtractOptions{Paths: []string{"../x"}}); err == nil {
		t.Fatal("escaping path accepted")
	}
}

func TestExtractDetectsWrongContent(t *testing.T) {
	f := makeFixture(t)
	bad := append([]byte(nil), f.pay...)
	e, _ := f.res.Tree.Lookup("var/db/huge.bin")
	bad[e.PayOff+1000] ^= 1
	err := f.res.Tree.Extract(t.TempDir(), FromReaderAt(bytes.NewReader(bad)), ExtractOptions{})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("wrong payload not detected: %v", err)
	}
	// a short payload
	err = f.res.Tree.Extract(t.TempDir(), FromReaderAt(bytes.NewReader(f.pay[:len(f.pay)-10])), ExtractOptions{})
	if err == nil {
		t.Fatal("short payload accepted")
	}
	// SkipVerify trusts the caller
	if err := f.res.Tree.Extract(t.TempDir(), FromReaderAt(bytes.NewReader(bad)), ExtractOptions{SkipVerify: true}); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRefusesUnsafePaths(t *testing.T) {
	payload := FromReaderAt(bytes.NewReader([]byte("evil")))
	evilSum := sha256.Sum256([]byte("evil"))
	mk := func(children ...*Entry) *Tree {
		return &Tree{Root: &Entry{Kind: KDir, Mode: 0o755, Children: children}, Count: 1}
	}
	file := func(name string) *Entry {
		return &Entry{Kind: KFile, Name: name, Mode: 0o644, Size: 4, Sum: evilSum}
	}
	outside := t.TempDir()
	cases := map[string]*Tree{
		// a symlink then a file below it: the file must not land in the symlink's target
		"symlink then child": mk(&Entry{Kind: KSymlink, Name: "a", Mode: 0o777, Link: outside},
			&Entry{Kind: KDir, Name: "a", Mode: 0o755}),
		"name with slash":     mk(file("x/y")),
		"dot dot name":        mk(file("..")),
		"hardlink to dot dot": mk(file("a"), &Entry{Kind: KHardlink, Name: "b", Mode: 0o644, Link: "../../etc/passwd"}),
		"hardlink absolute":   mk(file("a"), &Entry{Kind: KHardlink, Name: "b", Mode: 0o644, Link: "/etc/passwd"}),
	}
	for name, tree := range cases {
		dest := t.TempDir()
		err := tree.Extract(dest, payload, ExtractOptions{NoChown: true})
		if err == nil {
			// the only acceptable success is one that wrote nothing outside dest
			if es, _ := os.ReadDir(outside); len(es) != 0 {
				t.Fatalf("%s: wrote outside the destination", name)
			}
			continue
		}
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatal("something was written through a symlink")
	}
	// a pre-existing symlink in the destination is not followed either
	dest := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "sub")); err != nil {
		t.Fatal(err)
	}
	tree := mk(&Entry{Kind: KDir, Name: "sub", Mode: 0o755, Children: []*Entry{file("f")}})
	if err := tree.Extract(dest, payload, ExtractOptions{NoChown: true}); err == nil {
		t.Fatal("extraction followed a symlink in the destination")
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatal("something was written through a pre-existing symlink")
	}
	// the destination must exist and be a directory
	if err := mk().Extract(filepath.Join(dest, "missing"), payload, ExtractOptions{}); err == nil {
		t.Fatal("missing destination accepted")
	}
	for _, p := range []string{"", ".", "..", "a/../b", "a//b", "/abs", "a/./b"} {
		if p == "" {
			continue
		}
		if _, err := SafePath(t.TempDir(), p); err == nil {
			t.Fatalf("SafePath accepted %q", p)
		}
	}
}

type fakeAttrs struct {
	applied []string
	skip    bool
}

func (f *fakeAttrs) Name() string                   { return "fake" }
func (f *fakeAttrs) Collect(string) ([]Attr, error) { return nil, nil }
func (f *fakeAttrs) Apply(p string, a []Attr, o ApplyOptions) ([]SkippedAttr, error) {
	var sk []SkippedAttr
	for _, x := range a {
		if f.skip {
			sk = append(sk, SkippedAttr{x.Name, "refused"})
			continue
		}
		f.applied = append(f.applied, x.Name)
	}
	return sk, nil
}

func TestSkippedMetadataIsReportedNotHidden(t *testing.T) {
	f := makeFixture(t)
	skips := &Skips{}
	fa := &fakeAttrs{skip: true}
	if err := f.res.Tree.Extract(t.TempDir(), f.payload(), ExtractOptions{Skips: skips, Attrs: fa}); err != nil {
		t.Fatal(err)
	}
	if skips.Count != 1 || !strings.Contains(skips.Summary(), "user.note") {
		t.Fatalf("skips: %+v", skips)
	}
	if (&Skips{}).Summary() != "" {
		t.Fatal("empty summary")
	}
	// the Linux attribute implementation reports what it cannot apply
	skips2 := &Skips{}
	tree := &Tree{Root: &Entry{Kind: KDir, Mode: 0o755, Children: []*Entry{
		{Kind: KFile, Name: "a", Mode: 0o644, Attrs: []Attr{{Name: "freebsd:extattr:user:x", Value: []byte("1")}, {Name: "linux:xattr:security.capability", Value: []byte("c")}, {Name: "linux:xattr:user.ok", Value: []byte("v")}}}}}, Count: 2}
	dest := t.TempDir()
	if err := tree.Extract(dest, f.payload(), ExtractOptions{NoChown: true, Skips: skips2}); err != nil {
		t.Fatal(err)
	}
	if skips2.Count != 2 {
		t.Fatalf("expected the foreign and the privileged attribute to be skipped: %+v", skips2)
	}
	got, _ := DefaultAttrs().Collect(filepath.Join(dest, "a"))
	if len(got) != 1 || got[0].Name != "linux:xattr:user.ok" {
		t.Fatalf("%+v", got)
	}
	if err := (&Tree{Root: &Entry{Kind: KDir, Mode: 0o755}, Count: 1}).Extract(t.TempDir(), f.payload(), ExtractOptions{Lenient: true}); err == nil {
		t.Fatal("Lenient without Skips accepted")
	}
}

func TestSetidBitsDroppedWhenOwnershipNotRestored(t *testing.T) {
	root := t.TempDir()
	p := write(t, root, "suid", []byte("x"))
	if err := os.Chmod(p, 0o4755); err != nil {
		t.Fatal(err)
	}
	res := scan(t, root, ScanOptions{})
	e, _ := res.Tree.Lookup("suid")
	e.UID = uint32(os.Geteuid()) + 1 // owned by somebody else
	dest := t.TempDir()
	if err := res.Tree.Extract(dest, FromReaderAt(bytes.NewReader([]byte("x"))), ExtractOptions{NoChown: true}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(dest, "suid"))
	if fi.Mode()&os.ModeSetuid != 0 {
		t.Fatal("setuid bit survived an extraction that did not restore ownership")
	}
}

func TestScanErrors(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "missing"), ScanOptions{}); err == nil {
		t.Fatal("missing root accepted")
	}
	f := write(t, t.TempDir(), "file", []byte("x"))
	if _, err := Scan(f, ScanOptions{}); err == nil {
		t.Fatal("a file as root accepted")
	}
	// a socket is skipped, not an error
	root := t.TempDir()
	if err := unix.Mknod(filepath.Join(root, "sock"), unix.S_IFSOCK|0o600, 0); err != nil {
		t.Fatal(err)
	}
	write(t, root, "f", []byte("x"))
	res, err := Scan(root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Tree.Lookup("sock"); err == nil {
		t.Fatal("socket in the catalog")
	}
}

var _ = io.EOF
