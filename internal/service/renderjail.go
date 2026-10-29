package service

import (
	"context"
	"fmt"
	"net/netip"
	"path"
	"path/filepath"
	"slices"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// jailBuild is everything rendered for one jail. It is a pure function of the
// jail spec, the networks, the policy and the address sets.
type jailBuild struct {
	desired  reconcile.JailDesired
	jailConf string
	fstab    string
	hasFstab bool
	// guest maps an absolute path under the jail's root to its content.
	guest map[string]string
	// ifaces are the jail-side interface names.
	ifaces []string
	// devfsRules is the devfs ruleset text of the jail's devices ("" when it has none).
	devfsRules string
}

// Where JailOps writes the guest configuration. rc.conf.local is read by the
// stock rc after rc.conf, so the administrator's rc.conf stays untouched.
// UNVERIFIED: on FreeBSD 15 inside a thin jail whose /etc is its own dataset.
const (
	guestRcConf  = "/etc/rc.conf.local"
	guestResolv  = "/etc/resolv.conf"
	guestAlpineI = "/etc/network/interfaces"
	guestAlpineH = "/etc/hostname"
)

// jailKey is the project-qualified key of a jail: its OS-level name, the leaf
// of its dataset path and the handle everything below the API uses.
func jailKey(j api.Jail) string {
	k, err := validate.QualifiedName(j.Metadata.Project, j.Metadata.Name)
	if err != nil {
		return j.Metadata.Name
	}
	return k
}

func (s *Service) buildJail(ctx context.Context, j api.Jail, sets map[string]api.AddressSetSpec) (*jailBuild, error) {
	j.Spec = s.effective(j)
	name, id := jailKey(j), j.Metadata.UUID
	project := j.Metadata.Project
	if project == "" {
		return nil, ErrProjectRequired()
	}
	osf := j.Spec.OS
	if osf == "" {
		osf = api.OSFreeBSD
	}
	b := &jailBuild{guest: map[string]string{}}
	d := &b.desired
	d.Name, d.ID, d.Boot, d.Project = name, id, j.Spec.Boot, project
	d.Boot.Depends = nil
	for _, dep := range j.Spec.Boot.Depends {
		if k, err := validate.QualifiedName(project, dep); err == nil {
			d.Boot.Depends = append(d.Boot.Depends, k)
		}
	}
	var ifs []render.NetIface
	var gateways []string // the l3 end of the gateway pair of each network: what pf sees of traffic to and from the host
	var macs []string
	var linkLocal []netip.Addr // the kernel forms them from the MACs above
	v6 := 0                    // IPv6 attachments
	var dns []netip.Addr
	uplink := ""
	for i, a := range j.Spec.Network {
		n, err := s.netKind.get(a.Network)
		if err != nil {
			return nil, fmt.Errorf("network %q: %w", a.Network, err)
		}
		host, jailEnd, err := ipam.EpairNames(id, i)
		if err != nil {
			return nil, err
		}
		in := render.AttachInput{JailID: id, JailUUID: id, Index: i, Bridge: n.Spec.Bridge, MAC: a.MAC, MTU: attachMTU(a, n.Spec.MTU),
			Uplink: n.Spec.Uplink, Private: n.Spec.Private, HostConfigured: osf == api.OSLinux}
		nif := render.NetIface{Name: jailEnd, MTU: attachMTU(a, n.Spec.MTU)}
		if ad, ok := parseAddr(a.IPv4); ok && n.Spec.IPv4 != nil {
			in.IPv4 = netip.PrefixFrom(ad, ipam.GuestPrefixLen4())
			in.Gateway4, _ = netip.ParseAddr(n.Spec.IPv4.Gateway)
			nif.IPv4, nif.Gateway4 = in.IPv4, in.Gateway4
		}
		if ad, ok := parseAddr(a.IPv6); ok && n.Spec.IPv6 != nil {
			sub, _ := netip.ParsePrefix(n.Spec.IPv6.Subnet)
			in.IPv6 = netip.PrefixFrom(ad, ipam.GuestPrefixLen6(n.Spec.IPv6.Mode, sub.Bits()))
			in.Gateway6 = ipam.JailGateway6(n.Spec.IPv6)
			in.IPv6Mode = n.Spec.IPv6.Mode
			v6++
			nif.IPv6, nif.Gateway6 = in.IPv6, in.Gateway6
			if n.Spec.IPv6.Mode == api.IPv6Proxy {
				d.Proxy = append(d.Proxy, reconcile.ProxyAddr{Addr: ad.String(), Bridge: ipam.GatewayIface(n.Spec.Bridge), Uplink: n.Spec.Uplink})
			}
		}
		steps, err := render.AttachPlan(in)
		if err != nil {
			return nil, err
		}
		var hostSteps []render.Step
		for _, st := range steps {
			if st.Phase == render.PhaseHost {
				hostSteps = append(hostSteps, st)
			}
		}
		d.Attach = append(d.Attach, hostSteps)
		d.Bridges = append(d.Bridges, n.Spec.Bridge)
		gateways = append(gateways, ipam.GatewayIface(n.Spec.Bridge))
		d.Epairs = append(d.Epairs, host)
		b.ifaces = append(b.ifaces, jailEnd)
		if a.MAC != "" {
			macs = append(macs, a.MAC)
		} else if m, err := ipam.DeriveMACN(id, i, false); err == nil {
			macs = append(macs, m)
		}
		if len(macs) > 0 {
			if l, err := ipam.LinkLocalFromMAC(macs[len(macs)-1]); err == nil {
				linkLocal = append(linkLocal, l)
			}
		}
		ifs = append(ifs, nif)
		for _, ds := range n.Spec.DNS {
			if ad, err := netip.ParseAddr(ds); err == nil && !containsAddr(dns, ad) {
				dns = append(dns, ad)
			}
		}
		if uplink == "" {
			uplink = n.Spec.Uplink
		}
	}
	// Firewall anchor: the project's mandatory policy first, then the jail's own.
	var own *object[api.FirewallPolicySpec]
	if pn := j.Spec.Firewall.Policy; pn != "" {
		pol, err := s.polKind.p(project).get(pn)
		if err != nil {
			return nil, fmt.Errorf("firewall policy %q: %w", pn, err)
		}
		own = &pol
	}
	var mandatory *object[api.FirewallPolicySpec]
	if pr, err := s.projKind.get(project); err == nil && pr.Spec.Firewall != "" {
		pol, err := s.polKind.p(project).get(pr.Spec.Firewall)
		if err != nil {
			return nil, fmt.Errorf("the mandatory firewall policy %q of project %q: %w", pr.Spec.Firewall, project, err)
		}
		mandatory = &pol
	}
	// Proxy devices are inbound redirects in the jail's anchor.
	var proxyRules []api.FirewallRule
	for _, p := range j.Spec.Proxy {
		host, port, err := validate.ParseListen(p.Listen)
		if err != nil {
			return nil, fmt.Errorf("proxy %q: %w", p.Name, err)
		}
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		proxyRules = append(proxyRules, api.FirewallRule{Name: "proxy " + p.Name, Action: "allow", Direction: "in", Proto: proto,
			Redirect: &api.Redirect{Listen: host, HostPort: port, JailPort: p.JailPort}})
	}
	// Every jail has an anchor, with or without a policy: the isolation of projects and the anti-spoofing rules do not
	// depend on anyone having written a policy.
	{
		if addrs := jailAddrs(j.Spec); len(addrs) > 0 && len(d.Epairs) > 0 {
			in := render.PFInput{Jail: name, Interfaces: d.Epairs, Addresses: addrs, Tags: j.Spec.Firewall.Tags,
				TagMembers: map[string][]netip.Addr{}, Sets: sets, Uplink: uplink, AllowRaw: true,
				Gateways: uniqStrings(gateways), Antispoof: true, LinkLocal: linkLocal, NoAdvertise: v6 > 0,
				Foreign: s.foreignPrefixes(project)}
			// A jail without any policy is open (apart from the isolation of projects).
			in.Policy = api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "allow"}
			var pols []api.FirewallPolicySpec
			if own != nil {
				in.Policy = own.Spec
				pols = append(pols, own.Spec)
			}
			if mandatory != nil {
				in.Mandatory = &render.PFMandatory{Name: mandatory.Meta.Name, Policy: mandatory.Spec, AllowRaw: true}
				pols = append(pols, mandatory.Spec)
				if own == nil {
					// Without a policy of its own the jail takes the defaults of the project's.
					in.Policy = api.FirewallPolicySpec{DefaultIn: mandatory.Spec.DefaultIn, DefaultOut: mandatory.Spec.DefaultOut}
				} else {
					// The project's defaults are a floor: a jail's own policy can open ports, it cannot turn a
					// project's default deny into a default allow.
					in.Policy.DefaultIn = stricterDefault(in.Policy.DefaultIn, mandatory.Spec.DefaultIn, "in")
					in.Policy.DefaultOut = stricterDefault(in.Policy.DefaultOut, mandatory.Spec.DefaultOut, "out")
				}
			}
			for _, p := range pols {
				for t, m := range s.tagMembers(project, p) {
					in.TagMembers[t] = append(in.TagMembers[t], m...)
				}
			}
			in.Policy.Rules = append(append([]api.FirewallRule(nil), in.Policy.Rules...), proxyRules...)
			rs, err := render.PFAnchor(in)
			if err != nil {
				return nil, err
			}
			d.AnchorName, d.Ruleset = render.JailAnchor(name), rs
			d.ParentAnchors = map[string]string{render.ProjectAnchor(project): render.ProjectAnchorRules()}
		}
	}
	// jail.conf and fstab.
	host := j.Spec.Hostname
	if host == "" {
		host = j.Metadata.Name
	}
	in := render.JailConfInput{Name: name, Path: s.rootMount(name), Hostname: host, OS: osf, Interfaces: b.ifaces}
	if j.Spec.Delegate {
		// UNVERIFIED: a jail that may mount ZFS needs allow.mount.zfs and
		// enforce_statfs=1; handing the datasets over (jailed=on, zfs jail) is
		// the root helper's part and is not wired yet.
		in.Allow = []string{"mount", "mount.zfs"}
		one := 1
		in.EnforceStatfs = &one
	}
	devRS := 0
	if osf == api.OSLinux {
		n, rules, err := render.LinuxDevfsRuleset(render.DevfsDevices(j.Spec.Devfs))
		if err != nil {
			return nil, err
		}
		in.DevfsRuleset, b.devfsRules, devRS = n, rules, n
	} else if len(j.Spec.Devfs) > 0 {
		n, rules, err := render.DevfsRuleset(render.DevfsDevices(j.Spec.Devfs))
		if err != nil {
			return nil, err
		}
		in.DevfsRuleset, b.devfsRules = n, rules
	}
	// Volumes are ZFS datasets mounted straight at their path in the root (like the persistent ones), so only
	// host directories need a nullfs line.
	var vols []render.FstabVolume
	for _, m := range j.Spec.HostMounts {
		vols = append(vols, render.FstabVolume{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	if osf == api.OSLinux || len(vols) > 0 {
		fs, err := render.Fstab(render.FstabInput{OS: osf, Root: s.rootMount(name), Volumes: vols, DevfsRuleset: devRS})
		if err != nil {
			return nil, err
		}
		b.fstab, b.hasFstab = fs, true
		in.FstabPath = s.fstabPath(name)
	}
	jc, err := render.JailConf(in)
	if err != nil {
		return nil, err
	}
	b.jailConf = jc
	// Guest configuration.
	root := s.rootMount(name)
	ni := render.NetInput{Hostname: host, Ifaces: ifs, DNS: dns}
	switch osf {
	case api.OSLinux:
		for p, f := range map[string]func() (string, error){
			guestAlpineI: func() (string, error) { return render.AlpineInterfaces(ni) },
			guestAlpineH: func() (string, error) { return render.AlpineHostname(host) },
			guestResolv:  func() (string, error) { return render.AlpineResolvConf(dns, nil) },
		} {
			c, err := f()
			if err != nil {
				return nil, err
			}
			b.guest[path.Join(root, p)] = c
		}
	default:
		rc, err := render.RcConfFreeBSD(ni)
		if err != nil {
			return nil, err
		}
		b.guest[path.Join(root, guestRcConf)] = rc
		rv, err := render.ResolvConf(dns, nil)
		if err != nil {
			return nil, err
		}
		b.guest[path.Join(root, guestResolv)] = rv
	}
	return b, nil
}

func containsAddr(l []netip.Addr, a netip.Addr) bool {
	for _, x := range l {
		if x == a {
			return true
		}
	}
	return false
}

// Desired implements reconcile.Source.
func (s *Service) Desired(ctx context.Context) (reconcile.Desired, error) {
	var out reconcile.Desired
	out.Host = s.hostDesired()
	nets, err := s.netKind.list()
	if err != nil {
		return out, err
	}
	for _, n := range nets {
		nd := reconcile.NetworkDesired{Name: n.Meta.Name, ID: n.Meta.UUID, Bridge: n.Spec.Bridge, Spec: n.Spec}
		steps, err := render.NetworkPlan(render.NetworkInput{ID: n.Meta.UUID, Name: n.Meta.Name, Bridge: n.Spec.Bridge, Spec: n.Spec})
		if err != nil {
			nd.Problem = err.Error()
		} else {
			nd.Steps = steps
			net, err := render.PFNet(n.Meta.Name, n.Spec)
			if err != nil {
				nd.Problem = err.Error()
			}
			nd.Anchors = map[string]string{render.NetAnchor(n.Meta.Name): net}
		}
		nd.Allocated, nd.Free, _ = s.networkCounts(n.Spec, n.Meta.Name)
		out.Networks = append(out.Networks, nd)
	}
	s.mu.RLock()
	names := sortedKeys(s.jails)
	jails := make([]api.Jail, 0, len(names))
	for _, n := range names {
		jails = append(jails, s.jails[n].jail)
	}
	s.mu.RUnlock()
	setsOf := map[string]map[string]api.AddressSetSpec{}
	for _, j := range jails {
		p := j.Metadata.Project
		if _, ok := setsOf[p]; !ok {
			sets, err := s.allSets(p)
			if err != nil {
				return out, err
			}
			setsOf[p] = sets
		}
		b, err := s.buildJail(ctx, j, setsOf[p])
		if err != nil {
			out.Jails = append(out.Jails, reconcile.JailDesired{Name: jailKey(j), ID: j.Metadata.UUID, Project: j.Metadata.Project, Boot: j.Spec.Boot, Problem: err.Error()})
			continue
		}
		out.Jails = append(out.Jails, b.desired)
	}
	return out, nil
}

// ---- hooks (lifecycle.Hooks) ----

type hooks struct{ s *Service }

// PreStart renders the jail's configuration and makes sure its networks exist.
// The epair, bridge membership, jail-phase and firewall work happens in the
// exec hooks of jail.conf, which jail(8) runs through jailops-priv; the
// rendered PF anchor is left where that hook reads it. It is idempotent.
func (h *hooks) PreStart(ctx context.Context, name string) error {
	s := h.s
	j, err := s.jailSnapshot(name)
	if err != nil {
		return err
	}
	sets, err := s.allSets(j.Metadata.Project)
	if err != nil {
		return err
	}
	b, err := s.buildJail(ctx, j, sets)
	if err != nil {
		return err
	}
	obs, err := reconcile.Observe(ctx, s.d.Net, nil)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range j.Spec.Network {
		if seen[a.Network] {
			continue
		}
		seen[a.Network] = true
		if err := s.ensureNetwork(ctx, a.Network, obs); err != nil {
			return err
		}
	}
	f := s.d.Files
	if err := f.MkdirAll(ctx, s.confDir(name)); err != nil {
		return err
	}
	if b.devfsRules != "" {
		if err := f.WriteAtomic(ctx, s.devfsRulesPath(name), []byte(b.devfsRules)); err != nil {
			return err
		}
	}
	if err := f.WriteAtomic(ctx, s.confPath(name), []byte(b.jailConf)); err != nil {
		return err
	}
	// nullfs needs the mount point to exist; a thin root is rebased on updates, so it is made on every render
	for _, m := range j.Spec.HostMounts {
		if err := f.MkdirAll(ctx, s.rootMount(name)+m.Target); err != nil {
			return err
		}
	}
	if b.hasFstab {
		if err := f.WriteAtomic(ctx, s.fstabPath(name), []byte(b.fstab)); err != nil {
			return err
		}
	}
	for p, c := range b.guest {
		if err := f.MkdirAll(ctx, path.Dir(p)); err != nil {
			return err
		}
		if err := f.WriteAtomic(ctx, p, []byte(c)); err != nil {
			return err
		}
	}
	return s.writeAnchorFile(ctx, name, b.desired.Ruleset)
}

// writeAnchorFile leaves the rendered PF anchor for the start hook, or
// removes a stale one when the jail has no firewall.
func (s *Service) writeAnchorFile(ctx context.Context, name, ruleset string) error {
	if s.d.StateDir == "" {
		return nil
	}
	p := s.anchorFile(name)
	if ruleset == "" {
		if ok, _ := s.d.Files.Exists(ctx, p); ok {
			return s.d.Files.RemoveAll(ctx, p)
		}
		return nil
	}
	if err := s.d.Files.MkdirAll(ctx, path.Dir(p)); err != nil {
		return err
	}
	return s.d.Files.WriteAtomic(ctx, p, []byte(ruleset))
}

// ensureNetwork brings one network's bridge up before a jail attaches.
func (s *Service) ensureNetwork(ctx context.Context, name string, obs *reconcile.Observed) error {
	n, err := s.netKind.get(name)
	if err != nil {
		return err
	}
	steps, err := render.NetworkPlan(render.NetworkInput{ID: n.Meta.UUID, Name: name, Bridge: n.Spec.Bridge, Spec: n.Spec})
	if err != nil {
		return err
	}
	if i, ok := obs.Ifaces[n.Spec.Bridge]; ok && i.Description != render.BridgeDescription(n.Meta.UUID) {
		return api.Errorf(api.CodeConflict, "interface %s exists and is not managed by JailOps", n.Spec.Bridge)
	}
	unlock, err := s.locks.lockPatient(ctx, "prepare network "+name, networkLock(name))
	if err != nil {
		return err
	}
	defer unlock()
	nat := map[string]string{}
	rs, err := render.PFNet(name, n.Spec)
	if err != nil {
		return err
	}
	nat[render.NetAnchor(name)] = rs
	ex := s.netExec(nat)
	for _, st := range steps {
		if reconcile.Satisfied(st, obs) {
			continue
		}
		if err := ex.Exec(ctx, st, ""); err != nil {
			return fmt.Errorf("network %s: %s: %w", name, st.Describe(), err)
		}
	}
	return nil
}

func (s *Service) netExec(anchors map[string]string) *reconcile.NetExec {
	return &reconcile.NetExec{Net: s.d.Net, PF: s.d.PF, Run: s.d.Run, Jail: s.d.Jails, Sysctl: s.d.Sysctl, Anchors: anchors}
}

// PostStop has nothing to do: the poststop hook of jail.conf flushes the
// anchor and destroys the epairs, and the reconciler removes what is left.
func (h *hooks) PostStop(ctx context.Context, name string) error { return nil }

// Remove deletes the rendered files of a jail that is being destroyed.
func (h *hooks) Remove(ctx context.Context, name string) error {
	s := h.s
	for _, p := range []string{s.confPath(name), s.fstabPath(name), s.confDir(name)} {
		if ok, _ := s.d.Files.Exists(ctx, p); ok {
			if err := s.d.Files.RemoveAll(ctx, p); err != nil {
				return err
			}
		}
	}
	if s.d.StateDir != "" {
		if ok, _ := s.d.Files.Exists(ctx, s.anchorFile(name)); ok {
			return s.d.Files.RemoveAll(ctx, s.anchorFile(name))
		}
	}
	return nil
}

func attachMTU(a api.NetworkAttach, network int) int {
	if a.MTU > 0 {
		return a.MTU
	}
	return network
}

func uniqStrings(in []string) []string {
	var out []string
	for _, x := range in {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// placement is where PF rules attach: the bridge by default.

// devfsRulesPath is where the devfs ruleset of a jail is left for the root
// helper, which loads it before the jail starts. UNVERIFIED.
func (s *Service) devfsRulesPath(key string) string {
	return filepath.Join(s.confDir(key), "devfs.rules")
}
