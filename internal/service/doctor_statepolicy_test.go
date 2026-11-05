package service_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// loadedRules answers `pfctl -s rules` with a ruleset of the test; everything else goes to the fakes.
type loadedRules struct {
	run.Runner
	rules string
}

func (l loadedRules) Run(ctx context.Context, c run.Cmd) (run.Result, error) {
	if c.Name == "pfctl" && strings.Join(c.Args, " ") == "-s rules" {
		return run.Result{Stdout: []byte(l.rules)}, nil
	}
	return l.Runner.Run(ctx, c)
}

// doctor (pf.state-policy) warns about a host whose loaded ruleset blocks by default while jails are routed through it
// and whose pf.conf (the one rc.conf names) does not bind states to interfaces; it cannot tell without that file.
func TestDoctorStatePolicy(t *testing.T) {
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) {
		c.Runner = loadedRules{c.Runner, "scrub in all fragment reassemble\nblock drop log all\n" + `anchor "jailops/*" all` + "\npass out all flags S/SA keep state"}
	}})
	e.Seed()
	s := e.D.Service()
	createJail(t, e, s, "web")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(e.Host.Etc, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func() api.DoctorCheck {
		t.Helper()
		for _, c := range s.Doctor(as(globalAdmin, "")).Checks {
			if c.Name == "pf.state-policy" {
				return c
			}
		}
		t.Fatal("no pf.state-policy check")
		return api.DoctorCheck{}
	}
	write("pf.conf", "# set state-policy if-bound\nblock log all\n")
	if c := check(); c.Status != api.DoctorWarn || !strings.Contains(c.Hint, "set state-policy if-bound") ||
		!strings.Contains(c.Hint, "pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf") || !strings.Contains(c.Hint, "never `service pf restart`") {
		t.Fatalf("%+v", c)
	}
	write("pf.conf", "set skip on lo0\nset state-policy if-bound\nblock log all\n")
	if c := check(); c.Status != api.DoctorOK {
		t.Fatalf("%+v", c)
	}
	write("rc.conf", dtest.HostRC+"pf_rules=\"/etc/pf.jails.conf\" # ours\n")
	if c := check(); c.Status != api.DoctorUnknown || !strings.Contains(c.Message, "/etc/pf.jails.conf cannot be read") {
		t.Fatalf("the file rc.conf names, missing: %+v", c)
	}
	write("pf.jails.conf", "block log all\n")
	if c := check(); c.Status != api.DoctorWarn || !strings.Contains(c.Hint, "pfctl -nf /etc/pf.jails.conf") {
		t.Fatalf("%+v", c)
	}
}

// A pf.conf only root can read (mode 0600) is still checked: the daemon cannot open it, so the helper (root) does.
func TestDoctorStatePolicyUnreadablePFConf(t *testing.T) {
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) {
		c.Runner = loadedRules{c.Runner, "block drop log all\n" + `anchor "jailops/*" all`}
		c.HostFile = func(p string) ([]byte, error) {
			if p == "/etc/pf.conf" {
				return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
			}
			return nil, fs.ErrNotExist
		}
	}})
	e.Seed()
	s := e.D.Service()
	createJail(t, e, s, "web")
	check := func() api.DoctorCheck {
		t.Helper()
		for _, c := range s.Doctor(as(globalAdmin, "")).Checks {
			if c.Name == "pf.state-policy" {
				return c
			}
		}
		t.Fatal("no pf.state-policy check")
		return api.DoctorCheck{}
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(e.Host.Etc, "pf.conf"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("set skip on lo0\nset state-policy if-bound\nblock log all\n")
	if c := check(); c.Status != api.DoctorOK || !strings.Contains(c.Message, "binds states") {
		t.Fatalf("with the line, read by the helper: %+v", c)
	}
	write("set skip on lo0\n# set state-policy if-bound\nblock log all\n")
	if c := check(); c.Status != api.DoctorWarn || !strings.Contains(c.Hint, "set state-policy if-bound") {
		t.Fatalf("without the line: %+v", c)
	}
}
