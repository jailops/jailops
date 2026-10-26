package priv

import (
	"context"
	fmt "fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/sys/zfs"
)

type env struct {
	t      *testing.T
	base   string
	cfg    Config
	zfs    *zfs.Fake
	jails  *jailos.Fake
	pf     *pfctl.Fake
	net    *ifconfig.Fake
	runner *run.Fake
	jexec  string
	local  *Local
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, base: dir}
	e.cfg = Config{
		StorageRoots: []string{"tank/jailops"},
		StorageFile:  filepath.Join(dir, "priv.d", "storage.conf"),
		MountBase:    filepath.Join(dir, "jailops"),
		ConfDir:      filepath.Join(dir, "etc", "jails"),
		StateDir:     filepath.Join(dir, "jailops", "state"),
		HelperPath:   "/usr/local/libexec/jailops-priv",
		NDPProxy:     []NDPUplink{{Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8:1:2:100::/80")}}},
	}
	for _, d := range []string{e.cfg.MountBase, e.cfg.ConfDir, e.cfg.StateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.jexec = filepath.Join(dir, "jexec")
	// A stand-in for jexec(8): it records its arguments and runs the command
	// (after the optional -U user and the jail name) in this process tree.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\nif [ \"$1\" = -U ]; then shift 2; fi\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(e.jexec, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.zfs = zfs.NewFake("tank")
	if err := e.zfs.Create(context.Background(), zfs.CreateOpts{Name: "tank/jailops", Props: map[string]string{"jailops:managed": "on", "mountpoint": e.cfg.MountBase}}); err != nil {
		t.Fatal(err)
	}
	e.jails = jailos.NewFake()
	e.pf = pfctl.NewFake()
	e.net = ifconfig.NewFake(ifconfig.Options{Prefixes: []string{"jo"}})
	e.runner = run.NewFake()
	l, err := NewLocal(e.cfg, Deps{ZFS: e.zfs, Jails: e.jails, PF: e.pf, Net: e.net, Runner: e.runner, Jexec: e.jexec,
		NDP: newFakeNDP(dir)}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	e.local = l
	e.cfg = l.Config()
	return e
}

// managedDS creates a dataset below the root with jailops:managed=on.
func (e *env) managedDS(name string) {
	e.t.Helper()
	err := e.zfs.Create(context.Background(), zfs.CreateOpts{Name: name, Parents: true, Props: map[string]string{"jailops:managed": "on"}})
	if err != nil {
		e.t.Fatal(err)
	}
}

const goodConf = `# Managed by JailOps. Do not edit; changes are overwritten.
alpha_web {
	path = "%s";
	host.hostname = "web.example.org";
	vnet;
	vnet.interface = "jo0123456789a0b";
	devfs_ruleset = 4;
	mount.devfs;
	exec.clean;
	exec.prestart = "/usr/local/libexec/jailops-priv hook prestart ${name}";
	exec.poststart = "/usr/local/libexec/jailops-priv hook poststart ${name}";
	exec.prestop = "/usr/local/libexec/jailops-priv hook prestop ${name}";
	exec.poststop = "/usr/local/libexec/jailops-priv hook poststop ${name}";
	exec.start = "/bin/sh /etc/rc";
	exec.stop = "/bin/sh /etc/rc.shutdown jail";
	enforce_statfs = 2;
	securelevel = 2;
	osrelease = "15.0-RELEASE";
}
`

func (e *env) goodConf() string {
	return sprintf(goodConf, filepath.Join(e.cfg.MountBase, "projects", "alpha", "jails", "web", "root"))
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func jailosExec(argv ...string) jailos.ExecOpts { return jailos.ExecOpts{Argv: argv} }

type netipPrefix = netip.Prefix

func parsePrefix(s string) (netip.Prefix, error) { return netip.ParsePrefix(s) }

type jailosExecOpts = jailos.ExecOpts

func removeAll(p string) error { return os.RemoveAll(p) }

// conf makes jail key a managed jail (its conf file exists).
func (e *env) conf(t *testing.T, key string) {
	t.Helper()
	mustWrite(t, filepath.Join(e.cfg.ConfDir, key+".conf"), strings.Replace(e.goodConf(), "alpha_web {", key+" {", 1))
}
