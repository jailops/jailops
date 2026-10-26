package validate_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Every object of every example fleet must be accepted by the daemon's own validators, so an example cannot teach
// a network, jail, project, storage, policy or address set that the daemon would refuse.
func TestExampleFleetsPassTheDaemonValidators(t *testing.T) {
	var dirs []string
	err := filepath.WalkDir("../../examples", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "fleet.yaml" {
			dirs = append(dirs, filepath.Dir(p))
		}
		return err
	})
	if err != nil || len(dirs) < 6 {
		t.Fatalf("examples: %v %v", dirs, err)
	}
	for _, dir := range dirs {
		t.Run(filepath.ToSlash(dir), func(t *testing.T) {
			if _, err := os.Stat(dir); err != nil {
				t.Fatal(err)
			}
			f, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Check(); err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, o := range f.Objects {
				var err error
				switch s := o.Spec.(type) {
				case api.JailSpec:
					err = validate.JailSpec(o.Name, s)
				case api.NetworkSpec:
					err = validate.NetworkSpec(s)
				case api.ProjectSpec:
					err = validate.ProjectSpec(o.Name, s)
				case api.StorageSpec:
					err = validate.StorageSpec(o.Name, s)
				case api.FirewallPolicySpec:
					_, err = validate.FirewallPolicySpec(s)
				case api.AddressSetSpec:
					err = validate.AddressSetSpec(s)
				default:
					continue
				}
				n++
				if err != nil {
					t.Errorf("%s %s (%s): %v", o.Kind, o.Name, o.Origin, err)
				}
			}
			if n == 0 {
				t.Fatal("nothing was validated")
			}
		})
	}
}
