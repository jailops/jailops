package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

const applyRc = "/jailops/projects/alpha/jails/web/root/etc/rc.conf"

// applyJails plays the commands of a recipe inside a jail: services that run or do not, users that exist, a command
// that fails on request. A service that is started twice fails, as the rc scripts do.
type applyJails struct {
	mu      sync.Mutex
	cmds    []string
	running map[string]bool
	users   map[string]bool
	failOn  string
}

func (a *applyJails) fn(_ string, o jailos.ExecOpts) (run.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	argv := o.Argv
	if len(argv) < 3 || argv[0] != "/usr/bin/env" || argv[1] != "PATH="+service.ShellPATH {
		return run.Result{}, fmt.Errorf("a command of a recipe runs with the login PATH: %v", argv)
	}
	argv = argv[2:]
	a.cmds = append(a.cmds, strings.Join(argv, " "))
	fail := func(msg string) (run.Result, error) {
		return run.Result{Stderr: []byte(msg + "\n"), ExitCode: 1}, errors.New("exit status 1")
	}
	switch {
	case a.failOn != "" && strings.Contains(strings.Join(argv, " "), a.failOn):
		return fail("line one\nthe command broke")
	case len(argv) == 3 && argv[0] == "service" && argv[2] == "status":
		if !a.running[argv[1]] {
			return fail(argv[1] + " is not running")
		}
	case len(argv) == 3 && argv[0] == "service" && argv[2] == "stop":
		delete(a.running, argv[1])
	case len(argv) == 5 && argv[0] == "/bin/sh" && argv[3] == apps.ServiceStartName: // service start through the fixed wrapper
		if a.running[argv[4]] {
			return fail(argv[4] + " already running?")
		}
		a.running[argv[4]] = true
	case len(argv) == 3 && argv[0] == "pw" && argv[1] == "usershow":
		if !a.users[argv[2]] {
			return fail("pw: no such user")
		}
	case len(argv) >= 3 && argv[0] == "pw" && argv[1] == "useradd":
		a.users[argv[2]] = true
	}
	return run.Result{}, nil
}

func (a *applyJails) ran(sub string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.cmds {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// applyEnv is a running web jail with a root that has a user the recipes name.
func applyEnv(t *testing.T) (*dtest.Env, *applyJails) {
	t.Helper()
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	aj := &applyJails{running: map[string]bool{}, users: map[string]bool{}}
	e.Jails.SetExecFn(aj.fn)
	ctx := context.Background()
	op, err := e.Admin.Jails().Create(ctx, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	so, _ := e.Admin.Jails().Start(ctx, "web")
	if _, err := so.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	// the account of the test process: a file written for it keeps its owner, as it would for root in a real jail
	root := e.JailRootDir("alpha", "web")
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	acct := fmt.Sprintf("tester:*:%d:%d::0:0:Tester:/:/bin/sh\n", os.Getuid(), os.Getgid())
	grp := fmt.Sprintf("tester:*:%d:\n", os.Getgid())
	if err := os.WriteFile(filepath.Join(root, "etc/passwd"), []byte(acct), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/group"), []byte(grp), 0o644); err != nil {
		t.Fatal(err)
	}
	return e, aj
}

func str(s string) *string { return &s }
func yes(b bool) *bool     { return &b }

func applyOp(t *testing.T, c *sdk.Client, r api.Recipe, dry bool) (api.ApplyResult, error) {
	t.Helper()
	op, err := c.Jails().Apply(context.Background(), "web", api.ApplyRequest{Recipe: r, DryRun: dry})
	if err != nil {
		return api.ApplyResult{}, err
	}
	return op.Wait(context.Background())
}

func statuses(res api.ApplyResult) string {
	var s []string
	for _, st := range res.Steps {
		s = append(s, st.Status)
	}
	return strings.Join(s, ",")
}

var fullRecipe = api.Recipe{Steps: []api.RecipeStep{
	{Package: []string{"nginx", "rsync"}},
	{File: &api.RecipeFile{Path: "/usr/local/etc/nginx/nginx.conf", Content: str("worker_processes 1;\n"), Mode: "0640", Owner: "tester", Group: "tester"}},
	{Sysrc: map[string]string{"nginx_enable": "YES"}},
	{Sysrc: map[string]string{"daily_status_security_enable": "NO"}, In: "periodic.conf"},
	{Service: &api.RecipeService{Name: "nginx", Enabled: yes(true), Running: yes(true)}},
	{Run: []string{"nginx", "-t"}},
	{Run: []string{"pw", "useradd", "deploy"}, Unless: []string{"pw", "usershow", "deploy"}},
	{Run: []string{"touch", "/var/db/marker"}, Creates: "/usr/local/etc/nginx/nginx.conf"},
}}

// Every kind changes what differs, and the second run of the same recipe changes nothing: a run without a guard is
// reported executed and not changed, a guard that fires is a skip. The commands run with the login PATH.
func TestApplyRecipeIsRepeatable(t *testing.T) {
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	res, err := applyOp(t, c, fullRecipe, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(res); got != "changed,changed,changed,changed,changed,executed,changed,skipped" {
		t.Fatalf("first run: %s\n%v", got, res.Lines())
	}
	root := e.JailRootDir("alpha", "web")
	st, err := os.Stat(filepath.Join(root, "usr/local/etc/nginx/nginx.conf"))
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("the file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "usr/local/etc/nginx/nginx.conf")); string(b) != "worker_processes 1;\n" {
		t.Fatalf("content %q", b)
	}
	if rc := e.File(applyRc); !strings.Contains(rc, `nginx_enable="YES"`) {
		t.Fatalf("rc.conf: %q", rc)
	}
	if pc := e.File("/jailops/projects/alpha/jails/web/root/etc/periodic.conf"); pc != "daily_status_security_enable=\"NO\"\n" {
		t.Fatalf("periodic.conf: %q", pc)
	}
	if aj.ran("pkg install") != 0 || !aj.running["nginx"] || !aj.users["deploy"] {
		t.Fatalf("service %v users %v: %v", aj.running, aj.users, aj.cmds)
	}
	installed := strings.Join(e.Installed, ";")
	if !strings.Contains(installed, " nginx") || !strings.Contains(installed, " rsync") {
		t.Fatalf("packages: %v", e.Installed)
	}
	// again: nothing changes and nothing is started a second time (the fake refuses a start of a running service)
	res, err = applyOp(t, c, fullRecipe, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(res); got != "unchanged,unchanged,unchanged,unchanged,unchanged,executed,skipped,skipped" {
		t.Fatalf("second run: %s\n%v", got, res.Lines())
	}
	if !strings.Contains(strings.Join(res.Lines(), "\n"), "without creates or unless") {
		t.Fatalf("the summary does not say that a run had no guard: %v", res.Lines())
	}
	// a changed file is written again, and only that
	r2 := api.Recipe{Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/etc/nginx/nginx.conf", Content: str("worker_processes 2;\n"), Mode: "0640", Owner: "tester", Group: "tester"}}}}
	if res, err = applyOp(t, c, r2, false); err != nil || statuses(res) != "changed" || res.Steps[0].Detail != "content" {
		t.Fatalf("%+v %v", res, err)
	}
	// a service that runs already is not started, one that must stop is stopped, never restarted
	if res, err = applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{{Service: &api.RecipeService{Name: "nginx", Running: yes(true)}}}}, false); err != nil || statuses(res) != "unchanged" {
		t.Fatalf("%+v %v", res, err)
	}
	if res, err = applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{{Service: &api.RecipeService{Name: "nginx", Running: yes(false)}}}}, false); err != nil || statuses(res) != "changed" || aj.running["nginx"] {
		t.Fatalf("%+v %v", res, err)
	}
	if aj.ran("restart") != 0 {
		t.Fatal("a service was restarted")
	}
}

// A dry run reads the state and runs no command of the recipe: it asks the jail only for the status of services.
func TestApplyDryRunChangesAndRunsNothing(t *testing.T) {
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	res, err := applyOp(t, c, fullRecipe, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(res); got != "would change,would change,would change,would change,would change,would execute,would check,would change" || !res.DryRun {
		t.Fatalf("%s\n%v", got, res.Lines())
	}
	for _, cmd := range aj.cmds {
		if !strings.HasSuffix(cmd, " status") {
			t.Errorf("a dry run ran %q in the jail", cmd)
		}
	}
	if len(e.Installed) != 0 || e.HasFile(applyRc) || e.HasFile("/jailops/projects/alpha/jails/web/root/etc/periodic.conf") {
		t.Fatalf("a dry run wrote: %v", e.Installed)
	}
	if _, err := os.Stat(filepath.Join(e.JailRootDir("alpha", "web"), "usr/local/etc/nginx/nginx.conf")); err == nil {
		t.Fatal("a dry run wrote a file")
	}
}

// The first failed step stops the run with its output in a public error, the steps before it stay, and the same recipe
// run again finishes the work.
func TestApplyFailureStopsAndTheRerunFinishes(t *testing.T) {
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	r := api.Recipe{Steps: []api.RecipeStep{
		{File: &api.RecipeFile{Path: "/usr/local/etc/a.conf", Content: str("a\n")}},
		{Run: []string{"/usr/local/bin/build", "--all"}},
		{File: &api.RecipeFile{Path: "/usr/local/etc/b.conf", Content: str("b\n")}},
	}}
	aj.failOn = "build"
	_, err := applyOp(t, c, r, false)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeInternal || !strings.Contains(ae.Message, "step 2 of 3 failed") || !strings.Contains(ae.Message, "line one / the command broke") || strings.Contains(ae.Message, "\n") || !strings.Contains(ae.Hint, "a.conf") {
		t.Fatalf("the error does not carry the report and the output: %v", err)
	}
	root := e.JailRootDir("alpha", "web")
	if _, err := os.Stat(filepath.Join(root, "usr/local/etc/a.conf")); err != nil {
		t.Fatal("the step before the failure did not stay")
	}
	if _, err := os.Stat(filepath.Join(root, "usr/local/etc/b.conf")); err == nil {
		t.Fatal("a step after the failure ran")
	}
	aj.failOn = ""
	res, err := applyOp(t, c, r, false)
	if err != nil || statuses(res) != "unchanged,executed,changed" {
		t.Fatalf("%v %v", res.Lines(), err)
	}
}

// A service that is already running and a step that creates a service's variable: nothing starts twice.
func TestApplyServiceAlreadyRunning(t *testing.T) {
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	aj.running["nginx"] = true
	res, err := applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{{Service: &api.RecipeService{Name: "nginx", Enabled: yes(true), Running: yes(true)}}}}, false)
	if err != nil || statuses(res) != "changed" || res.Steps[0].Detail != "enabled" || aj.ran("jailops-start") != 0 {
		t.Fatalf("%+v %v %v", res, err, aj.cmds)
	}
}

// The base system directories are refused with a pointer to /usr/local/etc, the few files an administrator edits are
// allowed, and rc.conf style files only through sysrc.
func TestApplyRefusesBaseFiles(t *testing.T) {
	e, _ := applyEnv(t)
	c := e.Client(api.PermOperator)
	file := func(p string) api.Recipe {
		return api.Recipe{Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: p, Content: str("x\n")}}}}
	}
	for _, p := range []string{"/etc/passwd", "/etc/rc.conf", "/etc/rc.conf.local", "/etc/hosts", "/etc/resolv.conf", "/etc/ssh/ssh_config", "/usr/bin/env", "/boot/loader.conf", "/lib/libc.so.7"} {
		if _, err := applyOp(t, c, file(p), false); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "/usr/local/etc") {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"/etc/sysctl.conf", "/etc/periodic.conf", "/etc/ssh/sshd_config", "/etc/newsyslog.conf", "/usr/local/etc/x.conf"} {
		if _, err := applyOp(t, c, file(p), false); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{{Sysrc: map[string]string{"a_enable": "YES"}, In: "rc.conf.local"}}}, false); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("rc.conf.local is written by JailOps: %v", err)
	}
}

// An operator may apply a recipe, a viewer may not; a stopped jail and a Linux jail are refused with what to do.
func TestApplyPermissionsAndJailState(t *testing.T) {
	e, _ := applyEnv(t)
	r := api.Recipe{Steps: []api.RecipeStep{{Run: []string{"true"}}}}
	if _, err := applyOp(t, e.Client(api.PermOperator), r, false); err != nil {
		t.Fatalf("operator: %v", err)
	}
	if _, err := applyOp(t, e.Client(api.PermReadonly), r, false); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	ctx := context.Background()
	so, _ := e.Admin.Jails().Stop(ctx, "web")
	if _, err := so.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := applyOp(t, e.Admin, r, false); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("a stopped jail: %v", err)
	}
	e.SeedLinux()
	co, err := e.Admin.Jails().Create(ctx, dtest.AlpineJail("alp"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := co.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := e.Admin.Jails().Apply(ctx, "alp", api.ApplyRequest{Recipe: r})
	if err == nil {
		_, err = op.Wait(ctx)
	}
	if !api.IsCode(err, api.CodeUnsupported) {
		t.Fatalf("a Linux jail: %v", err)
	}
}
