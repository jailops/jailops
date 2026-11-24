package daemon_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	fcli "jailops.net/jailops/internal/fleet/cli"
	"jailops.net/jailops/internal/fleet/fleettest"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/sdk/api"
)

// jailops-sync against the real daemon: the real API and the real recipe engine, with the commands in the jails played by
// the test (dtest). It is the one place where the decision of sync meets what the engine reads in a jail.

func runSyncCLI(t *testing.T, fe *fleettest.Env, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		switch k {
		case secrets.EnvIdentityFile:
			return fe.Identity
		case fcli.EnvStateDir:
			return fe.State
		case fcli.EnvFleetPath:
			return fe.Dir
		}
		return ""
	}
	code := fcli.Run(fcli.Env{Stdout: &out, Stderr: &errb, Getenv: getenv, Version: "test"}, args)
	s := out.String() + errb.String()
	t.Logf("$ jailops-sync %s  (exit %d)\n%s", strings.Join(args, " "), code, s)
	return code, s
}

func syncJailYAML(name, extra string) string {
	return "apiVersion: jailops/v1\nkind: Jail\nhosts: [h1]\nmetadata:\n  name: " + name + "\nstart: true\n" + extra +
		"spec:\n  image: fimg\n  storage: default\n  network: [{network: fnet, ipv4: auto}]\n  firewall: {policies: [web]}\n"
}

const syncNginxRecipe = `vars:
  server_name: {default: localhost}
steps:
  - package: [nginx]
  - dir: {path: /var/db/site, mode: "0750"}
  - file:
      path: /usr/local/etc/nginx/nginx.conf
      source: nginx.conf
      template: true
  - service: {name: nginx, enabled: true, running: true}
  - run: [nginx, -t]
`

func TestSyncRecipesWithTheRealDaemon(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	aj := &applyJails{running: map[string]bool{}, users: map[string]bool{}}
	e.Jails.SetExecFn(aj.fn)
	for _, j := range []string{"blog", "api", "wiki"} { // the root of each jail, as the host has it
		if err := os.MkdirAll(filepath.Join(e.JailRootDir("web", j), "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fe := fleettest.New(t, "proto")
	fe.WriteSecret("h1-token", e.Token(api.PermAdmin))
	fe.Write("hosts/h1/host.yaml", "apiVersion: jailops/v1\nkind: Host\nmetadata:\n  name: h1\nspec:\n  url: "+e.HTTP.URL+"\n  token_secret: h1-token\n")
	fe.Write("shared/images/fimg.yaml", "apiVersion: jailops/v1\nkind: Image\nmetadata:\n  name: fimg\nbuild: true\nspec:\n  os: freebsd\n  release: 15.1-RELEASE\n  arch: amd64\n  source: {type: pkgbase}\n")
	fe.Write("shared/networks/fnet.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: fnet\nspec:\n  ipv4: {subnet: 10.77.0.0/24, gateway: 10.77.0.1, range: 10.77.0.10-10.77.0.50}\n")
	fe.Write("projects/web.yaml", "apiVersion: jailops/v1\nkind: Project\nmetadata:\n  name: web\nspec:\n  hosts: [h1]\n  networks: [{network: fnet}]\n  storage: [{storage: default}]\n")
	// the policy names a jail that does not exist yet: sync makes policies before jails, and the API only warns about it
	fe.Write("projects/web/firewall-policies/web.yaml", "apiVersion: jailops/v1\nkind: FirewallPolicy\nmetadata:\n  name: web\nspec:\n  default_in: deny\n  default_out: allow\n  rules:\n    - {name: from-wiki, action: allow, direction: in, proto: tcp, ports: [\"443\"], from: [\"jail:wiki\"]}\n")
	fe.Write("recipes/web/recipe.yaml", syncNginxRecipe)
	fe.Write("recipes/web/nginx.conf", "server_name ${server_name};\n")
	fe.Write("projects/web/jails/blog.yaml", syncJailYAML("blog", "recipes:\n  - {path: recipes/web, vars: {server_name: blog.example.test}}\n"))
	root := func(j, p string) string { return filepath.Join(e.JailRootDir("web", j), p) }
	conf := func(j string) string { return root(j, "usr/local/etc/nginx/nginx.conf") }
	label := func(j string) string {
		jl, err := e.Global().Project("web").Jails().Get(t.Context(), j)
		if err != nil {
			t.Fatal(err)
		}
		return jl.Metadata.Labels["sync.jailops.net/recipe.web"]
	}

	// offline check, then the jail is made and the recipe applied in one run
	if c, _ := runSyncCLI(t, fe, "validate"); c != 0 {
		t.Fatal("validate")
	}
	if c, out := runSyncCLI(t, fe, "sync", "--apply"); c != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("apply exit %d", c)
	}
	if b, _ := os.ReadFile(conf("blog")); string(b) != "server_name blog.example.test;\n" {
		t.Fatalf("file: %q", b)
	}
	if label("blog") == "" {
		t.Fatal("no record on the jail")
	}

	// the second run reads the jail and applies nothing: no command of the recipe runs (only service status)
	n := len(aj.cmds)
	if c, out := runSyncCLI(t, fe, "sync", "--apply"); c != 0 || strings.Contains(out, "(recipe)") {
		t.Fatalf("second run applied something (exit %d)", c)
	}
	for _, c := range aj.cmds[n:] {
		if !strings.Contains(c, "status") {
			t.Fatalf("a command ran on a converged jail: %s", c)
		}
	}
	if c, _ := runSyncCLI(t, fe, "drift"); c != 0 {
		t.Fatalf("drift exit %d on a converged fleet", c)
	}

	// a file beside the recipe changed: only that step changes
	fe.Write("recipes/web/nginx.conf", "server_name ${server_name};\nworker_processes 2;\n")
	_, out := runSyncCLI(t, fe, "plan")
	if !strings.Contains(out, "the recipe changed") || !strings.Contains(out, "would change") {
		t.Fatal("plan after a file change")
	}
	if _, out = runSyncCLI(t, fe, "sync", "--apply"); !strings.Contains(out, "1 changed") {
		t.Fatalf("only the file changes: %s", out)
	}

	// a directory with the wrong mode is drift the engine reads (dir is part of the classification)
	if err := os.Chmod(root("blog", "var/db/site"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c, out := runSyncCLI(t, fe, "drift"); c != 1 || !strings.Contains(out, "step(s) differ in the jail") {
		t.Fatalf("a wrong mode is drift: exit %d", c)
	}
	runSyncCLI(t, fe, "sync", "--apply")
	if fi, err := os.Stat(root("blog", "var/db/site")); err != nil || fi.Mode().Perm() != 0o750 {
		t.Fatalf("the mode was not repaired: %v %v", fi, err)
	}
	// an edit by hand is found by a look inside the jail (drift always looks; sync --apply looks once per drift_interval,
	// an hour by default) and put back
	os.WriteFile(conf("blog"), []byte("hand edited\n"), 0o644)
	if c, _ := runSyncCLI(t, fe, "drift"); c != 1 {
		t.Fatalf("an edit by hand is drift: exit %d", c)
	}
	runSyncCLI(t, fe, "sync", "--apply")
	if b, _ := os.ReadFile(conf("blog")); !strings.Contains(string(b), "worker_processes 2") {
		t.Fatalf("not restored: %q", b)
	}

	// a failing step: its jail stops, the jail that needs it waits for the next run, an unrelated jail goes on
	fe.Write("recipes/bad/recipe.yaml", "steps:\n  - run: [boom]\n")
	fe.Write("projects/web/jails/api.yaml", syncJailYAML("api", "recipes:\n  - {path: recipes/bad}\n"))
	fe.Write("projects/web/jails/wiki.yaml", syncJailYAML("wiki", "needs: [api]\nrecipes:\n  - {path: recipes/web, vars: {server_name: wiki.example.test}}\n"))
	aj.failOn = "boom"
	c, out := runSyncCLI(t, fe, "sync", "--apply")
	if c == 0 || !strings.Contains(out, "skipped") || !strings.Contains(out, "needs api") {
		t.Fatalf("a failed recipe must fail the run and skip its dependents (exit %d)", c)
	}
	if _, err := os.Stat(conf("wiki")); err == nil {
		t.Fatal("wiki ran although the jail it needs failed")
	}
	if label("blog") == "" {
		t.Fatal("the failure removed the record of an unrelated jail")
	}
	aj.failOn = ""
	if c, _ := runSyncCLI(t, fe, "sync", "--apply"); c != 0 {
		t.Fatal("after the fix")
	}
	if _, err := os.Stat(conf("wiki")); err != nil {
		t.Fatal("wiki did not follow api")
	}
	if c, _ := runSyncCLI(t, fe, "drift"); c != 0 {
		t.Fatalf("drift exit %d at the end", c)
	}

	// a jail that was stopped is never started again: its recipe waits
	op, err := e.Global().Project("web").Jails().Stop(t.Context(), "blog")
	if err == nil {
		_, err = op.Wait(t.Context())
	}
	if err != nil {
		t.Fatal(err)
	}
	fe.Write("recipes/web/nginx.conf", "server_name ${server_name};\nworker_processes 3;\n")
	c, out = runSyncCLI(t, fe, "sync", "--apply")
	if c != 0 || !strings.Contains(out, "waiting") || !strings.Contains(out, "not running") {
		t.Fatalf("stopped jail: exit %d", c)
	}
	if jl, _ := e.Global().Project("web").Jails().Get(t.Context(), "blog"); jl.Status.Power == api.PowerRunning {
		t.Fatal("sync started a stopped jail")
	}
}

// A secret reaches the file in the jail and nowhere else: the engine withholds the output of a failing command of a
// request that carries a secret, and sync prints and stores none of it.
func TestSyncRecipeSecretStaysInTheFile(t *testing.T) {
	const pw = "tr0ub4dor-and-3"
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	aj := &applyJails{running: map[string]bool{}, users: map[string]bool{}, failOut: "psql: password " + pw + " refused"}
	e.Jails.SetExecFn(aj.fn)
	if err := os.MkdirAll(filepath.Join(e.JailRootDir("web", "db"), "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	fe := fleettest.New(t, "proto")
	fe.WriteSecret("h1-token", e.Token(api.PermAdmin))
	fe.WriteSecret("dbpw", pw)
	fe.Write("hosts/h1/host.yaml", "apiVersion: jailops/v1\nkind: Host\nmetadata:\n  name: h1\nspec:\n  url: "+e.HTTP.URL+"\n  token_secret: h1-token\n")
	fe.Write("shared/images/fimg.yaml", "apiVersion: jailops/v1\nkind: Image\nmetadata:\n  name: fimg\nbuild: true\nspec:\n  os: freebsd\n  release: 15.1-RELEASE\n  arch: amd64\n  source: {type: pkgbase}\n")
	fe.Write("shared/networks/fnet.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: fnet\nspec:\n  ipv4: {subnet: 10.77.0.0/24, gateway: 10.77.0.1, range: 10.77.0.10-10.77.0.50}\n")
	fe.Write("projects/web.yaml", "apiVersion: jailops/v1\nkind: Project\nmetadata:\n  name: web\nspec:\n  hosts: [h1]\n  networks: [{network: fnet}]\n  storage: [{storage: default}]\n")
	fe.Write("projects/web/firewall-policies/web.yaml", "apiVersion: jailops/v1\nkind: FirewallPolicy\nmetadata:\n  name: web\nspec:\n  default_in: deny\n  default_out: allow\n  rules: []\n")
	fe.Write("recipes/dbconf/recipe.yaml", "vars:\n  db_password: {secret: true}\nsteps:\n  - file: {path: /usr/local/etc/app/db.conf, content: \"password=${db_password}\\n\", template: true, mode: \"0600\"}\n  - run: [psql, -f, /usr/local/etc/app/db.conf]\n")
	fe.Write("projects/web/jails/db.yaml", syncJailYAML("db", "recipes:\n  - {path: recipes/dbconf, secrets: {db_password: dbpw}}\n"))

	aj.failOn = "psql"
	c, out := runSyncCLI(t, fe, "sync", "--apply")
	if c == 0 || !strings.Contains(out, "withheld") {
		t.Fatalf("the failing command must fail the run and say its output is withheld (exit %d)", c)
	}
	_, out2 := runSyncCLI(t, fe, "sync", "--apply", "--json")
	if strings.Contains(out+out2, pw) {
		t.Fatalf("the secret is in the output:\n%s", out+out2)
	}
	b, err := os.ReadFile(filepath.Join(e.JailRootDir("web", "db"), "usr/local/etc/app/db.conf"))
	if err != nil || string(b) != "password="+pw+"\n" {
		t.Fatalf("the file in the jail: %q %v", b, err)
	}
	jl, _ := e.Global().Project("web").Jails().Get(t.Context(), "db")
	for k, v := range jl.Metadata.Labels {
		if strings.Contains(k+v, pw) {
			t.Fatalf("label %s holds the secret", k)
		}
	}
	for op, err := range e.Global().Operations().List(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if bs, _ := json.Marshal(op); strings.Contains(string(bs), pw) {
			t.Fatalf("operation %s holds the secret", op.ID)
		}
	}
	filepath.WalkDir(fe.State, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), pw) {
				t.Errorf("%s holds the secret", p)
			}
		}
		return nil
	})
}
