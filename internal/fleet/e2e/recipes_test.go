package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/fleet/fleettest"
	"jailops.net/jailops/sdk/api"
)

// The recipes of a jail, through the real command line against the fake hosts. The daemon's own engine is tested in
// internal/daemon (syncrecipes_test.go); here the fake remembers which steps it applied, so a second apply is unchanged.

const webRecipe = `vars:
  site: {default: example.test}
steps:
  - file: {path: /usr/local/etc/nginx/nginx.conf, source: nginx.conf, template: true}
  - run: [service, nginx, reload]
`

// recipeEnv is one host with the standard objects. interval is the drift_interval of fleet.yaml ("" keeps the default).
func recipeEnv(t *testing.T, interval string) *fleettest.Env {
	t.Helper()
	e := fleettest.New(t, "testfleet", "web1")
	e.StandardObjects()
	if interval != "" {
		e.Write("fleet.yaml", "id: testfleet\ndrift_interval: "+interval+"\n")
	}
	e.Write("recipes/web/recipe.yaml", webRecipe)
	e.Write("recipes/web/nginx.conf", "server_name ${site};\n")
	return e
}

func recipeJail(e *fleettest.Env, name, extra string) {
	e.Jail("web1", name, "tier: web", "start: true\n"+extra)
}

func labelsOf(t *testing.T, e *fleettest.Env, jail string) map[string]string {
	t.Helper()
	return getJail(t, e.Hosts["web1"], jail).Metadata.Labels
}

const recipeLabel = "sync.jailops.net/recipe."

func realApplies(e *fleettest.Env, jail string) int {
	n := 0
	for _, c := range e.Hosts["web1"].Server.ApplyCalls() {
		if c.Jail == jail && !c.Req.DryRun {
			n++
		}
	}
	return n
}

func TestRecipeReloadFailsOnce(t *testing.T) {
	e := recipeEnv(t, "1ns") // every run looks inside the jail again
	recipeJail(e, "app", "recipes:\n  - {path: recipes/web, vars: {site: a.example.test}}\n")
	srv := e.Hosts["web1"].Server
	mustOK(t, run(t, e, "validate"))

	// A new hash: the file is written, the reload fails. Nothing is recorded, so the next run goes on although the file
	// is by then as it should be.
	srv.SeedApplyFailure(fleettest.Project, "app", 1, "exit status 1: nginx is not running")
	r := mustFail(t, run(t, e, "sync", "--apply"), "exit status 1")
	contains(t, r.out, "failed")
	if l := labelsOf(t, e, "app"); l[recipeLabel+"web"] != "" {
		t.Fatalf("a failed recipe left a record: %v", l)
	}
	r = mustOK(t, run(t, e, "sync", "--apply"))
	contains(t, r.out, "unchanged")
	contains(t, r.out, "executed")
	hash := labelsOf(t, e, "app")[recipeLabel+"web"]
	if hash == "" {
		t.Fatal("no record after the recipe finished")
	}

	// Converged: nothing is applied.
	before := realApplies(e, "app")
	mustOK(t, run(t, e, "sync", "--apply"))
	if realApplies(e, "app") != before {
		t.Fatal("a converged recipe was applied again")
	}

	// An unchanged hash with drift: someone changed the file, sync repairs it, the reload fails. The record is removed before
	// the apply, so the failed run leaves none and the next run applies again with the file unchanged.
	srv.SeedApplyDrift(fleettest.Project, "app")
	srv.SeedApplyFailure(fleettest.Project, "app", 1, "exit status 1: nginx is not running")
	r = mustFail(t, run(t, e, "sync", "--apply"), "exit status 1")
	contains(t, r.out, "step(s) differ in the jail")
	if l := labelsOf(t, e, "app"); l[recipeLabel+"web"] != "" {
		t.Fatalf("the record was not removed before the apply: %v", l)
	}
	mustOK(t, run(t, e, "sync", "--apply"))
	if got := labelsOf(t, e, "app")[recipeLabel+"web"]; got != hash {
		t.Fatalf("record %q after the repair, want the unchanged hash %q", got, hash)
	}
	// The other labels of the jail are still there.
	if l := labelsOf(t, e, "app"); l["tier"] != "web" || l["sync.jailops.net/hash"] == "" {
		t.Fatalf("labels lost: %v", l)
	}
}

func TestRecipeNeedsNotDueAgainstFailedCheck(t *testing.T) {
	e := recipeEnv(t, "")
	e.Write("recipes/app/recipe.yaml", "steps:\n  - file: {path: /usr/local/etc/app.conf, source: app.conf}\n")
	e.Write("recipes/app/app.conf", "one\n")
	recipeJail(e, "db", "recipes:\n  - {path: recipes/web}\n")
	recipeJail(e, "app", "needs: [db]\nrecipes:\n  - {path: recipes/app}\n")
	srv := e.Hosts["web1"].Server
	mustOK(t, run(t, e, "sync", "--apply"))
	var order []string
	for _, c := range srv.ApplyCalls() {
		if !c.Req.DryRun {
			order = append(order, c.Jail)
		}
	}
	if strings.Join(order, ",") != "db,app" {
		t.Fatalf("the jail that is needed goes first, in one run: %v", order)
	}

	// The check of db is not due (drift_interval is an hour and db was converged just now): its earlier result is trusted
	// and app goes on.
	r := mustOK(t, run(t, e, "sync", "--apply", "--all"))
	contains(t, r.out, "not looked at again until")
	e.Write("recipes/app/app.conf", "two\n")
	r = mustOK(t, run(t, e, "sync", "--apply"))
	if strings.Contains(r.out, "waiting") || realApplies(e, "app") != 2 {
		t.Fatalf("app did not follow although db was trusted:\n%s", r.out)
	}

	// The last look at db FAILED: db is unknown, so app waits, and the plan says why. Exit 0: waiting is not a failure.
	e.Write("fleet.yaml", "id: testfleet\ndrift_interval: 1ns\n")
	e.Write("recipes/app/app.conf", "three\n")
	busy := &api.Error{Code: api.CodeBusy, Message: "the jail is busy"}
	srv.FailRequests("POST", "/api/v1/jails/db/apply", busy, 1)
	r = mustOK(t, run(t, e, "plan"))
	contains(t, r.out, "recipe unknown")
	contains(t, r.out, "not looked at inside the jail")
	contains(t, r.out, "needs db")
	srv.FailRequests("POST", "/api/v1/jails/db/apply", busy, 1)
	r = mustOK(t, run(t, e, "sync", "--apply"))
	contains(t, r.out, "waiting")
	contains(t, r.out, "needs db")
	if realApplies(e, "app") != 2 {
		t.Fatal("app was applied although the last look at db failed")
	}
	// The next run looks again, db is converged, and app follows.
	mustOK(t, run(t, e, "sync", "--apply"))
	if realApplies(e, "app") != 3 {
		t.Fatal("app did not follow once db was settled")
	}
}

func TestRecipeValidateNeedsNoNetwork(t *testing.T) {
	e := recipeEnv(t, "")
	recipeJail(e, "app", "recipes:\n  - {path: recipes/web}\n")
	e.Hosts["web1"].HTTP.Close() // no daemon to ask
	mustOK(t, run(t, e, "validate"))

	// A recipe the daemon would refuse is found offline, and the message names the step, never a value.
	e.Write("recipes/web/recipe.yaml", "steps:\n  - file: {path: /etc/hosts, content: hunter2-the-content}\n")
	r := mustFail(t, run(t, e, "validate"), "steps[0]")
	if strings.Contains(r.all(), "hunter2") {
		t.Fatalf("a value is in the message: %s", r.all())
	}
	// A variable nobody declares, a literal for a secret, and a needs that names no jail.
	e.Write("recipes/web/recipe.yaml", webRecipe)
	e.Write("projects/main/jails/app.yaml", strings.Replace(readFile(t, e, "projects/main/jails/app.yaml"), "start: true\n", "start: true\nvars: {sitee: x}\n", 1))
	mustFail(t, run(t, e, "validate"), "vars.sitee: no recipe of the jail declares")
	e.Write("recipes/sec/recipe.yaml", "vars:\n  pw: {secret: true}\nsteps:\n  - file: {path: /usr/local/etc/x, content: \"${pw}\", template: true, mode: \"0600\"}\n")
	recipeJail(e, "app", "recipes:\n  - {path: recipes/sec, vars: {pw: hunter2-literal}}\n")
	r = mustFail(t, run(t, e, "validate"), "pw is secret: give it under secrets:")
	if strings.Contains(r.all(), "hunter2") {
		t.Fatalf("the literal is in the message: %s", r.all())
	}
	recipeJail(e, "app", "needs: [nothere]\nrecipes:\n  - {path: recipes/web}\n")
	mustFail(t, run(t, e, "validate"), `needs "nothere"`)
}

func TestRecipeDriftExitCodes(t *testing.T) {
	e := recipeEnv(t, "")
	recipeJail(e, "app", "recipes:\n  - {path: recipes/web}\n")
	srv := e.Hosts["web1"].Server
	mustOK(t, run(t, e, "sync", "--apply"))
	mustOK(t, run(t, e, "drift"))

	// A change behind our back: drift is 1, and sync repairs it.
	srv.SeedApplyDrift(fleettest.Project, "app")
	r := run(t, e, "drift")
	if r.code != 1 {
		t.Fatalf("drift exit %d, want 1\n%s", r.code, r.all())
	}
	contains(t, r.out, "step(s) differ in the jail")
	mustOK(t, run(t, e, "sync", "--apply"))
	mustOK(t, run(t, e, "drift"))

	// Unknown (the look failed) exits 0 and is shown, in the text and in the JSON.
	srv.FailRequests("POST", "/api/v1/jails/app/apply", &api.Error{Code: api.CodeBusy, Message: "the jail is busy"}, 1)
	r = mustOK(t, run(t, e, "drift"))
	contains(t, r.out, "recipe unknown")
	srv.FailRequests("POST", "/api/v1/jails/app/apply", &api.Error{Code: api.CodeBusy, Message: "the jail is busy"}, 1)
	if st := recipeJSON(t, mustOK(t, run(t, e, "drift", "--json")).out, "app"); st.Status != "unknown" || st.Action != "match" {
		t.Fatalf("json of an unknown recipe: %+v", st)
	}

	// A stopped jail waits: exit 0, shown, in the JSON too. Its recipe changed, so there would be something to apply.
	op, err := client(t, e.Hosts["web1"]).Project(fleettest.Project).Jails().Stop(context.Background(), "app")
	if err == nil {
		_, err = op.Wait(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
	e.Write("recipes/web/nginx.conf", "server_name ${site};\n# changed\n")
	r = mustOK(t, run(t, e, "drift"))
	contains(t, r.out, "recipe wait")
	contains(t, r.out, "not running")
	if st := recipeJSON(t, mustOK(t, run(t, e, "drift", "--json")).out, "app"); st.Action != "wait" || st.Status != "unknown" {
		t.Fatalf("json of a waiting recipe: %+v", st)
	}
	// sync never starts a jail that exists.
	mustOK(t, run(t, e, "sync", "--apply"))
	if p := getJail(t, e.Hosts["web1"], "app").Status.Power; p == api.PowerRunning {
		t.Fatal("sync started a stopped jail")
	}
}

func recipeJSON(t *testing.T, out, jail string) jsonRecipe {
	t.Helper()
	var rep struct{ Recipes []jsonRecipe }
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, r := range rep.Recipes {
		if r.Jail == jail {
			return r
		}
	}
	t.Fatalf("no recipe of %s in %s", jail, out)
	return jsonRecipe{}
}

type jsonRecipe struct {
	Jail, Recipe, Action, Status, Why string
}

func TestRecipeOrphanLabelsAreCleaned(t *testing.T) {
	e := recipeEnv(t, "")
	e.Write("recipes/second/recipe.yaml", "steps:\n  - run: [echo, hello]\n    creates: /tmp/hello\n")
	recipeJail(e, "app", "recipes:\n  - {path: recipes/web}\n  - {path: recipes/second}\n")
	mustOK(t, run(t, e, "sync", "--apply"))
	l := labelsOf(t, e, "app")
	if l[recipeLabel+"web"] == "" || l[recipeLabel+"second"] == "" {
		t.Fatalf("records: %v", l)
	}
	// The recipe is removed from the file: its record goes, nothing is applied or undone, the other labels stay.
	recipeJail(e, "app", "recipes:\n  - {path: recipes/web}\n")
	before := len(e.Hosts["web1"].Server.ApplyCalls())
	r := mustOK(t, run(t, e, "sync", "--apply"))
	contains(t, r.out, "forgotten")
	l = labelsOf(t, e, "app")
	if _, ok := l[recipeLabel+"second"]; ok || l[recipeLabel+"web"] == "" || l["tier"] != "web" {
		t.Fatalf("labels after the cleanup: %v", l)
	}
	if n := len(e.Hosts["web1"].Server.ApplyCalls()); n != before { // nothing is applied, and nothing is undone
		t.Fatalf("%d apply requests during the cleanup", n-before)
	}
	// All recipes removed.
	recipeJail(e, "app", "")
	mustOK(t, run(t, e, "sync", "--apply"))
	for k := range labelsOf(t, e, "app") {
		if strings.HasPrefix(k, recipeLabel) {
			t.Fatalf("record %s left", k)
		}
	}
	mustOK(t, run(t, e, "drift"))
}

func TestRecipeFailureIsIsolated(t *testing.T) {
	e := recipeEnv(t, "")
	recipeJail(e, "api", "recipes:\n  - {path: recipes/web}\n")
	recipeJail(e, "wiki", "needs: [api]\nrecipes:\n  - {path: recipes/web}\n")
	recipeJail(e, "cache", "recipes:\n  - {path: recipes/web}\n")
	srv := e.Hosts["web1"].Server
	srv.SeedApplyFailure(fleettest.Project, "api", 0, "exit status 1: boom")
	r := mustFail(t, run(t, e, "sync", "--apply"), "boom")
	contains(t, r.out, "needs api")
	contains(t, r.out, "skipped")
	if labelsOf(t, e, "cache")[recipeLabel+"web"] == "" {
		t.Fatal("a failure of api stopped an unrelated jail")
	}
	for _, j := range []string{"api", "wiki"} {
		if labelsOf(t, e, j)[recipeLabel+"web"] != "" {
			t.Fatalf("%s has a record", j)
		}
	}
	if realApplies(e, "wiki") != 0 {
		t.Fatal("wiki ran although the jail it needs failed")
	}
	// The jails themselves were never stopped.
	for _, j := range []string{"api", "wiki", "cache"} {
		if getJail(t, e.Hosts["web1"], j).Status.Power != api.PowerRunning {
			t.Fatalf("%s is not running", j)
		}
	}
	// The next run finishes api and then wiki; cache is not touched again.
	cacheBefore := realApplies(e, "cache")
	mustOK(t, run(t, e, "sync", "--apply"))
	if labelsOf(t, e, "wiki")[recipeLabel+"web"] == "" || realApplies(e, "cache") != cacheBefore {
		t.Fatal("the next run did not finish the failed jails only")
	}
}

func TestRecipeSharedVarsOfAJail(t *testing.T) {
	e := recipeEnv(t, "")
	e.Write("recipes/motd/recipe.yaml", "vars:\n  site: {required: true}\n  who: {default: nobody}\nsteps:\n  - file: {path: /usr/local/etc/motd, content: \"${site} by ${who}\\n\", template: true}\n")
	// site is given once for the jail; one recipe takes it, another overrides it; the one that does not declare a name is not
	// bothered by it.
	e.Write("projects/main/jails/app.yaml", `apiVersion: jailops/v1
kind: Jail
hosts: [web1]
metadata: {name: app}
start: true
vars: {site: shared.example.test, who: ops}
recipes:
  - path: recipes/web
  - path: recipes/motd
spec: {image: freebsd-15, storage: default, network: [{network: internal, ipv4: auto}], firewall: {policies: [web]}}
`)
	mustOK(t, run(t, e, "sync", "--apply"))
	got := map[string]string{}
	for _, c := range e.Hosts["web1"].Server.ApplyCalls() {
		for _, st := range c.Req.Recipe.Steps {
			if st.File != nil && st.File.Content != nil {
				got[st.File.Path] = *st.File.Content
			}
		}
	}
	if got["/usr/local/etc/nginx/nginx.conf"] != "server_name shared.example.test;\n" || got["/usr/local/etc/motd"] != "shared.example.test by ops\n" {
		t.Fatalf("shared values: %q", got)
	}
	// The recipe's own value wins over the jail's.
	e.Write("projects/main/jails/app.yaml", strings.Replace(readFile(t, e, "projects/main/jails/app.yaml"), "path: recipes/motd", "path: recipes/motd\n    vars: {who: me}", 1))
	mustOK(t, run(t, e, "sync", "--apply"))
	calls := e.Hosts["web1"].Server.ApplyCalls()
	last := calls[len(calls)-1].Req.Recipe.Steps[0].File
	if *last.Content != "shared.example.test by me\n" {
		t.Fatalf("own value: %q", *last.Content)
	}
}

func TestRecipeSecretsNeverLeak(t *testing.T) {
	const pw = "s3cr3t-Value-Q9x7"
	e := recipeEnv(t, "1ns")
	e.Write("recipes/dbconf/recipe.yaml", "vars:\n  db_password: {secret: true}\nsteps:\n  - file: {path: /usr/local/etc/app/db.conf, content: \"password=${db_password}\\n\", template: true, mode: \"0600\"}\n  - run: [service, app, reload]\n")
	e.WriteSecret("dbpw", pw)
	recipeJail(e, "app", "recipes:\n  - {path: recipes/dbconf, secrets: {db_password: dbpw}}\n")
	var seen strings.Builder
	collect := func(r result) result { seen.WriteString(r.all()); return r }

	collect(mustOK(t, run(t, e, "validate")))
	collect(mustOK(t, run(t, e, "plan")))
	collect(mustOK(t, run(t, e, "plan", "--json")))
	collect(mustOK(t, run(t, e, "render")))
	collect(mustOK(t, run(t, e, "sync", "--apply")))
	collect(mustOK(t, run(t, e, "sync", "--apply", "--json")))
	collect(mustOK(t, run(t, e, "drift")))
	collect(mustOK(t, run(t, e, "drift", "--json")))
	e.Hosts["web1"].Server.SeedApplyDrift(fleettest.Project, "app")
	e.Hosts["web1"].Server.SeedApplyFailure(fleettest.Project, "app", 1, "exit status 1")
	collect(mustFail(t, run(t, e, "sync", "--apply"), ""))
	e.Hosts["web1"].Server.SeedApplyDrift(fleettest.Project, "app")
	e.Hosts["web1"].Server.SeedApplyFailure(fleettest.Project, "app", 1, "exit status 1")
	collect(mustFail(t, run(t, e, "sync", "--apply", "--json"), ""))

	// It reached the jail, as the secret and with the request marked sensitive, and nowhere else.
	sent := false
	for _, c := range e.Hosts["web1"].Server.ApplyCalls() {
		if c.Req.DryRun {
			continue
		}
		if f := c.Req.Recipe.Steps[0].File; f != nil && f.Content != nil && *f.Content == "password="+pw+"\n" && c.Req.Sensitive {
			sent = true
		}
	}
	if !sent {
		t.Fatal("the secret was not sent to the jail as a sensitive request")
	}
	if strings.Contains(seen.String(), pw) {
		t.Fatalf("the secret is in the output of sync:\n%s", seen.String())
	}
	// Labels, the state directory (logs, the memory of checks).
	for k, v := range labelsOf(t, e, "app") {
		if strings.Contains(k+v, pw) {
			t.Fatalf("label %s holds the secret", k)
		}
	}
	err := filepath.WalkDir(e.State, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), pw) {
			t.Errorf("%s holds the secret", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The hash holds a fingerprint of the stored secret: encrypting the same plaintext again changes it, and the recipe runs
	// again (and only that recipe).
	mustOK(t, run(t, e, "sync", "--apply"))
	hash := labelsOf(t, e, "app")[recipeLabel+"dbconf"]
	before := realApplies(e, "app")
	mustOK(t, run(t, e, "sync", "--apply"))
	if realApplies(e, "app") != before {
		t.Fatal("an unchanged secret ran the recipe again")
	}
	e.WriteSecret("dbpw", pw)
	mustOK(t, run(t, e, "sync", "--apply"))
	if realApplies(e, "app") != before+1 || labelsOf(t, e, "app")[recipeLabel+"dbconf"] == hash {
		t.Fatal("a re-encrypted secret did not run the recipe again")
	}
}
