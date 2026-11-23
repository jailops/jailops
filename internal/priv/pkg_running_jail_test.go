package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/sdk/api"
)

// The root of a running managed jail takes host pkg -r (it is upgraded while it runs). The checks are a detection: what
// they find comes back as suspected_compromised, all of it.
func TestPkgOnARunningJailRoot(t *testing.T) {
	e := newEnv(t)
	ctx := WithAdmin(context.Background(), "a")
	container := filepath.Join(e.cfg.MountBase, "projects", "demo", "jails", "web")
	root := filepath.Join(container, "root")
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.WriteFile(filepath.Join(container, "spec.json"), []byte(`{"spec":{"image":"freebsd-15-1"}}`), 0o600)
	e.conf(t, "demo_web")
	e.jails.Put(jailos.Jail{Name: "demo_web", Path: root})
	args := []string{"-r", root, "query", "%n"}
	if err := verifyPkg(ctx, e.local, args); err != nil {
		t.Fatalf("a running jail: %v", err)
	}
	// an unmanaged jail running in it is refused too
	e.jails.Put(jailos.Jail{Name: "other_x", Path: root})
	if err := verifyPkg(ctx, e.local, args); err == nil {
		t.Fatal("an unmanaged jail's root was accepted")
	}
	e.jails.Remove(ctx, filepath.Join(e.cfg.ConfDir, "other_x.conf"), "other_x")
	// every pkg -r checks pkg's own trees: a link there makes the jail suspected
	os.MkdirAll(filepath.Join(root, "var", "db", "pkg", "repos"), 0o755)
	os.Symlink("/tmp", filepath.Join(root, "var", "db", "pkg", "repos", "jailops"))
	if err := verifyPkg(ctx, e.local, args); err == nil || !strings.Contains(err.Error(), SuspectedPrefix+"var/db/pkg/repos/jailops is a symbolic link") {
		t.Errorf("a link among pkg's trees: %v", err)
	}
}

// The check before an upgrade looks at the paths pkg touches only. The links FreeBSD's own tools make (tzsetup's
// /etc/localtime), an admin's /var/www -> /usr/local/www, links in a data directory and a commented pkg.conf are
// clean; a link out of the root on a file or a directory of the base (etc/crontab, etc/ssl), a link among pkg's trees
// and a pkg.conf that turns plugins on are findings, all of them named.
func TestCheckRoot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	container := filepath.Join(e.cfg.MountBase, "projects", "demo", "jails", "web")
	root := filepath.Join(container, "root")
	for _, d := range []string{"etc/ssl", "usr/share/zoneinfo/Europe", "usr/local/www", "var/db/pkg", "srv/data", "usr/local/etc"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(container, "spec.json"), []byte(`{"spec":{"image":"freebsd-15-1"}}`), 0o600)
	os.WriteFile(filepath.Join(root, "etc/crontab"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "etc/ssl/openssl.cnf"), []byte("x"), 0o644)
	os.Symlink("/usr/share/zoneinfo/Europe/Berlin", filepath.Join(root, "etc/localtime"))
	os.Symlink("/usr/local/www", filepath.Join(root, "var/www"))
	os.Symlink("/etc/master.passwd", filepath.Join(root, "srv/data/passwd-link"))
	os.Symlink("/usr/share/misc/termcap", filepath.Join(root, "etc/termcap"))
	os.WriteFile(filepath.Join(root, "usr/local/etc/pkg.conf"), []byte("# PKG_ENABLE_PLUGINS = true;\n#PLUGINS [\n/* PLUGINS */\nPKG_ENABLE_PLUGINS = false;\nPACKAGESITE = \"http://example.org/\";\n"), 0o644)
	base := []string{"/etc/crontab", "/etc/ssl/openssl.cnf", "/etc/termcap", "/bin/sh", "/usr/share/zoneinfo/Europe/Berlin"}
	if err := e.local.CheckRoot(ctx, RootCheckReq{Root: root, Paths: base}); err != nil {
		t.Fatalf("a healthy root: %v", err)
	}
	os.Remove(filepath.Join(root, "etc/crontab"))
	os.Symlink("/etc/master.passwd", filepath.Join(root, "etc/crontab"))
	os.RemoveAll(filepath.Join(root, "etc/ssl"))
	os.Symlink(t.TempDir(), filepath.Join(root, "etc/ssl"))
	os.WriteFile(filepath.Join(root, "usr/local/etc/pkg.conf"), []byte("PKG_ENABLE_PLUGINS = true;\nPLUGINS [ \"evil\" ]\n"), 0o644)
	os.Symlink("/tmp", filepath.Join(root, "var/db/pkg/local.sqlite-journal"))
	err := e.local.CheckRoot(ctx, RootCheckReq{Root: root, Paths: base})
	for _, want := range []string{SuspectedPrefix, "etc/crontab is a symbolic link out of the root", "etc/ssl is a symbolic link out of the root", "usr/local/etc/pkg.conf turns pkg plugins on", "var/db/pkg/local.sqlite-journal is a symbolic link"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in %v", want, err)
		}
	}
	if err := e.local.CheckRoot(ctx, RootCheckReq{Root: t.TempDir(), Paths: base}); !api.IsCode(err, api.CodeForbidden) || strings.Contains(err.Error(), SuspectedPrefix) {
		t.Errorf("a directory that is not the root of a jail: %v", err)
	}
}

// A chain of links that stays in the root lexically but leaves it once resolved is a finding (pkg 2.9.2 wrote the
// files of FreeBSD-caroot into the host directory through usr/share/certs -> ../x, usr/x -> /tmp/hostcerts), as is a
// relative link that climbs through another link (usr/lib/foo -> y/../.., usr/lib/y -> /etc). A relative link that
// stays in the root, also through a chain, is not.
func TestCheckRootFollowsChainsOfLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	root := filepath.Join(e.cfg.MountBase, "projects", "demo", "jails", "web", "root")
	for _, d := range []string{"usr/share", "usr/lib", "usr/local/real", "etc", "var/x2"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	host := t.TempDir()
	os.Symlink("../x", filepath.Join(root, "usr/share/certs"))
	os.Symlink(host, filepath.Join(root, "usr/x"))
	os.Symlink("/etc", filepath.Join(root, "usr/lib/y"))
	os.Symlink("y/../..", filepath.Join(root, "usr/lib/foo"))
	// inside: etc/ssl -> ../var/x, var/x -> x2 (a directory of the root)
	os.Symlink("../var/x", filepath.Join(root, "etc/ssl"))
	os.Symlink("x2", filepath.Join(root, "var/x"))
	paths := []string{"/usr/share/certs/trusted/a.pem", "/usr/lib/foo/libc.so", "/etc/ssl/cert.pem"}
	err := e.local.CheckRoot(ctx, RootCheckReq{Root: root, Paths: paths})
	for _, want := range []string{SuspectedPrefix, "usr/share/certs is a symbolic link (to ../x) whose path escapes the root", "usr/lib/foo is a symbolic link (to y/../..) whose path escapes the root"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in %v", want, err)
		}
	}
	if err != nil && strings.Contains(err.Error(), "etc/ssl") {
		t.Errorf("a chain that stays in the root is a finding: %v", err)
	}
	if ents, _ := os.ReadDir(host); len(ents) != 0 {
		t.Errorf("the check wrote to the host: %v", ents)
	}
}
