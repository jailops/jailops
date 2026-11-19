package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// rewriteStored edits the stored object of a kind and name in the form an earlier release wrote it.
func rewriteStored(t *testing.T, stateDir, kind, name string, edit func(spec map[string]any)) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(stateDir, "objects", "*", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if json.Unmarshal(b, &doc) != nil || doc["kind"] != kind {
			continue
		}
		if m, _ := doc["metadata"].(map[string]any); m == nil || m["name"] != name {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		if spec == nil {
			t.Fatalf("%s %s has no spec", kind, name)
		}
		edit(spec)
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, out, 0o640); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("no stored %s %s under %s", kind, name, stateDir)
}

func storedText(t *testing.T, stateDir string) string {
	t.Helper()
	var all strings.Builder
	files, _ := filepath.Glob(filepath.Join(stateDir, "objects", "*", "*.json"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		all.Write(b)
	}
	return all.String()
}

// An upgrade from a release that kept port forwards as redirect rules of a policy and as proxy keys of a profile: the
// objects are read, the forwards become the exposure of the jails that used them, the stored objects lose the old forms,
// the jails still reach the Internet through the same host ports, and a second start changes nothing.
func TestUpgradeOfStoredRedirectsAndProxyKeysKeepsTheForwards(t *testing.T) {
	e, s := newEnv(t)
	natLan(t, s)
	admin := as(globalAdmin, dtest.SeedProject)
	seedJail(t, s, "uses-policy")
	seedJail(t, s, "uses-profile")
	seedJail(t, s, "plain")
	if _, err := s.CreatePolicy(admin, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "site"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProfile(admin, api.Profile{Metadata: api.ObjectMeta{Name: "pub"}, Spec: api.ProfileSpec{Config: map[string]string{"resources.pids": "300"}}}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJail(admin, "uses-policy")
	j.Spec.Firewall.Policies = []string{"site"}
	if _, err := s.UpdateJail(admin, "uses-policy", j, j.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJail(admin, "uses-profile")
	j.Spec.Profiles = []string{"pub"}
	if _, err := s.UpdateJail(admin, "uses-profile", j, j.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	stateDir := e.Cfg.StateDir
	e.Stop()

	// the objects as the earlier release stored them
	rewriteStored(t, stateDir, "FirewallPolicy", "site", func(spec map[string]any) {
		spec["rules"] = []any{
			map[string]any{"name": "ssh", "action": "allow", "direction": "in", "proto": "tcp", "ports": []any{"22"}},
			map[string]any{"name": "fwd", "comment": "site", "action": "allow", "direction": "in", "proto": "tcp", "redirect": map[string]any{"host_port": 8080, "jail_port": 80}},
		}
	})
	rewriteStored(t, stateDir, "Profile", "pub", func(spec map[string]any) {
		cfg, _ := spec["config"].(map[string]any)
		cfg["proxy.dns.listen"] = ":53"
		cfg["proxy.dns.jail_port"] = "53"
		cfg["proxy.dns.proto"] = "udp"
	})
	if !strings.Contains(storedText(t, stateDir), `"redirect"`) {
		t.Fatal("the stored policy has no redirect: the test does not start from the old form")
	}

	forwards := func(e2 *dtest.Env, name string) []string {
		t.Helper()
		r, err := e2.D.Service().JailReach(admin, name)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, x := range r.Exposed {
			if x.Via == "forward" {
				got = append(got, x.Proto+" host "+strconv.Itoa(x.HostPort)+" -> "+x.Ports)
			}
		}
		return got
	}
	e2 := dtest.New(t, dtest.Options{Reuse: e})
	if got := strings.Join(forwards(e2, "uses-policy"), ";"); got != "tcp host 8080 -> 80" {
		t.Errorf("uses-policy forwards %q after the upgrade", got)
	}
	if got := strings.Join(forwards(e2, "uses-profile"), ";"); got != "udp host 53 -> 53" {
		t.Errorf("uses-profile forwards %q after the upgrade", got)
	}
	if got := forwards(e2, "plain"); len(got) != 0 {
		t.Errorf("a jail that used neither forwards %v", got)
	}
	for _, name := range []string{"uses-policy", "uses-profile"} {
		j, err := e2.D.Service().GetJail(admin, name)
		if err != nil || len(j.Spec.Expose) != 1 || !strings.Contains(j.Spec.Expose[0].Comment, "migrated from") {
			t.Errorf("%s: exposure %+v err %v", name, j.Spec.Expose, err)
		}
	}
	if !strings.Contains(e2.Log(), "port forward migrated to the exposure of the jail") {
		t.Errorf("the log does not say the forwards were migrated:\n%s", e2.Log())
	}
	if text := storedText(t, stateDir); strings.Contains(text, `"redirect"`) || strings.Contains(text, "proxy.dns") {
		t.Errorf("the stored objects keep the old forms: %s", text)
	}

	// a second start adds nothing and loses nothing
	e2.Stop()
	e3 := dtest.New(t, dtest.Options{Reuse: e2})
	for _, name := range []string{"uses-policy", "uses-profile"} {
		j, err := e3.D.Service().GetJail(admin, name)
		if err != nil || len(j.Spec.Expose) != 1 {
			t.Errorf("%s after a second start: %+v err %v", name, j.Spec.Expose, err)
		}
	}
}
