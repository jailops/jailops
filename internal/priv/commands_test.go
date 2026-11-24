package priv

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

func TestCommandRulesAreDocumentedAndFixed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range CommandRules {
		if r.Reason == "" {
			t.Errorf("rule %s has no reason", r.Name)
		}
		if !filepath.IsAbs(r.Binary) || filepath.Clean(r.Binary) != r.Binary {
			t.Errorf("rule %s: binary %q", r.Name, r.Binary)
		}
		if seen[r.Name] {
			t.Errorf("rule %s twice", r.Name)
		}
		seen[r.Name] = true
		if r.MaxTimeoutSeconds <= 0 || r.MaxTimeoutSeconds > 3600 {
			t.Errorf("rule %s: timeout %d", r.Name, r.MaxTimeoutSeconds)
		}
	}
	for _, n := range []string{"pkg", "sysctl", "kldstat", "kldload", "pfctl", "uname", "rctl"} {
		if !seen[n] {
			t.Errorf("no rule for %s", n)
		}
	}
	// ifconfig is in the table only in the forms customIfconfig accepts: the
	// MTU and the host interfaces go through the net.* operations.
}

func TestExtendedCommandRules(t *testing.T) {
	e := newEnv(t)
	c := e.cfg
	ctx := context.Background()
	adm := WithAdmin(ctx, "alice")
	ok := func(ctx context.Context, bin string, argv ...string) {
		t.Helper()
		_, out, err := c.CheckCommandCtx(ctx, argv)
		if err != nil {
			t.Errorf("%q: %v", argv, err)
			return
		}
		if out[0] != bin || strings.Join(out[1:], "\x00") != strings.Join(argv[1:], "\x00") {
			t.Errorf("%q ran as %q", argv, out)
		}
	}
	bad := func(ctx context.Context, code api.Code, argv ...string) {
		t.Helper()
		_, _, err := c.CheckCommandCtx(ctx, argv)
		if !api.IsCode(err, code) {
			t.Errorf("%q: %v (want %s)", argv, err, code)
		}
	}
	// sysctl reads
	ok(ctx, "/sbin/sysctl", "sysctl", "-n", "kern.osrelease")
	ok(ctx, "/sbin/sysctl", "sysctl", "-n", "net.inet.ip.forwarding")
	ok(ctx, "/sbin/sysctl", "sysctl", "-n", "hw.ncpu")
	// The doctor reads the answer-off-link-probes switch of a shared IPv6 segment; nobody writes it through the helper.
	ok(ctx, "/sbin/sysctl", "sysctl", "-n", "net.inet6.icmp6.nd6_onlink_ns_rfc4861")
	bad(adm, api.CodeForbidden, "sysctl", "net.inet6.icmp6.nd6_onlink_ns_rfc4861=1")
	bad(adm, api.CodeForbidden, "sysctl", "-w", "net.inet6.icmp6.nd6_onlink_ns_rfc4861=1")
	ok(ctx, "/sbin/sysctl", "sysctl", "-n", "vfs.zfs.bclone_enabled") // read only: doctor zfs.block_cloning
	bad(adm, api.CodeForbidden, "sysctl", "vfs.zfs.bclone_enabled=0")
	bad(adm, api.CodeForbidden, "sysctl", "vfs.zfs.bclone_enabled=1")
	for _, a := range [][]string{
		{"-n", "kern.securelevel"}, {"-n", "net.link.epair.q2"}, {"-n"}, {}, {"kern.osrelease"}, {"-a"}, {"-n", "kern.osrelease", "hw.ncpu"}, {"-n", "kern.osrelease=1"},
		{"-n", "../kern"}, {"-N", "kern.osrelease"}, {"-w", "net.inet.ip.forwarding=1"}, {"-n", ""}, {"-n", "kern.osrelease\n"},
		{"--", "-n", "kern.osrelease"}, {"-n", "-n"}, {"-f", "/etc/sysctl.conf"}, {"-n", "net.inet.ip.forwarding=1"},
	} {
		bad(ctx, api.CodeForbidden, append([]string{"sysctl"}, a...)...)
	}
	// sysctl writes: admin, allow-listed name, value 0 or 1
	for _, n := range []string{"net.inet6.ip6.forwarding", "net.inet.ip.forwarding"} {
		ok(adm, "/sbin/sysctl", "sysctl", n+"=1")
		ok(adm, "/sbin/sysctl", "sysctl", n+"=0")
		bad(ctx, api.CodeForbidden, "sysctl", n+"=1")
		bad(adm, api.CodeInvalid, "sysctl", n+"=2")
		bad(adm, api.CodeInvalid, "sysctl", n+"=")
		bad(adm, api.CodeInvalid, "sysctl", n+"=1;id")
		bad(adm, api.CodeInvalid, "sysctl", n+"=01")
		bad(adm, api.CodeInvalid, "sysctl", n+"=-1")
		bad(adm, api.CodeInvalid, "sysctl", n+"=1=1")
	}
	for _, a := range []string{"kern.securelevel=-1", "kern.securelevel=0", "security.jail.allow_raw_sockets=1", "net.inet.ip.forwarding", "=1",
		"hw.ncpu=1", "kern.osrelease=x", "net.inet.ip.forwarding =1", "-w"} {
		bad(adm, api.CodeForbidden, "sysctl", a)
	}
	bad(adm, api.CodeForbidden, "sysctl", "-n", "net.inet.ip.forwarding=1")
	bad(adm, api.CodeForbidden, "sysctl", "net.inet.ip.forwarding=1", "kern.securelevel=3")
	// kldstat / kldload
	for _, m := range KldModules {
		ok(ctx, "/sbin/kldstat", "kldstat", "-n", m)
		ok(ctx, "/sbin/kldload", "kldload", m)
	}
	if len(KldModules) != 12 {
		t.Errorf("module list changed: %v", KldModules)
	}
	for _, a := range [][]string{{"-n", "vmm"}, {"-n", "pf.ko"}, {"-n", "../pf"}, {"-n", "/boot/kernel/pf.ko"}, {"-m", "pf"}, {"-v"}, {}, {"-n"}, {"-n", "pf", "zfs"}, {"-q", "-q", "-n", "pf"}, {"-n", "-q", "pf"}, {"-n", "pf\n"}, {"-n", "PF"}} {
		bad(ctx, api.CodeForbidden, append([]string{"kldstat"}, a...)...)
	}
	for _, a := range [][]string{{"vmm"}, {"pf.ko"}, {"/boot/modules/evil.ko"}, {"-v", "pf"}, {}, {"pf", "zfs"}, {"-n", "pf"}, {"-q", "-n", "pf"}, {"../pf"}, {"linux64 "}} {
		bad(ctx, api.CodeForbidden, append([]string{"kldload"}, a...)...)
	}
	// uname
	for _, f := range []string{"-r", "-m", "-s"} {
		ok(ctx, "/usr/bin/uname", "uname", f)
	}
	for _, a := range [][]string{{"-a"}, {"-rm"}, {"-r", "-m"}, {}, {"-n"}, {"-p"}, {"-s", "x"}} {
		bad(ctx, api.CodeForbidden, append([]string{"uname"}, a...)...)
	}
	// rctl: the usage of one jail by its key, nothing else
	ok(ctx, "/usr/bin/rctl", "rctl", "-u", "jail:alpha_web")
	ok(ctx, "/usr/bin/rctl", "rctl", "-u", "jail:shop_api-v2")
	for _, a := range [][]string{
		{}, {"-u"}, {"-u", "jail:"}, {"-u", "alpha_web"}, {"-u", "jail:web"}, {"-u", "jail:alpha_web", "x"}, {"-hu", "jail:alpha_web"},
		{"-u", "-h", "jail:alpha_web"}, {"-u", "jail:alpha_web\n"}, {"-u", "jail:alpha_web;id"}, {"-u", "jail:alpha_web/x"}, {"-u", "jail:a_b_c"},
		{"-u", "process:1"}, {"-u", "user:0"}, {"-u", "loginclass:default"}, {"-u", ":"}, {"-u", "jail:Alpha_web"}, {"-u", "jail:1"},
		{"-l", "jail:alpha_web"}, {"-r", "jail:alpha_web"}, {"-a", "jail:alpha_web:maxproc:deny=10"}, {"-e", "jail:alpha_web"},
	} {
		_, _, err := c.CheckCommandCtx(adm, append([]string{"rctl"}, a...))
		if err == nil {
			t.Errorf("rctl %q accepted", a)
		}
	}
	// dnctl: the pipes of the bandwidth limits, in JailOps's own range only
	ok(ctx, "/sbin/dnctl", "dnctl", "pipe", "40000", "config", "bw", "0")
	ok(ctx, "/sbin/dnctl", "dnctl", "pipe", "59999", "config", "bw", "10000000bit/s")
	ok(ctx, "/sbin/dnctl", "dnctl", "pipe", "delete", "40001")
	ok(ctx, "/sbin/dnctl", "dnctl", "pipe", "list")
	for _, a := range [][]string{
		{"pipe", "39999", "config", "bw", "10000000bit/s"}, {"pipe", "60000", "config", "bw", "0"}, {"pipe", "1", "config", "bw", "0"},
		{"pipe", "040000", "config", "bw", "0"}, {"pipe", "-1", "config", "bw", "0"}, {"pipe", "40000", "config", "bw", "10M"},
		{"pipe", "40000", "config", "bw", "10Mbit/s"}, {"pipe", "40000", "config", "bw", "-5bit/s"}, {"pipe", "40000", "config", "bw", "tun0"},
		{"pipe", "40000", "config", "delay", "250ms"}, {"pipe", "40000", "config", "bw", "10000bit/s", "queue", "50"},
		{"pipe", "delete", "1"}, {"pipe", "delete", "all"}, {"pipe", "delete"}, {"pipe", "show"}, {"queue", "40000", "config", "bw", "0"},
		{"sched", "40000", "config", "type", "wf2q+"}, {"-f", "pipe", "list"}, {"flush"}, {},
	} {
		bad(ctx, api.CodeForbidden, append([]string{"dnctl"}, a...)...)
	}
	// pfctl: read-only, jailops anchors only
	ok(ctx, "/sbin/pfctl", "pfctl", "-s", "info")
	ok(ctx, "/sbin/pfctl", "pfctl", "-a", "jailops/web/nginx", "-s", "rules")
	ok(ctx, "/sbin/pfctl", "pfctl", "-s", "nat", "-a", "jailops/web/nginx")
	ok(ctx, "/sbin/pfctl", "pfctl", "-a", "jailops/shared.pf", "-s", "Tables")
	ok(ctx, "/sbin/pfctl", "pfctl", "-s", "rules")
	ok(ctx, "/sbin/pfctl", "pfctl", "-s", "nat")
	ok(ctx, "/sbin/pfctl", "pfctl", "-s", "memory")
	ok(ctx, "/sbin/pfctl", "pfctl", "-a", "jailops", "-s", "Anchors")
	ok(ctx, "/sbin/pfctl", "pfctl", "-a", "jailops/web", "-s", "Anchors")
	for _, a := range [][]string{
		{"-s", "Tables"}, {"-s", "Anchors"}, // no anchor: only info, rules and nat of the main ruleset are readable
		{"-a", "other", "-s", "rules"}, {"-a", "jailops/../x", "-s", "rules"}, {"-a", "jailopsX/x", "-s", "rules"}, {"-a", "/jailops/x", "-s", "rules"},
		{"-a", "", "-s", "rules"}, {"-s", "all"}, {"-s", "states"}, {"-s", "Sources"}, {"-s", "labels"}, {"-s", "info", "-s", "rules"},
		{"-a", "jailops/x", "-a", "jailops/y", "-s", "rules"}, {"-f", "/etc/pf.conf"}, {"-d"}, {"-e"}, {"-F", "all"}, {"-t", "x", "-T", "flush"},
		{"-s"}, {}, {"-a", "jailops/x"}, {"-a", "jailops/x", "-s", "rules", "-f", "/x"}, {"-a", "jailops/x", "-s", "rules", "extra"}, {"-sr"}, {"-vs", "rules"},
		{"-a", "jailops/x y", "-s", "rules"}, {"-a", "jailops/x\n", "-s", "rules"}, {"-n", "-f", "/etc/pf.conf"},
	} {
		_, _, err := c.CheckCommandCtx(adm, append([]string{"pfctl"}, a...))
		if err == nil {
			t.Errorf("pfctl %q accepted", a)
		}
	}
	// the others stay unreachable
	for _, n := range []string{"ifconfig", "route", "zfs", "jail", "jexec", "sh", "rm", "kldunload", "service", "mount", "umount", "sysctl.conf"} {
		bad(adm, api.CodeForbidden, n, "x")
	}
}

func TestRunnerRunsExtendedRulesWithFixedBinaries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	adm := WithAdmin(ctx, "alice")
	r := e.local.Runner()
	e.runner.On("/sbin/kldload", []string{"if_epair"}, "")
	e.runner.On("/usr/bin/uname", []string{"-r"}, "15.0-RELEASE\n")
	e.runner.On("/sbin/sysctl", []string{"net.inet.ip.forwarding=1"}, "")
	e.runner.On("/sbin/pfctl", []string{"-a", "jailops/web/x", "-s", "rules"}, "pass\n")
	if _, err := r.Run(ctx, run.Cmd{Name: "kldload", Args: []string{"if_epair"}}); err != nil {
		t.Error(err)
	}
	if res, err := r.Run(ctx, run.Cmd{Name: "uname", Args: []string{"-r"}}); err != nil || string(res.Stdout) != "15.0-RELEASE\n" {
		t.Errorf("%q %v", res.Stdout, err)
	}
	if res, err := r.Run(ctx, run.Cmd{Name: "pfctl", Args: []string{"-a", "jailops/web/x", "-s", "rules"}}); err != nil || string(res.Stdout) != "pass\n" {
		t.Errorf("%q %v", res.Stdout, err)
	}
	if _, err := r.Run(ctx, run.Cmd{Name: "sysctl", Args: []string{"net.inet.ip.forwarding=1"}}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("sysctl write without admin: %v", err)
	}
	if _, err := r.Run(adm, run.Cmd{Name: "sysctl", Args: []string{"net.inet.ip.forwarding=1"}}); err != nil {
		t.Errorf("sysctl write with admin: %v", err)
	}
	if _, err := r.Run(ctx, run.Cmd{Name: "/sbin/kldload", Args: []string{"if_epair"}}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("a caller named binary: %v", err)
	}
	for _, c := range e.runner.Calls() {
		if !filepath.IsAbs(c.Cmd.Name) {
			t.Errorf("relative binary %q reached the runner", c.Cmd.Name)
		}
	}
}

// rctl reads the usage of a jail JailOps configured; a key without a conf
// file never reaches the runner.
func TestRunnerReadsUsageOfManagedJailsOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.local.Runner()
	const out = "maxproc=3\ncputime=7\nmemoryuse=4096\n"
	e.runner.On("/usr/bin/rctl", []string{"-u", run.AnyArg}, out)
	if _, err := r.Run(ctx, run.Cmd{Name: "rctl", Args: []string{"-u", "jail:alpha_ghost"}}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("unmanaged jail: %v", err)
	}
	e.conf(t, "alpha_web")
	res, err := r.Run(ctx, run.Cmd{Name: "rctl", Args: []string{"-u", "jail:alpha_web"}, Timeout: time.Hour})
	if err != nil || string(res.Stdout) != out {
		t.Fatalf("%q %v", res.Stdout, err)
	}
	calls := e.runner.Calls()
	if last := calls[len(calls)-1].Cmd; last.Name != "/usr/bin/rctl" || last.Timeout != 10*time.Second {
		t.Errorf("ran %+v", last)
	}
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Cmd.Args, " "), "ghost") {
			t.Errorf("an unmanaged jail reached the runner: %+v", c.Cmd)
		}
	}
}

// The admin assertion travels with the request, so a sysctl write works
// through the socket only for an admin.
func TestSysctlWriteOverTheSocket(t *testing.T) {
	f := newFixture(t)
	f.runner.On("/sbin/sysctl", []string{"net.inet.ip.forwarding=0"}, "")
	cmd := run.Cmd{Name: "sysctl", Args: []string{"net.inet.ip.forwarding=0"}}
	if _, err := f.client.Runner().Run(context.Background(), cmd); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("without admin: %v", err)
	}
	if _, err := f.client.Runner().Run(WithAdmin(context.Background(), "alice"), cmd); err != nil {
		t.Errorf("with admin: %v", err)
	}
}

func TestSetMTU(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, _, err := e.net.EpairCreate(ctx, "jo0"); err != nil {
		t.Fatal(err)
	}
	e.runner.On("ifconfig", []string{"jo0a", "mtu", "1400"}, "")
	if err := e.local.SetMTU(ctx, "jo0a", 1400); err != nil {
		t.Fatal(err)
	}
	last := e.runner.Calls()[len(e.runner.Calls())-1].Cmd
	if last.Name != "ifconfig" || strings.Join(last.Args, " ") != "jo0a mtu 1400" {
		t.Errorf("ran %+v", last)
	}
	for _, c := range []struct {
		iface string
		mtu   int
	}{{"em0", 1400}, {"jo0a", 0}, {"jo0a", -1}, {"jo0a", 575}, {"jo0a", 9217}, {"jo0a", 1 << 40}, {"-x", 1400}, {"", 1400}, {"jo0a mtu 9", 1400}, {"jo", 1400}} {
		if err := e.local.SetMTU(ctx, c.iface, c.mtu); err == nil {
			t.Errorf("SetMTU(%q, %d) accepted", c.iface, c.mtu)
		}
	}
	// the Runner cannot do it
	if _, err := e.local.Runner().Run(ctx, run.Cmd{Name: "ifconfig", Args: []string{"jo0a", "mtu", "1400"}}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("ifconfig through the runner: %v", err)
	}
}

// A client that implements the proposed ifconfig.Client method is used.
type mtuNet struct {
	*ifconfig.Fake
	got string
}

func (m *mtuNet) SetMTU(_ context.Context, iface string, mtu int) error {
	m.got = iface + ":" + string(rune('0'+mtu%10))
	return nil
}

func TestSetMTUUsesTheInterfaceMethodWhenPresent(t *testing.T) {
	e := newEnv(t)
	m := &mtuNet{Fake: e.net}
	l, err := NewLocal(e.cfg, Deps{ZFS: e.zfs, Net: m, Runner: e.runner}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.net.EpairCreate(context.Background(), "jo0"); err != nil {
		t.Fatal(err)
	}
	if err := l.SetMTU(context.Background(), "jo0a", 1500); err != nil || m.got != "jo0a:0" {
		t.Errorf("%q %v", m.got, err)
	}
	for _, c := range e.runner.Calls() {
		if c.Cmd.Name == "ifconfig" {
			t.Error("the runner was used")
		}
	}
}

func TestSetMTUOverTheSocket(t *testing.T) {
	f := newFixtureWith(t, func(e *env) {
		if _, _, err := e.net.EpairCreate(context.Background(), "jo0"); err != nil {
			t.Fatal(err)
		}
	})
	f.runner.On("ifconfig", []string{"jo0a", "mtu", "9000"}, "")
	if err := f.client.SetMTU(context.Background(), "jo0a", 9000); err != nil {
		t.Error(err)
	}
	if err := f.client.SetMTU(context.Background(), "em0", 9000); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("%v", err)
	}
}

// The in-place upgrade of a thick jail (internal/lifecycle/upgrade_thick.go) uses the pkg rule of the image builds, on the
// root of a jail that is stopped: the repository file and the work directory are in the jail's container, outside its
// root, and the package cache is the storage's. Nothing else is accepted.
func TestThickJailUpgradeCommands(t *testing.T) {
	e := newEnv(t)
	c := e.cfg
	ctx := context.Background()
	container := filepath.Join(c.MountBase, "projects", "demo", "jails", "web")
	root := filepath.Join(container, "root")
	repos := filepath.Join(container, ".upgrade-100", "repos")
	cache := filepath.Join(c.MountBase, "cache", "pkg")
	for _, d := range []string{root, repos, cache} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(repos, "jailops.conf"), []byte("jailops: {\n  url: \"pkg+https://pkg.FreeBSD.org/${ABI}/base_release_1\",\n  mirror_type: \"srv\",\n  signature_type: \"fingerprints\",\n  fingerprints: \"/usr/share/keys/pkgbase-${VERSION_MAJOR}\",\n  enabled: yes\n}\n"), 0o600)
	pkg := func(r, rp, ch string, extra ...string) []string {
		return append([]string{"pkg", "-r", r, "-o", "ABI=FreeBSD:15:amd64", "-o", "OSVERSION=1500000", "-o", "REPOS_DIR=" + rp, "-o", "PKG_CACHEDIR=" + ch, "-o", "RUN_SCRIPTS=false"}, extra...)
	}
	good := func(extra ...string) []string { return pkg(root, repos, cache, extra...) }
	scratch := filepath.Join(container, ".upgrade-100-check", "scratch")
	for _, tc := range []struct {
		name string
		argv []string
		ok   bool
	}{
		{"update", good("update", "-f", "-r", "jailops"), true},
		{"upgrade", good("upgrade", "-y", "-r", "jailops"), true},
		{"upgrade to another release", good("upgrade", "-f", "-y", "-r", "jailops"), true},
		{"release check", good("query", "%n %v", "FreeBSD-runtime"), true},
		{"the catalogue of the repository", pkg(filepath.Join(container, ".upgrade-100", "scratch"), repos, cache, "rquery", "-r", "jailops", "%n %v %sb"), true},
		{"the annotations of the catalogue", pkg(scratch, repos, cache, "rquery", "-r", "jailops", "%At %Av", "FreeBSD-runtime"), false},
		{"pkg's dry run on the copy of the database", pkg(scratch, repos, cache, "upgrade", "-n", "-U", "-r", "jailops"), true},
		{"pkg's dry run of a new release", pkg(scratch, repos, cache, "upgrade", "-f", "-n", "-U", "-r", "jailops"), true},
		{"pkg's dry run of an addition", pkg(scratch, repos, cache, "install", "-n", "-U", "-r", "jailops", "FreeBSD-ssh"), true},
		{"the packages of the copy", []string{"pkg", "-r", scratch, "query", "%n %v %sb"}, true},
		{"pkg audit of the copy with the host's database", []string{"pkg", "-r", scratch, "audit", "-f", "/var/db/pkg/vuln.xml"}, true},
		{"pkg audit with another database", []string{"pkg", "-r", scratch, "audit", "-f", "/tmp/vuln.xml"}, false},
		{"pkg audit that fetches", []string{"pkg", "-r", scratch, "audit", "-F", "-f", "/var/db/pkg/vuln.xml"}, false},
		{"the catalogue of another repository", good("rquery", "-r", "FreeBSD-ports", "%n %v"), false},
		{"another rquery format", good("rquery", "-r", "jailops", "%n %v %w"), false},
		{"the old rquery format", good("rquery", "-r", "jailops", "%n %v"), false},
		{"rquery of one package", good("rquery", "-r", "jailops", "%n %v", "FreeBSD-ssh"), false},
		{"installed packages with their sizes", good("query", "%n %v %sb"), true},
		{"the size of what fetch would download", good("fetch", "-n", "-y", "-d", "-r", "jailops", "FreeBSD-runtime"), true},
		{"the checksums of changed packages", good("check", "-s", "FreeBSD-runtime"), true},
		{"fetch the packages of an upgrade", good("fetch", "-y", "-d", "-r", "jailops", "FreeBSD-runtime"), true},
		{"add base packages", good("install", "-y", "-r", "jailops", "FreeBSD-ssh", "FreeBSD-ee"), true},
		{"add a base package, dry", good("install", "-n", "-r", "jailops", "FreeBSD-ssh"), true},
		{"add a package file", good("install", "-y", "-r", "jailops", "/tmp/x.pkg"), false},
		{"add a package archive name", good("install", "-y", "-r", "jailops", "FreeBSD-ssh.pkg"), false},
		{"add a name that climbs out", good("install", "-y", "-r", "jailops", "../FreeBSD-ssh"), false},
		{"add with a script option", good("install", "-y", "-r", "jailops", "--no-scripts", "FreeBSD-ssh"), false},
		{"add with another repository directory", pkg(root, "/usr/local/etc/pkg/repos", cache, "install", "-y", "-r", "jailops", "FreeBSD-ssh"), false},
		{"add into the host", pkg("/", repos, cache, "install", "-y", "-r", "jailops", "FreeBSD-ssh"), false},
		{"the root is the storage", pkg(c.MountBase, repos, cache, "upgrade", "-y", "-r", "jailops"), false},
		{"the root is the host", pkg("/", repos, cache, "upgrade", "-y", "-r", "jailops"), false},
		{"the root climbs out", pkg(root+"/../../../..", repos, cache, "upgrade", "-y", "-r", "jailops"), false},
		{"the repository directory is the host's", pkg(root, "/usr/local/etc/pkg/repos", cache, "upgrade", "-y", "-r", "jailops"), false},
		{"the cache is the host's", pkg(root, repos, "/var/cache/pkg", "upgrade", "-y", "-r", "jailops"), false},
		{"a repository name with a path", good("upgrade", "-y", "-r", "../jailops"), false},
		{"the repository is a file", good("upgrade", "-y", "-r", "jailops", "x.pkg"), false},
		{"a script of the jail", good("upgrade", "-y", "-r", "jailops", "--pre-script", "/x"), false},
		{"no root", []string{"pkg", "-o", "ABI=FreeBSD:15:amd64", "-o", "REPOS_DIR=" + repos, "upgrade", "-y"}, false},
		{"the option that runs no scripts", good("-o", "RUN_SCRIPTS=false", "upgrade"), true},
		{"the option that runs the scripts", good("-o", "RUN_SCRIPTS=true", "upgrade"), false},
		{"another database", append([]string{"pkg", "-r", root, "-o", "PKG_DBDIR=/var/db/pkg"}, "upgrade", "-y"), false},
	} {
		_, out, err := c.CheckCommandCtx(WithAdmin(ctx, "a"), tc.argv)
		if tc.ok && (err != nil || out[0] != "/usr/sbin/pkg") {
			t.Errorf("%s: %q refused: %v", tc.name, tc.argv, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: %q accepted", tc.name, tc.argv)
		}
	}
	// the root of a jail that runs is not a root for pkg -r: the verification stops it (pkgtarget_test.go has the cases)
}

func TestImageBuildingCommands(t *testing.T) {
	e := newEnv(t)
	c := e.cfg
	ctx := context.Background()
	root := filepath.Join(e.cfg.MountBase, "images", "freebsd-15", "g1", "root")
	work := filepath.Join(e.cfg.StateDir, "work")
	repos := filepath.Join(e.cfg.StateDir, "repos")
	for _, d := range []string{root, work, repos, filepath.Join(root, "etc")} {
		os.MkdirAll(d, 0o755)
	}
	mp := filepath.Join(work, "master.passwd")
	os.WriteFile(mp, []byte("root::0:0::0:0:r:/root:/bin/sh\n"), 0o600)
	ok := func(bin string, argv ...string) {
		t.Helper()
		_, out, err := c.CheckCommandCtx(ctx, argv)
		if err != nil || out[0] != bin {
			t.Errorf("%q: %v %q", argv, err, out)
		}
	}
	bad := func(argv ...string) {
		t.Helper()
		if _, _, err := c.CheckCommandCtx(WithAdmin(ctx, "a"), argv); err == nil {
			t.Errorf("accepted %q", argv)
		}
	}
	pkg := func(extra ...string) []string {
		return append([]string{"pkg", "-r", root, "-o", "REPOS_DIR=" + repos, "-o", "ASSUME_ALWAYS_YES=yes", "-o", "IGNORE_OSVERSION=yes", "-o", "ABI=FreeBSD:15:amd64"}, extra...)
	}
	for _, sub := range [][]string{{"install", "-y", "pkg"}, {"upgrade"}, {"query", "%n-%v"}, {"query", "%n-%v", "pkg"}, {"install", "nginx", "curl"}} {
		ok("/usr/sbin/pkg", pkg(sub...)...)
	}
	// the download limits of the shell setup: in front of -r and after it, as the daemon sends them
	ok("/usr/sbin/pkg", "pkg", "-o", "FETCH_TIMEOUT=10", "-o", "FETCH_RETRY=1", "-r", root, "install", "-y", "bash")
	ok("/usr/sbin/pkg", "pkg", "-o", "FETCH_TIMEOUT=10", "-o", "FETCH_RETRY=1", "-j", "demo_web", "install", "-y", "bash")
	for _, o := range []string{"FETCH_TIMEOUT=0", "FETCH_TIMEOUT=601", "FETCH_TIMEOUT=10s", "FETCH_TIMEOUT=-1", "FETCH_RETRY=11", "FETCH_RETRY=", "FETCH_RETRY=010"} {
		bad("pkg", "-r", root, "-o", o, "install", "x")
	}
	ok("/usr/sbin/pkg", "pkg", "config", "abi")
	bad("pkg", "config", "abi", "x")
	bad("pkg", "config", "repos")
	for _, o := range []string{"REPOS_DIR=/etc/pkg", "REPOS_DIR=" + root + "/../../..", "ASSUME_ALWAYS_YES=no", "IGNORE_OSVERSION=1", "ABI=;id", "ABI=", "PKG_DBDIR=/x",
		"ASSUME_ALWAYS_YES", "INSTALL_AS_USER=yes", "REPOS_DIR=relative", "ABI=a b"} {
		bad("pkg", "-r", root, "-o", o, "install", "x")
	}
	bad("pkg", "-o", "ABI=FreeBSD:15:amd64", "install", "x") // no -r
	bad("pkg", "-r", root, "query", "%n;id", "x")
	// pwd_mkdb
	ok("/usr/sbin/pwd_mkdb", "pwd_mkdb", "-p", "-d", filepath.Join(root, "etc"), mp)
	bad("pwd_mkdb", "-p", "-d", "/etc", mp)
	bad("pwd_mkdb", "-p", "-d", filepath.Join(root, "etc"), "/etc/master.passwd")
	bad("pwd_mkdb", "-p", "-d", filepath.Join(root, "etc"), filepath.Join(work, "shadow"))
	bad("pwd_mkdb", "-d", filepath.Join(root, "etc"), mp)
	bad("pwd_mkdb", "-p", "-d", filepath.Join(root, "etc"), mp, "x")
	bad("pwd_mkdb", "-u", "root", mp)
	// pw: only root's password in an image root
	ok("/usr/sbin/pw", "pw-lock-root", "-R", root, "usermod", "root", "-w", "no")
	bad("pw-lock-root", "-R", "/", "usermod", "root", "-w", "no")
	bad("pw-lock-root", "-R", root, "usermod", "toor", "-w", "no")
	bad("pw-lock-root", "-R", root, "usermod", "root", "-w", "none")
	bad("pw-lock-root", "-R", root, "usermod", "root", "-s", "/bin/sh")
	bad("pw-lock-root", "-R", root, "useradd", "x")
	bad("pw-lock-root", "usermod", "root", "-w", "no")
	// freebsd-version
	ok("/bin/freebsd-version", "freebsd-version", "-k")
	ok("/bin/freebsd-version", "freebsd-version", "-u")
	bad("freebsd-version", "-r")
	bad("freebsd-version")
	bad("freebsd-version", "-k", "-u")
	// a symlink in the work area cannot lead out of the roots
	os.Symlink("/etc", filepath.Join(work, "evil"))
}

func TestSeedKeys(t *testing.T) {
	e := newEnv(t)
	keys := filepath.Join(e.base, "hostkeys")
	os.MkdirAll(filepath.Join(keys, "pkg", "trusted"), 0o755)
	os.WriteFile(filepath.Join(keys, "pkg", "trusted", "pkg.freebsd.org.2013102301"), []byte("key"), 0o644)
	os.Symlink("pkg/trusted", filepath.Join(keys, "link"))
	l, err := NewLocal(e.cfg, Deps{ZFS: e.zfs, Jexec: e.jexec, KeysDir: keys}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := filepath.Join(e.cfg.MountBase, "images", "x", "g1", "root")
	os.MkdirAll(root, 0o755)
	res, err := l.SeedKeys(ctx, root)
	if err != nil || res.Files != 1 || res.Symlinks != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "usr", "share", "keys", "pkg", "trusted", "pkg.freebsd.org.2013102301"))
	if string(b) != "key" {
		t.Errorf("key %q", b)
	}
	if res, err := l.SeedKeys(ctx, root); err != nil || res.Files != 0 {
		t.Errorf("not idempotent: %+v %v", res, err)
	}
	// hostile roots
	outside := filepath.Join(e.base, "outside")
	os.MkdirAll(outside, 0o755)
	os.MkdirAll(filepath.Join(e.cfg.MountBase, "images", "evil"), 0o755)
	os.Symlink(outside, filepath.Join(e.cfg.MountBase, "images", "evil", "usr"))
	for name, r := range map[string]string{"outside": "/etc", "mount root": e.cfg.MountBase, "relative": "images/x", "dotdot": e.cfg.MountBase + "/../x",
		"symlinked usr": filepath.Join(e.cfg.MountBase, "images", "evil"), "missing": filepath.Join(e.cfg.MountBase, "nope"), "empty": ""} {
		if _, err := l.SeedKeys(ctx, r); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("written outside: %v", ents)
	}
	// missing host directory
	l2, _ := NewLocal(e.cfg, Deps{ZFS: e.zfs, KeysDir: filepath.Join(e.base, "nokeys")}, WithoutChown())
	root2 := filepath.Join(e.cfg.MountBase, "images", "y")
	os.MkdirAll(root2, 0o755)
	if _, err := l2.SeedKeys(ctx, root2); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("no host keys: %v", err)
	}
}

func TestSeedKeysOverTheSocket(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.cfg.MountBase, "images", "z", "root")
	os.MkdirAll(root, 0o755)
	// the fixture's helper reads the real host directory, which may not exist
	// here: only the refusal path is checked over the wire
	if _, err := f.client.SeedKeys(context.Background(), "/etc"); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("%v", err)
	}
}

// The grace of a command (SIGTERM first, SIGKILL after it) travels over the socket to the helper that runs the process:
// without it every cancelled pkg is killed at once. The helper caps what a caller asks for.
func TestTermGraceReachesTheHelper(t *testing.T) {
	f := newFixture(t)
	f.runner.On("/sbin/sysctl", []string{"net.inet.ip.forwarding=0"}, "")
	ctx := WithAdmin(context.Background(), "alice")
	for _, tc := range []struct{ ask, want time.Duration }{
		{3 * time.Second, 3 * time.Second},
		{time.Hour, maxTermGrace},
		{0, 0},
	} {
		cmd := run.Cmd{Name: "sysctl", Args: []string{"net.inet.ip.forwarding=0"}, TermGrace: tc.ask}
		if _, err := f.client.Runner().Run(ctx, cmd); err != nil {
			t.Fatal(err)
		}
		calls := f.runner.Calls()
		if got := calls[len(calls)-1].Cmd.TermGrace; got != tc.want {
			t.Errorf("asked %v: the helper ran the command with a grace of %v, want %v", tc.ask, got, tc.want)
		}
	}
}

// pkg -r on a jail tree: the directories pkg writes as root of the host must be real directories, and nothing below the
// package database, the cache or the configuration directories may be a symbolic link.
func TestPkgTreeBounds(t *testing.T) {
	e := newEnv(t)
	c := e.cfg
	mk := func(name string) (root, repos, cache string) {
		container := filepath.Join(c.MountBase, "projects", "demo", "jails", name)
		root = filepath.Join(container, "root")
		repos = filepath.Join(container, ".upgrade-100", "repos")
		cache = filepath.Join(c.MountBase, "cache", "pkg")
		for _, d := range []string{root, repos, cache} {
			os.MkdirAll(d, 0o755)
		}
		os.WriteFile(filepath.Join(repos, "jailops.conf"), []byte("jailops: {\n  url: \"pkg+https://pkg.FreeBSD.org/${ABI}/base_release_1\",\n  mirror_type: \"srv\",\n  signature_type: \"fingerprints\",\n  fingerprints: \"/usr/share/keys/pkgbase-${VERSION_MAJOR}\",\n  enabled: yes\n}\n"), 0o600)
		return
	}
	check := func(root string) error { return verifyPkgTree(nil, root) }
	for _, rel := range pkgTreeLinks {
		root, _, _ := mk("l" + strings.NewReplacer("/", "_", ".", "_").Replace(rel))
		target := t.TempDir()
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if err := check(root); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s as a symbolic link: %v", rel, err)
		}
	}
	// real directories, and a tree without them, are fine
	root, _, _ := mk("fine")
	os.MkdirAll(filepath.Join(root, "var/db/pkg/repos"), 0o755)
	os.MkdirAll(filepath.Join(root, "usr/local/etc/pkg/repos"), 0o755)
	if err := check(root); err != nil {
		t.Errorf("a plain tree: %v", err)
	}
	jail, _, _ := mk("jail")
	os.WriteFile(filepath.Join(filepath.Dir(jail), "spec.json"), []byte(`{"spec":{"image":"freebsd-15-1"}}`), 0o600)
	if err := check(jail); err != nil {
		t.Errorf("a jail: %v", err)
	}
	// a link anywhere below the trees pkg opens by plain path (the audit's var/db/pkg/repos/jailops -> a host directory)
	for _, rel := range []string{"var/db/pkg/repos/jailops", "var/db/pkg/repos/jailops/meta", "var/db/pkg/local.sqlite-journal",
		"var/cache/pkg/FreeBSD-runtime-15.1.pkg", "var/cache/pkg/All/x", "usr/local/etc/pkg/repos/FreeBSD.conf", "etc/pkg/FreeBSD.conf"} {
		root, _, _ := mk("deep" + strings.NewReplacer("/", "_", ".", "_").Replace(rel))
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(t.TempDir(), p); err != nil {
			t.Fatal(err)
		}
		if err := check(root); err == nil || !strings.Contains(err.Error(), rel+" is a symbolic link") {
			t.Errorf("%s as a symbolic link: %v", rel, err)
		}
	}
	// the links pkg makes in its own cache name a file of the same directory: they stay
	cache, _, _ := mk("cachelinks")
	os.MkdirAll(filepath.Join(cache, "var/cache/pkg"), 0o755)
	os.WriteFile(filepath.Join(cache, "var/cache/pkg/bash-5.3.20~1a2b3c.pkg"), nil, 0o644)
	os.Symlink("bash-5.3.20~1a2b3c.pkg", filepath.Join(cache, "var/cache/pkg/bash-5.3.20.pkg"))
	if err := check(cache); err != nil {
		t.Errorf("pkg's own cache links: %v", err)
	}
	for _, target := range []string{"..", "../../etc", "/etc/passwd"} {
		l := filepath.Join(cache, "var/cache/pkg/x.pkg")
		os.Remove(l)
		os.Symlink(target, l)
		if err := check(cache); err == nil {
			t.Errorf("a cache link to %s was accepted", target)
		}
	}
	// a symbolic link elsewhere in the root is not the package database's: the tree check lets it be
	other, _, _ := mk("other")
	os.MkdirAll(filepath.Join(other, "var/db/pkg"), 0o755)
	os.Symlink("/tmp", filepath.Join(other, "var/db/elsewhere"))
	if err := check(other); err != nil {
		t.Errorf("a link outside the pkg trees: %v", err)
	}
}

// Before pkg installs, upgrades or removes packages in a root, no symbolic link may lead out of the root, whatever it
// points to: pkg creates the directories of a package through such a link, and reads a configuration file through it to
// merge it. The links the base ships stay; in the trees no base package touches only a link to a host directory counts.
func TestPkgRootLinksOut(t *testing.T) {
	root := t.TempDir()
	hostDir := t.TempDir()
	hostFile := filepath.Join(hostDir, "secret")
	os.WriteFile(hostFile, []byte("x"), 0o644)
	for _, d := range []string{"etc", "var/unbound", "usr/share/nls/C", "usr/include", "usr/lib", "usr/local/www", "home/ann"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	// what FreeBSD ships: links inside the root, and the absolute termcap and motd
	os.Symlink("../var/unbound", filepath.Join(root, "etc/unbound"))
	os.Symlink("C", filepath.Join(root, "usr/share/nls/POSIX"))
	os.Symlink("../../usr/include", filepath.Join(root, "usr/lib/include"))
	os.Symlink("/usr/share/misc/termcap", filepath.Join(root, "etc/termcap"))
	os.Symlink("/var/run/motd", filepath.Join(root, "etc/motd"))
	os.Symlink("../var/run/os-release", filepath.Join(root, "etc/os-release"))
	// the jail's own links where no base package installs, to files and to nothing
	os.Symlink("/srv/app/current", filepath.Join(root, "usr/local/www/app"))
	os.Symlink(hostFile, filepath.Join(root, "home/ann/notes"))
	if err := verifyRootLinks(root); err != nil {
		t.Fatalf("a root as FreeBSD ships it: %v", err)
	}
	for _, tc := range []struct{ link, target string }{
		{"etc/ssl", hostDir},
		{"etc/ssl", "../../../../../../../../../../" + strings.TrimPrefix(hostDir, "/")},
		{"usr/share/certs", "/"},
		// the second audit: a configuration file linked to a file of the host, absolute or climbing, or to nothing
		{"etc/crontab", hostFile},
		{"etc/crontab", "../../../../../../../../../../" + strings.TrimPrefix(hostFile, "/")},
		{"etc/crontab", "/nonexistent/file"},
		{"root/.shrc", "/etc/master.passwd"},
		// a shipped name with another target
		{"etc/termcap", hostFile},
		// a directory of the host below a tree no base package touches
		{"usr/local/www/data", hostDir},
	} {
		p := filepath.Join(root, tc.link)
		old, hadOld := os.Readlink(p)
		os.Remove(p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(tc.target, p); err != nil {
			t.Fatal(err)
		}
		if err := verifyRootLinks(root); err == nil || !strings.Contains(err.Error(), tc.link+" is a symbolic link out of the root") {
			t.Errorf("%s -> %s: %v", tc.link, tc.target, err)
		}
		os.Remove(p)
		if hadOld == nil {
			os.Symlink(old, p)
		}
	}
	for args, want := range map[string]string{"-r /x install -y a": "install", "-C /c -o A=b -r /x upgrade": "upgrade", "-r /x query %n": "query", "-j j -y": ""} {
		if got := pkgSubcommand(strings.Fields(args)); got != want {
			t.Errorf("%s: %q", args, got)
		}
	}
}

// Every pkg -r the helper runs reads an empty configuration file of the helper (never the root's pkg.conf, which could
// load a plugin into a pkg running as root of the host), with plugins off; on the root of a thick jail no package
// script runs. A caller cannot name its own configuration file.
func TestPkgRootHardened(t *testing.T) {
	e := newEnv(t)
	c := e.cfg
	container := filepath.Join(c.MountBase, "projects", "demo", "jails", "web")
	root := filepath.Join(container, "root")
	os.MkdirAll(root, 0o755)
	l := &Local{cfg: c}
	harden := func(argv ...string) []string {
		t.Helper()
		out, cleanup, err := l.hardenPkgRoot(context.Background(), argv)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		return out
	}
	got := harden("pkg", "-r", root, "query", "%n")
	if len(got) < 6 || got[1] != "-C" || got[3] != "-o" || got[4] != "PKG_ENABLE_PLUGINS=false" {
		t.Fatalf("a jail root: %q", got)
	}
	if b, err := os.ReadFile(got[2]); err != nil || len(b) != 0 || !strings.HasPrefix(got[2], c.StateDir+"/") {
		t.Errorf("the configuration file %s: %q %v", got[2], b, err)
	}
	// the place makes it a jail root, whatever its container holds (no spec.json here)
	got = harden("pkg", "-r", root, "upgrade", "-y")
	if !slices.Contains(got, "RUN_SCRIPTS=false") || got[len(got)-2] != "upgrade" {
		t.Errorf("the root of a jail: %q", got)
	}
	// a root no jail has run in yet (an app being installed) is the copy of the image: its package scripts may run
	os.WriteFile(filepath.Join(container, NeverStartedFile), nil, 0o600)
	if got := harden("pkg", "-r", root, "install", "-y", "postgresql18-server"); slices.Contains(got, "RUN_SCRIPTS=false") {
		t.Errorf("a root that never ran: %q", got)
	}
	os.Remove(filepath.Join(container, NeverStartedFile))
	os.Symlink("/etc/hosts", filepath.Join(container, NeverStartedFile))
	if got := harden("pkg", "-r", root, "install", "-y", "x"); !slices.Contains(got, "RUN_SCRIPTS=false") {
		t.Errorf("a link named like the mark: %q", got)
	}
	os.Remove(filepath.Join(container, NeverStartedFile))
	// pkg -j and pkg config abi are not changed
	for _, argv := range [][]string{{"pkg", "-j", "demo_web", "upgrade", "-n"}, {"pkg", "config", "abi"}} {
		if got := harden(argv...); !slices.Equal(got, argv) {
			t.Errorf("%q became %q", argv, got)
		}
	}
	for _, argv := range [][]string{{"pkg", "-C", "/tmp/evil.conf", "-r", root, "query", "%n"}, {"pkg", "-r", root, "-C", "/tmp/evil.conf", "query", "%n"},
		{"pkg", "-r", root, "-o", "PKG_ENABLE_PLUGINS=true", "query", "%n"}, {"pkg", "-r", root, "-o", "PLUGINS=evil", "query", "%n"},
		{"pkg", "-r", root, "-o", "RUN_SCRIPTS=true", "upgrade"}, {"pkg", "-r", root, "-o", "OSVERSION=15", "upgrade"}} {
		if _, _, err := c.CheckCommandCtx(WithAdmin(context.Background(), "a"), argv); err == nil {
			t.Errorf("accepted %q", argv)
		}
	}
	if _, _, err := c.CheckCommandCtx(WithAdmin(context.Background(), "a"), []string{"pkg", "-r", root, "-o", "OSVERSION=1501000", "-o", "RUN_SCRIPTS=false", "upgrade", "-n"}); err != nil {
		t.Errorf("OSVERSION and RUN_SCRIPTS: %v", err)
	}
}
