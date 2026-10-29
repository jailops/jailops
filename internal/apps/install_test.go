package apps

import (
	"slices"
	"testing"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

func TestInstallCommands(t *testing.T) {
	root := "/jailops/projects/demo/jails/nginx/root"
	if got := PkgInstallArgv(root, "ports", []string{"nginx", "curl"}); !slices.Equal(got, []string{"pkg", "-r", root, "install", "-y", "nginx", "curl"}) {
		t.Errorf("%q", got)
	}
	if got := PkgInstallArgv(root, "jailops", []string{"jailops-tool"}); !slices.Equal(got, []string{"pkg", "-r", root, "install", "-y", "-r", "jailops", "jailops-tool"}) {
		t.Errorf("%q", got)
	}
	if got := PkgVersionArgv(root, "nginx"); !slices.Equal(got, []string{"pkg", "-r", root, "query", "%v", "nginx"}) {
		t.Errorf("%q", got)
	}
	if got := ServiceArgv("nginx", "start"); !slices.Equal(got, []string{"/usr/sbin/service", "nginx", "start"}) {
		t.Errorf("%q", got)
	}
	if RcConfPath(root) != root+"/etc/rc.conf" {
		t.Error(RcConfPath(root))
	}
	ports, jo := PackageGroups([]api.AppPackage{{Name: "a", Repo: "ports"}, {Name: "b", Repo: "jailops"}, {Name: "c", Repo: "ports"}})
	if !slices.Equal(ports, []string{"a", "c"}) || !slices.Equal(jo, []string{"b"}) {
		t.Errorf("%v %v", ports, jo)
	}
	vars := RcVars([]api.AppService{{Name: "php-fpm"}, {Name: "postgresql", RC: map[string]string{"postgresql_initdb_flags": "--encoding=utf-8", "postgresql_data": "/var/db/postgres/data18"}}})
	want := []render.RcVar{{Name: "php_fpm_enable", Value: "YES"}, {Name: "postgresql_enable", Value: "YES"}, {Name: "postgresql_data", Value: "/var/db/postgres/data18"}, {Name: "postgresql_initdb_flags", Value: "--encoding=utf-8"}}
	if !slices.Equal(vars, want) {
		t.Errorf("%v", vars)
	}
}

func TestVolumesAndLimits(t *testing.T) {
	for in, want := range map[string]string{
		"/var/db/postgres": "var-db-postgres", "/usr/local/www": "usr-local-www", "/home/Forge.Data+x": "home-forge-data-x",
		"/var/db/a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p/q/r/s/t/u/v": "var-db-a-b-c-d-e-f-g-h-i-j-k-l-m-n-o-p-q",
		"///": "data",
	} {
		if got := VolumeName(in); got != want || len(got) > 40 {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	vols := Volumes([]api.AppPath{{Path: "/var/db/x"}, {Path: "/var/db.x"}, {Path: "/var/db-x"}})
	if len(vols) != 3 || vols[0].Name != "var-db-x" || vols[1].Name != "var-db-x-2" || vols[2].Name != "var-db-x-3" || vols[2].Mount != "/var/db-x" {
		t.Errorf("%+v", vols)
	}
	if l := Limits(api.AppResources{Memory: "1G", Disk: "10G", CPU: "1.5"}); l.CPU != 2 || l.Memory != "1G" || l.Disk != "10G" || l.PIDs != 0 {
		t.Errorf("%+v", l)
	}
	if l := Limits(api.AppResources{}); l != (api.Resources{}) {
		t.Errorf("%+v", l)
	}
}

func TestReleaseAtLeast(t *testing.T) {
	for _, c := range []struct {
		rel, min string
		ok, err  bool
	}{
		{"15.1-RELEASE", "15.1", true, false}, {"15.1-RELEASE-p3", "15.1", true, false}, {"15.2-RELEASE", "15.1", true, false},
		{"16.0-RELEASE", "15.1", true, false}, {"15.0-RELEASE", "15.1", false, false}, {"14.3-RELEASE", "15.1", false, false},
		{"15.1", "15.1", true, false}, {"15-RELEASE", "15.1", false, true}, {"", "15.1", false, true}, {"15.1-RELEASE", "x", false, true},
	} {
		ok, err := ReleaseAtLeast(c.rel, c.min)
		if ok != c.ok || (err != nil) != c.err {
			t.Errorf("%s >= %s: %v %v", c.rel, c.min, ok, err)
		}
	}
}
