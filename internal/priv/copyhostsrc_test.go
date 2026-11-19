package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// hostSrcEnv makes an approved directory with a tree of files below it and an empty volume dataset next to the
// tests' other roots.
func hostSrcEnv(t *testing.T) (l *Local, approved, vol string) {
	t.Helper()
	e := newEnv(t)
	approved = filepath.Join(e.base, "srv")
	if err := os.MkdirAll(filepath.Join(approved, "data", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	vol = filepath.Join(e.cfg.MountBase, "projects", "alpha", "volumes", "imp")
	if err := os.MkdirAll(vol, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Admin.HostMountRoots = []string{approved}
	l, err := NewLocal(cfg, Deps{ZFS: e.zfs}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	return l, approved, vol
}

func TestCopyFromAHostDirectoryOnAPlainDisk(t *testing.T) {
	l, approved, vol := hostSrcEnv(t)
	src := filepath.Join(approved, "data")
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(src, rel)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "hello", 0o640)
	write("sub/b.txt", "world!", 0o600)
	write("suid", "x", os.ModeSetuid|os.ModeSetgid|0o755)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "abs-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/b.txt", filepath.Join(src, "rel-link")); err != nil {
		t.Fatal(err)
	}
	res, err := l.CopyTree(context.Background(), src, vol, CopyOpts{HostSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 3 || res.Symlinks != 2 || res.Bytes != int64(len("hello")+len("world!")+len("x")) {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(vol, "sub", "b.txt")); string(b) != "world!" {
		t.Fatalf("b.txt = %q", b)
	}
	for rel, want := range map[string]os.FileMode{"a.txt": 0o640, "sub/b.txt": 0o600, "suid": 0o755} {
		fi, err := os.Lstat(filepath.Join(vol, rel))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want || fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			t.Fatalf("%s mode = %v, want %v with no setuid or setgid", rel, fi.Mode(), want)
		}
	}
	// the links came across as links and nothing was followed
	if to, err := os.Readlink(filepath.Join(vol, "abs-link")); err != nil || to != "/etc/passwd" {
		t.Fatalf("abs-link = %q, %v", to, err)
	}
	if to, err := os.Readlink(filepath.Join(vol, "rel-link")); err != nil || to != "sub/b.txt" {
		t.Fatalf("rel-link = %q, %v", to, err)
	}
	noStale(t, vol)
}

func TestCopyFromAHostDirectoryRefusesWhatIsNotApproved(t *testing.T) {
	l, approved, vol := hostSrcEnv(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, src := range map[string]string{
		"outside the approved directories": outside,
		"the parent of an approved one":    filepath.Dir(approved),
		"a relative path":                  "srv/data",
		"a path with ..":                   approved + "/../srv/data",
		"an unclean path":                  approved + "//data",
	} {
		_, err := l.CopyTree(ctx, src, vol, CopyOpts{HostSource: true})
		if err == nil {
			t.Fatalf("%s: the copy was accepted", name)
		}
	}
	_, err := l.CopyTree(ctx, outside, vol, CopyOpts{HostSource: true})
	if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "admin-host-mount") {
		t.Fatalf("outside = %v", err)
	}
	// without the option the same path is a storage path: the host directory is not one
	if _, err := l.CopyTree(ctx, filepath.Join(approved, "data"), vol, CopyOpts{}); err == nil {
		t.Fatal("a host directory was copied without HostSource")
	}
}

func TestCopyFromAHostDirectoryNeverFollowsALinkOnTheWay(t *testing.T) {
	l, approved, vol := hostSrcEnv(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a link on the way to the source, and a link inside the tree that leaves it
	if err := os.Symlink(outside, filepath.Join(approved, "away")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CopyTree(context.Background(), filepath.Join(approved, "away"), vol, CopyOpts{HostSource: true}); err == nil {
		t.Fatal("the copy followed a symbolic link on the way to the source")
	}
	if err := os.Symlink(outside, filepath.Join(approved, "data", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CopyTree(context.Background(), filepath.Join(approved, "data"), vol, CopyOpts{HostSource: true}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(vol, "escape")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link out of the tree was followed and copied as %v, %v", fi, err)
	}
	if to, err := os.Readlink(filepath.Join(vol, "escape")); err != nil || to != outside {
		t.Fatalf("the link was not copied as a link: %q %v", to, err)
	}
}

func TestCopyFromTheApprovedDirectoryItselfAndCheckOnly(t *testing.T) {
	l, approved, vol := hostSrcEnv(t)
	if err := os.WriteFile(filepath.Join(approved, "top"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// the check opens the source and copies nothing; dst is not looked at
	if _, err := l.CopyTree(ctx, approved, "", CopyOpts{HostSource: true, CheckOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CopyTree(ctx, filepath.Join(approved, "missing"), "", CopyOpts{HostSource: true, CheckOnly: true}); err == nil {
		t.Fatal("the check of a missing directory passed")
	}
	if ents, _ := os.ReadDir(vol); len(ents) != 0 {
		t.Fatalf("a check wrote %v", ents)
	}
	if _, err := l.CopyTree(ctx, approved, filepath.Join(vol), CopyOpts{CheckOnly: true}); err == nil {
		t.Fatal("check_only without a host source was accepted")
	}
	res, err := l.CopyTree(ctx, approved, vol, CopyOpts{HostSource: true})
	if err != nil || res.Files < 1 {
		t.Fatalf("copy of the approved directory itself: %+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(vol, "top")); string(b) != "t" {
		t.Fatalf("top = %q", b)
	}
}
