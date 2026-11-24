package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/jailaudit"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// The audit of a running jail: operator in the project, never readonly; the helper's facts become the checks, nothing
// runs in the jail, the jail parameters come from the rendered jail.conf; a host without a vulnerability database is
// not checked, not a failure; the suggestions come only with suggest=true and never count; a critical finding sends
// jail-audit-critical and the doctor names the jail; the jail keeps running.
func TestJailAudit(t *testing.T) {
	e := seeded(t)
	ctx := ctxT(t)
	startJailIn(t, e.Admin, dtest.WebJail("shop"))
	e.Files.Audit = jailaudit.Facts{
		Accounts:   jailaudit.Accounts{Read: true, Root: jailaudit.PassLocked, Toor: jailaudit.PassLocked, RootShell: "/bin/sh", UID0: []string{"backdoor"}},
		OwnersRead: true,
		Texts:      map[string]string{"/etc/rc.conf": "cron_flags=\"-J 60\"\n"},
		Mounts:     []jailaudit.Mount{{Path: "/srv/data"}},
	}
	var inJail [][]string
	e.Jails.SetExecFn(func(jail string, o jailos.ExecOpts) (run.Result, error) {
		inJail = append(inJail, o.Argv)
		return run.Result{}, nil
	})
	if _, err := e.Scoped(api.PermReadonly, dtest.SeedProject).Jails().Audit(ctx, "shop"); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("readonly: %v", err)
	}
	evctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	events := e.Events(evctx, e.Admin)
	a, err := e.Scoped(api.PermOperator, dtest.SeedProject).Jails().Audit(ctx, "shop")
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	got := map[string]api.JailAuditCheck{}
	for _, c := range a.Checks {
		got[c.ID] = c
		if c.Suggestion {
			t.Errorf("a suggestion without suggest: %+v", c)
		}
	}
	if a.Jail != "shop" || !a.Running || a.Critical != 1 || a.Warnings != 0 || got["accounts.uid0"].Result != api.AuditCritical || !strings.Contains(got["accounts.uid0"].Detail, "backdoor") {
		t.Fatalf("%+v", a)
	}
	if _, ok := got["ports.listening"]; ok {
		t.Fatalf("ports.listening: %+v", got["ports.listening"])
	}
	if c := got["jail.enforce_statfs"]; c.Result != api.AuditPass {
		t.Fatalf("enforce_statfs: %+v", c)
	}
	for _, id := range []string{"pkg.vulnerabilities", "base.updates"} {
		if c := got[id]; c.Result != api.AuditInfo || !strings.HasPrefix(c.Detail, "not checked: ") {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	if len(inJail) != 0 {
		t.Fatalf("ran in the jail: %v", inJail)
	}
	s, err := e.Scoped(api.PermOperator, dtest.SeedProject).Jails().AuditWith(ctx, "shop", sdk.AuditOptions{Suggest: true})
	if err != nil {
		t.Fatal(err)
	}
	sg := map[string]api.JailAuditCheck{}
	for _, c := range s.Checks {
		sg[c.ID] = c
	}
	if c := sg["cron.jitter"]; !c.Suggestion || c.Result != api.AuditPass {
		t.Fatalf("cron: %+v", c)
	}
	// a mount without nosuid is a note among the suggestions, not a warning
	if c := sg["volumes.nosuid"]; !c.Suggestion || c.Result != api.AuditInfo || s.Critical != 1 || s.Warnings != 0 {
		t.Fatalf("volumes: %+v %d %d", c, s.Critical, s.Warnings)
	}
	for ev := range events {
		var le api.LifecycleEvent
		if json.Unmarshal(ev.Metadata, &le) == nil && le.Action == "jail-audit-critical" && le.Source == "/api/v1/jails/shop" {
			cancel()
			break
		}
	}
	if evctx.Err() == context.DeadlineExceeded {
		t.Fatal("no jail-audit-critical event")
	}
	rep, err := e.Admin.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range rep.Checks {
		if c.Name == "jail.audit" {
			found = c.Status == api.DoctorFail && strings.Contains(c.Message, "jail alpha/shop") && strings.Contains(c.Message, "accounts.uid0")
		}
	}
	if !found {
		t.Fatalf("doctor: %+v", rep.Checks)
	}
	if j, err := e.Admin.Jails().Get(ctx, "shop"); err != nil || j.Status.Power != api.PowerRunning {
		t.Fatalf("the audit changed the jail: %s %v", j.Status.Power, err)
	}
}
