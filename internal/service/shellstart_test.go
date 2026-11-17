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

// A start never waits on the shell: bash cannot be fetched before the start and pkg inside the jail never returns (a
// jail whose firewall is closed), and the jail still starts, at the deadline of the setup, with the shell it has.
func TestStartDoesNotWaitOnTheShellSetup(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.SetPkgInstallErr(errors.New("pkg: no network"))
	e.Run.SetHangOn(func(c run.Cmd) bool { return c.Name == "pkg" && slices.Contains(c.Args, "-j") })
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
	// the setup runs in the background after the start: wait for it to reach the pkg that hangs
	tried := false
	for deadline := time.Now().Add(10 * time.Second); !tried && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, c := range e.Run.Calls() {
			if c.Cmd.Name == "pkg" && slices.Contains(c.Cmd.Args, "-j") {
				tried = true
			}
		}
	}
	if !tried {
		t.Error("pkg was never run inside the jail: the hang was not reached")
	}
}

// The first start never waits for a package server: the jail starts at once, the host does not fetch bash before
// the start, and the jail says right after the start that bash is being installed in the background.
func TestFirstStartNeverWaitsForTheHost(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.Run.SetHangOn(func(c run.Cmd) bool {
		return c.Name == "pkg" && (slices.Contains(c.Args, "-r") || slices.Contains(c.Args, "-j")) && slices.Contains(c.Args, "install")
	})
	defer service.SetShellSetupDeadline(2 * time.Second)()

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
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("start: %+v\n%s", got, e.Log())
	}
	if d := time.Since(t0); d > 1500*time.Millisecond {
		t.Errorf("the start took %s: it waited for a package server", d)
	}
	for _, c := range e.Run.Calls() {
		if c.Cmd.Name == "pkg" && slices.Contains(c.Cmd.Args, "install") && slices.ContainsFunc(c.Cmd.Args, func(a string) bool { return strings.HasSuffix(a, "/jails/web/root") }) {
			t.Errorf("the host fetched bash before the first start: %v", c.Cmd.Args)
		}
	}
	j, err := s.GetJail(alpha, "web")
	if err != nil {
		t.Fatal(err)
	}
	installing := false
	for _, c := range j.Status.Conditions {
		installing = installing || (c.Type == "ShellSetup" && c.Reason == api.ShellInstalling)
	}
	if !installing {
		t.Errorf("the jail does not say that bash is being installed: %+v", j.Status.Conditions)
	}
}

// A shell session in a jail whose bash is being installed in the background waits for that, instead of answering
// that the shell does not exist; when the installation fails it says why.
func TestShellWaitsForTheBashOfTheBackground(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.Run.SetHangOn(func(c run.Cmd) bool {
		return c.Name == "pkg" && slices.Contains(c.Args, "-j") && slices.Contains(c.Args, "install")
	})
	defer service.SetShellSetupDeadline(600 * time.Millisecond)()

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
	_, err = s.StartExec(alpha, "web", api.ExecRequest{Shell: true})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeNotFound {
		t.Fatalf("a shell without bash: %v", err)
	}
	if d := time.Since(t0); d < 300*time.Millisecond {
		t.Errorf("the session did not wait for the installation of bash (%s)", d)
	}
	if !strings.Contains(ae.Hint, "the setup of bash failed") {
		t.Errorf("the hint does not say why: %q", ae.Hint)
	}
}

// A start after the first one, while bash is still missing, lets the host fetch it into the stopped root, three
// times at most; a failed setup in the background is not repeated at every start after that.
func TestRestartsOfAJailWithoutBashAreBounded(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	e.SetPkgInstallErr(errors.New("pkg: no network"))
	defer service.SetBashBeforeStartDeadline(300 * time.Millisecond)()
	defer service.SetShellSetupDeadline(300 * time.Millisecond)()

	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	act := func(action string) {
		t.Helper()
		op, err := s.JailAction(alpha, "web", api.JailAction{Action: action})
		if err != nil {
			t.Fatal(err)
		}
		if got := wait(t, s, op); got.Status != api.OpSuccess {
			t.Fatalf("%s: %+v\n%s", action, got, e.Log())
		}
	}
	pkgCalls := func() (host, inJail int) {
		for _, c := range e.Run.Calls() {
			if c.Cmd.Name != "pkg" || !slices.Contains(c.Cmd.Args, "install") {
				continue
			}
			switch {
			case slices.Contains(c.Cmd.Args, "-j"):
				inJail++
			case slices.ContainsFunc(c.Cmd.Args, func(a string) bool { return strings.HasSuffix(a, "/jails/web/root") }):
				host++ // the root of the jail, not the staging root of an image build
			}
		}
		return
	}
	act("start") // the first start: no host install; the jail's own attempt in the background
	time.Sleep(500 * time.Millisecond)
	if host, jail := pkgCalls(); host != 0 || jail != 1 {
		t.Fatalf("the first start made %d installs from the host and %d in the jail, want 0 and 1\n%s", host, jail, e.Log())
	}
	for i := 0; i < 6; i++ {
		act("restart")
		time.Sleep(400 * time.Millisecond)
	}
	// three tries of the host (the constant of the service), each followed by an attempt in the jail; later starts
	// repeat neither
	if host, jail := pkgCalls(); host != 3 || jail != 3 {
		t.Errorf("after six restarts: %d installs from the host and %d in the jail, want 3 and 3", host, jail)
	}
}

// An operation lists the steps it ran and how long each took, so that a slow start or stop explains itself in
// `jailops op show`.
func TestOperationListsItsSteps(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	for _, action := range []string{"start", "stop"} {
		op, err := s.JailAction(alpha, "web", api.JailAction{Action: action})
		if err != nil {
			t.Fatal(err)
		}
		got := wait(t, s, op)
		if got.Status != api.OpSuccess {
			t.Fatalf("%s: %+v\n%s", action, got, e.Log())
		}
		if len(got.Steps) == 0 {
			t.Fatalf("%s: the operation lists no steps: %+v", action, got)
		}
		if action == "stop" {
			names := []string{}
			for _, st := range got.Steps {
				names = append(names, st.Name)
			}
			for _, want := range []string{"jail-stop", "teardown", "unmount"} {
				if !slices.Contains(names, want) {
					t.Errorf("stop: step %q missing from %v", want, names)
				}
			}
		}
	}
}
