package fleet_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The fleet layer may import the standard library, the SDK and a short list of
// third-party modules, and nothing else from this repository.
func TestImportsAreRestricted(t *testing.T) {
	allowedThirdParty := []string{
		"github.com/spf13/cobra", "gopkg.in/yaml.v3", "filippo.io/age", "golang.org/x/term",
		"github.com/getsops/sops",
	}
	own := []string{"jailops.net/jailops/internal/fleet", "jailops.net/jailops/sdk"}
	roots := []string{"../../cmd/jailops-sync", "."}
	fset := token.NewFileSet()
	n := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			n++
			for _, im := range f.Imports {
				path := strings.Trim(im.Path.Value, `"`)
				if !strings.Contains(strings.Split(path, "/")[0], ".") {
					continue // standard library
				}
				ok := false
				for _, a := range allowedThirdParty {
					ok = ok || path == a || strings.HasPrefix(path, a+"/")
				}
				for _, a := range own {
					ok = ok || path == a || strings.HasPrefix(path, a+"/")
				}
				if !ok {
					t.Errorf("%s imports %s", p, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n < 20 {
		t.Fatalf("only %d files checked; the walk is wrong", n)
	}
}
