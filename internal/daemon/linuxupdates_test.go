package daemon_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// Linux jails look after their own packages: an update policy is refused on one, one named alone is refused by
// update check, apply and history, a selection leaves it out, and a policy stored before (0.9.2) loads and is
// ignored: never scheduled, never reported.
func TestLinuxJailsAreLeftOutOfUpdates(t *testing.T) {
	shellT = t
	var now = struct {
		sync.Mutex
		t time.Time
	}{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { now.Lock(); defer now.Unlock(); return now.t }
	e := dtest.New(t, dtest.Options{Now: clock, Mutate: func(c *daemon.Config) { c.Updates.Tick = daemon.Duration(20 * time.Millisecond) }})
	e.Seed()
	e.SeedLinux()
	sj := &scriptedJails{pkgs: pkgDryRun, env: e}
	e.SetPkgUpgrades(pkgDryRun)
	e.Jails.SetExecFn(sj.fn)
	runningWeb(t, e)
	ctx := context.Background()
	linuxRefusal := func(what string, err error) {
		t.Helper()
		if !api.IsCode(err, api.CodeUnsupported) || !strings.Contains(err.Error(), "Linux jails look after their own packages") {
			t.Fatalf("%s: %v", what, err)
		}
	}

	bad := dtest.AlpineJail("alp2")
	bad.Spec.Update = api.UpdatePolicy{Mode: api.UpdateModeNotify, Schedule: "0 3 * * *", Packages: true}
	_, err := e.Admin.Jails().Create(ctx, bad)
	linuxRefusal("create with a policy", err)
	mustOp(e.Admin.Jails().Create(ctx, dtest.AlpineJail("alp")))
	linuxRefusal("config set", opErr(e.Admin.Jails().Config(ctx, "alp", map[string]string{"update.mode": "notify", "update.schedule": "0 3 * * *"}, nil, false)))
	// the health check alone is no policy: restore tests run it too
	mustOp(e.Admin.Jails().Config(ctx, "alp", map[string]string{"update.healthcheck": "/bin/true"}, nil, false))
	mustOp(e.Admin.Jails().Start(ctx, "alp"))

	_, err = e.Admin.Updates().Check(ctx, "alp")
	linuxRefusal("check", err)
	_, err = e.Admin.Updates().Apply(ctx, api.UpdateApplyRequest{Jails: []string{"alp"}})
	linuxRefusal("apply", err)
	_, err = e.Admin.Jails().UpdateHistory(ctx, "alp")
	linuxRefusal("history", err)
	for _, req := range [][]string{{"web", "alp"}, nil} {
		op, err := e.Admin.Updates().Check(ctx, req...)
		if err != nil {
			t.Fatal(err)
		}
		reps, err := op.Wait(ctx)
		if err != nil || len(reps) != 1 || reps[0].Jail != "web" {
			t.Fatalf("check %v: %+v %v", req, reps, err)
		}
	}

	// a policy stored on a Linux jail by an earlier release: it loads, an edit of something else goes through, and it
	// never runs
	e.Stop()
	var sp string
	for _, p := range e.Files.Paths() {
		if strings.HasSuffix(p, "/alp/spec.json") {
			sp = p
		}
	}
	raw, ok := e.Files.Get(sp)
	if !ok {
		t.Fatal("no spec.json of the Linux jail")
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	rec["spec"].(map[string]any)["update"] = map[string]any{"mode": "managed", "schedule": "0 3 * * *", "packages": true, "healthcheck": "/bin/true"}
	raw, _ = json.Marshal(rec)
	e.Files.Put(sp, raw)
	e.Start()
	e.Jails.SetExecFn(sj.fn)
	j, err := e.Admin.Jails().Get(ctx, "alp")
	if err != nil || j.Spec.Update.Mode != api.UpdateModeManaged {
		t.Fatalf("the stored policy: %+v %v", j.Spec.Update, err)
	}
	mustOp(e.Admin.Jails().Config(ctx, "alp", map[string]string{"boot.priority": "20"}, nil, false))
	now.Lock()
	now.t = now.t.Add(30 * time.Hour)
	now.Unlock()
	time.Sleep(200 * time.Millisecond)
	if sj.ran("apk") {
		t.Fatal("JailOps ran apk in a Linux jail")
	}
	if l, err := e.Admin.Updates().List(ctx); err != nil || len(l) != 0 {
		t.Fatalf("reports: %+v %v", l, err)
	}
	// the stored policy is not changed, only taken away
	linuxRefusal("a change of the stored policy", opErr(e.Admin.Jails().Config(ctx, "alp", map[string]string{"update.mode": "notify"}, nil, false)))
	mustOp(e.Admin.Jails().Config(ctx, "alp", map[string]string{"update.mode": "manual"}, nil, false))
	mustOp(e.Admin.Jails().Config(ctx, "alp", nil, []string{"update.mode", "update.schedule", "update.packages"}, false))
	if j, err := e.Admin.Jails().Get(ctx, "alp"); err != nil || j.Spec.Update != (api.UpdatePolicy{Healthcheck: "/bin/true"}) {
		t.Fatalf("after the unset: %+v %v", j.Spec.Update, err)
	}
}
