package reconcile

import (
	"context"
	"net/netip"
	"sort"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// staleRoutePass removes the /128 host routes of proxy-mode networks that belong to no jail. A route outlives its jail
// when the daemon crashes between starting a jail and stopping it, when a jail is removed behind the daemon's back,
// or across a reboot of a host that keeps routes (a persistent route): the address then leads to a gateway interface
// with nobody behind it, and the proxy of a new jail that is given the address answers for a route that points
// nowhere useful. A route is stale when it leaves through the gateway interface of a network of ours and its address
// is not one of a jail that is, or is being, run: a jail that is stopped has no route, and a jail that is locked (a
// start or stop is in progress) is left alone. Nothing is touched when a jail could not be rendered, since then its
// addresses are unknown.
func (r *Reconciler) staleRoutePass(ctx context.Context, des Desired, obs *Observed, ex *NetExec, rep *Report) {
	r.staleNeighborPass(ctx, des, obs, ex, rep)
	if r.env.HostRoutes == nil {
		return
	}
	gateways := map[string]bool{}
	for _, n := range des.Networks {
		if n.Spec.IPv6 != nil && n.Spec.IPv6.Mode == api.IPv6Proxy {
			gateways[ipam.GatewayIface(n.Bridge)] = true
		}
	}
	if len(gateways) == 0 {
		return
	}
	keep := map[netip.Addr]bool{}
	for _, j := range des.Jails {
		if j.Problem != "" {
			return
		}
		_, up := obs.Running[j.Name]
		live := up || (r.env.Actions != nil && r.env.Actions.IsLocked("jail/"+j.Name))
		for _, p := range j.Proxy {
			a, err := netip.ParseAddr(p.Addr)
			if err != nil {
				continue
			}
			// a running jail, or one whose start or stop is in progress, owns its route
			if live {
				keep[a] = true
			}
		}
	}
	routes, err := r.env.HostRoutes(ctx)
	if err != nil {
		rep.Failures["routes"] = err.Error()
		return
	}
	var stale []netip.Addr
	for a, iface := range routes {
		if gateways[iface] && !keep[a] {
			stale = append(stale, a)
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].Less(stale[j]) })
	for _, a := range stale {
		st := render.Step{Op: render.OpDelHostRoute, Phase: render.PhaseHost, Iface: routes[a], Arg: netip.PrefixFrom(a, 128).String()}
		if err := ex.Exec(ctx, st, ""); err != nil {
			rep.Failures["route/"+a.String()] = err.Error()
			continue
		}
		rep.Applied = append(rep.Applied, st.Describe())
		rep.StaleRoutes = append(rep.StaleRoutes, a.String())
	}
}

// staleNeighborPass removes the permanent neighbor entries (link-local IPv6 and IPv4) the host made for a jail (render.OpSetNeighbor)
// that is gone: the jail was removed behind the daemon's back or the daemon died between start and stop. The global
// entry goes with the host route. An entry is a jail's when it sits on the gateway interface of a network of ours,
// its address is the link-local address the kernel forms from the MAC it points at (the host's own fe80::1 is not),
// and no running or locked jail has it among its attach steps.
func (r *Reconciler) staleNeighborPass(ctx context.Context, des Desired, obs *Observed, ex *NetExec, rep *Report) {
	if r.env.HostNeighbors == nil {
		return
	}
	keepHost := map[netip.Addr]bool{} // the networks' own gateway addresses
	gateways := map[string]bool{}
	v4 := map[string]netip.Prefix{} // gateway interface -> subnet whose entries are jails'
	for _, n := range des.Networks {
		gw := ipam.GatewayIface(n.Bridge)
		gateways[gw] = true
		if n.Spec.IPv4 != nil {
			if p, err := netip.ParsePrefix(n.Spec.IPv4.Subnet); err == nil {
				v4[gw] = p.Masked()
			}
			if g, err := netip.ParseAddr(n.Spec.IPv4.Gateway); err == nil {
				keepHost[g] = true
			}
		}
	}
	keep := map[netip.Addr]bool{}
	for _, j := range des.Jails {
		if j.Problem != "" {
			return
		}
		_, up := obs.Running[j.Name]
		if !up && !(r.env.Actions != nil && r.env.Actions.IsLocked("jail/"+j.Name)) {
			continue
		}
		for _, steps := range j.Attach {
			for _, s := range steps {
				if s.Op == render.OpSetNeighbor {
					if a, err := netip.ParseAddr(s.Arg); err == nil {
						keep[a] = true
					}
				}
			}
		}
	}
	list, err := r.env.HostNeighbors(ctx)
	if err != nil {
		rep.Failures["neighbors"] = err.Error()
		return
	}
	for _, nb := range list {
		if !gateways[nb.Iface] || keep[nb.Addr] {
			continue
		}
		if nb.Addr.Is4() {
			// an IPv4 entry is a jail's when it is in the network's subnet and is not the host's own address
			if p, ok := v4[nb.Iface]; !ok || !p.Contains(nb.Addr) || keepHost[nb.Addr] {
				continue
			}
		} else if ll, err := ipam.LinkLocalFromMAC(nb.MAC); err != nil || ll != nb.Addr {
			continue // not formed from the MAC it points at: not a jail's
		}
		st := render.Step{Op: render.OpDelNeighbor, Phase: render.PhaseHost, Iface: nb.Iface, Arg: nb.Addr.String()}
		if err := ex.Exec(ctx, st, ""); err != nil {
			rep.Failures["neighbor/"+nb.Addr.String()] = err.Error()
			continue
		}
		rep.Applied = append(rep.Applied, st.Describe())
		rep.StaleNeighbors = append(rep.StaleNeighbors, nb.Addr.String())
	}
}
