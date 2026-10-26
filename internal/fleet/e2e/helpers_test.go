package e2e

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"jailops.net/jailops/internal/fleet/cli"
	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/fleettest"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/internal/fleet/vault"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

type result struct {
	code     int
	out, err string
}

func (r result) all() string { return r.out + r.err }

// run executes the real jailops-sync command line against the environment.
func run(t *testing.T, e *fleettest.Env, args ...string) result {
	t.Helper()
	return runIn(t, e, "", args...)
}

func runIn(t *testing.T, e *fleettest.Env, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		switch k {
		case secrets.EnvIdentityFile:
			return e.Identity
		case cli.EnvStateDir:
			return e.State
		case cli.EnvFleetPath:
			return e.Dir
		case vault.EnvKey, vault.EnvKeyFile, vault.EnvKeyCmd:
			return os.Getenv(k) // SOPS reads the process environment itself
		}
		return ""
	}
	code := cli.Run(cli.Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Getenv: getenv, Version: "test"}, args)
	return result{code, out.String(), errb.String()}
}

func mustOK(t *testing.T, r result) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.out, r.err)
	}
	return r
}

func mustFail(t *testing.T, r result, want string) result {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("expected failure, got success\nstdout:\n%s", r.out)
	}
	if want != "" && !strings.Contains(r.all(), want) {
		t.Fatalf("output lacks %q:\nstdout:\n%s\nstderr:\n%s", want, r.out, r.err)
	}
	return r
}

func contains(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("missing %q in:\n%s", want, s)
	}
}

func client(t *testing.T, h *fleettest.Host) *sdk.Client {
	t.Helper()
	var opts []sdk.Option
	if h.Token != "" {
		opts = append(opts, sdk.WithToken(h.Token))
	}
	c, err := sdk.Connect(context.Background(), sdk.Remote(h.URL(), opts...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func getJail(t *testing.T, h *fleettest.Host, name string) api.Jail {
	t.Helper()
	j, err := client(t, h).Project(fleettest.Project).Jails().Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// standardFleet is three hosts, the standard shared objects and one jail each.
func standardFleet(t *testing.T) *fleettest.Env {
	t.Helper()
	e := fleettest.New(t, "testfleet", "web1", "web2", "db1")
	e.StandardObjects()
	e.Jail("web1", "blog", "tier: web", "")
	e.Jail("web2", "wiki", "tier: web", "")
	e.Jail("db1", "postgres", "tier: db", "")
	return e
}

type fleettestEnv = fleettest.Env

func newEnv(t *testing.T, id string, hosts ...string) *fleettest.Env {
	return fleettest.New(t, id, hosts...)
}

func loadFleet(t *testing.T, e *fleettest.Env) *config.Fleet {
	t.Helper()
	f, err := config.Load(e.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	return f
}

func getJailIn(t *testing.T, e *fleettestEnv, project, name string) api.Jail {
	t.Helper()
	for _, h := range e.Hosts {
		j, err := client(t, h).Project(project).Jails().Get(context.Background(), name)
		if err == nil {
			return j
		}
	}
	t.Fatalf("jail %s/%s not found", project, name)
	return api.Jail{}
}
