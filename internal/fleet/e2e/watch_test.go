package e2e

import (
	"jailops.net/jailops/internal/fleet/fleettest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for the watch tests: %v", err)
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestWatchPullsPlansAndApplies(t *testing.T) {
	e := standardFleet(t)
	remote := t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main", ".")
	git(t, e.Dir, "init", "-b", "main", ".")
	git(t, e.Dir, "add", "-A")
	git(t, e.Dir, "commit", "-m", "initial")
	git(t, e.Dir, "remote", "add", "origin", remote)
	git(t, e.Dir, "push", "-u", "origin", "main")

	// Without --apply watch only reports.
	r := mustOK(t, run(t, e, "watch", "--once"))
	contains(t, r.out, "create")
	if _, ok := e.Hosts["web1"].Server.JailIn(fleettest.Project, "blog"); ok {
		t.Fatal("watch without --apply changed a host")
	}

	// Someone else pushes a new jail.
	other := t.TempDir()
	git(t, other, "clone", "-q", remote, ".")
	if err := os.MkdirAll(filepath.Join(other, "projects/main/jails"), 0o755); err != nil {
		t.Fatal(err)
	}
	extra, _ := os.ReadFile(filepath.Join(e.Dir, "projects/main/jails/wiki.yaml"))
	extra = []byte(strings.ReplaceAll(string(extra), "name: wiki", "name: extra"))
	if err := os.WriteFile(filepath.Join(other, "projects/main/jails/extra.yaml"), extra, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "add extra")
	git(t, other, "push", "-q", "origin", "main")

	r = mustOK(t, run(t, e, "watch", "--once", "--apply"))
	contains(t, r.out, "repository updated to")
	if _, ok := e.Hosts["web2"].Server.JailIn(fleettest.Project, "extra"); !ok {
		t.Fatalf("the pulled jail was not applied:\n%s", r.all())
	}
	if _, ok := e.Hosts["web1"].Server.JailIn(fleettest.Project, "blog"); !ok {
		t.Fatal("the existing jail was not applied")
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "projects/main/jails/extra.yaml")); err != nil {
		t.Fatal("the repository was not fast-forwarded")
	}

	// Nothing new upstream and nothing drifted.
	r = mustOK(t, run(t, e, "watch", "--once"))
	contains(t, r.out, "no drift")

	// A dirty tree is never pulled over or applied.
	edited, _ := os.ReadFile(filepath.Join(e.Dir, "projects/main/jails/extra.yaml"))
	if err := os.WriteFile(filepath.Join(e.Dir, "projects/main/jails/extra.yaml"), append(edited, "# local edit\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	mustFail(t, run(t, e, "watch", "--once", "--apply"), "dirty")
	// sync --apply refuses the same way unless told otherwise.
	mustFail(t, run(t, e, "sync", "--apply"), "dirty")
}

func TestWatchRejectsBadCommitButKeepsRunning(t *testing.T) {
	e := standardFleet(t)
	remote := t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main", ".")
	git(t, e.Dir, "init", "-b", "main", ".")
	git(t, e.Dir, "add", "-A")
	git(t, e.Dir, "commit", "-m", "initial")
	git(t, e.Dir, "remote", "add", "origin", remote)
	git(t, e.Dir, "push", "-u", "origin", "main")
	other := t.TempDir()
	git(t, other, "clone", "-q", remote, ".")
	if err := os.WriteFile(filepath.Join(other, "shared/networks/broken.yaml"), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "broken")
	git(t, other, "push", "-q", "origin", "main")
	mustFail(t, run(t, e, "watch", "--once", "--apply"), "broken.yaml")
	if _, ok := e.Hosts["web1"].Server.Network("internal"); ok {
		t.Fatal("an invalid repository was applied")
	}
}
