package ndproxy

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func macN(i int) MAC { return MAC{2, 1, byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)} }

var unknownTarget = netip.MustParseAddr("2001:db8::99")

// solicit sends a solicitation of the requester mac for an address the proxy does not hold: it counts only against the
// limits, not against the per-target budget.
func (r *rig) solicit(mac MAC) {
	r.p.HandleFrame(mkNS(router, unknownTarget, SolicitedNode(unknownTarget), mac, nil))
}

func TestNormalSolicitationsAreNeverLimited(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	for i := 0; i < 600; i++ { // ten minutes at a solicitation every second, from three neighbors
		r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
		r.solicit(macB)
		r.solicit(macN(7))
		r.clk.Advance(time.Second)
	}
	if m := r.p.Metrics(); m.Floods != 0 || r.p.FloodsLastHour() != 0 {
		t.Fatalf("a normal rate was limited: %+v", m)
	}
}

func TestAFloodFromOneSourceIsCutAtItsBudget(t *testing.T) {
	r := newRig(t, Config{})
	for i := 0; i < 500; i++ { // all at once: the burst passes, the rest is dropped without an answer
		r.solicit(macB)
	}
	m := r.p.Metrics()
	if m.Unknown != solicitBurst || m.Floods != 500-solicitBurst {
		t.Fatalf("passed %d, dropped %d: %+v", m.Unknown, m.Floods, m)
	}
	// the budget refills with time: a second later the source has solicitPerSource more
	r.clk.Advance(time.Second)
	before := r.p.Metrics().Unknown
	for i := 0; i < 500; i++ {
		r.solicit(macB)
	}
	if got := r.p.Metrics().Unknown - before; got != solicitPerSource {
		t.Fatalf("after a second %d passed, want %d", got, solicitPerSource)
	}
}

func TestSourcesDoNotShareABudget(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	for i := 0; i < 1000; i++ {
		r.solicit(macB)
	}
	// the router has another address: its solicitation is answered while macB floods
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	if m := r.p.Metrics(); m.Answered != 1 {
		t.Fatalf("the router was not answered while another neighbor flooded: %+v", m)
	}
}

func TestAllSolicitationsTogetherHaveABudget(t *testing.T) {
	r := newRig(t, Config{})
	n := solicitGeneralBurst + 1000
	for i := 0; i < n; i++ { // every solicitation from a source of its own, so no source limit applies
		r.solicit(macN(i + 1))
	}
	m := r.p.Metrics()
	if m.Unknown != solicitGeneralBurst || m.Floods != 1000 {
		t.Fatalf("passed %d, dropped %d", m.Unknown, m.Floods)
	}
}

// routerAsks runs the audit's picture: the router asks for the address of a jail ten times a second for five seconds
// while a flood runs alongside (flood is called each tenth of a second); it returns how many advertisements the router got.
func routerAsks(t *testing.T, r *rig, flood func(step int)) uint64 {
	t.Helper()
	if !r.p.table.Has(jailA) {
		r.add(t, jailA)
	}
	before := r.p.Metrics().Answered
	for step := 0; step < 50; step++ {
		if flood != nil {
			flood(step)
		}
		r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
		r.clk.Advance(100 * time.Millisecond)
	}
	return r.p.Metrics().Answered - before
}

// A flood from one MAC at twenty thousand a second spends its own allowance and nothing of the router's: the router
// is answered as often as when nothing floods: the source's allowance is charged before the global budget.
func TestAFloodFromOneMACDoesNotStarveTheRouter(t *testing.T) {
	quiet := routerAsks(t, newRig(t, Config{}), nil)
	if quiet == 0 {
		t.Fatal("the router was never answered without a flood")
	}
	r := newRig(t, Config{})
	got := routerAsks(t, r, func(int) {
		for i := 0; i < 2000; i++ {
			r.solicit(macB)
		}
	})
	if got != quiet {
		t.Fatalf("the router got %d answers during the flood, %d without it", got, quiet)
	}
	if r.p.Metrics().Floods == 0 {
		t.Fatal("the flood was not counted")
	}
}

// The source link-layer option is the sender's own claim: a sender that puts a different one in every solicitation is
// still one Ethernet source with one allowance.
func TestRotatingTheLinkLayerOptionDoesNotEvadeTheSourceBudget(t *testing.T) {
	r := newRig(t, Config{})
	for i := 0; i < 3000; i++ {
		other := macN(i + 1)
		r.p.HandleFrame(mkNS(router, unknownTarget, SolicitedNode(unknownTarget), macB, &other))
	}
	m := r.p.Metrics()
	if m.Unknown != solicitBurst || m.Floods != 3000-solicitBurst {
		t.Fatalf("passed %d, dropped %d: %+v", m.Unknown, m.Floods, m)
	}
	r.p.mu.Lock()
	n := len(r.p.sources)
	r.p.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d senders held for one Ethernet source", n)
	}
}

// A flood from a new MAC in every frame passes the allowance of each source and uses up the general budget; the router,
// which the proxy has answered over time, keeps the reserved part of it.
func TestAFloodOfManyMACsLeavesTheKnownRouterItsReservedShare(t *testing.T) {
	quiet := routerAsks(t, newRig(t, Config{}), nil)
	r := newRig(t, Config{})
	r.add(t, jailA)
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	r.clk.Advance(2 * time.Second)
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	r.clk.Advance(time.Second)
	r.p.mu.Lock()
	known := r.p.sources[macA] != nil && r.p.sources[macA].known
	r.p.mu.Unlock()
	if !known {
		t.Fatal("a requester that was answered and asked again a second later is not known")
	}
	n := 0
	got := routerAsks(t, r, func(int) {
		for i := 0; i < 2000; i++ { // twenty thousand a second, every one from a MAC of its own
			n++
			r.solicit(macN(n))
		}
	})
	if got != quiet {
		t.Fatalf("the known router got %d answers during the flood, %d without it", got, quiet)
	}
}

// A sender that changes its MAC every time is never known, however often it is answered, and a full table of known
// senders is not emptied by new ones.
func TestAnUnknownFloodNeverBecomesKnown(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	for i := 0; i < 5000; i++ {
		m := macN(i + 1)
		r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), m, &m))
		if i%100 == 99 {
			r.clk.Advance(time.Second)
		}
	}
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	for m, b := range r.p.sources {
		if b.known {
			t.Fatalf("%v is known after one answer", m)
		}
	}
}

func TestTheTableOfSourcesIsBounded(t *testing.T) {
	r := newRig(t, Config{})
	for i := 0; i < sourceTableMax*3; i++ {
		r.solicit(macN(i + 1))
		if i%1000 == 0 {
			r.clk.Advance(time.Second) // keep the total budget from ending the test early
		}
	}
	r.p.mu.Lock()
	n := len(r.p.sources)
	r.p.mu.Unlock()
	if n > sourceTableMax {
		t.Fatalf("%d sources held, the bound is %d", n, sourceTableMax)
	}
}

func TestDropsOfTheLastHourAreCountedAndLoggedOncePerMinute(t *testing.T) {
	r := newRig(t, Config{})
	var mu sync.Mutex
	var logs []string
	p, err := New(Config{Uplink: "em0"}, Deps{Capture: r.cap, Injector: r.inj, Routes: r.rt, Joiner: r.jn, Clock: r.clk,
		Logf: func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	p.hostMAC = macH
	p.SetPrefixes([]netip.Prefix{netip.MustParsePrefix("2001:db8::/32")})
	flood := func(n int) {
		for i := 0; i < n; i++ {
			p.HandleFrame(mkNS(router, unknownTarget, SolicitedNode(unknownTarget), macB, nil))
		}
	}
	flood(solicitBurst + 50) // 50 dropped
	if got := p.Status().FloodsLastHour; got != 50 {
		t.Fatalf("floods in the last hour: %d, want 50", got)
	}
	flood(100) // inside the same minute: counted, not logged again
	mu.Lock()
	n := len(logs)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d log lines inside a minute: %v", n, logs)
	}
	r.clk.Advance(61 * time.Second)
	flood(solicitBurst + 10)
	mu.Lock()
	n = len(logs)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("%d log lines a minute later, want 2: %v", n, logs)
	}
	// after an hour without drops the count is gone
	r.clk.Advance(61 * time.Minute)
	if got := p.Status().FloodsLastHour; got != 0 {
		t.Fatalf("floods an hour later: %d", got)
	}
}

func TestAKnownSenderSurvivesAFloodThatFillsTheTable(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	r.clk.Advance(2 * time.Second)
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	for i := 0; i < sourceTableMax*3; i++ {
		r.solicit(macN(i + 1))
		if i%1000 == 0 {
			r.clk.Advance(time.Second)
		}
	}
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	if b := r.p.sources[macA]; b == nil || !b.known {
		t.Fatal("a flood of new senders pushed the known router out of the table")
	}
}

// A promiscuous capture on a shared segment sees every solicitation of the segment. The ones for addresses that are
// neither held nor inside a prefix of ours belong to other machines: they are counted and nothing else, so they use
// none of the allowance of the router (the same Ethernet source asks for ours) and do not fill the ring of events.
func TestSolicitationsForOtherMachinesUseNoAllowanceAndNoEvents(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	foreign := netip.MustParseAddr("2001:db9::7")
	for i := 0; i < 5000; i++ {
		r.p.HandleFrame(mkNS(router, foreign, SolicitedNode(foreign), macA, &macA))
	}
	if m := r.p.Metrics(); m.Floods != 0 || r.p.FloodsLastHour() != 0 {
		t.Fatalf("solicitations for other machines were charged against the allowance: %+v", m)
	}
	// the router, with the same Ethernet source, is still answered for the jail
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	if m := r.p.Metrics(); m.Answered != 1 {
		t.Fatalf("the router was not answered after the noise: %+v", m)
	}
	events, _ := r.p.EventsAfter(0)
	if len(events) != 1 {
		t.Fatalf("the noise filled the event ring: %d events", len(events))
	}
}

// The ring keeps the last events in order across the first wrap: the oldest one is the one that goes.
func TestEventRingKeepsTheNewestAcrossTheWrap(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	n := maxEvents + 5
	for i := 0; i < n; i++ {
		r.p.record("solicit", jailA, router, fmt.Sprintf("v%d", i))
	}
	events, last := r.p.EventsAfter(0)
	if len(events) != maxEvents {
		t.Fatalf("%d events, want %d", len(events), maxEvents)
	}
	for i, ev := range events {
		if want := fmt.Sprintf("v%d", n-maxEvents+i); ev.Verdict != want {
			t.Fatalf("event %d is %s, want %s: the ring is not in order", i, ev.Verdict, want)
		}
		if i > 0 && ev.Seq <= events[i-1].Seq {
			t.Fatalf("sequence numbers do not rise: %d after %d", ev.Seq, events[i-1].Seq)
		}
	}
	if last != events[len(events)-1].Seq {
		t.Fatalf("last is %d, the newest event has %d", last, events[len(events)-1].Seq)
	}
}

// Two proxies (two uplinks) of one daemon share one sequence: after the newest sequence number seen on either, neither
// proxy repeats an event, and none is lost to the other's numbers.
func TestEventSequenceIsOneForAllProxies(t *testing.T) {
	a, b := newRig(t, Config{}), newRig(t, Config{})
	a.add(t, jailA)
	b.add(t, jailA)
	a.p.record("solicit", jailA, router, "a1")
	b.p.record("solicit", jailA, router, "b1")
	a.p.record("solicit", jailA, router, "a2")
	_, lastA := a.p.EventsAfter(0)
	_, lastB := b.p.EventsAfter(0)
	cursor := max(lastA, lastB)
	b.p.record("solicit", jailA, router, "b2")
	if evs, _ := a.p.EventsAfter(cursor); len(evs) != 0 {
		t.Fatalf("proxy a repeats events: %v", evs)
	}
	if evs, _ := b.p.EventsAfter(cursor); len(evs) != 1 || evs[0].Verdict != "b2" {
		t.Fatalf("proxy b lost its event to the other's numbers: %v", evs)
	}
}
