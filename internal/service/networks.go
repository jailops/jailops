package service

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

func (s *Service) networkToAPI(ctx context.Context, o object[api.NetworkSpec]) api.Network {
	n := api.Network{Metadata: o.Meta, Spec: o.Spec}
	s.mu.RLock()
	n.Status = s.netStat[o.Meta.Name]
	s.mu.RUnlock()
	if a, f, ok := s.networkCounts(o.Spec, o.Meta.Name); ok {
		n.Status.Allocated, n.Status.Free = a, f
	}
	if a := ActorFrom(ctx); !a.Global || ProjectFrom(ctx) != "" {
		n.Status.Orphans = nil // host interfaces are not the business of a project
		n.Status.Members = nil
	}
	return n
}

// networkCounts reports allocated and free addresses of the IPv4 range (IPv6
// ranges are never enumerated). A network belongs to one project, so the range is the whole one.
func (s *Service) networkCounts(spec api.NetworkSpec, name string) (alloc, free int, ok bool) {
	if spec.IPv4 == nil {
		return 0, 0, false
	}
	a, err := ipam.NewV4In(spec.IPv4, ipam.Scope{}, s.addressesOn(name, "", false))
	if err != nil {
		return 0, 0, false
	}
	al, fr := a.Stats()
	return al, fr, true
}

// networkVisible says whether the caller may see a network: global identities
// see all, project identities the networks their projects are granted.
func (s *Service) networkVisible(ctx context.Context, name string) bool {
	a := ActorFrom(ctx)
	if a.Global && ProjectFrom(ctx) == "" {
		return true
	}
	projects := a.Projects
	if p := ProjectFrom(ctx); p != "" {
		if !a.CanProject(p) {
			return false
		}
		projects = []string{p}
	}
	for _, p := range projects {
		if o, err := s.projKind.get(p); err == nil {
			if _, ok := specGrantsNetwork(o.Spec, name); ok {
				return true
			}
		}
	}
	return false
}

// addressesOn lists the addresses of jails on a network, excluding one jail.
func (s *Service) addressesOn(network, except string, v6 bool) []netip.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []netip.Addr
	for name, e := range s.jails {
		if name == except {
			continue
		}
		for _, a := range e.jail.Spec.Network {
			if a.Network != network {
				continue
			}
			v := a.IPv4
			if v6 {
				v = a.IPv6
			}
			if ad, ok := parseAddr(v); ok {
				out = append(out, ad)
			}
		}
	}
	return out
}

func parseAddr(v string) (netip.Addr, bool) {
	if v == "" || v == "none" || v == "auto" {
		return netip.Addr{}, false
	}
	if p, err := netip.ParsePrefix(v); err == nil {
		return p.Addr(), true
	}
	a, err := netip.ParseAddr(v)
	return a, err == nil
}

// ListNetworks returns the networks the caller may see.
func (s *Service) ListNetworks(ctx context.Context, labels map[string]string) ([]api.Network, error) {
	if !ActorFrom(ctx).HasScope() {
		return nil, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	objs, err := s.netKind.list()
	if err != nil {
		return nil, err
	}
	out := []api.Network{}
	for _, o := range objs {
		if MatchLabels(o.Meta.Labels, labels) && s.networkVisible(ctx, o.Meta.Name) {
			out = append(out, s.networkToAPI(ctx, o))
		}
	}
	return out, nil
}

// GetNetwork returns one network; one that is not granted to the caller's
// projects is not_found.
func (s *Service) GetNetwork(ctx context.Context, name string) (api.Network, error) {
	if !ActorFrom(ctx).HasScope() {
		return api.Network{}, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	o, err := s.netKind.get(name)
	if err != nil || !s.networkVisible(ctx, name) {
		return api.Network{}, notFound("network", name)
	}
	return s.networkToAPI(ctx, o), nil
}

func (s *Service) validateNetwork(name string, spec *api.NetworkSpec, self string) error {
	if err := validate.Name("name", name); err != nil {
		return err
	}
	if err := validate.NetworkSpec(*spec); err != nil {
		return err
	}
	objs, _ := s.netKind.list()
	for _, o := range objs {
		if o.Meta.Name == self {
			continue
		}
		if spec.Bridge != "" && o.Spec.Bridge == spec.Bridge {
			return api.Errorf(api.CodeAlreadyExists, "bridge %s is already used by network %q", spec.Bridge, o.Meta.Name)
		}
		// Two networks whose subnets overlap cannot both be routed by the host, in any project: an address of one
		// would be answered, routed and filtered for the other.
		// (a network of the same name is reported as existing by the caller)
		if p, q, ok := overlappingSubnets(*spec, o.Spec); ok && o.Meta.Name != name {
			return api.Errorf(api.CodeConflict, "subnet %s overlaps subnet %s of network %q", p, q, o.Meta.Name).
				WithHint("choose a subnet that no other network uses")
		}
	}
	return nil
}

// overlappingSubnets returns the first pair of subnets, one of each network and of one family, that overlap.
func overlappingSubnets(a, b api.NetworkSpec) (netip.Prefix, netip.Prefix, bool) {
	subnets := func(s api.NetworkSpec) []netip.Prefix {
		var out []netip.Prefix
		if s.IPv4 != nil {
			if p, err := netip.ParsePrefix(s.IPv4.Subnet); err == nil {
				out = append(out, p.Masked())
			}
		}
		if s.IPv6 != nil && s.IPv6.Mode != api.IPv6None {
			if p, err := netip.ParsePrefix(s.IPv6.Subnet); err == nil {
				out = append(out, p.Masked())
			}
		}
		return out
	}
	for _, p := range subnets(a) {
		for _, q := range subnets(b) {
			if p.Overlaps(q) {
				return p, q, true
			}
		}
	}
	return netip.Prefix{}, netip.Prefix{}, false
}

// nextBridge returns the lowest unused "jo<n>".
func (s *Service) nextBridge() (string, error) {
	objs, err := s.netKind.list()
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, o := range objs {
		used[o.Spec.Bridge] = true
	}
	for i := 0; i < 1000; i++ {
		if b := ipam.BridgeName(i); !used[b] {
			return b, nil
		}
	}
	return "", api.Errorf(api.CodePoolExhausted, "no free bridge name")
}

// CreateNetwork stores a network. The bridge name is assigned here when empty.
func (s *Service) CreateNetwork(ctx context.Context, n api.Network) (api.Network, error) {
	if err := requireGlobal(ctx, "creating a network"); err != nil {
		return api.Network{}, err
	}
	name := n.Metadata.Name
	if err := s.validateNetwork(name, &n.Spec, ""); err != nil {
		return api.Network{}, err
	}
	unlock, err := s.locks.lockPatient(ctx, "create network "+name, networkLock(name), "network/_bridges")
	if err != nil {
		return api.Network{}, err
	}
	defer unlock()
	if s.netKind.exists(name) {
		return api.Network{}, api.Errorf(api.CodeAlreadyExists, "network %q already exists", name)
	}
	if n.Spec.Bridge == "" {
		if n.Spec.Bridge, err = s.nextBridge(); err != nil {
			return api.Network{}, err
		}
	}
	if err := s.validateNetwork(name, &n.Spec, ""); err != nil {
		return api.Network{}, err
	}
	o, err := s.netKind.create(ctx, n.Metadata, n.Spec)
	if err != nil {
		return api.Network{}, err
	}
	s.kick()
	return s.networkToAPI(ctx, o), nil
}

// UpdateNetwork replaces a network.
func (s *Service) UpdateNetwork(ctx context.Context, name string, n api.Network, expect int64) (api.Network, error) {
	if err := requireGlobal(ctx, "changing a network"); err != nil {
		return api.Network{}, err
	}
	unlock, err := s.locks.lockPatient(ctx, "update network "+name, networkLock(name), "network/_bridges")
	if err != nil {
		return api.Network{}, err
	}
	defer unlock()
	old, err := s.netKind.get(name)
	if err != nil {
		return api.Network{}, err
	}
	if n.Spec.Bridge == "" {
		n.Spec.Bridge = old.Spec.Bridge
	}
	if n.Spec.Bridge != old.Spec.Bridge {
		return api.Network{}, invalid("the bridge of a network cannot change").WithHint("create a new network and move the jails")
	}
	if n.Metadata.Name != "" && n.Metadata.Name != name {
		return api.Network{}, invalid("renaming is not supported")
	}
	if err := s.validateNetwork(name, &n.Spec, name); err != nil {
		return api.Network{}, err
	}
	if err := s.checkNetworkFits(name, n.Spec); err != nil {
		return api.Network{}, err
	}
	o, err := s.netKind.update(ctx, name, expect, n.Metadata.Labels, &n.Metadata.ManagedBy, n.Spec)
	if err != nil {
		return api.Network{}, err
	}
	s.kick()
	return s.networkToAPI(ctx, o), nil
}

// checkNetworkFits rejects a spec under which an attached jail's address
// would fall outside the subnet or on the gateway.
func (s *Service) checkNetworkFits(name string, spec api.NetworkSpec) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.jails {
		jn := jailDisplay(e.jail.Metadata.Project, e.jail.Metadata.Name)
		for _, a := range e.jail.Spec.Network {
			if a.Network != name {
				continue
			}
			for _, v6 := range []bool{false, true} {
				v := a.IPv4
				var sub, gw string
				if v6 {
					v = a.IPv6
					if spec.IPv6 != nil {
						sub, gw = spec.IPv6.Subnet, spec.IPv6.Gateway
					}
				} else if spec.IPv4 != nil {
					sub, gw = spec.IPv4.Subnet, spec.IPv4.Gateway
				}
				ad, ok := parseAddr(v)
				if !ok {
					continue
				}
				p, err := netip.ParsePrefix(sub)
				if err != nil || !p.Contains(ad) || gw == ad.String() {
					return api.Errorf(api.CodeIPConflict, "jail %q has address %s which the new network definition does not allow", jn, ad).
						WithDetail("address", ad.String())
				}
			}
		}
	}
	return nil
}

// PatchNetwork applies a merge patch.
func (s *Service) PatchNetwork(ctx context.Context, name string, patch map[string]any, expect int64) (api.Network, error) {
	if err := requireGlobal(ctx, "changing a network"); err != nil {
		return api.Network{}, err
	}
	cur, err := s.GetNetwork(ctx, name)
	if err != nil {
		return api.Network{}, err
	}
	if expect != AnyGeneration && expect != cur.Metadata.Generation {
		return api.Network{}, api.Errorf(api.CodeConflict, "network %q changed: current generation is %d", name, cur.Metadata.Generation)
	}
	var out api.Network
	if err := MergePatch(cur, patch, &out); err != nil {
		return api.Network{}, err
	}
	return s.UpdateNetwork(ctx, name, out, cur.Metadata.Generation)
}

// DeleteNetwork removes a network that no jail uses and, when JailOps owns
// the bridge and it has no members, the bridge as well.
func (s *Service) DeleteNetwork(ctx context.Context, name string, expect int64) error {
	if err := requireGlobal(ctx, "deleting a network"); err != nil {
		return err
	}
	unlock, err := s.locks.lockPatient(ctx, "delete network "+name, networkLock(name), "network/_bridges")
	if err != nil {
		return err
	}
	defer unlock()
	o, err := s.netKind.get(name)
	if err != nil {
		return err
	}
	s.mu.RLock()
	for _, e := range s.jails {
		for _, a := range e.jail.Spec.Network {
			if a.Network == name {
				s.mu.RUnlock()
				return api.Errorf(api.CodeBusy, "network %q is used by jail %q", name, jailDisplay(e.jail.Metadata.Project, e.jail.Metadata.Name)).
					WithHint("detach or delete the jail first")
			}
		}
	}
	s.mu.RUnlock()
	if projs, err := s.projKind.list(); err == nil {
		for _, p := range projs {
			for _, g := range p.Spec.Networks {
				if g.Network == name {
					return api.Errorf(api.CodeBusy, "network %q is granted to project %q", name, p.Meta.Name).WithHint("remove the grant first")
				}
			}
		}
	}
	if err := s.netKind.delete(ctx, name, expect); err != nil {
		return err
	}
	s.teardownNetwork(ctx, o)
	s.mu.Lock()
	delete(s.netStat, name)
	s.mu.Unlock()
	return nil
}

// teardownNetwork removes the bridge of a deleted network when it is ours.
func (s *Service) teardownNetwork(ctx context.Context, o object[api.NetworkSpec]) {
	// the anchor of a network holds the pass of its gateway port as well as the NAT, so every network has one
	if s.d.PF != nil {
		if err := s.d.PF.Flush(ctx, render.NetAnchor(o.Meta.Name)); err != nil && !api.IsCode(err, api.CodeNotFound) {
			// what stays is a pass of the gateway interface of a network that is gone, which another network may be given
			s.log.Warn("flushing the anchor of a deleted network failed", "network", o.Meta.Name, "err", err)
		}
	}
	if s.d.Net == nil {
		return
	}
	ifs, err := s.d.Net.List(ctx)
	if err != nil {
		return
	}
	gm, _, _ := ipam.GatewayNames(o.Spec.Bridge)
	for _, i := range ifs {
		if i.Name != o.Spec.Bridge || i.Description != render.BridgeDescription(o.Meta.UUID) {
			continue
		}
		// the gateway port is the host's own; any other port is a jail's, and the bridge stays while one is left
		busy := false
		for _, m := range i.Members {
			if m.Name != gm {
				busy = true
			}
		}
		if busy {
			continue
		}
		for _, g := range ifs {
			if g.Name == gm && g.Description == render.BridgeDescription(o.Meta.UUID) {
				if err := s.d.Net.EpairDestroy(ctx, gm); err != nil { // destroying one end removes both
					s.log.Warn("destroying the gateway interface of a deleted network failed", "interface", gm, "err", err)
				}
			}
		}
		if err := s.d.Net.BridgeDestroy(ctx, i.Name); err != nil {
			s.log.Warn("destroying the bridge of a deleted network failed", "bridge", i.Name, "err", err)
		}
	}
}

// Leases lists the addresses allocated on a network, derived from jail
// specs. A project identity sees the leases of its own projects only.
func (s *Service) Leases(ctx context.Context, network string) ([]api.Lease, error) {
	if !ActorFrom(ctx).HasScope() {
		return nil, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	if !s.netKind.exists(network) || !s.networkVisible(ctx, network) {
		return nil, notFound("network", network)
	}
	a := ActorFrom(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []api.Lease{}
	for _, key := range sortedKeys(s.jails) {
		j := s.jails[key].jail
		if !a.Global && !a.CanProject(j.Metadata.Project) {
			continue
		}
		if p := ProjectFrom(ctx); p != "" && j.Metadata.Project != p {
			continue
		}
		for _, at := range j.Spec.Network {
			if at.Network != network {
				continue
			}
			for _, v := range []string{at.IPv4, at.IPv6} {
				if _, ok := parseAddr(v); ok {
					out = append(out, api.Lease{Address: v, Jail: jailDisplay(j.Metadata.Project, j.Metadata.Name), Network: network})
				}
			}
		}
	}
	return out, nil
}

// assignAddresses resolves "auto" and checks fixed addresses of every
// attachment, writing the result back into spec. The caller holds the network
// locks. self is the key of the jail being created or changed, project its
// project: addresses come from the project's sub-range of the network, and
// the in-use set spans every project on the network.
func (s *Service) assignAddresses(project, self string, spec *api.JailSpec) error {
	for i := range spec.Network {
		a := &spec.Network[i]
		n, err := s.netKind.get(a.Network)
		if err != nil {
			return err
		}
		// An attachment that says nothing about addresses gets one of every family the network has.
		if a.IPv4 == "" && a.IPv6 == "" {
			if n.Spec.IPv4 != nil {
				a.IPv4 = "auto"
			}
			if n.Spec.IPv6 != nil && n.Spec.IPv6.Mode != api.IPv6None {
				a.IPv6 = "auto"
			}
		}
		for _, v6 := range []bool{false, true} {
			p := &a.IPv4
			if v6 {
				p = &a.IPv6
			}
			if *p == "" || *p == "none" {
				continue
			}
			var al *ipam.Allocator
			// Addresses assigned earlier in this same spec count as taken.
			inUse := s.addressesOn(a.Network, self, v6)
			for j := 0; j < i; j++ {
				if spec.Network[j].Network == a.Network {
					v := spec.Network[j].IPv4
					if v6 {
						v = spec.Network[j].IPv6
					}
					if ad, ok := parseAddr(v); ok {
						inUse = append(inUse, ad)
					}
				}
			}
			if v6 {
				if n.Spec.IPv6 == nil || n.Spec.IPv6.Mode == api.IPv6None {
					return invalid("network %q has no IPv6", a.Network)
				}
				al, err = ipam.NewV6In(n.Spec.IPv6, ipam.Scope{}, inUse)
			} else {
				if n.Spec.IPv4 == nil {
					return invalid("network %q has no IPv4", a.Network)
				}
				al, err = ipam.NewV4In(n.Spec.IPv4, ipam.Scope{}, inUse)
			}
			if err != nil {
				return err
			}
			if *p == "auto" {
				ad, err := al.Allocate(netip.Addr{}, self)
				if err != nil {
					return err
				}
				*p = ad.String()
				continue
			}
			ad, ok := parseAddr(*p)
			if !ok {
				return validate.Invalid("network.address", "invalid address %q", *p)
			}
			if !al.Prefix().Contains(ad) {
				return validate.Invalid("network.address", "address %s is outside %s", ad, al.Prefix())
			}
			if _, err := al.Allocate(ad, self); err != nil {
				return err
			}
			*p = ad.String()
		}
	}
	return nil
}

// kick asks the reconciler for a pass.
func (s *Service) kick() {
	if s.d.Reconciler != nil {
		s.d.Reconciler.Kick()
	}
}

// ---- reconcile sink ----

// NetworkStatus implements reconcile.Sink.
func (s *Service) NetworkStatus(name string, st api.NetworkStatus) {
	s.mu.Lock()
	s.netStat[name] = st
	s.mu.Unlock()
}

// JailStatus implements reconcile.Sink.
func (s *Service) JailStatus(name string, conds []api.Condition, drift []string) {
	s.mu.Lock()
	rt := s.status[name]
	rt.conditions, rt.drift = conds, drift
	s.status[name] = rt
	s.mu.Unlock()
}

var _ reconcile.Sink = (*Service)(nil)

func (s *Service) leaseOwnerCheck(addr netip.Addr, network string) string {
	for _, a := range s.addressesOn(network, "", addr.Is6()) {
		if a == addr {
			return "taken"
		}
	}
	return ""
}

var _ = fmt.Sprint
var _ = slices.Sort[[]string]
var _ = sort.Strings
var _ = strings.TrimSpace

// assignOwn allocates the addresses of a jail whose devices may come from
// profiles: the allocation runs on the effective spec and the addresses of
// the jail's own devices are written back, by device name. A device that only
// a profile defines never carries an address (a profile has no address keys).
func (s *Service) assignOwn(project, self string, own *api.JailSpec) error {
	api.NormalizeDevices(own)
	profs, err := s.resolveProfiles(project, own.Profiles)
	if err != nil {
		return err
	}
	eff, _ := api.ExpandedConfig(*own, profs)
	if err := s.assignAddresses(project, self, &eff); err != nil {
		return err
	}
	for i := range own.Network {
		for _, e := range eff.Network {
			if e.Name == own.Network[i].Name {
				own.Network[i].IPv4, own.Network[i].IPv6 = e.IPv4, e.IPv6
			}
		}
	}
	return nil
}
