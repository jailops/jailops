package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The recipes of examples/ are valid: they load, with the files beside them.
func TestExampleRecipesLoad(t *testing.T) {
	for _, p := range []string{"../../examples/recipes/nginx.yaml", "../../examples/recipes/postgres.yaml", "../../examples/provisioning/recipe", "../../examples/pkgops-packaged-service/recipe"} {
		r, err := (&app{}).loadRecipe(filepath.FromSlash(p), recipeFlags{})
		if err != nil || len(r.Recipe.Steps) == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// A source is read below the directory of the recipe: a link that stays inside is fine, one that leads out (to a file or
// through a directory) is refused and the error names the file.
func TestRecipeSourceStaysInsideTheDirectory(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "real.conf"), []byte("real\n"), 0o644))
	must(os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	must(os.Symlink("real.conf", filepath.Join(dir, "inside.conf")))
	must(os.Symlink("../real.conf", filepath.Join(dir, "sub", "up.conf")))
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "file.conf")))
	must(os.Symlink(outside, filepath.Join(dir, "linkdir")))
	recipe := func(src string) string {
		return "steps:\n  - file:\n      path: /usr/local/etc/a.conf\n      source: " + src + "\n"
	}
	for src, want := range map[string]string{"real.conf": "real\n", "inside.conf": "real\n", "sub/up.conf": "real\n"} {
		must(os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe(src)), 0o644))
		r, err := (&app{}).loadRecipe(dir, recipeFlags{})
		if err != nil || *r.Recipe.Steps[0].File.Content != want {
			t.Errorf("%s: %v %v", src, r, err)
		}
	}
	for _, src := range []string{"file.conf", "linkdir/secret"} {
		must(os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe(src)), 0o644))
		_, err := (&app{}).loadRecipe(dir, recipeFlags{})
		if err == nil || !strings.Contains(err.Error(), src) {
			t.Errorf("%s: %v", src, err)
		}
	}
}
