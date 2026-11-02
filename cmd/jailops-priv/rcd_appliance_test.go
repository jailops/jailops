package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rcStub stands in for /etc/rc.subr: load_rc_config reads the test's rc.conf, run_rc_command prints what the script
// derived and, for start, runs the precmd and postcmd with the helper, the daemon, install(1) and the system console
// replaced by recorders.
const rcStub = `
load_rc_config() { [ -f "$RC_CONF" ] && . "$RC_CONF"; return 0; }
err() { echo "err: $2" >&2; exit "$1"; }
install() { echo "install $*" >> "$LOG"; }
run_rc_command() {
	echo "command_args=${command_args}"
	echo "required_files=${required_files}"
	if [ "$1" = start ]; then
		procname="$FAKE_HELPER"
		jailopsd_helper="$FAKE_HELPER"
		jailopsd_console="$CONSOLE"
		su() { shift 3; sh -c "$*"; }
		if [ -n "${start_precmd}" ]; then eval ${start_precmd} || { echo "precmd failed"; return 1; }; fi
		if [ -n "${start_postcmd}" ]; then eval ${start_postcmd}; fi
	fi
}
`

type rcRun struct {
	out, log, console string
	code              int
}

// runRC runs a packaged rc script with rcconf as /etc/rc.conf; the fake helper records its arguments and answers
// setup-token with a token, and fails appliance-init when its pool is "badpool".
func runRC(t *testing.T, script, rcconf, arg string, privConf bool) rcRun {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Fatal("no sh: the rc.d tests need a POSIX shell")
	}
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join("..", "..", "packaging", "rc.d", script))
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "rc.subr")
	body := strings.Replace(string(b), ". /etc/rc.subr", ". "+stub, 1)
	conf := filepath.Join(dir, "priv.conf")
	rcconf += "\njailops_priv_config=\"" + conf + "\"\n"
	if privConf {
		os.WriteFile(conf, nil, 0o600)
	}
	for name, content := range map[string]string{"rc.subr": rcStub, "script": body, "rc.conf": rcconf,
		"helper": "#!/bin/sh\necho \"helper $*\" >> \"$LOG\"\ncase \"$*\" in *badpool*) exit 1 ;; *setup-token*) echo \"Setup token:       TOKEN\" ;; esac\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", filepath.Join(dir, "script"), arg)
	cmd.Env = append(os.Environ(), "RC_CONF="+filepath.Join(dir, "rc.conf"), "LOG="+filepath.Join(dir, "log"),
		"FAKE_HELPER="+filepath.Join(dir, "helper"), "CONSOLE="+filepath.Join(dir, "console"))
	out, err := cmd.CombinedOutput()
	r := rcRun{out: string(out)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	lb, _ := os.ReadFile(filepath.Join(dir, "log"))
	cb, _ := os.ReadFile(filepath.Join(dir, "console"))
	r.log, r.console = string(lb), string(cb)
	return r
}

// One rc.conf switch: jailops_appliance passes the modules to the helper and the daemon (all by default, the list
// turned into the flag's form), the helper's first start runs appliance-init without priv.conf and never with one,
// the daemon's start prints the setup token on the console; without the switch nothing of it happens.
func TestRCDApplianceSwitch(t *testing.T) {
	off := runRC(t, "jailops_priv", "", "status", true)
	if strings.Contains(off.out, "appliance") || !strings.Contains(off.out, "required_files=") || strings.Contains(off.out, "required_files=\n") {
		t.Errorf("off:\n%s", off.out)
	}
	on := runRC(t, "jailops_priv", `jailops_appliance="YES"`, "status", true)
	if !strings.Contains(on.out, "-appliance-modules all") || !strings.Contains(on.out, "required_files=\n") {
		t.Errorf("on:\n%s", on.out)
	}
	list := runRC(t, "jailops_priv", "jailops_appliance=\"yes\"\njailops_appliance_modules=\"identity firewall\"", "status", true)
	if !strings.Contains(list.out, "-appliance-modules identity,firewall") {
		t.Errorf("list:\n%s", list.out)
	}
	bad := runRC(t, "jailops_priv", "jailops_appliance=\"YES\"\njailops_appliance_modules=\"identity; reboot\"", "status", true)
	if bad.code != 1 || !strings.Contains(bad.out, "invalid jailops_appliance_modules") {
		t.Errorf("bad list: %d\n%s", bad.code, bad.out)
	}
	// first start: no priv.conf, appliance-init with the pool of rc.conf, then check-config with the modules
	first := runRC(t, "jailops_priv", "jailops_appliance=\"YES\"\njailops_pool=\"tank\"", "start", false)
	if !strings.Contains(first.log, "appliance-init -config ") || !strings.Contains(first.log, " -pool tank") ||
		!strings.Contains(first.log, "-appliance-modules all check-config") {
		t.Errorf("first start:\n%s\n%s", first.out, first.log)
	}
	if i, j := strings.Index(first.log, "appliance-init"), strings.Index(first.log, "install -d -m 0710 -o root -g jailops /var/run/jailops-priv"); i < 0 || j < i {
		t.Errorf("the group is used before appliance-init makes it:\n%s", first.log)
	}
	if noPool := runRC(t, "jailops_priv", `jailops_appliance="YES"`, "start", false); strings.Contains(noPool.log, "-pool") || !strings.Contains(noPool.log, "appliance-init") {
		t.Errorf("default pool:\n%s", noPool.log)
	}
	if failed := runRC(t, "jailops_priv", "jailops_appliance=\"YES\"\njailops_pool=\"badpool\"", "start", false); !strings.Contains(failed.out, "precmd failed") {
		t.Errorf("a failed setup starts the helper:\n%s", failed.out)
	}
	if odd := runRC(t, "jailops_priv", "jailops_appliance=\"YES\"\njailops_pool=\"tank;reboot\"", "start", false); !strings.Contains(odd.out, "is not a pool name") || strings.Contains(odd.log, "appliance-init") {
		t.Errorf("a pool name with a semicolon:\n%s\n%s", odd.out, odd.log)
	}
	if dash := runRC(t, "jailops_priv", "jailops_appliance=\"YES\"\njailops_pool=\"-dataset\"", "start", false); !strings.Contains(dash.out, "is not a pool name") || strings.Contains(dash.log, "appliance-init") {
		t.Errorf("a pool name with a leading dash:\n%s\n%s", dash.out, dash.log)
	}
	again := runRC(t, "jailops_priv", "jailops_appliance=\"YES\"\njailops_pool=\"tank\"", "start", true)
	if strings.Contains(again.log, "appliance-init") {
		t.Errorf("appliance-init with priv.conf there:\n%s", again.log)
	}
	if plain := runRC(t, "jailops_priv", "", "start", false); strings.Contains(plain.log, "appliance-init") {
		t.Errorf("appliance-init without the switch:\n%s", plain.log)
	}

	d := runRC(t, "jailopsd", "jailops_appliance=\"YES\"\njailopsd_web=\"YES\"", "start", true)
	if !strings.Contains(d.out, "-web -appliance-modules all") || !strings.Contains(d.log, "setup-token -wait 30s -syslog") ||
		!strings.Contains(d.console, "Setup token:       TOKEN") || !strings.Contains(d.out, "Setup token:       TOKEN") {
		t.Errorf("jailopsd:\n%s\n%s\n%s", d.out, d.log, d.console)
	}
	if !strings.Contains(d.log, "-appliance-modules all -check-config") {
		t.Errorf("checkconfig without the modules:\n%s", d.log)
	}
	if dOff := runRC(t, "jailopsd", `jailopsd_web="YES"`, "start", true); strings.Contains(dOff.log, "setup-token") || strings.Contains(dOff.out, "appliance") || dOff.console != "" {
		t.Errorf("jailopsd without the switch:\n%s\n%s", dOff.out, dOff.log)
	}
}
