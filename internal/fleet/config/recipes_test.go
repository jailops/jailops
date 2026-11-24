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
