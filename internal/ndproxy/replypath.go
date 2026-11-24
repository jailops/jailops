package ndproxy

import (
	"net/netip"
	"sort"
	"sync"
	"time"
)

// The reply path of the host's own neighbor advertisements.
//
// A host that holds its address as a /128, with its default route through a gateway (a VRRP pair of the provider) and no
// on-link route for the provider's routers, answers a unicast solicitation from a router's global address through the
// routing table: the advertisement goes to the gateway's MAC, not to the router that asked. When that router is the
// VRRP backup it never gets the answer, asks again three times a second apart and only then falls back to a multicast
// solicitation from its link-local address; traffic through it stalls for those seconds. Linux answers on the
// interface the solicitation came in on, to the neighbor's link-layer address.
//
// The capture of the proxy sees the uplink in both directions, so it sees the solicitation and the host's answer.
// replyPath keeps, per (router, host address), the repeated solicitations and where the host's answers went. It only
// observes: the host is not changed. The addresses of the jails are not part of it: the proxy answers for them itself,
// to the asker's MAC.

const (
	rpWindow  = 5 * time.Second        // the repeats of one router count within this window
	rpRepeats = 3                      // this many solicitations within the window are a repeat
	rpMinGap  = 500 * time.Millisecond // solicitations closer than this are one (a retransmission is a second apart)
	rpKeep    = 24 * time.Hour         // a pair without a repeat for this long is forgotten
	rpMax     = 256                    // pairs kept at most
	rpHosts   = 64                     // addresses of the host learned from its advertisements, at most
)

type rpKey struct{ router, host netip.Addr }

type rpState struct {
	routerMAC  MAC
	times      []time.Time // the solicitations within the window
	lastNS     time.Time
	repeats    uint64
	lastRepeat time.Time
	naDst      MAC
	misdirect  bool
}

// ReplyPath is one router that asked for an address of the host repeatedly.
type ReplyPath struct {
	Router, Host netip.Addr
	RouterMAC    MAC
	NADst        MAC // zero when no answer of the host was seen
	Repeats      uint64
	LastSeen     time.Time
	Misdirected  bool
}

type replyPath struct {
	mu       sync.Mutex
	pairs    map[rpKey]*rpState
	hosts    map[netip.Addr]time.Time // addresses of the host, learned from its own advertisements
	solicits uint64
}

func (r *replyPath) init() {
	if r.pairs == nil {
		r.pairs = map[rpKey]*rpState{}
		r.hosts = map[netip.Addr]time.Time{}
	}
}

// solicit notes a solicitation for an address that is not one of the proxy's. Only one from a router's global address
// is followed, and only when it is sent to the host's MAC or asks for an address the host has answered for.
func (r *replyPath) solicit(pkt *Packet, routerMAC, host MAC, now time.Time) {
	if !pkt.Src.IsGlobalUnicast() || pkt.Src.IsLinkLocalUnicast() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	if _, ok := r.hosts[pkt.Target]; !ok && pkt.EthDst != host {
		return
	}
	r.solicits++
	k := rpKey{pkt.Src, pkt.Target}
	st := r.pairs[k]
	if st == nil {
		if len(r.pairs) >= rpMax && !r.evict(now) {
			return
		}
		st = &rpState{}
		r.pairs[k] = st
	}
	st.routerMAC = routerMAC
	keep := st.times[:0]
	for _, t := range st.times {
		if now.Sub(t) < rpWindow {
			keep = append(keep, t)
		}
	}
	st.times = keep
	if n := len(st.times); n == 0 || now.Sub(st.times[n-1]) >= rpMinGap {
		st.times = append(st.times, now)
		if len(st.times) >= rpRepeats {
			st.repeats++
			st.lastRepeat = now
		}
	}
	st.lastNS = now
}

// advert notes an advertisement the host sent (Ethernet source the host's MAC) for an address that is not one of the
// proxy's: the target is an address of the host, and the Ethernet destination is where the answer went.
func (r *replyPath) advert(pkt *Packet, now time.Time) {
	if !pkt.Target.IsValid() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	if _, ok := r.hosts[pkt.Target]; ok || len(r.hosts) < rpHosts {
		r.hosts[pkt.Target] = now
	}
	st := r.pairs[rpKey{pkt.Dst, pkt.Target}]
	if st == nil || pkt.EthDst.IsMulticast() {
		return
	}
	st.naDst = pkt.EthDst
	st.misdirect = pkt.EthDst != st.routerMAC
}

// evict makes room: pairs not seen for rpKeep, then pairs without a repeat outside the window. It reports whether
// there is room now. The caller holds mu.
func (r *replyPath) evict(now time.Time) bool {
	for k, st := range r.pairs {
		if now.Sub(st.lastNS) > rpKeep || (st.repeats == 0 && now.Sub(st.lastNS) >= rpWindow) {
			delete(r.pairs, k)
		}
	}
	return len(r.pairs) < rpMax
}

// list returns the pairs with a repeat in the last rpKeep, by router and host address.
func (r *replyPath) list(now time.Time) ([]ReplyPath, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ReplyPath
	for k, st := range r.pairs {
		if st.repeats == 0 || now.Sub(st.lastRepeat) > rpKeep {
			continue
		}
		out = append(out, ReplyPath{Router: k.router, Host: k.host, RouterMAC: st.routerMAC, NADst: st.naDst,
			Repeats: st.repeats, LastSeen: st.lastRepeat, Misdirected: st.misdirect})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Router != out[j].Router {
			return out[i].Router.Less(out[j].Router)
		}
		return out[i].Host.Less(out[j].Host)
	})
	return out, r.solicits
}

// ReplyPaths returns the routers that asked for an address of the host repeatedly, and the number of solicitations for
// an address of the host seen since the proxy started.
func (p *Proxy) ReplyPaths() ([]ReplyPath, uint64) {
	return p.reply.list(p.deps.Clock.Now())
}
