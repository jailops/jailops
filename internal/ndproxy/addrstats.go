package ndproxy

import (
	"net/netip"
	"sort"
	"sync/atomic"
	"time"
)

// AddrStats is what the proxy knows about one address of a jail: whether it holds it, how often it answered for it
// and announced it, and how often the segment asked for it while it was not held. An operator whose jail has no IPv6
// reads this to see whether the router asked, and whether the proxy answered.
type AddrStats struct {
	Addr  netip.Addr
	Iface string
	// Held is true while the proxy answers for the address.
	Held  bool
	Since time.Time // when the address was added
	// Answered and LastAnswer count the advertisements sent for solicitations.
	Answered   uint64
	LastAnswer time.Time
	// Announced and LastAnnounce count the unsolicited advertisements.
	Announced    uint64
	LastAnnounce time.Time
	// Missed counts solicitations for the address that came while it was not held (inside a prefix of an NDP proxy
	// network), LastMissed is the last one: the router asked and nobody answered.
	Missed     uint64
	LastMissed time.Time
	// Seen counts the solicitations for the address, Dropped those that were not answered; LastSeen and LastFrom are
	// the time and the source address of the last one, LastVerdict what was done with it.
	Seen        uint64
	Dropped     uint64
	LastSeen    time.Time
	LastFrom    netip.Addr
	LastVerdict string
	// LastError is the last reason an advertisement could not be sent (the helper refused it).
	LastError string
}

// Event is one thing the proxy saw or did, for the live view of the operator. Seq grows by one per event.
type Event struct {
	Seq     uint64
	At      time.Time
	Kind    string // solicit, announce
	Target  netip.Addr
	From    netip.Addr
	Verdict string
}

// maxEvents is the size of the ring of recent events.
const maxEvents = 256

// maxMissed bounds the addresses the proxy remembers a miss for, so that a scan of the segment cannot grow it.
const maxMissed = 64

type addrState struct {
	AddrStats
}

// SetPrefixes tells the proxy which prefixes belong to the networks with the NDP proxy: a solicitation inside one for an
// address that is not held is counted as a miss. It changes no answer.
func (p *Proxy) SetPrefixes(prefixes []netip.Prefix) {
	p.mu.Lock()
	p.prefixes = append([]netip.Prefix(nil), prefixes...)
	p.mu.Unlock()
}

func (p *Proxy) inPrefix(a netip.Addr) bool {
	for _, pf := range p.prefixes {
		if pf.Contains(a) {
			return true
		}
	}
	return false
}

// noteAdded starts the statistics of an address; p.mu is held.
func (p *Proxy) noteAdded(a netip.Addr, iface string) {
	st := p.stats[a]
	if st == nil {
		st = &addrState{}
		p.stats[a] = st
	}
	st.Addr, st.Iface, st.Held, st.Since = a, iface, true, p.deps.Clock.Now()
	if m := p.missed[a]; m != nil { // the miss history of the address stays with it
		st.Missed, st.LastMissed = m.Missed, m.LastMissed
		delete(p.missed, a)
	}
}

// noteIface records the interface of the host route of an address (empty when the address has no route); p.mu is held.
func (p *Proxy) noteIface(a netip.Addr, iface string) {
	if st := p.stats[a]; st != nil {
		st.Iface = iface
	}
}

// noteDropped ends the holding of an address; the counters stay until the address is added again; p.mu is held.
func (p *Proxy) noteDropped(a netip.Addr) {
	if st := p.stats[a]; st != nil {
		st.Held = false
	}
}

// noteAnswer records an advertisement for a solicitation or an announcement; err is the reason it was not sent.
func (p *Proxy) noteSent(a netip.Addr, announce bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.stats[a]
	if st == nil {
		return
	}
	if err != nil {
		st.LastError = err.Error()
		return
	}
	now := p.deps.Clock.Now()
	if announce {
		st.Announced++
		st.LastAnnounce = now
	} else {
		st.Answered++
		st.LastAnswer = now
	}
}

// noteMissed records a solicitation for an address inside an NDP proxy prefix that the proxy does not hold.
func (p *Proxy) noteMissed(a netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.inPrefix(a) {
		return
	}
	if st := p.stats[a]; st != nil && !st.Held {
		st.Missed++
		st.LastMissed = p.deps.Clock.Now()
		return
	}
	m := p.missed[a]
	if m == nil {
		if len(p.missed) >= maxMissed {
			return
		}
		m = &AddrStats{Addr: a}
		p.missed[a] = m
	}
	m.Missed++
	m.LastMissed = p.deps.Clock.Now()
}

// Addresses lists the statistics of every address the proxy holds or was asked for, in address order.
func (p *Proxy) Addresses() []AddrStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]AddrStats, 0, len(p.stats)+len(p.missed))
	for _, st := range p.stats {
		out = append(out, st.AddrStats)
	}
	for _, m := range p.missed {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr.Less(out[j].Addr) })
	return out
}

// record notes one event: it counts it for the address and keeps it in the ring of recent events.
func (p *Proxy) record(kind string, target, from netip.Addr, verdict string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.deps.Clock.Now()
	p.seq++
	p.lastSeq = eventSeq.Add(1)
	ev := Event{Seq: p.lastSeq, At: now, Kind: kind, Target: target, From: from, Verdict: verdict}
	if len(p.ring) < maxEvents {
		p.ring = append(p.ring, ev)
	} else {
		p.ring[int((p.seq-1)%maxEvents)] = ev
	}
	if kind != "solicit" {
		return
	}
	st := p.stats[target]
	var s *AddrStats
	switch {
	case st != nil:
		s = &st.AddrStats
	case p.inPrefix(target):
		if p.missed[target] == nil {
			if len(p.missed) >= maxMissed {
				return
			}
			p.missed[target] = &AddrStats{Addr: target}
		}
		s = p.missed[target]
	default:
		return
	}
	s.Seen++
	s.LastSeen, s.LastFrom, s.LastVerdict = now, from, verdict
	if len(verdict) >= 7 && (verdict[:7] == "dropped" || verdict[:2] == "no") {
		s.Dropped++
	}
}

// eventSeq numbers the events of every proxy of the process in one sequence, so that one cursor ("the newest I have
// seen") is right for a daemon with proxies on several uplinks.
var eventSeq atomic.Uint64

// EventsAfter returns the events with a sequence number above after, oldest first, and the newest sequence number.
func (p *Proxy) EventsAfter(after uint64) ([]Event, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Event
	for _, ev := range p.ring {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, p.lastSeq
}
