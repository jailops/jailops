package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cpHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.seed()
	for _, n := range []string{"web", "web2", "db"} {
		h.run("", "create", n, "--image", "freebsd-15", "--network", "lan", "--storage", "default")
		h.run("", "start", n)
	}
	h.run("", "label", "web", "role=web")
	h.run("", "label", "web2", "role=web")
	for _, j := range []string{"web2", "db"} {
		h.srv.SeedJailFile("main", j, "/etc/hostname", []byte(j+"\n"), 0o644)
	}
	h.srv.SeedJailFile("main", "web", "/etc/motd", []byte("welcome\n"), 0o644)
	h.srv.SeedJailFile("main", "web", "/etc/rc.conf", []byte("sshd_enable=YES\n"), 0o640)
	h.srv.SeedJailFile("main", "web", "/usr/local/etc/app/app.conf", []byte("a=1\n"), 0o600)
	h.srv.SeedJailFile("main", "web", "/usr/local/etc/app/conf.d/x.conf", []byte("x\n"), 0o644)
	return h
}

func writeLocal(t *testing.T, p, data string, mode os.FileMode) {
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

func TestCpFileIntoAJail(t *testing.T) {
	h := cpHarness(t)
	dir := t.TempDir()
	writeLocal(t, filepath.Join(dir, "nginx.conf"), "worker_processes 2;\n", 0o640)
	out, errs, code := h.run("", "cp", filepath.Join(dir, "nginx.conf"), "web:/etc/nginx.conf")
	if code != 0 || !strings.Contains(out, "web:/etc/nginx.conf") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if b, ok := h.srv.JailFile("main", "web", "/etc/nginx.conf"); !ok || string(b) != "worker_processes 2;\n" {
		t.Fatalf("%q %v", b, ok)
	}
	// onto a directory: inside it, under the name of the source
	if _, errs, code := h.run("", "cp", filepath.Join(dir, "nginx.conf"), "web:/etc/"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if _, ok := h.srv.JailFile("main", "web", "/etc/nginx.conf"); !ok {
		t.Fatal("not in the directory")
	}
	// onto a directory that exists, without a slash
	if _, errs, code := h.run("", "cp", filepath.Join(dir, "nginx.conf"), "web:/usr/local/etc/app"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if _, ok := h.srv.JailFile("main", "web", "/usr/local/etc/app/nginx.conf"); !ok {
		t.Fatal("not inside the existing directory")
	}
	// a file that exists is replaced
	writeLocal(t, filepath.Join(dir, "motd"), "hello\n", 0o644)
	if _, errs, code := h.run("", "cp", filepath.Join(dir, "motd"), "web:/etc/motd"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if b, _ := h.srv.JailFile("main", "web", "/etc/motd"); string(b) != "hello\n" {
		t.Fatalf("%q", b)
	}
}

func TestCpDirectoryNeedsDashR(t *testing.T) {
	h := cpHarness(t)
	dir := t.TempDir()
	writeLocal(t, filepath.Join(dir, "site", "index.html"), "<h1>x</h1>", 0o644)
	writeLocal(t, filepath.Join(dir, "site", "css", "a.css"), "a{}", 0o644)
	_, errs, code := h.run("", "cp", filepath.Join(dir, "site"), "web:/etc/")
	if code != 2 || !strings.Contains(errs, "-r") {
		t.Fatalf("without -r: %d %q", code, errs)
	}
	out, errs, code := h.run("", "cp", "-r", filepath.Join(dir, "site")+"/", "web:/usr/local/etc/")
	if code != 0 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	for _, p := range []string{"/usr/local/etc/site/index.html", "/usr/local/etc/site/css/a.css"} {
		if _, ok := h.srv.JailFile("main", "web", p); !ok {
			t.Errorf("%s is missing", p)
		}
	}
	// a directory that does not exist becomes the new name of the source
	if _, errs, code := h.run("", "cp", "-r", filepath.Join(dir, "site"), "web:/usr/local/etc/renamed"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if _, ok := h.srv.JailFile("main", "web", "/usr/local/etc/renamed/css/a.css"); !ok {
		t.Fatal("not copied under the new name")
	}
	// a missing destination directory is refused, and created with --mkdir
	if _, _, code := h.run("", "cp", "-r", filepath.Join(dir, "site"), "web:/opt/app/site"); code == 0 {
		t.Fatal("a missing parent directory was accepted")
	}
	if _, errs, code := h.run("", "cp", "--mkdir", "-r", filepath.Join(dir, "site"), "web:/opt/app/site"); code != 0 {
		t.Fatalf("--mkdir: %d %q", code, errs)
	}
	if _, ok := h.srv.JailFile("main", "web", "/opt/app/site/index.html"); !ok {
		t.Fatal("--mkdir did not copy")
	}
}

func TestCpOutOfAJail(t *testing.T) {
	h := cpHarness(t)
	dst := t.TempDir()
	if _, errs, code := h.run("", "cp", "web:/etc/motd", filepath.Join(dst, "motd.copy")); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "motd.copy")); string(b) != "welcome\n" {
		t.Fatalf("%q", b)
	}
	// into a directory
	if _, errs, code := h.run("", "cp", "web:/etc/motd", "web:/etc/rc.conf", dst+"/"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	for _, n := range []string{"motd", "rc.conf"} {
		if _, err := os.Stat(filepath.Join(dst, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dst, "rc.conf")); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode())
	}
	// a tree needs -r
	if _, errs, code := h.run("", "cp", "web:/usr/local/etc/app", dst); code != 2 || !strings.Contains(errs, "-r") {
		t.Fatalf("%d %q", code, errs)
	}
	if _, errs, code := h.run("", "cp", "-r", "web:/usr/local/etc/app", dst); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "app", "conf.d", "x.conf")); err != nil || string(b) != "x\n" {
		t.Fatalf("%q %v", b, err)
	}
	// a path that is not there
	if _, errs, code := h.run("", "cp", "web:/etc/nothing", dst); code != 4 || !strings.Contains(errs, "no such file") {
		t.Fatalf("%d %q", code, errs)
	}
	// several sources need a directory
	if _, _, code := h.run("", "cp", "web:/etc/motd", "web:/etc/rc.conf", filepath.Join(dst, "nodir")); code != 2 {
		t.Fatalf("several sources into a file: %d", code)
	}
}

func TestCpBetweenJails(t *testing.T) {
	h := cpHarness(t)
	if _, errs, code := h.run("", "cp", "web:/etc/motd", "db:/etc/motd.web"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if b, ok := h.srv.JailFile("main", "db", "/etc/motd.web"); !ok || string(b) != "welcome\n" {
		t.Fatalf("%q %v", b, ok)
	}
	if _, errs, code := h.run("", "cp", "-r", "web:/usr/local/etc/app", "db:/usr/local/etc/"); code != 2 {
		// the target directory does not exist in db
		_ = errs
		if code == 0 {
			t.Fatal("copied into a directory that does not exist")
		}
	}
	if _, errs, code := h.run("", "cp", "--mkdir", "-r", "web:/usr/local/etc/app", "db:/usr/local/etc/"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if _, ok := h.srv.JailFile("main", "db", "/usr/local/etc/app/conf.d/x.conf"); !ok {
		t.Fatal("tree not copied")
	}
}

func TestCpToEveryJailOfASelector(t *testing.T) {
	h := cpHarness(t)
	dir := t.TempDir()
	writeLocal(t, filepath.Join(dir, "motd"), "fleet\n", 0o644)
	out, errs, code := h.run("", "cp", filepath.Join(dir, "motd"), "-l", "role=web", ":/etc/motd")
	if code != 0 || !strings.Contains(errs, "2 ok") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	for _, j := range []string{"web", "web2"} {
		if b, _ := h.srv.JailFile("main", j, "/etc/motd"); string(b) != "fleet\n" {
			t.Errorf("%s: %q", j, b)
		}
	}
	if _, ok := h.srv.JailFile("main", "db", "/etc/motd"); ok {
		t.Fatal("the jail that was not selected got the file")
	}
	// the destination must be :/path with a selector, a named jail without
	if _, _, code := h.run("", "cp", filepath.Join(dir, "motd"), "-l", "role=web", "web:/etc/motd"); code != 2 {
		t.Fatalf("a named jail with -l: %d", code)
	}
	if _, _, code := h.run("", "cp", filepath.Join(dir, "motd"), ":/etc/motd"); code != 2 {
		t.Fatalf(":/path without a selector: %d", code)
	}
}

func TestCpUsageErrors(t *testing.T) {
	h := cpHarness(t)
	dir := t.TempDir()
	writeLocal(t, filepath.Join(dir, "f"), "x", 0o644)
	if _, _, code := h.run("", "cp", filepath.Join(dir, "f"), filepath.Join(dir, "g")); code != 2 {
		t.Fatalf("no jail on either side: %d", code)
	}
	if _, _, code := h.run("", "cp", filepath.Join(dir, "f"), "web:/etc/a", "db:/etc/b"); code != 2 {
		// two destinations: the last is the destination, the others must be sources of one kind
		t.Fatalf("mixed sources: %d", code)
	}
	if _, errs, code := h.run("", "cp", filepath.Join(dir, "missing"), "web:/etc/a"); code == 0 || !strings.Contains(errs, "no such file") {
		t.Fatalf("a missing local source: %d %q", code, errs)
	}
	// a local path with a colon is written ./a:b
	writeLocal(t, filepath.Join(dir, "a:b"), "colon", 0o644)
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := h.run("", "cp", "./a:b", "web:/etc/colon"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if b, _ := h.srv.JailFile("main", "web", "/etc/colon"); string(b) != "colon" {
		t.Fatalf("%q", b)
	}
}

func TestCpDryRunCopiesNothing(t *testing.T) {
	h := cpHarness(t)
	dir := t.TempDir()
	writeLocal(t, filepath.Join(dir, "f"), "x", 0o644)
	out, _, code := h.run("", "--dry-run", "cp", filepath.Join(dir, "f"), "web:/etc/f")
	if code != 0 || !strings.Contains(out, "dry-run") {
		t.Fatalf("%d %q", code, out)
	}
	if _, ok := h.srv.JailFile("main", "web", "/etc/f"); ok {
		t.Fatal("dry-run copied")
	}
}

func TestCpDoesNotFollowLocalSymlinksInATree(t *testing.T) {
	h := cpHarness(t)
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	writeLocal(t, secret, "do not copy", 0o600)
	writeLocal(t, filepath.Join(dir, "tree", "a"), "a", 0o644)
	if err := os.Symlink(secret, filepath.Join(dir, "tree", "link")); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := h.run("", "cp", "-r", filepath.Join(dir, "tree"), "web:/usr/local/etc/"); code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	if b, ok := h.srv.JailFile("main", "web", "/usr/local/etc/tree/link"); ok {
		t.Fatalf("the target of a symlink was copied: %q", b)
	}
}
