package sdk_test

import (
	"testing"

	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func TestJailLogsAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	files, err := c.Jails().Logs(ctx, "web")
	if err != nil || len(files) != 0 {
		t.Fatalf("no logs yet: %v %v", err, files)
	}
	srv.SeedJailLog("web", "web", "messages", []byte("Oct 29 11:00:00 web one\nOct 29 11:30:00 web two\n"))
	srv.SeedJailLog("web", "web", "messages.1.gz", []byte("zz"))
	srv.SeedJailLog("web", "web", "nginx/error.log", []byte("e\n"))
	files, err = c.Jails().Logs(ctx, "web")
	if err != nil || len(files) != 3 || files[0].Name != "messages" || files[1].Kind != api.JailLogRotated || !files[1].Compressed || files[2].Name != "nginx/error.log" {
		t.Fatalf("%v %+v", err, files)
	}
	r, err := c.Jails().Log(ctx, "web", "messages", api.JailLogQuery{Tail: 1})
	if err != nil || len(r.Lines) != 1 || r.Lines[0].Text != "Oct 29 11:30:00 web two" || !r.Truncated || r.NextOffset != 48 {
		t.Fatalf("%v %+v", err, r)
	}
	r, err = c.Jails().Log(ctx, "web", "messages", api.JailLogQuery{Offset: r.NextOffset})
	if err != nil || len(r.Lines) != 0 || r.Rotated {
		t.Fatalf("nothing new: %v %+v", err, r)
	}
	srv.SeedJailLog("web", "web", "messages", []byte("fresh\n"))
	r, err = c.Jails().Log(ctx, "web", "messages", api.JailLogQuery{Offset: 48})
	if err != nil || !r.Rotated || len(r.Lines) != 1 || r.Lines[0].Text != "fresh" || r.NextOffset != 6 {
		t.Fatalf("rotated: %v %+v", err, r)
	}
	if _, err := c.Jails().Log(ctx, "web", "messages.1.gz", api.JailLogQuery{}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("compressed: %v", err)
	}
	if _, err := c.Jails().Log(ctx, "web", "nope", api.JailLogQuery{}); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := c.Jails().Log(ctx, "web", "../etc/passwd", api.JailLogQuery{}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := c.Jails().Log(ctx, "web", "messages", api.JailLogQuery{Tail: 9999}); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("tail limit: %v", err)
	}
	if _, err := c.Jails().Logs(ctx, "ghost"); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("ghost: %v", err)
	}
	srv.SeedJailUnmounted("web", "web", true)
	if _, err := c.Jails().Logs(ctx, "web"); !api.IsCode(err, api.CodeBusy) {
		t.Errorf("unmounted: %v", err)
	}
	// a server without the extension
	srv2, c2 := newEnv(t, fake.WithExtensions("jails"))
	seedBasics(srv2)
	if _, err := c2.Jails().Logs(ctx, "web"); !api.IsCode(err, api.CodeUnsupported) {
		t.Errorf("without the extension: %v", err)
	}
}
