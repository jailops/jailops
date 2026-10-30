package cli_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"jailops.net/jailops/internal/cli"
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

// commandAccepted parses a command line the way the real command tree would,
// without running it: the command must exist, its flags must be known and its
// arguments must fit. It returns the path of the command found.
func commandAccepted(line string) (string, error) {
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "jailops" {
		return "", fmt.Errorf("%q does not start with jailops", line)
	}
	root := cli.NewRootCommand(&cli.Env{})
	cmd, rest, err := root.Find(f[1:])
	if err != nil {
		return "", err
	}
	if cmd == root || !cmd.Runnable() {
		return "", fmt.Errorf("%q names no command", line)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		return "", err
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return "", err
	}
	return cmd.CommandPath(), nil
}

// The rollback command the install report prints must be one the CLI accepts.
func TestAppInstallRollbackCommandIsReal(t *testing.T) {
	if _, err := commandAccepted("jailops jail rollback web --scope all --snapshot app-installed"); err == nil {
		t.Fatal("the checker accepted an unknown flag")
	}
	h := newHarness(t)
	h.seed()
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "freebsd-151"},
		Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}, Aliases: []string{"stable"}}})
	h.srv.SeedGeneration("freebsd-151")
	out, errs, code := h.run("", "app", "install", "nginx", "--image", "freebsd-151@stable")
	if code != 0 {
		t.Fatal(out, errs)
	}
	m := regexp.MustCompile(`\((jailops [^)]*) goes back to it\)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no rollback command in the report:\n%s", out)
	}
	path, err := commandAccepted(m[1])
	if err != nil || path != "jailops jail rollback" {
		t.Fatalf("%q: %q %v", m[1], path, err)
	}
	if !strings.Contains(m[1], " --all ") || !strings.HasSuffix(m[1], " --snapshot app-installed") {
		t.Fatalf("the command must restore root and data from the install snapshot: %q", m[1])
	}
}

// Without --image the install takes the only FreeBSD image with the alias
// stable, here the quick start's freebsd-15 (too old for the definition).
func TestAppInstallDefaultImage(t *testing.T) {
	h := newHarness(t)
	h.seed()
	_, errs, code := h.run("", "app", "install", "nginx")
	if code != 2 || !strings.Contains(errs, `image "freebsd-15" is release 15.0-RELEASE`) {
		t.Fatalf("%d %s", code, errs)
	}
}
