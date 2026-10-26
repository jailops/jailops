package ndproxy

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{c.now.Add(d), ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

func req(n int) Requester {
	return Requester{IP: netip.MustParseAddr(fmt.Sprintf("fe80::%x", n)), MAC: MAC{2, 0, 0, 0, 1, byte(n)}}
}

func probeTable(t *testing.T) (*Table, *fakeClock) {
	c := newClock()
	tb := NewTable(TableConfig{Mode: ModeProbe, ValidTTL: 30 * time.Second, InvalidTTL: 10 * time.Second,
		RetransmitTime: time.Second, RetransmitLimit: 3, MaxPendingPerAddr: 2, MaxPendingTotal: 3}, c)
	if err := tb.Add(jailA); err != nil {
		t.Fatal(err)
	}
	return tb, c
}

func wantState(t *testing.T, tb *Table, a netip.Addr, s State) {
	t.Helper()
	if got, ok := tb.State(a); !ok || got != s {
		t.Fatalf("state = %v/%v, want %v", got, ok, s)
	}
}

func TestTableAddRemove(t *testing.T) {
	tb := NewTable(TableConfig{MaxEntries: 2}, newClock())
	for _, bad := range []string{"::", "::1", "fe80::1", "ff02::1", "192.0.2.1", "::ffff:192.0.2.1"} {
		if err := tb.Add(netip.MustParseAddr(bad)); !errors.Is(err, &api.Error{Code: api.CodeInvalid}) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if err := tb.Add(jailA); err != nil {
		t.Fatal(err)
	}
	if err := tb.Add(jailA); !errors.Is(err, &api.Error{Code: api.CodeAlreadyExists}) {
		t.Fatal(err)
	}
	if err := tb.Add(netip.MustParseAddr("2001:db8::9")); err != nil {
		t.Fatal(err)
	}
	if err := tb.Add(netip.MustParseAddr("2001:db8::a")); !errors.Is(err, &api.Error{Code: api.CodePoolExhausted}) {
		t.Fatal(err)
	}
	if !tb.Remove(jailA) || tb.Remove(jailA) || tb.Has(jailA) || tb.Len() != 1 {
		t.Fatal("remove")
	}
	if got := tb.Addrs(); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestImmediateMode(t *testing.T) {
	c := newClock()
	tb := NewTable(TableConfig{}, c)
	_ = tb.Add(jailA)
	wantState(t, tb, jailA, StateValid)
	c.Advance(24 * time.Hour)
	wantState(t, tb, jailA, StateValid) // never ages
	if r := tb.Solicit(jailA, req(1)); r != (Result{Verdict: VerdictAnswer}) {
		t.Fatal(r)
	}
	if r := tb.Solicit(netip.MustParseAddr("2001:db8::77"), req(1)); r.Verdict != VerdictUnknown {
		t.Fatal(r)
	}
	if _, ok := tb.Next(); ok {
		t.Fatal("immediate mode has timers")
	}
}

func TestProbeTransitions(t *testing.T) {
	tb, c := probeTable(t)
	wantState(t, tb, jailA, StateInvalid)

	// invalid (never probed) -> incomplete, first probe, queued
	if r := tb.Solicit(jailA, req(1)); r != (Result{Verdict: VerdictQueued, Probe: true}) {
		t.Fatal(r)
	}
	wantState(t, tb, jailA, StateIncomplete)
	// second requester queues without another probe; a repeat coalesces
	if r := tb.Solicit(jailA, req(2)); r != (Result{Verdict: VerdictQueued}) {
		t.Fatal(r)
	}
	tb.Solicit(jailA, req(2))
	if tb.Pending() != 2 {
		t.Fatalf("pending %d", tb.Pending())
	}
	// retransmit timing
	if res := tb.Tick(); len(res.Probes) != 0 {
		t.Fatal("early retransmit")
	}
	c.Advance(time.Second)
	if res := tb.Tick(); len(res.Probes) != 1 || res.Probes[0] != jailA {
		t.Fatal(res)
	}
	c.Advance(time.Second)
	if res := tb.Tick(); len(res.Probes) != 1 { // third and last probe
		t.Fatal(res)
	}
	// limit reached: incomplete -> invalid, queue dropped
	c.Advance(time.Second)
	if res := tb.Tick(); len(res.Probes) != 0 || res.Dropped != 2 {
		t.Fatal(res)
	}
	wantState(t, tb, jailA, StateInvalid)
	if tb.Pending() != 0 {
		t.Fatal("pending leaked")
	}
	// invalid is a negative cache for InvalidTTL
	if r := tb.Solicit(jailA, req(1)); r.Verdict != VerdictSuppressed || r.Probe {
		t.Fatal(r)
	}
	c.Advance(10 * time.Second)
	if r := tb.Solicit(jailA, req(1)); r != (Result{Verdict: VerdictQueued, Probe: true}) {
		t.Fatal(r)
	}
	// incomplete -> valid on reply, subscribers handed out
	got := tb.Reachable(jailA)
	if len(got) != 1 || got[0] != req(1) {
		t.Fatal(got)
	}
	wantState(t, tb, jailA, StateValid)
	if r := tb.Solicit(jailA, req(3)); r != (Result{Verdict: VerdictAnswer}) {
		t.Fatal(r)
	}
	// valid -> stale after ValidTTL
	c.Advance(30 * time.Second)
	wantState(t, tb, jailA, StateStale)
	// stale answers at once and starts exactly one refresh probe
	if r := tb.Solicit(jailA, req(3)); r != (Result{Verdict: VerdictAnswer, Probe: true}) {
		t.Fatal(r)
	}
	if r := tb.Solicit(jailA, req(3)); r != (Result{Verdict: VerdictAnswer}) {
		t.Fatal(r)
	}
	// stale -> valid on reply
	tb.Reachable(jailA)
	wantState(t, tb, jailA, StateValid)
	// stale -> invalid when the refresh fails
	c.Advance(30 * time.Second)
	tb.Solicit(jailA, req(3))
	for i := 0; i < 3; i++ {
		c.Advance(time.Second)
		tb.Tick()
	}
	wantState(t, tb, jailA, StateInvalid)
}

func TestNextDeadline(t *testing.T) {
	tb, c := probeTable(t)
	if _, ok := tb.Next(); ok {
		t.Fatal("idle invalid entry has a timer")
	}
	tb.Solicit(jailA, req(1))
	n, ok := tb.Next()
	if !ok || !n.Equal(c.Now().Add(time.Second)) {
		t.Fatal(n, ok)
	}
	tb.Reachable(jailA)
	n, ok = tb.Next()
	if !ok || !n.Equal(c.Now().Add(30*time.Second)) {
		t.Fatal(n, ok)
	}
}

func TestQueueBounds(t *testing.T) {
	tb, _ := probeTable(t)
	b := netip.MustParseAddr("2001:db8::b")
	_ = tb.Add(b)
	tb.Solicit(jailA, req(1))
	tb.Solicit(jailA, req(2))
	if r := tb.Solicit(jailA, req(3)); r.Verdict != VerdictFlood { // per-address bound 2
		t.Fatal(r)
	}
	tb.Solicit(b, req(4))
	if r := tb.Solicit(b, req(5)); r.Verdict != VerdictFlood { // total bound 3
		t.Fatal(r)
	}
	if tb.Pending() != 3 {
		t.Fatal(tb.Pending())
	}
	// A flood of distinct spoofed sources never grows memory past the bound.
	for i := 10; i < 5000; i++ {
		tb.Solicit(jailA, Requester{IP: netip.AddrFrom16([16]byte{0xfe, 0x80, 15: byte(i)}), MAC: MAC{2, byte(i >> 8), byte(i)}})
	}
	if tb.Pending() != 3 {
		t.Fatal(tb.Pending())
	}
	// removal releases the queue
	tb.Remove(jailA)
	if tb.Pending() != 1 {
		t.Fatal(tb.Pending())
	}
}

func TestSetConfigModeSwitch(t *testing.T) {
	tb, _ := probeTable(t)
	tb.Solicit(jailA, req(1))
	tb.SetConfig(TableConfig{Mode: ModeImmediate})
	wantState(t, tb, jailA, StateValid)
	if tb.Pending() != 0 {
		t.Fatal("pending kept")
	}
	tb.SetConfig(TableConfig{Mode: ModeProbe})
	wantState(t, tb, jailA, StateValid)
}
