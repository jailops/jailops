package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func rootFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// applyJails plays the commands of a recipe inside a jail: services that run or do not, users that exist, a command
// that fails on request. A service that is started twice fails, as the rc scripts do.
type applyJails struct {
	mu      sync.Mutex
	cmds    []string
	running map[string]bool
	users   map[string]bool
	failOn  string
	// failOut is what a failing command prints (default: two lines of text)
	failOut string
	// onUseradd is called with the name of every user a pw useradd makes (the jail's passwd file is the test's to change)
	onUseradd func(name string)
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
		if a.failOut != "" {
			return fail(a.failOut)
		}
		return fail("line one\nthe command broke")
	case len(argv) == 3 && argv[0] == "service" && argv[2] == "status":
		if !a.running[argv[1]] {
			return fail(argv[1] + " is not running")
		}
	case len(argv) == 3 && argv[0] == "service" && argv[2] == "stop":
		delete(a.running, argv[1])
	case len(argv) == 5 && argv[0] == "/bin/sh" && argv[3] == "jailops-start": // service start through the fixed wrapper
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
		if a.onUseradd != nil {
			a.onUseradd(argv[2])
		}
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
	if rc := rootFile(t, root, "etc/rc.conf"); !strings.Contains(rc, `nginx_enable="YES"`) {
		t.Fatalf("rc.conf: %q", rc)
	}
	if pc := rootFile(t, root, "etc/periodic.conf"); pc != "daily_status_security_enable=\"NO\"\n" {
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
	for _, f := range []string{"etc/rc.conf", "etc/periodic.conf"} {
		if _, err := os.Stat(filepath.Join(e.JailRootDir("alpha", "web"), f)); err == nil {
			t.Fatalf("a dry run wrote %s", f)
		}
	}
	if len(e.Installed) != 0 {
		t.Fatalf("a dry run installed: %v", e.Installed)
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
	if !errors.As(err, &ae) || ae.Code != api.CodeStepFailed || !strings.Contains(ae.Message, "step 2 of 3 failed") || !strings.Contains(ae.Message, "exit status 1: line one / the command broke") || strings.Contains(ae.Message, "\n") || !strings.Contains(ae.Hint, "a.conf") {
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

// The owner of a file is looked up in the passwd file of the jail when the step runs, not when the run began: a run step
// before it may have made the user.
func TestApplyFileOwnerCreatedByAnEarlierStep(t *testing.T) {
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	root := e.JailRootDir("alpha", "web")
	aj.onUseradd = func(name string) {
		f, err := os.OpenFile(filepath.Join(root, "etc/passwd"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s:*:%d:%d::0:0:Late:/:/bin/sh\n", name, os.Getuid(), os.Getgid())
	}
	r := api.Recipe{Steps: []api.RecipeStep{
		{File: &api.RecipeFile{Path: "/usr/local/etc/a.conf", Content: str("a\n"), Owner: "tester"}},
		{Run: []string{"pw", "useradd", "late"}},
		{File: &api.RecipeFile{Path: "/usr/local/etc/b.conf", Content: str("b\n"), Owner: "late"}},
	}}
	res, err := applyOp(t, c, r, false)
	if err != nil || statuses(res) != "changed,executed,changed" {
		t.Fatalf("%v %v", res.Lines(), err)
	}
	// a user nobody makes is still an error
	r.Steps[2].File.Owner = "nobody-here"
	r.Steps[2].File.Path = "/usr/local/etc/c.conf"
	if _, err := applyOp(t, c, r, false); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("a missing user: %v", err)
	}
}

// A symbolic link at the path of a file step is named as one (and not followed or called a directory).
func TestApplyFileOnASymlink(t *testing.T) {
	e, _ := applyEnv(t)
	c := e.Client(api.PermOperator)
	dir := filepath.Join(e.JailRootDir("alpha", "web"), "usr/local/etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.conf", filepath.Join(dir, "a.conf")); err != nil {
		t.Fatal(err)
	}
	r := api.Recipe{Steps: []api.RecipeStep{{File: &api.RecipeFile{Path: "/usr/local/etc/a.conf", Content: str("a\n")}}}}
	_, err := applyOp(t, c, r, false)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") || strings.Contains(err.Error(), "directory") {
		t.Fatalf("a link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "real.conf")); err == nil {
		t.Fatal("the link was followed")
	}
}

// A sysrc step keeps the mode of an rc.conf that exists.
func TestApplySysrcKeepsTheMode(t *testing.T) {
	e, _ := applyEnv(t)
	c := e.Client(api.PermOperator)
	root := e.JailRootDir("alpha", "web")
	rc := filepath.Join(root, "etc/rc.conf")
	if err := os.WriteFile(rc, []byte("hostname=\"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rc, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{{Sysrc: map[string]string{"nginx_enable": "YES"}}}}, false)
	if err != nil || statuses(res) != "changed" {
		t.Fatalf("%v %v", res.Lines(), err)
	}
	if st, err := os.Stat(rc); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode after the step: %v %v", st, err)
	}
	if got := rootFile(t, root, "etc/rc.conf"); !strings.Contains(got, `hostname="x"`) || !strings.Contains(got, `nginx_enable="YES"`) {
		t.Fatalf("rc.conf: %q", got)
	}
}

func dirStep(path, mode, owner string) api.RecipeStep {
	return api.RecipeStep{Dir: &api.RecipeDir{Path: path, Mode: mode, Owner: owner}}
}

// A dir step makes one directory (and the parents that are missing, never recursively below it), changes an existing
// one only in what the step names, and is a drift for the dry run when the mode or the owner is wrong.
func TestApplyDirStep(t *testing.T) {
	e, _ := applyEnv(t)
	c := e.Client(api.PermOperator)
	root := e.JailRootDir("alpha", "web")
	d := filepath.Join(root, "var/db/site/data")
	mode := func(p string) os.FileMode {
		t.Helper()
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			t.Fatalf("%s: %v %v", p, st, err)
		}
		return st.Mode().Perm()
	}
	r := api.Recipe{Steps: []api.RecipeStep{dirStep("/var/db/site/data", "0750", "tester")}}
	res, err := applyOp(t, c, r, true)
	if err != nil || statuses(res) != "would change" || res.Steps[0].Detail != "new directory" {
		t.Fatalf("dry run: %v %v", res.Lines(), err)
	}
	if _, err := os.Stat(d); err == nil {
		t.Fatal("a dry run made the directory")
	}
	if res, err = applyOp(t, c, r, false); err != nil || statuses(res) != "changed" || mode(d) != 0o750 {
		t.Fatalf("first run: %v %v", res.Lines(), err)
	}
	if mode(filepath.Join(root, "var/db/site")) != 0o755 {
		t.Fatal("a parent that was missing is 0755")
	}
	if res, err = applyOp(t, c, r, false); err != nil || statuses(res) != "unchanged" {
		t.Fatalf("second run: %v %v", res.Lines(), err)
	}
	// drift: the mode is wrong; the dry run says so and a run repairs it
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if res, err = applyOp(t, c, r, true); err != nil || statuses(res) != "would change" || res.Steps[0].Detail != "mode" {
		t.Fatalf("drift: %v %v", res.Lines(), err)
	}
	if res, err = applyOp(t, c, r, false); err != nil || statuses(res) != "changed" || mode(d) != 0o750 {
		t.Fatalf("repair: %v %v", res.Lines(), err)
	}
	// a step that names no mode leaves the mode of a directory that exists
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if res, err = applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{dirStep("/var/db/site/data", "", "")}}, false); err != nil || statuses(res) != "unchanged" || mode(d) != 0o700 {
		t.Fatalf("no mode named: %v %v", res.Lines(), err)
	}
	// the owner is a drift too: a second account in the jail's passwd file
	passwd := filepath.Join(root, "etc/passwd")
	f, err := os.OpenFile(passwd, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "other:*:%d:%d::0:0:Other:/:/bin/sh\n", os.Getuid()+1, os.Getgid())
	f.Close()
	if res, err = applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{dirStep("/var/db/site/data", "", "other")}}, true); err != nil || statuses(res) != "would change" || res.Steps[0].Detail != "owner" {
		t.Fatalf("owner drift: %v %v", res.Lines(), err)
	}
	// a file, or a link, at the path is refused with a message that says so
	if err := os.WriteFile(filepath.Join(root, "var/db/site/afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data", filepath.Join(root, "var/db/site/alink")); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"/var/db/site/afile": "not a directory", "/var/db/site/alink": "symbolic link"} {
		if _, err := applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{dirStep(p, "0755", "")}}, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", p, err)
		}
	}
	// the base system is refused, as for a file
	if _, err := applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{dirStep("/etc/foo", "0755", "")}}, false); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("/etc/foo: %v", err)
	}
	if _, err := applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{dirStep("/var/db/x", "0555", "")}}, false); !api.IsCode(err, api.CodeInvalid) {
		t.Errorf("a mode without write for the owner: %v", err)
	}
}

// The reload that failed once: the first run writes the file and fails at the unguarded run; the next run finds the file
// unchanged and still runs the reload, so the work is finished.
func TestApplyUnguardedRunFailsOnceAndTheNextRunFinishesIt(t *testing.T) {
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	r := api.Recipe{Steps: []api.RecipeStep{
		{File: &api.RecipeFile{Path: "/usr/local/etc/nginx/nginx.conf", Content: str("worker_processes 2;\n")}},
		{Run: []string{"service", "nginx", "reload"}},
	}}
	aj.failOn = "reload"
	if _, err := applyOp(t, c, r, false); !api.IsCode(err, api.CodeStepFailed) {
		t.Fatalf("the first run: %v", err)
	}
	if got := rootFile(t, e.JailRootDir("alpha", "web"), "usr/local/etc/nginx/nginx.conf"); got != "worker_processes 2;\n" {
		t.Fatalf("the file: %q", got)
	}
	aj.failOn = ""
	res, err := applyOp(t, c, r, false)
	if err != nil || statuses(res) != "unchanged,executed" {
		t.Fatalf("the second run: %v %v", res.Lines(), err)
	}
	if aj.ran("nginx reload") != 2 {
		t.Fatalf("the reload ran %d times: %v", aj.ran("nginx reload"), aj.cmds)
	}
}

// The recipe is checked without a jail: an operator may, a viewer may not, and the answer to an invalid recipe names the
// step and the field and nothing of the content.
func TestValidateRecipeNeedsNoJail(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := context.Background()
	ok := api.RecipeValidateRequest{Recipe: api.Recipe{Steps: []api.RecipeStep{{Package: []string{"nginx"}}, dirStep("/var/db/x", "0750", "")}}}
	res, err := e.Client(api.PermOperator).ValidateRecipe(ctx, ok)
	if err != nil || !res.Valid || res.Steps != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := e.Client(api.PermReadonly).ValidateRecipe(ctx, ok); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a viewer: %v", err)
	}
	const secret = "tr0ub4dor-and-3"
	bad := api.RecipeValidateRequest{Sensitive: true, Recipe: api.Recipe{Steps: []api.RecipeStep{
		{File: &api.RecipeFile{Path: "/etc/rc.d/x", Content: str("password=" + secret + "\n")}},
	}}}
	_, err = e.Client(api.PermOperator).ValidateRecipe(ctx, bad)
	if !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "steps[0].file.path") || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/etc/rc.d") {
		t.Fatalf("an invalid recipe: %v", err)
	}
	bad.Recipe.Steps = []api.RecipeStep{{Package: []string{secret + " $(x)"}}}
	if _, err = e.Client(api.PermOperator).ValidateRecipe(ctx, bad); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("a value in the message: %v", err)
	}
}

// A request that carries a secret: it is in the file in the jail and nowhere else. The result, the errors, the operation
// records, the audit, the log and every file of the host are searched for it, and no temporary file is left in the jail.
func TestApplySensitiveRequestLeaksNothing(t *testing.T) {
	const secret = "S3cr3t-Value-0xC0FFEE"
	e, aj := applyEnv(t)
	c := e.Client(api.PermOperator)
	ctx := context.Background()
	req := func(dry bool, steps ...api.RecipeStep) api.ApplyRequest {
		return api.ApplyRequest{Sensitive: true, DryRun: dry, Recipe: api.Recipe{Steps: steps}}
	}
	conf := api.RecipeStep{File: &api.RecipeFile{Path: "/usr/local/etc/app/app.conf", Content: str("password=" + secret + "\n"), Mode: "0600", Owner: "tester"}}
	read := api.RecipeStep{Run: []string{"/usr/local/bin/appctl", "--check"}}
	var seen []string // every text the daemon handed back
	collect := func(v any) {
		b, _ := json.Marshal(v)
		seen = append(seen, string(b))
	}
	do := func(r api.ApplyRequest) (api.ApplyResult, error) {
		op, err := c.Jails().Apply(ctx, "web", r)
		if err != nil {
			return api.ApplyResult{}, err
		}
		res, err := op.Wait(ctx)
		collect(res)
		if err != nil {
			collect(err.Error())
			var ae *api.Error
			if errors.As(err, &ae) {
				collect(ae)
			}
		}
		return res, err
	}
	if res, err := do(req(true, conf, read)); err != nil || !res.DryRun {
		t.Fatalf("dry run: %v %v", res.Lines(), err)
	}
	// the command that reads the file fails and prints what it read
	aj.failOn, aj.failOut = "appctl", "ERROR: bad configuration password="+secret
	_, err := do(req(false, conf, read))
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeStepFailed || !strings.Contains(ae.Message, "step 2 of 2 failed (run /usr/local/bin/appctl --check): exit status 1") {
		t.Fatalf("the failure shows the step and the exit status: %v", err)
	}
	if strings.Contains(ae.Message, "ERROR") || strings.Contains(ae.Message, "bad configuration") {
		t.Fatalf("the output of the command is in the error: %v", err)
	}
	// a run that succeeds, and a second run
	aj.failOn, aj.failOut = "", ""
	if res, err := do(req(false, conf, read)); err != nil || statuses(res) != "unchanged,executed" {
		t.Fatalf("second: %v %v", res.Lines(), err)
	}
	ops := e.Admin.Operations().List(ctx)
	for o, err := range ops {
		if err != nil {
			t.Fatal(err)
		}
		collect(o)
		if full, err := e.Admin.Operations().Get(ctx, o.ID); err == nil {
			collect(full)
		}
	}
	entries, err := e.Admin.Audit(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	collect(entries)
	seen = append(seen, e.Log())
	for i, text := range seen {
		if strings.Contains(text, secret) {
			t.Errorf("the secret is in what the daemon returned or logged (item %d): %.300s", i, text)
		}
	}
	// the host: no file outside the jail root has it; in the root only the file it was meant for, and no temporary file
	jailRoot := e.JailRootDir("alpha", "web")
	var inRoot []string
	err = filepath.WalkDir(e.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), secret) {
			return nil
		}
		if rel, rerr := filepath.Rel(jailRoot, p); rerr == nil && !strings.HasPrefix(rel, "..") {
			inRoot = append(inRoot, rel)
			return nil
		}
		t.Errorf("the secret is in a file of the host: %s", p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inRoot) != 1 || inRoot[0] != "usr/local/etc/app/app.conf" {
		t.Errorf("in the jail root: %v", inRoot)
	}
	if st, err := os.Stat(filepath.Join(jailRoot, "usr/local/etc/app/app.conf")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file in the jail: %v %v", st, err)
	}
	if left, _ := filepath.Glob(filepath.Join(jailRoot, "usr/local/etc/app/.jailops-cp-*")); len(left) != 0 {
		t.Errorf("temporary files: %v", left)
	}
	// a request without the flag keeps the output in the error: the flag is what changes it
	aj.failOn, aj.failOut = "appctl", "ERROR: visible"
	_, err = applyOp(t, c, api.Recipe{Steps: []api.RecipeStep{read}}, false)
	if err == nil || !strings.Contains(err.Error(), "exit status 1: ERROR: visible") {
		t.Errorf("without the flag: %v", err)
	}
}
