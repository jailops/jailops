package cli_test

import (
	"encoding/json"
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
