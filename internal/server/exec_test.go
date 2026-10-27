package server_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func startedWeb(t *testing.T, e *dtest.Env) {
	t.Helper()
	c := ctxT(t)
	mustJail(t, e, "web")
	so, _ := e.Admin.Jails().Start(c, "web")
	if _, err := so.Wait(c); err != nil {
		t.Fatal(err)
	}
}

func drain(s *sdk.ExecSession) (string, string, int) {
	var eb []byte
	done := make(chan struct{})
	if s.Stderr() != nil {
		go func() { eb, _ = io.ReadAll(s.Stderr()); close(done) }()
	} else {
		close(done)
	}
	ob, _ := io.ReadAll(s.Stdout())
	<-done
	code, _ := s.Wait()
	return string(ob), string(eb), code
}

func TestExecStreamsAuditsAndRespectsProjects(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	startedWeb(t, e)

	s, err := e.Admin.Jails().Exec(c, "web", []string{"echo", "hello", "world"}, sdk.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out, _, code := drain(s); out != "hello world\n" || code != 0 {
		t.Fatalf("%q %d", out, code)
	}
	s, _ = e.Admin.Jails().Exec(c, "web", []string{"stderr", "oops"}, sdk.ExecOptions{})
	if out, errs, code := drain(s); out != "" || errs != "oops\n" || code != 2 {
		t.Fatalf("%q %q %d", out, errs, code)
	}
	// Input reaches the command; a large write is split in frames.
	s, _ = e.Admin.Jails().Exec(c, "web", []string{"cat"}, sdk.ExecOptions{})
	big := strings.Repeat("x", 200<<10)
	go func() { s.Stdin().Write([]byte(big)); s.Stdin().Close() }()
	if out, _, code := drain(s); out != big || code != 0 {
		t.Fatalf("%d %d", len(out), code)
	}
	// TTY mode has no stderr.
	s, _ = e.Admin.Jails().Exec(c, "web", []string{"exit", "5"}, sdk.ExecOptions{TTY: true, Rows: 10, Cols: 20})
	if s.Stderr() != nil {
		t.Fatal("stderr in TTY mode")
	}
	if _, _, code := drain(s); code != 5 {
		t.Fatal(code)
	}
	// A command is killed with a signal.
	s, _ = e.Admin.Jails().Exec(c, "web", []string{"sleep"}, sdk.ExecOptions{})
	if err := s.Signal(15); err != nil {
		t.Fatal(err)
	}
	if _, _, code := drain(s); code == 0 {
		t.Fatal("a signalled sleep exited cleanly")
	}

	// Errors arrive before the stream starts.
	if _, err := e.Admin.Jails().Exec(c, "nope", []string{"ls"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if _, err := e.Admin.Project("other").Jails().Exec(c, "web", []string{"ls"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	// No command means the jail's shell; this jail has none.
	if _, err := e.Admin.Jails().Exec(c, "web", nil, sdk.ExecOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	// A read-only identity cannot exec.
	if _, err := e.Client(api.PermReadonly).Jails().Exec(c, "web", []string{"ls"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatal(err)
	}
	// A plain request without the upgrade is refused.
	if code, _ := e.Raw("POST", "/api/v1/jails/web/exec?project=alpha", e.Token(api.PermAdmin), `{"argv":["ls"]}`); code != 400 {
		t.Fatalf("%d", code)
	}

	// Every exec is audited with its argv, and the secrets stay out of it.
	entries, _ := e.Admin.Audit(c, 500)
	var execs []string
	for _, a := range entries {
		if strings.Contains(a.Action, "/exec") {
			execs = append(execs, a.Detail)
		}
	}
	if !strings.Contains(strings.Join(execs, "\n"), `argv=["echo" "hello" "world"]`) {
		t.Fatalf("exec audit: %q", execs)
	}
}

func TestExecLimits(t *testing.T) {
	e := dtest.New(t, dtest.Options{Mutate: func(c *daemon.Config) {
		c.Limits.ExecSessionsPerJail = 1
		c.Limits.ExecIdle = daemon.Duration(300 * time.Millisecond)
	}})
	e.Seed()
	c := ctxT(t)
	startedWeb(t, e)
	s, err := e.Admin.Jails().Exec(c, "web", []string{"sleep"}, sdk.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin.Jails().Exec(c, "web", []string{"echo", "x"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeBusy) && !api.IsCode(err, api.CodeQuotaExceeded) {
		t.Fatalf("second session: %v", err)
	}
	// The idle session is cut off and reports -1 or a kill status.
	done := make(chan int, 1)
	go func() { _, _, code := drain(s); done <- code }()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("idle session ended cleanly")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the idle timeout did not end the session")
	}
	// The slot is free again (the server releases it a moment after the client saw the end).
	for i := 0; ; i++ {
		s, err = e.Admin.Jails().Exec(c, "web", []string{"echo", "x"}, sdk.ExecOptions{})
		if err == nil {
			break
		}
		if i > 50 || !api.IsCode(err, api.CodeBusy) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	drain(s)
}

func TestExecThroughAHub(t *testing.T) {
	host, hub := hubPair(t, api.CertificateSpec{Permission: api.PermAdmin, Global: true})
	ctx := ctxT(t)
	w := hub.Scoped(api.PermAdmin, "web").Host("h1").Project("web")
	op, err := w.Jails().Create(ctx, dtest.WebJailOn("w", "lan-web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	so, _ := w.Jails().Start(ctx, "w")
	if _, err := so.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := w.Jails().Exec(ctx, "w", []string{"cat"}, sdk.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	go func() { s.Stdin().Write([]byte("via hub")); s.Stdin().Close() }()
	if out, _, code := drain(s); out != "via hub" || code != 0 {
		t.Fatalf("%q %d", out, code)
	}
	if _, err := hub.Scoped(api.PermAdmin, "web").Host("h1").Project("db").Jails().Exec(ctx, "w", []string{"ls"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if n := len(host.FakeExec.Calls()); n != 1 {
		t.Fatalf("host ran %d commands", n)
	}
}
