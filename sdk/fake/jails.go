package fake

import (
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

func (s *Server) setupJails() {
	k := s.jails
	k.asyncCreate, k.asyncDelete = true, true
	k.keep = func(dst, old *api.Jail) { dst.Status = old.Status }
	k.expand = func(j *api.Jail) any { return s.jailExpanded(j) }
	k.validate = func(j, old *api.Jail) *api.Error {
		if j.Spec.Image == "" {
			return api.Errorf(api.CodeInvalid, "spec.image is required")
		}
		if e := s.checkJail(j, old); e != nil {
			return e
		}
		if s.images.items[imageName(j.Spec.Image)] == nil {
			return notFound("image", imageName(j.Spec.Image)).WithHint("create or import the image first")
		}
		if j.Spec.Delegate && (old == nil || !old.Spec.Delegate) {
			// Only a global admin may delegate ZFS; the check lives in guard.
			return nil
		}
		return nil
	}
	k.guard = func(rc *reqInfo, j *api.Jail, verb string) *api.Error {
		if verb != "delete" && j.Spec.Delegate && !rc.id.Global {
			return api.Errorf(api.CodeForbidden, "only a global admin may delegate ZFS to a jail")
		}
		if verb != "delete" && len(j.Spec.HostMounts) > 0 && !rc.id.Global {
			return api.Errorf(api.CodeForbidden, "mounting host directories into a jail is granted by a global administrator only")
		}
		return nil
	}
	k.assign = func(j, old *api.Jail) *api.Error {
		if e := s.assignJail(j, old); e != nil {
			return e
		}
		if old == nil {
			s.initJailStatus(j)
		}
		return nil
	}
	k.beforeDelete = func(j *api.Jail) *api.Error {
		if j.Status.Power == api.PowerRunning {
			return api.Errorf(api.CodeBusy, "jail %q is running", j.Metadata.Name).WithHint("stop it first")
		}
		return nil
	}
	k.onDelete = func(j *api.Jail) { delete(s.snapshots, j.Metadata.Project+"/"+j.Metadata.Name) }
}

// assignJail fills defaults and allocates "auto" addresses. s.mu is held.
func (s *Server) assignJail(j, old *api.Jail) *api.Error {
	if j.Spec.Layout == "" {
		j.Spec.Layout = api.LayoutThin
	}
	if j.Spec.Hostname == "" {
		j.Spec.Hostname = j.Metadata.Name
	}
	for i := range j.Spec.Network {
		a := &j.Spec.Network[i]
		// Saying nothing about addresses means one of every family the network has.
		if a.IPv4 == "" && a.IPv6 == "" {
			if n := s.networks.items[a.Network]; n != nil {
				if n.Spec.IPv4 != nil {
					a.IPv4 = "auto"
				}
				if n.Spec.IPv6 != nil && n.Spec.IPv6.Mode != api.IPv6None {
					a.IPv6 = "auto"
				}
			}
		}
		for _, v6 := range []bool{false, true} {
			p := &a.IPv4
			if v6 {
				p = &a.IPv6
			}
			switch *p {
			case "", "none":
			case "auto":
				addr, e := s.allocate(a.Network, v6, j.Metadata.Project, j.Metadata.Name)
				if e != nil {
					return e
				}
				*p = addr
			default:
				if e := s.checkFixed(a.Network, *p, j.Metadata.Project, j.Metadata.Name); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func (s *Server) initJailStatus(j *api.Jail) {
	j.Status = api.JailStatus{Power: api.PowerStopped, Generation: "g1"}
	if i := strings.IndexByte(j.Spec.Image, '#'); i >= 0 {
		j.Status.Generation = j.Spec.Image[i+1:]
	} else if g := s.gens[imageName(j.Spec.Image)]; len(g) > 0 {
		j.Status.Generation = "g" + strconv.Itoa(g[len(g)-1].Number)
	}
}

func (s *Server) jailState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	var st api.JailStatus
	if j != nil {
		st = clone(j).Status
	}
	s.mu.Unlock()
	if j == nil {
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	}
	writeSync(w, 200, st)
}

func (s *Server) jailAction(w http.ResponseWriter, r *http.Request) {
	act, e := decode[api.JailAction](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	rc := s.info(r)
	jk := s.jails.key(r, name)
	s.mu.Lock()
	rp := func() reply {
		if s.jails.items[jk] == nil {
			return reply{err: notFound("jail", name)}
		}
		switch act.Action {
		case "start", "stop", "restart":
		default:
			return reply{err: api.Errorf(api.CodeInvalid, "unknown action %q", act.Action)}
		}
		op := s.startOp(rc.project, "jails."+act.Action, act.Action+" jail "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[jk]
			if j == nil {
				return nil, notFound("jail", name)
			}
			if act.Action != "start" {
				j.Status.Power, j.Status.JID, j.Status.StartedAt, j.Status.Addresses = api.PowerStopped, 0, time.Time{}, nil
			}
			if act.Action != "stop" && j.Status.Power != api.PowerRunning {
				s.nextJID++
				j.Status.Power, j.Status.JID, j.Status.StartedAt = api.PowerRunning, s.nextJID, s.now()
				j.Status.Addresses = nil
				for _, a := range j.Spec.Network {
					for _, ad := range []string{a.IPv4, a.IPv6} {
						if ad != "" && ad != "none" && ad != "auto" {
							j.Status.Addresses = append(j.Status.Addresses, ad)
						}
					}
				}
			}
			s.emitIn(rc.project, "jail-"+map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}[act.Action], "/api/v1/jails/"+name)
			return clone(j), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// --- snapshots ---

func (s *Server) snapList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	jk := s.jails.key(r, name)
	s.mu.Lock()
	out := []api.JailSnapshot{}
	for _, sn := range s.snapshots[jk] {
		out = append(out, clone(sn))
	}
	exists := s.jails.items[jk] != nil
	s.mu.Unlock()
	if !exists {
		writeErr(w, notFound("jail", name))
		return
	}
	writeSync(w, 200, out)
}

func (s *Server) findSnap(jail, snap string) *api.JailSnapshot {
	for _, sn := range s.snapshots[jail] {
		if sn.Metadata.Name == snap {
			return sn
		}
	}
	return nil
}

func (s *Server) snapGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var out api.JailSnapshot
	sn := s.findSnap(s.jails.key(r, r.PathValue("name")), r.PathValue("snap"))
	if sn != nil {
		out = clone(sn)
	}
	s.mu.Unlock()
	if sn == nil {
		writeErr(w, notFound("snapshot", r.PathValue("snap")))
		return
	}
	writeSync(w, 200, out)
}

func (s *Server) snapCreate(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.JailSnapshot](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	jail := r.PathValue("name")
	rc := s.info(r)
	jk := s.jails.key(r, jail)
	s.mu.Lock()
	rp := func() reply {
		if s.jails.items[jk] == nil {
			return reply{err: notFound("jail", jail)}
		}
		name := req.Metadata.Name
		if name == "" {
			name = "snap-" + randHex(3)
		}
		if !validName(name) {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid snapshot name %q", name)}
		}
		if s.findSnap(jk, name) != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "snapshot %q already exists", name)}
		}
		op := s.startOp(rc.project, "jails.snapshot", "Snapshot "+jail, map[string][]string{"jails": {"/api/v1/jails/" + jail}}, func() (any, *api.Error) {
			if s.jails.items[jk] == nil {
				return nil, notFound("jail", jail)
			}
			sn := &api.JailSnapshot{
				Metadata: api.ObjectMeta{Name: name, UUID: newUUID(), Project: rc.project, Scope: api.ScopeHost, Host: s.uuid, Generation: 1, CreatedAt: s.now(), UpdatedAt: s.now()},
				Spec:     api.JailSnapshotSpec{Jail: jail, Comment: req.Spec.Comment},
			}
			s.snapshots[jk] = append(s.snapshots[jk], sn)
			s.emitIn(rc.project, "jail-snapshot-created", "/api/v1/jails/"+jail+"/snapshots/"+name)
			return clone(sn), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) snapDelete(w http.ResponseWriter, r *http.Request) {
	jail, snap := r.PathValue("name"), r.PathValue("snap")
	rc := s.info(r)
	jk := s.jails.key(r, jail)
	s.mu.Lock()
	rp := func() reply {
		if s.findSnap(jk, snap) == nil {
			return reply{err: notFound("snapshot", snap)}
		}
		op := s.startOp(rc.project, "jails.snapshot-delete", "Delete snapshot "+snap, nil, func() (any, *api.Error) {
			l := s.snapshots[jk]
			for i, sn := range l {
				if sn.Metadata.Name == snap {
					s.snapshots[jk] = append(l[:i:i], l[i+1:]...)
					s.emitIn(rc.project, "jail-snapshot-deleted", "/api/v1/jails/"+jail+"/snapshots/"+snap)
					return nil, nil
				}
			}
			return nil, notFound("snapshot", snap)
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// --- rollback and upgrade ---

func (s *Server) jailRollback(w http.ResponseWriter, r *http.Request) {
	rb, e := decode[api.JailRollback](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	rc := s.info(r)
	jk := s.jails.key(r, name)
	s.mu.Lock()
	rp := func() reply {
		j := s.jails.items[jk]
		if j == nil {
			return reply{err: notFound("jail", name)}
		}
		if rb.Scope == "" {
			rb.Scope = api.RollbackRoot
		}
		var target *api.JailSnapshot
		switch rb.Scope {
		case api.RollbackRoot:
			if genNumber(j.Status.Generation) <= 1 {
				return reply{err: api.Errorf(api.CodeInvalid, "jail %q has no previous generation", name)}
			}
		case api.RollbackData, api.RollbackAll:
			if !rb.ConfirmDataLoss {
				return reply{err: api.Errorf(api.CodeDataLoss, "scope %q discards data written since the snapshot", rb.Scope).
					WithHint("set confirm_data_loss after checking what was written")}
			}
			l := s.snapshots[jk]
			if rb.Snapshot == "" && len(l) > 0 {
				target = l[len(l)-1]
			} else if target = s.findSnap(jk, rb.Snapshot); target == nil {
				return reply{err: notFound("snapshot", rb.Snapshot)}
			}
		default:
			return reply{err: api.Errorf(api.CodeInvalid, "unknown rollback scope %q", rb.Scope)}
		}
		op := s.startOp(rc.project, "jails.rollback", "Rollback "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[jk]
			if j == nil {
				return nil, notFound("jail", name)
			}
			if rb.Scope != api.RollbackData {
				j.Status.Generation = "g" + strconv.Itoa(genNumber(j.Status.Generation)-1)
			}
			if target != nil { // ZFS discards snapshots newer than the target
				l := s.snapshots[jk]
				for i, sn := range l {
					if sn == target {
						s.snapshots[jk] = l[:i+1]
					}
				}
			}
			s.emitIn(rc.project, "jail-rolled-back", "/api/v1/jails/"+name)
			return clone(j), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) jailUpgrade(w http.ResponseWriter, r *http.Request) {
	up, e := decode[api.JailUpgrade](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	rc := s.info(r)
	jk := s.jails.key(r, name)
	s.mu.Lock()
	rp := func() reply {
		j := s.jails.items[jk]
		if j == nil {
			return reply{err: notFound("jail", name)}
		}
		gens := s.gens[imageName(j.Spec.Image)]
		target := genNumber(up.Generation)
		if up.Generation == "" && len(gens) > 0 {
			target = gens[len(gens)-1].Number
		}
		if target == 0 || target > len(gens) {
			return reply{err: api.Errorf(api.CodeInvalid, "target generation does not exist")}
		}
		if target == genNumber(j.Status.Generation) {
			return reply{err: api.Errorf(api.CodeInvalid, "jail %q is already on generation g%d", name, target)}
		}
		op := s.startOp(rc.project, "jails.upgrade", "Upgrade "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[jk]
			if j == nil {
				return nil, notFound("jail", name)
			}
			if !up.DryRun {
				j.Status.Generation = "g" + strconv.Itoa(target)
				if i := strings.IndexByte(j.Spec.Image, '#'); i >= 0 {
					j.Spec.Image = j.Spec.Image[:i+1] + j.Status.Generation
					j.Metadata.Generation++
				}
				s.emitIn(rc.project, "jail-upgraded", "/api/v1/jails/"+name)
			}
			return clone(j), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// --- networks and IPAM ---

func (s *Server) setupNetworks() {
	k := s.networks
	k.keep = func(dst, old *api.Network) { dst.Status = old.Status }
	k.validate = func(n, _ *api.Network) *api.Error {
		if n.Spec.IPv4 == nil && n.Spec.IPv6 == nil {
			return api.Errorf(api.CodeInvalid, "a network needs ipv4 or ipv6")
		}
		for _, v := range []*struct{ sub, rng string }{
			func() *struct{ sub, rng string } {
				if n.Spec.IPv4 != nil {
					return &struct{ sub, rng string }{n.Spec.IPv4.Subnet, n.Spec.IPv4.Range}
				}
				return nil
			}(),
			func() *struct{ sub, rng string } {
				if n.Spec.IPv6 != nil {
					return &struct{ sub, rng string }{n.Spec.IPv6.Subnet, n.Spec.IPv6.Range}
				}
				return nil
			}(),
		} {
			if v == nil {
				continue
			}
			if _, err := netip.ParsePrefix(v.sub); err != nil {
				return api.Errorf(api.CodeInvalid, "invalid subnet %q", v.sub)
			}
			if _, _, err := parseRange(v.rng); err != nil {
				return api.Errorf(api.CodeInvalid, "invalid range %q", v.rng)
			}
		}
		return nil
	}
	k.beforeDelete = func(n *api.Network) *api.Error {
		for _, p := range s.projects.items {
			for _, g := range p.Spec.Networks {
				if g.Network == n.Metadata.Name {
					return api.Errorf(api.CodeBusy, "network %q is granted to project %q", n.Metadata.Name, p.Metadata.Name).WithHint("remove the grant first")
				}
			}
		}
		for _, j := range s.jails.items {
			for _, a := range j.Spec.Network {
				if a.Network == n.Metadata.Name {
					return api.Errorf(api.CodeBusy, "network %q is used by jail %q", n.Metadata.Name, j.Metadata.Name)
				}
			}
		}
		return nil
	}
	k.decorate = func(n *api.Network) {
		n.Status.Allocated = len(s.leasesOf(n.Metadata.Name))
		if n.Spec.IPv4 != nil {
			if a, b, err := parseRange(n.Spec.IPv4.Range); err == nil && a.Is4() {
				total := int(addrNum(b)-addrNum(a)) + 1
				n.Status.Free = max(total-n.Status.Allocated, 0)
			}
		}
	}
}

func addrNum(a netip.Addr) uint64 {
	b := a.As16()
	var n uint64
	for _, x := range b[8:] {
		n = n<<8 | uint64(x)
	}
	return n
}

func parseRange(r string) (netip.Addr, netip.Addr, error) {
	a, b, ok := strings.Cut(r, "-")
	if !ok {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("range must be a-b")
	}
	x, err := netip.ParseAddr(strings.TrimSpace(a))
	if err != nil {
		return x, x, err
	}
	y, err := netip.ParseAddr(strings.TrimSpace(b))
	if err != nil {
		return x, y, err
	}
	if x.Is4() != y.Is4() || y.Less(x) {
		return x, y, fmt.Errorf("bad range")
	}
	return x, y, nil
}

func addrOf(s string) (netip.Addr, bool) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), true
	}
	a, err := netip.ParseAddr(s)
	return a, err == nil
}

type lease struct {
	api.Lease
	project, key string
}

func (s *Server) leasesOf(network string) []lease {
	var out []lease
	keys := make([]string, 0, len(s.jails.items))
	for n := range s.jails.items {
		keys = append(keys, n)
	}
	sort.Strings(keys)
	for _, key := range keys {
		j := s.jails.items[key]
		for _, a := range j.Spec.Network {
			if a.Network != network {
				continue
			}
			for _, ad := range []string{a.IPv4, a.IPv6} {
				if _, ok := addrOf(ad); ok {
					out = append(out, lease{api.Lease{Address: ad, Jail: j.Metadata.Name, Network: network}, j.Metadata.Project, key})
				}
			}
		}
	}
	return out
}

// usedAddrs maps every address in use on a network, across all projects, to
// its owner's key; the jail self is left out.
func (s *Server) usedAddrs(network, self string) map[netip.Addr]string {
	used := map[netip.Addr]string{}
	for _, l := range s.leasesOf(network) {
		if l.key != self {
			a, _ := addrOf(l.Address)
			used[a] = l.Jail
		}
	}
	return used
}

func (s *Server) allocate(network string, v6 bool, project, jail string) (string, *api.Error) {
	self := s.jails.keyIn(project, jail)
	n := s.networks.items[network]
	if n == nil {
		return "", notFound("network", network)
	}
	var rng, gw string
	var excl []string
	if v6 && n.Spec.IPv6 != nil {
		rng, gw, excl = n.Spec.IPv6.Range, n.Spec.IPv6.Gateway, n.Spec.IPv6.Exclude
	} else if !v6 && n.Spec.IPv4 != nil {
		rng, gw, excl = n.Spec.IPv4.Range, n.Spec.IPv4.Gateway, n.Spec.IPv4.Exclude
	} else {
		return "", api.Errorf(api.CodeInvalid, "network %q has no such address family", network)
	}
	lo, hi, err := parseRange(rng)
	if err != nil {
		return "", api.Errorf(api.CodeInvalid, "network %q has a bad range", network)
	}
	used := s.usedAddrs(network, self)
	if g, ok := addrOf(gw); ok {
		used[g] = "gateway"
	}
	for a := lo; a.IsValid() && !hi.Less(a); a = a.Next() {
		if _, taken := used[a]; taken || excluded(excl, a) {
			continue
		}
		return a.String(), nil
	}
	return "", api.Errorf(api.CodePoolExhausted, "network %q has no free address for project %q", network, project)
}

func excluded(ex []string, a netip.Addr) bool {
	for _, e := range ex {
		if lo, hi, err := parseRange(e); err == nil {
			if !a.Less(lo) && !hi.Less(a) {
				return true
			}
		} else if x, ok := addrOf(e); ok && x == a {
			return true
		}
	}
	return false
}

func (s *Server) checkFixed(network, addr, project, jail string) *api.Error {
	a, ok := addrOf(addr)
	if !ok {
		return api.Errorf(api.CodeInvalid, "invalid address %q", addr)
	}
	if n := s.networks.items[network]; n != nil {
		sub := ""
		if a.Is6() && n.Spec.IPv6 != nil {
			sub = n.Spec.IPv6.Subnet
		} else if !a.Is6() && n.Spec.IPv4 != nil {
			sub = n.Spec.IPv4.Subnet
		}
		if pf, err := netip.ParsePrefix(sub); err != nil || !pf.Contains(a) {
			return api.Errorf(api.CodeInvalid, "address %s is outside the subnet of network %q", addr, network)
		}
	}
	if owner, taken := s.usedAddrs(network, s.jails.keyIn(project, jail))[a]; taken {
		return api.Errorf(api.CodeIPConflict, "address %s is already used by jail %q", addr, owner).WithDetail("address", addr)
	}
	return nil
}

func (s *Server) leases(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rc := s.info(r)
	s.mu.Lock()
	_, nw := s.networks.find(r, name)
	var out []api.Lease
	if nw != nil {
		for _, l := range s.leasesOf(name) {
			if rc.canProject(l.project) {
				out = append(out, l.Lease)
			}
		}
	}
	s.mu.Unlock()
	if nw == nil {
		writeErr(w, notFound("network", name))
		return
	}
	if out == nil {
		out = []api.Lease{}
	}
	writeSync(w, 200, out)
}

// --- images ---

func (s *Server) setupImages() {
	k := s.images
	k.keep = func(dst, old *api.Image) { dst.Status = old.Status }
	k.validate = func(i, _ *api.Image) *api.Error {
		if i.Spec.OS == "" || i.Spec.Release == "" {
			return api.Errorf(api.CodeInvalid, "spec.os and spec.release are required")
		}
		return nil
	}
	k.beforeDelete = func(i *api.Image) *api.Error {
		for _, j := range s.jails.items {
			if imageName(j.Spec.Image) == i.Metadata.Name {
				return api.Errorf(api.CodeImageInUse, "image %q is used by jail %q", i.Metadata.Name, j.Metadata.Name)
			}
		}
		return nil
	}
	k.onDelete = func(i *api.Image) {
		delete(s.gens, i.Metadata.Name)
		delete(s.genMax, i.Metadata.Name)
		delete(s.aliases, i.Metadata.Name)
	}
	k.decorate = func(i *api.Image) {
		i.Status.Generations = nil
		for _, g := range s.gens[i.Metadata.Name] {
			c := clone(g)
			c.Refs = s.genRefs(i.Metadata.Name, g.Number)
			i.Status.Generations = append(i.Status.Generations, c)
		}
		i.Status.Aliases = nil
		if len(s.aliases[i.Metadata.Name]) > 0 {
			i.Status.Aliases = maps.Clone(s.aliases[i.Metadata.Name])
		}
	}
}

func (s *Server) genRefs(image string, n int) []string {
	var refs []string
	for _, j := range s.jails.items {
		if imageName(j.Spec.Image) == image && genNumber(j.Status.Generation) == n {
			refs = append(refs, j.Metadata.Name)
		}
	}
	sort.Strings(refs)
	return refs
}

func (s *Server) genList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	_, im := s.images.find(r, name)
	exists := im != nil
	out := []api.ImageGeneration{}
	for _, g := range s.gens[name] {
		c := clone(g)
		c.Refs = s.genRefs(name, g.Number)
		out = append(out, c)
	}
	s.mu.Unlock()
	if !exists {
		writeErr(w, notFound("image", name))
		return
	}
	writeSync(w, 200, out)
}

func (s *Server) genBuild(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.ImageBuild](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	s.mu.Lock()
	rp := func() reply {
		if s.images.items[name] == nil {
			return reply{err: notFound("image", name)}
		}
		if b.FromGeneration < 0 || (b.FromGeneration > 0 && s.findGen(name, b.FromGeneration) == nil) {
			return reply{err: api.Errorf(api.CodeInvalid, "generation g%d does not exist", b.FromGeneration)}
		}
		op := s.startOp("", "images.build", "Build "+name, map[string][]string{"images": {"/api/v1/images/" + name}}, func() (any, *api.Error) {
			if s.images.items[name] == nil {
				return nil, notFound("image", name)
			}
			n := s.nextGen(name)
			g := &api.ImageGeneration{
				Number: n, Name: fmt.Sprintf("%s#g%d", name, n), Parent: b.FromGeneration,
				Manifest: "sha256:" + randHex(16), Packages: 120, CreatedAt: s.now(),
				Dataset: fmt.Sprintf("tank/jailops/images/%s/g%d", name, n),
			}
			if !b.DryRun {
				s.gens[name] = append(s.gens[name], g)
				s.genMax[name] = n
				s.followAliases(name, n)
				s.emit("image-generation-created", "/api/v1/images/"+name+"/generations/"+strconv.Itoa(n))
			}
			return clone(g), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) genDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	n, _ := strconv.Atoi(r.PathValue("n"))
	s.mu.Lock()
	rp := func() reply {
		idx := -1
		for i, g := range s.gens[name] {
			if g.Number == n {
				idx = i
			}
		}
		if idx < 0 {
			return reply{err: notFound("generation", r.PathValue("n"))}
		}
		if refs := s.genRefs(name, n); len(refs) > 0 {
			return reply{err: api.Errorf(api.CodeImageInUse, "generation g%d is used by jail %s", n, strings.Join(refs, ", ")).
				WithDetail("jails", strings.Join(refs, ",")).WithHint("upgrade or destroy those jails first")}
		}
		op := s.startOp("", "images.delete-generation", "Delete generation", nil, func() (any, *api.Error) {
			l := s.gens[name]
			for i, g := range l {
				if g.Number == n {
					s.gens[name] = append(l[:i:i], l[i+1:]...)
					s.moveAliasesFrom(name, n)
					return nil, nil
				}
			}
			return nil, notFound("generation", strconv.Itoa(n))
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// nextGen returns the next generation number of an image; numbers are never reused.
func (s *Server) nextGen(image string) int {
	n := s.genMax[image]
	for _, g := range s.gens[image] {
		n = max(n, g.Number)
	}
	return n + 1
}

func (s *Server) findGen(image string, n int) *api.ImageGeneration {
	for _, g := range s.gens[image] {
		if g.Number == n {
			return g
		}
	}
	return nil
}

// followAliases points the aliases named in the image spec at the new generation, as a build does.
func (s *Server) followAliases(image string, n int) {
	im := s.images.items[image]
	if im == nil {
		return
	}
	for _, a := range im.Spec.Aliases {
		if s.aliases[image] == nil {
			s.aliases[image] = map[string]int{}
		}
		s.aliases[image][a] = n
	}
}

// moveAliasesFrom moves the aliases that pointed at a deleted generation to the newest remaining one, or drops them.
func (s *Server) moveAliasesFrom(image string, n int) {
	newest := 0
	for _, g := range s.gens[image] {
		newest = max(newest, g.Number)
	}
	for a, an := range s.aliases[image] {
		if an != n {
			continue
		}
		if newest > 0 {
			s.aliases[image][a] = newest
		} else {
			delete(s.aliases[image], a)
		}
	}
}

func (s *Server) aliasSet(w http.ResponseWriter, r *http.Request) {
	name, alias := r.PathValue("name"), r.PathValue("alias")
	b, e := decode[api.ImageAliasSet](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, im := s.images.find(r, name)
	if im == nil {
		writeErr(w, notFound("image", name))
		return
	}
	n := (*b).Generation
	if n == 0 {
		for _, g := range s.gens[name] {
			n = max(n, g.Number)
		}
		if n == 0 {
			writeErr(w, notFound("generation", "newest").WithHint("build one first"))
			return
		}
	} else if s.findGen(name, n) == nil {
		writeErr(w, notFound("generation", strconv.Itoa(n)))
		return
	}
	if s.aliases[name] == nil {
		s.aliases[name] = map[string]int{}
	}
	s.aliases[name][alias] = n
	if !slices.Contains(im.Spec.Aliases, alias) {
		im.Spec.Aliases = append(im.Spec.Aliases, alias)
		im.Metadata.Generation++
	}
	out := clone(im)
	s.images.decorate(&out)
	writeSync(w, 200, out)
}

func (s *Server) aliasDelete(w http.ResponseWriter, r *http.Request) {
	name, alias := r.PathValue("name"), r.PathValue("alias")
	s.mu.Lock()
	defer s.mu.Unlock()
	_, im := s.images.find(r, name)
	if im == nil {
		writeErr(w, notFound("image", name))
		return
	}
	_, had := s.aliases[name][alias]
	if !had && !slices.Contains(im.Spec.Aliases, alias) {
		writeErr(w, notFound("alias", alias))
		return
	}
	delete(s.aliases[name], alias)
	if slices.Contains(im.Spec.Aliases, alias) {
		im.Spec.Aliases = slices.DeleteFunc(im.Spec.Aliases, func(a string) bool { return a == alias })
		im.Metadata.Generation++
	}
	out := clone(im)
	s.images.decorate(&out)
	writeSync(w, 200, out)
}
