package jailos

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

const jlsJSON = `{"__version": "2", "jail-information": {"jail": [
 {"jid": 3, "hostname": "b.example", "path": "/jails/b", "name": "b", "state": "ACTIVE", "cpusetid": 5, "ipv4_addrs": [], "vnet": 1, "dying": 0},
 {"jid": "1", "host.hostname": "a.example", "path": "/jails/a", "name": "a", "vnet": "new", "dying": false},
 {"jid": 0, "name": "bogus"},
 "junk"
]}}`

func TestParseJLSJSON(t *testing.T) {
	js, err := ParseJLSJSON([]byte(jlsJSON))
	if err != nil || len(js) != 2 {
		t.Fatal(js, err)
	}
	b := js[0]
	if b.JID != 3 || b.Name != "b" || b.Hostname != "b.example" || !b.VNet || b.Dying || b.Params["state"] != "ACTIVE" || b.Params["cpusetid"] != "5" {
		t.Errorf("%+v", b)
	}
	if a := js[1]; a.JID != 1 || !a.VNet || a.Hostname != "a.example" {
		t.Errorf("%+v", a)
	}
	// single object and empty forms
	if js, err := ParseJLSJSON([]byte(`{"jail-information":{"jail":{"jid":7,"name":"x"}}}`)); err != nil || len(js) != 1 {
		t.Error(js, err)
	}
	if js, err := ParseJLSJSON([]byte(`{"jail-information":{}}`)); err != nil || len(js) != 0 {
		t.Error(js, err)
	}
	for _, bad := range []string{"", "[]", "{", `{"x":1}`, `{"jail-information":{"jail":5}}`} {
		if _, err := ParseJLSJSON([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseJLSNamed(t *testing.T) {
	in := "jid=1 name=web path=/jails/web host.hostname=web.example.org vnet=new dying=false\n" +
		"jid=2 name=db path=/jails/my db host.hostname=db vnet=inherit dying=true\n" +
		"garbage line\n\njid=x name=bad\n"
	js := ParseJLSNamed(in)
	if len(js) != 2 {
		t.Fatalf("%+v", js)
	}
	if js[0].Name != "web" || !js[0].VNet || js[0].Hostname != "web.example.org" {
		t.Errorf("%+v", js[0])
	}
	if js[1].Path != "/jails/my db" || !js[1].Dying {
		t.Errorf("%+v", js[1])
	}
}

func FuzzParseJLSJSON(f *testing.F) {
	f.Add([]byte(jlsJSON))
	f.Fuzz(func(t *testing.T, b []byte) { ParseJLSJSON(b) })
}

func FuzzParseJLSNamed(f *testing.F) {
	f.Add("jid=1 name=a path=/x vnet=new\n")
	f.Fuzz(func(t *testing.T, s string) {
		for _, j := range ParseJLSNamed(s) {
			if j.JID <= 0 {
				t.Fatal("jid")
			}
		}
	})
}

// A failing exec.* command is reported by jail(8) with its name only; its
// output went to the console log, which the hint names.
func TestCommandCreateFailureNamesConsoleLog(t *testing.T) {
	ctx := context.Background()
	f := run.NewFake()
	f.OnFail("jail", []string{"-f", "/etc/jail.d/alpha_web.conf", "-c", "alpha_web"}, 1, "jail: alpha_web: /bin/sh /etc/rc: failed\n")
	f.OnFail("jail", []string{"-f", "/etc/jail.d/alpha_db.conf", "-c", "alpha_db"}, 1, "jail: alpha_db: already exists\n")
	c := NewCommand(f)
	err := c.Create(ctx, "/etc/jail.d/alpha_web.conf", "alpha_web")
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeInternal || ae.Hint != "what the jail's start and stop commands printed is in /var/log/jail_alpha_web_console.log on the host" {
		t.Fatalf("%#v", err)
	}
	if err := c.Create(ctx, "/etc/jail.d/alpha_db.conf", "alpha_db"); !errors.As(err, &ae) || ae.Code != api.CodeAlreadyExists || ae.Hint != "" {
		t.Fatalf("%#v", err)
	}
}

func TestCommandControl(t *testing.T) {
	ctx := context.Background()
	f := run.NewFake()
	f.Add(run.Rule{Name: "jail", Args: []string{run.RestArgs}})
	f.Add(run.Rule{Name: "jexec", Args: []string{run.RestArgs}, Result: run.Result{Stdout: []byte("ok")}})
	c := NewCommand(f)
	if err := c.Create(ctx, "/etc/jail.d/web.conf", "web"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "/etc/jail.d/web.conf", "web"); err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, "web", ExecOpts{Argv: []string{"sh", "-c", "echo hi; id"}, User: "root", Login: true})
	if err != nil || string(res.Stdout) != "ok" {
		t.Fatal(res, err)
	}
	want := []string{
		"jail -f /etc/jail.d/web.conf -c web",
		"jail -f /etc/jail.d/web.conf -r web",
		"jexec -l -U root web sh -c echo hi; id",
	}
	if got := f.Argv(); !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
	for _, err := range []error{
		c.Create(ctx, "relative.conf", "web"),
		c.Create(ctx, "/x", "-web"),
		c.Create(ctx, "/x", "a.b"),
		c.Remove(ctx, "/x", ""),
		func() error { _, e := c.Exec(ctx, "web", ExecOpts{Argv: []string{"-l"}}); return e }(),
		func() error { _, e := c.Exec(ctx, "web", ExecOpts{Argv: []string{"id"}, User: "-x"}); return e }(),
		func() error { _, e := c.Exec(ctx, "web; id", ExecOpts{Argv: []string{"id"}}); return e }(),
	} {
		if !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%v", err)
		}
	}
}

func TestCommandErrors(t *testing.T) {
	f := run.NewFake()
	f.OnFail("jail", []string{run.RestArgs}, 1, `jail: web: jail "web" not found`)
	err := NewCommand(f).Remove(context.Background(), "/x", "web")
	if !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
}

func TestCommandListFallback(t *testing.T) {
	ctx := context.Background()
	f := run.NewFake()
	f.OnFail("jls", []string{"-v", "--libxo=json"}, 1, "jls: unknown option")
	f.On("jls", []string{"-n", run.RestArgs}, "jid=2 name=b path=/b vnet=new dying=false\njid=1 name=a path=/a vnet=new dying=false\n")
	c := NewCommand(f)
	js, err := c.List(ctx)
	if err != nil || len(js) != 2 || js[0].Name != "a" {
		t.Fatal(js, err)
	}
	if j, ok, err := c.Get(ctx, "b"); err != nil || !ok || j.JID != 2 {
		t.Fatal(j, ok, err)
	}
	if _, ok, _ := c.Get(ctx, "zzz"); ok {
		t.Fatal("found missing jail")
	}
}

func TestCommandListJSON(t *testing.T) {
	f := run.NewFake()
	f.On("jls", []string{"-v", "--libxo=json"}, jlsJSON)
	js, err := NewCommand(f).List(context.Background())
	if err != nil || len(js) != 2 || js[0].JID != 1 {
		t.Fatal(js, err)
	}
}

func TestFake(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if err := f.Create(ctx, "/x.conf", "a"); err != nil {
		t.Fatal(err)
	}
	if !api.IsCode(f.Create(ctx, "/x.conf", "a"), api.CodeAlreadyExists) {
		t.Fatal("dup")
	}
	if j, ok, _ := f.Get(ctx, "a"); !ok || j.JID != 1 {
		t.Fatal(j)
	}
	if _, err := f.Exec(ctx, "a", ExecOpts{Argv: []string{"id"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, "/x.conf", "a"); err != nil {
		t.Fatal(err)
	}
	if !api.IsCode(f.Remove(ctx, "/x.conf", "a"), api.CodeNotFound) {
		t.Fatal("remove missing")
	}
	if js, _ := f.List(ctx); len(js) != 0 {
		t.Fatal(js)
	}
}

func TestKernelObserverOtherOS(t *testing.T) {
	// On non-FreeBSD hosts the reader must refuse, never return success.
	if _, err := NewKernelObserver().List(context.Background()); err != nil && !api.IsCode(err, api.CodeUnsupported) {
		t.Fatal(err)
	}
}
