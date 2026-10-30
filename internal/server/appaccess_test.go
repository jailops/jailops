package server_test

import (
	"context"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// appDB is a database definition with an access block, like the catalog's postgresql.
const appDB = `version: 1
name: db
title: DB
summary: A database server.
category: database
image: {os: freebsd, release_min: "15.1"}
packages:
  - {name: db-server}
services:
  - {name: postgresql}
ports:
  - {name: postgres, port: 5432}
  - {name: metrics, port: 9187}
data:
  - {path: /var/db/postgres}
config:
  - {path: /var/db/postgres/data18/pg_hba.conf, restart: postgresql}
health:
  exec: [/usr/local/bin/pg_isready]
  wait_seconds: 2
access:
  type: postgresql
  port: postgres
  service: postgresql
  hba_file: /var/db/postgres/data18/pg_hba.conf
`

const hbaStock = "local all all trust\nhost all all 127.0.0.1/32 trust\n"

// fakePG answers the psql of app access in a jail as a server would: it
// keeps the roles and databases the statements make, listen_addresses, and
// every script it was sent.
type fakePG struct {
	mu      sync.Mutex
	roles   map[string]bool
	dbs     map[string]bool
	listen  string
	scripts []string // "user db: script"
	argvs   []string
	fail    string // a statement prefix that fails
}

func newFakePG() *fakePG {
	return &fakePG{roles: map[string]bool{}, dbs: map[string]bool{}, listen: "localhost"}
}

func (p *fakePG) exec(name string, o jailos.ExecOpts) (run.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.argvs = append(p.argvs, o.User+" "+strings.Join(o.Argv, " "))
	if len(o.Argv) == 0 || o.Argv[0] != apps.PsqlPath {
		return run.Result{}, nil
	}
	in := ""
	if o.Stdin != nil {
		b, _ := io.ReadAll(o.Stdin)
		in = string(b)
	}
	p.scripts = append(p.scripts, o.User+" "+o.Argv[len(o.Argv)-1]+": "+in)
	var out strings.Builder
	q := regexp.MustCompile(`'([a-z0-9_]+)'`)
	for _, l := range strings.Split(strings.TrimSpace(in), "\n") {
		if p.fail != "" && strings.HasPrefix(l, p.fail) {
			return run.Result{Stderr: []byte("ERROR: failed")}, &run.ExitError{Code: 3}
		}
		id := regexp.MustCompile(`"([a-z0-9_]+)"`).FindStringSubmatch(l)
		switch {
		case strings.HasPrefix(l, "SELECT 'role='"):
			out.WriteString("role=" + map[bool]string{true: "1", false: "0"}[p.roles[last(q.FindAllStringSubmatch(l, -1))]] + "\n")
		case strings.HasPrefix(l, "SELECT 'database='"):
			out.WriteString("database=" + map[bool]string{true: "1", false: "0"}[p.dbs[last(q.FindAllStringSubmatch(l, -1))]] + "\n")
		case strings.HasPrefix(l, "SELECT 'listen='"):
			out.WriteString("listen=" + p.listen + "\n")
		case strings.HasPrefix(l, "CREATE ROLE"):
			p.roles[id[1]] = true
		case strings.HasPrefix(l, "CREATE DATABASE"):
			p.dbs[id[1]] = true
		case strings.HasPrefix(l, "DROP ROLE"):
			delete(p.roles, id[1])
		case strings.HasPrefix(l, "ALTER SYSTEM SET listen_addresses"):
			p.listen = regexp.MustCompile(`'([^']*)'`).FindStringSubmatch(l)[1]
		}
	}
	return run.Result{Stdout: []byte(out.String())}, nil
}

func last(m [][]string) string { return m[len(m)-1][1] }

func (p *fakePG) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.scripts
	p.scripts = nil
	return s
}

func installApp(t *testing.T, c *sdk.Client, app, jail string) api.AppInstallResult {
	t.Helper()
	op, err := c.Apps().Install(ctxT(t), app, api.AppInstall{Jail: jail, Image: "fb151@stable", Network: "lan", Storage: "default"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := op.Wait(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var connRE = regexp.MustCompile(`^postgres://([a-z_]+):([A-Za-z0-9]{32})@10\.20\.0\.10:5432/([a-z_]+)\?sslmode=disable$`)

// The whole flow of app access on a database: closed after the installation,
// allow makes the role, the database, the pg_hba.conf line and the firewall
// rule and prints the connection string once; again changes nothing; deny
// takes it back; nothing of the password is kept or shown anywhere else.
func TestAppAccessPostgreSQL(t *testing.T) {
	e, c := appEnv(t, map[string]string{"db": appDB, "tool": appExecCheck})
	pg := newFakePG()
	e.Jails.SetExecFn(pg.exec)
	oper := e.Scoped(api.PermOperator, dtest.SeedProject)
	installApp(t, oper, "db", "db")
	installApp(t, oper, "tool", "web-api") // 10.20.0.11, a source
	root := "/jailops/projects/alpha/jails/db/root"
	hbaPath := root + "/var/db/postgres/data18/pg_hba.conf"
	e.Files.Put(hbaPath, []byte(hbaStock))

	// closed from the installation on: the managed policy denies, nobody may edit it by hand
	pol, err := e.Admin.Firewall().Policies().Get(c, "app-db")
	if err != nil || pol.Metadata.ManagedBy != api.AppAccessManagedBy || pol.Spec.DefaultIn != "deny" || len(pol.Spec.Rules) != 0 {
		t.Fatalf("%v %+v", err, pol)
	}
	_, err = e.Admin.Firewall().Policies().Update(c, "app-db", pol, "")
	wantCode(t, err, api.CodeForbidden)
	wantCode(t, e.Admin.Firewall().Policies().Delete(c, "app-db"), api.CodeForbidden)
	acc, err := oper.Apps().Access(c, "db")
	if err != nil || !acc.Closed || acc.Type != "postgresql" || len(acc.Grants) != 0 || !slices.Equal(acc.Ports, []string{"5432/tcp"}) || acc.Policy != "app-db" {
		t.Fatalf("%v %+v", err, acc)
	}

	// the dry run changes nothing and shows no secret
	dry, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api", DryRun: true})
	if err != nil || !dry.DryRun || dry.ConnectionString != "" || dry.Grant.Database != "web_api" || len(dry.SQL) != 3 ||
		dry.SQL[0] != `CREATE ROLE "web_api" LOGIN PASSWORD '<generated, shown once>';` || !slices.Equal(dry.Grant.HBA, []string{"host web_api web_api 10.20.0.11/32 scram-sha-256"}) {
		t.Fatalf("%v %+v\n%s", err, dry, e.Log())
	}
	if e.File(hbaPath) != hbaStock || len(pg.roles) != 0 {
		t.Fatal("the dry run changed something")
	}
	pg.take()

	res, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api"})
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	m := connRE.FindStringSubmatch(res.ConnectionString)
	if !res.Changed || m == nil || m[1] != "web_api" || m[3] != "web_api" {
		t.Fatalf("%+v", res)
	}
	password := m[2]
	g := res.Grant
	if g.Source != "@web-api" || g.SourceKind != api.AccessSourceJail || !g.RoleCreated || !g.DatabaseCreated || !slices.Equal(g.Ports, []string{"5432/tcp"}) ||
		!slices.Equal(g.Rules, []string{"allow in tcp from 10.20.0.11 to port 5432"}) {
		t.Fatalf("%+v", g)
	}
	// psql ran as postgres with the statements on standard input; the server got a SCRAM verifier, never the password
	scripts := pg.take()
	all := strings.Join(scripts, "\n")
	if strings.Contains(all, password) || !strings.Contains(all, `CREATE ROLE "web_api" LOGIN PASSWORD 'SCRAM-SHA-256$4096:`) ||
		!strings.Contains(all, `CREATE DATABASE "web_api" OWNER "web_api";`) || !strings.Contains(all, "ALTER SYSTEM SET listen_addresses TO 'localhost,10.20.0.10';") {
		t.Fatalf("%q", scripts)
	}
	for _, s := range scripts {
		if !strings.HasPrefix(s, "postgres ") {
			t.Fatalf("psql did not run as postgres: %q", s)
		}
	}
	for _, a := range pg.argvs {
		if strings.Contains(a, password) || strings.Contains(a, "SCRAM") {
			t.Fatalf("a secret in a command line: %q", a)
		}
	}
	restart, _ := apps.ServiceRestartArgv("postgresql")
	if !slices.Contains(pg.argvs, " "+strings.Join(restart, " ")) || !slices.Contains(pg.argvs, " /usr/sbin/chown postgres:postgres /var/db/postgres/data18/pg_hba.conf") ||
		!slices.Contains(pg.argvs, " /bin/chmod 0600 /var/db/postgres/data18/pg_hba.conf") {
		t.Fatalf("%q", pg.argvs)
	}
	hba := e.File(hbaPath)
	if !strings.HasPrefix(hba, hbaStock) || !strings.Contains(hba, apps.HBABegin+"\nhost web_api web_api 10.20.0.11/32 scram-sha-256\n"+apps.HBAEnd+"\n") {
		t.Fatalf("%s", hba)
	}
	// the firewall: the rule is in the managed policy and, after a pass, in the jail's loaded anchor
	pol, _ = e.Admin.Firewall().Policies().Get(c, "app-db")
	if len(pol.Spec.Rules) != 1 || pol.Spec.Rules[0].Name != "access web-api" || !slices.Equal(pol.Spec.Rules[0].From, []string{"10.20.0.11"}) {
		t.Fatalf("%+v", pol.Spec)
	}
	e.D.Reconciler().Pass(c)
	loaded, err := e.PF.Read(c, render.JailAnchorFor("alpha", "db"))
	if err != nil || !strings.Contains(loaded, "# rule 1: access web-api\npass out quick on") || !strings.Contains(loaded, "from 10.20.0.11 to 10.20.0.10 port 5432") ||
		!strings.Contains(loaded, "block drop out quick on") {
		t.Fatalf("%v\n%s", err, loaded)
	}
	// what was allowed reads back, with the rendered lines and no problem; the password is nowhere
	acc, err = oper.Apps().Access(c, "db")
	if err != nil || len(acc.Grants) != 1 || len(acc.Grants[0].Problems) != 0 || len(acc.Grants[0].PF) < 2 || !strings.Contains(acc.Grants[0].PF[0], "port 5432") ||
		strings.Contains(acc.Grants[0].PF[0], "label") || acc.Grants[0].CreatedBy == "" {
		t.Fatalf("%v %+v", err, acc)
	}
	if strings.Contains(e.Log(), password) {
		t.Fatal("the password is in the daemon log")
	}
	_, body := e.Raw("GET", "/api/v1/audit", e.Token(api.PermAdmin), "")
	if strings.Contains(body, password) || !strings.Contains(body, "allow source=@web-api") {
		t.Fatalf("audit: %s", body)
	}

	// again: nothing changes, no new password
	again, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "@alpha/web-api"})
	if err != nil || again.Changed || again.ConnectionString != "" || len(pg.take()) != 1 {
		t.Fatalf("%v %+v", err, again)
	}
	// rotate: a new password through ALTER ROLE
	rot, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api", Rotate: true})
	if err != nil || !rot.Changed || rot.ConnectionString == res.ConnectionString || !connRE.MatchString(rot.ConnectionString) {
		t.Fatalf("%v %+v", err, rot)
	}
	if s := strings.Join(pg.take(), "\n"); !strings.Contains(s, `ALTER ROLE "web_api" LOGIN PASSWORD 'SCRAM`) || strings.Contains(s, "CREATE") {
		t.Fatalf("%s", s)
	}
	// another database for the same source is refused; an address needs --db
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api", Database: "other"})
	wantCode(t, err, api.CodeConflict)
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "192.0.2.0/24"})
	wantCode(t, err, api.CodeInvalid)
	for _, bad := range []string{`x"; DROP ROLE postgres; --`, "postgres", "pg_read_all_data", "Shop"} {
		_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "192.0.2.0/24", Database: bad})
		wantCode(t, err, api.CodeInvalid)
	}
	// a role JailOps did not make is never touched
	pg.mu.Lock()
	pg.roles["legacy"], pg.dbs["legacy"] = true, true
	pg.mu.Unlock()
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "192.0.2.0/24", Database: "legacy"})
	wantCode(t, err, api.CodeConflict)
	cidr, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "192.0.2.9/24", Database: "shop"})
	if err != nil || cidr.Grant.Source != "192.0.2.0/24" || !strings.Contains(cidr.ConnectionString, "/shop?") ||
		!strings.Contains(e.File(hbaPath), "host shop shop 192.0.2.0/24 scram-sha-256") {
		t.Fatalf("%v %+v", err, cidr)
	}

	// readonly may read, not allow; unknown sources and other jails are refused
	ro := e.Scoped(api.PermReadonly, dtest.SeedProject)
	if _, err := ro.Apps().Access(c, "db"); err != nil {
		t.Fatal(err)
	}
	_, err = ro.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api"})
	wantCode(t, err, api.CodeForbidden)
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "@nobody"})
	wantCode(t, err, api.CodeNotFound)
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "@db"})
	wantCode(t, err, api.CodeInvalid)
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "@other/web"})
	wantCode(t, err, api.CodeNotFound)

	// drift: a line removed by hand is reported, by the grant and by doctor, and allow writes it again
	e.Files.Put(hbaPath, []byte(strings.Replace(e.File(hbaPath), "host web_api web_api 10.20.0.11/32 scram-sha-256\n", "", 1)))
	acc, _ = oper.Apps().Access(c, "db")
	if i := slices.IndexFunc(acc.Grants, func(g api.AppAccessGrant) bool { return g.Source == "@web-api" }); i < 0 ||
		!slices.Equal(acc.Grants[i].Problems, []string{"pg_hba line missing: host web_api web_api 10.20.0.11/32 scram-sha-256"}) {
		t.Fatalf("%+v", acc.Grants)
	}
	rep, err := e.Admin.Doctor(c)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(rep.Checks, func(ch api.DoctorCheck) bool { return ch.Name == "apps.access" }); i < 0 || rep.Checks[i].Status != api.DoctorWarn ||
		!strings.Contains(rep.Checks[i].Message, "alpha/db @web-api: pg_hba line missing") {
		t.Fatalf("%+v", rep.Checks)
	}
	if fixed, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api"}); err != nil || !fixed.Changed || fixed.ConnectionString != "" {
		t.Fatalf("%v %+v", err, fixed)
	}

	// deny: the rule and the line go, the role loses its login and keeps its database
	pg.take()
	den, err := oper.Apps().Deny(c, "db", api.AppDeny{Source: "@web-api"})
	if err != nil || !den.Changed || den.Removed.Source != "@web-api" {
		t.Fatalf("%v %+v", err, den)
	}
	if s := strings.Join(pg.take(), "\n"); !strings.Contains(s, `ALTER ROLE "web_api" NOLOGIN PASSWORD NULL;`) || strings.Contains(s, "DROP") {
		t.Fatalf("%s", s)
	}
	if strings.Contains(e.File(hbaPath), "web_api") || !strings.Contains(e.File(hbaPath), "host shop shop") {
		t.Fatalf("%s", e.File(hbaPath))
	}
	pol, _ = e.Admin.Firewall().Policies().Get(c, "app-db")
	if slices.ContainsFunc(pol.Spec.Rules, func(r api.FirewallRule) bool { return r.Name == "access web-api" }) || pol.Spec.DefaultIn != "deny" {
		t.Fatalf("%+v", pol.Spec)
	}
	if again, err := oper.Apps().Deny(c, "db", api.AppDeny{Source: "@web-api"}); err != nil || again.Changed {
		t.Fatalf("%v %+v", err, again)
	}
	// a later allow gives the kept role a login again, with a new password
	back, err := oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api"})
	if err != nil || !connRE.MatchString(back.ConnectionString) {
		t.Fatalf("%v %+v", err, back)
	}
	if s := strings.Join(pg.take(), "\n"); !strings.Contains(s, `ALTER ROLE "web_api" LOGIN PASSWORD`) || strings.Contains(s, "CREATE ROLE") {
		t.Fatalf("%s", s)
	}
	// --drop-role: what it owned goes to postgres in its database, then the role goes; the database stays
	if _, err := oper.Apps().Deny(c, "db", api.AppDeny{Source: "@web-api", DropRole: true}); err != nil {
		t.Fatal(err)
	}
	s := pg.take()
	if len(s) < 3 || !strings.HasPrefix(s[1], "postgres web_api: REASSIGN OWNED BY \"web_api\" TO postgres;") || !strings.Contains(s[2], `DROP ROLE IF EXISTS "web_api";`) {
		t.Fatalf("%q", s)
	}

	// the app's jail deleted: its policy and grants go with it
	stop(t, e.Admin, "db")
	_, err = oper.Apps().Allow(c, "db", api.AppAllow{Source: "@web-api"})
	wantCode(t, err, api.CodeBusy)
	_, err = oper.Apps().Deny(c, "db", api.AppDeny{Source: "192.0.2.0/24"})
	wantCode(t, err, api.CodeBusy)
	if acc, _ := oper.Apps().Access(c, "db"); !slices.ContainsFunc(acc.Warnings, func(w string) bool { return strings.Contains(w, "stopped") }) {
		t.Fatalf("%+v", acc.Warnings)
	}
	dop, err := e.Admin.Jails().Delete(c, "db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dop.Wait(c); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin.Firewall().Policies().Get(c, "app-db"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("the policy of a deleted app stayed: %v", err)
	}
}

func stop(t *testing.T, c *sdk.Client, name string) {
	t.Helper()
	op, err := c.Jails().Stop(ctxT(t), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

// An app without an access block gets the firewall rule only, for every port
// of its definition or the ones named; address sets, host and jails of another
// project are sources; a deleted source jail loses its rule.
func TestAppAccessFirewallOnly(t *testing.T) {
	e, c := appEnv(t, map[string]string{"web": appWeb, "tool": appExecCheck})
	e.SetDial(httpAnswer(204, nil))
	oper := e.Scoped(api.PermOperator, dtest.SeedProject)
	installApp(t, oper, "web", "web")
	installApp(t, oper, "tool", "cli")
	if _, err := e.Admin.Firewall().AddressSets().Create(c, api.AddressSet{Metadata: api.ObjectMeta{Name: "office"},
		Spec: api.AddressSetSpec{Addresses: []string{"198.51.100.0/24"}}}); err != nil {
		t.Fatal(err)
	}
	res, err := oper.Apps().Allow(c, "web", api.AppAllow{Source: "@office"})
	if err != nil || res.ConnectionString != "" || !slices.Equal(res.Grant.Rules, []string{"allow in tcp from @office to port 8080,9090"}) ||
		len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "only the firewall changed") {
		t.Fatalf("%v %+v", err, res)
	}
	if res, err = oper.Apps().Allow(c, "web", api.AppAllow{Source: "host", Ports: []string{"8080", "53/udp"}}); err != nil ||
		!slices.Equal(res.Grant.Rules, []string{"allow in tcp from 10.20.0.1 to port 8080", "allow in udp from 10.20.0.1 to port 53"}) {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := oper.Apps().Allow(c, "web", api.AppAllow{Source: "@cli", Ports: []string{"8080"}}); err != nil {
		t.Fatal(err)
	}
	// a jail of another project: the operator must be allowed there too; its own policy lets it out to the app
	net := e.MakeNetwork("b")
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	beta := e.Scoped(api.PermAdmin, "beta")
	startJailIn(t, beta, dtest.WebJailOn("api", net))
	_, err = oper.Apps().Allow(c, "web", api.AppAllow{Source: "@beta/api", Ports: []string{"8080"}})
	wantCode(t, err, api.CodeNotFound)
	both := e.Scoped(api.PermOperator, dtest.SeedProject, "beta")
	if res, err = both.Apps().Allow(c, "web", api.AppAllow{Source: "@beta/api", Ports: []string{"8080"}}); err != nil || res.Grant.SourceProject != "beta" {
		t.Fatalf("%v %+v", err, res)
	}
	out, err := beta.Firewall().Policies().Get(c, "app-api")
	if err != nil || out.Spec.DefaultIn != "" || len(out.Spec.Rules) != 1 || out.Spec.Rules[0].Direction != "out" || !slices.Equal(out.Spec.Rules[0].To, []string{"10.20.0.10"}) {
		t.Fatalf("%v %+v", err, out)
	}
	e.D.Reconciler().Pass(c)
	if a, _ := e.PF.Read(c, render.JailAnchorFor("beta", "api")); !strings.Contains(a, "# rule 1: access to alpha.web") || !strings.Contains(a, "pass in quick on") {
		t.Fatalf("%s", a)
	}
	acc, err := oper.Apps().Access(c, "web")
	if err != nil || len(acc.Grants) != 4 || acc.Type != "" {
		t.Fatalf("%v %+v", err, acc)
	}
	// the source jail deleted: its rule is gone at once, the grant says so until it is denied
	stop(t, e.Admin, "cli")
	dop, _ := e.Admin.Jails().Delete(c, "cli")
	if _, err := dop.Wait(c); err != nil {
		t.Fatal(err)
	}
	pol, _ := e.Admin.Firewall().Policies().Get(c, "app-web")
	if slices.ContainsFunc(pol.Spec.Rules, func(r api.FirewallRule) bool { return r.Name == "access cli" }) {
		t.Fatalf("%+v", pol.Spec.Rules)
	}
	acc, _ = oper.Apps().Access(c, "web")
	if i := slices.IndexFunc(acc.Grants, func(g api.AppAccessGrant) bool { return g.Source == "@cli" }); i < 0 || len(acc.Grants[i].Problems) != 1 ||
		!strings.Contains(acc.Grants[i].Problems[0], "no longer exists") {
		t.Fatalf("%+v", acc.Grants)
	}
	if d, err := oper.Apps().Deny(c, "web", api.AppDeny{Source: "@cli"}); err != nil || !d.Changed {
		t.Fatalf("%v %+v", err, d)
	}
	// deny of the cross-project grant clears the source jail's policy, which goes when it is empty
	if _, err := both.Apps().Deny(c, "web", api.AppDeny{Source: "@beta/api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := beta.Firewall().Policies().Get(c, "app-api"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	// a jail that is not an installed app has no access to manage
	_, err = e.Admin.Project("beta").Apps().Allow(c, "api", api.AppAllow{Source: "host"})
	wantCode(t, err, api.CodeInvalid)
}

// Allow and deny of one app run one at a time: concurrent requests leave a
// registry and a policy that agree.
func TestAppAccessConcurrent(t *testing.T) {
	e, c := appEnv(t, map[string]string{"web": appWeb})
	e.SetDial(httpAnswer(204, nil))
	oper := e.Scoped(api.PermOperator, dtest.SeedProject)
	installApp(t, oper, "web", "web")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := "192.0.2." + string(rune('1'+i))
			if _, err := oper.Apps().Allow(context.Background(), "web", api.AppAllow{Source: src, Ports: []string{"8080"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	acc, _ := oper.Apps().Access(c, "web")
	pol, _ := e.Admin.Firewall().Policies().Get(c, "app-web")
	if len(acc.Grants) != 8 || len(pol.Spec.Rules) != 8 {
		t.Fatalf("%d grants, %d rules", len(acc.Grants), len(pol.Spec.Rules))
	}
}

// A rate limit is stored and said not to be enforced: on the jail and by doctor.
func TestRateLimitNotEnforced(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	startJailIn(t, e.Admin, dtest.WebJail("web"))
	op, err := e.Admin.Jails().Config(c, "web", map[string]string{"nic.eth0.ingress": "10M"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
	j, err := e.Admin.Jails().Get(c, "web")
	if err != nil || len(j.Status.Warnings) != 1 || !strings.Contains(j.Status.Warnings[0], "not enforced yet") {
		t.Fatalf("%v %+v", err, j.Status)
	}
	rep, err := e.Admin.Doctor(c)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rep.Checks, func(ch api.DoctorCheck) bool { return ch.Name == "jails.rate_limits" })
	if i < 0 || rep.Checks[i].Status != api.DoctorInfo || !strings.Contains(rep.Checks[i].Message, "alpha/web") {
		t.Fatalf("%+v", rep.Checks)
	}
}
