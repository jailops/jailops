package service

import (
	"context"
	"sort"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/sdk/api"
)

// NDPStatus describes the neighbor discovery proxy of every uplink (GET /api/v1/ndp).
func (s *Service) NDPStatus(_ context.Context) api.NDPStatus {
	st := api.NDPStatus{Uplinks: []api.NDPUplink{}, Networks: []api.NDPNetwork{}}
	if s.d.Reconciler != nil {
		st = s.d.Reconciler.NDPStatus()
	}
	objs, _ := s.netKind.list()
	for _, o := range objs {
		v6 := o.Spec.IPv6
		if v6 == nil || v6.Mode == api.IPv6None {
			continue
		}
		n := api.NDPNetwork{Name: o.Meta.Name, Enabled: v6.Mode == api.IPv6Proxy, Uplink: o.Spec.Uplink, Prefix: v6.Subnet, Range: v6.Range}
		if n.Enabled {
			addrs, more, _ := ipam.ProxyAddrs6(v6)
			n.Answers, n.Beyond = len(addrs), more
		}
		st.Networks = append(st.Networks, n)
	}
	sort.Slice(st.Networks, func(i, j int) bool { return st.Networks[i].Name < st.Networks[j].Name })
	return st
}

// NDPEvents returns the recent events of the proxies above a sequence number (GET /api/v1/ndp/events?after=N).
func (s *Service) NDPEvents(_ context.Context, after uint64) api.NDPEvents {
	if s.d.Reconciler == nil {
		return api.NDPEvents{Events: []api.NDPEvent{}}
	}
	return s.d.Reconciler.NDPEvents(after)
}
