package priv

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// What app access asks of the helper goes through the real checks: psql as
// postgres in a managed jail with the statements on standard input (nothing of
// them in the argument vector), chown and chmod of pg_hba.conf, the restart
// wrapper, and the write of pg_hba.conf below the jail root through the file
// operations. A jail the helper does not manage and a path outside the roots
// are refused.
func TestAppAccessThroughTheHelper(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	root := filepath.Join(e.cfg.MountBase, "projects", "alpha", "jails", "db", "root")
	if err := os.MkdirAll(filepath.Join(root, "var/db/postgres/data18"), 0o755); err != nil {
		t.Fatal(err)
	}
	var users, stdins []string
	e.jails.SetExecFn(func(name string, o jailos.ExecOpts) (run.Result, error) {
		users = append(users, o.User)
		if o.Stdin != nil {
			b, _ := io.ReadAll(o.Stdin)
			stdins = append(stdins, string(b))
		}
		return run.Result{}, nil
	})
	jails := e.local.Jails()
	sql := apps.Script([]apps.Statement{apps.CreateRole("web", "SCRAM-SHA-256$4096:c2FsdA==$a:b"), apps.CreateDatabase("web", "web")})
	psql := jailos.ExecOpts{Argv: apps.PsqlArgv("postgres"), User: apps.PsqlUser, Stdin: strings.NewReader(sql)}
	if _, err := jails.Exec(ctx, "alpha_db", psql); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("psql in an unmanaged jail: %v", err)
	}
	e.conf(t, "alpha_db")
	if err := e.jails.Create(ctx, filepath.Join(e.cfg.ConfDir, "alpha_db.conf"), "alpha_db"); err != nil {
		t.Fatal(err)
	}
	psql.Stdin = strings.NewReader(sql)
	restart, err := apps.ServiceRestartArgv("postgresql")
	if err != nil {
		t.Fatal(err)
	}
	hba := "/var/db/postgres/data18/pg_hba.conf"
	if _, err := jails.Exec(ctx, "alpha_db", psql); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{apps.HBAOwnerArgv(hba), apps.HBAModeArgv(hba), restart} {
		if _, err := jails.Exec(ctx, "alpha_db", jailosExec(argv...)); err != nil {
			t.Errorf("%q: %v", argv, err)
		}
	}
	if !slices.Equal(users, []string{"postgres", "", "", ""}) || len(stdins) != 1 || stdins[0] != sql {
		t.Fatalf("users %q stdin %q", users, stdins)
	}
	for _, call := range e.jails.Calls() {
		if strings.Contains(strings.Join(call.Argv, " "), "SCRAM") || strings.Contains(strings.Join(call.Argv, " "), "CREATE") {
			t.Fatalf("SQL in an argument vector: %q", call.Argv)
		}
	}
	// a user that is a flag or carries a shell character is refused
	for _, u := range []string{"-x", "postgres;id", "post gres"} {
		if _, err := jails.Exec(ctx, "alpha_db", jailos.ExecOpts{Argv: apps.PsqlArgv("postgres"), User: u}); err == nil {
			t.Errorf("user %q accepted", u)
		}
	}
	// pg_hba.conf below the jail root, where the data volume is mounted, through the file operations
	path := root + hba
	content, err := apps.SetHBABlock("local all all trust\n", []string{"host web web 192.0.2.7/32 scram-sha-256"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.local.WriteFile(ctx, WriteFileReq{Path: path, Data: []byte(content), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if b, err := e.local.ReadFile(ctx, path); err != nil || string(b) != content {
		t.Fatalf("%q %v", b, err)
	}
	for _, p := range []string{"/var/db/postgres/data18/pg_hba.conf", root + "/../../../../../../../tmp/pg_hba.conf", "/etc/pg_hba.conf"} {
		if err := e.local.WriteFile(ctx, WriteFileReq{Path: p, Data: []byte(content), Mode: 0o644}); err == nil {
			t.Errorf("wrote %s", p)
		}
	}
}
