package service

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

// hostAddrCache keeps the host's addresses for a moment: every jail's anchor names them (the table <host>), and the
// reconcile loop renders every anchor on each pass.
type hostAddrCache struct {
	mu    sync.Mutex
	at    time.Time
	addrs []netip.Addr
}

// hostAddrTTL is how long a reading of the host's addresses is used.
const hostAddrTTL = 5 * time.Second

// hostAddrs are the addresses of the host's own interfaces (the uplink's, the gateways on the jails' links, ...), without
// the loopback's: what a jail must not reach unless a rule says so. Nil when the interfaces cannot be read here: a
// caller that renders a firewall uses hostAddrsChecked, which says so.
func (s *Service) hostAddrs(ctx context.Context) []netip.Addr {
	a, _ := s.hostAddrsChecked(ctx)
	return a
}

// hostAddrsChecked is hostAddrs with the failure: an anchor rendered from a table that is empty because the interfaces
// could not be listed would not close the host, so the render stops. Without a network reader (a test host with no
// FreeBSD under it) there is nothing to read and no error. A reading is used for hostAddrTTL, and every render of the
// reconcile pass reads again: an address the host gets later (a lease, an alias, a tunnel) is in the tables of every
// anchor after the next pass.
func (s *Service) hostAddrsChecked(ctx context.Context) ([]netip.Addr, error) {
	if s.d.Net == nil {
		return nil, nil
	}
	c := &s.hostAddr
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.addrs != nil && s.now().Sub(c.at) < hostAddrTTL {
		return append([]netip.Addr(nil), c.addrs...), nil
	}
	list, err := s.d.Net.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	for _, i := range list {
		for _, p := range append(append([]netip.Prefix(nil), i.Inet...), i.Inet6...) {
			a := p.Addr().WithZone("")
			if a.IsLoopback() || a.IsUnspecified() || seen[a] {
				continue
			}
			seen[a] = true
			out = append(out, a)
		}
	}
	c.at, c.addrs = s.now(), out
	return append([]netip.Addr(nil), out...), nil
}
