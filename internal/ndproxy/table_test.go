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

func TestTableAddRemove(t *testing.T) {
	tb := NewTable()
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
	if !tb.Remove(jailA) || tb.Remove(jailA) || tb.Has(jailA) || tb.Len() != 1 {
		t.Fatal("remove")
	}
	if got := tb.Addrs(); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestTableIsBounded(t *testing.T) {
	tb := NewTable()
	for i := 0; i < maxEntries; i++ {
		if err := tb.Add(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 14: byte(i >> 8), 15: byte(i)})); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := tb.Add(netip.MustParseAddr("2001:db8:1::1")); !errors.Is(err, &api.Error{Code: api.CodePoolExhausted}) {
		t.Fatal(err)
	}
}
