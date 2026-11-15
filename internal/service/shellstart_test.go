package service_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// A start never waits on the shell: bash cannot be fetched before the start and pkg inside the jail never returns (a
// jail whose firewall is closed), and the jail still starts, at the deadline of the setup, with the shell it has.
func TestStartDoesNotWaitOnTheShellSetup(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.SetPkgInstallErr(errors.New("pkg: no network"))
	e.Run.HangOn = func(c run.Cmd) bool { return c.Name == "pkg" && slices.Contains(c.Args, "-j") }
	defer service.SetShellSetupDeadline(300 * time.Millisecond)()

	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	t0 := time.Now()
	op, err = s.JailAction(alpha, "web", api.JailAction{Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, s, op)
	if got.Status != api.OpSuccess {
		t.Fatalf("start: %+v\n%s", got, e.Log())
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Errorf("the start took %s", d)
	}
	if _, ok, _ := e.Jails.Get(alpha, "alpha_web"); !ok {
		t.Error("the jail does not run")
	}
	tried := false
	for _, c := range e.Run.Calls() {
		if c.Cmd.Name == "pkg" && slices.Contains(c.Cmd.Args, "-j") {
			tried = true
		}
	}
	if !tried {
		t.Error("pkg was never run inside the jail: the hang was not reached")
	}
}
