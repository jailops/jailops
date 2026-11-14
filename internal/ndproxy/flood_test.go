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
	n := solicitTotalBurst + 1000
	for i := 0; i < n; i++ { // every solicitation from a source of its own, so no source limit applies
		r.solicit(macN(i + 1))
	}
	m := r.p.Metrics()
	if m.Unknown != solicitTotalBurst || m.Floods != 1000 {
		t.Fatalf("passed %d, dropped %d", m.Unknown, m.Floods)
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
	p, err := New(Config{Uplink: "em0"}, Deps{Capture: r.cap, Injector: r.inj, Routes: r.rt, Joiner: r.jn, Prober: r.pr, Clock: r.clk,
		Logf: func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	p.hostMAC = macH
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
