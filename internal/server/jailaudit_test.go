package server_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/jailaudit"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// The audit of a running jail: operator in the project, never readonly; the helper's facts become the checks, the
// listening sockets are read with sockstat in the jail; a critical finding sends jail-audit-critical and the doctor
// names the jail; the jail keeps running.
func TestJailAudit(t *testing.T) {
	e := seeded(t)
	ctx := ctxT(t)
	startJailIn(t, e.Admin, dtest.WebJail("shop"))
	e.Files.Audit = jailaudit.Facts{
		Accounts: jailaudit.Accounts{Read: true, Root: jailaudit.PassLocked, Toor: jailaudit.PassLocked, RootShell: "/bin/sh", UID0: []string{"backdoor"}},
		Texts:    map[string]string{"/etc/rc.conf": "cron_flags=\"-J 60\"\n"},
	}
	var inJail [][]string
	e.Jails.SetExecFn(func(jail string, o jailos.ExecOpts) (run.Result, error) {
		inJail = append(inJail, o.Argv)
		if slices.Equal(o.Argv, apps.SockstatArgv()) {
			return run.Result{Stdout: []byte("postgres postgres 900 6 tcp4 *:5432 *:*\nroot syslogd 7 3 udp4 127.0.0.1:514 *:*\n")}, nil
		}
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
	}
	if a.Jail != "shop" || !a.Running || a.Critical != 1 || got["accounts.uid0"].Result != api.AuditCritical || !strings.Contains(got["accounts.uid0"].Detail, "backdoor") {
		t.Fatalf("%+v", a)
	}
	if c := got["ports.listening"]; c.Result != api.AuditInfo || !strings.Contains(c.Detail, "tcp/5432 (postgres)") || strings.Contains(c.Detail, "514") {
		t.Fatalf("listening: %+v", c)
	}
	if got["cron.jitter"].Result != api.AuditPass {
		t.Fatalf("cron: %+v", got["cron.jitter"])
	}
	if len(inJail) != 1 {
		t.Fatalf("ran in the jail: %v", inJail)
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
