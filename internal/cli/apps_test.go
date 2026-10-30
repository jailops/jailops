package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

// The catalog is browsed with app list and app info; an invalid definition
// is listed with its reason and refused by info.
func TestApps(t *testing.T) {
	h := newHarness(t)
	h.srv.SeedInvalidApp("broken", "title: is required")
	tr := h.transcript()
	tr.ok("app", "list")
	tr.ok("app", "list", "-o", "wide")
	tr.ok("app", "list", "--category", "database")
	tr.do("", "app", "list", "--category", "games")
	tr.ok("app", "list", "post*", "val*")
	tr.ok("app", "list", "-c", "name,version", "--sort", "version")
	tr.ok("app", "list", "-o", "name")
	tr.ok("app", "list", "-o", "csv")
	tr.ok("app", "info", "postgresql")
	tr.ok("app", "info", "nginx", "-o", "yaml")
	tr.do("", "app", "info", "broken")
	tr.do("", "app", "info", "missing")
	h.tty = true
	h.env["NO_COLOR"] = "1"
	tr.ok("app", "list")
	h.cols = 40
	tr.ok("app", "list")
	h.cols = 0
	h.tty = false
	tr.golden("apps")

	out, _, code := h.run("", "app", "info", "valkey", "-o", "json")
	if code != 0 {
		t.Fatal(out)
	}
	var det api.AppDetail
	if err := json.Unmarshal([]byte(out), &det); err != nil || det.Definition.Name != "valkey" || !strings.HasPrefix(det.Hash, "sha256:") || det.File != "valkey.yaml" {
		t.Fatalf("%v %+v", err, det)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"app", "info", ""}, "postgresql"},
		{[]string{"app", "list", "--category", ""}, "monitoring"},
	} {
		out, _, code := h.run("", append([]string{"__complete"}, c.args...)...)
		if code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("complete %v: %d %q", c.args, code, out)
		}
	}
}

// A server without the extension answers unsupported before anything is sent.
func TestAppsNeedTheExtension(t *testing.T) {
	h := newHarness(t, fake.WithExtensions("operations", "etag", "events", "jails"))
	_, errs, code := h.run("", "app", "list")
	if code != 6 || !strings.Contains(errs, "unsupported") {
		t.Fatalf("%d %s", code, errs)
	}
}

// app validate checks definition files on this machine with the daemon's
// rules: no server is configured here, so any request would fail.
func TestAppValidate(t *testing.T) {
	h := newHarness(t)
	h.env["JAILOPS_CONFIG"] = filepath.Join(h.dir, "none.yaml")
	ref := filepath.Join("..", "apps", "testdata", "catalog")
	tr := h.transcript()
	tr.ok("app", "validate", ref)
	tr.ok("app", "validate", filepath.Join(ref, "nginx.yaml"), filepath.Join(ref, "nsd.yaml"))
	tr.ok("app", "validate", ref, "-o", "json")

	bad := filepath.Join(h.dir, "apps")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	nginx, err := os.ReadFile(filepath.Join(ref, "nginx.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	nsd, err := os.ReadFile(filepath.Join(ref, "nsd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, bad, "nginx.yaml", string(nginx), 0o644)
	writeFile(t, bad, "web.yaml", string(nginx), 0o644)
	writeFile(t, bad, "nsd.yaml", string(nsd), 0o644) // its icon is not copied
	writeFile(t, bad, "newer.yaml", strings.Replace(string(nginx), "name: nginx\n", "name: newer\nreplicas: 2\n", 1), 0o644)
	tr.do("", "app", "validate", bad, filepath.Join(h.dir, "missing"))
	tr.do("", "app", "validate", filepath.Join(bad, "web.yaml"), "-o", "json")
	tr.do("", "app", "validate", writeFile(t, bad, "notes.txt", "not a definition", 0o644))
	tr.do("", "app", "validate")
	tr.do("", "app", "validate", ref, "-o", "name")
	tr.golden("app_validate")

	out, _, code := h.run("", "app", "validate", bad, "-o", "json")
	var rep struct {
		Definitions int
		Icons       int
		Problems    []struct{ File, Reason string }
	}
	if code != 2 || json.Unmarshal([]byte(out), &rep) != nil || rep.Definitions != 4 || rep.Icons != 1 || len(rep.Problems) != 3 {
		t.Fatalf("%d %+v\n%s", code, rep, out)
	}
}
