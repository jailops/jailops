package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/internal/repo/store"
)

var bg = context.Background()

// ---- fixtures ------------------------------------------------------------------------

type backendCase struct {
	name string
	mk   func(t testing.TB) datastore.Backend
}

var backendCases = []backendCase{
	{"mem", func(t testing.TB) datastore.Backend { return datastore.NewMemBackend() }},
	{"dir", func(t testing.TB) datastore.Backend {
		b, err := datastore.NewDirBackend(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		b.NoSync = true
		return b
	}},
}

type env struct {
	t     testing.TB
	b     datastore.Backend
	s     *store.Store
	key   *keys.Key
	repo  StoreRepo
	reads *readCounter
	now   time.Time
}

type readCounter struct {
	mu    sync.Mutex
	paths map[string]int
	bytes atomic.Int64
}

func (r *readCounter) open(p string) (io.ReadCloser, error) {
	r.mu.Lock()
	r.paths[p]++
	r.mu.Unlock()
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	return &countedFile{f: f, n: &r.bytes}, nil
}

func (r *readCounter) reset() {
	r.mu.Lock()
	r.paths = map[string]int{}
	r.mu.Unlock()
	r.bytes.Store(0)
}

func (r *readCounter) opened(p string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paths[p]
}

func (r *readCounter) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

type countedFile struct {
	f *os.File
	n *atomic.Int64
}

func (c *countedFile) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.n.Add(int64(n))
	return n, err
}
func (c *countedFile) Close() error { return c.f.Close() }

func testKey(t testing.TB, seed byte) *keys.Key {
	k, err := keys.NewKey(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newEnv(t testing.TB, bc backendCase) *env {
	t.Helper()
	b := bc.mk(t)
	return newEnvOn(t, b)
}

func newEnvOn(t testing.TB, b datastore.Backend) *env {
	t.Helper()
	key := testKey(t, 0x5a)
	s, err := store.Init(bg, b, store.InitOptions{ServerID: "node", KeyDomain: key.DomainID()})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, b: b, s: s, key: key, repo: StoreRepo{s}, reads: &readCounter{paths: map[string]int{}},
		now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
}

func (e *env) opts(streams ...Stream) BackupOptions {
	return BackupOptions{
		Key: e.key, Escrow: e.s, Host: "host-1", Jail: "jail-1", JailName: "web1", Policy: "nightly",
		Streams: streams, Repo: e.repo, NewUploader: StoreUploader(e.s),
		Now: func() time.Time { e.now = e.now.Add(time.Second); return e.now }, OpenFile: e.reads.open,
	}
}

func (e *env) backup(o BackupOptions) *BackupResult {
	e.t.Helper()
	e.reads.reset()
	res, err := Backup(bg, o)
	if err != nil {
		e.t.Fatalf("backup: %v", err)
	}
	return res
}

func (e *env) group() string { return e.key.Keys.GroupID("host-1", "jail-1") }

func (e *env) chunkKeys() map[string]bool {
	out := map[string]bool{}
	_ = e.b.List(bg, "chunks/", func(o datastore.ObjectInfo) error { out[o.Key] = true; return nil })
	return out
}

func (e *env) restore(snap, stream, dest string, mut func(*RestoreOptions)) error {
	o := RestoreOptions{Key: e.key, Repo: e.repo, Group: e.group(), Snapshot: snap, Stream: stream, Dest: dest, NoChown: os.Geteuid() != 0}
	if mut != nil {
		mut(&o)
	}
	_, err := Restore(bg, o)
	return err
}

func rnd(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func wr(t testing.TB, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// genTree builds a tree of about 14 MiB: large files, packs of small files,
// links, a fifo, extended attributes and a sparse (all zero) file.
func genTree(t testing.TB) string {
	root := t.TempDir()
	wr(t, root, "db/main.dat", rnd(1, 5<<20))
	wr(t, root, "db/index.dat", rnd(2, 3<<20))
	wr(t, root, "db/wal.log", append(bytes.Repeat([]byte("log line\n"), 40000), rnd(3, 300<<10)...))
	wr(t, root, "db/zeros.img", make([]byte, 6<<20))
	for i := 0; i < 600; i++ {
		wr(t, root, fmt.Sprintf("app/src/f%03d.go", i), rnd(int64(100+i), 1000+(i%50)*400))
	}
	wr(t, root, "etc/rc.conf", []byte("hostname=web1\n"))
	wr(t, root, "etc/empty", nil)
	_ = os.Symlink("../etc/rc.conf", filepath.Join(root, "app/link"))
	_ = os.Symlink("/definitely/outside", filepath.Join(root, "app/outside"))
	_ = os.Link(filepath.Join(root, "etc/rc.conf"), filepath.Join(root, "etc/rc.conf.hard"))
	_ = unix.Mkfifo(filepath.Join(root, "etc/fifo"), 0o600)
	if err := unix.Setxattr(filepath.Join(root, "etc/rc.conf"), "user.owner", []byte("ops"), 0); err != nil {
		t.Fatalf("needs user xattrs on the temp directory: %v", err)
	}
	return root
}

type fileInfo struct {
	kind   string
	mode   os.FileMode
	size   int64
	mtime  int64
	sum    [32]byte
	link   string
	xattr  string
	inoKey string
}

// snapshotOf describes a tree for comparison: every entry with its content hash.
func snapshotOf(t testing.TB, root string) map[string]fileInfo {
	t.Helper()
	out := map[string]fileInfo{}
	inodes := map[uint64]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		fo := fileInfo{mode: fi.Mode(), size: fi.Size()}
		if fi.Mode()&os.ModeSymlink == 0 {
			fo.mtime = fi.ModTime().UnixNano()
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			fo.kind = "symlink"
			fo.link, _ = os.Readlink(p)
		case fi.IsDir():
			fo.kind = "dir"
			fo.size = 0
		case fi.Mode().IsRegular():
			fo.kind = "file"
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fo.sum = sha256.Sum256(b)
			var st unix.Stat_t
			_ = unix.Lstat(p, &st)
			if st.Nlink > 1 {
				if first, ok := inodes[st.Ino]; ok {
					fo.inoKey = "hardlink-of-" + first
				} else {
					inodes[st.Ino] = rel
					fo.inoKey = "first"
				}
			}
			buf := make([]byte, 256)
			if n, err := unix.Lgetxattr(p, "user.owner", buf); err == nil {
				fo.xattr = string(buf[:n])
			}
		default:
			fo.kind = "special"
		}
		if rel == "." {
			rel = ""
		}
		out[rel] = fo
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTrees(t testing.TB, want, got map[string]fileInfo) {
	t.Helper()
	var names []string
	for k := range want {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		w, ok := want[k]
		if !ok {
			continue
		}
		g, ok := got[k]
		if !ok {
			t.Fatalf("%q missing from the restored tree", k)
		}
		if k == "" {
			continue // the root's own mtime/mode are not part of the comparison here
		}
		if w.kind != g.kind || w.mode != g.mode || w.size != g.size || w.sum != g.sum || w.link != g.link || w.xattr != g.xattr || w.inoKey != g.inoKey {
			t.Fatalf("%q differs:\n want %+v\n  got %+v", k, w, g)
		}
		if w.kind != "dir" && w.mtime != g.mtime {
			t.Fatalf("%q: mtime %d, want %d", k, g.mtime, w.mtime)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Fatalf("%q appeared in the restored tree", k)
		}
	}
}

// ---- tests -------------------------------------------------------------------------------

func TestBackupRestoreEveryByte(t *testing.T) {
	for _, bc := range backendCases {
		t.Run(bc.name, func(t *testing.T) {
			e := newEnv(t, bc)
			src := genTree(t)
			want := snapshotOf(t, src)
			res := e.backup(e.opts(Stream{Name: "tank/jails/web1", Root: src}))
			if res.Receipt == nil || res.NewChunks == 0 || !res.Full {
				t.Fatalf("%+v", res)
			}
			if len(res.Warnings) != 1 || res.KeyStatus != keys.StatusPresent {
				t.Fatalf("a not-escrowed key must warn: %+v", res)
			}
			// the sparse file costs nothing: zero entries, no blobs
			if res.UploadedBytes+5<<20 > res.LogicalBytes {
				t.Fatalf("uploaded %d bytes of %d logical: the 6 MiB of zeros must cost nothing", res.UploadedBytes, res.LogicalBytes)
			}
			dest := t.TempDir()
			if err := e.restore("", "", dest, nil); err != nil {
				t.Fatal(err)
			}
			sameTrees(t, want, snapshotOf(t, dest))
			// the symlink was stored as a link, not followed
			if l, _ := os.Readlink(filepath.Join(dest, "app/outside")); l != "/definitely/outside" {
				t.Fatalf("symlink target %q", l)
			}
		})
	}
}

func TestIncrementalUploadsOnlyChangedChunksAndReadsOnlyChangedFiles(t *testing.T) {
	for _, bc := range backendCases {
		t.Run(bc.name, func(t *testing.T) {
			e := newEnv(t, bc)
			src := genTree(t)
			stream := Stream{Name: "data", Root: src}
			first := e.backup(e.opts(stream))
			stateOne := snapshotOf(t, src)
			chunksOne := e.chunkKeys()

			// change a few files: append to one large file, rewrite a small one, add one, delete one
			f, _ := os.OpenFile(filepath.Join(src, "db/wal.log"), os.O_APPEND|os.O_WRONLY, 0)
			_, _ = f.Write([]byte("new log entries\n"))
			f.Close()
			wr(t, src, "app/src/f010.go", rnd(999, 5000))
			wr(t, src, "app/src/new.go", []byte("package new\n"))
			_ = os.Remove(filepath.Join(src, "app/src/f377.go"))
			stateTwo := snapshotOf(t, src)

			second := e.backup(e.opts(stream))
			if second.Full {
				t.Fatalf("the second run read everything: %+v", second.Streams)
			}
			if second.Previous != first.Snapshot {
				t.Fatalf("base %s, want %s", second.Previous, first.Snapshot)
			}
			// the files that did not change were not read: not even opened
			for _, rel := range []string{"db/main.dat", "db/index.dat", "db/zeros.img", "etc/rc.conf"} {
				if n := e.reads.opened(filepath.Join(src, rel)); n != 0 {
					t.Fatalf("%s was opened %d times although it did not change", rel, n)
				}
			}
			if got := e.reads.bytes.Load(); got > 4<<20 {
				t.Fatalf("read %d bytes for a change of a few KiB (the wal.log is %d)", got, 400<<10)
			}
			// small files travel in packs: only the packs holding a changed file are read again
			smallOpened := 0
			for p := range e.reads.paths {
				if strings.Contains(p, "app/src/") {
					smallOpened++
				}
			}
			if smallOpened == 0 || smallOpened > 200 {
				t.Fatalf("%d of 600 small files were read for 3 changed ones", smallOpened)
			}
			// the changed ones were
			if e.reads.opened(filepath.Join(src, "db/wal.log")) != 1 || e.reads.opened(filepath.Join(src, "app/src/f010.go")) != 1 {
				t.Fatalf("changed files not read exactly once: %v", e.reads.paths)
			}
			// only changed chunks were uploaded: the new objects are few and small
			after := e.chunkKeys()
			newKeys := 0
			for k := range after {
				if !chunksOne[k] {
					newKeys++
				}
			}
			if newKeys != second.NewChunks {
				t.Fatalf("%d new chunk objects, the result says %d", newKeys, second.NewChunks)
			}
			if second.NewChunks > 12 || second.NewChunks >= first.NewChunks/2 {
				t.Fatalf("second backup uploaded %d chunks (first: %d) for a tiny change", second.NewChunks, first.NewChunks)
			}
			if second.UploadedBytes > 1<<20+first.UploadedBytes/8 {
				t.Fatalf("uploaded %d bytes", second.UploadedBytes)
			}
			if second.ReusedBytes < 8<<20 {
				t.Fatalf("only %d bytes reused", second.ReusedBytes)
			}
			// both snapshots restore to exactly their own state
			d1, d2 := t.TempDir(), t.TempDir()
			if err := e.restore(first.Snapshot, "data", d1, nil); err != nil {
				t.Fatal(err)
			}
			sameTrees(t, stateOne, snapshotOf(t, d1))
			if err := e.restore(second.Snapshot, "data", d2, nil); err != nil {
				t.Fatal(err)
			}
			sameTrees(t, stateTwo, snapshotOf(t, d2))
			// an unchanged third run uploads nothing but possibly catalog chunks, reads nothing
			third := e.backup(e.opts(stream))
			if e.reads.total() != 0 || third.ReadBytes != 0 || third.Full {
				t.Fatalf("unchanged tree: %d files opened, %d bytes read", e.reads.total(), third.ReadBytes)
			}
		})
	}
}

func TestForcedFullReadStillDeduplicates(t *testing.T) {
	e := newEnv(t, backendCases[0])
	src := genTree(t)
	stream := Stream{Name: "data", Root: src}
	first := e.backup(e.opts(stream))
	o := e.opts(stream)
	o.ForceFull = true
	second := e.backup(o)
	if !second.Full || second.Streams[0].WhyFull == "" {
		t.Fatalf("%+v", second.Streams)
	}
	if e.reads.opened(filepath.Join(src, "db/main.dat")) != 1 {
		t.Fatal("a forced full read did not read every file")
	}
	if second.ReadBytes < first.ReadBytes/2 {
		t.Fatalf("read only %d bytes", second.ReadBytes)
	}
	if second.NewChunks > 3 { // only the catalog (new mtimes of directories) may differ
		t.Fatalf("a full read of unchanged data uploaded %d new chunks", second.NewChunks)
	}
	// the clock moving past FullEvery forces a full read too
	o = e.opts(stream)
	later := e.now.Add(8 * 24 * time.Hour)
	o.Now = func() time.Time { later = later.Add(time.Second); return later }
	third := e.backup(o)
	if !third.Full || !strings.Contains(third.Streams[0].WhyFull, "older") {
		t.Fatalf("%+v", third.Streams)
	}
	// a clock in the past (the last full read is in the future) is not trusted either
	o = e.opts(stream)
	earlier := e.now.Add(-30 * 24 * time.Hour)
	o.Now = func() time.Time { earlier = earlier.Add(time.Second); return earlier }
	if fourth := e.backup(o); !fourth.Full {
		t.Fatal("a snapshot dated before its base must read everything")
	}
}

func TestFailedPreviousSnapshotIsNeverABase(t *testing.T) {
	e := newEnv(t, backendCases[0])
	src := genTree(t)
	stream := Stream{Name: "data", Root: src}
	first := e.backup(e.opts(stream))
	if err := e.s.SetVerify(bg, first.Group, first.Snapshot, store.VerifyRecord{Failed: true, Mode: "deep"}); err != nil {
		t.Fatal(err)
	}
	second := e.backup(e.opts(stream))
	if !second.Full || second.Previous != "" || !strings.Contains(second.Streams[0].WhyFull, "failed verification") {
		t.Fatalf("%+v", second)
	}
}

func TestReusedChunkMissingFallsBackToFullRead(t *testing.T) {
	m := datastore.NewMemBackend()
	e := newEnvOn(t, m)
	src := genTree(t)
	stream := Stream{Name: "data", Root: src}
	first := e.backup(e.opts(stream))
	// remove the chunks of one large unchanged file from the repository
	snap, err := OpenSnapshot(bg, e.repo, e.key.Keys, first.Group, first.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := snap.LoadCatalog(bg, &snap.Manifest.Streams[0], 0, 0)
	ent, _ := tree.Lookup("db/index.dat")
	sub, err := snap.Index.Sub(ent.PayOff, ent.PayOff+ent.Size)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range sub.Entries {
		_ = m.Delete(bg, store.ChunkKey(en.ID))
	}
	second := e.backup(e.opts(stream))
	if !second.Full || len(second.Warnings) < 2 {
		t.Fatalf("expected the automatic full read: %+v", second)
	}
	dest := t.TempDir()
	if err := e.restore(second.Snapshot, "data", dest, nil); err != nil {
		t.Fatal(err)
	}
	sameTrees(t, snapshotOf(t, src), snapshotOf(t, dest))
}

func TestMultipleStreams(t *testing.T) {
	e := newEnv(t, backendCases[1])
	a, b := genTree(t), t.TempDir()
	wr(t, b, "x/y/z.txt", []byte("second dataset"))
	wr(t, b, "big", rnd(77, 1<<20))
	streams := []Stream{{Name: "tank/jails/web1", Root: a, ZFSSnapshot: "jo-1", GUID: 42, CreateTxg: 7, Props: map[string]string{"recordsize": "128K"}}, {Name: "tank/jails/web1/data", Root: b}}
	r1 := e.backup(e.opts(streams...))
	r2 := e.backup(e.opts(streams...))
	if r2.Full || len(r2.Streams) != 2 || r2.ReadBytes != 0 {
		t.Fatalf("%+v", r2)
	}
	snap, err := OpenSnapshot(bg, e.repo, e.key.Keys, r2.Group, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Manifest.Streams) != 2 || snap.Manifest.Streams[0].GUID != 42 || snap.Manifest.Streams[0].ZFSSnapshot != "jo-1" || snap.Manifest.Streams[0].Props["recordsize"] != "128K" {
		t.Fatalf("%+v", snap.Manifest.Streams)
	}
	if snap.Manifest.PreviousID != r1.Snapshot || snap.Manifest.Jail != "jail-1" {
		t.Fatalf("%+v", snap.Manifest)
	}
	d1, d2 := t.TempDir(), t.TempDir()
	if _, err := Restore(bg, RestoreOptions{Key: e.key, Repo: e.repo, Group: r2.Group, NoChown: os.Geteuid() != 0,
		Dests: map[string]string{"tank/jails/web1": d1, "tank/jails/web1/data": d2}}); err != nil {
		t.Fatal(err)
	}
	sameTrees(t, snapshotOf(t, a), snapshotOf(t, d1))
	sameTrees(t, snapshotOf(t, b), snapshotOf(t, d2))
	if err := e.restore("", "", t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "name the one") {
		t.Fatalf("restoring without naming one of two streams: %v", err)
	}
	if err := e.restore("", "nope", t.TempDir(), nil); err == nil {
		t.Fatal("unknown stream accepted")
	}
}

func TestSingleFileAndSubtreeRestoreFetchOnlyNeededChunks(t *testing.T) {
	e := newEnv(t, backendCases[0])
	src := genTree(t)
	res := e.backup(e.opts(Stream{Name: "data", Root: src}))
	cr := &countingRepo{Repository: e.repo}
	snap, err := OpenSnapshot(bg, cr, e.key.Keys, res.Group, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := snap.OpenView(bg, "data", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	catalogFetches := cr.n.Load()
	totalChunks := len(snap.Index.ChunkIDs())
	if int(catalogFetches) >= totalChunks/2 {
		t.Fatalf("opening the view fetched %d of %d chunks", catalogFetches, totalChunks)
	}
	var buf bytes.Buffer
	if err := v.Cat(&buf, "etc/rc.conf"); err != nil || buf.String() != "hostname=web1\n" {
		t.Fatalf("%v %q", err, buf.String())
	}
	if err := v.Cat(&buf, "etc/rc.conf.hard"); err != nil {
		t.Fatalf("cat through a hard link: %v", err)
	}
	if err := v.Cat(io.Discard, "app"); err == nil {
		t.Fatal("cat of a directory accepted")
	}
	if got := cr.n.Load() - catalogFetches; got > 2 {
		t.Fatalf("reading one 14 byte file fetched %d chunks", got)
	}
	// subtree restore through the options
	dest := t.TempDir()
	cr.n.Store(0)
	if err := e.restoreVia(cr, dest, []string{"db/index.dat", "etc"}); err != nil {
		t.Fatal(err)
	}
	if cr.n.Load() >= int64(totalChunks)-3 {
		t.Fatalf("a two path restore fetched %d of %d chunks", cr.n.Load(), totalChunks)
	}
	want := snapshotOf(t, src)
	got := snapshotOf(t, dest)
	for _, p := range []string{"db/index.dat", "etc/rc.conf", "etc/empty"} {
		if want[p].sum != got[p].sum {
			t.Fatalf("%s differs", p)
		}
	}
	if _, ok := got["db/main.dat"]; ok {
		t.Fatal("an unselected file was restored")
	}
}

func (e *env) restoreVia(r Repository, dest string, paths []string) error {
	_, err := Restore(bg, RestoreOptions{Key: e.key, Repo: r, Group: e.group(), Stream: "data", Dest: dest, Paths: paths, NoChown: os.Geteuid() != 0})
	return err
}

type countingRepo struct {
	Repository
	n atomic.Int64
}

func (c *countingRepo) GetChunk(ctx context.Context, id format.ID) ([]byte, error) {
	c.n.Add(1)
	return c.Repository.GetChunk(ctx, id)
}

// ---- failure and tamper tests --------------------------------------------------------

func TestWrongKeyCannotRestoreOrBackup(t *testing.T) {
	e := newEnv(t, backendCases[0])
	src := genTree(t)
	res := e.backup(e.opts(Stream{Name: "data", Root: src}))
	wrong := testKey(t, 0x77)
	_, err := Restore(bg, RestoreOptions{Key: wrong, Repo: e.repo, Group: res.Group, Stream: "data", Dest: t.TempDir(), NoChown: true})
	if !errors.Is(err, ErrWrongKey) {
		t.Fatalf("restore with the wrong key: %v", err)
	}
	if _, err := Verify(bg, VerifyOptions{Key: wrong, Repo: e.repo, Group: res.Group}); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("verify with the wrong key: %v", err)
	}
	// the right group for the wrong key does not exist: the wrong key sees no snapshot at all
	_, err = Restore(bg, RestoreOptions{Key: wrong, Repo: e.repo, Group: wrong.Keys.GroupID("host-1", "jail-1"), Dest: t.TempDir()})
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("other group: %v", err)
	}
	// a backup with another key into this repository is refused (the repository records its domain)
	o := e.opts(Stream{Name: "data", Root: src})
	o.Key = wrong
	if _, err := Backup(bg, o); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("backup with another key domain: %v", err)
	}
	if _, err := Restore(bg, RestoreOptions{Repo: e.repo}); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
	// a repository that did not record its domain: opening the previous manifest with the wrong key is still refused
	m := datastore.NewMemBackend()
	s2, _ := store.Init(bg, m, store.InitOptions{})
	e2 := &env{t: t, b: m, s: s2, key: e.key, repo: StoreRepo{s2}, reads: e.reads, now: e.now}
	e2.backup(e2.opts(Stream{Name: "data", Root: src}))
	o = e2.opts(Stream{Name: "data", Root: src})
	o.Key = wrong
	o.Host, o.Jail = "host-1", "jail-1"
	// same group is impossible with another key; the group differs, so it is a fresh jail: allowed
	if _, err := Backup(bg, o); err != nil {
		t.Fatalf("a different key backs up a different group: %v", err)
	}
}

func TestCorruptChunkFailsDeepVerifyAndRestore(t *testing.T) {
	m := datastore.NewMemBackend()
	e := newEnvOn(t, m)
	src := genTree(t)
	res := e.backup(e.opts(Stream{Name: "data", Root: src}))
	vr, err := Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group})
	if err != nil || !vr.OK || vr.Chunks == 0 || vr.Files == 0 {
		t.Fatalf("a healthy repository must verify: %v %+v", err, vr)
	}
	keys := e.chunkKeys()
	var victim string
	for k := range keys {
		if victim == "" || k < victim {
			victim = k
		}
	}
	m.Corrupt(victim, 40)
	vr, err = Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group, Mode: VerifyDeep})
	if err != nil {
		t.Fatal(err)
	}
	if vr.OK || len(vr.Bad) != 1 || !strings.HasSuffix(victim, vr.Bad[0].ID.String()) {
		t.Fatalf("deep verify missed the corrupt chunk: %+v", vr)
	}
	// the keyless check finds it too (trailer)
	b, _ := e.s.GetChunk(bg, vr.Bad[0].ID)
	if format.VerifyBlob(b) == nil {
		t.Fatal("trailer did not catch a flipped byte")
	}
	if err := e.restore(res.Snapshot, "data", t.TempDir(), nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("restore over a corrupt chunk: %v", err)
	}
	// a missing chunk is reported too
	m2 := datastore.NewMemBackend()
	e2 := newEnvOn(t, m2)
	r2 := e2.backup(e2.opts(Stream{Name: "data", Root: src}))
	for k := range e2.chunkKeys() {
		_ = m2.Delete(bg, k)
		break
	}
	vr, _ = Verify(bg, VerifyOptions{Key: e2.key, Repo: e2.repo, Group: r2.Group})
	if vr.OK || len(vr.Bad) != 1 {
		t.Fatalf("%+v", vr)
	}
}

func TestChunkIDPoisoningIsDetectedByDeepVerify(t *testing.T) {
	m := datastore.NewMemBackend()
	e := newEnvOn(t, m)
	src := genTree(t)
	res := e.backup(e.opts(Stream{Name: "data", Root: src}))
	var victim format.ID
	for k := range e.chunkKeys() {
		id, _ := format.ParseID(k[strings.LastIndex(k, "/")+1:])
		if victim.IsZero() || id.String() < victim.String() {
			victim = id
		}
	}
	// a host holding the key plants a self-consistent blob under the victim's id
	forged, err := e.key.Keys.SealChunk(victim, []byte("attacker content"), format.EncodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	m.Overwrite(store.ChunkKey(victim), forged)
	if !e.s.ChunkHealthy(bg, victim) {
		t.Fatal("the keyless check must accept the forged blob: that is the point of this test")
	}
	vr, err := Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group})
	if err != nil {
		t.Fatal(err)
	}
	if vr.OK || len(vr.Bad) != 1 || vr.Bad[0].ID != victim || !strings.Contains(vr.Bad[0].Reason, "chunk") {
		t.Fatalf("poisoning not detected: %+v", vr)
	}
	if err := e.restore("", "data", t.TempDir(), nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("restore accepted a poisoned chunk: %v", err)
	}
	// quarantine (by the key holder) removes it; the next backup heals it by re-uploading
	if err := e.s.QuarantineKeyed(bg, victim); err != nil {
		t.Fatal(err)
	}
	o := e.opts(Stream{Name: "data", Root: src})
	o.ForceFull = true
	healed := e.backup(o)
	if healed.NewChunks != 1 {
		t.Fatalf("the next full read must re-upload exactly the quarantined chunk, uploaded %d", healed.NewChunks)
	}
	vr, _ = Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group})
	if !vr.OK {
		t.Fatalf("not healed: %+v", vr)
	}
}

func TestTamperedIndexOrManifestOrReceiptIsRefused(t *testing.T) {
	m := datastore.NewMemBackend()
	e := newEnvOn(t, m)
	src := genTree(t)
	res := e.backup(e.opts(Stream{Name: "data", Root: src}))
	prefix := store.SnapPrefix(res.Group, res.Snapshot)
	for _, file := range []string{"index", "manifest", "commit.json"} {
		t.Run(file, func(t *testing.T) {
			orig, _ := datastore.ReadAll(bg, m, prefix+file, 1<<30)
			bad := append([]byte(nil), orig...)
			bad[len(bad)/2] ^= 1
			m.Overwrite(prefix+file, bad)
			defer m.Overwrite(prefix+file, orig)
			if _, err := OpenSnapshot(bg, e.repo, e.key.Keys, res.Group, res.Snapshot); err == nil {
				t.Fatal("a tampered object was accepted")
			}
		})
	}
	// an index that is internally valid but not the one the manifest names
	other := &format.Index{Size: 5, Entries: []format.Entry{{End: 5, ID: format.ID{1}}}}
	ob, _ := other.Marshal()
	orig, _ := datastore.ReadAll(bg, m, prefix+"index", 1<<30)
	m.Overwrite(prefix+"index", ob)
	if _, err := OpenSnapshot(bg, e.repo, e.key.Keys, res.Group, res.Snapshot); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("swapped index: %v", err)
	}
	m.Overwrite(prefix+"index", orig)
	// moving a manifest to another snapshot id does not open
	mb, _ := datastore.ReadAll(bg, m, prefix+"manifest", 1<<30)
	if _, err := e.key.Keys.OpenManifest(mb, res.Group, "20200101000000-00000000"); err == nil {
		t.Fatal("manifest opened under another snapshot id")
	}
}

func TestExtractAndFullVerify(t *testing.T) {
	e := newEnv(t, backendCases[1])
	src := genTree(t)
	res := e.backup(e.opts(Stream{Name: "data", Root: src}))
	for _, mode := range []string{VerifyDeep, VerifyExtract, VerifyFull} {
		vr, err := Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group, Mode: mode, Scratch: t.TempDir()})
		if err != nil || !vr.OK {
			t.Fatalf("%s: %v %+v", mode, err, vr)
		}
		if vr.Mode != mode || vr.Files == 0 {
			t.Fatalf("%+v", vr)
		}
	}
	if _, err := Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group, Mode: "nonsense"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	// the scratch tree is removed
	scratch := t.TempDir()
	_, _ = Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: res.Group, Mode: VerifyExtract, Scratch: scratch})
	if es, _ := os.ReadDir(scratch); len(es) != 0 {
		t.Fatalf("scratch not cleaned: %v", es)
	}
	// extract verification catches a corrupt chunk too
	e2 := newEnvOn(t, datastore.NewMemBackend())
	r2 := e2.backup(e2.opts(Stream{Name: "data", Root: src}))
	mem := e2.b.(*datastore.MemBackend)
	for k := range e2.chunkKeys() {
		mem.Corrupt(k, 50)
		break
	}
	vr, err := Verify(bg, VerifyOptions{Key: e2.key, Repo: e2.repo, Group: r2.Group, Mode: VerifyExtract, Scratch: t.TempDir()})
	if err != nil || vr.OK {
		t.Fatalf("extract verify missed a corrupt chunk: %v %+v", err, vr)
	}
}

// ---- key status and escrow ----------------------------------------------------------------

func TestKeyStatusIsCheckedBeforeABackup(t *testing.T) {
	e := newEnv(t, backendCases[0])
	src := genTree(t)
	o := e.opts(Stream{Name: "data", Root: src})
	o.Key = nil
	if _, err := Backup(bg, o); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	if e.reads.total() != 0 {
		t.Fatal("files were read before the key was checked")
	}
	o = e.opts(Stream{Name: "data", Root: src})
	o.RequireEscrow = true
	if _, err := Backup(bg, o); !errors.Is(err, ErrNotEscrowed) {
		t.Fatalf("required escrow: %v", err)
	}
	if n := len(e.chunkKeys()); n != 0 {
		t.Fatalf("%d chunks uploaded before the escrow check", n)
	}
	id, rcpt, err := keys.GenerateRecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Escrow(bg, e.key, []string{rcpt}, e.s); err != nil {
		t.Fatal(err)
	}
	o = e.opts(Stream{Name: "data", Root: src})
	o.RequireEscrow = true
	res, err := Backup(bg, o)
	if err != nil || res.KeyStatus != keys.StatusEscrowed || len(res.Warnings) != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	// disaster: a new host has nothing but the repository and the offline identity
	blob, err := e.s.GetEscrow(bg, e.key.DomainID())
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := keys.OpenEscrow(blob, id)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Restore(bg, RestoreOptions{Key: recovered, Repo: e.repo, Group: res.Group, Stream: "data", Dest: dest, NoChown: os.Geteuid() != 0}); err != nil {
		t.Fatal(err)
	}
	sameTrees(t, snapshotOf(t, src), snapshotOf(t, dest))
	// and so does the printed recovery key
	rk, err := keys.FromRecoveryKey(e.key.RecoveryKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(bg, RestoreOptions{Key: rk, Repo: e.repo, Group: res.Group, Stream: "data", Dest: t.TempDir(), NoChown: os.Geteuid() != 0}); err != nil {
		t.Fatal(err)
	}
}

// ---- crash safety ----------------------------------------------------------------------------

// A crash at every backend mutation of a whole backup: the snapshot is invisible until the
// marker is written, never half there, and a restarted backup completes.
func TestCrashAtEveryStepOfABackupLeavesNoVisibleSnapshot(t *testing.T) {
	src := t.TempDir()
	wr(t, src, "a/one", rnd(1, 300<<10))
	wr(t, src, "a/two", rnd(2, 100<<10))
	wr(t, src, "b/three", []byte("three"))
	for _, bc := range backendCases {
		t.Run(bc.name, func(t *testing.T) {
			total := 0
			{
				fb := datastore.NewFaultBackend(bc.mk(t))
				e := newEnvOn(t, fb)
				fb.CrashAtCall(1 << 30)
				e.backup(e.opts(Stream{Name: "data", Root: src}))
				total = fb.Calls()
			}
			if total < 5 {
				t.Fatalf("only %d mutating calls", total)
			}
			for n := 1; n <= total; n++ {
				fb := datastore.NewFaultBackend(bc.mk(t))
				e := newEnvOn(t, fb)
				fb.CrashAtCall(n)
				_, err := Backup(bg, e.opts(Stream{Name: "data", Root: src}))
				fb.Reset()
				snaps, lerr := e.s.ListSnapshots(bg, e.group())
				if lerr != nil {
					t.Fatal(lerr)
				}
				if err != nil && len(snaps) != 0 && n < total {
					t.Fatalf("crash at call %d of %d: a snapshot is visible although the backup failed", n, total)
				}
				for _, si := range snaps {
					// a visible snapshot is complete: it opens, verifies and restores
					vr, verr := Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: e.group(), Snapshot: si.Snapshot})
					if verr != nil || !vr.OK {
						t.Fatalf("crash at call %d: visible snapshot is damaged: %v %+v", n, verr, vr)
					}
				}
				// the restarted backup completes and restores
				res, err := Backup(bg, e.opts(Stream{Name: "data", Root: src}))
				if err != nil {
					t.Fatalf("crash at call %d: retry failed: %v", n, err)
				}
				dest := t.TempDir()
				if err := e.restore(res.Snapshot, "data", dest, nil); err != nil {
					t.Fatalf("crash at call %d: %v", n, err)
				}
				sameTrees(t, snapshotOf(t, src), snapshotOf(t, dest))
			}
		})
	}
}

// ---- memory ----------------------------------------------------------------------------------

func writeBig(t testing.TB, p string, size int) {
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(int64(size)))
	buf := make([]byte, 1<<20)
	for i := 0; i < size>>20; i++ {
		if i%3 == 0 {
			clear(buf)
		} else {
			r.Read(buf)
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// The heap an operation needs must not grow with the size of the data: the same
// code on a file 3.3 times larger needs about the same memory.
func TestMemoryIsBoundedByTheWindowNotTheDataSize(t *testing.T) {
	old := debug.SetGCPercent(20) // so that the heap in use tracks what is live
	defer debug.SetGCPercent(old)
	measure := func(size int) (backup, restore uint64) {
		src := t.TempDir()
		p := filepath.Join(src, "big.bin")
		writeBig(t, p, size)
		b, err := datastore.NewDirBackend(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		b.NoSync = true
		e := newEnvOn(t, b)
		backup = measurePeak(func() { e.backup(e.opts(Stream{Name: "data", Root: src})) })
		dest := t.TempDir()
		restore = measurePeak(func() {
			if err := e.restore("", "data", dest, nil); err != nil {
				t.Fatal(err)
			}
		})
		same, err := sameFile(p, filepath.Join(dest, "big.bin"))
		if err != nil || !same {
			t.Fatalf("restored file differs: %v", err)
		}
		return
	}
	smallB, smallR := measure(48 << 20)
	bigB, bigR := measure(160 << 20)
	t.Logf("48 MiB: backup +%d MiB, restore +%d MiB; 160 MiB: backup +%d MiB, restore +%d MiB", smallB>>20, smallR>>20, bigB>>20, bigR>>20)
	if bigB > smallB*3/2+24<<20 || bigB > 120<<20 {
		t.Fatalf("backup heap grew from %d to %d MiB with the data size", smallB>>20, bigB>>20)
	}
	if bigR > smallR*3/2+24<<20 || bigR > 120<<20 {
		t.Fatalf("restore heap grew from %d to %d MiB with the data size", smallR>>20, bigR>>20)
	}
}

func sameFile(a, b string) (bool, error) {
	ha, err := fileSum(a)
	if err != nil {
		return false, err
	}
	hb, err := fileSum(b)
	return ha == hb, err
}

func fileSum(p string) ([32]byte, error) {
	var out [32]byte
	f, err := os.Open(p)
	if err != nil {
		return out, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}

// commitHook runs fn right before the real Commit.
type commitHook struct {
	Uploader
	fn func()
}

func (c *commitHook) Commit(ctx context.Context) (*format.Receipt, error) {
	if c.fn != nil {
		fn := c.fn
		c.fn = nil // only the first commit
		fn()
	}
	return c.Uploader.Commit(ctx)
}

// A chunk that disappears between the engine's check and the commit is caught by the commit's own
// check against the backend; the engine reads everything again and the second commit succeeds.
func TestChunkLostBeforeCommitIsRepaired(t *testing.T) {
	m := datastore.NewMemBackend()
	e := newEnvOn(t, m)
	src := genTree(t)
	stream := Stream{Name: "data", Root: src}
	first := e.backup(e.opts(stream))
	snap, _ := OpenSnapshot(bg, e.repo, e.key.Keys, first.Group, first.Snapshot)
	tree, _ := snap.LoadCatalog(bg, &snap.Manifest.Streams[0], 0, 0)
	ent, _ := tree.Lookup("db/main.dat")
	sub, _ := snap.Index.Sub(ent.PayOff, ent.PayOff+ent.Size)
	victim := sub.Entries[0].ID
	o := e.opts(stream)
	o.NewUploader = func(ctx context.Context, g, s, p string) (Uploader, error) {
		u, err := StoreUploader(e.s)(ctx, g, s, p)
		return &commitHook{Uploader: u, fn: func() { _ = m.Delete(bg, store.ChunkKey(victim)) }}, err
	}
	second := e.backup(o)
	if !second.Full || len(second.Warnings) < 2 {
		t.Fatalf("%+v", second)
	}
	if vr, err := Verify(bg, VerifyOptions{Key: e.key, Repo: e.repo, Group: second.Group}); err != nil || !vr.OK {
		t.Fatalf("%v %+v", err, vr)
	}
}
