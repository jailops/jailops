package reconcile_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/ndproxy"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

var bg = context.Background()

const netID = "11111111-1111-4111-8111-111111111111"
const jailID = "22222222-2222-4222-8222-222222222222"

type world struct {
	t      *testing.T
	Net    *ifconfig.Fake
	PF     *pfctl.Fake
	Jails  *jailos.Fake
	des    reconcile.Desired
	mu     sync.Mutex
	locks  map[string]bool
	starts []string
	fail   map[string]error
	netSt  map[string]api.NetworkStatus
	jailSt map[string][]api.Condition
	drift  map[string][]string
	R      *reconcile.Reconciler
	clock  *fakeClock
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}
type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []*fakeTimer
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
	c.mu.Unlock()
}
func (c *fakeClock) Pending() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.timers) }

func (w *world) Desired(context.Context) (reconcile.Desired, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.des, nil
}
func (w *world) NetworkStatus(n string, st api.NetworkStatus) {
	w.mu.Lock()
	w.netSt[n] = st
	w.mu.Unlock()
}
func (w *world) JailStatus(n string, c []api.Condition, d []string) {
	w.mu.Lock()
	w.jailSt[n], w.drift[n] = c, d
	w.mu.Unlock()
}
func (w *world) TryLock(h string, keys ...string) (func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range keys {
		if w.locks[k] {
			return nil, api.Errorf(api.CodeBusy, "%s busy", k)
		}
	}
	for _, k := range keys {
		w.locks[k] = true
	}
	return func() {
		w.mu.Lock()
		for _, k := range keys {
			delete(w.locks, k)
		}
		w.mu.Unlock()
	}, nil
}
func (w *world) IsLocked(k string) bool { w.mu.Lock(); defer w.mu.Unlock(); return w.locks[k] }
func (w *world) StartJail(_ context.Context, name string) error {
	w.mu.Lock()
	w.starts = append(w.starts, name)
	err := w.fail[name]
	w.mu.Unlock()
	if err == nil {
		return w.Jails.Create(bg, "/etc/jailops/jails/"+name+"/jail.conf", name)
	}
	return err
}

func sysctl(context.Context, string) (string, error) { return "1", nil }

func newWorld(t *testing.T, cfg reconcile.Config) *world {
	w := &world{t: t, Net: ifconfig.NewFake(ifconfig.Options{}), PF: pfctl.NewFake(), Jails: jailos.NewFake(),
		locks: map[string]bool{}, fail: map[string]error{}, netSt: map[string]api.NetworkStatus{},
		jailSt: map[string][]api.Condition{}, drift: map[string][]string{},
		clock: &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	w.R = reconcile.New(cfg, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Run: privateRunner(), Sysctl: sysctl, Clock: w.clock})
	t.Cleanup(w.R.Close)
	return w
}

func lan() reconcile.NetworkDesired {
	spec := api.NetworkSpec{Bridge: "jo0", IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.50"}}
	steps, err := render.NetworkPlan(render.NetworkInput{ID: netID, Name: "lan", Bridge: "jo0", Spec: spec})
	if err != nil {
		panic(err)
	}
	anchor, err := render.PFNet("lan", spec)
	if err != nil {
		panic(err)
	}
	return reconcile.NetworkDesired{Name: "lan", ID: netID, Bridge: "jo0", Spec: spec, Steps: steps, Allocated: 2, Free: 39,
		Anchors: map[string]string{render.NetAnchor("lan"): anchor}}
}

func find(l []ifconfig.Iface, name string) (ifconfig.Iface, bool) {
	for _, i := range l {
		if i.Name == name {
			return i, true
		}
	}
	return ifconfig.Iface{}, false
}

func TestNetworkPassCreatesAndConverges(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}}
	rep := w.R.Pass(bg)
	if rep.Err != nil || len(rep.Failures) != 0 {
		t.Fatalf("%+v", rep)
	}
	ifs, _ := w.Net.List(bg)
	br, ok := find(ifs, "jo0")
	if !ok || !br.Up() || br.Description != "jailops net=lan bridge" || len(br.Inet) != 0 || len(br.Members) != 1 || br.Members[0].Name != "jo0ga" {
		t.Fatalf("the bridge carries no address and has the gateway port as its only member: %+v", br)
	}
	gw, ok := find(ifs, "jo0gb")
	if !ok || !gw.Up() || gw.Description != "jailops net=lan gateway (host side)" || len(gw.Inet) != 1 || gw.Inet[0].String() != "10.20.0.1/24" {
		t.Fatalf("the gateway addresses are on the l3 end of the gateway pair: %+v", gw)
	}
	if ga, ok := find(ifs, "jo0ga"); !ok || !ga.Up() {
		t.Fatalf("%+v", ga)
	}
	st := w.netSt["lan"]
	if !st.BridgeUp || st.Allocated != 2 || st.Free != 39 || len(st.Conditions) != 1 || st.Conditions[0].Status != api.ConditionTrue {
		t.Fatalf("%+v", st)
	}
	// A second pass has nothing to do.
	if rep2 := w.R.Pass(bg); len(rep2.Applied) != 0 {
		t.Fatalf("not idempotent: %v", rep2.Applied)
	}
	// Drift: the address disappears and an extra one appears; both are fixed.
	_ = w.Net.AddrRemove(bg, "jo0gb", netip.MustParsePrefix("10.20.0.1/24"))
	_ = w.Net.AddrAdd(bg, "jo0gb", netip.MustParsePrefix("192.0.2.9/24"))
	_ = w.Net.AddrAdd(bg, "jo0", netip.MustParsePrefix("192.0.2.10/24")) // an address on the bridge (the old layout) goes too
	rep3 := w.R.Pass(bg)
	ifs, _ = w.Net.List(bg)
	br, _ = find(ifs, "jo0")
	gw, _ = find(ifs, "jo0gb")
	if len(gw.Inet) != 1 || gw.Inet[0].String() != "10.20.0.1/24" || len(br.Inet) != 0 || len(rep3.Applied) < 3 {
		t.Fatalf("%+v %+v %v", gw.Inet, br.Inet, rep3.Applied)
	}
	// The whole gateway pair is repaired when it disappears.
	_ = w.Net.EpairDestroy(bg, "jo0ga")
	rep4 := w.R.Pass(bg)
	ifs, _ = w.Net.List(bg)
	if gw, ok = find(ifs, "jo0gb"); !ok || len(gw.Inet) != 1 || len(rep4.Applied) == 0 {
		t.Fatalf("the gateway pair was not recreated: %+v %v", gw, rep4.Applied)
	}
	if br, _ = find(ifs, "jo0"); len(br.Members) != 1 || br.Members[0].Name != "jo0ga" {
		t.Fatalf("the gateway port is not back on the bridge: %+v", br.Members)
	}
}

func TestNetworkPassNeverTouchesForeignBridge(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	w.Net.AddForeign("jo0", true)
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}}
	rep := w.R.Pass(bg)
	if len(rep.Applied) != 0 || rep.Failures["network/lan"] == "" {
		t.Fatalf("%+v", rep)
	}
	ifs, _ := w.Net.List(bg)
	if br, _ := find(ifs, "jo0"); len(br.Inet) != 0 || br.Description != "" {
		t.Fatalf("a bridge JailOps does not own was changed: %+v", br)
	}
	if c := w.netSt["lan"].Conditions; len(c) != 1 || c[0].Reason != "ForeignBridge" || c[0].Status != api.ConditionFalse {
		t.Fatalf("%+v", c)
	}
}

func TestNetworkPassSkipsBusyNetworkAndReportsPrecondition(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}}
	w.locks["network/lan"] = true
	if rep := w.R.Pass(bg); len(rep.Applied) != 0 {
		t.Fatalf("applied under a held lock: %v", rep.Applied)
	}
	delete(w.locks, "network/lan")
	// A failing precondition becomes a condition; nothing is edited.
	w2 := newWorld(t, reconcile.Config{})
	w2.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w2, Sink: w2, Actions: w2, Net: w2.Net, PF: w2.PF, Jails: w2.Jails, Run: privateRunner(),
		Sysctl: func(context.Context, string) (string, error) { return "0", nil }, Clock: w2.clock})
	routed := lan()
	routed.Spec.Uplink = "em0" // routing needs forwarding, which reads 0 here
	routed.Steps, _ = render.NetworkPlan(render.NetworkInput{ID: netID, Name: "lan", Bridge: "jo0", Spec: routed.Spec})
	w2.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{routed}}
	rep := w2.R.Pass(bg)
	c := w2.netSt["lan"].Conditions
	if len(c) != 1 || c[0].Reason != "Precondition" || !strings.Contains(c[0].Message, "sysctl") || len(rep.Applied) != 0 {
		t.Fatalf("%+v %v", c, rep.Applied)
	}
}

func epairOf(jid string, i int) (string, string) {
	h, j, _ := ipam.EpairNames(jid, i)
	return h, j
}

func jailDesired(name string) reconcile.JailDesired {
	host, jail := epairOf(jailID, 0)
	_ = jail
	steps, _ := render.AttachPlan(render.AttachInput{JailID: jailID, JailUUID: jailID, Index: 0, Bridge: "jo0",
		IPv4: netip.MustParsePrefix("10.20.0.10/24"), Gateway4: netip.MustParseAddr("10.20.0.1")})
	var hostSteps []render.Step
	for _, s := range steps {
		if s.Phase == render.PhaseHost {
			hostSteps = append(hostSteps, s)
		}
	}
	return reconcile.JailDesired{Name: name, ID: jailID, Attach: [][]render.Step{hostSteps}, Bridges: []string{"jo0"}, Epairs: []string{host},
		AnchorName: "jailops/" + name, Ruleset: "pass in all\n"}
}

func TestOrphanEpairCleanup(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{jailDesired("web")}}
	w.R.Pass(bg) // bridge exists
	ctx := bg
	mk := func(id string, idx int, member bool) string {
		h, _ := epairOf(id, idx)
		if _, _, err := w.Net.EpairCreate(ctx, h[:len(h)-1]); err != nil {
			t.Fatal(err)
		}
		_ = w.Net.SetDescription(ctx, h, "jailops:"+id)
		if member {
			_ = w.Net.BridgeAddMember(ctx, "jo0", h, false)
		}
		return h
	}
	orphan := mk("33333333-3333-4333-8333-333333333333", 0, true)
	stale := mk(jailID, 0, false) // jail "web" exists but is stopped
	// An interface with a JailOps looking name but a foreign description is left alone.
	fh, _ := epairOf("44444444-4444-4444-8444-444444444444", 0)
	if _, _, err := w.Net.EpairCreate(ctx, fh[:len(fh)-1]); err != nil {
		t.Fatal(err)
	}
	_ = w.Net.SetDescription(ctx, fh, "someone else's")
	rep := w.R.Pass(bg)
	if len(rep.Cleaned) != 2 {
		t.Fatalf("%+v", rep)
	}
	ifs, _ := w.Net.List(ctx)
	if _, ok := find(ifs, orphan); ok {
		t.Error("orphan epair survived")
	}
	if _, ok := find(ifs, stale); ok {
		t.Error("epair of a stopped jail survived")
	}
	if _, ok := find(ifs, fh); !ok {
		t.Error("an interface JailOps does not own was destroyed")
	}
	// An epair of a jail whose lock is held (a start in flight) is kept.
	w.locks["jail/web"] = true
	keep := mk(jailID, 0, false)
	if rep := w.R.Pass(bg); len(rep.Cleaned) != 0 {
		t.Fatalf("%+v", rep)
	}
	delete(w.locks, "jail/web")
	// And a running jail keeps its epair.
	if err := w.Jails.Create(ctx, "/etc/jailops/jails/web/jail.conf", "web"); err != nil {
		t.Fatal(err)
	}
	w.R.Pass(bg)
	ifs, _ = w.Net.List(ctx)
	if _, ok := find(ifs, keep); !ok {
		t.Error("the epair of a running jail was destroyed")
	}
}

func TestDriftAndAnchorHeal(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	j := jailDesired("web")
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	// The jail runs but has no epair and a foreign anchor.
	_ = w.PF.Load(bg, "jailops/web", "block all\n")
	rep := w.R.Pass(bg)
	if len(rep.Drift["web"]) == 0 || !strings.Contains(strings.Join(rep.Drift["web"], ";"), "is missing") {
		t.Fatalf("%+v", rep.Drift)
	}
	live, _ := w.PF.Read(bg, "jailops/web")
	if live != j.Ruleset {
		t.Fatalf("anchor not healed: %q", live)
	}
	conds := w.jailSt["web"]
	if len(conds) != 2 || conds[1].Type != reconcile.CondDrift || conds[1].Status != api.ConditionFalse {
		t.Fatalf("%+v", conds)
	}
	// Create the epair as a start would; the next pass is clean.
	h := j.Epairs[0]
	_, _, _ = w.Net.EpairCreate(bg, h[:len(h)-1])
	_ = w.Net.SetDescription(bg, h, "jailops:"+jailID)
	rep = w.R.Pass(bg)
	if len(rep.Drift["web"]) != 0 {
		t.Fatalf("%+v", rep.Drift)
	}
	if w.jailSt["web"][1].Status != api.ConditionTrue {
		t.Fatalf("%+v", w.jailSt["web"])
	}
	// The membership of the epair is repaired.
	ifs, _ := w.Net.List(bg)
	br, _ := find(ifs, "jo0")
	if len(br.Members) != 2 || br.Members[1].Name != h { // the gateway port and the jail's
		t.Fatalf("%+v", br.Members)
	}
}

func jd(name string, prio int, deps ...string) reconcile.JailDesired {
	return reconcile.JailDesired{Name: name, ID: name + "-id", Boot: api.BootPolicy{Autostart: true, Priority: prio, Depends: deps}}
}

func TestStartOrder(t *testing.T) {
	t.Run("priority then name", func(t *testing.T) {
		got, skipped := reconcile.StartOrder([]reconcile.JailDesired{jd("c", 1), jd("a", 2), jd("b", 1)}, nil)
		if strings.Join(got, ",") != "b,c,a" || len(skipped) != 0 {
			t.Fatalf("%v %v", got, skipped)
		}
	})
	t.Run("depends first, even against priority", func(t *testing.T) {
		got, _ := reconcile.StartOrder([]reconcile.JailDesired{jd("app", 1, "db"), jd("db", 9)}, nil)
		if strings.Join(got, ",") != "db,app" {
			t.Fatalf("%v", got)
		}
	})
	t.Run("a dependency without autostart is started too", func(t *testing.T) {
		db := reconcile.JailDesired{Name: "db", ID: "x"}
		got, _ := reconcile.StartOrder([]reconcile.JailDesired{jd("app", 0, "db"), db}, nil)
		if strings.Join(got, ",") != "db,app" {
			t.Fatalf("%v", got)
		}
	})
	t.Run("running dependencies and running jails", func(t *testing.T) {
		got, _ := reconcile.StartOrder([]reconcile.JailDesired{jd("app", 0, "db"), jd("db", 0), jd("x", 0)}, map[string]bool{"db": true, "x": true})
		if strings.Join(got, ",") != "app" {
			t.Fatalf("%v", got)
		}
	})
	t.Run("cycles and unknown dependencies are reported, not guessed", func(t *testing.T) {
		got, skipped := reconcile.StartOrder([]reconcile.JailDesired{jd("a", 0, "b"), jd("b", 0, "a"), jd("c", 0, "ghost"), jd("ok", 0)}, nil)
		if strings.Join(got, ",") != "ok" || skipped["a"] == "" || skipped["b"] == "" || !strings.Contains(skipped["c"], "does not exist") {
			t.Fatalf("%v %v", got, skipped)
		}
	})
	t.Run("a dependent of a cycle is skipped too", func(t *testing.T) {
		got, skipped := reconcile.StartOrder([]reconcile.JailDesired{jd("a", 0, "b"), jd("b", 0, "a"), jd("z", 0, "a")}, nil)
		if len(got) != 0 || skipped["z"] == "" {
			t.Fatalf("%v %v", got, skipped)
		}
	})
}

func TestAutostartOnFirstPassOnly(t *testing.T) {
	w := newWorld(t, reconcile.Config{Autostart: true})
	w.des = reconcile.Desired{Jails: []reconcile.JailDesired{jd("db", 0), jd("app", 0, "db"), jd("web", 0, "app"), jd("cache", 5)}}
	w.fail["db"] = errors.New("boom")
	rep := w.R.Pass(bg)
	if strings.Join(w.starts, ",") != "db,cache" {
		t.Fatalf("starts %v", w.starts)
	}
	if len(rep.Started) != 1 || rep.Started[0] != "cache" {
		t.Fatalf("%+v", rep)
	}
	byName := func(n string) api.Condition {
		for _, c := range w.jailSt[n] {
			if c.Type == reconcile.CondBoot {
				return c
			}
		}
		t.Fatalf("no autostart condition for %s: %+v", n, w.jailSt[n])
		return api.Condition{}
	}
	if c := byName("db"); c.Status != api.ConditionFalse || c.Reason != "StartFailed" || !strings.Contains(c.Message, "boom") {
		t.Fatalf("%+v", c)
	}
	if c := byName("app"); c.Reason != "DependencyFailed" {
		t.Fatalf("%+v", c)
	}
	if c := byName("web"); c.Reason != "DependencyFailed" {
		t.Fatalf("%+v", c)
	}
	if c := byName("cache"); c.Status != api.ConditionTrue {
		t.Fatalf("%+v", c)
	}
	// An operator stops a jail later: the loop does not start it again.
	_ = w.Jails.Remove(bg, "/etc/jailops/jails/cache/jail.conf", "cache")
	delete(w.fail, "db")
	w.R.Pass(bg)
	if len(w.starts) != 2 {
		t.Fatalf("started again: %v", w.starts)
	}
}

type fakeNDP struct {
	mu      sync.Mutex
	entries [][]ndproxy.Entry
	run     chan struct{}
	// shut makes the capture report as closed with the reason openErr; closes counts Close calls.
	shut    bool
	openErr string
	closes  int
}

func (f *fakeNDP) Status() ndproxy.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ndproxy.Status{Open: !f.shut, Err: f.openErr}
}
func (f *fakeNDP) Close() error { f.mu.Lock(); f.closes++; f.mu.Unlock(); return nil }

func (f *fakeNDP) Sync(_ context.Context, e []ndproxy.Entry) error {
	f.mu.Lock()
	f.entries = append(f.entries, e)
	f.mu.Unlock()
	return nil
}
func (f *fakeNDP) Run(ctx context.Context) error { close(f.run); <-ctx.Done(); return nil }
func (f *fakeNDP) last() []ndproxy.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) == 0 {
		return nil
	}
	return f.entries[len(f.entries)-1]
}

func proxyNet() reconcile.NetworkDesired {
	spec := api.NetworkSpec{Bridge: "jo0", Uplink: "em0", IPv6: &api.IPv6Net{Mode: api.IPv6Proxy, Subnet: "2001:db8:1::/64", Gateway: "fe80::1", Range: "2001:db8:1::10-2001:db8:1::20"}}
	steps, err := render.NetworkPlan(render.NetworkInput{ID: netID, Name: "lan", Bridge: "jo0", Spec: spec})
	if err != nil {
		panic(err)
	}
	return reconcile.NetworkDesired{Name: "lan", ID: netID, Bridge: "jo0", Spec: spec, Steps: steps}
}

func TestNDPProxyManagement(t *testing.T) {
	ndp := &fakeNDP{run: make(chan struct{})}
	var built []string
	w := newWorld(t, reconcile.Config{NDP: func(up string) (reconcile.NDP, error) { built = append(built, up); return ndp, nil }})
	j := jailDesired("web")
	j.Proxy = []reconcile.ProxyAddr{{Addr: "2001:db8:1::10", Bridge: "jo0", Uplink: "em0"}}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}, Jails: []reconcile.JailDesired{j}}
	// A stopped jail is not answered for.
	w.R.Pass(bg)
	if len(built) != 1 || built[0] != "em0" || len(ndp.last()) != 0 {
		t.Fatalf("%v %v", built, ndp.last())
	}
	<-ndp.run
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	w.R.Pass(bg)
	got := ndp.last()
	if len(got) != 1 || got[0].Addr.String() != "2001:db8:1::10" || got[0].Bridge != "jo0" {
		t.Fatalf("%+v", got)
	}
	var cond api.Condition
	for _, c := range w.netSt["lan"].Conditions {
		if c.Type == reconcile.CondProxy {
			cond = c
		}
	}
	if cond.Status != api.ConditionTrue || cond.Reason != "Answering" {
		t.Fatalf("%+v", w.netSt["lan"].Conditions)
	}
	// The jail stops: the address is withdrawn.
	_ = w.Jails.Remove(bg, "/etc/jailops/jails/web/jail.conf", "web")
	w.R.Pass(bg)
	if len(ndp.last()) != 0 {
		t.Fatalf("%+v", ndp.last())
	}
	if len(built) != 1 {
		t.Fatalf("the proxy was rebuilt: %v", built)
	}
}

func TestNDPProxyUnsupportedOrFailing(t *testing.T) {
	for name, cfg := range map[string]reconcile.Config{
		"unsupported": {},
		"fails":       {NDP: func(string) (reconcile.NDP, error) { return nil, errors.New("no bpf") }},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, cfg)
			w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}}
			w.R.Pass(bg)
			var c api.Condition
			for _, x := range w.netSt["lan"].Conditions {
				if x.Type == reconcile.CondProxy {
					c = x
				}
			}
			if c.Status != api.ConditionFalse || c.Reason == "" {
				t.Fatalf("%+v", w.netSt["lan"].Conditions)
			}
		})
	}
}

func TestRunLoopTriggers(t *testing.T) {
	w := newWorld(t, reconcile.Config{Interval: time.Minute, Debounce: time.Second})
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { w.R.Run(ctx); close(done) }()
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", what)
	}
	waitFor("first pass", func() bool { return !w.R.Last().At.IsZero() && w.clock.Pending() > 0 })
	first := w.R.Last().At
	// The timer triggers the next pass.
	w.clock.Advance(time.Minute)
	waitFor("timer pass", func() bool { return w.R.Last().At.After(first) })
	second := w.R.Last().At
	// A kick is debounced and then runs a pass.
	w.R.Kick()
	waitFor("debounce timer", func() bool { return w.clock.Pending() >= 2 })
	w.clock.Advance(time.Second)
	waitFor("kicked pass", func() bool { return w.R.Last().At.After(second) })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop")
	}
}

// A jail of a project is known by its qualified key, its anchor is nested
// below the project's parent anchor, and the parent is loaded first.
func TestProjectJailAnchorsAndParent(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	j := jailDesired("web_nginx")
	j.Project = "web"
	j.AnchorName = render.JailAnchor("web_nginx")
	j.ParentAnchors = map[string]string{render.ProjectAnchor("web"): render.ProjectAnchorRules()}
	if j.AnchorName != "jailops/web/nginx" {
		t.Fatal(j.AnchorName)
	}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web_nginx/jail.conf", "web_nginx")
	h := j.Epairs[0]
	_, _, _ = w.Net.EpairCreate(bg, h[:len(h)-1])
	_ = w.Net.SetDescription(bg, h, "jailops:"+jailID)
	rep := w.R.Pass(bg)
	if len(rep.Drift["web_nginx"]) != 0 {
		t.Fatalf("%+v", rep.Drift)
	}
	if live, _ := w.PF.Read(bg, "jailops/web/nginx"); live != j.Ruleset {
		t.Fatalf("jail anchor: %q", live)
	}
	parent, _ := w.PF.Read(bg, "jailops/web")
	if !strings.Contains(parent, `anchor "*"`) || !strings.Contains(parent, `nat-anchor "*"`) {
		t.Fatalf("parent anchor: %q", parent)
	}
	// A foreign parent anchor is healed like the jail's own.
	_ = w.PF.Load(bg, "jailops/web", "block all\n")
	w.R.Pass(bg)
	if parent, _ := w.PF.Read(bg, "jailops/web"); !strings.Contains(parent, `anchor "*"`) {
		t.Fatalf("parent anchor not healed: %q", parent)
	}
}

// Dependencies are between keys: the same name in two projects is two jails.
func TestStartOrderWithQualifiedKeys(t *testing.T) {
	a := jd("web_app", 1, "web_db")
	b := jd("web_db", 2)
	c := jd("db_app", 0, "db_db")
	d := jd("db_db", 2)
	got, skipped := reconcile.StartOrder([]reconcile.JailDesired{a, b, c, d}, nil)
	if len(skipped) != 0 {
		t.Fatal(skipped)
	}
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	if len(got) != 4 || pos["web_db"] > pos["web_app"] || pos["db_db"] > pos["db_app"] {
		t.Fatalf("%v", got)
	}
}

func TestAProxyWhoseCaptureIsClosedDoesNotReportAnswering(t *testing.T) {
	ndp := &fakeNDP{run: make(chan struct{}), shut: true, openErr: "ndp.open refused: the IPv6 proxy is not approved"}
	w := newWorld(t, reconcile.Config{NDP: func(string) (reconcile.NDP, error) { return ndp, nil }})
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}}
	rep := w.R.Pass(bg)
	var c api.Condition
	for _, x := range w.netSt["lan"].Conditions {
		if x.Type == reconcile.CondProxy {
			c = x
		}
	}
	if c.Status != api.ConditionFalse || c.Reason != "NoCapture" || !strings.Contains(c.Message, "not approved") {
		t.Fatalf("a proxy that cannot capture says it answers: %+v", w.netSt["lan"].Conditions)
	}
	if rep.Failures["ndproxy/em0"] == "" {
		t.Fatalf("the failure is not in the report: %+v", rep.Failures)
	}
	// the capture opens: the condition turns true
	ndp.mu.Lock()
	ndp.shut, ndp.openErr = false, ""
	ndp.mu.Unlock()
	w.R.Pass(bg)
	for _, x := range w.netSt["lan"].Conditions {
		if x.Type == reconcile.CondProxy {
			c = x
		}
	}
	if c.Status != api.ConditionTrue {
		t.Fatalf("%+v", w.netSt["lan"].Conditions)
	}
	// stopping the reconciler releases the proxy's sockets
	w.R.Close()
	ndp.mu.Lock()
	n := ndp.closes
	ndp.mu.Unlock()
	if n != 1 {
		t.Fatalf("Close called %d times", n)
	}
}

// privateRunner stands in for ifconfig(8): every jail port is made private by the plan.
func privateRunner() run.Runner {
	f := run.NewFake()
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	return f
}

// A proxy that was just started reports "Starting" for the pass that started it (its capture opens in its own
// goroutine). The loop looks again after the debounce instead of waiting a whole interval.
func TestAJustStartedProxyGetsASecondLookBeforeTheInterval(t *testing.T) {
	ndp := &fakeNDP{run: make(chan struct{}), shut: true}
	w := newWorld(t, reconcile.Config{Interval: time.Minute, Debounce: time.Second, NDP: func(string) (reconcile.NDP, error) { return ndp, nil }})
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{proxyNet()}}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { w.R.Run(ctx); close(done) }()
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", what)
	}
	waitFor("first pass", func() bool { return !w.R.Last().At.IsZero() && w.clock.Pending() > 0 })
	first := w.R.Last().At
	w.mu.Lock()
	cond := w.netSt["lan"].Conditions
	w.mu.Unlock()
	if len(cond) == 0 || cond[len(cond)-1].Reason != "Starting" {
		t.Fatalf("%+v", cond)
	}
	// The capture opens; the debounce timer, not the interval, brings the next pass.
	ndp.mu.Lock()
	ndp.shut = false
	ndp.mu.Unlock()
	w.clock.Advance(time.Second)
	waitFor("second pass", func() bool { return w.R.Last().At.After(first) })
	w.mu.Lock()
	cond = w.netSt["lan"].Conditions
	w.mu.Unlock()
	if len(cond) == 0 || cond[len(cond)-1].Reason != "Answering" {
		t.Fatalf("%+v", cond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop")
	}
}
