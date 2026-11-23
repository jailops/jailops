package service_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// JailOps never installs a shell: a jail whose preferred shell is bash starts without any pkg run for it, a shell
// session fails with a clear precondition error and runs nothing else, and once the administrator has installed bash
// the next session gets it, without a restart.
func TestAShellThatIsNotInstalledIsAnError(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(alpha, dtest.WebJail("web")) // the test host's default shell is bash
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
	for _, c := range e.Run.Calls() {
		if c.Cmd.Name == "pkg" && slices.Contains(c.Cmd.Args, "install") && (slices.Contains(c.Cmd.Args, "-j") || slices.Contains(c.Cmd.Args, api.BashPackage)) {
			t.Errorf("a start ran pkg install: %v", c.Cmd.Args)
		}
	}
	j, err := s.GetJail(alpha, "web")
	if err != nil {
		t.Fatal(err)
	}
	if j.Spec.Shell != api.BashPath || j.Status.ShellSetup != nil {
		t.Fatalf("shell %q, setup %+v", j.Spec.Shell, j.Status.ShellSetup)
	}

	n := len(e.FakeExec.Calls())
	_, err = s.StartExec(alpha, "web", api.ExecRequest{Shell: true})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodePreconditionFailed || ae.Message != "the shell /usr/local/bin/bash is not installed in jail web" ||
		!strings.HasPrefix(ae.Hint, "install it with: jailops pkg install web -- bash") {
		t.Fatalf("a shell that is not installed: %v", err)
	}
	if len(e.FakeExec.Calls()) != n {
		t.Fatal("something ran in place of the missing shell")
	}
	// exec is not a shell session
	if _, err := s.StartExec(alpha, "web", api.ExecRequest{Argv: []string{"pkg", "install", "-y", "bash"}}); err != nil {
		t.Fatalf("exec: %v", err)
	}

	e.Files.Put("/jailops/projects/alpha/jails/web/root"+api.BashPath, []byte("x"))
	if _, err := s.StartExec(alpha, "web", api.ExecRequest{Shell: true}); err != nil {
		t.Fatalf("bash after its installation: %v", err)
	}
	calls := e.FakeExec.Calls()
	last := calls[len(calls)-1]
	if !slices.Equal(last.Argv, []string{api.BashPath}) || slices.ContainsFunc(last.Opts.Env, func(v string) bool { return strings.HasPrefix(v, "ENV=") }) {
		t.Fatalf("the session: %v %v", last.Argv, last.Opts.Env)
	}
}

// root's interactive sh reads /root/.shrc, where the prompt is: the session gets ENV, as a login would.
func TestAnShSessionReadsTheShrc(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	j := dtest.WebJail("web")
	j.Spec.Shell = api.ShellSh
	op, err := s.CreateJail(alpha, j)
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
	e.Files.Put("/jailops/projects/alpha/jails/web/root"+api.ShPath, []byte("x"))
	if _, err := s.StartExec(alpha, "web", api.ExecRequest{Shell: true}); err != nil {
		t.Fatal(err)
	}
	calls := e.FakeExec.Calls()
	last := calls[len(calls)-1]
	if !slices.Equal(last.Argv, []string{api.ShPath}) || !slices.Contains(last.Opts.Env, "ENV="+api.ShrcPath) || !slices.Contains(last.Opts.Env, "HOME=/root") {
		t.Fatalf("the session: %v %v", last.Argv, last.Opts.Env)
	}
	// another user's sh is left to its own files
	if _, err := s.StartExec(alpha, "web", api.ExecRequest{Shell: true, User: "www"}); err != nil {
		t.Fatal(err)
	}
	calls = e.FakeExec.Calls()
	if slices.ContainsFunc(calls[len(calls)-1].Opts.Env, func(v string) bool { return strings.HasPrefix(v, "ENV=") }) {
		t.Fatalf("ENV for another user: %v", calls[len(calls)-1].Opts.Env)
	}
}
