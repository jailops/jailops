package lifecycle

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// pkg 2.9.2 on FreeBSD 15.2-PRERELEASE, a 15.1 root against base_latest (upgrade -f -n), cut down.
const pkgForcedDryRun = `Updating jailops repository catalogue...
jailops repository is up to date.
Checking for upgrades (109 candidates): .......... done
Processing candidates (109 candidates): .......... done
Checking integrity... done (0 conflicting)
The following 4 package(s) will be affected (of 0 checked):

New packages to be INSTALLED:
	FreeBSD-acpi: 15.snap20261117012503 (required by FreeBSD-bhyve)

Installed packages to be DOWNGRADED:
	FreeBSD-acct: 15.1p4 -> 15.snap20261113224120
	FreeBSD-at: 15.1 -> 15.snap20261113035357

Installed packages to be REINSTALLED:
	FreeBSD-caroot-15.snap20261021081405 [jailops]

Number of packages to be installed: 1
Number of packages to be downgraded: 2
Number of packages to be reinstalled: 1

The process will require 4 MiB more space.
12 MiB to be downloaded.
`

func TestParsePkgPlan(t *testing.T) {
	p, err := parsePkgPlan(pkgForcedDryRun)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range p.Changes {
		got = append(got, c.Kind+" "+c.String())
	}
	want := []string{"downgrade FreeBSD-acct 15.1p4 -> 15.snap20261113224120", "install FreeBSD-acpi (new) 15.snap20261117012503",
		"downgrade FreeBSD-at 15.1 -> 15.snap20261113035357", "reinstall FreeBSD-caroot 15.snap20261021081405 (reinstalled)"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if p.Size != 4<<20 || p.Download != 12<<20 {
		t.Fatalf("size %d download %d", p.Size, p.Download)
	}
	up, err := parsePkgPlan("Checking for upgrades (1 candidates): . done\nProcessing candidates (1 candidates): . done\nChecking integrity... done (0 conflicting)\nThe following 1 package(s) will be affected (of 0 checked):\n\nInstalled packages to be UPGRADED:\n\tFreeBSD-openssl: 15.1p6 -> 15.1p7\n\nNumber of packages to be upgraded: 1\n\nThe process will free 20 KiB.\n")
	if err != nil || len(up.Changes) != 1 || up.Changes[0] != (pkgChange{Name: "FreeBSD-openssl", From: "15.1p6", To: "15.1p7", Kind: "upgrade"}) || up.Size != -20<<10 || up.Download != 0 {
		t.Fatalf("%+v %v", up, err)
	}
	// pkg's own "nothing": the audit's case, a version in the database that the catalogue does not move (0 candidates)
	for _, out := range []string{"Checking for upgrades (0 candidates): 100%\nYour packages are up to date.\n",
		"Checking for upgrades (109 candidates): .......... done\nProcessing candidates (109 candidates): . done\nChecking integrity... done (0 conflicting)\nYour packages are up to date.\n",
		"Checking integrity... done (0 conflicting)\nThe most recent versions of packages are already installed\n"} {
		if p, err := parsePkgPlan(out); err != nil || len(p.Changes) != 0 {
			t.Errorf("%q: %+v %v", out, p, err)
		}
	}
	// what is not pkg's answer is never read as "nothing"
	for _, out := range []string{"", "pkg: Repository jailops cannot be opened. 'pkg update' required\n", "Installed packages to be UPGRADED:\n\tgarbage\n"} {
		if _, err := parsePkgPlan(out); err == nil {
			t.Errorf("%q was understood", out)
		}
	}
}

// pkg's plan is the judge after the run too: a package pkg planned to upgrade that is not at that version afterwards
// (pkg upgrade did less than its dry run said) fails the upgrade and the root goes back.
func TestThickUpgradePostFlightComparesWithPkgsPlan(t *testing.T) {
	w := thickUpgradable(t)
	w.Z.WriteFile(jc+"/root", "/usr/bin/tool", "old")
	before := w.state()
	w.pkgFail = func(sub string, argv []string) error {
		if sub == "upgrade" {
			w.Z.WriteFile(jc+"/root", "/usr/bin/tool", "new")
			w.repo["FreeBSD-runtime"] = w.installed["FreeBSD-runtime"] // the run leaves it where it was
		}
		return nil
	}
	_, err := upgrade(w, api.JailUpgrade{})
	if err == nil || !strings.Contains(err.Error(), "not what pkg planned") || !strings.Contains(err.Error(), "FreeBSD-runtime") {
		t.Fatal(err)
	}
	// reported, not rolled back: the jail runs on and the snapshot stays for a person
	if c, _ := w.Z.ReadFile(jc+"/root", "/usr/bin/tool"); c != "new" || !w.Jails.IsUp("alpha_web") {
		t.Fatalf("rolled back or stopped: %q", c)
	}
	_ = before
	if err := checkPlanned(map[string]pkgInfo{"FreeBSD-runtime": {Version: "15.0p3"}, "FreeBSD-ssh": {Version: "15.1"}, "bash": {Version: "5.3"}},
		map[string]pkgInfo{"FreeBSD-runtime": {Version: "15.0p2"}, "FreeBSD-ssh": {Version: "15.0"}, "bash": {Version: "5.2"}},
		map[string]string{"FreeBSD-runtime": "15.0p3"}); err == nil || !strings.Contains(err.Error(), "FreeBSD-ssh changed") {
		t.Fatalf("an unplanned change of a base package: %v", err)
	}
}

// The scripts of the base packages do not run from the host (RUN_SCRIPTS=false); what they do runs inside the jail
// after every base change, before the health check.
func TestThickUpgradeSettlesInsideTheJail(t *testing.T) {
	w := thickUpgradable(t)
	var inJail []string
	w.Jails.ExecFn = func(name string, argv []string) ([]byte, error) {
		inJail = append(inJail, strings.Join(argv, " "))
		return nil, nil
	}
	if _, err := upgrade(w, api.JailUpgrade{}); err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, c := range settleOrder {
		i := slices.Index(inJail, strings.Join(settleArgv[c], " "))
		if i <= last {
			t.Fatalf("%s did not run in order inside the jail: %q", c, inJail)
		}
		last = i
	}
	if h := slices.Index(inJail[last:], "/bin/true"); h < 0 {
		t.Fatalf("no health check after the settle step: %q", inJail)
	}
	for _, c := range pkgCalls(w) {
		if strings.HasPrefix(c, "pkg -r "+rootMPt+" ") && !strings.Contains(c, " -o RUN_SCRIPTS=false ") {
			t.Fatalf("pkg ran scripts from the host: %s", c)
		}
	}
}

// A repository whose packages were built for a newer FreeBSD than the host kernel (base_latest moved to the next minor
// before the host) is refused by pkg itself (OSVERSION is the kernel's) before the jail is stopped.
func TestThickUpgradeCatalogueNewerThanTheKernel(t *testing.T) {
	w := thickUpgradable(t)
	w.repoOSVersion = 1501000 // the host kernel is 15.0-RELEASE
	before := w.state()
	w.F.Reset(0)
	for _, dry := range []bool{false, true} {
		_, err := upgrade(w, api.JailUpgrade{DryRun: dry})
		if !api.IsCode(err, api.CodeVersionSkew) || !strings.Contains(err.Error(), "newer FreeBSD than the host kernel") {
			t.Fatal(dry, err)
		}
	}
	if w.state() != before || logIndex(w, "jail stop", 0) >= 0 {
		t.Fatal("the jail was touched")
	}
	for _, c := range pkgCalls(w) {
		if strings.Contains(c, "IGNORE_OSVERSION") || !strings.Contains(c, " -o OSVERSION=1500999 ") && strings.Contains(c, " -o ABI=") {
			t.Fatalf("pkg is not held to the host kernel: %s", c)
		}
	}
	w.repoOSVersion = 1500500
	if res, err := upgrade(w, api.JailUpgrade{}); err != nil || !res.Changed {
		t.Fatal(res, err)
	}
}

// What pkg finds is the answer: a jail pkg finds current is not stopped, whatever JailOps would compare.
func TestThickUpgradeNothingIsPkgsAnswer(t *testing.T) {
	w := thickUpgradable(t)
	note := "upgrade-jail alpha_web failed at step pkg-upgrade and the rollback did not finish"
	w.Z.Set(bg, jc, map[string]string{PropUpgradeIncomplete: note})
	// installed and repository differ, but pkg's dry run (of the copy of the database) says nothing changes
	w.repo["FreeBSD-runtime"] = w.installed["FreeBSD-runtime"]
	w.F.Reset(0)
	res, err := upgrade(w, api.JailUpgrade{})
	if err != nil || !res.Nothing || logIndex(w, "jail stop", 0) >= 0 {
		t.Fatal(res, err)
	}
	if w.S.UpgradeIncomplete(bg, "alpha_web") != "" {
		t.Fatal("the note of an earlier failed rollback stays on a jail pkg finds current")
	}
}

// A daemon that dies after any step of an in-place upgrade finds the jail running at its next start: the plan rebuilt
// from the journal's state has the same steps, and its replay ends the run without stopping, starting or rolling back
// anything (the work directory goes; the snapshot stays for a person). Running pkg again completes a half-done upgrade.
func TestThickUpgradeCrashRecovery(t *testing.T) {
	req := UpgradeRequest{Stamp: "300"}
	w := thickUpgradable(t)
	p0, _, err := w.S.PlanUpgrade(bg, "alpha_web", req)
	if err != nil || p0 == nil {
		t.Fatal(p0, err)
	}
	for k := 1; k <= len(p0.Steps); k++ {
		w := thickUpgradable(t)
		p, res, err := w.S.PlanUpgrade(bg, "alpha_web", req)
		if err != nil {
			t.Fatal(err)
		}
		crash(t, w, "upgrade/alpha_web", p, k)
		st := res.State
		w.F.Reset(0)
		rp, _, err := w.S.PlanUpgrade(bg, "alpha_web", UpgradeRequest{Stamp: "300", Resume: &st})
		if err != nil {
			t.Fatalf("crash after step %d (%s): the plan cannot be rebuilt: %v", k, p.Steps[k-1].Name, err)
		}
		if got, want := strings.Join(rp.Names(), ","), strings.Join(p.Names(), ","); got != want {
			t.Fatalf("the rebuilt plan has other steps:\n%s\n%s", got, want)
		}
		if found, err := w.S.RecoverRun(bg, "upgrade", "alpha_web", rp); !found || err != nil {
			t.Fatalf("crash after step %d (%s): %v %v", k, p.Steps[k-1].Name, found, err)
		}
		if !w.Jails.IsUp("alpha_web") {
			t.Fatalf("crash after step %d: the jail is down", k)
		}
		for _, l := range w.F.Log {
			if strings.HasPrefix(l, "jail stop") || strings.HasPrefix(l, "jail start") || strings.HasPrefix(l, "zfs rollback") {
				t.Fatalf("crash after step %d (%s): the recovery did %s", k, p.Steps[k-1].Name, l)
			}
		}
	}
}

// OSVERSION is the host kernel's, held to the major release of the jail: pkg takes the major of its ABI from it, so a 15
// jail on a 16 host must not read the FreeBSD:16 repository.
func TestHostOSVersionStaysInTheMajor(t *testing.T) {
	w := newWorld(t)
	for _, tc := range []struct {
		host  string
		major int
		want  string
	}{{"15.0-RELEASE", 15, "1500999"}, {"16.0-CURRENT", 15, "1599999"}, {"15.2-PRERELEASE", 15, "1502999"}, {"16.0-CURRENT", 16, "1600999"}} {
		if got := w.S.hostOSVersion(bg, tc.host, tc.major); got != tc.want {
			t.Errorf("%s, major %d: %s, want %s", tc.host, tc.major, got, tc.want)
		}
	}
}

// The release JailOps recorded decides, not the root: a root that claims another release than the one JailOps installed
// is refused unless --release names the target; the release recorded after an upgrade is the one read before the jail
// ran again, and a rollback records the release of its snapshot.
func TestThickReleaseIsTheRecordedOne(t *testing.T) {
	w := thickUpgradable(t)
	fv := jroot + "/root/bin/freebsd-version"
	w.Files.Data[fv] = []byte(`USERLAND_VERSION="15.0-RELEASE-p2"` + "\n")
	w.Z.Set(bg, jc+"/root", map[string]string{PropFreeBSDVersion: "15.0-RELEASE-p2"})
	// the tenant claims a newer release
	w.Files.Data[fv] = []byte(`USERLAND_VERSION="16.0-CURRENT"` + "\n")
	_, err := upgrade(w, api.JailUpgrade{})
	if !api.IsCode(err, api.CodePreconditionFailed) || !strings.Contains(err.Error(), "says it is 16.0-CURRENT and JailOps installed 15.0-RELEASE-p2") {
		t.Fatal(err)
	}
	w.Files.Data[fv] = []byte(`USERLAND_VERSION="15.0-RELEASE-p2"` + "\n")
	// pkg installs p3; the jail rewrites the file as soon as it runs again
	w.pkgFail = func(sub string, argv []string) error {
		if sub == "upgrade" && argv[2] == rootMPt {
			w.Files.Data[fv] = []byte(`USERLAND_VERSION="15.0-RELEASE-p3"` + "\n")
		}
		return nil
	}
	w.Jails.OnStart = func(string) error { w.Files.Data[fv] = []byte(`USERLAND_VERSION="16.0-CURRENT"` + "\n"); return nil }
	res, err := upgrade(w, api.JailUpgrade{})
	if err != nil || !res.Changed {
		t.Fatal(res, err)
	}
	if got := w.Z.PropOrEmpty(jc+"/root", PropFreeBSDVersion); got != "15.0-RELEASE-p3" {
		t.Fatalf("recorded %q after the upgrade", got)
	}
	if got := w.Z.PropOrEmpty(jc+"/root@jo-pre-upgrade-100", PropFreeBSDVersion); got != "15.0-RELEASE-p2" {
		t.Fatalf("the snapshot records %q", got)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.S.RollbackJail(bg, "alpha_web", RollbackOptions{JailRollback: api.JailRollback{ConfirmDataLoss: true}, Stamp: "200"}, false); err != nil {
		t.Fatal(err)
	}
	if got := w.Z.PropOrEmpty(jc+"/root", PropFreeBSDVersion); got != "15.0-RELEASE-p2" {
		t.Fatalf("recorded %q after the rollback", got)
	}
}

// Adding base packages that are installed already is nothing to add, whatever pkg would print about it (pkg 2.9.2 on a
// 16 host prints only "Major OS version upgrade detected" for it); an answer of pkg that is not understood refuses with
// what pkg said, never as an internal error.
func TestThickBaseAddOfAnInstalledPackage(t *testing.T) {
	w := thickUpgradable(t)
	w.Exec.Calls = nil
	res, err := upgrade(w, api.JailUpgrade{AddPackages: []string{"FreeBSD-utilities"}})
	if err != nil || !res.Nothing {
		t.Fatal(res, err)
	}
	for _, c := range pkgCalls(w) {
		if strings.Contains(c, " install ") {
			t.Fatalf("pkg was asked: %s", c)
		}
	}
	if _, err := parsePkgPlan("pkg: Warning: Major OS version upgrade detected.  Running \"pkg bootstrap -f\" recommended\nChecking integrity... done (0 conflicting)\nThe most recent versions of packages are already installed\n"); err != nil {
		t.Fatalf("the warning is noise: %v", err)
	}
	w.repo["FreeBSD-ssh"] = "15.0"
	w.pkgDryRunOut = "pkg: Warning: Major OS version upgrade detected.  Running \"pkg bootstrap -f\" recommended\n"
	_, err = upgrade(w, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh"}})
	if !api.IsCode(err, api.CodePreconditionFailed) || !strings.Contains(err.Error(), "Major OS version upgrade detected") {
		t.Fatal(err)
	}
}

// The read of a stopped jail's packages mounts its root for the copy and unmounts it after; a root that is mounted
// already (an operation has it) is left mounted.
func TestThickBaseListLeavesAMountedRootMounted(t *testing.T) {
	w := newWorld(t)
	w.thickCreated()
	if err := w.S.mountTree(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.S.BasePackages(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if got := w.S.prop(bg, jc+"/root", "mounted"); got != "yes" {
		t.Fatalf("the root of an operation was unmounted (mounted=%q)", got)
	}
}

// The helper's preflight detection (a root prepared against host-side pkg) refuses the jail before anything changes,
// with what it found; the error says the jail is suspected of being compromised.
func TestThickUpgradeSuspectedRootIsLeftAlone(t *testing.T) {
	w := thickUpgradable(t)
	w.Files.CheckErr = errors.New("forbidden: suspected_compromised: etc/crontab is a symbolic link out of the root (to /etc/master.passwd)")
	before := w.state()
	w.F.Reset(0)
	_, err := upgrade(w, api.JailUpgrade{})
	var se *SuspectedError
	if !errors.As(err, &se) || !strings.Contains(se.Found, "etc/crontab is a symbolic link out of the root") || !strings.HasPrefix(err.Error(), "suspected_compromised: jail web") {
		t.Fatal(err)
	}
	if w.state() != before || logIndex(w, "zfs snapshot", 0) >= 0 || !w.Jails.IsUp("alpha_web") {
		t.Fatalf("the jail was touched: %v", w.F.Log)
	}
	// the check was given the files of the base packages, read from the copy of the jail's package database
	if len(w.Files.Checked) == 0 {
		t.Fatal("the check had no paths")
	}
}

// The processes that still run replaced files are listed from procstat -v inside the jail (a vnode mapping without a
// path); nothing is restarted.
func TestThickRestartPending(t *testing.T) {
	w := thickUpgradable(t)
	w.Jails.ExecFn = func(name string, argv []string) ([]byte, error) {
		switch argv[0] {
		case "procstat":
			return []byte("  PID              START                END PRT    RES   PRES REF SHD FLAG   TP PATH\n" +
				"  812     0x1000     0x2000 r-x  5 12 3 1 CN--- vn /usr/sbin/cron\n" +
				"  812     0x3000     0x4000 r-x  5 12 3 1 CN--- vn \n" +
				"  901     0x3000     0x4000 r-x  5 12 3 1 CN--- vn /usr/local/sbin/unbound\n" +
				"  955     0x3000     0x4000 r-x  5 12 3 1 CN--- vn \n"), nil
		case "ps":
			return []byte("  812 cron\n  901 unbound\n  955 procstat\n"), nil
		}
		return nil, nil
	}
	got := w.S.RestartPending(bg, "alpha_web")
	if strings.Join(got, ",") != "cron (pid 812)" {
		t.Fatalf("%q", got)
	}
}

// A volume at a directory of the jail's system (left from JailOps 0.9.2) holds what pkg writes outside the root and
// its snapshot: the upgrade refuses the jail, naming the volume, before it touches anything.
func TestThickUpgradeRefusesAVolumeAtASystemDirectory(t *testing.T) {
	w := thickUpgradable(t)
	spec := jroot + "/spec.json"
	b := string(w.Files.Data[spec])
	if !strings.Contains(b, `"mount": "/srv/db"`) {
		t.Fatalf("the record has no volume: %s", b)
	}
	w.Files.Data[spec] = []byte(strings.Replace(b, `"mount": "/srv/db"`, `"mount": "/var/db"`, 1))
	before := w.state()
	_, err := upgrade(w, api.JailUpgrade{})
	if !api.IsCode(err, api.CodePreconditionFailed) || !strings.Contains(err.Error(), `volume "db" is mounted at /var/db`) {
		t.Fatal(err)
	}
	if w.state() != before || len(pkgCalls(w)) != 0 {
		t.Fatalf("the jail was touched: %v", w.F.Log)
	}
}
