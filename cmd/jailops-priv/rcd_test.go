package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rcHeader is the rcorder(8) header of a script: PROVIDE, REQUIRE and BEFORE.
type rcHeader struct{ provide, require, before []string }

func readRCHeader(t *testing.T, path string) rcHeader {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var h rcHeader
	for _, ln := range strings.Split(string(b), "\n") {
		for key, dst := range map[string]*[]string{"# PROVIDE:": &h.provide, "# REQUIRE:": &h.require, "# BEFORE:": &h.before} {
			if v, ok := strings.CutPrefix(ln, key); ok {
				*dst = append(*dst, strings.Fields(v)...)
			}
		}
	}
	return h
}

// rcOrder sorts the scripts as rcorder does: REQUIRE and BEFORE are edges; a cycle fails the test.
func rcOrder(t *testing.T, scripts map[string]rcHeader) []string {
	t.Helper()
	provider := map[string]string{}
	for name, h := range scripts {
		for _, p := range h.provide {
			provider[p] = name
		}
	}
	after := map[string][]string{} // name -> what must run first
	for name, h := range scripts {
		for _, r := range h.require {
			if p, ok := provider[r]; ok {
				after[name] = append(after[name], p)
			}
		}
		for _, b := range h.before {
			if p, ok := provider[b]; ok {
				after[p] = append(after[p], name)
			}
		}
	}
	var out []string
	state := map[string]int{}
	var visit func(string)
	visit = func(n string) {
		switch state[n] {
		case 1:
			t.Fatalf("a cycle through %s", n)
		case 2:
			return
		}
		state[n] = 1
		deps := slices.Clone(after[n])
		slices.Sort(deps)
		for _, d := range deps {
			visit(d)
		}
		state[n] = 2
		out = append(out, n)
	}
	names := make([]string, 0, len(scripts))
	for n := range scripts {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		visit(n)
	}
	return out
}

// The boot order the package's scripts ask for, against the headers of the FreeBSD 15 base scripts they name: an
// unconfirmed host change is put back (jailops_revert) before netif and pf start, and before the helper itself.
// UNVERIFIED: this checks the text of the headers only; a real boot with a pending change has not been run.
func TestRCDOrdering(t *testing.T) {
	dir := filepath.Join("..", "..", "packaging", "rc.d")
	scripts := map[string]rcHeader{
		// the base scripts, with the headers of /etc/rc.d on 15.1 (only the lines that matter here)
		"FILESYSTEMS": {provide: []string{"FILESYSTEMS"}, require: []string{"root", "zfs"}},
		"root":        {provide: []string{"root"}},
		"zfs":         {provide: []string{"zfs"}, require: []string{"zfsbe"}, before: []string{"FILESYSTEMS", "var"}},
		"netif":       {provide: []string{"netif"}, require: []string{"FILESYSTEMS", "iovctl", "serial", "sppp", "sysctl"}},
		"routing":     {provide: []string{"routing"}, require: []string{"faith", "netif", "ppp", "stf"}},
		"pf":          {provide: []string{"pf"}, require: []string{"FILESYSTEMS", "netif", "pflog", "pfsync", "routing"}},
		"NETWORKING":  {provide: []string{"NETWORKING", "NETWORK"}, require: []string{"netif", "netwait", "routing", "ppp", "stf"}},
	}
	for _, n := range []string{"jailops_revert", "jailops_priv", "jailopsd"} {
		scripts[n] = readRCHeader(t, filepath.Join(dir, n))
	}
	rev := scripts["jailops_revert"]
	if !slices.Equal(rev.provide, []string{"jailops_revert"}) || !slices.Contains(rev.require, "FILESYSTEMS") ||
		!slices.Contains(rev.before, "netif") || !slices.Contains(rev.before, "pf") || !slices.Contains(rev.before, "jailops_priv") {
		t.Errorf("jailops_revert: %+v", rev)
	}
	order := rcOrder(t, scripts)
	pos := func(n string) int { return slices.Index(order, n) }
	for _, pair := range [][2]string{
		{"FILESYSTEMS", "jailops_revert"}, {"jailops_revert", "netif"}, {"jailops_revert", "pf"}, {"jailops_revert", "jailops_priv"},
		{"jailops_priv", "jailopsd"}, {"pf", "jailopsd"}, {"NETWORKING", "jailopsd"},
	} {
		if pos(pair[0]) >= pos(pair[1]) {
			t.Errorf("%s does not come before %s: %v", pair[0], pair[1], order)
		}
	}
	// the script runs the helper's own subcommand, whenever the helper is enabled
	b, _ := os.ReadFile(filepath.Join(dir, "jailops_revert"))
	for _, want := range []string{`rcvar="jailops_priv_enable"`, "jailops-priv -log syslog -config ${jailops_priv_config} revert-pending", "run_rc_command"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("jailops_revert lacks %q", want)
		}
	}
	pkg, _ := os.ReadFile(filepath.Join("..", "..", "scripts", "pkg-build.sh"))
	if !strings.Contains(string(pkg), `"$pk/rc.d/jailops_revert"`) {
		t.Error("the package does not install jailops_revert")
	}
}

// revert-pending is what the boot script runs: nothing pending is a quiet success; it takes no arguments and
// needs root.
func TestRevertPendingCommand(t *testing.T) {
	useFakeSystem(t, nil)
	dir := t.TempDir()
	conf := filepath.Join(dir, "priv.conf")
	if err := os.WriteFile(conf, []byte("state-dir = "+filepath.Join(dir, "state")+"\nhost-dir = "+filepath.Join(dir, "host")+"\nhost-manage = firewall\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runArgs(t, 0, "-dev", "-config", conf, "revert-pending"); code != 0 || out != "" {
		t.Errorf("nothing pending: %d %q %q", code, out, errs)
	}
	if code, _, _ := runArgs(t, 0, "-dev", "-config", conf, "revert-pending", "now"); code != 2 {
		t.Errorf("an argument: %d", code)
	}
	if code, _, _ := runArgs(t, 1000, "-config", conf, "revert-pending"); code != 1 {
		t.Errorf("not root: %d", code)
	}
}
