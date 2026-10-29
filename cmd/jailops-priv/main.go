// Command jailops-priv is the privileged helper of JailOps. It runs as root,
// listens on a unix socket and performs a fixed list of typed operations for
// jailopsd (see internal/priv). It also implements the hook subcommand that
// jail.conf's exec.* lines call:
//
//	jailops-priv [flags]                     serve
//	jailops-priv [flags] hook <phase> <jail> run one jail hook and exit
//	jailops-priv [flags] check-config        validate the configuration, print the effective roots and allow-lists
//	jailops-priv init -pool POOL [-dry-run]  one-time setup of a host (init.go)
//	jailops-priv version | --version
//
// SIGHUP rereads the storage file and verifies every storage root again.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"jailops.net/jailops/internal/priv"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

const defaultConfig = "/usr/local/etc/jailops/priv.conf"

const synopsis = "usage: jailops-priv [flags] | version | check-config | init -pool POOL [-dry-run] | hook PHASE JAIL\n"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Geteuid()))
}

// list is a repeatable, comma separated flag.
// ndpList collects the ndp-proxy lines. Its value contains commas (the prefixes), so it is not split.
type ndpList []priv.NDPUplink

func (l *ndpList) String() string { return fmt.Sprint(len(*l), " uplink(s)") }
func (l *ndpList) Set(v string) error {
	u, err := priv.ParseNDPProxy(v)
	if err != nil {
		return err
	}
	*l = append(*l, u)
	return nil
}

type list []string

func (l *list) String() string { return strings.Join(*l, ",") }
func (l *list) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

type options struct {
	config      string
	socket      string
	group       string
	mode        string
	pidfile     string
	logTo       string
	dev         bool
	daemon      bool
	rootDS      string
	storage     list
	storageFile string
	poolRoot    bool
	mountBase   string
	confDir     string
	stateDir    string
	workDir     string
	helperPath  string
	ifPrefix    list
	allowUser   list
	allowGroup  list
	ndpProxy    ndpList
	sysctl      list
	fetchHost   list
	mirror      list

	adminRawParam list
	adminAllow    list
	adminDevfs    list
	adminMounts   list
	adminZFSProp  list
	adminRoutes   bool
	adminRawPF    bool
	adminResume   bool
}

func newFlags(o *options, errw io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("jailops-priv", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s\nWithout a command the helper serves on its socket. PHASE is prestart, poststart, prestop or poststop.\n\nflags (also keys of the configuration file):\n", synopsis)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.config, "config", defaultConfig, "configuration file (key = value lines named like these flags)")
	fs.StringVar(&o.socket, "socket", priv.DefaultSocket, "unix socket path")
	fs.StringVar(&o.group, "group", "jailops", "group that owns the socket (mode 0660)")
	fs.StringVar(&o.mode, "mode", "0660", "socket mode")
	fs.StringVar(&o.pidfile, "pidfile", "", "write the process id here")
	fs.StringVar(&o.logTo, "log", "", "audit log destination: stderr or syslog (default stderr; syslog with -daemon)")
	fs.BoolVar(&o.dev, "dev", false, "development: do not require root, allow the invoking user")
	fs.BoolVar(&o.daemon, "daemon", false, "detach from the terminal (rc.d may use daemon(8) with the foreground default instead)")
	fs.StringVar(&o.rootDS, "root-dataset", "", "deprecated: same as storage-root")
	fs.Var(&o.storage, "storage-root", "storage root dataset fixed in this file, for example tank/jailops (repeatable); more are registered at run time")
	fs.StringVar(&o.storageFile, "storage-file", priv.DefaultStorageFile, "file that persists the storage roots registered at run time (root-owned, mode 0600)")
	fs.BoolVar(&o.poolRoot, "allow-pool-root", false, "allow a pool's top dataset as a storage root")
	fs.StringVar(&o.mountBase, "mount-base", "", "optional: default state-dir is <mount-base>/state; paths are checked against the storage roots' mountpoints")
	fs.StringVar(&o.confDir, "conf-dir", priv.DefaultConfDir, "directory of rendered jail.conf files")
	fs.StringVar(&o.workDir, "work-dir", "", "daemon scratch directory for image builds (optional)")
	fs.StringVar(&o.stateDir, "state-dir", "", "state directory (default <mount-base>/state)")
	fs.StringVar(&o.helperPath, "helper-path", priv.DefaultHelperPath, "path jail.conf hooks call")
	fs.Var(&o.ifPrefix, "if-prefix", "interface name prefix owned by JailOps (default jo)")
	fs.Var(&o.allowUser, "allow-user", "user allowed to connect (jailopsd's user)")
	fs.Var(&o.allowGroup, "allow-group", "group allowed to connect")
	fs.Var(&o.ndpProxy, "ndp-proxy", "an uplink the IPv6 proxy may use and the prefixes it may answer for: <uplink>=<prefix>[,<prefix>...] (repeatable)")
	fs.Var(&o.sysctl, "sysctl", "extra sysctl name readable through the helper")
	fs.Var(&o.fetchHost, "fetch-host", "host fetch may download from over https (default dl-cdn.alpinelinux.org and pkg.freebsd.org)")
	fs.Var(&o.mirror, "image-mirror", "directory fs.read_file may read (a local mirror of image archives)")
	fs.Var(&o.adminRawParam, "admin-raw-param", "jail.conf parameter an admin may set")
	fs.Var(&o.adminAllow, "admin-allow-flag", "allow.* flag an admin may set (without prefix)")
	fs.Var(&o.adminDevfs, "admin-devfs-ruleset", "devfs ruleset number an admin may use")
	fs.Var(&o.adminMounts, "admin-host-mount", "host directory an admin may mount into a jail")
	fs.Var(&o.adminZFSProp, "admin-zfs-prop", "native zfs property an admin may set")
	fs.BoolVar(&o.adminRoutes, "admin-routes", false, "admin may add routes through a gateway")
	fs.BoolVar(&o.adminRawPF, "admin-raw-pf", false, "admin may load raw pf rulesets")
	fs.BoolVar(&o.adminResume, "admin-resume-send", false, "admin may resume a zfs send from a token")
	return fs
}

// loadConfigFile applies "key = value" lines to the flag set.
func loadConfigFile(fs *flag.FlagSet, path string, required bool) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"`)
		if !ok || k == "" || k == "config" {
			return fmt.Errorf("%s:%d: expected key = value", path, n)
		}
		if fs.Lookup(k) == nil {
			return fmt.Errorf("%s:%d: unknown key %q", path, n, k)
		}
		if err := fs.Set(k, v); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}

func (o *options) privConfig() priv.Config {
	c := priv.Config{
		StorageRoots: append([]string(nil), o.storage...), StorageFile: o.storageFile, AllowPoolRoot: o.poolRoot,
		IfPrefixes: o.ifPrefix, MountBase: o.mountBase,
		ConfDir: o.confDir, StateDir: o.stateDir, WorkDir: o.workDir, HelperPath: o.helperPath,
		NDPProxy: o.ndpProxy, Sysctls: o.sysctl, FetchHosts: o.fetchHost, ImageMirrors: o.mirror,
		Admin: priv.AdminPolicy{
			RawJailParams: o.adminRawParam, AllowFlags: o.adminAllow, HostMountRoots: o.adminMounts,
			ZFSProps: o.adminZFSProp, Routes: o.adminRoutes, RawPF: o.adminRawPF, ResumeSend: o.adminResume,
		},
	}
	for _, d := range o.adminDevfs {
		if n, err := strconv.Atoi(d); err == nil {
			c.Admin.DevfsRulesets = append(c.Admin.DevfsRulesets, n)
		}
	}
	if o.rootDS != "" {
		c.StorageRoots = append(c.StorageRoots, o.rootDS)
	}
	return c
}

// newSystem builds the real helper; tests replace it.
var newSystem = func(cfg priv.Config) (*priv.Local, error) { return priv.NewSystem(cfg) }

func run(args []string, stdout, stderr io.Writer, euid int) int {
	// The file agent the helper starts for the daemon needs no configuration and must start fast.
	if len(args) > 0 && args[0] == priv.AgentSubcommand {
		return agentMain(args[1:], stderr, euid)
	}
	if len(args) > 0 && args[0] == "init" {
		return initMain(args[1:], stdout, stderr, euid)
	}
	var o options
	fs := newFlags(&o, stderr)
	// help and the version need no configuration
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help":
			fs.SetOutput(stdout)
			fs.Usage()
			return 0
		case "-version", "--version":
			printVersion(stdout)
			return 0
		}
	}
	// -config is needed before the file can be read.
	for i, a := range args {
		if (a == "-config" || a == "--config") && i+1 < len(args) {
			o.config = args[i+1]
		} else if v, ok := strings.CutPrefix(strings.TrimLeft(a, "-"), "config="); ok && strings.HasPrefix(a, "-") {
			o.config = v
		}
	}
	explicit := o.config != "" && o.config != defaultConfig
	if o.config == "" {
		o.config = defaultConfig
	}
	if err := loadConfigFile(fs, o.config, explicit); err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "version" {
		printVersion(stdout)
		return 0
	}
	cfg := o.privConfig()
	if len(rest) > 0 && rest[0] == "check-config" {
		return checkConfig(&o, cfg, stdout, stderr)
	}
	// the command is checked before root is required
	if len(rest) > 0 && (rest[0] != "hook" || len(rest) != 3) {
		fmt.Fprint(stderr, synopsis)
		return 2
	}
	if euid != 0 && !o.dev {
		fmt.Fprintln(stderr, "jailops-priv: must run as root (use --dev for development)")
		return 1
	}
	if _, err := cfg.Normalize(); err != nil {
		fmt.Fprintf(stderr, "jailops-priv: configuration: %v\n", err)
		return 2
	}
	if !o.dev {
		if err := checkFileSafety(o.config); err != nil {
			fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
			return 2
		}
	}
	if len(rest) == 0 {
		return serve(&o, cfg, stderr)
	}
	return hook(&o, cfg, rest[1], rest[2], stderr)
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "jailops-priv %s (protocol %d)\n", version, priv.ProtocolVersion)
}

func newAuditor(o *options, daemon bool, stderr io.Writer) *priv.Auditor {
	if o.logTo == "syslog" || (o.logTo == "" && daemon) {
		if a, err := priv.NewSyslogAuditor("jailops-priv"); err == nil {
			return a
		} else {
			fmt.Fprintf(stderr, "jailops-priv: syslog unavailable (%v), logging to stderr\n", err)
		}
	}
	return priv.NewAuditor(stderr)
}

func hook(o *options, cfg priv.Config, phase, jail string, stderr io.Writer) int {
	l, err := newSystem(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	aud := newAuditor(o, true, stderr)
	ev := priv.AuditEvent{UID: uint32(os.Geteuid()), PID: int32(os.Getpid()), Op: "hook." + phase, Target: map[string]string{"jail": jail}}
	start := time.Now()
	err = l.RunHook(ctx, phase, jail)
	ev.Dur = time.Since(start)
	if err != nil {
		ev.Outcome, ev.Err = "error", err.Error()
		aud.Log(ev)
		fmt.Fprintf(stderr, "jailops-priv hook %s %s: %v\n", phase, jail, err)
		return 1
	}
	ev.Outcome = "ok"
	aud.Log(ev)
	return 0
}

func resolvePeers(o *options) (priv.PeerPolicy, error) {
	var p priv.PeerPolicy
	for _, name := range o.allowUser {
		u, err := user.Lookup(name)
		if err != nil {
			return p, fmt.Errorf("allow-user %q: %w", name, err)
		}
		n, _ := strconv.ParseUint(u.Uid, 10, 32)
		p.UIDs = append(p.UIDs, uint32(n))
	}
	for _, name := range o.allowGroup {
		g, err := user.LookupGroup(name)
		if err != nil {
			return p, fmt.Errorf("allow-group %q: %w", name, err)
		}
		n, _ := strconv.ParseUint(g.Gid, 10, 32)
		p.GIDs = append(p.GIDs, uint32(n))
	}
	if len(p.UIDs) == 0 && len(p.GIDs) == 0 && o.dev {
		p.UIDs = append(p.UIDs, uint32(os.Geteuid()))
	}
	return p, nil
}

// listen creates the socket with mode and group set before anyone can connect.
func listen(path, group string, mode os.FileMode, dev bool) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o751); err != nil {
		return nil, err
	}
	gid := -1
	if group != "" {
		if g, err := user.LookupGroup(group); err == nil {
			n, _ := strconv.Atoi(g.Gid)
			gid = n
		} else if !dev {
			return nil, fmt.Errorf("group %q: %w", group, err)
		}
	}
	if gid >= 0 {
		// The directory also holds the daemon's own socket (jailopsd runs as a member of the group and makes it
		// here), so: group may create (the sticky bit keeps it from touching the helper's files), everybody may
		// pass through (the sockets decide who may connect; this one is group only).
		_ = os.Chown(dir, -1, gid)
		_ = os.Chmod(dir, os.ModeSticky|0o771)
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another jailops-priv is listening on %s", path)
		}
		os.Remove(path)
	}
	old := syscall.Umask(0o117)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if gid >= 0 {
		if err := os.Chown(path, -1, gid); err != nil && !dev {
			ln.Close()
			return nil, err
		}
	}
	if err := os.Chmod(path, mode); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func serve(o *options, cfg priv.Config, stderr io.Writer) int {
	if o.daemon && os.Getenv("JAILOPS_PRIV_CHILD") == "" {
		return detach(stderr)
	}
	mode, err := strconv.ParseUint(o.mode, 8, 32)
	if err != nil || mode&^0o777 != 0 || mode&0o007 != 0 {
		fmt.Fprintf(stderr, "jailops-priv: invalid socket mode %q (others must have no access)\n", o.mode)
		return 2
	}
	peers, err := resolvePeers(o)
	if err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 2
	}
	if len(peers.UIDs) == 0 && len(peers.GIDs) == 0 {
		fmt.Fprintln(stderr, "jailops-priv: no allowed peer: set allow-user (jailopsd's user) or allow-group")
		return 2
	}
	l, err := newSystem(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 2
	}
	aud := newAuditor(o, o.daemon, stderr)
	srv := priv.NewServer(l, priv.ServerOptions{Peers: peers, Audit: aud, Version: version})
	ln, err := listen(o.socket, o.group, os.FileMode(mode), o.dev)
	if err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 1
	}
	if o.pidfile != "" {
		if err := os.WriteFile(o.pidfile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			fmt.Fprintf(stderr, "jailops-priv: pidfile: %v\n", err)
			ln.Close()
			return 1
		}
		defer os.Remove(o.pidfile)
	}
	defer os.Remove(o.socket)
	aud.Note("priv.start", "version", version, "socket", o.socket, "storage_roots", strings.Join(l.Config().Roots(), ","))
	noteRoots(aud, l)

	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	signal.Ignore(syscall.SIGPIPE)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	for {
		select {
		case s := <-sig:
			if s == syscall.SIGHUP {
				if err := l.ReloadRoots(context.Background()); err != nil {
					aud.Note("priv.reload_failed", "error", err.Error())
				} else {
					aud.Note("priv.reload", "storage_roots", strings.Join(l.Config().Roots(), ","))
					noteRoots(aud, l)
				}
				continue
			}
			aud.Note("priv.stop", "signal", s.String())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			<-done
			return 0
		case err := <-done:
			if err != nil {
				fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
				return 1
			}
			return 0
		}
	}
}

// noteRoots logs every storage root that is configured but not usable.
func noteRoots(aud *priv.Auditor, l *priv.Local) {
	for _, r := range l.RootStatuses() {
		if !r.Active {
			aud.Note("priv.storage_root_inactive", "dataset", r.Dataset, "error", r.Error)
		}
	}
}

// checkFileSafety refuses a configuration file others can write.
func checkFileSafety(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return nil // a missing default file is not an error here
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != 0 && int(sys.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is owned by uid %d", path, sys.Uid)
	}
	return nil
}

// detach re-executes the program in a new session with no terminal.
func detach(stderr io.Writer) int {
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = append(os.Environ(), "JAILOPS_PRIV_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 1
	}
	return 0
}

// checkConfig validates the configuration and prints what it amounts to:
// the effective storage roots (with the reason a root is inactive), the
// interface prefixes, directories, admin policy and every allow-list. It
// exits 2 when the configuration or the storage file is invalid; inactive
// roots are warnings (a pool may not be imported yet).
func checkConfig(o *options, cfg priv.Config, stdout, stderr io.Writer) int {
	if _, err := cfg.Normalize(); err != nil {
		fmt.Fprintf(stderr, "jailops-priv: configuration: %v\n", err)
		return 2
	}
	if err := checkFileSafety(o.config); err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 2
	}
	l, err := newSystem(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "jailops-priv: %v\n", err)
		return 2
	}
	c := l.Config()
	w := func(f string, a ...any) { fmt.Fprintf(stdout, f, a...) }
	w("config file:    %s\n", o.config)
	w("storage file:   %s\n", c.StorageFile)
	w("conf dir:       %s\n", c.ConfDir)
	w("state dir:      %s\n", c.StateDir)
	w("helper path:    %s\n", c.HelperPath)
	w("interfaces:     %s\n", strings.Join(c.IfPrefixes, ", "))
	for _, u := range c.NDPProxy {
		var ps []string
		for _, p := range u.Prefixes {
			ps = append(ps, p.String())
		}
		w("ipv6 proxy:     %s answers for %s\n", u.Uplink, strings.Join(ps, ", "))
	}
	w("storage roots:\n")
	warn := 0
	if st := l.RootStatuses(); len(st) == 0 {
		w("  (none; register one with storage.register)\n")
	} else {
		for _, r := range st {
			w("  %s\n", r)
			if !r.Active {
				warn++
			}
		}
	}
	w("admin policy:\n")
	w("  raw jail params: %s\n", strings.Join(c.Admin.RawJailParams, ", "))
	w("  allow flags:     %s\n", strings.Join(c.Admin.AllowFlags, ", "))
	w("  devfs rulesets:  %v\n", c.Admin.DevfsRulesets)
	w("  host mounts:     %s\n", strings.Join(c.Admin.HostMountRoots, ", "))
	w("  zfs properties:  %s\n", strings.Join(c.Admin.ZFSProps, ", "))
	w("  routes: %v  raw pf: %v  resume send: %v\n", c.Admin.Routes, c.Admin.RawPF, c.Admin.ResumeSend)
	w("commands (fixed binaries):\n")
	for _, r := range priv.CommandRules {
		w("  %-15s %-26s %s\n", r.Name, r.Binary, r.Reason)
	}
	w("fetch hosts:    %s\n", strings.Join(c.EffectiveFetchHosts(), ", "))
	w("image mirrors:  %s\n", strings.Join(c.ImageMirrors, ", "))
	w("readable files: %s\n", strings.Join(priv.ReadOnlyFiles, ", "))
	w("kernel modules: %s\n", strings.Join(priv.KldModules, ", "))
	var reads, writes []string
	reads = append(append(reads, priv.DefaultSysctls...), c.Sysctls...)
	for n := range priv.SysctlWrites {
		writes = append(writes, n)
	}
	sort.Strings(reads)
	sort.Strings(writes)
	w("sysctl read:    %s\n", strings.Join(reads, ", "))
	w("sysctl write (admin only): %s\n", strings.Join(writes, ", "))
	if warn > 0 {
		fmt.Fprintf(stderr, "jailops-priv: warning: %d storage root(s) inactive\n", warn)
	}
	return 0
}
