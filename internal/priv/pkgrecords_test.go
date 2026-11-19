package priv

import (
	"context"
	"jailops.net/jailops/internal/sys/jailos"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/run"
)

const recManifest = "name = \"FreeBSD-runtime\";\nversion = \"15.1p5\";\nscripts {\n  post-install = \"true\";\n}\n"

// recEnv is a jail root cloned from a generation, both mounted, and a manifest the daemon wrote.
func recEnv(t *testing.T) (e *env, tree, manifest string) {
	e = newEnv(t)
	tree = filepath.Join(e.cfg.MountBase, "projects", "p", "jails", "web", "root")
	gen := filepath.Join(e.cfg.MountBase, "images", "x", "g2")
	for _, d := range []string{tree, gen, filepath.Join(e.cfg.StateDir, "work")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest = filepath.Join(e.cfg.StateDir, "work", "FreeBSD-runtime.ucl")
	if err := os.WriteFile(manifest, []byte(recManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	e.runner.On("mount", []string{"-p"}, "tank/jailops/projects/p/jails/web/root "+tree+" zfs rw 0 0\ntank/jailops/images/x/g2 "+gen+" zfs rw 0 0\n")
	e.runner.On("zfs", []string{"get", "-H", "-o", "value", "origin", "tank/jailops/projects/p/jails/web/root"}, "tank/jailops/images/x/g2@ro\n")
	e.runner.On("/usr/sbin/pkg", []string{"-r", gen, "info", "--raw", "--raw-format", "ucl", "FreeBSD-runtime"}, recManifest)
	return
}

func registerArgv(tree, manifest string) []string {
	return []string{"/usr/sbin/pkg", "-r", tree, "register", "-t", "-M", manifest}
}

// The manifest a register reads is the one the image generation of the jail has, as a copy the daemon cannot change.
func TestSealRecordManifestRegistersWhatTheGenerationSays(t *testing.T) {
	e, tree, manifest := recEnv(t)
	argv, cleanup, err := e.local.sealRecordManifest(context.Background(), registerArgv(tree, manifest))
	if err != nil {
		t.Fatal(err)
	}
	sealed := argv[6]
	if sealed == manifest || !strings.HasPrefix(sealed, e.cfg.StateDir+"/") {
		t.Fatalf("pkg reads %q: not a copy of the helper's own", sealed)
	}
	if fi, err := os.Stat(sealed); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if err := os.WriteFile(manifest, []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(sealed); string(b) != recManifest {
		t.Fatalf("the copy followed the original: %q", b)
	}
	cleanup()
	if _, err := os.Stat(sealed); err == nil {
		t.Fatal("the copy was not removed")
	}
}

func TestSealRecordManifestRefusesWhatTheGenerationDoesNotSay(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(e *env, tree, manifest string)
		want   string
	}{
		"a script added to the manifest": {func(e *env, tree, m string) {
			os.WriteFile(m, []byte(recManifest+"scripts { pre-deinstall = \"rm -rf /\"; }\n"), 0o644)
		}, "not the one of the image generation"},
		"one byte different": {func(e *env, tree, m string) {
			os.WriteFile(m, []byte(strings.Replace(recManifest, "p5", "p6", 1)), 0o644)
		}, "not the one of the image generation"},
		"a link in place of the file": {func(e *env, tree, m string) {
			os.Remove(m)
			os.Symlink("/etc/master.passwd", m)
		}, ""},
		"a name that is not a package": {func(e *env, tree, m string) {
			os.Rename(m, filepath.Join(filepath.Dir(m), "..x.ucl"))
		}, "not the manifest of a package"},
		"a root that is not a clone": {func(e *env, tree, m string) {
			e.runner.Prepend(run.Rule{Name: "zfs", Args: []string{"get", "-H", "-o", "value", "origin", "tank/jailops/projects/p/jails/web/root"}, Result: run.Result{Stdout: []byte("-\n")}})
		}, "not a clone of an image generation"},
		"a tree nothing is mounted at": {func(e *env, tree, m string) {
			e.runner.Prepend(run.Rule{Name: "mount", Args: []string{"-p"}, Result: run.Result{Stdout: []byte("")}})
		}, "nothing is mounted"},
		"a generation that is not mounted": {func(e *env, tree, m string) {
			e.runner.Prepend(run.Rule{Name: "mount", Args: []string{"-p"}, Result: run.Result{Stdout: []byte("tank/jailops/projects/p/jails/web/root " + tree + " zfs rw 0 0\n")}})
		}, "is not mounted"},
	} {
		e, tree, manifest := recEnv(t)
		tc.change(e, tree, manifest)
		argv := registerArgv(tree, manifest)
		if name == "a name that is not a package" {
			argv[6] = filepath.Join(filepath.Dir(manifest), "..x.ucl")
		}
		_, _, err := e.local.sealRecordManifest(context.Background(), argv)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a refusal with %q", name, err, tc.want)
		}
	}
}

// The other forms are not touched.
func TestSealRecordManifestLeavesTheReadsAlone(t *testing.T) {
	e, tree, _ := recEnv(t)
	for _, argv := range [][]string{
		{"/usr/sbin/pkg", "-r", tree, "info", "-a", "-q"},
		{"/usr/sbin/pkg", "-r", tree, "unregister", "-f", "-y", "FreeBSD-runtime"},
	} {
		got, cleanup, err := e.local.sealRecordManifest(context.Background(), argv)
		cleanup()
		if err != nil || strings.Join(got, " ") != strings.Join(argv, " ") {
			t.Errorf("%v: %v %v", argv, got, err)
		}
	}
}

// The helper opens a package database as root only in a tree no jail runs in, and only a real one.
func TestPkgRecordsRefusesARunningJailAndALinkedDatabase(t *testing.T) {
	e, tree, _ := recEnv(t)
	for _, d := range []string{"var/db/pkg", "var/log"} {
		if err := os.MkdirAll(filepath.Join(tree, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"-r", tree, "info", "-a", "-q"}
	if err := verifyPkgRecords(context.Background(), e.local, argv); err != nil {
		t.Fatalf("a stopped jail with a real database: %v", err)
	}
	e.jails.Put(jailos.Jail{Name: "alpha_web", Path: tree})
	if err := verifyPkgRecords(context.Background(), e.local, argv); err == nil || !strings.Contains(err.Error(), "running jail") {
		t.Fatalf("a running jail: %v", err)
	}
	for name, mk := range map[string]func(tree string){
		"var/db/pkg is a link": func(tree string) {
			os.RemoveAll(filepath.Join(tree, "var/db/pkg"))
			os.Symlink("/var/db/pkg", filepath.Join(tree, "var/db/pkg"))
		},
		"the database is a link": func(tree string) {
			os.RemoveAll(filepath.Join(tree, "var/db/pkg"))
			os.MkdirAll(filepath.Join(tree, "var/db/pkg"), 0o755)
			os.Symlink("/var/db/pkg/local.sqlite", filepath.Join(tree, "var/db/pkg/local.sqlite"))
		},
		"var is a link": func(tree string) {
			os.RemoveAll(filepath.Join(tree, "var"))
			os.Symlink("/var", filepath.Join(tree, "var"))
		},
	} {
		e, tree, _ := recEnv(t)
		os.MkdirAll(filepath.Join(tree, "var/db/pkg"), 0o755)
		argv := []string{"-r", tree, "info", "-a", "-q"}
		mk(tree)
		if err := verifyPkgRecords(context.Background(), e.local, argv); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
