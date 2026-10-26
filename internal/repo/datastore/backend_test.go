package datastore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- directory backend: crash safety ----------------------------------------------

func TestDirCrashAtEveryPutPoint(t *testing.T) {
	points := []string{"put:created", "put:written", "put:synced", "put:linked", "put:done"}
	for _, ifAbsent := range []bool{true, false} {
		for _, point := range points {
			t.Run(fmt.Sprintf("ifAbsent=%v/%s", ifAbsent, point), func(t *testing.T) {
				b, err := NewDirBackend(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				old := []byte("old object")
				if !ifAbsent {
					mustPut(t, b, "k/obj", old, PutOptions{})
				}
				data := bytes.Repeat([]byte("new object content "), 5000)
				b.Hook = func(p, _ string) error {
					if p == point {
						return ErrCrash
					}
					return nil
				}
				err = put(t, b, "k/obj", data, PutOptions{IfAbsent: ifAbsent})
				if !errors.Is(err, ErrCrash) {
					t.Fatalf("expected the injected crash, got %v", err)
				}
				// a "restarted process": the object is absent, the old one, or the complete new one; never partial
				b.Hook = nil
				got, gerr := ReadAll(context.Background(), b, "k/obj", 1<<20)
				switch {
				case errors.Is(gerr, ErrNotFound):
					if !ifAbsent {
						t.Fatal("the existing object vanished")
					}
				case gerr != nil:
					t.Fatal(gerr)
				case bytes.Equal(got, data):
					if point == "put:created" || point == "put:written" || point == "put:synced" {
						t.Fatalf("object visible before it was published (point %s)", point)
					}
				case bytes.Equal(got, old):
					if ifAbsent {
						t.Fatal("old content without a previous put")
					}
				default:
					t.Fatalf("torn object of %d bytes visible after a crash at %s", len(got), point)
				}
				// listing never shows temp debris or partial objects
				for _, k := range listKeys(t, b, "") {
					if k != "k/obj" {
						t.Fatalf("unexpected key %q after a crash", k)
					}
				}
				// the restarted process can write the key (a crash after the link leaves it existing)
				err = put(t, b, "k/obj", []byte("again"), PutOptions{IfAbsent: ifAbsent})
				if err != nil && !(ifAbsent && errors.Is(err, ErrExists)) {
					t.Fatalf("write after the crash: %v", err)
				}
				// debris is removed by CleanTmp
				if _, err := b.CleanTmp(0); err != nil {
					t.Fatal(err)
				}
				if left := b.TempFiles(); len(left) != 0 {
					t.Fatalf("temp debris after cleaning: %v", left)
				}
			})
		}
	}
}

func TestDirNoTempLeftOnFailure(t *testing.T) {
	b, _ := NewDirBackend(t.TempDir())
	_ = b.Put(context.Background(), "x/y", &failingReader{data: make([]byte, 100), n: 10, err: errors.New("boom")}, 100, PutOptions{})
	_ = b.Put(context.Background(), "x/z", strings.NewReader("abc"), 10, PutOptions{})
	_ = put(t, b, "x/dup", []byte("1"), PutOptions{IfAbsent: true})
	_ = put(t, b, "x/dup", []byte("2"), PutOptions{IfAbsent: true})
	if left := b.TempFiles(); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestDirKeysCannotEscapeTheRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	b, err := NewDirBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(parent, "secret")
	if err := os.WriteFile(secret, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"../secret", "a/../../secret", "/etc/passwd", filepath.Join(parent, "secret"), "a/..", "./x", "..\\secret"} {
		if err := put(t, b, k, []byte("pwned"), PutOptions{}); err == nil {
			t.Fatalf("put %q accepted", k)
		}
		if _, err := b.Get(context.Background(), k); err == nil {
			t.Fatalf("get %q accepted", k)
		}
		if err := b.Delete(context.Background(), k); err == nil {
			t.Fatalf("delete %q accepted", k)
		}
	}
	if got, _ := os.ReadFile(secret); string(got) != "do not touch" {
		t.Fatalf("the file outside the root was modified: %q", got)
	}
	// a symlink planted inside the repository is never listed as an object
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if keys := listKeys(t, b, ""); len(keys) != 0 {
		t.Fatalf("a symlink was listed: %v", keys)
	}
	if err := b.List(context.Background(), "../", func(ObjectInfo) error { return nil }); err == nil {
		t.Fatal("listing a prefix with .. accepted")
	}
}

func TestDirObjectAndPrefixConflict(t *testing.T) {
	b, _ := NewDirBackend(t.TempDir())
	mustPut(t, b, "a/b", []byte("x"), PutOptions{})
	if err := put(t, b, "a/b/c", []byte("y"), PutOptions{}); err == nil {
		t.Fatal("an object below an object was accepted")
	}
	if got := get(t, b, "a/b"); string(got) != "x" {
		t.Fatal("conflict damaged the object")
	}
}

func TestDirReopen(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewDirBackend(dir)
	mustPut(t, b, "chunks/ab/cd/x", []byte("1"), PutOptions{IfAbsent: true})
	b2, err := NewDirBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, b2, "chunks/ab/cd/x"); string(got) != "1" {
		t.Fatal("reopened backend lost the object")
	}
}

// ---- memory backend --------------------------------------------------------------

func TestMemHooks(t *testing.T) {
	m := NewMemBackend()
	m.TornPut = func(key string) bool { return key == "torn" }
	if err := put(t, m, "torn", []byte("x"), PutOptions{}); !errors.Is(err, ErrTorn) {
		t.Fatalf("got %v", err)
	}
	mustMissing(t, m, "torn")
	boom := errors.New("boom")
	m.Fault = func(op, key string) error {
		if op == "head" && key == "h" {
			return boom
		}
		return nil
	}
	if _, err := m.Head(context.Background(), "h"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	m.Fault = nil
	mustPut(t, m, "c", []byte("0123456789"), PutOptions{})
	_ = get(t, m, "c")
	if m.BytesRead.Load() != 10 || m.Gets.Load() != 1 || m.Puts.Load() < 2 {
		t.Fatalf("counters: %d %d %d", m.BytesRead.Load(), m.Gets.Load(), m.Puts.Load())
	}
	m.Corrupt("c", 3)
	if got := get(t, m, "c"); bytes.Equal(got, []byte("0123456789")) {
		t.Fatal("Corrupt did nothing")
	}
}

func TestMemClock(t *testing.T) {
	m := NewMemBackend()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return now })
	mustPut(t, m, "a", []byte("x"), PutOptions{})
	info, _ := m.Head(context.Background(), "a")
	if !info.ModTime.Equal(now) {
		t.Fatal(info.ModTime)
	}
	m.SetModTime("a", now.Add(-time.Hour))
	info, _ = m.Head(context.Background(), "a")
	if !info.ModTime.Equal(now.Add(-time.Hour)) {
		t.Fatal("SetModTime")
	}
}

// ---- maintenance modes ------------------------------------------------------------

func TestModeBackend(t *testing.T) {
	m := NewMemBackend()
	mb := NewModeBackend(m, ModeOnline)
	mustPut(t, mb, "k", []byte("v"), PutOptions{})
	mb.SetMode(ModeReadOnly)
	if err := put(t, mb, "k2", []byte("v"), PutOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("write in read-only: %v", err)
	}
	if err := mb.Delete(context.Background(), "k"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete in read-only: %v", err)
	}
	if got := get(t, mb, "k"); string(got) != "v" {
		t.Fatal("read in read-only mode")
	}
	if _, err := mb.Head(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if len(listKeys(t, mb, "")) != 1 {
		t.Fatal("list in read-only mode")
	}
	if _, err := mb.GetRange(context.Background(), "k", 0, 1); err != nil {
		t.Fatal(err)
	}
	mb.SetMode(ModeOffline)
	if _, err := mb.Get(context.Background(), "k"); !errors.Is(err, ErrOffline) {
		t.Fatalf("read offline: %v", err)
	}
	if _, err := mb.Head(context.Background(), "k"); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
	if err := mb.List(context.Background(), "", func(ObjectInfo) error { return nil }); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
	if err := put(t, mb, "k3", []byte("v"), PutOptions{}); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
	if err := mb.Delete(context.Background(), "k"); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
	// nothing reached the storage while refused
	mb.SetMode(ModeOnline)
	if keys := m.Keys(); len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("storage changed while the mode refused writes: %v", keys)
	}
	if Unwrap(mb) != Backend(m) || mb.Kind() != "memory" {
		t.Fatal("unwrap or kind")
	}
	for _, s := range []string{"online", "read-only", "offline"} {
		md, ok := ParseMode(s)
		if !ok || md.String() != s {
			t.Fatal(s)
		}
	}
	if _, ok := ParseMode("bogus"); ok {
		t.Fatal("bogus mode parsed")
	}
}

func TestModeChangesWhileInUse(t *testing.T) {
	mb := NewModeBackend(NewMemBackend(), ModeOnline)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = mb.Put(context.Background(), fmt.Sprintf("k%d-%d", i, j), strings.NewReader("x"), 1, PutOptions{})
			}
		}()
	}
	for j := 0; j < 50; j++ {
		mb.SetMode(Mode(j % 3))
	}
	wg.Wait()
}

func TestFaultBackend(t *testing.T) {
	m := NewMemBackend()
	f := NewFaultBackend(m)
	f.CrashAtCall(3)
	mustPut(t, f, "a", []byte("1"), PutOptions{})
	mustPut(t, f, "b", []byte("1"), PutOptions{})
	if err := put(t, f, "c", []byte("1"), PutOptions{}); !errors.Is(err, ErrCrash) {
		t.Fatal(err)
	}
	if err := f.Delete(context.Background(), "a"); !errors.Is(err, ErrCrash) {
		t.Fatal("calls after the crash must fail")
	}
	if !f.Crashed() || len(m.Keys()) != 2 {
		t.Fatal("state after crash")
	}
	if got := get(t, f, "a"); string(got) != "1" {
		t.Fatal("reads must keep working after a crash")
	}
	f.Reset()
	mustPut(t, f, "c", []byte("1"), PutOptions{})
	f.CrashWhen(func(op, key string) bool { return key == "stop" })
	mustPut(t, f, "ok", []byte("1"), PutOptions{})
	if err := put(t, f, "stop", []byte("1"), PutOptions{}); !errors.Is(err, ErrCrash) {
		t.Fatal(err)
	}
}

// ---- the suite must fail on a broken backend -----------------------------------------

type fakeT struct{ msg string }

type fatalSentinel struct{}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatal(args ...any) {
	f.msg = fmt.Sprint(args...)
	panic(fatalSentinel{})
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	panic(fatalSentinel{})
}

// failures runs every check against b and returns the names that failed.
func failures(factory func() Backend) map[string]string {
	out := map[string]string{}
	for _, c := range conformanceCases() {
		ft := &fakeT{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(fatalSentinel); !ok {
						panic(r)
					}
				}
			}()
			c.fn(ft, factory())
		}()
		if ft.msg != "" {
			out[c.name] = ft.msg
		}
	}
	return out
}

// overwriting ignores IfAbsent.
type overwritingBackend struct{ *MemBackend }

func (o overwritingBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	opts.IfAbsent = false
	return o.MemBackend.Put(ctx, key, r, size, opts)
}

// racy checks existence and writes in two steps.
type racyBackend struct{ *MemBackend }

func (o racyBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	if opts.IfAbsent {
		if _, err := o.MemBackend.Head(ctx, key); err == nil {
			return ErrExists
		}
		time.Sleep(time.Millisecond)
		opts.IfAbsent = false
	}
	return o.MemBackend.Put(ctx, key, r, size, opts)
}

// tearing leaves a partial object when the reader fails.
type tearingBackend struct{ *MemBackend }

func (o tearingBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	buf, err := io.ReadAll(r)
	if err != nil {
		_ = o.MemBackend.Put(ctx, key, bytes.NewReader(buf), int64(len(buf)), PutOptions{})
		return err
	}
	return o.MemBackend.Put(ctx, key, bytes.NewReader(buf), size, opts)
}

// unsorted lists in reverse order.
type unsortedBackend struct{ *MemBackend }

func (o unsortedBackend) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	var all []ObjectInfo
	_ = o.MemBackend.List(ctx, prefix, func(i ObjectInfo) error { all = append(all, i); return nil })
	for i := len(all) - 1; i >= 0; i-- {
		if err := fn(all[i]); err != nil {
			return err
		}
	}
	return nil
}

// truncating lists only the first 1000 keys (a backend that ignores continuation tokens).
type truncatingBackend struct{ *MemBackend }

func (o truncatingBackend) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	n := 0
	return o.MemBackend.List(ctx, prefix, func(i ObjectInfo) error {
		if n++; n > 1000 {
			return nil
		}
		return fn(i)
	})
}

// strictDelete fails on a missing key.
type strictDeleteBackend struct{ *MemBackend }

func (o strictDeleteBackend) Delete(ctx context.Context, key string) error {
	if _, err := o.MemBackend.Head(ctx, key); err != nil {
		return err
	}
	return o.MemBackend.Delete(ctx, key)
}

// lenientKeys accepts keys with ".." components.
type lenientKeysBackend struct{ *MemBackend }

func (o lenientKeysBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	if strings.Contains(key, "..") {
		return o.MemBackend.Put(ctx, "x"+strings.ReplaceAll(key, "..", "_"), r, size, opts)
	}
	return o.MemBackend.Put(ctx, key, r, size, opts)
}

func TestConformanceCatchesBrokenBackends(t *testing.T) {
	good := failures(func() Backend { return NewMemBackend() })
	if len(good) != 0 {
		t.Fatalf("the reference backend fails the suite: %v", good)
	}
	cases := []struct {
		name  string
		mk    func() Backend
		wants []string
	}{
		{"overwrites despite IfAbsent", func() Backend { return overwritingBackend{NewMemBackend()} }, []string{"CreateOnly", "ConcurrentCreateOnly16"}},
		{"non atomic create-only", func() Backend { return racyBackend{NewMemBackend()} }, []string{"ConcurrentCreateOnly16"}},
		{"torn put leaves an object", func() Backend { return tearingBackend{NewMemBackend()} }, []string{"TornPutLeavesNothing", "TornReplaceKeepsOldObject"}},
		{"unsorted list", func() Backend { return unsortedBackend{NewMemBackend()} }, []string{"ListPrefixAndOrder", "ListLarge2500"}},
		{"truncated list", func() Backend { return truncatingBackend{NewMemBackend()} }, []string{"ListLarge2500"}},
		{"strict delete", func() Backend { return strictDeleteBackend{NewMemBackend()} }, []string{"DeleteIsIdempotent"}},
		{"accepts .. keys", func() Backend { return lenientKeysBackend{NewMemBackend()} }, []string{"InvalidKeysRejected"}},
	}
	for _, c := range cases {
		got := failures(c.mk)
		for _, w := range c.wants {
			if _, ok := got[w]; !ok {
				t.Errorf("%s: check %s did not fail (failed: %v)", c.name, w, keysOf(got))
			}
		}
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"a", "a/b", "chunks/ab/cd/" + strings.Repeat("a", 64), "x y", "é/ü", ".h", "a..b"} {
		if err := ValidKey(k); err != nil {
			t.Errorf("%q rejected: %v", k, err)
		}
	}
	for _, k := range []string{"", "/", "a/", "/a", "a//b", ".", "..", "a/./b", "a/../b", "tmp/x", "a\x00", "a\\b", strings.Repeat("a", MaxComponentLen+1)} {
		if ValidKey(k) == nil {
			t.Errorf("%q accepted", k)
		}
	}
	if ValidKey("tmp") != nil || ValidKey("tmpx/y") != nil {
		t.Error("only the tmp/ directory is reserved")
	}
}

func FuzzValidKey(f *testing.F) {
	for _, s := range []string{"a/b", "../x", "tmp/y", "a//b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if ValidKey(key) != nil {
			return
		}
		// a valid key never resolves outside the root
		root := "/r"
		p := filepath.Join(root, filepath.FromSlash(key))
		if !strings.HasPrefix(p, root+"/") {
			t.Fatalf("key %q resolves to %q", key, p)
		}
	})
}
