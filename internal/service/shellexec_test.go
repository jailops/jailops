package service_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// While bash is being installed in the background, a session that needs no bash enters the jail at once, the
// condition names the step and since when, and a package operation of someone else is refused with its name and age.
func TestASessionWithoutBashEntersWhileTheShellIsSetUp(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.Run.SetHangOn(func(c run.Cmd) bool {
		return c.Name == "pkg" && slices.Contains(c.Args, "-j") && slices.Contains(c.Args, "install")
	})
	defer service.SetShellSetupDeadline(5 * time.Second)()
	defer service.SetShellSettle(200*time.Millisecond, 200*time.Millisecond)()

	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	op, err = s.JailAction(alpha, "web", api.JailAction{Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("start: %+v\n%s", got, e.Log())
	}
	j, err := s.GetJail(alpha, "web")
	if err != nil {
		t.Fatal(err)
	}
	var cond *api.Condition
	for i := range j.Status.Conditions {
		if j.Status.Conditions[i].Type == "ShellSetup" {
			cond = &j.Status.Conditions[i]
		}
	}
	if cond == nil || cond.Reason != api.ShellInstalling || api.ShellInstallingStep(cond.Message) == "" || cond.Since.IsZero() {
		t.Fatalf("the condition does not name the step and the time: %+v", cond)
	}

	// a command that needs no bash is not kept out
	t0 := time.Now()
	st, err := s.StartExec(alpha, "web", api.ExecRequest{Argv: []string{"/bin/sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("a session without bash while the shell is set up: %v", err)
	}
	if d := time.Since(t0); d > 4*time.Second {
		t.Errorf("the session waited %s for the setup", d)
	}
	_ = st

	// a package operation of someone else gives the setup of the shell a moment and then ends it; a second one is
	// refused, with what holds the jail and since when
	release, err := service.HoldForTest(s, dtest.SeedProject+"_web", "an app installation")
	if err != nil {
		t.Fatalf("an app installation does not get the jail from the setup of the shell: %v", err)
	}
	defer release()
	_, err = service.HoldForTest(s, dtest.SeedProject+"_web", "a package update")
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeBusy || !strings.Contains(ae.Message, "an app installation") || !strings.Contains(ae.Message, "started") {
		t.Errorf("the refusal does not name the operation and its age: %v", err)
	}
}

// A stop that comes while pkg hangs in the background setup of the shell gives the setup a moment, then ends it
// gently (the command carries a grace for SIGTERM and fetch limits that let a dead address family fail fast), and goes
// on: the jail stops, and the setup is no failure.
func TestAStopCancelsTheSetupOfTheShellInsteadOfWaitingForIt(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.Run.SetHangOn(func(c run.Cmd) bool {
		return c.Name == "pkg" && slices.Contains(c.Args, "-j") && slices.Contains(c.Args, "install")
	})
	defer service.SetShellSetupDeadline(30 * time.Second)()
	defer service.SetShellSettle(200*time.Millisecond, 200*time.Millisecond)()

	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	op, err = s.JailAction(alpha, "web", api.JailAction{Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("start: %+v\n%s", got, e.Log())
	}
	t0 := time.Now()
	op, err = s.JailAction(alpha, "web", api.JailAction{Action: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("stop: %+v\n%s", got, e.Log())
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Errorf("the stop waited %s for the setup of the shell", d)
	}
	j, err := s.GetJail(alpha, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range j.Status.Conditions {
		if c.Type == "ShellSetup" {
			t.Errorf("a cancelled setup is no condition: %+v", c)
		}
	}
	found := false
	for _, c := range e.Run.Calls() {
		if c.Cmd.Name == "pkg" && slices.Contains(c.Cmd.Args, "-j") && slices.Contains(c.Cmd.Args, "install") {
			found = true
			if c.Cmd.TermGrace != 200*time.Millisecond || !slices.Contains(c.Cmd.Args, "FETCH_TIMEOUT=10") || !slices.Contains(c.Cmd.Args, "FETCH_RETRY=1") {
				t.Errorf("pkg runs without a gentle end or fetch limits: %+v", c.Cmd)
			}
		}
	}
	if !found {
		t.Error("pkg never ran in the jail")
	}
}
