package priv

import (
	"context"
	"net/netip"
	"path/filepath"
	"time"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
)

// ---- jails ----

type jailGuard struct{ l *Local }

var _ jailos.Jails = jailGuard{}

// confPathFor checks that confPath is the jail.conf of name inside ConfDir,
// re-validates its content (a file that was written by something else is not
// trusted) and returns the path to pass on.
func (g jailGuard) confPathFor(ctx context.Context, confPath, name string) (string, error) {
	if err := ValidJailName(name); err != nil {
		return "", err
	}
	if !filepath.IsAbs(confPath) || filepath.Clean(confPath) != confPath {
		return "", invalid("invalid configuration path %q", confPath)
	}
	if confPath != filepath.Join(g.l.cfg.ConfDir, name+".conf") && confPath != filepath.Join(g.l.cfg.ConfDir, name, "jail.conf") {
		return "", forbidden("configuration path %q is not the jail.conf of %s", confPath, name)
	}
	b, err := g.l.ReadFile(ctx, confPath)
	if err != nil {
		return "", err
	}
	// Admin-only parameters were allowed when the file was written; the
	// allow-lists still apply.
	rc := WithAdmin(ctx, "recheck")
	if err := g.l.cfg.checkJailConf(rc, name, string(b)); err != nil {
		return "", err
	}
	// The fstab is read by mount(8) on the host when the jail starts: recheck it against the operator's lists and
	// against symbolic links now, with the jail stopped, as the file may have been written long ago.
	if fp, ok := confParam(string(b), "mount.fstab"); ok {
		ft, err := g.l.ReadFile(ctx, fp)
		if err != nil {
			return "", err
		}
		if err := g.l.cfg.checkFstab(rc, string(ft), string(ft)); err != nil {
			return "", err
		}
		root, _ := confParam(string(b), "path")
		if err := g.l.cfg.checkFstabPaths(string(ft), root); err != nil {
			return "", err
		}
	}
	return confPath, nil
}

// managed reports whether name is a jail JailOps configured.
func (g jailGuard) managed(name string) error {
	if err := ValidJailName(name); err != nil {
		return err
	}
	if _, ok := g.l.confFileFor(name); !ok {
		return forbidden("jail %q is not managed by JailOps", name)
	}
	return nil
}

func (g jailGuard) List(ctx context.Context) ([]jailos.Jail, error) { return g.l.d.Jails.List(ctx) }

func (g jailGuard) Get(ctx context.Context, name string) (jailos.Jail, bool, error) {
	return g.l.d.Jails.Get(ctx, name)
}

func (g jailGuard) Create(ctx context.Context, confPath, name string) error {
	p, err := g.confPathFor(ctx, confPath, name)
	if err != nil {
		return err
	}
	return g.l.d.Jails.Create(ctx, p, name)
}

func (g jailGuard) Remove(ctx context.Context, confPath, name string) error {
	p, err := g.confPathFor(ctx, confPath, name)
	if err != nil {
		return err
	}
	return g.l.d.Jails.Remove(ctx, p, name)
}

func (g jailGuard) Exec(ctx context.Context, name string, o jailos.ExecOpts) (run.Result, error) {
	if err := g.managed(name); err != nil {
		return run.Result{}, err
	}
	c := g.l.cfg
	if len(o.Argv) == 0 || len(o.Argv) > c.Limits.MaxArgv {
		return run.Result{}, invalid("expected between 1 and %d arguments", c.Limits.MaxArgv)
	}
	total := 0
	for _, a := range o.Argv {
		total += len(a) + 1
		if containsNUL(a) {
			return run.Result{}, invalid("argument contains a NUL byte")
		}
	}
	if total > c.Limits.MaxArgvBytes {
		return run.Result{}, invalid("command line too long")
	}
	if o.Timeout < 0 || o.Timeout > c.Limits.MaxStreamTimeout {
		return run.Result{}, invalid("invalid timeout")
	}
	return g.l.d.Jails.Exec(ctx, name, o)
}

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// ---- pf ----

type pfGuard struct{ l *Local }

var _ pfctl.Client = pfGuard{}

func (g pfGuard) Validate(ctx context.Context, anchor, rs string) (string, error) {
	if err := checkAnchor(anchor); err != nil {
		return "", err
	}
	if err := g.l.cfg.checkRuleset(ctx, rs); err != nil {
		return "", err
	}
	return g.l.d.PF.Validate(ctx, anchor, rs)
}

func (g pfGuard) Load(ctx context.Context, anchor, rs string) error {
	if err := checkAnchor(anchor); err != nil {
		return err
	}
	if err := g.l.cfg.checkRuleset(ctx, rs); err != nil {
		return err
	}
	return g.l.d.PF.Load(ctx, anchor, rs)
}

func (g pfGuard) Read(ctx context.Context, anchor string) (string, error) {
	if err := checkAnchor(anchor); err != nil {
		return "", err
	}
	return g.l.d.PF.Read(ctx, anchor)
}

func (g pfGuard) Flush(ctx context.Context, anchor string) error {
	if err := checkAnchor(anchor); err != nil {
		return err
	}
	return g.l.d.PF.Flush(ctx, anchor)
}

func (g pfGuard) table(anchor, table string, addrs []string) error {
	if err := checkAnchor(anchor); err != nil {
		return err
	}
	if err := pfctl.ValidateTable(table); err != nil {
		return err
	}
	if len(addrs) > g.l.cfg.Limits.MaxNames*16 {
		return invalid("too many addresses")
	}
	return nil
}

func (g pfGuard) TableAdd(ctx context.Context, anchor, table string, addrs ...string) error {
	if err := g.table(anchor, table, addrs); err != nil {
		return err
	}
	return g.l.d.PF.TableAdd(ctx, anchor, table, addrs...)
}

func (g pfGuard) TableDelete(ctx context.Context, anchor, table string, addrs ...string) error {
	if err := g.table(anchor, table, addrs); err != nil {
		return err
	}
	return g.l.d.PF.TableDelete(ctx, anchor, table, addrs...)
}

// KillLabel kills the states of rules JailOps labelled. Only its own labels are accepted: the daemon cannot name an
// address, an interface or another rule, so it can cut off the flows of a policy it changed and nothing else.
func (g pfGuard) KillLabel(ctx context.Context, label string) error {
	if err := pfctl.ValidateLabel(label); err != nil {
		return err
	}
	return g.l.d.PF.KillLabel(ctx, label)
}

func (g pfGuard) TableShow(ctx context.Context, anchor, table string) ([]string, error) {
	if err := g.table(anchor, table, nil); err != nil {
		return nil, err
	}
	return g.l.d.PF.TableShow(ctx, anchor, table)
}

// ---- interfaces ----

type netGuard struct{ l *Local }

var _ ifconfig.Client = netGuard{}

func (g netGuard) owned(names ...string) error {
	for _, n := range names {
		if err := g.l.cfg.ifOwned(n); err != nil {
			return err
		}
	}
	return nil
}

func (g netGuard) List(ctx context.Context) ([]ifconfig.Iface, error) { return g.l.d.Net.List(ctx) }

func (g netGuard) BridgeCreate(ctx context.Context, name string) error {
	if err := g.owned(name); err != nil {
		return err
	}
	return g.l.d.Net.BridgeCreate(ctx, name)
}

func (g netGuard) BridgeDestroy(ctx context.Context, name string) error {
	if err := g.owned(name); err != nil {
		return err
	}
	return g.l.d.Net.BridgeDestroy(ctx, name)
}

func (g netGuard) BridgeAddMember(ctx context.Context, bridge, member string, private bool) error {
	if err := g.owned(bridge, member); err != nil {
		return err
	}
	return g.l.d.Net.BridgeAddMember(ctx, bridge, member, private)
}

func (g netGuard) BridgeDeleteMember(ctx context.Context, bridge, member string) error {
	if err := g.owned(bridge, member); err != nil {
		return err
	}
	return g.l.d.Net.BridgeDeleteMember(ctx, bridge, member)
}

func (g netGuard) SetDescription(ctx context.Context, iface, desc string) error {
	if err := g.owned(iface); err != nil {
		return err
	}
	if err := ifconfig.ValidateDescription(desc); err != nil {
		return err
	}
	return g.l.d.Net.SetDescription(ctx, iface, desc)
}

func (g netGuard) EpairCreate(ctx context.Context, name string) (string, string, error) {
	if err := g.owned(name + "a"); err != nil {
		return "", "", err
	}
	if err := ifconfig.ValidateIfName(name + "b"); err != nil {
		return "", "", err
	}
	return g.l.d.Net.EpairCreate(ctx, name)
}

func (g netGuard) EpairDestroy(ctx context.Context, iface string) error {
	if err := g.owned(iface); err != nil {
		return err
	}
	return g.l.d.Net.EpairDestroy(ctx, iface)
}

func (g netGuard) move(iface, jail string) error {
	if err := g.owned(iface); err != nil {
		return err
	}
	return jailGuard{g.l}.managed(jail)
}

func (g netGuard) MoveToJail(ctx context.Context, iface, jail string) error {
	if err := g.move(iface, jail); err != nil {
		return err
	}
	return g.l.d.Net.MoveToJail(ctx, iface, jail)
}

func (g netGuard) MoveFromJail(ctx context.Context, iface, jail string) error {
	if err := g.move(iface, jail); err != nil {
		return err
	}
	return g.l.d.Net.MoveFromJail(ctx, iface, jail)
}

func (g netGuard) SetLink(ctx context.Context, iface string, up bool) error {
	if err := g.owned(iface); err != nil {
		return err
	}
	return g.l.d.Net.SetLink(ctx, iface, up)
}

func (g netGuard) addr(iface string, p netip.Prefix) error {
	if err := g.owned(iface); err != nil {
		return err
	}
	if !p.IsValid() || p.Addr().Zone() != "" {
		return invalid("invalid address %q", p)
	}
	return nil
}

func (g netGuard) AddrAdd(ctx context.Context, iface string, p netip.Prefix) error {
	if err := g.addr(iface, p); err != nil {
		return err
	}
	return g.l.d.Net.AddrAdd(ctx, iface, p)
}

func (g netGuard) AddrRemove(ctx context.Context, iface string, p netip.Prefix) error {
	if err := g.addr(iface, p); err != nil {
		return err
	}
	return g.l.d.Net.AddrRemove(ctx, iface, p)
}

func (g netGuard) RouteAdd(ctx context.Context, r ifconfig.Route) error {
	if err := g.l.cfg.checkRoute(ctx, r); err != nil {
		return err
	}
	return g.l.d.Net.RouteAdd(ctx, r)
}

func (g netGuard) RouteDelete(ctx context.Context, r ifconfig.Route) error {
	if err := g.l.cfg.checkRoute(ctx, r); err != nil {
		return err
	}
	return g.l.d.Net.RouteDelete(ctx, r)
}

func (g netGuard) NeighborSet(ctx context.Context, iface string, a netip.Addr, mac string) error {
	if err := g.l.cfg.ifOwned(iface); err != nil {
		return err
	}
	return g.l.d.Net.NeighborSet(ctx, iface, a, mac)
}

func (g netGuard) NeighborDelete(ctx context.Context, iface string, a netip.Addr) error {
	if err := g.l.cfg.ifOwned(iface); err != nil {
		return err
	}
	return g.l.d.Net.NeighborDelete(ctx, iface, a)
}

func (g netGuard) IPv6Forwarding(ctx context.Context) (bool, error) {
	return g.l.d.Net.IPv6Forwarding(ctx)
}

// ---- runner ----

type runGuard struct{ l *Local }

var _ run.Runner = runGuard{}

// Run runs a command from CommandRules. The caller's Env and Dir are refused,
// the binary is the rule's fixed path and the timeout is capped.
func (g runGuard) Run(ctx context.Context, c run.Cmd) (run.Result, error) {
	if c.Env != nil || c.Dir != "" {
		return run.Result{}, invalid("env and dir cannot be set")
	}
	rule, argv, err := g.l.cfg.CheckCommandCtx(ctx, append([]string{c.Name}, c.Args...))
	if err != nil {
		return run.Result{}, err
	}
	if rule.Name == "pkg" {
		if d := reposDirOf(argv[1:]); d != "" {
			defer g.l.freeze(d)()
		}
	}
	if rule.Verify != nil {
		if err := rule.Verify(ctx, g.l, argv[1:]); err != nil {
			return run.Result{}, err
		}
	}
	max := time.Duration(rule.MaxTimeoutSeconds) * time.Second
	if c.Timeout == 0 || c.Timeout == run.NoTimeout || c.Timeout > max {
		c.Timeout = max
	}
	const maxOut = 64 << 20
	if c.MaxOutput <= 0 || c.MaxOutput > maxOut {
		c.MaxOutput = run.DefaultMaxStdout
	}
	c.Name, c.Args = argv[0], argv[1:]
	if rule.Env != nil {
		c.Env = rule.Env
	}
	return g.l.d.Runner.Run(ctx, c)
}
