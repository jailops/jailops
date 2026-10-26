package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func indexOf(log []string, exact string) int {
	for i, l := range log {
		if l == exact || strings.HasPrefix(l, exact+" ") {
			return i
		}
	}
	return -1
}

func TestStartStopOrdering(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.F.Reset(0)
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	log := w.F.Log
	root := indexOf(log, "zfs mount tank/jailops/projects/alpha/jails/web/root")
	etc := indexOf(log, "zfs mount tank/jailops/projects/alpha/jails/web/etc")
	vol := indexOf(log, "zfs mount tank/jailops/projects/alpha/jails/web/data/db")
	hook := indexOf(log, "prestart alpha_web")
	jail := indexOf(log, "jail start alpha_web")
	if !(root >= 0 && root < etc && etc < vol && vol < hook && hook < jail) {
		t.Fatalf("order wrong: %v", log)
	}
	if indexOf(log, "mkdir /jailops/projects/alpha/jails/web/root/srv/db") < 0 || indexOf(log, "mkdir /jailops/projects/alpha/jails/web/root/srv/db") > vol {
		t.Fatal("volume mount dir must exist before the volume is mounted", log)
	}
	if !w.Jails.IsUp("alpha_web") {
		t.Fatal("not running")
	}
	// idempotent
	w.F.Reset(0)
	if err := w.S.StartJail(bg, "alpha_web"); err != nil || w.F.Count() != 0 {
		t.Fatal(err, w.F.Count())
	}
	w.F.Reset(0)
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	log = w.F.Log
	if !(indexOf(log, "jail stop alpha_web") < indexOf(log, "poststop alpha_web") && indexOf(log, "poststop alpha_web") < indexOf(log, "zfs unmount tank/jailops/projects/alpha/jails/web/data/db") &&
		indexOf(log, "zfs unmount tank/jailops/projects/alpha/jails/web/data/db") < indexOf(log, "zfs unmount tank/jailops/projects/alpha/jails/web/etc") &&
		indexOf(log, "zfs unmount tank/jailops/projects/alpha/jails/web/etc") < indexOf(log, "zfs unmount tank/jailops/projects/alpha/jails/web/root")) {
		t.Fatalf("stop order wrong: %v", log)
	}
	if mounted, _ := w.Z.Mounted(bg, "tank/jailops/projects/alpha/jails/web/root"); mounted {
		t.Fatal("root still mounted")
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal("stop must be idempotent:", err)
	}
}

func TestStartFailureSweep(t *testing.T) {
	setup := func(t *testing.T) *world { w := newWorld(t); w.created("web"); return w }
	sweep(t, setup, func(w *world) error { return w.S.StartJail(bg, "alpha_web") })
}

func TestStartNotReady(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.Jails.Never = true
	w.S.Cfg.ReadyTimeout, w.S.Cfg.PollInterval = 3*time.Second, time.Second
	w.S.Cfg.Sleep = func(_ context.Context, d time.Duration) error { w.now = w.now.Add(d); return nil }
	before := w.state()
	err := w.S.StartJail(bg, "alpha_web")
	if err == nil || !strings.Contains(err.Error(), "not running after 3s") {
		t.Fatal(err)
	}
	if w.state() != before {
		t.Fatal("not-ready start was not undone")
	}
}

func TestStartRefusesIncompleteAndUnknown(t *testing.T) {
	w := newWorld(t)
	wantCode(t, w.S.StartJail(bg, "alpha_nope"), api.CodeNotFound)
	w.created("web")
	w.Z.Set(bg, "tank/jailops/projects/alpha/jails/web", map[string]string{PropComplete: "off"})
	wantCode(t, w.S.StartJail(bg, "alpha_web"), api.CodeBusy)
	// a dataset that is not ours is invisible
	w.Z.Create(bg, "tank/jailops/projects/alpha/jails/alien", nil)
	wantCode(t, w.S.StartJail(bg, "alpha_alien"), api.CodeNotFound)
}

func TestDestroy(t *testing.T) {
	w := newWorld(t)
	w.running("web")
	if _, err := w.S.DestroyJail(bg, "alpha_web", false, false); !api.IsCode(err, api.CodeBusy) {
		t.Fatal(err)
	}
	r, err := w.S.DestroyJail(bg, "alpha_web", true, false)
	if err != nil || len(r.Warnings) != 0 {
		t.Fatal(err, r)
	}
	for _, n := range w.Z.Names() {
		if strings.HasPrefix(n, "tank/jailops/projects/alpha/jails/web") {
			t.Fatalf("leaked %s", n)
		}
	}
	if w.Jails.IsUp("alpha_web") || w.Images.collects != 1 || indexOf(w.Hooks.Log, "remove alpha_web") < 0 {
		t.Fatal("follow-up work missing")
	}
	wantCode(t, func() error { _, e := w.S.DestroyJail(bg, "alpha_web", true, false); return e }(), api.CodeNotFound)
}

func TestDestroyRefusesForeignClone(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	if _, err := w.S.SnapshotJail(bg, "alpha_web", "keep", ""); err != nil {
		t.Fatal(err)
	}
	w.Z.Clone(bg, "tank/jailops/projects/alpha/jails/web/home@keep", "tank/jailops/projects/alpha/jails/scratch", nil)
	if _, err := w.S.DestroyJail(bg, "alpha_web", false, false); !api.IsCode(err, api.CodeBusy) {
		t.Fatal(err)
	}
}

func TestDestroyFailureSweep(t *testing.T) {
	setup := func(t *testing.T) *world { w := newWorld(t); w.running("web"); return w }
	sweep(t, setup, func(w *world) error {
		r, err := w.S.DestroyJail(bg, "alpha_web", true, false)
		if err == nil && len(r.Warnings) > 0 {
			return nil // post-destroy cleanup failed: reported, not undone
		}
		return err
	})
}

func TestStartFailureReason(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.F.Reset(1)
	if err := w.S.StartJail(bg, "alpha_web"); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatal(err)
	}
	_ = errors.New
}
