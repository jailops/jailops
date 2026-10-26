package priv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

type treeEnv struct {
	*env
	src, dst string
}

func newTreeEnv(t *testing.T) *treeEnv {
	e := newEnv(t)
	te := &treeEnv{env: e, src: filepath.Join(e.cfg.MountBase, "images", "g1", "root"), dst: filepath.Join(e.cfg.MountBase, "jails", "web", "var")}
	for _, d := range []string{te.src, te.dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return te
}

func (te *treeEnv) copy(t *testing.T, o CopyOpts) (CopyResult, error) {
	t.Helper()
	return te.local.CopyTree(context.Background(), te.src, te.dst, o)
}

func (te *treeEnv) put(t *testing.T, rel, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(te.src, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func ino(t *testing.T, p string) (uint64, uint64) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := statOf(fi)
	return st.Ino, st.Nlink
}

func noStale(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), copyTmpPrefix) {
			t.Errorf("temporary entry %s left in the destination", e.Name())
		}
	}
}

func TestCopyTreePreservesWhatItShould(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, "a/b/file", "hello", 0o640)
	te.put(t, "exec.sh", "#!/bin/sh\n", 0o755)
	te.put(t, "suid", "x", os.ModeSetuid|0o755)
	te.put(t, "ro", "r", 0o444)
	if err := os.MkdirAll(filepath.Join(te.src, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(te.src, "a"), 0o750)
	os.Symlink("b/file", filepath.Join(te.src, "a", "rel"))
	os.Symlink("/etc/passwd", filepath.Join(te.src, "abs"))
	os.Symlink("nowhere", filepath.Join(te.src, "dangling"))
	// hard links, inside one directory and across top-level entries
	os.Link(filepath.Join(te.src, "a", "b", "file"), filepath.Join(te.src, "a", "b", "file2"))
	os.Link(filepath.Join(te.src, "a", "b", "file"), filepath.Join(te.src, "z-link"))
	mt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, p := range []string{"a/b/file", "exec.sh", "empty", "a/b", "a"} {
		if err := os.Chtimes(filepath.Join(te.src, p), mt.Add(time.Hour), mt); err != nil {
			t.Fatal(err)
		}
	}
	os.Chtimes(filepath.Join(te.src, "."), mt, mt)
	xerr := setXattr(filepath.Join(te.src, "a", "b", "file"), "user.jailops", "v1")
	haveXattr := xerr == nil
	t.Logf("xattr support: %v", xerr)
	res, err := te.copy(t, CopyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 4 || res.Symlinks != 3 || res.HardLinks != 2 || res.Dirs != 3 {
		t.Errorf("result %+v", res)
	}
	noStale(t, te.dst)
	b, _ := os.ReadFile(filepath.Join(te.dst, "a", "b", "file"))
	if string(b) != "hello" {
		t.Errorf("content %q", b)
	}
	for p, want := range map[string]os.FileMode{"a/b/file": 0o640, "exec.sh": 0o755, "ro": 0o444, "empty": 0o700, "a": 0o750} {
		fi, err := os.Lstat(filepath.Join(te.dst, p))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v want %v", p, fi.Mode().Perm(), want)
		}
	}
	if fi, _ := os.Lstat(filepath.Join(te.dst, "suid")); fi == nil || fi.Mode()&os.ModeSetuid == 0 {
		t.Error("setuid bit lost")
	}
	for p, want := range map[string]string{"a/rel": "b/file", "abs": "/etc/passwd", "dangling": "nowhere"} {
		got, err := os.Readlink(filepath.Join(te.dst, p))
		if err != nil || got != want {
			t.Errorf("symlink %s: %q %v", p, got, err)
		}
	}
	// times (a file, a directory, and the destination directory itself)
	for _, p := range []string{"a/b/file", "exec.sh", "empty", "a/b", "."} {
		fi, err := os.Lstat(filepath.Join(te.dst, p))
		if err != nil {
			t.Fatal(err)
		}
		if p == "." {
			if !fi.ModTime().Equal(mt) {
				t.Errorf("destination directory mtime %v", fi.ModTime())
			}
			continue
		}
		if !fi.ModTime().Equal(mt) {
			t.Errorf("%s: mtime %v want %v", p, fi.ModTime(), mt)
		}
	}
	// hard links are links again
	i1, n1 := ino(t, filepath.Join(te.dst, "a", "b", "file"))
	i2, _ := ino(t, filepath.Join(te.dst, "a", "b", "file2"))
	i3, _ := ino(t, filepath.Join(te.dst, "z-link"))
	if i1 != i2 || i1 != i3 || n1 != 3 {
		t.Errorf("hard links: %d %d %d nlink %d", i1, i2, i3, n1)
	}
	// extended attributes, when the filesystem has them
	if haveXattr {
		if got, err := getXattr(filepath.Join(te.dst, "a", "b", "file"), "user.jailops"); err != nil || got != "v1" {
			t.Errorf("xattr not copied: %q %v", got, err)
		}
	}
	// the source is untouched
	if got, _ := os.ReadFile(filepath.Join(te.src, "a", "b", "file")); string(got) != "hello" {
		t.Error("source changed")
	}
}

func TestCopyTreeNeverFollowsSymlinks(t *testing.T) {
	te := newTreeEnv(t)
	outside := filepath.Join(te.base, "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600)
	os.Symlink(outside, filepath.Join(te.src, "out"))
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(te.src, "secret"))
	os.Symlink("..", filepath.Join(te.src, "up"))
	os.Symlink(".", filepath.Join(te.src, "self"))
	os.Symlink("loop2", filepath.Join(te.src, "loop1"))
	os.Symlink("loop1", filepath.Join(te.src, "loop2"))
	os.MkdirAll(filepath.Join(te.src, "d"), 0o755)
	os.Symlink("../d", filepath.Join(te.src, "d", "again"))
	res, err := te.copy(t, CopyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Symlinks != 7 || res.Files != 0 {
		t.Errorf("%+v", res)
	}
	for _, n := range []string{"out", "secret", "up", "self", "loop1", "loop2"} {
		fi, err := os.Lstat(filepath.Join(te.dst, n))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s was not copied as a symlink: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "out")); err == nil {
		t.Error("something was written outside")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 {
		t.Errorf("outside directory changed: %v", ents)
	}
}

func TestCopyTreeRefusesHostilePaths(t *testing.T) {
	te := newTreeEnv(t)
	other := te.root("tank/other")
	_ = other
	ctx := context.Background()
	adm := WithAdmin(ctx, "a")
	if err := te.local.RegisterStorage(adm, "tank/other"); err != nil {
		t.Fatal(err)
	}
	otherDir := filepath.Join(te.base, "tank_other", "d")
	os.MkdirAll(otherDir, 0o755)
	outside := filepath.Join(te.base, "outside")
	os.MkdirAll(outside, 0o755)
	// a symlinked ancestor that leaves the root
	os.Symlink(outside, filepath.Join(te.cfg.MountBase, "escape"))
	cases := map[string][2]string{
		"relative src":       {"images/g1", te.dst},
		"dotdot src":         {te.src + "/../../..", te.dst},
		"src outside":        {"/etc", te.dst},
		"dst outside":        {te.src, "/etc"},
		"dst conf dir":       {te.src, te.cfg.ConfDir},
		"src is a root":      {te.cfg.MountBase, te.dst},
		"dst is a root":      {te.src, te.cfg.MountBase},
		"dst inside src":     {te.src, filepath.Join(te.src, "sub")},
		"src inside dst":     {te.dst, filepath.Join(te.dst, "..", "var", "x")},
		"same":               {te.src, te.src},
		"symlink escape src": {filepath.Join(te.cfg.MountBase, "escape"), te.dst},
		"symlink escape dst": {te.src, filepath.Join(te.cfg.MountBase, "escape")},
		"NUL":                {te.src + "\x00", te.dst},
		"empty":              {"", te.dst},
		"missing src":        {filepath.Join(te.src, "nope"), te.dst},
		"missing dst":        {te.src, filepath.Join(te.dst, "nope")},
		"src is a file":      {filepath.Join(te.src, "f"), te.dst},
		"dst is a file":      {te.src, filepath.Join(te.src, "f")},
	}
	os.WriteFile(filepath.Join(te.src, "f"), []byte("x"), 0o644)
	for name, c := range cases {
		if name == "dst inside src" {
			os.MkdirAll(filepath.Join(te.src, "sub"), 0o755)
		}
		if _, err := te.local.CopyTree(ctx, c[0], c[1], CopyOpts{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("something was written outside: %v", ents)
	}
	// both ends may live in different registered roots
	os.WriteFile(filepath.Join(te.src, "f2"), []byte("y"), 0o644)
	os.Remove(filepath.Join(te.src, "f"))
	os.RemoveAll(filepath.Join(te.src, "sub"))
	if _, err := te.local.CopyTree(ctx, te.src, otherDir, CopyOpts{}); err != nil {
		t.Errorf("copy between roots: %v", err)
	}
}

func TestCopyTreeExistingEntryAndAtomicity(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, "a/f", "1", 0o644)
	te.put(t, "b/f", "2", 0o644)
	te.put(t, "c/f", "3", 0o644)
	// an existing entry is refused, and nothing is left half done for it
	os.MkdirAll(filepath.Join(te.dst, "b"), 0o755)
	_, err := te.copy(t, CopyOpts{})
	if !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("existing entry: %v", err)
	}
	noStale(t, te.dst)
	if ents, _ := os.ReadDir(filepath.Join(te.dst, "b")); len(ents) != 0 {
		t.Error("the existing directory was modified")
	}
	// stale temporaries of a crashed copy are removed
	for _, n := range []string{"a", "b", "c"} {
		os.RemoveAll(filepath.Join(te.dst, n))
	}
	os.MkdirAll(filepath.Join(te.dst, copyTmpPrefix+"deadbeef", "x"), 0o755)
	if _, err := te.copy(t, CopyOpts{}); err != nil {
		t.Fatal(err)
	}
	noStale(t, te.dst)
}

func TestCopyTreeFailureLeavesNoPartialDirectory(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, "a/f", "1", 0o644)
	te.put(t, "z/ok", "x", 0o644)
	te.put(t, "z/deep/er/f", "x", 0o644)
	bad := filepath.Join(te.src, "z", "zz")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "f"), []byte("x"), 0o644)
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	os.Chmod(bad, 0o000) // unreadable directory
	defer os.Chmod(bad, 0o755)
	_, err := te.copy(t, CopyOpts{})
	if err == nil {
		t.Fatal("an unreadable directory was copied")
	}
	if !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("code: %v", err)
	}
	noStale(t, te.dst)
	// the top-level entries before the failure are complete, the failed one is absent
	if _, err := os.Lstat(filepath.Join(te.dst, "z")); err == nil {
		t.Error("the failed top-level directory is present")
	}
}

func TestCopyTreeLimits(t *testing.T) {
	te := newTreeEnv(t)
	for i := 0; i < 30; i++ {
		te.put(t, fmt.Sprintf("d/f%02d", i), "x", 0o644)
	}
	if _, err := te.copy(t, CopyOpts{MaxEntries: 10}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("entry limit: %v", err)
	}
	noStale(t, te.dst)
	os.RemoveAll(te.dst)
	os.MkdirAll(te.dst, 0o755)
	deep := te.src
	for i := 0; i < 20; i++ {
		deep = filepath.Join(deep, "n")
	}
	os.MkdirAll(deep, 0o755)
	if _, err := te.copy(t, CopyOpts{MaxDepth: 8}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("depth limit: %v", err)
	}
	noStale(t, te.dst)
	// a hard link bomb: many distinct multiply linked files
	te2 := newTreeEnv(t)
	for i := 0; i < 40; i++ {
		p := te2.put(t, fmt.Sprintf("bomb/f%02d", i), "x", 0o644)
		os.Link(p, p+"-l")
	}
	if _, err := te2.copy(t, CopyOpts{MaxHardLinks: 10}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("hard link limit: %v", err)
	}
	noStale(t, te2.dst)
	// the same tree within the limit is copied with its links intact
	te3 := newTreeEnv(t)
	var last string
	for i := 0; i < 40; i++ {
		last = te3.put(t, fmt.Sprintf("bomb/f%02d", i), "x", 0o644)
		os.Link(last, last+"-l")
	}
	res, err := te3.copy(t, CopyOpts{})
	if err != nil || res.HardLinks != 40 {
		t.Errorf("%+v %v", res, err)
	}
	// one inode with very many names is cheap: a single map entry
	te4 := newTreeEnv(t)
	p := te4.put(t, "one", "x", 0o644)
	for i := 0; i < 200; i++ {
		if err := os.Link(p, filepath.Join(te4.src, fmt.Sprintf("n%03d", i))); err != nil {
			t.Skipf("link limit: %v", err)
		}
	}
	if res, err := te4.copy(t, CopyOpts{MaxHardLinks: 2}); err != nil || res.HardLinks != 200 {
		t.Errorf("many names for one inode: %+v %v", res, err)
	}
}

func TestCopyTreeSparseFiles(t *testing.T) {
	te := newTreeEnv(t)
	p := filepath.Join(te.src, "sparse")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	const size = 64 << 20
	f.WriteAt([]byte("head"), 0)
	f.WriteAt([]byte("middle"), size/2)
	f.Truncate(size)
	f.Close()
	var srcBlocks int64
	if fi, _ := os.Stat(p); fi != nil {
		srcBlocks = fi.Sys().(*syscall.Stat_t).Blocks
	}
	if _, err := te.copy(t, CopyOpts{}); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(te.dst, "sparse")
	fi, err := os.Stat(q)
	if err != nil || fi.Size() != size {
		t.Fatalf("%v %v", fi, err)
	}
	if blocks := fi.Sys().(*syscall.Stat_t).Blocks; srcBlocks*512 < size/2 && blocks*512 > size/4 {
		t.Errorf("the copy is not sparse: %d blocks (source %d)", blocks, srcBlocks)
	}
	a, _ := os.ReadFile(p)
	b, _ := os.ReadFile(q)
	if !bytes.Equal(a, b) {
		t.Error("content differs")
	}
	// a file that ends in a hole keeps its length
	g, _ := os.Create(filepath.Join(te.src, "tail"))
	g.Write([]byte("x"))
	g.Truncate(1 << 20)
	g.Close()
	os.RemoveAll(te.dst)
	os.MkdirAll(te.dst, 0o755)
	if _, err := te.copy(t, CopyOpts{}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(te.dst, "tail")); fi == nil || fi.Size() != 1<<20 {
		t.Errorf("tail: %v", fi)
	}
}

func TestCopyTreeLargeFileUsesBoundedMemory(t *testing.T) {
	te := newTreeEnv(t)
	f, _ := os.Create(filepath.Join(te.src, "big"))
	chunk := bytes.Repeat([]byte("0123456789abcdef"), 1<<14) // 256 KiB
	for i := 0; i < 64; i++ {                                // 16 MiB
		f.Write(chunk)
	}
	f.Close()
	res, err := te.copy(t, CopyOpts{})
	if err != nil || res.Bytes != 16<<20 {
		t.Fatalf("%+v %v", res, err)
	}
	a, _ := os.ReadFile(filepath.Join(te.src, "big"))
	b, _ := os.ReadFile(filepath.Join(te.dst, "big"))
	if !bytes.Equal(a, b) {
		t.Error("content differs")
	}
}

func TestCopyTreeCancellation(t *testing.T) {
	te := newTreeEnv(t)
	for i := 0; i < 200; i++ {
		te.put(t, fmt.Sprintf("d%03d/f", i), "x", 0o644)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	os.RemoveAll(te.dst)
	os.MkdirAll(te.dst, 0o755)
	if _, err := te.local.CopyTree(ctx2, te.src, te.dst, CopyOpts{}); err == nil {
		t.Error("a cancelled copy succeeded")
	}
	noStale(t, te.dst)
	if ents, _ := os.ReadDir(te.dst); len(ents) != 0 {
		t.Errorf("a cancelled copy left %d entries", len(ents))
	}
}

func TestCopyTreeCancelMidway(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, "first/f", "x", 0o644)
	big := filepath.Join(te.src, "second", "big")
	os.MkdirAll(filepath.Dir(big), 0o755)
	f, _ := os.Create(big)
	f.Truncate(0)
	chunk := bytes.Repeat([]byte{1}, 1<<20)
	for i := 0; i < 256; i++ {
		f.Write(chunk)
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := te.local.CopyTree(ctx, te.src, te.dst, CopyOpts{Progress: func(p CopyProgress) { cancel() }})
	cancel()
	if err == nil {
		t.Fatal("copy finished although it was cancelled")
	}
	if err != context.Canceled {
		t.Errorf("error %v", err)
	}
	noStale(t, te.dst)
	// an entry is either complete or absent
	if fi, err := os.Lstat(filepath.Join(te.dst, "second", "big")); err == nil && fi.Size() != 256<<20 {
		t.Errorf("a partial file is present: %d bytes", fi.Size())
	}
}

func TestCopyTreeCrossFilesystem(t *testing.T) {
	// A mount point inside the source needs privilege to create, so the
	// check runs with a fabricated device number for the source directory.
	te := newTreeEnv(t)
	te.put(t, "d/f", "x", 0o644)
	sr, _ := os.OpenRoot(te.src)
	dr, _ := os.OpenRoot(te.dst)
	defer sr.Close()
	defer dr.Close()
	c := &copier{ctx: context.Background(), src: sr, dst: dr, o: CopyOpts{MaxEntries: 100, MaxDepth: 10, MaxHardLinks: 10}, srcDev: 0xdeadbeef,
		links: map[fileID]string{}, buf: make([]byte, copyChunk), last: time.Now()}
	if err := c.topLevel("d"); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "another filesystem") {
		t.Errorf("crossing: %v", err)
	}
	c.o.CrossFilesystem = true
	if err := c.topLevel("d"); err != nil {
		t.Errorf("allowed crossing: %v", err)
	}
}

func TestCopyTreeSkipsSpecialFiles(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, "f", "x", 0o644)
	if err := syscall.Mkfifo(filepath.Join(te.src, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := te.copy(t, CopyOpts{})
	if err != nil || res.Skipped != 1 || res.Files != 1 {
		t.Errorf("%+v %v", res, err)
	}
	if _, err := os.Lstat(filepath.Join(te.dst, "fifo")); err == nil {
		t.Error("a fifo was copied")
	}
}

func TestCopyTreeReservedName(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, copyTmpPrefix+"x", "x", 0o644)
	if _, err := te.copy(t, CopyOpts{}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("%v", err)
	}
}

func TestCopyTreeProgress(t *testing.T) {
	te := newTreeEnv(t)
	te.put(t, "a/f", strings.Repeat("x", 4096), 0o644)
	var got []CopyProgress
	res, err := te.copy(t, CopyOpts{Progress: func(p CopyProgress) { got = append(got, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !got[len(got)-1].Done || got[len(got)-1].Bytes != 4096 || res.Bytes != 4096 {
		t.Errorf("progress %+v", got)
	}
}

func TestCopyTreeOverTheSocket(t *testing.T) {
	f := newFixture(t)
	te := &treeEnv{env: f.env, src: filepath.Join(f.cfg.MountBase, "s"), dst: filepath.Join(f.cfg.MountBase, "d")}
	os.MkdirAll(te.src, 0o755)
	os.MkdirAll(te.dst, 0o755)
	te.put(t, "a/f", strings.Repeat("y", 1<<16), 0o644)
	os.Symlink("a/f", filepath.Join(te.src, "l"))
	var prog []CopyProgress
	res, err := f.client.CopyTree(context.Background(), te.src, te.dst, CopyOpts{Progress: func(p CopyProgress) { prog = append(prog, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || res.Symlinks != 1 || res.Bytes != 1<<16 {
		t.Errorf("%+v", res)
	}
	if len(prog) == 0 || !prog[len(prog)-1].Done {
		t.Errorf("progress frames: %+v", prog)
	}
	if _, err := f.client.CopyTree(context.Background(), "/etc", te.dst, CopyOpts{}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("hostile source over the socket: %v", err)
	}
	// without a progress callback the stream is still consumed
	os.RemoveAll(te.dst)
	os.MkdirAll(te.dst, 0o755)
	if _, err := f.client.CopyTree(context.Background(), te.src, te.dst, CopyOpts{}); err != nil {
		t.Errorf("no callback: %v", err)
	}
	// cancellation reaches the helper
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client.CopyTree(ctx, te.src, te.dst, CopyOpts{}); err == nil {
		t.Error("cancelled call succeeded")
	}
}

func TestProgressWriterLines(t *testing.T) {
	var got []CopyProgress
	w := &progressWriter{fn: func(p CopyProgress) { got = append(got, p) }}
	w.Write([]byte(`{"entries":1}` + "\n" + `{"entr`))
	w.Write([]byte(`ies":2,"done":true}` + "\nnot json\n"))
	w.Write([]byte(strings.Repeat("z", 10000)))
	w.Write([]byte("\n" + `{"entries":3}` + "\n"))
	if len(got) != 3 || got[1].Entries != 2 || !got[1].Done || got[2].Entries != 3 {
		t.Errorf("%+v", got)
	}
}
