package reconcile

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Config tunes the loop.
type Config struct {
	// Interval is the period of the timer trigger (default 60s).
	Interval time.Duration
	// Debounce coalesces bursts of triggers (default 250ms).
	Debounce time.Duration
	// Autostart starts jails with boot.autostart on the first pass.
	Autostart bool
	// NDP builds the neighbor discovery proxies of networks in IPv6 proxy mode; without it those networks report
	// that no proxy is available.
	NDP NDPFactory
}

// Env is everything the loop talks to.
type Env struct {
	Source  Source
	Sink    Sink
	Actions Actions
	Net     ifconfig.Client
	PF      pfctl.Client
	Jails   jailos.Jails
	Run     run.Runner
	// Sysctl reads sysctl values (optional; the runner is used otherwise).
	Sysctl func(ctx context.Context, name string) (string, error)
	Clock  Clock
	Log    *slog.Logger
	// HostRoutes reads the IPv6 /128 host routes (address to interface). Optional: without it stale routes of
	// proxy-mode networks are not collected.
	HostRoutes func(ctx context.Context) (map[netip.Addr]string, error)
	// HostNeighbors reads the permanent link-local neighbor entries. Optional: without it stale entries are not collected.
	HostNeighbors func(ctx context.Context) ([]ifconfig.Neighbor, error)
	// Trigger is an optional extra trigger (state watch events).
	Trigger <-chan struct{}
}

// Report summarises one pass; tests and `doctor` read it.
type Report struct {
	At      time.Time
	Err     error
	Applied []string            // steps executed
	Drift   map[string][]string // by jail
	Orphans []string            // epairs found without a jail
	Cleaned []string            // orphans removed
	// StaleRoutes are the /128 host routes of proxy-mode networks that belonged to no jail and were removed.
	StaleRoutes []string
	// StaleNeighbors are the link-local static neighbor entries of jails that are gone, removed.
	StaleNeighbors []string
	Started        []string          // autostarted jails
	Failures       map[string]string // resource -> reason
}

// Reconciler is the loop.
type Reconciler struct {
	cfg Config
	env Env
	log *slog.Logger

	passMu sync.Mutex
	passes int
	kick   chan struct{}

	mu       sync.Mutex
	last     Report
	bootDone bool
	bootCond map[string]api.Condition
	proxies  map[string]*proxyHandle
	// setSysctl records the sysctls this daemon changed, for Release.
	setSysctl map[string]bool
}

// New builds a Reconciler.
func New(cfg Config, env Env) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 250 * time.Millisecond
	}
	if env.Clock == nil {
		env.Clock = SystemClock{}
	}
	if env.Log == nil {
		env.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Reconciler{cfg: cfg, env: env, log: env.Log, kick: make(chan struct{}, 1),
		bootCond: map[string]api.Condition{}, proxies: map[string]*proxyHandle{}, setSysctl: map[string]bool{}}
}

// Kick asks for a pass soon. It never blocks.
func (r *Reconciler) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Last returns the report of the latest pass.
func (r *Reconciler) Last() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Run loops until ctx ends.
func (r *Reconciler) Run(ctx context.Context) error {
	defer r.stopProxies()
	for {
		r.Pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-r.kick:
		case <-r.env.Trigger:
		case <-r.env.Clock.After(r.cfg.Interval):
			continue
		}
		// Debounce: let a burst settle, then drain what arrived meanwhile.
		select {
		case <-ctx.Done():
			return nil
		case <-r.env.Clock.After(r.cfg.Debounce):
		}
		select {
		case <-r.kick:
		default:
		}
	}
}

// Pass runs one observe, render, plan, apply, verify cycle.
func (r *Reconciler) Pass(ctx context.Context) Report {
	r.passMu.Lock()
	defer r.passMu.Unlock()
	r.passes++
	now := r.env.Clock.Now()
	rep := Report{At: now, Drift: map[string][]string{}, Failures: map[string]string{}}
	defer func() {
		r.mu.Lock()
		r.last = rep
		r.mu.Unlock()
	}()
	des, err := r.env.Source.Desired(ctx)
	if err != nil {
		rep.Err = err
		r.log.Warn("reconcile: desired state unavailable", "err", err)
		return rep
	}
	r.hostPass(ctx, des.Host, &rep)
	obs, err := Observe(ctx, r.env.Net, r.env.Jails)
	if err != nil {
		rep.Err = err
		r.log.Warn("reconcile: observation failed", "err", err)
		return rep
	}
	anchors := map[string]string{}
	for _, n := range des.Networks {
		for k, v := range n.Anchors {
			anchors[k] = v
		}
	}
	ex := &NetExec{Net: r.env.Net, PF: r.env.PF, Run: r.env.Run, Jail: r.env.Jails, Sysctl: r.env.Sysctl, Anchors: anchors}

	netStatus := map[string]*api.NetworkStatus{}
	for _, n := range des.Networks {
		st := r.networkPass(ctx, n, obs, ex, &rep, now)
		netStatus[n.Name] = st
	}
	// Networks changed interfaces; look again before judging jails.
	if len(rep.Applied) > 0 {
		if o2, err := Observe(ctx, r.env.Net, r.env.Jails); err == nil {
			obs = o2
		}
	}
	jailConds := map[string][]api.Condition{}
	for _, j := range des.Jails {
		jailConds[j.Name] = r.jailPass(ctx, j, obs, ex, &rep, now)
	}
	r.orphanPass(ctx, des, obs, ex, netStatus, &rep)
	r.staleRoutePass(ctx, des, obs, ex, &rep)
	if r.cfg.Autostart && !r.bootDone {
		r.bootPass(ctx, des, obs, &rep, now)
		r.bootDone = true
	}
	r.proxyPass(ctx, des, obs, netStatus, &rep, now)
	for _, n := range des.Networks {
		if st := netStatus[n.Name]; st != nil && r.env.Sink != nil {
			r.env.Sink.NetworkStatus(n.Name, *st)
		}
	}
	if r.env.Sink != nil {
		r.mu.Lock()
		for _, j := range des.Jails {
			conds := jailConds[j.Name]
			if c, ok := r.bootCond[j.Name]; ok {
				conds = append(conds, c)
			}
			r.env.Sink.JailStatus(j.Name, conds, rep.Drift[j.Name])
		}
		r.mu.Unlock()
	}
	return rep
}

// ---- networks ----

func (r *Reconciler) networkPass(ctx context.Context, n NetworkDesired, obs *Observed, ex *NetExec, rep *Report, now time.Time) *api.NetworkStatus {
	st := &api.NetworkStatus{Allocated: n.Allocated, Free: n.Free}
	fail := func(reason, msg string) *api.NetworkStatus {
		st.Conditions = append(st.Conditions, cond(CondReady, false, reason, msg, now))
		rep.Failures["network/"+n.Name] = msg
		return st
	}
	if n.Problem != "" {
		return fail("Invalid", n.Problem)
	}
	gwMember, gwL3, _ := ipam.GatewayNames(n.Bridge)
	for _, name := range []string{n.Bridge, gwMember, gwL3} {
		if i, ok := obs.Ifaces[name]; ok && name != "" && !owned(i, n.ID) {
			if i.Description == "" || !ours(i) {
				return fail("ForeignBridge", fmt.Sprintf("interface %s exists and is not managed by JailOps; it is left untouched", name))
			}
			return fail("ForeignBridge", fmt.Sprintf("interface %s belongs to another JailOps object (%s)", name, i.Description))
		}
	}
	unlock, err := r.env.Actions.TryLock("reconcile "+n.Name, "network/"+n.Name)
	if err != nil {
		// An operation is changing this network; keep what we reported before.
		return nil
	}
	defer unlock()
	for _, step := range n.Steps {
		if Satisfied(step, obs) {
			continue
		}
		// The network's anchor is loaded only when what is live differs from what is rendered.
		if step.Op == render.OpLoadAnchor && r.anchorDrift(ctx, JailDesired{AnchorName: step.Arg, Ruleset: n.Anchors[step.Arg]}) == "" {
			continue
		}
		if step.Op == render.OpRequireSysctl {
			// A precondition, checked every pass; it changes nothing.
			if err := ex.Exec(ctx, step, ""); err != nil {
				return r.networkStatusFrom(n, obs, st, fail("Precondition", err.Error()), ctx, now)
			}
			continue
		}
		if err := ex.Exec(ctx, step, ""); err != nil {
			rep.Applied = append(rep.Applied, step.Describe()+" (failed)")
			return r.networkStatusFrom(n, obs, st, fail("ApplyFailed", fmt.Sprintf("%s: %v", step.Describe(), err)), ctx, now)
		}
		rep.Applied = append(rep.Applied, step.Describe())
	}
	// Remove addresses on our own interfaces that the spec no longer asks for. The bridge itself carries none (an
	// older layout put the gateway addresses on it, and a stray address there would be answered outside the
	// gateway's pf rules).
	for _, name := range []string{n.Bridge, gwL3} {
		i, ok := obs.Ifaces[name]
		if !ok || !owned(i, n.ID) {
			continue
		}
		want := map[netip.Prefix]bool{}
		for _, s := range n.Steps {
			if s.Op == render.OpAddAddr && s.Iface == name {
				if p, err := netip.ParsePrefix(s.Arg); err == nil {
					want[p] = true
				}
			}
		}
		for _, list := range [][]netip.Prefix{i.Inet, i.Inet6} {
			for _, p := range list {
				if want[p] || p.Addr().IsLinkLocalUnicast() || p.Addr().IsLoopback() {
					continue
				}
				s := render.Step{Op: render.OpRemoveAddr, Phase: render.PhaseHost, Iface: name, Arg: p.String()}
				if err := ex.Exec(ctx, s, ""); err != nil {
					rep.Failures["network/"+n.Name] = err.Error()
					continue
				}
				rep.Applied = append(rep.Applied, s.Describe())
			}
		}
	}
	return r.networkStatusFrom(n, obs, st, nil, ctx, now)
}

// networkStatusFrom verifies the network against a fresh observation.
func (r *Reconciler) networkStatusFrom(n NetworkDesired, obs *Observed, st, failed *api.NetworkStatus, ctx context.Context, now time.Time) *api.NetworkStatus {
	if failed != nil {
		st = failed
	}
	fresh := obs
	if o2, err := Observe(ctx, r.env.Net, nil); err == nil {
		fresh = &Observed{Ifaces: o2.Ifaces, Running: obs.Running}
	}
	if i, ok := fresh.Ifaces[n.Bridge]; ok {
		st.BridgeUp = i.Up()
		gm, _, _ := ipam.GatewayNames(n.Bridge)
		for _, m := range i.Members {
			if m.Name != gm { // the host's own port is not a jail
				st.Members = append(st.Members, m.Name)
			}
		}
		slices.Sort(st.Members)
	}
	if failed != nil {
		return st
	}
	for _, s := range n.Steps {
		if s.Op == render.OpRequireSysctl || s.Op == render.OpLoadAnchor || s.Op == render.OpAddHostRoute {
			continue
		}
		if !Satisfied(s, fresh) {
			st.Conditions = append(st.Conditions, cond(CondReady, false, "VerifyFailed", "not in effect after apply: "+s.Describe(), now))
			return st
		}
	}
	st.Conditions = append(st.Conditions, cond(CondReady, true, "Applied", "bridge "+n.Bridge+" with its gateway interface is configured", now))
	return st
}

// ---- jails ----

func (r *Reconciler) jailPass(ctx context.Context, j JailDesired, obs *Observed, ex *NetExec, rep *Report, now time.Time) []api.Condition {
	if j.Problem != "" {
		rep.Failures["jail/"+j.Name] = j.Problem
		return []api.Condition{cond(CondReady, false, "Invalid", j.Problem, now)}
	}
	if _, up := obs.Running[j.Name]; !up {
		return []api.Condition{cond(CondReady, true, "Stopped", "the jail is stopped", now)}
	}
	var drift []string
	// Interfaces: the host end of every epair must exist and be a bridge member.
	for idx, ep := range j.Epairs {
		i, ok := obs.Ifaces[ep]
		if !ok {
			drift = append(drift, fmt.Sprintf("epair %s is missing; restart the jail to recreate it", ep))
			continue
		}
		if idx < len(j.Attach) {
			for _, s := range j.Attach[idx] {
				switch s.Op {
				case render.OpAddMember, render.OpSetPrivate, render.OpIfaceUp, render.OpSetDescription:
				default:
					continue
				}
				if Satisfied(s, obs) {
					continue
				}
				unlock, err := r.env.Actions.TryLock("reconcile "+j.Name, "jail/"+j.Name)
				if err != nil {
					break
				}
				if err := ex.Exec(ctx, s, j.Name); err != nil {
					drift = append(drift, fmt.Sprintf("%s: %v", s.Describe(), err))
				} else {
					rep.Applied = append(rep.Applied, s.Describe())
				}
				unlock()
			}
		}
		_ = i
	}
	// Firewall anchor, and the parent anchors that lead to it.
	if j.Ruleset != "" && r.env.PF != nil {
		for _, an := range sortedAnchors(j.ParentAnchors) {
			pj := JailDesired{Name: j.Name, AnchorName: an, Ruleset: j.ParentAnchors[an]}
			if r.anchorDrift(ctx, pj) == "" {
				continue
			}
			unlock, err := r.env.Actions.TryLock("reconcile "+an, "anchor/_parent/"+an)
			if err != nil {
				continue
			}
			if err := r.env.PF.Load(ctx, an, pj.Ruleset); err != nil {
				drift = append(drift, fmt.Sprintf("parent anchor %s: %v", an, err))
			} else if d := r.verifyLoaded(ctx, pj); d != "" {
				drift = append(drift, d)
			} else {
				rep.Applied = append(rep.Applied, "pfctl -a "+an+" -f <rendered>")
			}
			unlock()
		}
		if d := r.anchorDrift(ctx, j); d != "" {
			unlock, err := r.env.Actions.TryLock("reconcile "+j.Name, "anchor/"+j.Name)
			if err == nil {
				if err := r.loadAnchor(ctx, j.AnchorName, j.Ruleset); err != nil {
					drift = append(drift, fmt.Sprintf("firewall anchor %s: %v", j.AnchorName, err))
				} else if d := r.verifyLoaded(ctx, j); d != "" {
					drift = append(drift, d)
				} else {
					rep.Applied = append(rep.Applied, "pfctl -a "+j.AnchorName+" -f <rendered>")
				}
				unlock()
			}
		}
	}
	if len(drift) > 0 {
		rep.Drift[j.Name] = drift
		return []api.Condition{cond(CondReady, true, "Running", "the jail is running", now),
			cond(CondDrift, false, "Drift", strings.Join(drift, "; "), now)}
	}
	return []api.Condition{cond(CondReady, true, "Running", "the jail is running", now), cond(CondDrift, true, "InSync", "rendered and live state agree", now)}
}

// verifyLoaded reads the anchor back (pfctl -a <anchor> -sr) after a load and
// checks that the rules the loop loaded are present. A load that pfctl
// accepted but that did not take effect is drift, not success.
func (r *Reconciler) verifyLoaded(ctx context.Context, j JailDesired) string {
	if d := r.anchorDrift(ctx, j); d != "" {
		return fmt.Sprintf("anchor %s: after loading, %s", j.AnchorName, d)
	}
	return ""
}

func sortedAnchors(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// anchorDrift returns a description when the loaded anchor differs from the
// rendered ruleset, "" when they agree.
func (r *Reconciler) anchorDrift(ctx context.Context, j JailDesired) string {
	norm, err := r.env.PF.Validate(ctx, j.AnchorName, j.Ruleset)
	if err != nil {
		return "rendered ruleset does not validate: " + err.Error()
	}
	live, err := r.env.PF.Read(ctx, j.AnchorName)
	if err != nil {
		return "anchor cannot be read: " + err.Error()
	}
	if !slices.Equal(pfctl.RuleLines(norm), pfctl.RuleLines(live)) {
		return "loaded rules differ from the rendered ruleset"
	}
	// Rule lines name tables but do not show their members: compare those too.
	for name, want := range tableDefs(j.Ruleset) {
		got, ok := r.liveTable(ctx, j.AnchorName, name, live)
		if ok && !slices.Equal(want, got) {
			return fmt.Sprintf("table <%s> differs from the rendered address set", name)
		}
	}
	return ""
}

// liveTable returns the members of a table of the anchor: from pfctl when it
// knows the table, else from the table definition in the anchor text.
func (r *Reconciler) liveTable(ctx context.Context, anchor, name, anchorText string) ([]string, bool) {
	if m, err := r.env.PF.TableShow(ctx, anchor, name); err == nil {
		out := normMembers(m)
		return out, true
	}
	if d, ok := tableDefs(anchorText)[name]; ok {
		return d, true
	}
	return nil, false
}

var tableRe = regexp.MustCompile(`table\s+<([A-Za-z][A-Za-z0-9_]*)>[^{\n]*\{([^}]*)\}`)

// tableDefs extracts "table <name> ... { members }" definitions.
func tableDefs(text string) map[string][]string {
	out := map[string][]string{}
	for _, m := range tableRe.FindAllStringSubmatch(text, -1) {
		out[m[1]] = normMembers(strings.FieldsFunc(m[2], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }))
	}
	return out
}

func normMembers(in []string) []string {
	out := make([]string, 0, len(in))
	for _, x := range in {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ---- orphans ----

func (r *Reconciler) orphanPass(ctx context.Context, des Desired, obs *Observed, ex *NetExec, netStatus map[string]*api.NetworkStatus, rep *Report) {
	byID := map[string]JailDesired{}
	for _, j := range des.Jails {
		byID[j.ID] = j
	}
	bridgeNet := map[string]string{}
	for _, n := range des.Networks {
		bridgeNet[n.Bridge] = n.Name
	}
	for _, name := range sortedIfaces(obs) {
		i := obs.Ifaces[name]
		if !ipam.IsEpairName(name) || !ours(i) || !strings.HasSuffix(name, "a") {
			continue
		}
		id := strings.TrimPrefix(i.Description, "jailops:")
		j, known := byID[id]
		orphan := !known
		if known {
			if _, up := obs.Running[j.Name]; !up && !r.env.Actions.IsLocked("jail/"+j.Name) {
				orphan = true // left over from a stopped jail
			}
		}
		if !orphan {
			continue
		}
		rep.Orphans = append(rep.Orphans, name)
		for bn, b := range bridgeNet {
			if bi, ok := obs.Ifaces[bn]; ok {
				for _, m := range bi.Members {
					if m.Name == name {
						if st := netStatus[b]; st != nil {
							st.Orphans = append(st.Orphans, name)
						}
					}
				}
			}
		}
		step := render.Step{Op: render.OpEpairDestroy, Phase: render.PhaseHost, Iface: name}
		if err := ex.Exec(ctx, step, ""); err != nil {
			rep.Failures["interface/"+name] = err.Error()
			continue
		}
		rep.Cleaned = append(rep.Cleaned, name)
		rep.Applied = append(rep.Applied, step.Describe())
	}
}

func sortedIfaces(o *Observed) []string {
	out := make([]string, 0, len(o.Ifaces))
	for k := range o.Ifaces {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// loadAnchor loads the rules of an anchor and then ends the flows of the rules that are gone. pf keeps the state of a
// connection that a rule once admitted even after the rule is removed or changed, so without this a policy that was
// tightened would let the open connections go on until they closed. Rules that did not change keep their label, so
// their connections are not touched (a policy edit does not cut what is still allowed).
func (r *Reconciler) loadAnchor(ctx context.Context, name, ruleset string) error {
	// What is loaded now says which flows the new rules may end. An anchor that does not exist yet has nothing to end;
	// any other failure to read must not be taken for that, or the flows of a removed rule would never be ended:
	// the load is tried again on the next pass.
	old, err := r.env.PF.Read(ctx, name)
	if err != nil && !api.IsCode(err, api.CodeNotFound) {
		return fmt.Errorf("reading the loaded rules of %s before replacing them: %w", name, err)
	}
	if err := r.env.PF.Load(ctx, name, ruleset); err != nil {
		return err
	}
	keep := pfctl.LabelsOf(ruleset)
	var gone []string
	for l := range pfctl.LabelsOf(old) {
		if !keep[l] {
			gone = append(gone, l)
		}
	}
	slices.Sort(gone)
	for _, l := range gone {
		if err := r.env.PF.KillLabel(ctx, l); err != nil {
			r.log.Warn("reconcile: ending the states of a removed rule failed", "anchor", name, "label", l, "err", err)
		}
	}
	return nil
}
