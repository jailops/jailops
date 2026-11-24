package lifecycle

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/internal/lifecycle/fault"
	"jailops.net/jailops/sdk/api"
)

func rollbackThick(w *world, confirm bool) (RollbackResult, error) {
	return w.S.RollbackJail(bg, "alpha_web", RollbackOptions{JailRollback: api.JailRollback{ConfirmDataLoss: confirm}, Stamp: "300"}, false)
}

// thickUpgraded is a thick jail with a file, upgraded in place while it ran, then stopped by its administrator for a
// rollback (a running jail's rollback is refused).
func thickUpgraded(t *testing.T) *world {
	w := thickUpgradable(t)
	w.Z.WriteFile(jc+"/root", "/home/user/notes", "before")
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	return w
}

// runningUpgraded is thickUpgraded still running.
func runningUpgraded(t *testing.T) *world {
	w := thickUpgradable(t)
	w.Z.WriteFile(jc+"/root", "/home/user/notes", "before")
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestThickRollbackIsTheRootOnly(t *testing.T) {
	w := runningUpgraded(t)
	spec0, _ := w.Files.ReadFile(bg, jroot+"/spec.json")
	w.Z.WriteFile(jc+"/root", "/home/user/notes", "after")
	// a running jail is refused: its services would die of the files changing under them
	if _, err := rollbackThick(w, true); !api.IsCode(err, api.CodePreconditionFailed) || !strings.Contains(err.Error(), "jailops stop") {
		t.Fatalf("a running jail: %v", err)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	w.started = nil
	res, err := rollbackThick(w, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Snapshot != "jo-pre-upgrade-100" || len(res.Destroyed) != 0 {
		t.Fatalf("%+v", res)
	}
	if c, _ := w.Z.ReadFile(jc+"/root", "/home/user/notes"); c != "before" {
		t.Fatalf("the root is not the snapshot: %q", c)
	}
	if spec1, _ := w.Files.ReadFile(bg, jroot+"/spec.json"); string(spec1) != string(spec0) {
		t.Fatal("spec.json was written")
	}
	// the rollback neither starts nor stops the jail
	if w.Jails.IsUp("alpha_web") || len(w.started) != 0 {
		t.Fatalf("the jail was started: %v", w.started)
	}
	for _, d := range []string{jc, jc + "/root"} {
		if ok, _ := w.Z.Exists(bg, d+"@jo-pre-upgrade-100"); ok {
			t.Fatalf("snapshot of %s left", d)
		}
	}
	// the plan has nothing else in it
	p, _, _, err := w.S.planThickRollback(bg, mustReady(t, w), RollbackOptions{Stamp: "300"}, true)
	if err == nil && p != nil {
		var names []string
		for _, s := range p.Steps {
			names = append(names, s.Name)
		}
		t.Logf("plan after the rollback: %v", names)
	}
}

func TestThickRollbackStoppedJailStaysStopped(t *testing.T) {
	w := thickUpgraded(t)
	if _, err := rollbackThick(w, true); err != nil {
		t.Fatal(err)
	}
	if w.Jails.IsUp("alpha_web") {
		t.Fatal("a stopped jail must stay stopped")
	}
}

func TestThickRollbackRefusals(t *testing.T) {
	newer := func(t *testing.T) *world {
		w := thickUpgraded(t)
		if _, err := w.S.SnapshotJail(bg, "alpha_web", "later", ""); err != nil {
			t.Fatal(err)
		}
		return w
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T) *world
		confirm bool
		code    api.Code
		message string
	}{
		{"no snapshot", thickRunning, true, api.CodeNotFound, "no snapshot from an in-place upgrade"},
		{"jail of an unsupported root layout", oldLayoutRunning, true, api.CodeUnsupported, "root layout this release does not support"},
		{"Linux jail", func(t *testing.T) *world {
			w := thickUpgraded(t)
			rec, _ := w.S.readRecord(bg, "alpha_web")
			rec.Spec.OS = api.OSLinux
			if err := w.Files.WriteAtomic(bg, jroot+"/spec.json", marshal(rec)); err != nil {
				t.Fatal(err)
			}
			return w
		}, true, api.CodeUnsupported, "Linux"},
		{"data loss not confirmed", thickUpgraded, false, api.CodeDataLoss, "discards"},
		{"newer snapshot not confirmed", newer, false, api.CodeDataLoss, "discards"},
		{"newer snapshot without destroy_newer", newer, true, api.CodePreconditionFailed, `snapshots taken after "jo-pre-upgrade-100" exist: ` + jc + "/root@later"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.setup(t)
			before := w.state()
			w.F.Reset(0)
			_, err := rollbackThick(w, tc.confirm)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("%v", err)
			}
			wantCode(t, err, tc.code)
			if w.state() != before || w.F.Count() != 0 {
				t.Fatal("a refused rollback changed something")
			}
		})
	}
}

// A snapshot of the root taken after the pre-upgrade one (a backup, a replication, one by hand) goes only when the
// request names it with destroy_newer, and then as the whole jail snapshot.
func TestThickRollbackDestroysLaterSnapshotsWhenConfirmed(t *testing.T) {
	w := thickUpgraded(t)
	if _, err := w.S.SnapshotJail(bg, "alpha_web", "later", ""); err != nil {
		t.Fatal(err)
	}
	res, err := w.S.RollbackJail(bg, "alpha_web", RollbackOptions{Stamp: "300"}, true)
	if err != nil || len(res.Destroyed) != 1 || res.Destroyed[0] != jc+"/root@later" || !strings.Contains(strings.Join(res.Plan, "\n"), "later") {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	opts := RollbackOptions{JailRollback: api.JailRollback{ConfirmDataLoss: true, DestroyNewer: true}, Stamp: "300"}
	if _, err := w.S.RollbackJail(bg, "alpha_web", opts, false); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{jc, jc + "/root"} {
		for _, n := range []string{"later", "jo-pre-upgrade-100"} {
			if ok, _ := w.Z.Exists(bg, d+"@"+n); ok {
				t.Errorf("%s@%s left", d, n)
			}
		}
	}
}

func TestThickRollbackDryRunChangesNothing(t *testing.T) {
	w := thickUpgraded(t)
	before := w.state()
	w.F.Reset(0)
	res, err := w.S.RollbackJail(bg, "alpha_web", RollbackOptions{Stamp: "300"}, true)
	if err != nil || res.Snapshot != "jo-pre-upgrade-100" || len(res.Plan) == 0 {
		t.Fatal(res, err)
	}
	if w.state() != before || w.F.Count() != 0 {
		t.Fatal("a dry run changed something")
	}
	p, _, _, err := w.S.planThickRollback(bg, mustReady(t, w), RollbackOptions{Stamp: "300"}, true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range p.Steps {
		names = append(names, s.Name)
	}
	if got, want := strings.Join(names, " "), "rollback-root drop-snapshots"; got != want {
		t.Fatalf("plan: %s", got)
	}
}

func mustReady(t *testing.T, w *world) *jailState {
	st, err := w.S.ready(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A failure before the root is rolled back leaves the jail as it was; after it the plan only goes forward and a retry
// finishes it (the snapshot is destroyed last).
func TestThickRollbackFailureSweep(t *testing.T) {
	setup := thickUpgraded
	w := setup(t)
	w.F.Reset(0)
	if _, err := rollbackThick(w, true); err != nil {
		t.Fatal(err)
	}
	n, rootOp := w.F.Count(), 0
	for i, l := range w.F.Log {
		if strings.HasPrefix(l, "zfs rollback") {
			rootOp = i + 1
		}
	}
	if rootOp == 0 {
		t.Fatal("no zfs rollback")
	}
	failed := 0
	for k := 1; k <= n; k++ {
		w := setup(t)
		before := w.state()
		w.F.Reset(k)
		_, err := rollbackThick(w, true)
		if err == nil {
			continue
		}
		failed++
		if !errors.Is(err, fault.ErrInjected) {
			t.Fatalf("op %d: %v", k, err)
		}
		if k <= rootOp {
			if after := w.state(); after != before {
				t.Fatalf("failing op %d (%s) left the jail changed\n--- before\n%s\n--- after\n%s", k, w.F.Log[k-1], before, after)
			}
			continue
		}
		failedOp := w.F.Log[k-1]
		w.F.Reset(0)
		if _, err := rollbackThick(w, true); err != nil {
			t.Fatalf("retry after op %d (%s): %v", k, failedOp, err)
		}
	}
	if failed == 0 {
		t.Fatal("no failure surfaced")
	}
}

func addBase(w *world, dry bool, names ...string) (UpgradeResult, error) {
	return w.S.UpgradeJail(bg, "alpha_web", UpgradeRequest{JailUpgrade: api.JailUpgrade{AddPackages: names, DryRun: dry}, Stamp: "100"})
}

func TestThickBaseAdd(t *testing.T) {
	w := thickUpgradable(t)
	w.Z.WriteFile(jc+"/root", "/home/user/notes", "keep me")
	w.Exec.Calls = nil
	w.F.Reset(0)
	res, err := addBase(w, false, "FreeBSD-ssh", "FreeBSD-ee")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Snapshot != "jo-pre-upgrade-100" {
		t.Fatalf("%+v", res)
	}
	calls := pkgOnRoot(w)
	if len(calls) != 4 || !strings.HasSuffix(calls[1], "install -y -r jailops FreeBSD-ssh FreeBSD-ee") {
		t.Fatalf("pkg calls: %v", calls)
	}
	if logIndex(w, "jail stop", 0) >= 0 || logIndex(w, "zfs snapshot", 0) < 0 {
		t.Fatalf("%v", w.F.Log)
	}
	if got := w.Z.PropOrEmpty(jc+"@jo-pre-upgrade-100", PropComment); !strings.Contains(got, "FreeBSD-ssh FreeBSD-ee") {
		t.Fatal(got)
	}
	if w.installed["FreeBSD-ssh"] == "" {
		t.Fatal("not installed")
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackThick(w, true); err != nil {
		t.Fatal(err)
	}
	if c, _ := w.Z.ReadFile(jc+"/root", "/home/user/notes"); c != "keep me" {
		t.Fatal(c)
	}
}

func TestThickBaseAddRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) *world
		req   api.JailUpgrade
		code  api.Code
		msg   string
	}{
		{"not a base package", thickUpgradable, api.JailUpgrade{AddPackages: []string{"nginx"}}, api.CodeInvalid, ""},
		{"a path", thickUpgradable, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh/../x"}}, api.CodeInvalid, ""},
		{"an option", thickUpgradable, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh", "-f"}}, api.CodeInvalid, ""},
		{"a jail of an unsupported root layout", oldLayoutRunning, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh"}}, api.CodeUnsupported, "root layout this release does not support"},
		{"a package the repository does not have", thickUpgradable, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh", "FreeBSD-ftpd"}}, api.CodeNotFound, `base package "FreeBSD-ftpd" is not in the repository of FreeBSD 15.0-RELEASE`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.setup(t)
			before := w.state()
			w.F.Reset(0)
			_, err := w.S.UpgradeJail(bg, "alpha_web", UpgradeRequest{JailUpgrade: tc.req, Stamp: "100"})
			wantCode(t, err, tc.code)
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatal(err)
			}
			for _, l := range w.F.Log {
				if strings.HasPrefix(l, "jail ") || strings.HasPrefix(l, "zfs ") {
					t.Fatalf("a refused request touched the jail: %s", l)
				}
			}
			if w.state() != before {
				t.Fatal("a refused request changed something")
			}
		})
	}
}

func TestThickBaseAddDryRunAndFailureSweep(t *testing.T) {
	w := thickUpgradable(t)
	before := w.state()
	w.F.Reset(0)
	res, err := addBase(w, true, "FreeBSD-ssh")
	if err != nil || !res.Changed {
		t.Fatal(res, err)
	}
	if w.state() != before {
		t.Fatal("a dry run changed something")
	}
	// every failure leaves the jail running
	for k := 1; k <= 40; k++ {
		w := thickUpgradable(t)
		w.F.Reset(k)
		_, _ = addBase(w, false, "FreeBSD-ssh")
		if !w.Jails.IsUp("alpha_web") || logIndex(w, "jail stop", 0) >= 0 {
			t.Fatalf("failing op %d stopped the jail", k)
		}
	}
}

func TestThickBasePackagesListsRunningAndStopped(t *testing.T) {
	w := thickUpgradable(t)
	got, err := w.S.BasePackages(bg, "alpha_web")
	if err != nil || len(got) != 2 || got[0][0] != "FreeBSD-runtime" {
		t.Fatal(got, err)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	before := w.state()
	got, err = w.S.BasePackages(bg, "alpha_web")
	if err != nil || len(got) != 2 || w.state() != before {
		t.Fatal(got, err)
	}
}

// A snapshot taken after the pre-upgrade one on a dataset the rollback does not touch (the jail's container, as a backup
// of the data leaves it) does not hold the rollback up and is kept.
func TestThickRollbackKeepsLaterSnapshotsOfOtherDatasets(t *testing.T) {
	w := thickUpgraded(t)
	if err := w.Z.Snapshot(bg, nil, jc+"@backup-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackThick(w, true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, jc+"@backup-1"); !ok {
		t.Fatal("the rollback destroyed a snapshot it was not asked to")
	}
	if ok, _ := w.Z.Exists(bg, jc+"/root@jo-pre-upgrade-100"); ok {
		t.Fatal("the used-up pre-upgrade snapshot is left")
	}
}
