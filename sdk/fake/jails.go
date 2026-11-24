package fake

import (
	"fmt"
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
	k.decorate = func(j *api.Jail) { j.Status.Exposed = s.fakeReach(j.Metadata.Project, j).Exposed }
	k.expand = func(j *api.Jail) any { return s.jailExpanded(j) }
	k.validate = func(j, old *api.Jail) *api.Error {
		if j.Spec.Image == "" {
			return api.Errorf(api.CodeInvalid, "spec.image is required")
		}
		if e := s.checkJail(j, old); e != nil {
			return e
		}
		if e := s.checkAttachments(j, old); e != nil {
			return e
		}
		// a new jail needs its image; an existing one is a copy and outlives it
		if old == nil && s.images.items[imageName(j.Spec.Image)] == nil {
			return notFound("image", imageName(j.Spec.Image)).WithHint(s.imageCreateHint(j.Spec.Image))
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
	k.onDelete = func(j *api.Jail) {
		delete(s.snapshots, j.Metadata.Project+"/"+j.Metadata.Name)
	}
}

// assignJail fills defaults and allocates "auto" addresses. s.mu is held.
func (s *Server) assignJail(j, old *api.Jail) *api.Error {
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
				if n.Spec.IPv6 != nil {
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
	// the server gives a jail the OS of its image
	if im := s.images.items[imageName(j.Spec.Image)]; j.Spec.OS == "" && im != nil {
		j.Spec.OS = im.Spec.OS
	}
	j.Status = api.JailStatus{Power: api.PowerStopped, ImageGeneration: "g1"}
	if i := strings.IndexByte(j.Spec.Image, '#'); i >= 0 {
		j.Status.ImageGeneration = j.Spec.Image[i+1:]
	} else if g := s.gens[imageName(j.Spec.Image)]; len(g) > 0 {
		j.Status.ImageGeneration = "g" + strconv.Itoa(g[len(g)-1].Number)
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

// SeedJailUsage sets what GET /api/v1/jails/{name}/usage answers for a jail;
// power and the jail name are filled in from the jail when the reply is
// built. Without it a running jail reports fixed numbers and a stopped jail
// only its disk.
func (s *Server) SeedJailUsage(project, name string, u api.JailUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		s.usage = map[string]api.JailUsage{}
	}
	s.usage[project+"/"+name] = u
}

// jailBase answers the base packages of a FreeBSD jail: the fake keeps no packages, so the list is the minimal jail set's
// runtime and whatever upgrades added (nothing).
func (s *Server) jailBase(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	s.mu.Unlock()
	switch {
	case j == nil:
		writeErr(w, notFound("jail", r.PathValue("name")))
	case j.Spec.OS == api.OSLinux:
		writeErr(w, api.Errorf(api.CodeUnsupported, "base packages are listed for FreeBSD jails"))
	default:
		writeSync(w, 200, api.JailBase{Jail: j.Metadata.Name, Packages: []api.JailBasePackage{{Name: "FreeBSD-runtime", Version: "15.1p2"}}})
	}
}

// jailAudit answers the audit of a FreeBSD jail: the fake has no root to read, so every check passes; suggest=true adds
// two suggestions, one a note. A jail labelled audit=critical has a second account with uid 0, for tests of the exit
// status.
func (s *Server) jailAudit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	s.mu.Unlock()
	switch {
	case j == nil:
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	case j.Spec.OS == api.OSLinux:
		writeErr(w, api.Errorf(api.CodeInvalid, "jail %q is a Linux jail; the audit reads a FreeBSD jail", j.Metadata.Name))
		return
	}
	a := api.JailAudit{Jail: j.Metadata.Name, Running: j.Status.Power == api.PowerRunning, Checks: []api.JailAuditCheck{
		{ID: "root.links", Group: "security", Result: api.AuditPass, Detail: "no link out of the root on a path the base's packages own"},
		{ID: "accounts.uid0", Group: "security", Result: api.AuditPass, Detail: "root and toor are the only accounts with uid 0"},
		{ID: "pkg.base_repository", Group: "security", Result: api.AuditPass, Detail: "FreeBSD-base is disabled inside the jail; the host upgrades the base"},
		{ID: "pkg.kmods_repository", Group: "security", Result: api.AuditPass, Detail: "FreeBSD-ports-kmods is disabled inside the jail"},
		{ID: "pkg.vulnerabilities", Group: "security", Result: api.AuditPass, Detail: "pkg audit names no vulnerable package"},
	}}
	if r.URL.Query().Get("suggest") == "true" {
		a.Checks = append(a.Checks,
			api.JailAuditCheck{ID: "cron.jitter", Group: "practice", Result: api.AuditPass, Suggestion: true, Detail: `cron starts the jobs of root (periodic among them) at a random second: cron_flags="-J 60"`},
			api.JailAuditCheck{ID: "disk.limit", Group: "practice", Result: api.AuditInfo, Suggestion: true, Detail: "the jail has no disk limit: it can fill its storage", Fix: "jailops config set " + j.Metadata.Name + " resources.disk=10G (a size that fits the jail)"})
	}
	if j.Metadata.Labels["audit"] == "critical" {
		a.Checks[1] = api.JailAuditCheck{ID: "accounts.uid0", Group: "security", Result: api.AuditCritical, Detail: "accounts with uid 0 besides root and toor: backdoor",
			Fix: "remove each account (jailops exec " + j.Metadata.Name + " -- pw userdel NAME) unless you made it on purpose, and find out how it got there"}
		a.Critical = 1
	}
	writeSync(w, 200, a)
}

func (s *Server) jailUsage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	key, j := s.jails.find(r, r.PathValue("name"))
	var u api.JailUsage
	if j != nil {
		seeded, ok := s.usage[key]
		switch {
		case ok:
			u = seeded
		case j.Status.Power == api.PowerRunning:
			u = api.JailUsage{Accounting: &api.JailAccounting{Processes: 23, CPUSeconds: 8013, MemoryBytes: 407048192, SwapBytes: 268435456}}
		}
		if !ok {
			u.Disk = api.JailDiskUsage{RootBytes: 268435456, RootReferencedBytes: 1610612736, DataBytes: 73400320}
		}
		u.Jail, u.Power, u.MeasuredAt = j.Metadata.Name, j.Status.Power, s.now().UTC()
		if u.Power != api.PowerRunning {
			u.Accounting = nil
		}
	}
	s.mu.Unlock()
	if j == nil {
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	}
	writeSync(w, 200, u)
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
		if l := s.jails.items[jk].Status.Locked; act.Action != "stop" && len(l) > 0 {
			return reply{err: api.Errorf(api.CodeLocked, "jail %q is locked: the encrypted volume(s) %s wait for a passphrase", name, strings.Join(l, ", ")).
				WithHint("jailops volume unlock " + l[0])}
		}
		if act.Action != "stop" {
			for _, a := range s.jails.items[jk].Spec.Volumes {
				if v := s.volumes.items[rc.project+"/"+a.Name]; v != nil && v.Spec.IsHostDir() && s.vol.lostDirs[v.Spec.Path] {
					return reply{err: api.Errorf(api.CodeBusy, "volume %q is lost: the directory is not on a mount (%s)", a.Name, v.Spec.Path).
						WithHint("mount it again; the jail starts when it is there")}
				}
			}
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
			// the fake's upgrades find nothing newer, so no jail has a snapshot of an upgrade to go back to
			return reply{err: api.Errorf(api.CodeNotFound, "jail %s has no snapshot from an in-place upgrade to go back to", name)}
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
			if !rb.DestroyNewer {
				var later []string
				seen := false
				for _, sn := range l {
					if seen {
						later = append(later, sn.Metadata.Name)
					}
					seen = seen || sn == target
				}
				if len(later) > 0 {
					return reply{err: api.Errorf(api.CodePreconditionFailed, "snapshots taken after %q exist: %s; a rollback to %q destroys them", target.Metadata.Name, strings.Join(later, ", "), target.Metadata.Name).
						WithDetail("later_snapshots", strings.Join(later, ",")).
						WithHint("pass --destroy-newer with --confirm-data-loss (destroy_newer and confirm_data_loss in the API)")}
				}
			}
		default:
			return reply{err: api.Errorf(api.CodeInvalid, "unknown rollback scope %q", rb.Scope)}
		}
		op := s.startOp(rc.project, "jails.rollback", "Rollback "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[jk]
			if j == nil {
				return nil, notFound("jail", name)
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

// jailCopy implements POST /api/v1/jails/{name}: a copy with the data of the source (the fake keeps no data): the same
// specification under a new name, with addresses of its own, no volume and no exposure.
func (s *Server) jailCopy(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.JailCopy](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	src := r.PathValue("name")
	rc := s.info(r)
	sk := s.jails.key(r, src)
	s.mu.Lock()
	rp := func() reply {
		from := s.jails.items[sk]
		if from == nil {
			return reply{err: notFound("jail", src)}
		}
		if !validName(req.Name) {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid name %q", req.Name)}
		}
		dk := s.jails.keyIn(rc.project, req.Name)
		if s.jails.items[dk] != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "jail %q already exists", req.Name)}
		}
		if req.Snapshot != "" && s.findSnap(sk, req.Snapshot) == nil {
			return reply{err: notFound("snapshot", req.Snapshot).WithHint("jailops jail snapshot list " + src)}
		}
		op := s.startOp(rc.project, "jails.copy", "Copy jail "+src+" to "+req.Name, map[string][]string{"jails": {"/api/v1/jails/" + src, "/api/v1/jails/" + req.Name}}, func() (any, *api.Error) {
			from := s.jails.items[sk]
			if from == nil {
				return nil, notFound("jail", src)
			}
			nj := clone(from)
			nj.Metadata = api.ObjectMeta{Name: req.Name, Project: rc.project, Labels: from.Metadata.Labels}
			if req.Labels != nil {
				nj.Metadata.Labels = req.Labels
			}
			nj.Spec.Volumes, nj.Spec.Expose = nil, nil
			for i := range nj.Spec.Network {
				n := &nj.Spec.Network[i]
				n.MAC = ""
				for _, p := range []*string{&n.IPv4, &n.IPv6} {
					if *p != "" && *p != "none" {
						*p = "auto"
					}
				}
			}
			if e := s.assignJail(&nj, nil); e != nil {
				return nil, e
			}
			s.initJailStatus(&nj)
			s.jails.insert(&nj, false)
			return clone(&nj), nil
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
		if j.Spec.OS == api.OSLinux {
			return reply{err: api.Errorf(api.CodeUnsupported, "Linux jails look after their own packages: JailOps does not update them").WithHint("run apk or apt in the jail, for instance from its cron")}
		}
		if len(up.AddPackages) > 0 {
			// base packages: the fake keeps no packages
			op := s.startOp(rc.project, "jails.upgrade", "Upgrade "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
				return clone(s.jails.items[jk]), nil
			})
			return reply{op: &op}
		}
		// a jail is upgraded in place by pkg: the fake finds nothing newer
		op := s.startOp(rc.project, "jails.upgrade", "Upgrade "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			return map[string]any{"jail": clone(s.jails.items[jk]), "upgrade": map[string]any{"nothing_to_upgrade": true, "installed": "15.1-RELEASE-p7"}}, nil
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
	// The real daemon notes, in the answer of a create, the setting a shared IPv6 segment's routers may need (the
	// sysctl is 0; see WithSysctl).
	k.created = func(n *api.Network) {
		const oid = "net.inet6.icmp6.nd6_onlink_ns_rfc4861"
		if n.Spec.IPv6 != nil && n.Spec.IPv6.NDPProxy && s.sysctls[oid] == "0" {
			n.Status.Notes = append(n.Status.Notes, "on some shared IPv6 segments the setting "+oid+" matters (it is 0): https://jailops.net/articles/shared-ipv6-neighbor-discovery/#fix")
		}
	}
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
	k.listed = func(rc *reqInfo, all bool, n *api.Network) bool {
		return all || rc.project == "" || slices.Contains(s.projectsGranting(func(sp api.ProjectSpec) bool {
			return slices.ContainsFunc(sp.Networks, func(g api.ProjectNetwork) bool { return g.Network == n.Metadata.Name })
		}), rc.project)
	}
	k.decorate = func(n *api.Network) {
		n.Status.GrantedTo = s.projectsGranting(func(sp api.ProjectSpec) bool {
			return slices.ContainsFunc(sp.Networks, func(g api.ProjectNetwork) bool { return g.Network == n.Metadata.Name })
		})
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
		rng, excl = n.Spec.IPv6.Range, n.Spec.IPv6.Exclude
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
		if au := i.Spec.AutoUpgrade; au != nil && au.At != "" {
			if _, err := time.Parse("15:04", au.At); err != nil || len(au.At) != 5 {
				return api.Errorf(api.CodeInvalid, "spec.auto_upgrade.at %q is not a time of day such as 03:00", au.At)
			}
		}
		return nil
	}
	// no jail depends on an image (a jail is an independent copy): an image is deleted while jails made from it exist
	k.onDelete = func(i *api.Image) {
		delete(s.gens, i.Metadata.Name)
		delete(s.genMax, i.Metadata.Name)
	}
	k.decorate = func(i *api.Image) {
		i.Status.Generations = nil
		for _, g := range s.gens[i.Metadata.Name] {
			c := clone(g)
			i.Status.Generations = append(i.Status.Generations, c)
		}
		i.Status.AutoUpgrade = nil
		if au := i.Spec.AutoUpgrade; au != nil && au.Enabled {
			i.Status.AutoUpgrade = &api.ImageAutoUpgradeStatus{NextAt: nextAt(s.now(), au.At)}
		}
	}
}

// nextAt is the next time of day at (HH:MM, empty for the default) after now, in now's zone.
func nextAt(now time.Time, at string) time.Time {
	if at == "" {
		at = api.DefaultAutoUpgradeAt
	}
	t, err := time.Parse("15:04", at)
	if err != nil {
		return time.Time{}
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (s *Server) genList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	_, im := s.images.find(r, name)
	exists := im != nil
	out := []api.ImageGeneration{}
	for _, g := range s.gens[name] {
		c := clone(g)
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
				s.emit("image-generation-created", "/api/v1/images/"+name+"/generations/"+strconv.Itoa(n))
			}
			return clone(g), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// imageUpgrade implements POST /api/v1/images/{name}/upgrade: the next generation from the newest one. No jail is
// touched. An image seeded with SeedImageCurrent has nothing to upgrade: no generation.
func (s *Server) imageUpgrade(w http.ResponseWriter, r *http.Request) {
	u, e := decodeOptional[api.ImageUpgrade](r)
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
		from := 0
		for _, g := range s.gens[name] {
			from = max(from, g.Number)
		}
		if from == 0 {
			return reply{err: api.Errorf(api.CodeNotFound, "image %q has no generation to upgrade", name).
				WithHint("build the first one with 'jailops image build " + name + "'")}
		}
		res := map[string][]string{"images": {"/api/v1/images/" + name}}
		op := s.startOp("", "images.upgrade", "Upgrade image "+name, res, func() (any, *api.Error) {
			if s.images.items[name] == nil {
				return nil, notFound("image", name)
			}
			out := api.ImageUpgradeResult{From: fmt.Sprintf("%s#g%d", name, from), DryRun: u.DryRun, Unchanged: s.imgSame[name]}
			n := s.nextGen(name)
			out.Plan = []string{fmt.Sprintf("zfs clone tank/jailops/images/%s/g%d@ro tank/jailops/images/%s/staging-g%d", name, from, name, n),
				fmt.Sprintf("pkg -r /jailops/images/%s/staging-g%d upgrade -y -r jailops", name, n)}
			if !u.DryRun && !out.Unchanged {
				g := &api.ImageGeneration{Number: n, Name: fmt.Sprintf("%s#g%d", name, n), Parent: from,
					Manifest: "sha256:" + randHex(16), Packages: 120, CreatedAt: s.now(), Dataset: fmt.Sprintf("tank/jailops/images/%s/g%d", name, n)}
				s.gens[name] = append(s.gens[name], g)
				s.genMax[name] = n
				s.emit("image-generation-created", "/api/v1/images/"+name+"/generations/"+strconv.Itoa(n))
				c := clone(g)
				out.Generation = &c
				s.emit("image-upgraded", "/api/v1/images/"+name)
			}
			return out, nil
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
		op := s.startOp("", "images.delete-generation", "Delete generation", nil, func() (any, *api.Error) {
			l := s.gens[name]
			for i, g := range l {
				if g.Number == n {
					s.gens[name] = append(l[:i:i], l[i+1:]...)
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
