package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// An app is installed as a jail with one command; the report says how to
// reach it, 'installed' lists what came from the catalog, and the refusals of
// the server are reported as they are.
func TestAppInstall(t *testing.T) {
	h := newHarness(t)
	h.seed() // freebsd-15 is 15.0-RELEASE: too old for the definitions
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "freebsd-151"},
		Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}, Aliases: []string{"stable"}}})
	h.srv.SeedGeneration("freebsd-151")
	h.srv.SeedInvalidApp("broken", "title: is required")
	tr := h.transcript()
	tr.ok("app", "installed")
	tr.ok("app", "install", "nginx", "--image", "freebsd-151@stable", "--dry-run")
	tr.ok("app", "install", "nginx", "--image", "freebsd-151@stable")
	tr.ok("app", "install", "postgresql", "--name", "db", "--image", "freebsd-151@stable", "--network", "lan", "--storage", "default", "--memory", "2G", "--disk", "20G", "--cpu", "2")
	tr.ok("app", "install", "valkey", "--image", "freebsd-151@stable", "--no-wait")
	tr.ok("app", "installed")
	tr.ok("app", "installed", "ng*")
	tr.ok("app", "installed", "-o", "name")
	tr.ok("list")
	tr.do("", "app", "install", "nginx", "--image", "freebsd-151@stable")
	tr.do("", "app", "install", "nginx", "--name", "web2")
	tr.do("", "app", "install", "nginx", "--name", "web2", "--image", "freebsd-15@stable")
	tr.do("", "app", "install", "missing")
	tr.do("", "app", "install", "broken")
	tr.do("", "app", "install", "nginx", "--name", "web2", "--image", "freebsd-151@stable", "--network", "other")
	tr.do("", "app", "install")
	tr.golden("appinstall")

	out, _, code := h.run("", "app", "install", "nginx", "--name", "web3", "--image", "freebsd-151@stable", "-o", "json")
	if code != 0 {
		t.Fatal(out)
	}
	var res api.AppInstallResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.App != "nginx" || res.Jail.Metadata.Name != "web3" || res.Snapshot != "app-installed" ||
		res.Jail.Metadata.Labels[api.LabelApp] != "nginx" || res.Jail.Metadata.Labels[api.LabelAppVersion] != "1.30.6,3" || len(res.Ports) != 1 || res.Ports[0].Endpoint == "" {
		t.Fatalf("%v %+v", err, res)
	}
	if len(res.Jail.Spec.Volumes) != 1 || res.Jail.Spec.Volumes[0].Mount != "/usr/local/www" || res.Jail.Spec.Resources.Memory != "256M" {
		t.Fatalf("%+v", res.Jail.Spec)
	}
	out, _, code = h.run("", "app", "installed", "-o", "json")
	if code != 0 {
		t.Fatal(out)
	}
	var rows []struct {
		Metadata api.ObjectMeta `json:"metadata"`
		App      string         `json:"app"`
		Version  string         `json:"version"`
		Ports    []string       `json:"ports"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 4 || rows[0].App != "postgresql" || rows[0].Ports[0] != "5432/tcp" || rows[0].Version != "18.6" {
		t.Fatalf("%v %+v", err, rows)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"app", "install", ""}, "postgresql"},
		{[]string{"app", "install", "nginx", "--network", ""}, "lan"},
		{[]string{"app", "install", "nginx", "--storage", ""}, "default"},
		{[]string{"app", "install", "nginx", "--image", ""}, "freebsd-151"},
	} {
		out, _, code := h.run("", append([]string{"__complete"}, c.args...)...)
		if code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("complete %v: %d %q", c.args, code, out)
		}
	}
}
