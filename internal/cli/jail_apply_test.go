package cli

import (
	"path/filepath"
	"testing"
)

// The recipes of examples/ are valid: they load, with the files beside them.
func TestExampleRecipesLoad(t *testing.T) {
	for _, p := range []string{"../../examples/recipes/nginx.yaml", "../../examples/recipes/postgres.yaml", "../../examples/provisioning/recipe", "../../examples/pkgops-packaged-service/recipe"} {
		r, err := (&app{}).loadRecipe(filepath.FromSlash(p))
		if err != nil || len(r.Steps) == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
}
