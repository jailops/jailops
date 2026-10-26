package service

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

func polToAPI(o object[api.FirewallPolicySpec]) api.FirewallPolicy {
	return api.FirewallPolicy{Metadata: o.Meta, Spec: o.Spec}
}
func setToAPI(o object[api.AddressSetSpec]) api.AddressSet {
	return api.AddressSet{Metadata: o.Meta, Spec: o.Spec}
}

// allSets returns the address sets of one project.
func (s *Service) allSets(project string) (map[string]api.AddressSetSpec, error) {
	objs, err := s.setKind.p(project).list()
	if err != nil {
		return nil, err
	}
	m := map[string]api.AddressSetSpec{}
	for _, o := range objs {
		m[o.Meta.Name] = o.Spec
	}
	return m, nil
}

// bodyProject checks that the project a request body names, if any, is the
// project of the request.
func bodyProject(p, body string) error {
	if body != "" && body != p {
		return invalid("metadata.project %q does not match the project of the request (%q)", body, p).
			WithHint("select the project with ?project=")
	}
	return nil
}

// checkPolicy validates a policy, renders it against placeholder values and,
// when a PF client exists, has pfctl -n check the result. A raw fragment needs
// the admin role.
func (s *Service) checkPolicy(ctx context.Context, name string, spec api.FirewallPolicySpec, sets map[string]api.AddressSetSpec) error {
	if err := validate.Name("name", name); err != nil {
		return err
	}
	rep, err := validate.FirewallPolicySpec(spec)
	if err != nil {
		return err
	}
	if rep.RequiresAdmin && !ActorFrom(ctx).Perm.Satisfies(api.PermAdmin) {
		return api.Errorf(api.CodeForbidden, "a raw PF fragment needs the admin role")
	}
	in := render.PFInput{Jail: "check_policy", Interfaces: []string{"jo000000000a"}, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		Policy: spec, Sets: sets, Uplink: "jo0", AllowRaw: true, TagMembers: map[string][]netip.Addr{}}
	for _, r := range spec.Rules {
		for _, e := range append(append([]string{}, r.From...), r.To...) {
			if k, v, err := validate.ParseEndpoint(e); err == nil && k == validate.EndpointTag {
				in.TagMembers[v] = []netip.Addr{netip.MustParseAddr("192.0.2.2")}
			}
		}
	}
	rs, err := render.PFAnchor(in)
	if err != nil {
		return err
	}
	if s.d.PF != nil {
		if _, err := s.d.PF.Validate(ctx, render.JailAnchor("check_policy"), rs); err != nil {
			return invalid("pfctl rejected the policy: %v", err)
		}
	}
	return nil
}

// ListPolicies lists the firewall policies of the request's project.
func (s *Service) ListPolicies(ctx context.Context, labels map[string]string) ([]api.FirewallPolicy, error) {
	p, err := s.access(ctx)
	if err != nil {
		return nil, err
	}
	objs, err := s.polKind.p(p.Meta.Name).list()
	if err != nil {
		return nil, err
	}
	out := []api.FirewallPolicy{}
	for _, o := range objs {
		if MatchLabels(o.Meta.Labels, labels) {
			out = append(out, polToAPI(o))
		}
	}
	return out, nil
}

// GetPolicy returns a policy.
func (s *Service) GetPolicy(ctx context.Context, name string) (api.FirewallPolicy, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	o, err := s.polKind.p(p.Meta.Name).get(name)
	return polToAPI(o), err
}

// CreatePolicy stores a policy in the request's project.
func (s *Service) CreatePolicy(ctx context.Context, in api.FirewallPolicy) (api.FirewallPolicy, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	if err := bodyProject(p.Meta.Name, in.Metadata.Project); err != nil {
		return api.FirewallPolicy{}, err
	}
	ks := s.polKind.p(p.Meta.Name)
	if err := ks.notTaken(in.Metadata.Name); err != nil {
		return api.FirewallPolicy{}, err
	}
	sets, err := s.allSets(p.Meta.Name)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	if err := s.checkPolicy(ctx, in.Metadata.Name, in.Spec, sets); err != nil {
		return api.FirewallPolicy{}, err
	}
	o, err := ks.create(ctx, in.Metadata, in.Spec)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	return polToAPI(o), nil
}

// protectedPolicy reports whether the policy is the project's mandatory
// firewall policy, which only a global administrator may change.
func (s *Service) protectedPolicy(ctx context.Context, p project, name string) error {
	if p.Spec.Firewall == name && !ActorFrom(ctx).Global {
		return api.Errorf(api.CodeForbidden, "firewall policy %q is the mandatory policy of project %q; a global administrator manages it", name, p.Meta.Name)
	}
	return nil
}

// UpdatePolicy replaces a policy and has running jails reload their anchors.
func (s *Service) UpdatePolicy(ctx context.Context, name string, in api.FirewallPolicy, expect int64) (api.FirewallPolicy, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	if in.Metadata.Name != "" && in.Metadata.Name != name {
		return api.FirewallPolicy{}, invalid("renaming is not supported")
	}
	if err := bodyProject(p.Meta.Name, in.Metadata.Project); err != nil {
		return api.FirewallPolicy{}, err
	}
	ks := s.polKind.p(p.Meta.Name)
	if !ks.exists(name) {
		return api.FirewallPolicy{}, notFound("firewall-policy", name)
	}
	if err := s.protectedPolicy(ctx, p, name); err != nil {
		return api.FirewallPolicy{}, err
	}
	sets, err := s.allSets(p.Meta.Name)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	if err := s.checkPolicy(ctx, name, in.Spec, sets); err != nil {
		return api.FirewallPolicy{}, err
	}
	o, err := ks.update(ctx, name, expect, in.Metadata.Labels, &in.Metadata.ManagedBy, in.Spec)
	if err != nil {
		return api.FirewallPolicy{}, err
	}
	s.kick()
	return polToAPI(o), nil
}

// PatchPolicy applies a merge patch.
func (s *Service) PatchPolicy(ctx context.Context, name string, patch map[string]any, expect int64) (api.FirewallPolicy, error) {
	cur, err := s.GetPolicy(ctx, name)
	if err != nil {
		return cur, err
	}
	if expect != AnyGeneration && expect != cur.Metadata.Generation {
		return cur, api.Errorf(api.CodeConflict, "firewall-policy %q changed: current generation is %d", name, cur.Metadata.Generation)
	}
	var out api.FirewallPolicy
	if err := MergePatch(cur, patch, &out); err != nil {
		return out, err
	}
	return s.UpdatePolicy(ctx, name, out, cur.Metadata.Generation)
}

// DeletePolicy removes a policy no jail uses.
func (s *Service) DeletePolicy(ctx context.Context, name string, expect int64) error {
	p, err := s.access(ctx)
	if err != nil {
		return err
	}
	ks := s.polKind.p(p.Meta.Name)
	if !ks.exists(name) {
		return notFound("firewall-policy", name)
	}
	if err := s.protectedPolicy(ctx, p, name); err != nil {
		return err
	}
	if p.Spec.Firewall == name {
		return api.Errorf(api.CodeBusy, "firewall policy %q is the mandatory policy of project %q", name, p.Meta.Name).
			WithHint("unset spec.firewall on the project first")
	}
	for _, j := range s.projectJails(p.Meta.Name) {
		if j.Spec.Firewall.Policy == name {
			return api.Errorf(api.CodeBusy, "firewall policy %q is used by jail %q", name, j.Metadata.Name)
		}
	}
	return ks.delete(ctx, name, expect)
}

// ListAddressSets lists the address sets of the request's project.
func (s *Service) ListAddressSets(ctx context.Context, labels map[string]string) ([]api.AddressSet, error) {
	p, err := s.access(ctx)
	if err != nil {
		return nil, err
	}
	objs, err := s.setKind.p(p.Meta.Name).list()
	if err != nil {
		return nil, err
	}
	out := []api.AddressSet{}
	for _, o := range objs {
		if MatchLabels(o.Meta.Labels, labels) {
			out = append(out, setToAPI(o))
		}
	}
	return out, nil
}

// GetAddressSet returns an address set.
func (s *Service) GetAddressSet(ctx context.Context, name string) (api.AddressSet, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.AddressSet{}, err
	}
	o, err := s.setKind.p(p.Meta.Name).get(name)
	return setToAPI(o), err
}

func (s *Service) checkSet(project, name string, spec api.AddressSetSpec) error {
	if err := validate.SetName("name", name); err != nil {
		return err
	}
	if err := validate.AddressSetSpec(spec); err != nil {
		return err
	}
	sets, err := s.allSets(project)
	if err != nil {
		return err
	}
	sets[name] = spec
	if _, err := validate.FlattenSet(name, sets); err != nil {
		return err
	}
	return nil
}

// CreateAddressSet stores an address set in the request's project.
func (s *Service) CreateAddressSet(ctx context.Context, in api.AddressSet) (api.AddressSet, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.AddressSet{}, err
	}
	if err := bodyProject(p.Meta.Name, in.Metadata.Project); err != nil {
		return api.AddressSet{}, err
	}
	ks := s.setKind.p(p.Meta.Name)
	if err := ks.notTaken(in.Metadata.Name); err != nil {
		return api.AddressSet{}, err
	}
	if err := s.checkSet(p.Meta.Name, in.Metadata.Name, in.Spec); err != nil {
		return api.AddressSet{}, err
	}
	o, err := ks.create(ctx, in.Metadata, in.Spec)
	if err != nil {
		return api.AddressSet{}, err
	}
	return setToAPI(o), nil
}

// UpdateAddressSet replaces an address set; anchors that use it are reloaded.
func (s *Service) UpdateAddressSet(ctx context.Context, name string, in api.AddressSet, expect int64) (api.AddressSet, error) {
	p, err := s.access(ctx)
	if err != nil {
		return api.AddressSet{}, err
	}
	if in.Metadata.Name != "" && in.Metadata.Name != name {
		return api.AddressSet{}, invalid("renaming is not supported")
	}
	if err := bodyProject(p.Meta.Name, in.Metadata.Project); err != nil {
		return api.AddressSet{}, err
	}
	ks := s.setKind.p(p.Meta.Name)
	if !ks.exists(name) {
		return api.AddressSet{}, notFound("address-set", name)
	}
	if err := s.checkSet(p.Meta.Name, name, in.Spec); err != nil {
		return api.AddressSet{}, err
	}
	o, err := ks.update(ctx, name, expect, in.Metadata.Labels, &in.Metadata.ManagedBy, in.Spec)
	if err != nil {
		return api.AddressSet{}, err
	}
	s.kick()
	return setToAPI(o), nil
}

// PatchAddressSet applies a merge patch.
func (s *Service) PatchAddressSet(ctx context.Context, name string, patch map[string]any, expect int64) (api.AddressSet, error) {
	cur, err := s.GetAddressSet(ctx, name)
	if err != nil {
		return cur, err
	}
	if expect != AnyGeneration && expect != cur.Metadata.Generation {
		return cur, api.Errorf(api.CodeConflict, "address-set %q changed: current generation is %d", name, cur.Metadata.Generation)
	}
	var out api.AddressSet
	if err := MergePatch(cur, patch, &out); err != nil {
		return out, err
	}
	return s.UpdateAddressSet(ctx, name, out, cur.Metadata.Generation)
}

// DeleteAddressSet removes an address set nothing refers to.
func (s *Service) DeleteAddressSet(ctx context.Context, name string, expect int64) error {
	p, err := s.access(ctx)
	if err != nil {
		return err
	}
	ks := s.setKind.p(p.Meta.Name)
	if !ks.exists(name) {
		return notFound("address-set", name)
	}
	pols, _ := s.polKind.p(p.Meta.Name).list()
	for _, pol := range pols {
		for _, r := range pol.Spec.Rules {
			for _, e := range append(append([]string{}, r.From...), r.To...) {
				if e == "@"+name {
					return api.Errorf(api.CodeBusy, "address set %q is used by firewall policy %q", name, pol.Meta.Name)
				}
			}
		}
	}
	sets, _ := ks.list()
	for _, o := range sets {
		for _, inc := range o.Spec.Includes {
			if inc == name {
				return api.Errorf(api.CodeBusy, "address set %q is included by %q", name, o.Meta.Name)
			}
		}
	}
	return ks.delete(ctx, name, expect)
}

// jailAddresses returns the addresses a jail's firewall rules apply to.
func jailAddrs(spec api.JailSpec) []netip.Addr {
	var out []netip.Addr
	for _, a := range spec.Network {
		for _, v := range []string{a.IPv4, a.IPv6} {
			if ad, ok := parseAddr(v); ok {
				out = append(out, ad)
			}
		}
	}
	return out
}

// tagMembers returns the addresses of the jails of one project that carry the
// tags the policy refers to: a tag never reaches into another project.
func (s *Service) tagMembers(project string, policy api.FirewallPolicySpec) map[string][]netip.Addr {
	want := map[string]bool{}
	for _, r := range policy.Rules {
		for _, e := range append(append([]string{}, r.From...), r.To...) {
			if strings.HasPrefix(e, "tag:") {
				want[strings.TrimPrefix(e, "tag:")] = true
			}
		}
	}
	out := map[string][]netip.Addr{}
	if len(want) == 0 {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range sortedKeys(s.jails) {
		if s.jails[n].jail.Metadata.Project != project {
			continue
		}
		sp := s.jails[n].jail.Spec
		for _, t := range sp.Firewall.Tags {
			if want[t] {
				out[t] = append(out[t], jailAddrs(sp)...)
			}
		}
	}
	return out
}

var _ = ipam.BridgeName
var _ = fmt.Sprint

// stricterDefault returns the stricter of two defaults. An unset inbound default is deny and an unset outbound
// default is allow (what the renderer does with an empty value).
func stricterDefault(a, b, dir string) string {
	deny := func(v string) bool { return v == "deny" || (dir == "in" && v == "") }
	if deny(a) || deny(b) {
		return "deny"
	}
	return "allow"
}

// foreignPrefixes are the subnets of the networks that belong to projects other than project. A network is never
// shared between projects, so everything in them is another project's.
func (s *Service) foreignPrefixes(project string) []netip.Prefix {
	projs, err := s.projKind.list()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, p := range projs {
		if p.Meta.Name == project {
			continue
		}
		for _, pn := range p.Spec.Networks {
			n, err := s.netKind.get(pn.Network)
			if err != nil {
				continue
			}
			if n.Spec.IPv4 != nil {
				if pf, err := netip.ParsePrefix(n.Spec.IPv4.Subnet); err == nil {
					out = append(out, pf)
				}
			}
			if n.Spec.IPv6 != nil && n.Spec.IPv6.Mode != api.IPv6None {
				if pf, err := netip.ParsePrefix(n.Spec.IPv6.Subnet); err == nil {
					out = append(out, pf)
				}
			}
		}
	}
	return out
}
