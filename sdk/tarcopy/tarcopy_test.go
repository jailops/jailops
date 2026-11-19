package tarcopy

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func root(t *testing.T) (string, *os.Root) {
	t.Helper()
	d := t.TempDir()
	r, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return d, r
}

func write(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// archive builds a tar stream from headers and bodies.
func archive(t *testing.T, es ...any) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for i := 0; i < len(es); i += 2 {
		h := es[i].(*tar.Header)
		body, _ := es[i+1].(string)
		if h.Typeflag == tar.TypeReg || h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	return &b
}

func TestRoundTripOfATree(t *testing.T) {
	src, sr := root(t)
	write(t, filepath.Join(src, "tree", "a.txt"), "alpha", 0o640)
	write(t, filepath.Join(src, "tree", "sub", "b.sh"), "#!/bin/sh\n", 0o755)
	if err := os.Symlink("a.txt", filepath.Join(src, "tree", "link")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st, err := Create(sr, "tree", "", &buf, CreateOpts{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 2 || st.Dirs != 2 || st.Symlinks != 1 || st.Bytes != int64(len("alpha")+len("#!/bin/sh\n")) {
		t.Fatalf("created %+v", st)
	}
	dst, dr := root(t)
	got, err := Extract(dr, &buf, ExtractOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != 2 || got.Dirs != 2 || got.Symlinks != 1 {
		t.Fatalf("extracted %+v", got)
	}
	if read(t, filepath.Join(dst, "tree", "a.txt")) != "alpha" {
		t.Fatal("content")
	}
	if fi, _ := os.Stat(filepath.Join(dst, "tree", "a.txt")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dst, "tree", "sub", "b.sh")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if l, err := os.Readlink(filepath.Join(dst, "tree", "link")); err != nil || l != "a.txt" {
		t.Fatalf("link %q %v", l, err)
	}
}

func TestCreateNamesTheEntriesBelowAs(t *testing.T) {
	src, sr := root(t)
	write(t, filepath.Join(src, "one"), "1", 0o644)
	var buf bytes.Buffer
	if _, err := Create(sr, "one", "renamed", &buf, CreateOpts{}); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	h, err := tr.Next()
	if err != nil || h.Name != "renamed" {
		t.Fatalf("%v %v", h, err)
	}
}

func TestCreateRefusesADirectoryWithoutRecursive(t *testing.T) {
	src, sr := root(t)
	if err := os.Mkdir(filepath.Join(src, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Create(sr, "d", "", io.Discard, CreateOpts{})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "-r") {
		t.Fatalf("%v", err)
	}
}

func TestCreateMissing(t *testing.T) {
	_, sr := root(t)
	if _, err := Create(sr, "nope", "", io.Discard, CreateOpts{}); !errors.Is(err, ErrNotExist) {
		t.Fatalf("%v", err)
	}
}

func TestCreateDoesNotFollowASymlinkInATree(t *testing.T) {
	outside, _ := root(t)
	write(t, filepath.Join(outside, "secret"), "s3cret", 0o600)
	src, sr := root(t)
	if err := os.MkdirAll(filepath.Join(src, "t"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "t", "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "t", "abs")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st, err := Create(sr, "t", "", &buf, CreateOpts{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Symlinks != 2 || st.Files != 0 || bytes.Contains(buf.Bytes(), []byte("s3cret")) {
		t.Fatalf("%+v", st)
	}
}

func TestCreateSkipsSpecialFiles(t *testing.T) {
	src, sr := root(t)
	if err := os.MkdirAll(filepath.Join(src, "t"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(src, "t", "fifo"), 0o644); err != nil {
		t.Skip("no fifo:", err)
	}
	write(t, filepath.Join(src, "t", "f"), "x", 0o644)
	st, err := Create(sr, "t", "", io.Discard, CreateOpts{Recursive: true})
	if err != nil || st.Skipped != 1 || st.Files != 1 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestCreateKeepsNoSetIDBits(t *testing.T) {
	src, sr := root(t)
	write(t, filepath.Join(src, "su"), "x", 0o755)
	if err := os.Chmod(filepath.Join(src, "su"), 0o4755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Create(sr, "su", "", &buf, CreateOpts{}); err != nil {
		t.Fatal(err)
	}
	h, _ := tar.NewReader(&buf).Next()
	if h.Mode&0o7000 != 0 {
		t.Fatalf("mode %o", h.Mode)
	}
}

func TestExtractRefusesBadNames(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/abs", "a//b", "a/./b", "", "./x"} {
		_, dr := root(t)
		_, err := Extract(dr, archive(t, &tar.Header{Name: name, Mode: 0o644}, "x"), ExtractOpts{})
		if !errors.Is(err, ErrRefused) {
			t.Errorf("name %q: %v", name, err)
		}
	}
}

func TestCheckNameRefusesANulByte(t *testing.T) {
	if err := checkName("a\x00b", 8); !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
}

func TestExtractNeverWritesOutsideTheRoot(t *testing.T) {
	parent, _ := root(t)
	dst := filepath.Join(parent, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	dr, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer dr.Close()
	_, err = Extract(dr, archive(t, &tar.Header{Name: "../pwned", Mode: 0o644}, "x"), ExtractOpts{})
	if err == nil {
		t.Fatal("accepted")
	}
	if _, err := os.Stat(filepath.Join(parent, "pwned")); err == nil {
		t.Fatal("written outside")
	}
}

func TestExtractDoesNotFollowASymlinkDirectory(t *testing.T) {
	outside, _ := root(t)
	dst, dr := root(t)
	if err := os.Symlink(outside, filepath.Join(dst, "d")); err != nil {
		t.Fatal(err)
	}
	_, err := Extract(dr, archive(t, &tar.Header{Name: "d/file", Mode: 0o644}, "x"), ExtractOpts{})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); err == nil {
		t.Fatal("written through the symlink")
	}
}

func TestExtractReplacesASymlinkTargetFileNotItsDestination(t *testing.T) {
	outside, _ := root(t)
	write(t, filepath.Join(outside, "victim"), "keep", 0o644)
	dst, dr := root(t)
	if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(dst, "f")); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(dr, archive(t, &tar.Header{Name: "f", Mode: 0o644}, "new"), ExtractOpts{}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(outside, "victim")) != "keep" {
		t.Fatal("the destination of the symlink was written")
	}
	if fi, err := os.Lstat(filepath.Join(dst, "f")); err != nil || !fi.Mode().IsRegular() || read(t, filepath.Join(dst, "f")) != "new" {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestExtractNeverReplacesADirectoryWithAFile(t *testing.T) {
	dst, dr := root(t)
	if err := os.Mkdir(filepath.Join(dst, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Extract(dr, archive(t, &tar.Header{Name: "d", Mode: 0o644}, "x"), ExtractOpts{})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
}

func TestExtractStripsSetIDBits(t *testing.T) {
	dst, dr := root(t)
	if _, err := Extract(dr, archive(t, &tar.Header{Name: "su", Mode: 0o4755}, "x", &tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o2755}, ""), ExtractOpts{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"su", "d"} {
		fi, err := os.Stat(filepath.Join(dst, n))
		if err != nil || fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Fatalf("%s: %v %v", n, fi.Mode(), err)
		}
	}
}

func TestExtractRefusesLinksAndDevices(t *testing.T) {
	for name, h := range map[string]*tar.Header{
		"hardlink": {Name: "h", Typeflag: tar.TypeLink, Linkname: "x"},
		"char":     {Name: "c", Typeflag: tar.TypeChar, Mode: 0o600},
		"fifo":     {Name: "f", Typeflag: tar.TypeFifo, Mode: 0o600},
	} {
		_, dr := root(t)
		if _, err := Extract(dr, archive(t, h, ""), ExtractOpts{}); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExtractKeepsASymlinkAsTextAndDoesNotFollowItLater(t *testing.T) {
	outside, _ := root(t)
	dst, dr := root(t)
	// the link is created, then a file below it is refused: the helper never follows it
	_, err := Extract(dr, archive(t,
		&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: outside},
		"",
		&tar.Header{Name: "l/f", Mode: 0o644}, "x"), ExtractOpts{})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "l")); err != nil || l != outside {
		t.Fatalf("%q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Fatal("followed")
	}
}

func TestExtractBytesLimit(t *testing.T) {
	_, dr := root(t)
	_, err := Extract(dr, archive(t, &tar.Header{Name: "big", Mode: 0o644}, strings.Repeat("x", 5000)), ExtractOpts{Limits: Limits{MaxBytes: 1000}})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%v", err)
	}
}

func TestExtractEntriesAndDepthLimits(t *testing.T) {
	_, dr := root(t)
	var es []any
	for i := 0; i < 5; i++ {
		es = append(es, &tar.Header{Name: "f" + string(rune('a'+i)), Mode: 0o644}, "x")
	}
	if _, err := Extract(dr, archive(t, es...), ExtractOpts{Limits: Limits{MaxEntries: 3}}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("entries: %v", err)
	}
	_, dr2 := root(t)
	deep := strings.Repeat("d/", 6) + "f"
	if _, err := Extract(dr2, archive(t, &tar.Header{Name: deep, Mode: 0o644}, "x"), ExtractOpts{Limits: Limits{MaxDepth: 4}}); !errors.Is(err, ErrRefused) {
		t.Fatalf("depth: %v", err)
	}
}

func TestExtractDamagedArchive(t *testing.T) {
	_, dr := root(t)
	if _, err := Extract(dr, bytes.NewReader([]byte(strings.Repeat("garbage", 200))), ExtractOpts{}); !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
}

func TestExtractReplacesAnExistingFileAtomically(t *testing.T) {
	dst, dr := root(t)
	write(t, filepath.Join(dst, "f"), "old", 0o600)
	if _, err := Extract(dr, archive(t, &tar.Header{Name: "f", Mode: 0o644}, "newer"), ExtractOpts{}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dst, "f")) != "newer" {
		t.Fatal("not replaced")
	}
	es, _ := os.ReadDir(dst)
	if len(es) != 1 {
		t.Fatalf("a temporary file is left: %v", es)
	}
}

func TestPreserveSetsTheTimes(t *testing.T) {
	src, sr := root(t)
	write(t, filepath.Join(src, "f"), "x", 0o644)
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "f"), when, when); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Create(sr, "f", "", &buf, CreateOpts{Preserve: true}); err != nil {
		t.Fatal(err)
	}
	dst, dr := root(t)
	if _, err := Extract(dr, &buf, ExtractOpts{Preserve: true}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "f"))
	if err != nil || !fi.ModTime().Equal(when) {
		t.Fatalf("%v %v", fi.ModTime(), err)
	}
	// without Preserve the time is the time of the copy
	dst2, dr2 := root(t)
	var buf2 bytes.Buffer
	if _, err := Create(sr, "f", "", &buf2, CreateOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(dr2, &buf2, ExtractOpts{}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dst2, "f")); fi.ModTime().Equal(when) {
		t.Fatal("the time was kept without Preserve")
	}
}

func TestCreateByteLimit(t *testing.T) {
	src, sr := root(t)
	write(t, filepath.Join(src, "f"), strings.Repeat("x", 5000), 0o644)
	if _, err := Create(sr, "f", "", io.Discard, CreateOpts{Limits: Limits{MaxBytes: 100}}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%v", err)
	}
}
