package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestImages(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("image", "create", "alpine-3", "--os", "linux", "--release", "3.22", "--source-type", "minirootfs", "--alias", "latest")
	tr.do("", "image", "create", "bad")
	tr.ok("image", "list")
	tr.ok("image", "show", "freebsd-15")
	tr.ok("image", "build", "freebsd-15")
	tr.ok("image", "generations", "freebsd-15")
	tr.ok("image", "build", "freebsd-15", "--dry-run")
	tr.ok("image", "build", "freebsd-15", "--from", "g1")
	tr.do("", "image", "build", "freebsd-15", "--from", "x")
	tr.ok("image", "generations", "freebsd-15", "-o", "name")
	tr.ok("image", "alias", "set", "freebsd-15", "edge")            // newest
	tr.ok("image", "alias", "set", "freebsd-15", "stable", "g1")    // pin
	tr.do("", "image", "alias", "set", "freebsd-15", "stable", "9") // no such generation
	tr.do("", "image", "alias", "set", "freebsd-15", "stable", "x")
	tr.ok("image", "show", "freebsd-15", "-o", "json")
	tr.ok("image", "alias", "delete", "-y", "freebsd-15", "edge")
	tr.do("", "image", "alias", "delete", "-y", "freebsd-15", "edge") // already gone
	tr.ok("image", "list", "-o", "name")
	tr.ok("jail", "create", "web", "--image", "freebsd-15#g1", "--network", "lan", "--storage", "default")
	tr.do("", "image", "delete-generation", "-y", "freebsd-15", "1") // in use
	tr.ok("image", "alias", "set", "freebsd-15", "latest")           // newest (g3)
	tr.ok("image", "delete-generation", "-y", "freebsd-15", "3")     // the alias that pointed at it moves to g2
	tr.ok("image", "show", "freebsd-15")
	tr.do("", "image", "delete", "-y", "freebsd-15") // in use by web
	tr.ok("image", "delete", "-y", "alpine-3")
	// an image with generations and aliases is deleted as a whole once no jail uses it
	tr.ok("jail", "delete", "-y", "web")
	tr.ok("image", "delete", "-y", "freebsd-15")
	tr.ok("image", "list", "-o", "name")
	tr.golden("images")
	if im, ok := collectImages(t, h); ok && len(im) != 0 {
		t.Fatalf("images: %d", len(im))
	}
}

func TestImageUpgrade(t *testing.T) {
	h := newHarness(t)
	h.seed()
	// A second project with a jail on the same image: an image upgrade moves the jails of every project.
	h.srv.SeedProject(api.Project{Metadata: api.ObjectMeta{Name: "shop"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: "lan"}}, Storage: []api.ProjectStorage{{Storage: "default"}}}})
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "api", Project: "shop"}, Spec: api.JailSpec{Image: "freebsd-15@stable", Storage: "default"}})
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15@stable", "--network", "lan", "--storage", "default")
	tr.ok("jail", "create", "pin", "--image", "freebsd-15#g1", "--network", "lan", "--storage", "default")
	before := len(h.mutations())
	tr.ok("image", "upgrade", "freebsd-15", "--dry-run")
	var u api.ImageUpgrade
	_ = json.Unmarshal(h.lastBody("POST", "/api/v1/images/freebsd-15/upgrade"), &u)
	if !u.DryRun || u.Project != "" || len(h.mutations()) != before+1 {
		t.Fatalf("dry-run request: %+v %v", u, h.mutations())
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Status.Generation != "g1" {
		t.Fatalf("dry-run moved the jail: %+v", j.Status)
	}
	tr.ok("image", "upgrade", "freebsd-15")
	if j, _ := h.srv.JailIn("shop", "api"); j.Status.Generation != "g2" {
		t.Fatalf("the other project's jail did not move: %+v", j.Status)
	}
	tr.ok("image", "upgrade", "freebsd-15", "--project", "shop", "-o", "json")
	_ = json.Unmarshal(h.lastBody("POST", "/api/v1/images/freebsd-15/upgrade"), &u)
	if u.Project != "shop" {
		t.Fatalf("request: %+v", u)
	}
	if j, _ := h.srv.JailIn("main", "web"); j.Status.Generation != "g2" {
		t.Fatalf("--project shop moved a jail of main: %+v", j.Status)
	}
	tr.ok("image", "upgrade", "freebsd-15", "--no-jails")
	h.srv.SeedImageCurrent("freebsd-15", true)
	tr.ok("image", "upgrade", "freebsd-15")
	h.srv.SeedImageCurrent("freebsd-15", false)
	h.srv.SeedUpdateFailure("main", "web", true)
	tr.do("", "image", "upgrade", "freebsd-15") // web fails: the jails after it are not attempted, exit 1
	tr.do("", "image", "upgrade", "freebsd-15", "--project", "nope")
	tr.do("", "image", "upgrade", "nope")
	tr.ok("image", "upgrade", "freebsd-15", "--no-wait", "--no-jails")
	tr.ok("image", "generations", "freebsd-15")
	tr.golden("image_upgrade")
}

func collectImages(t *testing.T, h *harness) ([]api.Image, bool) {
	out, _, code := h.run("", "image", "list", "-o", "json")
	if code != 0 {
		return nil, false
	}
	var im []api.Image
	if err := json.Unmarshal([]byte(out), &im); err != nil {
		t.Fatal(err)
	}
	return im, true
}

func TestNetworks(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("network", "create", "dmz", "--subnet", "10.30.0.0/24", "--gateway", "10.30.0.1", "--range", "10.30.0.10-10.30.0.12", "--nat", "--dns", "10.30.0.1", "--uplink", "em1")
	tr.do("", "network", "create", "bad", "--subnet", "nonsense", "--gateway", "10.30.0.1", "--range", "a-b")
	tr.do("", "network", "create", "short")
	tr.ok("network", "list")
	tr.ok("network", "grant", "dmz", "--project", "main")
	tr.do("", "network", "grant", "lan", "--project", "other") // no such project
	tr.ok("jail", "create", "web", "--image", "freebsd-15@stable", "--storage", "default", "--network", "dmz")
	tr.ok("jail", "create", "db", "--image", "freebsd-15@stable", "--storage", "default", "--network", "dmz")
	tr.ok("network", "leases", "dmz")
	tr.ok("network", "leases", "dmz", "-o", "json")
	tr.ok("network", "show", "dmz", "-o", "yaml")
	// The fake picks which user to name from a map; one user keeps the output stable.
	tr.ok("network", "create", "solo", "--subnet", "10.50.0.0/24", "--gateway", "10.50.0.1", "--range", "10.50.0.5-10.50.0.9")
	tr.ok("network", "grant", "solo", "--project", "main")
	tr.ok("jail", "create", "only", "--image", "freebsd-15@stable", "--storage", "default", "--network", "solo")
	tr.do("", "network", "delete", "-y", "solo") // busy
	tr.do("", "network", "delete", "-y", "lan")  // granted to a project
	tr.ok("network", "revoke", "lan", "--project", "main")
	tr.ok("network", "delete", "-y", "lan")
	f := writeFile(t, h.dir, "net.yaml", "kind: Network\nmetadata: {name: viafile}\nspec:\n  ipv4: {subnet: 10.40.0.0/24, gateway: 10.40.0.1, range: 10.40.0.5-10.40.0.9}\n", 0o644)
	tr.ok("network", "create", "-f", f)
	tr.ok("network", "edit", "viafile", "-f", writeFile(t, h.dir, "net2.yaml", "ipv4: {subnet: 10.40.0.0/24, gateway: 10.40.0.1, range: 10.40.0.5-10.40.0.30}\nmtu: 1400\n", 0o644))
	tr.ok("network", "show", "viafile")
	tr.ok("network", "create", "sw", "--ipv4", "10.94.0.1/24", "--uplink", "em0", "--ipv6", "2001:db8:1:2:100::/80", "--ipv6-mode", "proxy")
	tr.ok("network", "show", "sw", "-o", "yaml")
	tr.do("", "network", "create", "badmode", "--ipv4", "10.95.0.1/24", "--ipv6", "2001:db8:1:2:200::/80", "--ipv6-mode", "bogus")
	tr.do("", "network", "create", "nomode", "--ipv4", "10.96.0.1/24", "--ipv6-mode", "proxy")
	tr.golden("networks")
}

func TestFirewall(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("firewall", "address-set", "create", "office", "--address", "192.0.2.0/24", "--address", "198.51.100.7")
	tr.do("", "firewall", "address-set", "create", "empty")
	tr.ok("firewall", "policy", "create", "web", "--default-in", "deny", "--default-out", "allow",
		"--rule", "allow in tcp port 80,443", "--rule", "allow in tcp from @office port 22 log", "--rule", "allow in tcp port 8080 rdr 8080:80")
	tr.do("", "firewall", "policy", "create", "bad", "--rule", "allow")
	tr.do("", "firewall", "policy", "create", "bad2", "--rule", "allow in tcp bogus 1")
	tr.ok("firewall", "policy", "list")
	tr.ok("firewall", "policy", "show", "web")
	tr.ok("firewall", "address-set", "list")
	tr.ok("firewall", "address-set", "show", "office", "-o", "json")

	good := writeFile(t, h.dir, "good.yaml", `kind: FirewallPolicy
metadata: {name: p1}
spec:
  default_in: deny
  rules:
    - {action: allow, direction: in, proto: tcp, ports: ["22", "8000-8100"], from: ["@office", "10.0.0.0/8", "tag:admin"]}
---
kind: AddressSet
metadata: {name: office}
spec: {addresses: ["192.0.2.0/24"]}
`, 0o644)
	badf := writeFile(t, h.dir, "bad.yaml", `kind: FirewallPolicy
metadata: {name: p2}
spec:
  default_in: maybe
  rules:
    - {action: permit, direction: sideways, proto: tcp, ports: ["99999"], from: ["300.1.1.1", "@nowhere"]}
    - {action: allow, direction: in, ports: ["80"]}
    - {action: allow, direction: out, proto: tcp, redirect: {host_port: 80, jail_port: 80}}
  raw: "block all"
---
kind: AddressSet
metadata: {name: a}
spec: {addresses: ["10.0.0.0/8", "nope"], includes: [b]}
---
kind: AddressSet
metadata: {name: b}
spec: {includes: [a]}
`, 0o644)
	tr.ok("firewall", "validate", "-f", good)
	tr.ok("firewall", "validate", "-f", good, "--offline")
	_, _, code := tr.do("", "firewall", "validate", "-f", badf)
	if code != 2 {
		t.Fatalf("validate bad: %d", code)
	}
	tr.do("", "firewall", "validate", "-f", badf, "-o", "json")
	tr.do("", "firewall", "validate")
	tr.do("", "firewall", "validate", "-f", writeFile(t, h.dir, "j.yaml", "kind: Jail\nmetadata: {name: x}\nspec: {}\n", 0o644))
	tr.ok("firewall", "policy", "edit", "web", "-f", writeFile(t, h.dir, "p.yaml", "default_in: allow\nrules: []\n", 0o644))
	tr.ok("firewall", "policy", "delete", "-y", "web")
	tr.ok("firewall", "address-set", "delete", "-y", "office")
	tr.golden("firewall")
}

func TestBackup(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedBackup(api.Backup{
		Metadata: api.ObjectMeta{Name: "20260101020000-ab12cd34", Project: "main"},
		Spec: api.BackupSpec{Jail: "web", JailUUID: "11111111-2222-4333-8444-555555555555", Target: "nightly", Snapshot: "20260101020000-ab12cd34",
			Bundle: api.RestoreBundle{Spec: api.JailSpec{Image: "freebsd-15@stable", Network: []api.NetworkAttach{{Network: "lan", IPv4: "10.20.0.15"}}}}},
		Status: api.BackupStatus{Bytes: 5 << 20, NewBytes: 1 << 20, Verified: true},
	})
	h.srv.SeedBackup(api.Backup{
		Metadata: api.ObjectMeta{Name: "20260102020000-ee00ff11", Project: "main"},
		Spec:     api.BackupSpec{Jail: "db", Target: "usb", Snapshot: "20260102020000-ee00ff11", Bundle: api.RestoreBundle{Spec: api.JailSpec{Image: "freebsd-15@stable"}}},
		Status:   api.BackupStatus{Bytes: 100, Failed: true},
	})
	tr := h.transcript()
	tr.ok("backup", "target", "create", "nightly", "--kind", "node", "--address", "https://b.example:8443", "--fingerprint", "abcd")
	tr.ok("backup", "target", "list")
	tr.ok("backup", "policy", "create", "nightly", "--selector", "env=prod", "--target", "nightly", "--schedule", "0 2 * * *", "--keep-daily", "14", "--keep-weekly", "8")
	tr.do("", "backup", "policy", "create", "x", "--target", "nightly")
	tr.ok("backup", "policy", "list")
	tr.ok("backup", "policy", "show", "nightly")
	tr.ok("backup", "list")
	tr.ok("backup", "list", "--jail", "db", "-o", "name")
	tr.ok("backup", "show", "20260101020000-ab12cd34")
	tr.do("", "backup", "run", "nightly")
	tr.do("", "backup", "verify", "20260101020000-ab12cd34")

	id := "20260101020000-ab12cd34"
	n := len(h.mutations())
	tr.ok("backup", "restore", id, "--dry-run")
	var rr api.RestoreRequest
	_ = json.Unmarshal(h.lastBody("POST", "/api/v1/restores"), &rr)
	if !rr.DryRun {
		t.Fatalf("dry-run restore sent a real restore: %+v", rr)
	}
	if _, ok := h.srv.JailIn("main", "web"); ok {
		t.Fatal("dry-run restored")
	}
	_ = n
	// No answer: refuses.
	_, _, code := tr.do("", "backup", "restore", id)
	if code != 2 {
		t.Fatalf("restore without confirmation: %d", code)
	}
	if _, ok := h.srv.JailIn("main", "web"); ok {
		t.Fatal("restored without confirmation")
	}
	// Declined.
	_, _, code = tr.do("n\n", "backup", "restore", id)
	if code != 1 {
		t.Fatalf("declined: %d", code)
	}
	// Confirmed.
	out, errs, code := tr.do("y\n", "backup", "restore", id)
	if code != 0 || !strings.Contains(out, "jail/web restored") || !strings.Contains(errs, "Restore backup") {
		t.Fatalf("confirmed: %d %s %s", code, out, errs)
	}
	// The preflight now fails: the jail exists.
	_, _, code = tr.do("", "backup", "restore", id, "-y")
	if code != 2 {
		t.Fatalf("preflight failure: %d", code)
	}
	tr.do("", "backup", "restore", id, "--as", "web-b", "--map", "lan=elsewhere", "-y")
	tr.ok("backup", "restore", id, "--as", "web-b", "-y")
	// --replace needs the typed name; -y is not enough.
	_, _, code = tr.do("", "backup", "restore", id, "--replace", "-y")
	if code != 2 {
		t.Fatalf("replace with -y: %d", code)
	}
	_, _, code = tr.do("web\n", "backup", "restore", id, "--replace")
	if code != 0 {
		t.Fatalf("replace typed: %d", code)
	}
	tr.do("", "backup", "restore", "nope", "--dry-run")
	tr.do("", "backup", "delete", "-y", "20260101020000-ab12cd34")
	tr.golden("backup")
}

func TestBackupExtensions(t *testing.T) {
	h := newHarness(t)
	h.seed()
	// The fake server implements backup_run now: an unknown policy is not_found
	// (exit 4), no longer unsupported.
	out, errs, code := h.run("", "backup", "run", "nightly")
	if code != 4 || out != "" || !strings.HasPrefix(errs, "not_found:") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}
