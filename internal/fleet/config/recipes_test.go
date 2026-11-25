package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recipeJail(t *testing.T, d, name, extra string) {
	t.Helper()
	write(t, d, "projects/w/jails/"+name+".yaml", "apiVersion: jailops/v1\nkind: Jail\nhosts: [a]\nmetadata:\n  name: "+name+"\n"+extra+"spec: {image: i}\n")
}

func recipeFleet(t *testing.T) string {
	d := base(t)
	project(t, d, "w", "[a]", "")
	write(t, d, "recipes/web/recipe.yaml", "vars:\n  site: {default: x.test}\nsteps:\n  - file: {path: /usr/local/etc/site, content: \"${site}\", template: true}\n")
	return d
}

func TestRecipePathsAreInsideTheRepository(t *testing.T) {
	d := recipeFleet(t)
	for _, p := range []string{"../x", "/etc", "recipes/../hosts", "web", "recipes/web/"} {
		recipeJail(t, d, "j", "recipes: [{path: "+p+"}]\n")
		if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "inside recipes/") {
			t.Fatalf("%q: %v", p, err)
		}
	}
	// A link that leaves the recipe directory is refused, one that stays is read.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, d, "recipes/linked/recipe.yaml", "steps:\n  - file: {path: /usr/local/etc/x, source: out.txt}\n")
	if err := os.Symlink(outside, filepath.Join(d, "recipes/linked/out.txt")); err != nil {
		t.Fatal(err)
	}
	recipeJail(t, d, "j", "recipes: [{path: recipes/linked}]\n")
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "out.txt") {
		t.Fatalf("a link out of the recipe directory: %v", err)
	}
}

func TestRecipeVariablesAreCheckedOffline(t *testing.T) {
	d := recipeFleet(t)
	for name, tc := range map[string]struct{ extra, want string }{
		"unknown variable":  {"recipes: [{path: recipes/web, vars: {nope: 1}}]\n", "nope"},
		"unused jail var":   {"vars: {nope: 1}\nrecipes: [{path: recipes/web}]\n", "vars.nope: no recipe of the jail declares"},
		"same name twice":   {"recipes: [{path: recipes/web}, {path: recipes/web}]\n", "two recipes of a jail are named"},
		"a secret is a var": {"recipes: [{path: recipes/web, secrets: {site: a-secret}}]\n", "not a secret variable"},
	} {
		recipeJail(t, d, "j", tc.extra)
		if _, err := Load(d); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	recipeJail(t, d, "j", "vars: {site: shared.test}\nrecipes: [{path: recipes/web}]\n")
	if _, err := Load(d); err != nil {
		t.Fatal(err)
	}
}

func TestNeedsMustExistAndNotCycle(t *testing.T) {
	d := recipeFleet(t)
	recipeJail(t, d, "a1", "needs: [nothere]\nrecipes: [{path: recipes/web}]\n")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Check(); err == nil || !strings.Contains(err.Error(), `needs "nothere"`) {
		t.Fatalf("%v", err)
	}
	recipeJail(t, d, "a1", "needs: [b1]\nrecipes: [{path: recipes/web}]\n")
	recipeJail(t, d, "b1", "needs: [a1]\nrecipes: [{path: recipes/web}]\n")
	f, err = Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Check(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("%v", err)
	}
}

func TestDriftIntervalDefaultsAndParses(t *testing.T) {
	d := recipeFleet(t)
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.DriftInterval(); got != DefaultDriftInterval {
		t.Fatalf("default %v", got)
	}
	write(t, d, "fleet.yaml", "id: f\ndrift_interval: 15m\n")
	if f, err = Load(d); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.DriftInterval(); got.Minutes() != 15 {
		t.Fatalf("got %v", got)
	}
	write(t, d, "fleet.yaml", "id: f\ndrift_interval: soon\n")
	if _, err = Load(d); err == nil || !strings.Contains(err.Error(), "drift_interval") {
		t.Fatalf("%v", err)
	}
}

func profileFile(t *testing.T, d, name, extra string) {
	t.Helper()
	write(t, d, "profiles/"+name+".yaml", "apiVersion: jailops/v1\nkind: Profile\nmetadata:\n  name: "+name+"\n"+extra+"spec: {}\n")
}

func profiledJail(t *testing.T, d, name, extra string) {
	t.Helper()
	write(t, d, "projects/w/jails/"+name+".yaml", "apiVersion: jailops/v1\nkind: Jail\nhosts: [a]\nmetadata:\n  name: "+name+"\n"+extra+"spec: {image: i, profiles: [base]}\n")
}

func jailOf(t *testing.T, f *Fleet, name string) *Object {
	t.Helper()
	for _, o := range f.Objects {
		if o.Name == name && o.Kind == "Jail" {
			return o
		}
	}
	t.Fatalf("no jail %s", name)
	return nil
}

// A profile's recipes run before the jail's own, its needs are added, and the jail's vars win over the profile's.
func TestProfileRecipesAreMergedBeforeTheJailsOwn(t *testing.T) {
	d := recipeFleet(t)
	write(t, d, "recipes/hardening/recipe.yaml", "steps:\n  - sysrc: {sendmail_enable: \"NO\"}\n")
	profileFile(t, d, "base", "recipes: [{path: recipes/hardening}, {path: recipes/web}]\nneeds: [db]\nvars: {site: profile-var.test}\n")
	write(t, d, "recipes/own/recipe.yaml", "steps:\n  - sysrc: {x_enable: \"YES\"}\n")
	profiledJail(t, d, "j", "recipes: [{path: recipes/own}]\nvars: {site: jail.test}\n")
	recipeJail(t, d, "db", "recipes: [{path: recipes/own}]\n")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	j := jailOf(t, f, "j")
	var names []string
	for _, r := range j.Recipes {
		names = append(names, r.Name)
	}
	if strings.Join(names, " ") != "hardening web own" {
		t.Fatalf("order: %v", names)
	}
	if j.JailVars["site"] != "jail.test" || len(j.Needs) != 1 || j.Needs[0] != "db" {
		t.Fatalf("vars %v needs %v", j.JailVars, j.Needs)
	}
	noNeedsError(t, f)
	// the jail's var reaches the rendering, and the hash follows it
	web := j.Recipes[1]
	r1, err := web.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	web.JailVars = map[string]string{"site": "other.test"}
	r2, err := web.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Hash == r2.Hash || *r1.Recipe.Steps[0].File.Content != "jail.test" {
		t.Fatalf("hash %s %s, content %q", r1.Hash, r2.Hash, *r1.Recipe.Steps[0].File.Content)
	}
	// the profile's var applies when the jail gives none
	profiledJail(t, d, "j", "recipes: [{path: recipes/own}]\n")
	f, err = Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if v := jailOf(t, f, "j").JailVars["site"]; v != "profile-var.test" {
		t.Fatalf("site %q", v)
	}
}

// The checks of a jail's recipes hold for those of its profiles.
func TestProfileRecipesAreChecked(t *testing.T) {
	d := recipeFleet(t)
	write(t, d, "recipes/own/recipe.yaml", "steps:\n  - sysrc: {x_enable: \"YES\"}\n")
	for name, tc := range map[string]struct{ profile, jail, want string }{
		"outside the repository": {"recipes: [{path: ../x}]\n", "", "inside recipes/"},
		"unknown variable":       {"recipes: [{path: recipes/web, vars: {nope: 1}}]\n", "", "nope"},
		"a name twice":           {"recipes: [{path: recipes/web}]\n", "recipes: [{path: recipes/web}]\n", "two recipes of a jail are named"},
		"no recipe declares var": {"vars: {nope: 1}\nrecipes: [{path: recipes/web}]\n", "", "vars.nope: no recipe of the jail"},
	} {
		profileFile(t, d, "base", tc.profile)
		profiledJail(t, d, "j", tc.jail)
		if _, err := Load(d); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// a profile that needs the jail it fits is not a cycle: a jail is never its own need
	profileFile(t, d, "base", "recipes: [{path: recipes/web}]\nneeds: [j]\n")
	profiledJail(t, d, "j", "")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	noNeedsError(t, f)
	if n := jailOf(t, f, "j").Needs; len(n) != 0 {
		t.Fatalf("needs %v", n)
	}
}

// noNeedsError: the fixtures here have no image, network or storage, so Check complains of those; it must not of needs.
func noNeedsError(t *testing.T, f *Fleet) {
	t.Helper()
	if err := f.Check(); err != nil && (strings.Contains(err.Error(), ": needs ") || strings.Contains(err.Error(), "cycle")) {
		t.Fatal(err)
	}
}

func TestRequirementsAreInTheHashAndTheOfflineCheck(t *testing.T) {
	d := recipeFleet(t)
	hash := func(extra string) string {
		write(t, d, "recipes/web/recipe.yaml", extra+"steps:\n  - sysrc: {web_enable: \"YES\"}\n")
		recipeJail(t, d, "j", "recipes: [{path: recipes/web}]\n")
		f, err := Load(d)
		if err != nil {
			t.Fatal(err)
		}
		r, err := jailOf(t, f, "j").Recipes[0].Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return r.Hash
	}
	none := hash("")
	one := hash("requirements:\n  - {check: [/usr/bin/true], message: first}\n")
	other := hash("requirements:\n  - {check: [/usr/bin/true], message: second}\n")
	cmd := hash("requirements:\n  - {check: [/usr/bin/false], message: first}\n")
	if none == one || one == other || one == cmd || hash("requirements:\n  - {check: [/usr/bin/true], message: first}\n") != one {
		t.Fatalf("the hash does not follow the requirements: %s %s %s %s", none, one, other, cmd)
	}
	// a requirement that is wrong is found by the check of the repository, with no jail
	write(t, d, "recipes/web/recipe.yaml", "requirements:\n  - {check: [/usr/bin/true]}\nsteps:\n  - sysrc: {web_enable: \"YES\"}\n")
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "requirements[0].message") {
		t.Fatalf("%v", err)
	}
}

func TestRecipesOfALinuxJailAreNotedOnce(t *testing.T) {
	d := recipeFleet(t)
	write(t, d, "projects/w/jails/lin.yaml", "apiVersion: jailops/v1\nkind: Jail\nhosts: [a]\nmetadata:\n  name: lin\nrecipes: [{path: recipes/web}]\nspec: {image: i, os: linux}\n")
	recipeJail(t, d, "bsd", "recipes: [{path: recipes/web}]\n")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Check() // the stand-in jails have no image or network: the notes are what is asked
	var notes []string
	for _, w := range f.Warnings {
		if strings.Contains(w, "Linux jail") {
			notes = append(notes, w)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "lin") || strings.Contains(notes[0], "RISK") {
		t.Fatalf("%v", f.Warnings)
	}
}
