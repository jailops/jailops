package lifecycle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

const (
	testPass = "marker-correct-horse-staple"
	newPass  = "marker-another-long-phrase"
	volDS    = "tank/jailops/projects/alpha/jails/web/data/db"
)

// logJournal keeps every record it was given, to prove no passphrase reaches the journal.
type logJournal struct {
	plan.MemJournal
	log []string
}

func (l *logJournal) Put(ctx context.Context, r plan.Record) error {
	b, _ := json.Marshal(r)
	l.log = append(l.log, string(b))
	return l.MemJournal.Put(ctx, r)
}

func encWorld(t *testing.T) (*world, *logJournal) {
	w := newWorld(t)
	lj := &logJournal{}
	w.S.Executor = &plan.Executor{Journal: lj}
	w.S.Keys = w.Z
	return w, lj
}

func encJail(w *world, name string) api.Jail {
	j := w.jail(name)
	j.Spec.Volumes[0].Encrypted = true
	return j
}

func TestCreateEncryptedVolume(t *testing.T) {
	w, lj := encWorld(t)
	res, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass))
	if err != nil {
		t.Fatal(err)
	}
	if w.Z.Passphrase(volDS) != testPass || w.Z.PropOrEmpty(volDS, PropEncrypted) != "on" {
		t.Fatalf("volume not encrypted: %q", w.Z.Passphrase(volDS))
	}
	if ks, _ := w.Z.KeyStatus(bg, volDS); ks != "available" {
		t.Fatalf("a new volume is unlocked, got %q", ks)
	}
	for _, s := range append(append(res.Plan, lj.log...), w.Z.Dump()) {
		if strings.Contains(s, "marker-") {
			t.Fatalf("the passphrase leaked: %s", s)
		}
	}
	b, _ := w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/spec.json")
	if strings.Contains(string(b), "marker-") || !strings.Contains(string(b), `"encrypted": true`) {
		t.Fatalf("spec.json: %s", b)
	}
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if l, err := w.S.Locked(bg, "alpha_web"); err != nil || l != nil {
		t.Fatalf("locked %v %v", l, err)
	}
}

func TestCreateEncryptedRefusals(t *testing.T) {
	w, _ := encWorld(t)
	if _, err := w.S.CreateJail(bg, encJail(w, "web"), false); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("no passphrase: %v", err)
	}
	if _, err := w.S.CreateJailKeyed(bg, w.jail("web"), false, []byte(testPass)); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("a passphrase and no encrypted volume: %v", err)
	}
	if _, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte("short")); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("short passphrase: %v", err)
	}
	w.S.Keys = nil
	if _, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass)); !api.IsCode(err, api.CodeUnsupported) {
		t.Fatalf("no key operations: %v", err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web"); ok {
		t.Fatal("a refused creation left datasets")
	}
	// a rebuilt plan rolls back without a passphrase
	w.S.Keys = w.Z
	if _, _, err := w.S.PlanCreate(bg, encJail(w, "web"), true); err != nil {
		t.Fatal(err)
	}
}

func TestCreateEncryptedFailureSweep(t *testing.T) {
	sweep(t, func(t *testing.T) *world { w, _ := encWorld(t); return w }, func(w *world) error {
		_, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass))
		return err
	})
}

// reboot unloads every key and unmounts what was mounted, as a host start does.
func reboot(w *world) {
	for _, n := range w.Z.Names() {
		_ = w.Z.Unmount(bg, n)
	}
	w.Z.DropKeys()
	w.Jails.Up = map[string]bool{}
}

func TestLockedJailDoesNotStartUntilUnlocked(t *testing.T) {
	w, _ := encWorld(t)
	if _, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass)); err != nil {
		t.Fatal(err)
	}
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	w.Z.WriteFile(volDS, "/data.txt", "kept")
	reboot(w)
	if l, err := w.S.Locked(bg, "alpha_web"); err != nil || len(l) != 1 || l[0] != "db" {
		t.Fatalf("locked %v %v", l, err)
	}
	r, err := w.S.Rebuild(bg, "alpha_web")
	if err != nil || len(r.Locked) != 1 {
		t.Fatalf("rebuild %v %v", r.Locked, err)
	}
	err = w.S.StartJail(bg, "alpha_web")
	wantCode(t, err, api.CodeLocked)
	var ae *api.Error
	if !strings.Contains(err.Error(), "jailops jail unlock web") || !asErr(err, &ae) || ae.Detail["volumes"] != "db" {
		t.Fatalf("hint and detail: %v", err)
	}
	if w.Jails.Up["alpha_web"] {
		t.Fatal("a locked jail started")
	}
	wantCode(t, w.S.UnlockVolumes(bg, "alpha_web", []byte("marker-wrong-passphrase")), api.CodeBadPassphrase)
	if l, _ := w.S.Locked(bg, "alpha_web"); len(l) != 1 {
		t.Fatal("a wrong passphrase unlocked")
	}
	if err := w.S.UnlockVolumes(bg, "alpha_web", []byte(testPass)); err != nil {
		t.Fatal(err)
	}
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if c, ok := w.Z.ReadFile(volDS, "/data.txt"); !ok || c != "kept" {
		t.Fatal("the data of the volume is gone")
	}
	// lock: stopped, unmounted, key unloaded
	if err := w.S.LockVolumes(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if w.Jails.Up["alpha_web"] {
		t.Fatal("lock did not stop the jail")
	}
	if m, _ := w.Z.Mounted(bg, volDS); m {
		t.Fatal("lock left the volume mounted")
	}
	if l, _ := w.S.Locked(bg, "alpha_web"); len(l) != 1 {
		t.Fatal("lock left the key loaded")
	}
	wantCode(t, w.S.StartJail(bg, "alpha_web"), api.CodeLocked)
	// a jail without encrypted volumes has nothing to unlock
	w.created("plain")
	wantCode(t, w.S.UnlockVolumes(bg, "alpha_plain", []byte(testPass)), api.CodeInvalid)
	wantCode(t, w.S.LockVolumes(bg, "alpha_plain"), api.CodeInvalid)
}

func asErr(err error, out **api.Error) bool {
	e, ok := err.(*api.Error)
	*out = e
	return ok
}

func TestChangePassphrase(t *testing.T) {
	w, _ := encWorld(t)
	j := encJail(w, "web")
	j.Spec.Volumes = append(j.Spec.Volumes, api.VolumeMount{Name: "logs", Mount: "/srv/logs", Encrypted: true})
	if _, err := w.S.CreateJailKeyed(bg, j, false, []byte(testPass)); err != nil {
		t.Fatal(err)
	}
	logs := "tank/jailops/projects/alpha/jails/web/data/logs"
	wantCode(t, w.S.ChangePassphrase(bg, "alpha_web", []byte("marker-wrong-passphrase"), []byte(newPass)), api.CodeBadPassphrase)
	// the second volume fails: the first is changed back
	w.F.Reset(2) // change-key of db, then of logs, which fails
	if err := w.S.ChangePassphrase(bg, "alpha_web", []byte(testPass), []byte(newPass)); err == nil {
		t.Fatal("the injected failure was not reported")
	}
	w.F.Reset(0)
	if w.Z.Passphrase(volDS) != testPass || w.Z.Passphrase(logs) != testPass {
		t.Fatalf("passphrases differ after a failed change: %q %q", w.Z.Passphrase(volDS), w.Z.Passphrase(logs))
	}
	if err := w.S.ChangePassphrase(bg, "alpha_web", []byte(testPass), []byte(newPass)); err != nil {
		t.Fatal(err)
	}
	if w.Z.Passphrase(volDS) != newPass || w.Z.Passphrase(logs) != newPass {
		t.Fatal("not changed")
	}
	reboot(w)
	wantCode(t, w.S.ChangePassphrase(bg, "alpha_web", []byte(newPass), []byte(testPass)), api.CodeLocked)
	wantCode(t, w.S.UnlockVolumes(bg, "alpha_web", []byte(testPass)), api.CodeBadPassphrase)
	if err := w.S.UnlockVolumes(bg, "alpha_web", []byte(newPass)); err != nil {
		t.Fatal(err)
	}
}

func TestCopyKeepsAVolumeEncrypted(t *testing.T) {
	w, _ := encWorld(t)
	if _, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass)); err != nil {
		t.Fatal(err)
	}
	w.Z.WriteFile(volDS, "/data.txt", "secret data")
	res, err := w.S.CopyJail(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Plan, "\n"), "zfs send -w "+volDS+"@jo-copy-7") {
		t.Fatalf("plan %v", res.Plan)
	}
	dst := "tank/jailops/projects/alpha/jails/web2/data/db"
	if w.Z.Passphrase(dst) != testPass || w.Z.PropOrEmpty(dst, PropEncrypted) != "on" {
		t.Fatal("the copy of an encrypted volume is not encrypted")
	}
	if c, _ := w.Z.ReadFile(dst, "/data.txt"); c != "secret data" {
		t.Fatal("data not copied")
	}
	if l, _ := w.S.Locked(bg, "alpha_web2"); len(l) != 1 {
		t.Fatal("a copy starts locked")
	}
	wantCode(t, w.S.StartJail(bg, "alpha_web2"), api.CodeLocked)
	if err := w.S.UnlockVolumes(bg, "alpha_web2", []byte(testPass)); err != nil {
		t.Fatal(err)
	}
}

func TestLayoutMarksLockedVolumes(t *testing.T) {
	w, _ := encWorld(t)
	if _, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass)); err != nil {
		t.Fatal(err)
	}
	find := func() JailDataset {
		lay, err := w.S.Layout(bg, "alpha_web")
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range lay.Datasets {
			if d.Name == volDS {
				return d
			}
		}
		t.Fatal("no volume in the layout")
		return JailDataset{}
	}
	if d := find(); !d.Encrypted || d.Locked {
		t.Fatalf("%+v", d)
	}
	reboot(w)
	if d := find(); !d.Encrypted || !d.Locked {
		t.Fatalf("%+v", d)
	}
}

// An upgrade ends with a start: a locked jail is refused before anything changes. The plans that roll an interrupted
// start or upgrade back are still built after a reboot.
func TestLockedJailRefusesAnUpgradeButRecovers(t *testing.T) {
	w, _ := encWorld(t)
	if _, err := w.S.CreateJailKeyed(bg, encJail(w, "web"), false, []byte(testPass)); err != nil {
		t.Fatal(err)
	}
	reboot(w)
	before := w.state()
	_, _, err := w.S.PlanUpgrade(bg, "alpha_web", UpgradeRequest{Stamp: "9"})
	wantCode(t, err, api.CodeLocked)
	if w.state() != before {
		t.Fatal("a refused upgrade changed the pool")
	}
	if _, err := w.S.PlanStart(bg, "alpha_web"); err != nil {
		t.Fatalf("the start plan of a recovery: %v", err)
	}
	if _, _, err := w.S.PlanUpgrade(bg, "alpha_web", UpgradeRequest{Stamp: "9", Resume: &SwitchState{FromGen: 1}}); api.IsCode(err, api.CodeLocked) {
		t.Fatalf("the upgrade plan of a recovery: %v", err)
	}
}
