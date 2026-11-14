package ndproxy

import (
	"fmt"
	"time"
)

// Bounds on what a neighbor on the uplink can make the proxy spend. They are fixed numbers, not settings: a segment of
// jails needs a few solicitations a second, so these never touch normal use, and a flood meets them whatever the
// operator would have set.
const (
	// solicitPerSource is how many solicitations one requester MAC may send per second (and solicitBurst how many at
	// once). The per-target budget alone lets a source that asks for many different addresses use the whole answer
	// budget, which the router needs.
	solicitPerSource = 50
	solicitBurst     = 100
	// solicitTotal is the budget of all solicitations the proxy looks at per second, whoever sends them.
	solicitTotal      = 2000
	solicitTotalBurst = 4000
	// sourceTableMax bounds the table of requesters; when it is full the oldest entries go first.
	sourceTableMax = 4096
	// floodLogEvery is the longest rate at which the proxy writes down that it dropped solicitations.
	floodLogEvery = time.Minute
	floodWindow   = time.Hour
)

// sourceBucket is the token bucket of one requester.
type sourceBucket struct {
	tokenBucket
}

// admit applies the budgets to one solicitation of the requester mac: the budget of all solicitations, then the
// budget of the requester.
func (p *Proxy) admit(mac MAC) bool {
	now := p.deps.Clock.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.total.take(now, solicitTotal, solicitTotalBurst) {
		return false
	}
	b := p.sources[mac]
	if b == nil {
		if len(p.sources) >= sourceTableMax {
			p.evictSources(now)
		}
		b = &sourceBucket{}
		p.sources[mac] = b
	}
	return b.take(now, solicitPerSource, solicitBurst)
}

// evictSources makes room in the table of requesters: the buckets that have filled up again (the requester is quiet)
// go first; when none has, the one that was used longest ago.
func (p *Proxy) evictSources(now time.Time) {
	var oldest MAC
	var oldestAt time.Time
	for m, b := range p.sources {
		if now.Sub(b.last).Seconds()*solicitPerSource >= solicitBurst {
			delete(p.sources, m)
			continue
		}
		if oldestAt.IsZero() || b.last.Before(oldestAt) {
			oldest, oldestAt = m, b.last
		}
	}
	if len(p.sources) >= sourceTableMax {
		delete(p.sources, oldest)
	}
}

// flood counts a solicitation that was dropped by a bound or a rate limit, keeps the count of the last hour for
// doctor, and writes it down at most once a minute.
func (p *Proxy) flood() {
	p.m.floods.Add(1)
	p.m.dropped.Add(1)
	now := p.deps.Clock.Now()
	p.mu.Lock()
	p.floodLog.add(now)
	p.floodSince++
	var msg string
	if p.floodLogged.IsZero() || now.Sub(p.floodLogged) >= floodLogEvery {
		msg = fmt.Sprintf("neighbor discovery proxy on %s: dropped %d solicitation(s) over its limits (a neighbor on the uplink is sending too many)", p.cfg.Uplink, p.floodSince)
		p.floodSince, p.floodLogged = 0, now
	}
	p.mu.Unlock()
	if msg != "" && p.deps.Logf != nil {
		p.deps.Logf("%s", msg)
	}
}

// FloodsLastHour is how many solicitations the limits dropped in the last hour.
func (p *Proxy) FloodsLastHour() uint64 {
	now := p.deps.Clock.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.floodLog.sum(now)
}

// hourLog counts events per minute for the last hour.
type hourLog struct {
	minute [60]int64 // the minute (Unix time / 60) each slot holds
	count  [60]uint64
}

func (h *hourLog) add(now time.Time) {
	m := now.Unix() / 60
	i := int(m % 60)
	if h.minute[i] != m {
		h.minute[i], h.count[i] = m, 0
	}
	h.count[i]++
}

func (h *hourLog) sum(now time.Time) uint64 {
	m := now.Unix() / 60
	var n uint64
	for i := range h.minute {
		if h.minute[i] > m-60 && h.minute[i] <= m {
			n += h.count[i]
		}
	}
	return n
}
