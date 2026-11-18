package ndproxy

import (
	"net/netip"
	"strings"
	"testing"
)

// An operator whose jail has no IPv6 reads the statistics: an address the router asked for and the proxy does not
// hold shows the questions and no answer, a held address shows its answers.
func TestStatisticsShowAskedAndAnswered(t *testing.T) {
	r := newRig(t, Config{})
	r.p.SetPrefixes([]netip.Prefix{netip.MustParsePrefix("2001:db8::/32")})
	r.add(t, jailA)
	missing := netip.MustParseAddr("2001:db8::99")
	r.p.HandleFrame(mkNS(router, jailA, SolicitedNode(jailA), macA, &macA))
	for i := 0; i < 3; i++ {
		r.p.HandleFrame(mkNS(router, missing, SolicitedNode(missing), macA, &macA))
	}
	var held, asked *AddrStats
	for _, a := range r.p.Addresses() {
		a := a
		switch a.Addr {
		case jailA:
			held = &a
		case missing:
			asked = &a
		}
	}
	if held == nil || !held.Held || held.Seen != 1 || held.Answered != 1 || held.LastAnswer.IsZero() {
		t.Fatalf("held address: %+v", held)
	}
	if asked == nil || asked.Held || asked.Seen != 3 || asked.Answered != 0 || asked.Dropped != 3 || !strings.HasPrefix(asked.LastVerdict, "no entry") {
		t.Fatalf("address asked for: %+v", asked)
	}
	evs, last := r.p.EventsAfter(0)
	if len(evs) != 4 || last != 4 || evs[0].Target != jailA || evs[3].Verdict[:8] != "no entry" {
		t.Fatalf("events %+v last %d", evs, last)
	}
	if again, _ := r.p.EventsAfter(last); len(again) != 0 {
		t.Fatalf("%d events after the newest", len(again))
	}
}
