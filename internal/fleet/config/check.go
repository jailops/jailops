package config

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/fleet/vault"
	"jailops.net/jailops/sdk/api"
)

// ImageName returns the image part of an image reference such as
// "freebsd-15.1@stable" or "freebsd-15.1#g3".
func ImageName(ref string) string {
	if i := strings.IndexAny(ref, "@#"); i >= 0 {
		return ref[:i]
	}
	return ref
}

// Check validates references and values across the repository. It collects
// every problem in one pass. Warnings (security-relevant but valid
// configuration) are stored in f.Warnings.
func (f *Fleet) Check() error {
	f.Warnings = nil
	var errs []error
	add := func(o *Object, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", o.Origin, fmt.Sprintf(format, args...)))
	}
	warn := func(o *Object, format string, args ...any) {
		f.Warnings = append(f.Warnings, fmt.Sprintf("RISK: %s: %s", o.Origin, fmt.Sprintf(format, args...)))
	}

	// Every SOPS file needs a creation_rule, or nobody knows who may decrypt it.
	if f.SopsSecrets != nil {
		if err := vault.CheckCovered(f.Path, vault.SecretsRel); err != nil {
			errs = append(errs, err)
		}
	}
	for _, name := range f.HostNames() {
		if f.Remotes[name] != nil {
			if err := vault.CheckCovered(f.Path, vault.RemoteRel(name)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, name := range f.HostNames() {
		h := f.Hosts[name]
		if t := h.Spec.TokenSecret; t != "" && !f.Secrets[t] {
			errs = append(errs, fmt.Errorf("%s: token_secret %q: no secrets/%s.age", h.Origin, t, t))
		}
		if len(f.ObjectsFor(name)) == 0 {
			f.Warnings = append(f.Warnings, fmt.Sprintf("host %s declares no objects", name))
		}
	}
	if h := f.Meta.Hub; h != nil && h.TokenSecret != "" && !f.Secrets[h.TokenSecret] {
		errs = append(errs, fmt.Errorf("%s: hub token_secret %q: no secrets/%s.age", FleetFile, h.TokenSecret, h.TokenSecret))
	}
	for _, g := range f.GroupNames() {
		grp := f.Groups[g]
		if len(grp.Spec.Members) == 0 && len(grp.Spec.Selector) == 0 {
			errs = append(errs, fmt.Errorf("%s: group has neither members nor selector", grp.Origin))
			continue
		}
		hs, _ := f.ExpandHosts([]string{"@" + g})
		if len(hs) == 0 {
			f.Warnings = append(f.Warnings, fmt.Sprintf("group @%s matches no host", g))
		}
	}
	// A network belongs to one project: two projects granting the same network on one host is an error.
	holder := map[string]string{} // host/network -> project
	for _, pn := range f.ProjectNames() {
		pd := f.Projects[pn]
		for _, g := range pd.Spec.Networks {
			for _, h := range pd.Hosts {
				k := h + "/" + g.Network
				if other, ok := holder[k]; ok && other != pn {
					errs = append(errs, fmt.Errorf("%s: network %q is already granted to project %s on host %s: a network belongs to one project", pd.Origin, g.Network, other, h))
					continue
				}
				holder[k] = pn
			}
		}
	}
	for _, rn := range f.RolloutNames() {
		r := f.Rollouts[rn]
		if len(r.Spec.Hosts) > 0 {
			if _, err := f.ExpandHosts(r.Spec.Hosts); err != nil {
				errs = append(errs, fmt.Errorf("%s: hosts: %w", r.Origin, err))
			}
		}
	}

	// Per-host name tables for reference checks.
	tables := map[string]map[Key]*Object{}
	for _, h := range f.HostNames() {
		t := map[Key]*Object{}
		for _, o := range f.ObjectsFor(h) {
			t[o.Key()] = o
		}
		tables[h] = t
	}
	for _, rn := range f.RolloutNames() {
		r := f.Rollouts[rn]
		if p := r.Spec.Project; p == "" {
			errs = append(errs, fmt.Errorf("%s: a rollout names the project whose jails it upgrades (spec.project)", r.Origin))
		} else if f.Projects[p] == nil {
			errs = append(errs, fmt.Errorf("%s: project %q is not defined", r.Origin, p))
		}
	}

	for _, o := range f.Objects {
		// The daemon's name rules are narrower than the repository's file-name rule: a name it refuses fails at apply time.
		switch o.Kind {
		case api.KindJail, api.KindNetwork, api.KindFirewallPolicy:
			if !daemonNameRE.MatchString(o.Name) {
				add(o, "%s name %q must match %s (lowercase letters, digits and '-', starting with a letter)", o.Kind, o.Name, daemonNameRE)
			}
		case api.KindAddressSet:
			if !setNameRE.MatchString(o.Name) {
				add(o, "address set name %q must match %s (lowercase letters, digits and '_', starting with a letter: it becomes a PF table)", o.Name, setNameRE)
			}
		}
		for _, s := range o.SecretRefs {
			if IsSopsRef(s) {
				if !vault.PathExists(f.SopsSecrets, s) {
					add(o, "env refers to secret %q: no such path in %s", s, vault.SecretsRel)
				}
			} else if !f.Secrets[s] {
				add(o, "env refers to secret %q: no secrets/%s.age", s, s)
			}
		}
		// Reference checks are per host: a shared object must resolve on each
		// host it applies to.
		reported := map[string]bool{}
		missing := func(h string, kind api.Kind, project, name string, o *Object, what string) {
			if _, ok := tables[h][Key{kind, project, name}]; ok {
				return
			}
			key := fmt.Sprintf("%s/%s/%s", what, kind, name)
			if reported[key] {
				return
			}
			reported[key] = true
			add(o, "%s refers to unknown %s %q (not declared for host %s)", what, kind, name, h)
		}
		switch s := o.Spec.(type) {
		case api.JailSpec:
			f.checkJail(o, s, add, warn)
			pdef := f.Projects[o.Project]
			for _, h := range o.Hosts {
				if s.Image != "" {
					missing(h, api.KindImage, "", ImageName(s.Image), o, "image")
				}
				if len(s.Network) == 0 {
					add(o, "a jail needs a network: name one in spec.network or use a profile that has one")
				}
				for _, n := range s.Network {
					name := n.Network
					if name == "" {
						add(o, "a network device names no network: nothing is attached by default")
						continue
					}
					missing(h, api.KindNetwork, "", name, o, "network")
					if pdef != nil && !grantsNetwork(pdef.Spec, name) {
						add(o, "network %q is not granted to project %s", name, o.Project)
					}
				}
				if s.Storage == "" {
					add(o, "a jail needs a storage: name one in spec.storage or use a profile that has one")
				} else if s.Storage != "default" { // the host's built-in storage needs no declaration
					missing(h, api.KindStorage, "", s.Storage, o, "storage")
				}
				if pdef != nil {
					if s.Storage != "" && !grantsStorage(pdef.Spec, s.Storage) {
						add(o, "storage %q is not granted to project %s", s.Storage, o.Project)
					}
					if len(pdef.Spec.Images) > 0 && !slices.Contains(pdef.Spec.Images, ImageName(s.Image)) {
						add(o, "image %q is not in project %s's image allow-list", ImageName(s.Image), o.Project)
					}
				}
				if s.Firewall.Policy != "" {
					missing(h, api.KindFirewallPolicy, o.Project, s.Firewall.Policy, o, "firewall policy")
				}
				for _, d := range s.Boot.Depends {
					missing(h, api.KindJail, o.Project, d, o, "boot.depends")
				}
			}
		case api.FirewallPolicySpec:
			checkPolicy(o, s, add, warn)
			for _, h := range o.Hosts {
				for _, r := range s.Rules {
					for _, ref := range append(slices.Clone(r.From), r.To...) {
						if n, ok := strings.CutPrefix(ref, "@"); ok {
							missing(h, api.KindAddressSet, o.Project, n, o, "rule")
						}
					}
				}
			}
		case api.AddressSetSpec:
			checkAddressSet(o, s, add, warn)
			for _, h := range o.Hosts {
				for _, inc := range s.Includes {
					missing(h, api.KindAddressSet, o.Project, inc, o, "includes")
				}
			}
			for _, h := range o.Hosts {
				sets := map[string]*Object{}
				for k, v := range tables[h] {
					if k.Kind == api.KindAddressSet && k.Project == o.Project {
						sets[k.Name] = v
					}
				}
				if cyc := setCycle(sets, o.Name, nil); cyc != "" {
					add(o, "address set include cycle: %s", cyc)
					break
				}
			}
		case api.NetworkSpec:
			checkNetwork(o, s, add, warn)
		case api.StorageSpec:
			if s.Dataset == "" || strings.HasPrefix(s.Dataset, "/") || !strings.Contains(s.Dataset, "/") {
				add(o, "spec.dataset is required and names a ZFS dataset such as tank/jailops")
			}
			if s.Encrypted {
				add(o, "spec.encrypted is reserved: native ZFS encryption is not implemented and the daemon refuses it")
			}
		case api.ProjectSpec:
			f.checkProject(o, s, tables, add, warn)
		case api.ImageSpec:
			if s.OS == "" || s.Release == "" || s.Arch == "" || s.Source.Type == "" {
				add(o, "spec.os, release, arch and source.type are required")
			}
		case api.BackupTargetSpec:
			switch s.Kind {
			case "node":
				if s.Address == "" {
					add(o, "a node target needs an address")
				} else if s.Fingerprint == "" {
					add(o, "a node target needs a pinned fingerprint")
				}
			case "directory":
				if s.Path == "" || !strings.HasPrefix(s.Path, "/") {
					add(o, "a directory target needs an absolute path")
				}
			default:
				add(o, "kind must be node or directory, got %q", s.Kind)
			}
		case api.BackupPolicySpec:
			if len(s.Targets) == 0 {
				add(o, "a backup policy needs at least one target")
			}
			if len(strings.Fields(s.Schedule)) != 5 {
				add(o, "schedule %q must be a five-field cron expression", s.Schedule)
			}
			if (s.Retention == api.RetentionPolicy{}) {
				add(o, "retention must keep something")
			}
			if len(s.Selector) == 0 {
				warn(o, "empty selector matches no jail; use the label \"all\" to match every jail")
			}
			for _, h := range o.Hosts {
				for _, t := range s.Targets {
					missing(h, api.KindBackupTarget, "", t, o, "target")
				}
			}
		}
	}
	// Two networks of one host may not overlap, in any project: the daemon refuses the second one at apply time.
	for _, h := range f.HostNames() {
		var nets []*Object
		for _, o := range f.ObjectsFor(h) {
			if o.Kind == api.KindNetwork {
				nets = append(nets, o)
			}
		}
		for i, a := range nets {
			for _, b := range nets[i+1:] {
				for _, pa := range networkPrefixes(a.Spec.(api.NetworkSpec)) {
					for _, pb := range networkPrefixes(b.Spec.(api.NetworkSpec)) {
						if pa.Overlaps(pb) {
							errs = append(errs, fmt.Errorf("%s: network %s (%s) overlaps network %s (%s) on host %s: subnets may not overlap", b.Origin, b.Name, pb, a.Name, pa, h))
						}
					}
				}
			}
		}
	}
	// Storage datasets of one host may not be equal or nested: the daemon refuses the second one.
	for _, h := range f.HostNames() {
		var st []*Object
		for _, o := range f.ObjectsFor(h) {
			if o.Kind == api.KindStorage {
				st = append(st, o)
			}
		}
		for i, a := range st {
			for _, b := range st[i+1:] {
				da, db := a.Spec.(api.StorageSpec).Dataset, b.Spec.(api.StorageSpec).Dataset
				if da != "" && db != "" && (da == db || strings.HasPrefix(da, db+"/") || strings.HasPrefix(db, da+"/")) {
					errs = append(errs, fmt.Errorf("%s: storage %s (%s) overlaps storage %s (%s) on host %s: datasets may not be equal or nested", b.Origin, b.Name, db, a.Name, da, h))
				}
			}
		}
	}
	slices.Sort(f.Warnings)
	f.Warnings = slices.Compact(f.Warnings)
	return errors.Join(errs...)
}

var (
	daemonNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	setNameRE    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
)

func grantsNetwork(p api.ProjectSpec, n string) bool {
	for _, g := range p.Networks {
		if g.Network == n {
			return true
		}
	}
	return false
}

func grantsStorage(p api.ProjectSpec, n string) bool {
	for _, g := range p.Storage {
		if g.Storage == n || g.Storage == "*" {
			return true
		}
	}
	return false
}

func (f *Fleet) checkProject(o *Object, s api.ProjectSpec, tables map[string]map[Key]*Object, add func(*Object, string, ...any), warn func(*Object, string, ...any)) {
	if len(s.Networks) == 0 {
		warn(o, "project %s grants no network: its jails cannot attach to one", o.Name)
	}
	if len(s.Storage) == 0 {
		warn(o, "project %s grants no storage: it cannot hold jails", o.Name)
	}
	for _, g := range s.Networks {
		switch g.Network {
		case "":
			add(o, "a network grant needs a network")
		case "*":
			add(o, "networks are granted one by one: a network belongs to one project, so \"*\" is not allowed")
		}
	}
	for _, g := range s.Storage {
		if g.Storage == "" {
			add(o, "a storage grant needs a storage")
		}
	}
	if s.NewNetwork {
		add(o, "new_network is a create-time convenience of the CLI and is never stored: declare the network in shared/networks/ and grant it under networks")
	}
	if s.Quotas.Jails < 0 || s.Quotas.CPU < 0 || s.Quotas.PIDs < 0 || s.Quotas.Addresses < 0 {
		add(o, "quotas must not be negative")
	}
	if s.Firewall == "" {
		warn(o, "project %s has no mandatory firewall policy", o.Name)
	} else {
		for _, h := range o.Hosts {
			if _, ok := tables[h][Key{api.KindFirewallPolicy, o.Name, s.Firewall}]; !ok {
				add(o, "mandatory firewall policy %q is not declared in project %s for host %s", s.Firewall, o.Name, h)
				break
			}
		}
	}
}

func (f *Fleet) checkJail(o *Object, s api.JailSpec, add func(*Object, string, ...any), warn func(*Object, string, ...any)) {
	if s.Image == "" {
		add(o, "spec.image is required")
	}
	switch s.Update.Strategy {
	case "", api.UpdateRebase, api.UpdateInPlace, api.UpdateManual:
	default:
		add(o, "update.strategy %q is not rebase, inplace or manual", s.Update.Strategy)
	}
	for _, v := range s.Volumes {
		if !strings.HasPrefix(v.Mount, "/") {
			add(o, "volume %q: mount %q must be an absolute path", v.Name, v.Mount)
		}
	}
	for k, v := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			add(o, "env name %q is invalid", k)
		}
		if strings.HasPrefix(v, SecretPrefix) {
			if err := checkSecretRef(strings.TrimPrefix(v, SecretPrefix)); err != nil {
				add(o, "env %s: secret reference: %v", k, err)
			}
		}
	}
	if s.Firewall.Policy == "" {
		warn(o, "jail %s has no firewall policy", o.Name)
	}
	if s.Encrypted {
		add(o, "spec.encrypted is reserved: native ZFS encryption of jails is not implemented and the daemon refuses it")
	}
	if len(s.Packages) > 0 {
		add(o, "spec.packages is reserved and the daemon refuses it: install packages with spec.provision.packages")
	}
	targets := map[string]bool{}
	for i, m := range s.HostMounts {
		if !strings.HasPrefix(m.Source, "/") || !strings.HasPrefix(m.Target, "/") || m.Target == "/" {
			add(o, "host_mounts[%d]: source and target must be absolute paths and the target must not be /", i)
		}
		if targets[m.Target] {
			add(o, "host_mounts[%d]: duplicate mount point %q", i, m.Target)
		}
		targets[m.Target] = true
		if !m.ReadOnly {
			warn(o, "host_mounts[%d] %s is read-write: the jail can change or delete the host's data (use read_only: true where you can)", i, m.Source)
		}
	}
	if len(s.HostMounts) > 0 {
		warn(o, "host_mounts need a global-admin identity and a source below an admin-host-mount directory of the host's priv.conf; the helper refuses the jail's start otherwise")
	}
	if s.Delegate {
		warn(o, "zfs_delegation needs a global admin; the daemon accepts it but the helper does not hand the dataset over yet")
	}
}

// checkSecretRef checks the shape of a secret reference: a simple name for
// secrets/<name>.age, or a dotted path of simple segments for the SOPS file.
func checkSecretRef(ref string) error {
	if !IsSopsRef(ref) {
		return model.CheckSimpleName(ref)
	}
	for _, seg := range strings.Split(ref, ".") {
		if seg == "" || !secretSegment.MatchString(seg) {
			return fmt.Errorf("path %q: every segment is letters, digits, - or _", ref)
		}
	}
	return nil
}

var secretSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func checkPolicy(o *Object, s api.FirewallPolicySpec, add func(*Object, string, ...any), warn func(*Object, string, ...any)) {
	for _, d := range []struct{ n, v string }{{"default_in", s.DefaultIn}, {"default_out", s.DefaultOut}} {
		if d.v != "" && d.v != "allow" && d.v != "deny" {
			add(o, "%s must be allow or deny, got %q", d.n, d.v)
		}
	}
	if s.DefaultIn == "allow" {
		warn(o, "default_in is allow: inbound traffic is open unless a rule denies it")
	}
	for i, r := range s.Rules {
		w := fmt.Sprintf("rules[%d]", i)
		if r.Action != "allow" && r.Action != "deny" {
			add(o, "%s: action must be allow or deny", w)
		}
		if r.Direction != "in" && r.Direction != "out" {
			add(o, "%s: direction must be in or out", w)
		}
		switch r.Proto {
		case "", "tcp", "udp", "icmp", "icmp6":
		default:
			add(o, "%s: proto %q is not tcp, udp, icmp or icmp6", w, r.Proto)
		}
		for _, p := range r.Ports {
			if !validPorts(p) {
				add(o, "%s: invalid port %q", w, p)
			}
		}
		if r.Redirect != nil {
			if r.Direction != "in" {
				add(o, "%s: redirect is allowed on inbound rules only", w)
			}
			if r.Redirect.HostPort < 1 || r.Redirect.HostPort > 65535 || r.Redirect.JailPort < 1 || r.Redirect.JailPort > 65535 {
				add(o, "%s: redirect ports must be 1-65535", w)
			}
		}
		for _, a := range append(slices.Clone(r.From), r.To...) {
			if a == "" {
				add(o, "%s: empty address", w)
			}
		}
		if r.Action == "allow" && r.Direction == "in" && len(r.From) == 0 {
			warn(o, "%s allows inbound traffic from anywhere", w)
		}
	}
	if s.Raw != "" {
		warn(o, "raw PF fragment needs the admin role and bypasses typed validation")
	}
}

func validPorts(p string) bool {
	lo, hi, isRange := strings.Cut(p, "-")
	n, err := strconv.Atoi(lo)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	if !isRange {
		return true
	}
	m, err := strconv.Atoi(hi)
	return err == nil && m >= n && m <= 65535
}

func checkAddressSet(o *Object, s api.AddressSetSpec, add func(*Object, string, ...any), warn func(*Object, string, ...any)) {
	if len(s.Addresses) == 0 && len(s.Includes) == 0 {
		warn(o, "address set %s is empty: rules that reference it match nothing", o.Name)
	}
	for _, a := range s.Addresses {
		if _, err := netip.ParsePrefix(a); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(a); err != nil {
			add(o, "address %q is neither an address nor a prefix", a)
		}
	}
}

// setCycle returns the include path of a cycle reachable from name, or "".
func setCycle(sets map[string]*Object, name string, stack []string) string {
	if i := slices.Index(stack, name); i >= 0 {
		return strings.Join(append(slices.Clone(stack[i:]), name), " -> ")
	}
	o := sets[name]
	if o == nil {
		return ""
	}
	s, _ := o.Spec.(api.AddressSetSpec)
	stack = append(slices.Clone(stack), name)
	for _, inc := range s.Includes {
		if c := setCycle(sets, inc, stack); c != "" {
			return c
		}
	}
	return ""
}

func checkNetwork(o *Object, s api.NetworkSpec, add func(*Object, string, ...any), warn func(*Object, string, ...any)) {
	if s.IPv4 == nil && (s.IPv6 == nil || s.IPv6.Mode == api.IPv6None) {
		add(o, "a network needs ipv4 or ipv6")
	}
	if s.Private && s.Uplink == "" {
		add(o, "private blocks all traffic between the jails of the network and is for networks routed through the host: set an uplink")
	}
	if s.Uplink != "" && s.Uplink == s.Bridge {
		add(o, "uplink must not be the bridge itself")
	}
	if v := s.IPv4; v != nil {
		sub, err := netip.ParsePrefix(v.Subnet)
		if err != nil || !sub.Addr().Is4() {
			add(o, "ipv4.subnet %q is not an IPv4 prefix", v.Subnet)
		} else {
			if gw, err := netip.ParseAddr(v.Gateway); err != nil || !sub.Contains(gw) {
				add(o, "ipv4.gateway %q must be an address inside %s", v.Gateway, v.Subnet)
			}
			checkRange(o, "ipv4.range", v.Range, sub, add)
		}
		if v.NAT && s.Uplink == "" {
			add(o, "ipv4.nat needs an uplink")
		}
	}
	if v := s.IPv6; v != nil {
		switch v.Mode {
		case api.IPv6Routed, api.IPv6None, api.IPv6Proxy:
		default:
			add(o, "ipv6.mode must be routed, proxy or none")
		}
		if v.Mode == api.IPv6None {
			return
		}
		sub, err := netip.ParsePrefix(v.Subnet)
		if err != nil || !sub.Addr().Is6() {
			add(o, "ipv6.subnet %q is not an IPv6 prefix", v.Subnet)
			return
		}
		if sub != sub.Masked() {
			add(o, "ipv6.subnet %q has host bits set (want %s)", v.Subnet, sub.Masked())
		}
		lo := 32 // routed: a /32 to a /126
		if v.Mode == api.IPv6Proxy {
			lo = 48 // proxy: inside a prefix the host operator approved with ndp-proxy (/48 to /120)
		}
		if sub.Bits() < lo || sub.Bits() > 126 {
			add(o, "ipv6.subnet %s: in %s mode the prefix length must be between /%d and /126", sub, v.Mode, lo)
		}
		checkRange(o, "ipv6.range", v.Range, sub, add)
		gw, gerr := netip.ParseAddr(v.Gateway)
		switch {
		case gerr != nil || !gw.Is6():
			add(o, "ipv6.gateway %q is not an IPv6 address", v.Gateway)
		case !gw.IsLinkLocalUnicast() && !sub.Contains(gw):
			add(o, "ipv6.gateway %s is outside %s (only a link-local gateway may be)", gw, sub)
		}
		if v.Mode == api.IPv6Proxy {
			if gerr == nil && !gw.IsLinkLocalUnicast() {
				add(o, "in proxy mode the gateway must be the bridge's link-local address (for example fe80::1): the bridge must not hold an on-link copy of the upstream prefix")
			}
			if s.Uplink == "" {
				add(o, "ipv6 proxy mode needs an uplink to answer neighbor solicitations on")
			}
		}
	}
}

// networkPrefixes lists the subnets a network claims.
func networkPrefixes(s api.NetworkSpec) []netip.Prefix {
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

func checkRange(o *Object, what, r string, sub netip.Prefix, add func(*Object, string, ...any)) {
	a, b, ok := strings.Cut(r, "-")
	lo, e1 := netip.ParseAddr(strings.TrimSpace(a))
	hi, e2 := netip.ParseAddr(strings.TrimSpace(b))
	if !ok || e1 != nil || e2 != nil || hi.Less(lo) {
		add(o, "%s %q must be an inclusive span \"a-b\"", what, r)
		return
	}
	if !sub.Contains(lo) || !sub.Contains(hi) {
		add(o, "%s %q is outside %s", what, r, sub)
	}
}
