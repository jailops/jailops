package sdk_test

import (
	"io"
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func TestConsoleAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	if _, err := c.Jails().Console(ctx, "web", sdk.ExecOptions{}); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("stopped jail: %v", err)
	}
	if _, err := c.Jails().Console(ctx, "ghost", sdk.ExecOptions{}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("missing jail: %v", err)
	}
	op, err := c.Jails().Start(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := c.Jails().Console(ctx, "web", sdk.ExecOptions{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(40, 120); err != nil {
		t.Fatal(err)
	}
	// input is echoed; Ctrl-D ends the session with status 0
	go func() { _, _ = s.Write([]byte("héllo\n")); _, _ = s.Write([]byte{4}) }()
	out, _ := io.ReadAll(s.Output())
	if string(out) != "héllo\n" {
		t.Fatalf("out %q", out)
	}
	if code, err := s.Wait(); err != nil || code != 0 {
		t.Fatalf("wait %d %v", code, err)
	}
	calls := srv.ConsoleCalls()
	if len(calls) != 1 || calls[0].Rows != 30 || calls[0].Cols != 100 || len(calls[0].Resizes) != 1 || calls[0].Resizes[0] != [2]uint16{40, 120} {
		t.Fatalf("calls %+v", calls)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Fatal("input after the end must fail")
	}
	_ = s.Close()

	// Close kills a running session
	s, err = c.Jails().Console(ctx, "web", sdk.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, s.Output())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _ := s.Wait(); code != 137 {
		t.Fatalf("killed session status %d", code)
	}
}
