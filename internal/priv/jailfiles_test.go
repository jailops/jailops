package priv

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/tarcopy"
)

func jfTree(t *testing.T, e *env) (jail, root string) {
	t.Helper()
	root = filepath.Join(e.cfg.MountBase, "projects", "shop", "jails", "web", "root")
	mustWrite(t, filepath.Join(root, "etc", "motd"), "welcome\n")
	mustWrite(t, filepath.Join(root, "usr", "local", "etc", "app.conf"), "x=1\n")
	mustWrite(t, filepath.Join(e.base, "host-secret"), "host secret\n")
	for link, target := range map[string]string{
		filepath.Join(root, "etc", "out"):    filepath.Join(e.base, "host-secret"),
		filepath.Join(root, "etc", "inside"): "motd",
		filepath.Join(root, "etcdir"):        filepath.Join(e.base),
		filepath.Join(root, "relup"):         "../../../..",
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return "shop_web", root
}

func tarOf(t *testing.T, name, body string, mode int64) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	return &b
}

func TestJailStat(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	for p, want := range map[string]api.JailFileType{
		"":              api.FileTypeDir,
		"etc":           api.FileTypeDir,
		"etc/motd":      api.FileTypeFile,
		"etc/inside":    api.FileTypeSymlink,
		"etc/missing":   api.FileTypeNone,
		"nodir/missing": api.FileTypeNone,
	} {
		st, err := e.local.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: p})
		if err != nil {
			t.Errorf("%q: %v", p, err)
			continue
		}
		if st.Type != want {
			t.Errorf("%q: %+v, want %s", p, st, want)
		}
	}
	st, _ := e.local.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: "etc/motd"})
	if st.Size != int64(len("welcome\n")) || st.Mode == "" || st.ModifiedAt == nil {
		t.Errorf("%+v", st)
	}
	if st, _ := e.local.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: "etc/inside"}); st.Link != "motd" {
		t.Errorf("link %q", st.Link)
	}
	// below a file is no path
	if _, err := e.local.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: "etc/motd/nothing"}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("below a file: %v", err)
	}
	// a path through a symlink directory is refused, not answered with what is behind it
	for _, p := range []string{"etcdir/host-secret", "relup/x"} {
		if _, err := e.local.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: p}); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"../x", "/abs", "a//b", "a/../b"} {
		if _, err := e.local.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: p}); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestJailPutAndGet(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	st, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, tarOf(t, "new.conf", "k=v\n", 0o640))
	if err != nil || st.Files != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "etc", "new.conf"))
	if string(b) != "k=v\n" {
		t.Fatalf("%q", b)
	}
	if fi, _ := os.Stat(filepath.Join(root, "etc", "new.conf")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("%v", fi.Mode())
	}
	var out bytes.Buffer
	gst, err := e.local.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: "etc/new.conf"}, &out)
	if err != nil || gst.Files != 1 {
		t.Fatalf("%+v %v", gst, err)
	}
	h, err := tar.NewReader(&out).Next()
	if err != nil || h.Name != "new.conf" || h.Size != 4 {
		t.Fatalf("%+v %v", h, err)
	}
	// a directory needs recursive
	if _, err := e.local.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: "usr"}, &bytes.Buffer{}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("directory without recursive: %v", err)
	}
	out.Reset()
	if gst, err = e.local.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: "usr", Recursive: true}, &out); err != nil || gst.Files != 1 || gst.Dirs != 3 {
		t.Fatalf("%+v %v", gst, err)
	}
	// the root itself is not copied
	if _, err := e.local.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: "", Recursive: true}, &out); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("the root: %v", err)
	}
	// a directory that is missing is not_found unless mkdir
	if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "opt/app"}, tarOf(t, "f", "x", 0o644)); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("missing directory: %v", err)
	}
	if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "opt/app", Mkdir: true}, tarOf(t, "f", "x", 0o644)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func TestJailPutNeverLeavesTheJailRoot(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	// a symlink directory in the destination path
	for _, dir := range []string{"etcdir", "relup", "etc/out"} {
		if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: dir}, tarOf(t, "pwned", "x", 0o644)); err == nil {
			t.Errorf("a path through %s was written", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(e.base, "pwned")); err == nil {
		t.Fatal("written outside the jail")
	}
	// names in the archive
	for _, name := range []string{"../pwned", "../../pwned", "/etc/pwned", "etc/../../pwned"} {
		if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, tarOf(t, name, "x", 0o644)); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%q: %v", name, err)
		}
	}
	// a file named like a symlink in the jail replaces the link, it does not write through it
	if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, tarOf(t, "out", "replaced", 0o644)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.base, "host-secret")); string(b) != "host secret\n" {
		t.Fatalf("the target of the symlink was written: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(root, "etc", "out")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestJailGetDoesNotFollowSymlinks(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	var out bytes.Buffer
	st, err := e.local.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: "etc", Recursive: true}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("host secret")) || st.Symlinks != 2 {
		t.Fatalf("%+v", st)
	}
	for _, p := range []string{"etcdir/host-secret", "relup/x", "etc/out/x"} {
		if _, err := e.local.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: p}, &bytes.Buffer{}); err == nil {
			t.Errorf("%s was read", p)
		}
	}
}

func TestJailFilesRefuseAnotherRoot(t *testing.T) {
	e := newEnv(t)
	jfTree(t, e)
	ctx := context.Background()
	for name, q := range map[string]service.JailStatReq{
		"other jail name": {Jail: "shop_db", Root: filepath.Join(e.cfg.MountBase, "projects", "shop", "jails", "web", "root")},
		"the mount":       {Jail: "shop_web", Root: e.cfg.MountBase},
		"not a root":      {Jail: "shop_web", Root: filepath.Join(e.cfg.MountBase, "projects", "shop", "jails", "web", "root", "etc")},
		"host":            {Jail: "shop_web", Root: "/etc"},
	} {
		if _, err := e.local.JailStat(ctx, q); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a jail whose root is a symlink to another jail's root
	alias := filepath.Join(e.cfg.MountBase, "projects", "shop", "jails", "alias")
	os.MkdirAll(alias, 0o755)
	if err := os.Symlink("../web/root", filepath.Join(alias, "root")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.local.JailStat(ctx, service.JailStatReq{Jail: "shop_alias", Root: filepath.Join(alias, "root")}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("alias: %v", err)
	}
}

func TestJailFilesOnARootThatIsNotMounted(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(e.cfg.MountBase, "projects", "shop", "jails", "cold", "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := e.local.JailStat(context.Background(), service.JailStatReq{Jail: "shop_cold", Root: root})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeNotFound || ae.Detail["missing"] != "root" {
		t.Fatalf("%v", err)
	}
	_, err = e.local.JailPut(context.Background(), service.JailPutReq{Jail: "shop_cold", Root: root}, tarOf(t, "f", "x", 0o644))
	if !errors.As(err, &ae) || ae.Detail["missing"] != "root" {
		t.Fatalf("%v", err)
	}
}

func TestJailPutLimits(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	_, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc", MaxBytes: 10}, tarOf(t, "big", strings.Repeat("x", 100), 0o644))
	if !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "10 bytes") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "big")); err == nil {
		t.Fatal("a file over the limit was written")
	}
	// the helper's own limit holds when the caller asks for more
	e.local.cfg.Limits.MaxJailCopyBytes = 50
	_, err = e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc", MaxBytes: 1 << 40}, tarOf(t, "big", strings.Repeat("x", 100), 0o644))
	if !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("%v", err)
	}
	// transfers at once
	e.local.cfg.Limits.MaxJailCopies = 1
	e.local.copies.Store(1)
	_, err = e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, tarOf(t, "f", "x", 0o644))
	if !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("%v", err)
	}
	e.local.copies.Store(0)
}

func TestJailPutStripsSetIDBitsAndRefusesDevices(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, tarOf(t, "su", "x", 0o4755)); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "etc", "su")); fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("%v", fi.Mode())
	}
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: "null", Typeflag: tar.TypeChar, Mode: 0o666, Devmajor: 1, Devminor: 3})
	tw.Close()
	if _, err := e.local.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, &b); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("device: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc", "null")); err == nil {
		t.Fatal("a device node was made")
	}
}

func TestJailFilesOverTheSocket(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	f := e.serve(t, selfPeers())
	c, err := Dial(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	var big bytes.Buffer
	tw := tar.NewWriter(&big)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB: more than one frame
	tw.WriteHeader(&tar.Header{Name: "blob", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
	tw.Write(data)
	tw.Close()
	st, err := c.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etc"}, &big)
	if err != nil || st.Files != 1 || st.Bytes != int64(len(data)) {
		t.Fatalf("%+v %v", st, err)
	}
	var out bytes.Buffer
	gst, err := c.JailGet(ctx, service.JailGetReq{Jail: jail, Root: root, Path: "etc/blob"}, &out)
	if err != nil || gst.Bytes != int64(len(data)) {
		t.Fatalf("%+v %v", gst, err)
	}
	tr := tar.NewReader(&out)
	h, err := tr.Next()
	if err != nil || h.Size != int64(len(data)) {
		t.Fatalf("%+v %v", h, err)
	}
	got := new(bytes.Buffer)
	got.ReadFrom(tr)
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("the bytes differ")
	}
	if s, err := c.JailStat(ctx, service.JailStatReq{Jail: jail, Root: root, Path: "etc/blob"}); err != nil || s.Type != api.FileTypeFile {
		t.Fatalf("%+v %v", s, err)
	}
	// an error comes back as the helper's error
	if _, err := c.JailPut(ctx, service.JailPutReq{Jail: jail, Root: root, Path: "etcdir"}, tarOf(t, "x", "x", 0o644)); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("%v", err)
	}
	var _ tarcopy.Stats = st
}

// A directory made for a file is a system directory: 0755 whatever the umask says (and root:wheel for the helper, which
// runs as root), never the owner of the file.
func TestJailPutMakesParentsRootWheel0755(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if _, err := e.local.JailPut(context.Background(), service.JailPutReq{Jail: jail, Root: root, Path: "opt/app/conf", Mkdir: true, Preserve: true}, tarOf(t, "f", "x", 0o600)); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"opt", "opt/app", "opt/app/conf"} {
		st, err := os.Stat(filepath.Join(root, d))
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Errorf("%s: %v %v", d, st, err)
		}
	}
	if st, err := os.Stat(filepath.Join(root, "opt/app/conf/f")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file keeps its own mode: %v %v", st, err)
	}
}

func TestJailList(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	ctx := context.Background()
	l, err := e.local.JailList(ctx, service.JailStatReq{Jail: jail, Root: root, Path: "etc"})
	if err != nil || l.Path != "/etc" || l.Truncated {
		t.Fatalf("%+v %v", l, err)
	}
	got := map[string]api.JailFileType{}
	var names []string
	for _, en := range l.Entries {
		got[en.Name] = en.Type
		names = append(names, en.Name)
	}
	// a symlink is listed as a symlink, never followed; the entries come by name
	if len(got) != 3 || got["motd"] != api.FileTypeFile || got["inside"] != api.FileTypeSymlink || got["out"] != api.FileTypeSymlink {
		t.Errorf("%v", got)
	}
	if strings.Join(names, " ") != "inside motd out" {
		t.Errorf("order %v", names)
	}
	if l, err = e.local.JailList(ctx, service.JailStatReq{Jail: jail, Root: root}); err != nil || len(l.Entries) == 0 || l.Path != "/" {
		t.Errorf("root: %+v %v", l, err)
	}
	for p, code := range map[string]api.Code{
		"etc/motd":     api.CodeInvalid,   // a file is not a directory
		"etc/missing":  api.CodeNotFound,  // nothing there
		"etcdir":       api.CodeForbidden, // a symlink to a directory is not followed
		"etc/out":      api.CodeForbidden,
		"etcdir/inner": api.CodeForbidden,
		"../x":         api.CodeInvalid,
		"a//b":         api.CodeInvalid,
	} {
		if _, err := e.local.JailList(ctx, service.JailStatReq{Jail: jail, Root: root, Path: p}); !api.IsCode(err, code) {
			t.Errorf("%q: %v, want %s", p, err, code)
		}
	}
}

func TestJailListIsCapped(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	for i := 0; i < api.MaxFileListEntries+5; i++ {
		mustWrite(t, filepath.Join(root, "many", "f"+strings.Repeat("x", 3)+strconv.Itoa(i)), "")
	}
	l, err := e.local.JailList(context.Background(), service.JailStatReq{Jail: jail, Root: root, Path: "many"})
	if err != nil || len(l.Entries) != api.MaxFileListEntries || !l.Truncated {
		t.Fatalf("%d entries, truncated=%v, %v", len(l.Entries), l.Truncated, err)
	}
}

func TestJailListOverTheSocket(t *testing.T) {
	e := newEnv(t)
	jail, root := jfTree(t, e)
	f := e.serve(t, selfPeers())
	c, err := Dial(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	l, err := c.JailList(context.Background(), service.JailStatReq{Jail: jail, Root: root, Path: "etc"})
	if err != nil || len(l.Entries) != 3 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := c.JailList(context.Background(), service.JailStatReq{Jail: jail, Root: root, Path: "etcdir"}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("%v", err)
	}
}
