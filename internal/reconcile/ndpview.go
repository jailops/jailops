package reconcile

import (
	"net/netip"
	"sort"
	"time"

	"jailops.net/jailops/internal/ndproxy"
	"jailops.net/jailops/sdk/api"
)

// ndpEvents is the part of a proxy that keeps the ring of recent events (the real proxy has it; a fake need not).
type ndpEvents interface {
	EventsAfter(after uint64) ([]ndproxy.Event, uint64)
}

// ndpPrefixer is the part of a proxy that takes the prefixes of the networks with the NDP proxy, to count the solicitations
// for addresses that are not held.
type ndpPrefixer interface {
	SetPrefixes(prefixes []netip.Prefix)
}

func tptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// NDPStatus describes the neighbor discovery proxy of every uplink, for jailops ndp status.
func (r *Reconciler) NDPStatus() api.NDPStatus {
	r.mu.Lock()
	view := make(map[string]NDP, len(r.ndpView))
	for k, v := range r.ndpView {
		view[k] = v
	}
	r.mu.Unlock()
	out := api.NDPStatus{Uplinks: []api.NDPUplink{}, Networks: []api.NDPNetwork{}}
	for uplink, n := range view {
		st := n.Status()
		u := api.NDPUplink{Uplink: uplink, Open: st.Open, Error: st.Err, Reopens: st.Reopens, Addresses: st.Addresses,
			Answered: st.Answered, Announced: st.Announced, Dropped: st.Dropped, Floods: st.Floods, InjectErrors: st.InjectErrors}
		for _, a := range st.Addrs {
			u.Addrs = append(u.Addrs, api.NDPAddress{Address: a.Addr.String(), Interface: a.Iface, Held: a.Held, Since: tptr(a.Since),
				Seen: a.Seen, Answered: a.Answered, Dropped: a.Dropped, Announced: a.Announced,
				LastSeen: tptr(a.LastSeen), LastFrom: addrString(a.LastFrom), LastVerdict: a.LastVerdict,
				LastAnswer: tptr(a.LastAnswer), LastAnnounce: tptr(a.LastAnnounce), LastError: a.LastError})
		}
		u.HostSolicits = st.HostSolicits
		for _, rp := range st.ReplyPath {
			e := api.NDPReplyPath{Uplink: uplink, Router: rp.Router.String(), HostAddress: rp.Host.String(), Repeats: rp.Repeats,
				LastSeen: rp.LastSeen, RouterMAC: rp.RouterMAC.String(), Misdirected: rp.Misdirected}
			if !rp.NADst.IsZero() {
				e.NADstMAC = rp.NADst.String()
			}
			out.ReplyPath = append(out.ReplyPath, e)
		}
		out.Uplinks = append(out.Uplinks, u)
	}
	sort.Slice(out.Uplinks, func(i, j int) bool { return out.Uplinks[i].Uplink < out.Uplinks[j].Uplink })
	sort.SliceStable(out.ReplyPath, func(i, j int) bool { return out.ReplyPath[i].Uplink < out.ReplyPath[j].Uplink })
	return out
}

// NDPEvents returns the recent events of the proxies with a sequence number above after. The proxies of one daemon
// share one sequence, so the newest number seen (Last) is the cursor for all uplinks.
func (r *Reconciler) NDPEvents(after uint64) api.NDPEvents {
	r.mu.Lock()
	view := make(map[string]NDP, len(r.ndpView))
	for k, v := range r.ndpView {
		view[k] = v
	}
	r.mu.Unlock()
	out := api.NDPEvents{Events: []api.NDPEvent{}}
	for uplink, n := range view {
		e, ok := n.(ndpEvents)
		if !ok {
			continue
		}
		evs, last := e.EventsAfter(after)
		if last > out.Last {
			out.Last = last
		}
		for _, ev := range evs {
			out.Events = append(out.Events, api.NDPEvent{Seq: ev.Seq, Time: ev.At, Uplink: uplink, Kind: ev.Kind,
				Target: ev.Target.String(), From: addrString(ev.From), Verdict: ev.Verdict})
		}
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].Time.Before(out.Events[j].Time) })
	return out
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}
