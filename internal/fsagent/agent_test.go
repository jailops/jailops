package fsagent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"jailops.net/jailops/internal/repo/catalog"
)

func write(t testing.TB, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func rnd(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// makeTree is a varied tree: small, large and empty files, symlinks, a hard link, a fifo, a socket, xattrs.
func makeTree(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "etc/rc.conf", []byte("hostname=web1\n"))
	write(t, root, "etc/motd", []byte("welcome\n"))
	write(t, root, "var/db/big.bin", rnd(1, 700<<10))
	write(t, root, "var/db/huge.bin", rnd(2, 3<<20))
	write(t, root, "var/empty", nil)
	for i := 0; i < 30; i++ {
		write(t, root, fmt.Sprintf("usr/small/f%03d", i), rnd(int64(100+i), 3000+i*911))
	}
	for _, l := range [][2]string{{"../etc/rc.conf", "var/link"}, {"/nonexistent/target", "var/dangling"}} {
		if err := os.Symlink(l[0], filepath.Join(root, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(root, "etc/motd"), filepath.Join(root, "etc/motd.hard")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "var/pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(root, "var", "sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	_ = unix.Setxattr(filepath.Join(root, "etc/rc.conf"), "user.note", []byte("kept"), 0)
	old := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for _, p := range []string{"etc/rc.conf", "etc/motd", "var/db/big.bin"} {
		if err := os.Chtimes(filepath.Join(root, p), old, old); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// start runs an agent on one end of an in-memory connection and returns a client on the other.
func start(t testing.TB, mode Mode, root string) *Client {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(a, Config{Mode: mode, Root: root}); a.Close() }()
	c := NewClient(b)
	t.Cleanup(func() {
		c.Close()
		if err := <-done; err != nil {
			t.Errorf("the agent ended with %v", err)
		}
	})
	return c
}

// shape is a tree without what differs between two reads of the same files (nothing) or two copies (inodes).
func shape(e *catalog.Entry, withIno bool) string {
	var b bytes.Buffer
	var rec func(p string, e *catalog.Entry)
	rec = func(p string, e *catalog.Entry) {
		ino := uint64(0)
		if withIno {
			ino = e.Inode
		}
		fmt.Fprintf(&b, "%s k=%d m=%o u=%d g=%d mt=%d sz=%d link=%q maj=%d min=%d fl=%d ino=%d attrs=%d\n",
			p, e.Kind, e.Mode, e.UID, e.GID, e.MtimeNs, e.Size, e.Link, e.Major, e.Minor, e.Flags, ino, len(e.Attrs))
		for _, c := range e.Children {
			rec(p+"/"+c.Name, c)
		}
	}
	rec("", e)
	return b.String()
}

func scanFill(t testing.TB, root string, o catalog.ScanOptions) (*catalog.Result, [][]byte) {
	t.Helper()
	res, err := catalog.Scan(root, o)
	if err != nil {
		t.Fatal(err)
	}
	fsys := o.FS
	if fsys == nil {
		fsys = catalog.LocalFS(nil)
	}
	var contents [][]byte
	for _, r := range res.Runs {
		for _, f := range r.Files {
			rc, err := fsys.Open(f.Path)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			f.Entry.Sum = sha256.Sum256(b)
			contents = append(contents, b)
		}
	}
	return res, contents
}

func TestReadAgentScansLikeTheLocalFileSystem(t *testing.T) {
	root := makeTree(t)
	c := start(t, ModeRead, root)
	local, lc := scanFill(t, root, catalog.ScanOptions{})
	remote, rc := scanFill(t, "/", catalog.ScanOptions{FS: c})
	if a, b := shape(local.Tree.Root, true), shape(remote.Tree.Root, true); a != b {
		t.Fatalf("the trees differ\nlocal:\n%s\nremote:\n%s", a, b)
	}
	if local.PayloadSize != remote.PayloadSize || len(lc) != len(rc) {
		t.Fatalf("payload %d/%d, files %d/%d", local.PayloadSize, remote.PayloadSize, len(lc), len(rc))
	}
	for i := range lc {
		if !bytes.Equal(lc[i], rc[i]) {
			t.Fatalf("content %d differs", i)
		}
	}
	// the socket is skipped on both sides, the fifo and the hard link are there
	if _, err := remote.Tree.Lookup("var/sock"); err == nil {
		t.Fatal("a socket was archived")
	}
	if e, err := remote.Tree.Lookup("etc/motd.hard"); err != nil || e.Kind != catalog.KHardlink {
		t.Fatalf("hard link: %+v %v", e, err)
	}
}

func TestReadAgentErrorsKeepTheirMeaning(t *testing.T) {
	root := makeTree(t)
	c := start(t, ModeRead, root)
	if _, err := c.ReadNames("/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing directory: %v", err)
	}
	if _, _, _, err := c.StatEntry("/nope", "nope"); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("stat of nothing: %v", err)
	}
	if _, _, _, err := c.StatEntry("/var/sock", "sock"); !errors.Is(err, catalog.ErrSocket) {
		t.Fatalf("a socket must come back as ErrSocket: %v", err)
	}
	// a fifo is never opened (it would block), a directory is not a file, a symlink is not followed
	for _, p := range []string{"/var/pipe", "/var", "/var/link"} {
		if _, err := c.Open(p); err == nil {
			t.Errorf("Open(%s) succeeded", p)
		}
	}
	// the client cannot leave the root with dots
	rc, err := c.Open("/../../../../etc/rc.conf")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hostname=web1\n" {
		t.Fatalf("a path with dots must stay below the root: %q", b)
	}
}

func TestModesAreEnforced(t *testing.T) {
	root := t.TempDir()
	r := start(t, ModeRead, root)
	if err := r.Mkdir("x"); err == nil {
		t.Error("a read agent made a directory")
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Error("it did")
	}
	w := start(t, ModeWrite, root)
	if _, err := w.ReadNames("/"); err == nil {
		t.Error("a write agent listed a directory")
	}
	if _, err := w.Open("/anything"); err == nil {
		t.Error("a write agent opened a file")
	}
}

func TestConcurrentOpensWithFlowControl(t *testing.T) {
	root := makeTree(t)
	c := start(t, ModeRead, root)
	want := map[string][]byte{}
	for _, p := range []string{"var/db/huge.bin", "var/db/big.bin", "usr/small/f007"} {
		b, _ := os.ReadFile(filepath.Join(root, p))
		want[p] = b
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		for p, w := range want {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rc, err := c.Open("/" + p)
				if err != nil {
					t.Error(err)
					return
				}
				defer rc.Close()
				got, err := io.ReadAll(rc)
				if err != nil || !bytes.Equal(got, w) {
					t.Errorf("%s: %d bytes, err %v", p, len(got), err)
				}
			}()
		}
	}
	wg.Wait()
	// a reader that gives up early does not wedge the connection
	rc, err := c.Open("/var/db/huge.bin")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	_, _ = io.ReadFull(rc, buf)
	rc.Close()
	if _, err := c.ReadNames("/etc"); err != nil {
		t.Fatalf("after an abandoned read: %v", err)
	}
}

func TestWriteAgentExtractsLikeTheLocalSink(t *testing.T) {
	src := makeTree(t)
	res, contents := scanFill(t, src, catalog.ScanOptions{})
	var pay []byte
	for _, c := range contents {
		pay = append(pay, c...)
	}
	opts := catalog.ExtractOptions{NoChown: true, Lenient: true, SkipDevices: true}

	localDest := t.TempDir()
	o := opts
	o.Skips = &catalog.Skips{}
	if err := res.Tree.Extract(localDest, catalog.FromReaderAt(bytes.NewReader(pay)), o); err != nil {
		t.Fatal(err)
	}

	agentDest := t.TempDir()
	c := start(t, ModeWrite, agentDest)
	o = opts
	o.Skips = &catalog.Skips{}
	if err := res.Tree.ExtractTo(c, catalog.FromReaderAt(bytes.NewReader(pay)), o); err != nil {
		t.Fatal(err)
	}
	ls, _ := catalog.Scan(localDest, catalog.ScanOptions{})
	as, _ := catalog.Scan(agentDest, catalog.ScanOptions{})
	if a, b := shape(ls.Tree.Root, false), shape(as.Tree.Root, false); a != b {
		t.Fatalf("extractions differ\nlocal:\n%s\nagent:\n%s", a, b)
	}
	// the contents too
	for _, p := range []string{"var/db/huge.bin", "etc/motd.hard", "usr/small/f012"} {
		a, _ := os.ReadFile(filepath.Join(localDest, p))
		b, _ := os.ReadFile(filepath.Join(agentDest, p))
		if !bytes.Equal(a, b) || len(a) == 0 {
			t.Errorf("%s differs (%d, %d bytes)", p, len(a), len(b))
		}
	}
}

func TestWriteAgentRefusesWhatLocalSinkRefuses(t *testing.T) {
	dest := t.TempDir()
	c := start(t, ModeWrite, dest)
	if err := c.Begin(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../escape", "a/../../escape", "/abs", "x//y", "."} {
		if err := c.Mkdir(rel); err == nil {
			t.Errorf("Mkdir(%q) succeeded", rel)
		}
		if err := c.WriteFile(rel, 1, nil, bytes.NewReader([]byte("x"))); err == nil {
			t.Errorf("WriteFile(%q) succeeded", rel)
		}
	}
	// a symlink planted earlier is never followed by a later entry
	if err := c.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	if err := c.Symlink("d/out", "/etc"); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteFile("d/out/evil", 1, nil, bytes.NewReader([]byte("x"))); err == nil {
		t.Error("a file was written through a symlink")
	}
	// the connection stays usable after a refused write that left its data frames behind
	if err := c.Mkdir("after"); err != nil {
		t.Fatalf("after refusals: %v", err)
	}
	// a wrong checksum is an error
	var wrong [32]byte
	if err := c.WriteFile("bad", 3, &wrong, bytes.NewReader([]byte("abc"))); err == nil {
		t.Error("a file with the wrong SHA-256 was accepted")
	}
	// a short reader fails the write and the connection survives
	if err := c.WriteFile("short", 10, nil, bytes.NewReader([]byte("abc"))); err == nil {
		t.Error("a short file was accepted")
	}
	if err := c.Mkdir("still"); err != nil {
		t.Fatalf("after a short write: %v", err)
	}
}

func TestClientFailsWhenTheAgentGoesAway(t *testing.T) {
	a, b := net.Pipe()
	c := NewClient(b)
	a.Close()
	if _, err := c.ReadNames("/"); err == nil {
		t.Fatal("no error from a dead agent")
	}
	if _, err := c.Open("/x"); err == nil {
		t.Fatal("no error from a dead agent")
	}
}

func TestFrameLimits(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, frame{kind: kindData, id: 1, body: make([]byte, MaxFrame+1)}); err == nil {
		t.Error("an oversized frame was written")
	}
	// a hostile length is refused before anything is allocated
	hostile := []byte{0xff, 0xff, 0xff, 0xff, 'Q', 0, 0, 0, 1}
	if _, err := readFrame(bufioReader(hostile)); err == nil {
		t.Error("a 4 GiB frame was accepted")
	}
	if _, err := readFrame(bufioReader([]byte{0, 0, 0, 2, 'Q', 0, 0, 0, 1})); err == nil {
		t.Error("a frame too short for its header was accepted")
	}
}

func bufioReader(b []byte) *bufio.Reader { return bufio.NewReader(bytes.NewReader(b)) }

func FuzzServeRead(f *testing.F) {
	f.Add([]byte{0, 0, 0, 9, 'Q', 0, 0, 0, 1, '{', '}', 'x', 'y'})
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := net.Pipe()
		go func() { b.Write(data); b.Close() }()
		done := make(chan struct{})
		go func() { _ = Serve(a, Config{Mode: ModeRead, Root: t.TempDir()}); close(done) }()
		go io.Copy(io.Discard, b)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the agent hung on garbage")
		}
		a.Close()
	})
}
