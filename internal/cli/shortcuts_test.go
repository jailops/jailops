package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestTopLevelShortcuts(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("create", "nginx", "--image", "freebsd-15", "--storage", "default", "--network", "lan")
	tr.ok("list")
	tr.ok("start", "nginx")
	tr.ok("restart", "nginx")
	tr.ok("stop", "nginx")
	tr.do("", "exec", "nginx", "--", "echo", "hi") // not running
	tr.do("", "exec", "nginx", "--", "/bin/ls")
	tr.do("", "shell", "nginx")
	tr.do("", "shell", "nginx", "--shell", "bash")
	_, errs, code := tr.do("", "shell", "nginx", "--shell", "/usr/local/bin/bash")
	// exec is implemented by the server now; a stopped jail answers busy and
	// the CLI never falls back to another shell.
	if code != 5 || !strings.HasPrefix(errs, "busy:") {
		t.Fatalf("a stopped jail must answer busy, never fall back: %d %s", code, errs)
	}
	tr.ok("delete", "--confirm-data-loss", "nginx")
	// The jail group keeps every form as an alias.
	tr.ok("jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	tr.ok("jail", "list", "-o", "name")
	tr.ok("jails", "list", "-o", "name")
	tr.golden("shortcuts")
}

func TestConfigCommands(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("create", "nginx", "--image", "freebsd-15", "--storage", "default", "--network", "lan")
	tr.ok("start", "nginx")
	tr.ok("config", "show", "nginx")
	tr.ok("config", "set", "nginx", "resources.memory", "2G", "resources.cpu", "2")
	tr.ok("config", "get", "nginx", "resources.memory")
	tr.ok("config", "get", "nginx", "resources.memory", "-o", "json")
	tr.do("", "config", "get", "nginx", "resources.pids")
	tr.do("", "config", "get", "nginx", "no.such.key")
	tr.do("", "config", "set", "nginx", "resources.memory")
	tr.do("", "config", "set", "nginx", "resources.cpu", "many")
	tr.do("", "config", "set", "nginx", "no.such.key", "1")
	// An atomic change: one bad pair leaves the rest unapplied.
	tr.do("", "config", "set", "nginx", "resources.cpu", "4", "resources.pids", "bad")
	tr.ok("config", "get", "nginx", "resources.cpu")
	tr.ok("config", "set", "nginx", "resources.pids=100", "boot.autostart=true")
	tr.ok("config", "unset", "nginx", "resources.pids")
	tr.ok("config", "show", "nginx", "--expanded")
	tr.ok("config", "show", "nginx", "-o", "yaml")
	tr.ok("config", "keys", "limits")
	tr.ok("--dry-run", "config", "set", "nginx", "resources.cpu", "8")
	tr.ok("config", "get", "nginx", "resources.cpu")

	// restart_required: reported, and the jail is never restarted unless asked.
	const restartKey = "resources.pids"
	if k, _, ok := api.ConfigKeyFor(restartKey); !ok || !k.RestartRequired {
		t.Fatalf("%s must be a restart_required key in the registry", restartKey)
	}
	jidBefore := mustJID(t, h, "nginx")
	before := len(h.mutations())
	out, _, code := tr.do("", "config", "set", "nginx", restartKey, "700")
	if code != 0 || !strings.Contains(out, "restart required for: "+restartKey) || !strings.Contains(out, "was not restarted") {
		t.Fatalf("restart_required: %d %s", code, out)
	}
	if mustJID(t, h, "nginx") != jidBefore {
		t.Fatal("the jail was restarted without --restart")
	}
	for _, m := range h.mutations()[before:] {
		if strings.HasSuffix(m, "/state") {
			t.Fatalf("a state change was requested without --restart: %s", m)
		}
	}
	out, errs, code := tr.do("", "config", "set", "nginx", restartKey, "800", "--restart")
	if code != 0 || !strings.Contains(out, "restarted to apply: "+restartKey) {
		t.Fatalf("--restart: %d %s %s", code, out, errs)
	}
	if mustJID(t, h, "nginx") == jidBefore {
		t.Fatal("--restart did not restart the jail")
	}
	// A change that needs no restart never says so and never restarts.
	out, _, _ = tr.do("", "config", "set", "nginx", "boot.priority", "3", "--restart")
	if strings.Contains(out, "restart") {
		t.Fatalf("spurious restart text: %s", out)
	}
	if mustJID(t, h, "nginx") == 0 {
		t.Fatal("jail stopped")
	}
	tr.golden("config")
}

func mustJID(t *testing.T, h *harness, name string) int {
	t.Helper()
	j, ok := h.srv.JailIn("main", name)
	if !ok {
		t.Fatal("no jail")
	}
	return j.Status.JID
}

func TestConfigEdit(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "create", "nginx", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	h.run("", "config", "set", "nginx", "resources.cpu", "2", "resources.memory", "1G")
	script := writeFile(t, h.dir, "ed.sh", "#!/bin/sh\nsed -e 's/^resources.cpu: .*/resources.cpu: \"4\"/' -e '/^resources.memory/d' \"$1\" > \"$1.new\" && mv \"$1.new\" \"$1\"\n", 0o755)
	h.env["EDITOR"] = script
	tr := h.transcript()
	tr.ok("config", "edit", "nginx")
	tr.ok("config", "show", "nginx")
	h.env["EDITOR"] = writeFile(t, h.dir, "noop.sh", "#!/bin/sh\nexit 0\n", 0o755)
	tr.ok("config", "edit", "nginx")
	tr.golden("config_edit")
}

func TestKeyCompletion(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "create", "nginx", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	h.run("", "project", "create", "web")
	out, _, _ := h.run("", "__complete", "config", "set", "nginx", "resources.")
	if !strings.Contains(out, "resources.memory") || !strings.Contains(out, "resources.cpu") {
		t.Fatalf("keys: %q", out)
	}
	out, _, _ = h.run("", "__complete", "config", "get", "")
	if !strings.Contains(out, "nginx") {
		t.Fatalf("jail names first: %q", out)
	}
	out, _, _ = h.run("", "__complete", "config", "set", "nginx", "bo")
	if !strings.Contains(out, "boot.autostart") {
		t.Fatalf("set alias keys: %q", out)
	}
	out, _, _ = h.run("", "__complete", "project", "switch", "")
	if !strings.Contains(out, "web") || !strings.Contains(out, "main") || strings.Contains(out, "default") {
		t.Fatalf("projects: %q", out)
	}
	out, _, _ = h.run("", "__complete", "--project", "")
	if !strings.Contains(out, "web") {
		t.Fatalf("--project value: %q", out)
	}
	out, _, _ = h.run("", "__complete", "storage", "show", "")
	if !strings.Contains(out, "default") {
		t.Fatalf("storage: %q", out)
	}
	out, _, _ = h.run("", "__complete", "create", "x", "--image", "")
	if !strings.Contains(out, "freebsd-15") {
		t.Fatalf("image flag: %q", out)
	}
	out, _, _ = h.run("", "__complete", "create", "x", "--network", "")
	if !strings.Contains(out, "lan") {
		t.Fatalf("network flag: %q", out)
	}
	out, _, _ = h.run("", "__complete", "network", "grant", "")
	if !strings.Contains(out, "lan") {
		t.Fatalf("network grant: %q", out)
	}
	out, _, _ = h.run("", "__complete", "storage", "grant", "")
	if !strings.Contains(out, "default") {
		t.Fatalf("storage grant: %q", out)
	}
	_ = json.Valid
}

func TestProfiles(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("profile", "create", "base", "--comment", "defaults", "--set", "resources.memory=1G", "--set", "boot.autostart=true")
	tr.ok("profile", "create", "web", "--set", "resources.memory=4G", "--set", "network.eth1=lan")
	tr.do("", "profile", "create", "bad", "--set", "hostname=x")
	tr.do("", "profile", "create", "bad2", "--set", "nonsense=1")
	tr.do("", "profile", "create", "bad3", "--set", "resources.memory")
	tr.ok("profile", "list")
	tr.ok("profile", "show", "web")
	tr.ok("create", "nginx", "--image", "freebsd-15", "--network", "lan", "--storage", "default", "--profile", "base", "--profile", "web")
	tr.ok("config", "set", "nginx", "resources.memory", "8G")
	tr.ok("config", "show", "nginx", "--expanded")
	tr.ok("config", "show", "nginx")
	tr.ok("profile", "edit", "base", "-f", writeFile(t, h.dir, "p.yaml", "comment: defaults\nconfig:\n  resources.memory: 2G\n  boot.priority: \"5\"\n", 0o644))
	tr.ok("config", "show", "nginx", "--expanded", "-o", "json")
	tr.do("", "profile", "delete", "-y", "base") // in use
	tr.ok("delete", "--confirm-data-loss", "nginx")
	tr.ok("profile", "delete", "-y", "base")
	tr.ok("--project", "host", "profile", "list")
	tr.golden("profiles")
}
