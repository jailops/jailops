package sdk_test

import (
	"io"
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func TestExecAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	if _, err := c.Jails().Exec(ctx, "web", []string{"echo", "hi"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("stopped jail: %v", err)
	}
	op, err := c.Jails().Start(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := c.Jails().Exec(ctx, "web", []string{"echo", "hi"}, sdk.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Stderr() == nil {
		t.Fatal("stderr must exist outside TTY mode")
	}
	go io.Copy(io.Discard, s.Stderr())
	out, _ := io.ReadAll(s.Stdout())
	if string(out) != "hi\n" {
		t.Fatalf("out %q", out)
	}
	if code, err := s.Wait(); err != nil || code != 0 {
		t.Fatalf("wait %d %v", code, err)
	}

	s, err = c.Jails().Exec(ctx, "web", []string{"cat"}, sdk.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, s.Stderr())
	go func() { s.Stdin().Write([]byte("ping")); s.Stdin().Close() }()
	out, _ = io.ReadAll(s.Stdout())
	if string(out) != "ping" {
		t.Fatalf("cat %q", out)
	}
	if code, _ := s.Wait(); code != 0 {
		t.Fatal(code)
	}

	s, err = c.Jails().Exec(ctx, "web", []string{"exit", "7"}, sdk.ExecOptions{TTY: true, Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	if s.Stderr() != nil {
		t.Fatal("TTY has no stderr")
	}
	io.Copy(io.Discard, s.Stdout())
	if code, _ := s.Wait(); code != 7 {
		t.Fatalf("exit %d", code)
	}
	if n := len(srv.ExecCalls()); n != 3 {
		t.Fatalf("calls %d", n)
	}
	if _, err := c.Jails().Exec(ctx, "nope", []string{"ls"}, sdk.ExecOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
}
