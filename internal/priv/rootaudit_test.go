package priv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"
	"jailops.net/jailops/internal/jailaudit"
	"jailops.net/jailops/sdk/api"
)

const cleanPasswd = `# $FreeBSD$
root:*:0:0::0:0:Charlie &:/root:/bin/sh
toor:*:0:0::0:0:Bourne-again Superuser:/root:
daemon:*:1:1::0:0:Owner of many system processes:/root:/usr/sbin/nologin
www:*:80:80::0:0:World Wide Web Owner:/nonexistent:/usr/sbin/nologin
`

func sum1(b string) string {
	h := sha256.Sum256([]byte(b))
	return "1$" + hex.EncodeToString(h[:])
}

// auditTree makes the root of a jail as a fresh one looks: the base's programs, a setuid program of the base, the
// links tzsetup and the base make, the repository files of FreeBSD with FreeBSD-base disabled.
func auditTree(t *testing.T, e *env) (string, []jailaudit.File) {
	t.Helper()
	container := filepath.Join(e.cfg.MountBase, "projects", "demo", "jails", "web")
	root := filepath.Join(container, "root")
	for _, d := range []string{"bin", "usr/bin", "etc/pkg", "usr/local/etc/pkg/repos", "usr/share/zoneinfo/Europe", "var/db/pkg", "root"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, p), mode); err != nil {
			t.Fatal(err)
		}
	}
	w("bin/sh", "the shell", 0o555)
	w("usr/bin/su", "su", 0o4555)
	w("etc/master.passwd", cleanPasswd, 0o600)
	w("etc/crontab", "SHELL=/bin/sh\n", 0o644)
	w("etc/rc.conf", "cron_flags=\"-J 60\"\n", 0o644)
	w("etc/pkg/FreeBSD.conf", "FreeBSD-ports: {\n  url: \"pkg+https://pkg.FreeBSD.org/${ABI}/quarterly\",\n  enabled: yes\n}\nFreeBSD-base: {\n  url: \"pkg+https://pkg.FreeBSD.org/${ABI}/base_release_${VERSION_MINOR}\",\n  enabled: yes\n}\n", 0o644)
	w("usr/local/etc/pkg/repos/FreeBSD-base.conf", "FreeBSD-base: { enabled: no }\n", 0o644)
	w("usr/share/zoneinfo/Europe/Berlin", "tz", 0o444)
	os.Symlink("/usr/share/zoneinfo/Europe/Berlin", filepath.Join(root, "etc/localtime"))
	// a library link of the base: pkg's checksum of a link is not the content's, and a link is never compared
	os.MkdirAll(filepath.Join(root, "usr/lib"), 0o755)
	w("usr/lib/libx.so.6", "the library", 0o444)
	os.Symlink("libx.so.6", filepath.Join(root, "usr/lib/libx.so"))
	files := []jailaudit.File{{Path: "/usr/lib/libx.so", Sum: sum1("libx.so.6")}, {Path: "/usr/lib/libx.so.6", Sum: sum1("the library")}, {Path: "/bin/sh", Sum: sum1("the shell")}, {Path: "/usr/bin/su", Sum: sum1("su")}, {Path: "/etc/crontab"},
		{Path: "/etc/master.passwd"}, {Path: "/usr/share/zoneinfo/Europe/Berlin"}}
	return root, files
}

func TestAuditRootCleanJail(t *testing.T) {
	e := newEnv(t)
	root, files := auditTree(t, e)
	f, err := e.local.AuditRoot(context.Background(), RootAuditReq{Root: root, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Suspected) != 0 || len(f.SetID) != 0 || len(f.Modified) != 0 || f.Verified != 3 || f.SSHD {
		t.Fatalf("a clean root: %+v", f)
	}
	a := f.Accounts
	if !a.Read || a.Root != jailaudit.PassLocked || a.Toor != jailaudit.PassLocked || len(a.UID0) != 0 || len(a.Empty) != 0 || a.RootShell != "/bin/sh" {
		t.Fatalf("accounts: %+v", a)
	}
	if !strings.Contains(f.Texts["/etc/rc.conf"], "-J 60") || f.Texts["/usr/local/etc/pkg/repos/FreeBSD-base.conf"] == "" || f.Texts["/etc/pkg/FreeBSD.conf"] == "" {
		t.Fatalf("texts: %v", f.Texts)
	}
	b, _ := os.ReadFile(filepath.Join(root, "etc/master.passwd"))
	for _, v := range f.Texts {
		if strings.Contains(v, "Charlie") || string(b) == v {
			t.Fatal("master.passwd left the helper")
		}
	}
	// the jail's checks see nothing wrong in a clean root
	for _, c := range jailaudit.Evaluate(jailaudit.Input{Facts: f, HasPkgDB: true, Owned: map[string]bool{"/usr/bin/su": true}}) {
		if c.Group == jailaudit.GroupSecurity && c.Result != api.AuditPass {
			t.Errorf("%s: %s %s", c.ID, c.Result, c.Detail)
		}
	}
}

func TestAuditRootFindsWhatWasPlanted(t *testing.T) {
	e := newEnv(t)
	root, files := auditTree(t, e)
	w := func(p, s string, mode os.FileMode) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.Remove(filepath.Join(root, p))
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(filepath.Join(root, p), mode)
	}
	w("etc/master.passwd", cleanPasswd+"backdoor:$6$x$y:0:0::0:0:x:/root:/bin/sh\nguest::1001:1001::0:0:x:/home/guest:/bin/sh\n", 0o600)
	os.Remove(filepath.Join(root, "etc/crontab"))
	os.Symlink("/etc/master.passwd", filepath.Join(root, "etc/crontab"))
	w("usr/local/etc/pkg/repos/extra.conf", "extra: { url: \"https://pkg.example.org/${ABI}\", enabled: yes }\n", 0o644)
	w("usr/local/etc/pkg/repos/FreeBSD-base.conf", "FreeBSD-base: { enabled: yes }\n", 0o644)
	w("usr/local/etc/pkg.conf", "PKG_ENABLE_PLUGINS = true;\n", 0o644)
	w("tmp/.x/rootsh", "x", 0o4755)
	w("bin/sh", "a shell with something added", 0o555)
	w("usr/sbin/sshd", "sshd", 0o555)
	w("etc/ssh/sshd_config", "#PermitRootLogin no\nPermitRootLogin yes\nPasswordAuthentication yes\n", 0o644)
	os.Remove(filepath.Join(root, "etc/periodic.conf"))
	os.Symlink("/etc/periodic.conf", filepath.Join(root, "etc/periodic.conf"))
	f, err := e.local.AuditRoot(context.Background(), RootAuditReq{Root: root, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Suspected) != 2 || !strings.Contains(strings.Join(f.Suspected, ";"), "etc/crontab is a symbolic link out of the root") ||
		!strings.Contains(strings.Join(f.Suspected, ";"), "pkg.conf turns pkg plugins on") {
		t.Errorf("suspected: %v", f.Suspected)
	}
	if !slices.Equal(f.Accounts.UID0, []string{"backdoor"}) || !slices.Equal(f.Accounts.Empty, []string{"guest"}) {
		t.Errorf("accounts: %+v", f.Accounts)
	}
	// a file system that drops the set-id bits of an unprivileged chmod (some sandboxes) cannot hold the planted file
	setid := true
	if fi, err := os.Lstat(filepath.Join(root, "tmp/.x/rootsh")); err != nil || fi.Mode()&os.ModeSetuid == 0 {
		setid = false
		t.Log("this file system keeps no set-uid bit: the walk for set-id files is not exercised here")
	}
	if setid && !slices.Equal(f.SetID, []string{"/tmp/.x/rootsh"}) || !slices.Equal(f.Modified, []string{"/bin/sh"}) || !f.SSHD {
		t.Errorf("setid %v modified %v sshd %v", f.SetID, f.Modified, f.SSHD)
	}
	if _, ok := f.Texts["/etc/periodic.conf"]; ok || f.Unread["/etc/periodic.conf"] != "it is a symbolic link" {
		t.Errorf("a link is read: %v %v", f.Texts["/etc/periodic.conf"], f.Unread)
	}
	got := map[string]api.JailAuditResult{}
	for _, c := range jailaudit.Evaluate(jailaudit.Input{Facts: f, HasPkgDB: true, Owned: map[string]bool{"/usr/bin/su": true}}) {
		got[c.ID] = c.Result
		if c.ID == "pkg.base_repository" && !strings.Contains(c.Detail, "by /usr/local/etc/pkg/repos/FreeBSD-base.conf") {
			t.Errorf("the file that turns FreeBSD-base on: %s", c.Detail)
		}
	}
	want := map[string]api.JailAuditResult{"root.links": api.AuditCritical, "accounts.uid0": api.AuditCritical, "accounts.passwords": api.AuditWarn,
		"files.setid": api.AuditWarn, "pkg.repositories": api.AuditWarn, "pkg.base_repository": api.AuditWarn, "base.checksums": api.AuditWarn, "sshd.config": api.AuditWarn}
	if !setid {
		delete(want, "files.setid")
	}
	for id, r := range want {
		if got[id] != r {
			t.Errorf("%s: %s, want %s", id, got[id], r)
		}
	}
}

func TestAuditRootRefusesAnotherDirectory(t *testing.T) {
	e := newEnv(t)
	if _, err := e.local.AuditRoot(context.Background(), RootAuditReq{Root: t.TempDir()}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a directory that is not the root of a jail: %v", err)
	}
}

// A tenant cannot make the audit slow or fail by the size of its files: a sparse file on a base program's
// path (1 TiB here) is listed as modified without being read, a file larger than any base program too, and configuration files
// that are too large together (or full of control bytes) are left unread, never a failed audit. A cancelled audit
// stops reading.
func TestAuditRootIsBoundedBySize(t *testing.T) {
	e := newEnv(t)
	root, files := auditTree(t, e)
	os.Chmod(filepath.Join(root, "bin/sh"), 0o755)
	if err := os.Truncate(filepath.Join(root, "bin/sh"), 1<<40); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "usr/lib/libx.so.6")
	os.Chmod(big, 0o644)
	if err := os.Truncate(big, jailaudit.MaxHashFile+1); err != nil {
		t.Fatal(err)
	}
	for i := range jailaudit.MaxRepoFiles {
		b := []byte(strings.Repeat("\x01", 200<<10))
		os.WriteFile(filepath.Join(root, "usr/local/etc/pkg/repos", "r"+strings.Repeat("x", i)+".conf"), b, 0o644)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f, err := e.local.AuditRoot(ctx, RootAuditReq{Root: root, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	mod := strings.Join(f.Modified, "\n")
	if !strings.Contains(mod, "/bin/sh (larger than any base program)") || !strings.Contains(mod, "/usr/lib/libx.so.6 (larger than any base program)") {
		t.Fatalf("modified: %q", f.Modified)
	}
	size := 0
	for _, v := range f.Texts {
		size += len(v)
	}
	if size > jailaudit.MaxTexts || len(f.Unread) == 0 {
		t.Fatalf("texts %d bytes, unread %v", size, f.Unread)
	}
	if b, err := json.Marshal(f); err != nil || len(b) > 4<<20 {
		t.Fatalf("the answer: %d bytes, %v", len(b), err)
	}
	// a cancelled audit stops
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, _, _, err := fileSum(cctx, mustRoot(t, root), "etc/crontab", "1$"); err == nil {
		t.Fatal("a cancelled read went on")
	}
}

func mustRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// pkg 2 records the checksum of a file as "2$" and the BLAKE2b-512 of its content in its own base32 (the encoding was
// compared with what pkg 2.9.2 recorded for /bin/sh of a FreeBSD 16 jail); both kinds are compared.
func TestAuditRootComparesBlake2Sums(t *testing.T) {
	e := newEnv(t)
	root, files := auditTree(t, e)
	b2 := func(s string) string { h := blake2b.Sum512([]byte(s)); return "2$" + pkgBase32(h[:]) }
	for i := range files {
		switch files[i].Path {
		case "/bin/sh":
			files[i].Sum = b2("the shell")
		case "/usr/lib/libx.so.6":
			files[i].Sum = b2("another library")
		}
	}
	f, err := e.local.AuditRoot(context.Background(), RootAuditReq{Root: root, Files: files})
	if err != nil || f.Verified != 3 || !slices.Equal(f.Modified, []string{"/usr/lib/libx.so.6"}) {
		t.Fatalf("%v %d %v", err, f.Verified, f.Modified)
	}
	if got := pkgBase32(make([]byte, 64)); len(got) != 103 {
		t.Fatalf("a 512-bit sum is 103 characters in pkg's base32, got %d", len(got))
	}
}
