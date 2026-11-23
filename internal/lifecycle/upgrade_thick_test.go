package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

const rootMPt = "/jailops/projects/alpha/jails/web/root"

// thickUpgradable is a running thick jail (15.0-RELEASE, FreeBSD-runtime 15.0p2) whose repository has 15.0p3.
func thickUpgradable(t *testing.T) *world { return thickRunning(t) }

func pkgCalls(w *world) []string {
	var out []string
	for _, c := range w.Exec.Calls {
		if c[0] == "pkg" {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

// pkgOnRoot lists the pkg calls that work on the root of the jail (not on the scratch root of the catalogue).
func pkgOnRoot(w *world) []string {
	var out []string
	for _, c := range pkgCalls(w) {
		// the preflight query (the helper's detection) reads the check directory's repositories; the run uses the work's
		if strings.HasPrefix(c, "pkg -r "+rootMPt+" ") && !strings.Contains(c, ".upgrade-100-check") {
			out = append(out, c)
		}
	}
	return out
}

// logIndex is the position of the first entry of the fault log that starts with prefix, or -1.
func logIndex(w *world, prefix string, from int) int {
	for i := from; i < len(w.F.Log); i++ {
		if strings.HasPrefix(w.F.Log[i], prefix) {
			return i
		}
	}
	return -1
}

// The base of a thick jail is upgraded while it runs: pkg works on the running root through the helper's chroot
// (pkg-live), the jail is never stopped or started, the snapshot is taken while it runs and kept for a manual rollback.
func TestThickUpgradeInPlace(t *testing.T) {
	w := thickUpgradable(t)
	w.Z.WriteFile(jc+"/root", "/home/user/notes", "keep me")
	spec0, _ := w.Files.ReadFile(bg, jroot+"/spec.json")
	w.Exec.Calls = nil
	w.started = nil
	w.F.Reset(0)
	res, err := upgrade(w, api.JailUpgrade{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Nothing || res.WillStop || res.Snapshot != "jo-pre-upgrade-100" || res.FromRelease != "15.0-RELEASE" || len(res.BasePending) != 1 || res.BasePending[0] != "FreeBSD-runtime" {
		t.Fatalf("%+v", res)
	}
	calls := pkgOnRoot(w)
	repos := "REPOS_DIR=/jailops/projects/alpha/jails/web/.upgrade-100/repos"
	var subs []string
	for _, c := range calls {
		if !strings.HasPrefix(c, "pkg -r "+rootMPt+" -o ABI=FreeBSD:15:amd64 -o OSVERSION=1500999 -o ") || !strings.Contains(c, " -o RUN_SCRIPTS=false ") {
			t.Errorf("call: %s", c)
		}
		if strings.Contains(c, repos) {
			subs = append(subs, c[strings.Index(c, "RUN_SCRIPTS=false ")+len("RUN_SCRIPTS=false "):])
		}
	}
	if got := strings.Join(subs, "|"); got != "update -f -r jailops|upgrade -y -r jailops|query %n %v|check -d -a" {
		t.Fatalf("pkg on the running root: %s", got)
	}
	for _, bad := range []string{"jail stop", "jail start", "zfs rollback"} {
		if i := logIndex(w, bad, 0); i >= 0 {
			t.Fatalf("%s during an upgrade: %v", bad, w.F.Log)
		}
	}
	// fetched before the snapshot, the snapshot before the first change
	fetch, snap, pk := logIndex(w, "exec pkg -r /jailops/projects/alpha/jails/web/.upgrade-100/scratch", 0), logIndex(w, "zfs snapshot", 0), logIndex(w, "exec pkg -r "+rootMPt+" ", logIndex(w, "zfs snapshot", 0))
	if !(fetch >= 0 && fetch < snap && snap < pk) {
		t.Fatalf("order: fetch %d snapshot %d pkg %d\n%v", fetch, snap, pk, w.F.Log)
	}
	for _, p := range w.Files.Paths() {
		if strings.Contains(p, ".upgrade-100") || strings.Contains(p, "-check") {
			t.Fatalf("work file left: %s", p)
		}
	}
	if !w.Jails.IsUp("alpha_web") || len(w.started) != 0 {
		t.Fatalf("the jail was started or is down: %v", w.started)
	}
	if c, _ := w.Z.ReadFile(jc+"/root", "/home/user/notes"); c != "keep me" {
		t.Fatal(c)
	}
	if spec1, _ := w.Files.ReadFile(bg, jroot+"/spec.json"); string(spec1) != string(spec0) {
		t.Fatal("spec.json changed")
	}
	if ok, _ := w.Z.Exists(bg, jc+"/root@jo-pre-upgrade-100"); !ok {
		t.Fatal("no snapshot of the root")
	}
	if got := w.Z.PropOrEmpty(jc+"@jo-pre-upgrade-100", PropCleanupAfter); got != "2026-05-02T12:00:00Z" {
		t.Fatal(got)
	}
	res2, err := w.S.UpgradeJail(bg, "alpha_web", UpgradeRequest{Stamp: "101"})
	if err != nil || !res2.Nothing || res2.Changed {
		t.Fatal(res2, err)
	}
}

func TestThickUpgradeNothingToUpgradeTouchesNothing(t *testing.T) {
	for _, running := range []bool{true, false} {
		name := "running"
		if !running {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			w := thickUpgradable(t)
			if !running {
				if err := w.S.StopJail(bg, "alpha_web"); err != nil {
					t.Fatal(err)
				}
			}
			w.installed["FreeBSD-runtime"] = w.repo["FreeBSD-runtime"]
			before := w.state()
			w.F.Reset(0)
			w.started = nil
			w.Exec.Calls = nil
			res, err := upgrade(w, api.JailUpgrade{})
			if err != nil || !res.Nothing || res.Changed || res.Installed == "" {
				t.Fatal(res, err)
			}
			for _, l := range w.F.Log {
				for _, bad := range []string{"jail stop", "jail start", "zfs snapshot", "jexec", "zfs rollback"} {
					if strings.HasPrefix(l, bad) {
						t.Fatalf("a call that must not happen: %s", l)
					}
				}
			}
			for _, c := range pkgOnRoot(w) {
				if strings.Contains(c, " upgrade ") || strings.Contains(c, " install ") || strings.Contains(c, " fetch ") {
					t.Fatalf("pkg changed something: %s", c)
				}
			}
			if len(w.started) != 0 || len(w.J.Recs) != 0 || w.state() != before {
				t.Fatalf("something changed:\n%s", w.state())
			}
		})
	}
}

// A stopped thick jail is not upgraded: JailOps never starts or stops a jail for an upgrade. Its dry run still answers.
func TestThickUpgradeStoppedJailIsNotStarted(t *testing.T) {
	w := newWorld(t)
	w.thickCreated()
	before := w.state()
	if res, err := upgrade(w, api.JailUpgrade{DryRun: true}); err != nil || !res.Changed {
		t.Fatal(res, err)
	}
	_, err := upgrade(w, api.JailUpgrade{})
	if !api.IsCode(err, api.CodePreconditionFailed) || !strings.Contains(err.Error(), "does not run") {
		t.Fatal(err)
	}
	if w.Jails.IsUp("alpha_web") || w.state() != before {
		t.Fatalf("the stopped jail was touched:\n%s", w.state())
	}
}

func TestThickUpgradeAddWithReleaseIsInvalid(t *testing.T) {
	w := thickUpgradable(t)
	before := w.state()
	w.F.Reset(0)
	_, err := upgrade(w, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh"}, Release: "15.0-RELEASE"})
	wantCode(t, err, api.CodeInvalid)
	if !strings.Contains(err.Error(), "release") || w.state() != before || w.F.Count() != 0 {
		t.Fatalf("%v", err)
	}
}

func TestThickUpgradeRelease(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		release string
		code    api.Code
		message string
	}{
		{"a bad spelling", "16.0-CURRENT", "sixteen", api.CodeInvalid, "not a FreeBSD release"},
		{"no type", "16.0-CURRENT", "15.1", api.CodeInvalid, "not a FreeBSD release"},
		{"older", "16.0-CURRENT", "14.2-RELEASE", api.CodeInvalid, "older"},
		{"newer than the kernel", "15.1-RELEASE", "15.2-RELEASE", api.CodeVersionSkew, "kernel"},
		{"newer major than the kernel", "15.1-RELEASE", "16.0-CURRENT", api.CodeVersionSkew, "kernel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := thickUpgradable(t)
			w.Jails.Release = tc.host
			before := w.state()
			w.F.Reset(0)
			_, err := upgrade(w, api.JailUpgrade{Release: tc.release})
			wantCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) || w.state() != before {
				t.Fatalf("%v", err)
			}
			for _, l := range w.F.Log {
				if strings.HasPrefix(l, "jail ") || strings.HasPrefix(l, "zfs ") {
					t.Fatalf("a refused request touched the jail: %s", l)
				}
			}
		})
	}
	// accepted spellings
	for _, r := range []string{"15.2-RELEASE", "15.2-STABLE", "16.0-CURRENT", "15.2-PRERELEASE", "15.2-BETA1", "15.2-RC2", "16.0-ALPHA3"} {
		if !reReleaseArg.MatchString(r) {
			t.Errorf("%s refused", r)
		}
	}
	for _, r := range []string{"15.2", "15.2-release", "15-RELEASE", "15.2-RELEASE-p1", "15.2-RELEASE ", "-RELEASE"} {
		if reReleaseArg.MatchString(r) {
			t.Errorf("%s accepted", r)
		}
	}
}

func TestThickUpgradeToANewRelease(t *testing.T) {
	w := thickUpgradable(t)
	w.Jails.Release = "15.1-RELEASE"
	w.pkgVersion = "15.1p2"
	w.repo["FreeBSD-runtime"] = "15.1p2"
	spec0, _ := w.Files.ReadFile(bg, jroot+"/spec.json")
	w.Exec.Calls = nil
	res, err := upgrade(w, api.JailUpgrade{Release: "15.1-RELEASE"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.FromRelease != "15.0-RELEASE" || res.ToRelease != "15.1-RELEASE" {
		t.Fatalf("%+v", res)
	}
	calls := pkgOnRoot(w)
	if len(calls) != 4 || !strings.HasSuffix(calls[1], "upgrade -f -y -r jailops") {
		t.Fatalf("pkg calls: %v", calls)
	}
	// nothing is recorded: the image the jail was made from stays
	spec1, _ := w.Files.ReadFile(bg, jroot+"/spec.json")
	if string(spec1) != string(spec0) || w.Z.PropOrEmpty(jc, PropImage) != "fb" || w.Z.PropOrEmpty(jc+"/root", PropGen) != "1" {
		t.Fatal("the upgrade rewrote the record")
	}
}

// A failure of pkg is reported and left: the jail runs on as pkg left it, nothing rolls back on its own (that would lose
// what the services wrote), the snapshot stays for a manual rollback, and the work directory goes.
func TestThickUpgradeFailureIsReportedAndLeft(t *testing.T) {
	w := thickUpgradable(t)
	w.Z.WriteFile(jc+"/root", "/usr/bin/tool", "old")
	w.started = nil
	w.F.Reset(0)
	w.pkgFail = func(sub string, argv []string) error {
		if sub == "upgrade" {
			w.Z.WriteFile(jc+"/root", "/usr/bin/tool", "half new")
			return errors.New("exit status 1")
		}
		return nil
	}
	_, err := upgrade(w, api.JailUpgrade{})
	if err == nil || !strings.Contains(err.Error(), "pkg -r") {
		t.Fatal(err)
	}
	if c, _ := w.Z.ReadFile(jc+"/root", "/usr/bin/tool"); c != "half new" {
		t.Fatalf("the root was changed back: %q", c)
	}
	if !w.Jails.IsUp("alpha_web") || len(w.started) != 0 || logIndex(w, "jail stop", 0) >= 0 {
		t.Fatalf("the jail was stopped or started: %v", w.F.Log)
	}
	if ok, _ := w.Z.Exists(bg, jc+"/root@jo-pre-upgrade-100"); !ok {
		t.Fatal("the snapshot for a manual rollback is gone")
	}
}

// A health check that fails after the upgrade is reported, with its command and output; nothing is rolled back or restarted.
func TestThickUpgradeHealthCheckFailureIsReported(t *testing.T) {
	w := thickUpgradable(t)
	upgraded := false
	w.Jails.ExecFn = func(name string, argv []string) ([]byte, error) {
		if !upgraded || argv[0] != "/bin/true" {
			return nil, nil
		}
		return []byte("connection refused"), errors.New("exit 1")
	}
	w.pkgFail = func(sub string, argv []string) error {
		if sub == "upgrade" {
			upgraded = true
			w.Z.WriteFile(jc+"/root", "/usr/bin/tool", "new")
		}
		return nil
	}
	_, err := upgrade(w, api.JailUpgrade{})
	var he *HealthError
	if !errors.As(err, &he) || he.Output != "connection refused" {
		t.Fatal(err)
	}
	if c, _ := w.Z.ReadFile(jc+"/root", "/usr/bin/tool"); c != "new" || !w.Jails.IsUp("alpha_web") {
		t.Fatalf("rolled back or stopped: %q", c)
	}
}

func TestThickUpgradeDryRunChangesNothing(t *testing.T) {
	w := thickUpgradable(t)
	before := w.state()
	w.F.Reset(0)
	res, err := upgrade(w, api.JailUpgrade{DryRun: true})
	if err != nil || !res.Changed {
		t.Fatal(res, err)
	}
	for _, l := range w.F.Log {
		for _, bad := range []string{"jail ", "zfs "} {
			if strings.HasPrefix(l, bad) {
				t.Fatalf("a dry run did: %s", l)
			}
		}
	}
	if w.state() != before {
		t.Fatal("a dry run changed something")
	}
	p, _, err := w.S.PlanUpgrade(bg, "alpha_web", UpgradeRequest{JailUpgrade: api.JailUpgrade{DryRun: true}, Stamp: "100"})
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
	var names []string
	for _, s := range p.Steps {
		names = append(names, s.Name)
	}
	want := "prefetch snapshot write-repo-conf pkg-update pkg-upgrade verify-release verify-deps clean-work settle verify-sums restart-pending health-check"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("plan: %s", got)
	}
}

// Every failure the plan can meet (each mutating operation failing in turn) leaves the jail running: nothing stops it,
// nothing starts it, and the journal is clean.
func TestThickUpgradeFailureSweep(t *testing.T) {
	w := thickUpgradable(t)
	w.F.Reset(0)
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	n := w.F.Count()
	for k := 1; k <= n; k++ {
		w := thickUpgradable(t)
		w.F.Reset(k)
		_, _ = upgrade(w, api.JailUpgrade{})
		if !w.Jails.IsUp("alpha_web") {
			t.Fatalf("failing op %d (%s) left the jail down", k, w.F.Log[k-1])
		}
		for _, l := range w.F.Log {
			if strings.HasPrefix(l, "jail stop") || strings.HasPrefix(l, "jail start") || strings.HasPrefix(l, "zfs rollback") {
				t.Fatalf("failing op %d: %s", k, l)
			}
		}
		if len(w.J.Recs) != 0 {
			t.Fatalf("op %d: journal not cleaned: %v", k, w.J.Recs)
		}
	}
	t.Logf("%d injected failures, the jail ran through each", n)
}

// A stopped jail is refused before any mutating operation.
func TestThickUpgradeStoppedJailChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.thickCreated()
	w.F.Reset(0)
	_, _ = upgrade(w, api.JailUpgrade{})
	for _, l := range w.F.Log {
		if strings.HasPrefix(l, "zfs snapshot") || strings.HasPrefix(l, "jail start") || strings.HasPrefix(l, "exec pkg -r "+rootMPt) {
			t.Fatalf("a stopped jail: %s", l)
		}
	}
}

func TestThickUpgradeLinuxJailIsRefused(t *testing.T) {
	w := thickUpgradable(t)
	rec, _ := w.S.readRecord(bg, "alpha_web")
	rec.Spec.OS = api.OSLinux
	if err := w.Files.WriteAtomic(bg, jroot+"/spec.json", marshal(rec)); err != nil {
		t.Fatal(err)
	}
	_, err := upgrade(w, api.JailUpgrade{})
	wantCode(t, err, api.CodeUnsupported)
}

func TestThickUpgradeSnapshotIsCleanedAfterTheRetention(t *testing.T) {
	w := thickUpgradable(t)
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.S.CleanupRootOld(bg, "alpha_web"); ok || err != nil {
		t.Fatal("retention not over", ok, err)
	}
	w.now = w.now.Add(25 * time.Hour)
	if ok, err := w.S.CleanupRootOld(bg, "alpha_web"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	for _, d := range []string{jc, jc + "/root"} {
		if ok, _ := w.Z.Exists(bg, d+"@jo-pre-upgrade-100"); ok {
			t.Fatalf("snapshot of %s survived", d)
		}
	}
	// an unmarked snapshot of the same name is never touched
	if err := w.Z.Snapshot(bg, nil, jc+"@jo-pre-upgrade-200"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.S.CleanupRootOld(bg, "alpha_web"); ok {
		t.Fatal("unmarked snapshot destroyed")
	}
}

const baseRepoPath = jroot + "/root/usr/local/etc/pkg/repos/FreeBSD-base.conf"

// The FreeBSD-base repository stays disabled inside a thick jail (contract rule 2), also when its image is older than
// the file that says so (FreeBSD 16 ships the repository enabled): the creation writes the file that disables it, and
// an upgrade never enables it.
func TestThickJailKeepsTheBaseRepoDisabled(t *testing.T) {
	w := thickUpgradable(t)
	b, err := w.Files.ReadFile(bg, baseRepoPath)
	if err != nil || !strings.Contains(string(b), "FreeBSD-base: { enabled: no }") {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range w.Files.Paths() {
		if strings.Contains(p, "/root/usr/local/etc/pkg/repos/") && p != baseRepoPath {
			t.Fatalf("another repository file in the root: %s", p)
		}
	}
	if b2, _ := w.Files.ReadFile(bg, baseRepoPath); string(b2) != string(b) {
		t.Fatalf("the upgrade changed the file: %q", b2)
	}
}

// The ports part of the same transaction: with only ports packages newer the jail is never stopped; with both parts the
// ports run after the base, once the jail runs; a failure in either part puts the root back.
func TestThickUpgradePortsInTheSameTransaction(t *testing.T) {
	ports := func(w *world, calls *[]string, fail bool) *PortsUpgrade {
		return &PortsUpgrade{Pending: []string{"nginx"}, Run: func(ctx context.Context) error {
			*calls = append(*calls, "ports")
			ok, _ := w.Z.Exists(ctx, jc+"/root@jo-pre-upgrade-100")
			if !ok {
				return errors.New("the snapshot is not there before the first ports change")
			}
			w.Z.WriteFile(jc+"/root", "/usr/local/bin/nginx", "new")
			if fail {
				return errors.New("pkg: exit 1")
			}
			return nil
		}}
	}
	run := func(w *world, p *PortsUpgrade, noPackages bool) (UpgradeResult, error) {
		return w.S.UpgradeJail(bg, "alpha_web", UpgradeRequest{JailUpgrade: api.JailUpgrade{NoPackages: noPackages}, Stamp: "100", Ports: p})
	}
	t.Run("only ports: no stop", func(t *testing.T) {
		w := thickUpgradable(t)
		w.installed["FreeBSD-runtime"] = w.repo["FreeBSD-runtime"]
		w.F.Reset(0)
		var calls []string
		res, err := run(w, ports(w, &calls, false), false)
		if err != nil || !res.Changed || len(calls) != 1 || len(res.PortsPending) != 1 {
			t.Fatal(res, err, calls)
		}
		for _, l := range w.F.Log {
			if strings.HasPrefix(l, "jail stop") || strings.HasPrefix(l, "jail start") {
				t.Fatalf("a ports-only upgrade stopped the jail: %s", l)
			}
		}
		if !w.Jails.IsUp("alpha_web") {
			t.Fatal("not running")
		}
	})
	t.Run("only ports failing: reported and left", func(t *testing.T) {
		w := thickUpgradable(t)
		w.installed["FreeBSD-runtime"] = w.repo["FreeBSD-runtime"]
		w.F.Reset(0)
		var calls []string
		if _, err := run(w, ports(w, &calls, true), false); err == nil {
			t.Fatal("the failure was not reported")
		}
		if c, _ := w.Z.ReadFile(jc+"/root", "/usr/local/bin/nginx"); c != "new" || !w.Jails.IsUp("alpha_web") {
			t.Fatalf("rolled back or stopped: %q", c)
		}
		if logIndex(w, "jail stop", 0) >= 0 || logIndex(w, "zfs rollback", 0) >= 0 {
			t.Fatalf("%v", w.F.Log)
		}
	})
	t.Run("base and ports: one snapshot, ports after the base, no stop", func(t *testing.T) {
		w := thickUpgradable(t)
		w.F.Reset(0)
		var calls []string
		if _, err := run(w, ports(w, &calls, false), false); err != nil {
			t.Fatal(err)
		}
		snap := logIndex(w, "zfs snapshot", 0)
		if logIndex(w, "zfs snapshot", snap+1) >= 0 {
			t.Fatal("more than one snapshot")
		}
		if len(calls) != 1 || snap < 0 || logIndex(w, "jail stop", 0) >= 0 || logIndex(w, "jail start", 0) >= 0 {
			t.Fatalf("%v %v", calls, w.F.Log)
		}
	})
	t.Run("no ports packages: no ports step, said in the checks", func(t *testing.T) {
		w := thickUpgradable(t)
		res, err := run(w, &PortsUpgrade{None: true}, false)
		if err != nil || !res.Changed || res.State.PortsStep || len(res.PortsPending) != 0 ||
			!slices.Contains(res.Checks, "no ports packages: the ports catalogue is not fetched and there is no ports step") {
			t.Fatal(res, err)
		}
	})
	t.Run("no-packages skips the ports part", func(t *testing.T) {
		w := thickUpgradable(t)
		w.installed["FreeBSD-runtime"] = w.repo["FreeBSD-runtime"]
		var calls []string
		res, err := run(w, ports(w, &calls, false), true)
		if err != nil || !res.Nothing || len(calls) != 0 {
			t.Fatal(res, err, calls)
		}
	})
	t.Run("failures never stop the jail", func(t *testing.T) {
		w := thickUpgradable(t)
		w.F.Reset(0)
		var calls []string
		if _, err := run(w, ports(w, &calls, false), false); err != nil {
			t.Fatal(err)
		}
		for k := 1; k <= w.F.Count(); k++ {
			w2 := thickUpgradable(t)
			w2.F.Reset(k)
			var c2 []string
			_, _ = run(w2, ports(w2, &c2, false), false)
			if !w2.Jails.IsUp("alpha_web") || logIndex(w2, "jail stop", 0) >= 0 {
				t.Fatalf("failing op %d stopped the jail", k)
			}
		}
	})
}

// A jail that is unhealthy before the upgrade is not upgraded (its rollback point would be broken) unless forced.
func TestThickUpgradeRefusesAnUnhealthyJail(t *testing.T) {
	w := thickUpgradable(t)
	w.Jails.ExecFn = func(name string, argv []string) ([]byte, error) { return []byte("down"), errors.New("exit 1") }
	before := w.state()
	w.F.Reset(0)
	_, err := upgrade(w, api.JailUpgrade{})
	wantCode(t, err, api.CodePreconditionFailed)
	if !strings.Contains(err.Error(), "--force") || w.state() != before {
		t.Fatal(err)
	}
	for _, l := range w.F.Log {
		if strings.HasPrefix(l, "jail ") || strings.HasPrefix(l, "zfs ") {
			t.Fatalf("touched: %s", l)
		}
	}
}

// The repository that cannot be read stops everything before the jail is touched, with the repository in the message.
func TestThickUpgradeUnreachableRepository(t *testing.T) {
	w := thickUpgradable(t)
	w.pkgFail = func(sub string, argv []string) error {
		if sub == "update" {
			return errors.New("Unable to update repository jailops: 404")
		}
		return nil
	}
	before := w.state()
	w.F.Reset(0)
	_, err := upgrade(w, api.JailUpgrade{})
	wantCode(t, err, api.CodePreconditionFailed)
	if !strings.Contains(err.Error(), "base_release_0") || !strings.Contains(err.Error(), "404") || w.state() != before {
		t.Fatal(err)
	}
	for _, l := range w.F.Log {
		if strings.HasPrefix(l, "jail ") || strings.HasPrefix(l, "zfs ") {
			t.Fatalf("touched: %s", l)
		}
	}
}

// The helper takes at most 64 arguments of one pkg command: a move of every package is fetched in chunks.
func TestThickUpgradeFetchesInChunks(t *testing.T) {
	w := thickUpgradable(t)
	for i := 0; i < 130; i++ {
		n := "FreeBSD-lib" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		w.installed[n], w.repo[n] = "15.0", "15.0p1"
	}
	w.Exec.Calls = nil
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	fetches, names := 0, 0
	for _, c := range pkgCalls(w) {
		if i := strings.Index(c, " fetch "); i >= 0 {
			fetches++
			args := strings.Fields(c[i:])
			if len(args) > 60 {
				t.Fatalf("a fetch with %d arguments", len(args))
			}
			names += len(args) - 5
		}
	}
	if fetches < 4 || names < 130 {
		t.Fatalf("%d fetches, %d names", fetches, names)
	}
}

// The release of a thick jail is the one installed in it (the userland version of /bin/freebsd-version, which carries the
// type and the patch level), also when the jail is stopped; the version of the FreeBSD-runtime package of a branch has
// no minor and tells nothing.
func TestThickInstalledRelease(t *testing.T) {
	for _, running := range []bool{true, false} {
		w := thickUpgradable(t)
		if !running {
			if err := w.S.StopJail(bg, "alpha_web"); err != nil {
				t.Fatal(err)
			}
		}
		w.installed["FreeBSD-runtime"] = w.repo["FreeBSD-runtime"]
		w.Files.Data[jroot+"/root/bin/freebsd-version"] = []byte("#!/bin/sh\nUSERLAND_VERSION=\"15.0-STABLE\"\n")
		res, err := upgrade(w, api.JailUpgrade{})
		if err != nil || res.Installed != "15.0-STABLE" || res.FromRelease != "15.0-STABLE" {
			t.Fatalf("running %v: %+v %v", running, res, err)
		}
		// a branch reads base_latest
		found := false
		for _, c := range w.Exec.Calls {
			found = found || strings.Contains(strings.Join(c, " "), "rquery")
		}
		if !found {
			t.Fatal("no catalogue read")
		}
	}
	w := thickUpgradable(t)
	w.installed["FreeBSD-runtime"] = "15.snap20261120121141"
	_, err := upgrade(w, api.JailUpgrade{})
	wantCode(t, err, api.CodeInternal)
}

// Not enough room is refused before anything is stopped, with the numbers; a dry run reports it and goes on.
func TestThickUpgradeRefusesWhenThereIsNoRoom(t *testing.T) {
	w := thickUpgradable(t)
	w.Z.Set(bg, jc+"/root", map[string]string{"available": "1048576"})
	before := w.state()
	w.F.Reset(0)
	_, err := upgrade(w, api.JailUpgrade{})
	wantCode(t, err, api.CodeQuotaExceeded)
	if !strings.Contains(err.Error(), "1 MiB free") || !strings.Contains(err.Error(), "needs about") || w.state() != before {
		t.Fatal(err)
	}
	for _, l := range w.F.Log {
		if strings.HasPrefix(l, "jail ") || strings.HasPrefix(l, "zfs snapshot") {
			t.Fatalf("touched: %s", l)
		}
	}
	res, err := upgrade(w, api.JailUpgrade{DryRun: true})
	if err != nil || len(res.Failed) != 1 || !strings.Contains(res.Failed[0], "free in its root") {
		t.Fatal(res.Failed, err)
	}
	// the pool that holds the cache
	w2 := thickUpgradable(t)
	w2.Z.Set(bg, "tank/jailops", map[string]string{"available": "1048576"})
	_, err = upgrade(w2, api.JailUpgrade{})
	wantCode(t, err, api.CodePoolExhausted)
}
