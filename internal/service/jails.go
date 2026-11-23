package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// ---- registry ----
//
// The registry is keyed by the project-qualified jail key (validate.QualifiedName):
// "<project>_<name>" (a jail always belongs to a project). The
// key is what the OS, the datasets, the lifecycle and the reconciler know; the
// API speaks (project, name) and converts at the edge.

// RefreshJails rebuilds the in-memory registry from the jails' own datasets
// (ZFS is authoritative for host-scope jails), on every storage.
func (s *Service) RefreshJails(ctx context.Context) error {
	if s.life == nil {
		return nil
	}
	found, err := s.life.Discover(ctx)
	if err != nil {
		return err
	}
	m := map[string]*jailEntry{}
	for _, f := range found {
		project, name := validate.SplitQualified(f.Key)
		r, err := s.life.RebuildOn(ctx, f)
		if err != nil {
			s.log.Warn("jail cannot be rebuilt", "jail", f.Key, "err", err)
			m[f.Key] = &jailEntry{jail: api.Jail{Metadata: api.ObjectMeta{Name: name, Project: project, Scope: api.ScopeHost, Host: s.d.HostUUID},
				Spec: api.JailSpec{Storage: f.Storage.Name}}, problems: []string{err.Error()}}
			continue
		}
		j := r.Jail
		j.Metadata.Host = s.d.HostUUID
		if j.Spec.Storage == "" {
			j.Spec.Storage = f.Storage.Name
		}
		m[f.Key] = &jailEntry{jail: j, problems: r.Problems, locked: r.Locked, incomplete: s.life.UpgradeIncomplete(ctx, f.Key), suspected: s.life.Suspected(ctx, f.Key), unsupported: r.UnsupportedLayout}
		if r.UnsupportedLayout {
			s.log.Warn("jail has a root layout this release does not support: it does not start; a running one can be stopped, and it can be deleted", "jail", f.Key)
		}
	}
	s.mu.Lock()
	s.jails = m
	s.mu.Unlock()
	return nil
}

func (s *Service) refreshJail(ctx context.Context, key string) error {
	r, err := s.life.Rebuild(ctx, key)
	if err != nil {
		return err
	}
	j := r.Jail
	j.Metadata.Host = s.d.HostUUID
	if j.Spec.Storage == "" {
		if si, ok := s.storageOfJail(key); ok {
			j.Spec.Storage = si.Name
		}
	}
	inc, sus := s.life.UpgradeIncomplete(ctx, key), s.life.Suspected(ctx, key)
	s.mu.Lock()
	s.jails[key] = &jailEntry{jail: j, problems: r.Problems, locked: r.Locked, incomplete: inc, suspected: sus, unsupported: r.UnsupportedLayout}
	s.mu.Unlock()
	s.unplace(key)
	return nil
}

func (s *Service) jailSnapshot(key string) (api.Jail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.jails[key]
	if !ok {
		return api.Jail{}, notFound("jail", key)
	}
	return cloneJail(e.jail), nil
}

// jailRef resolves a jail named in a request: the project of the request
// (checked: a project the caller may not use is not_found) and the key.
func (s *Service) jailRef(ctx context.Context, name string) (p project, key string, err error) {
	p, err = s.access(ctx)
	if err != nil {
		return p, "", err
	}
	key, err = validate.QualifiedName(p.Meta.Name, name)
	if err != nil {
		// a name that cannot be a jail is not a jail of this project
		return p, "", notFound("jail", name)
	}
	return p, key, nil
}

func cloneJail(j api.Jail) api.Jail {
	b, _ := json.Marshal(j)
	var out api.Jail
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Service) markStarted(name string) {
	s.mu.Lock()
	rt := s.status[name]
	rt.startedAt = s.now().UTC()
	s.status[name] = rt
	s.mu.Unlock()
}

func (s *Service) markStopped(name string) {
	s.mu.Lock()
	rt := s.status[name]
	rt.startedAt = time.Time{}
	s.status[name] = rt
	s.mu.Unlock()
}

// view builds the API form of a jail with its observed status. running is
// the observed set (nil means ask).
func (s *Service) view(ctx context.Context, e *jailEntry, running map[string]int) api.Jail {
	j := cloneJail(e.jail)
	name := jailKey(j)
	st := api.JailStatus{Power: api.PowerStopped, ImageGeneration: e.jail.Status.ImageGeneration}
	// a jail is at the release installed in it (its generation is where it came from)
	if st.FreeBSDVersion = e.jail.Status.FreeBSDVersion; st.FreeBSDVersion == "" {
		st.FreeBSDVersion = s.generationFreeBSD(ctx, st.ImageGeneration)
	}
	jid, up := 0, false
	if running != nil {
		jid, up = running[name], false
		_, up = running[name]
	} else if s.d.Jails != nil {
		if o, ok, err := s.d.Jails.Get(ctx, name); err == nil && ok {
			jid, up = o.JID, true
		}
	}
	s.mu.RLock()
	rt := s.status[name]
	s.mu.RUnlock()
	if up {
		st.Power, st.JID = api.PowerRunning, jid
		st.StartedAt = rt.startedAt
		for _, a := range jailAddrs(j.Spec) {
			st.Addresses = append(st.Addresses, a.String())
		}
	}
	if up {
		if c, w, ok := s.limitsCondition(ctx, j); ok {
			st.Conditions = append(st.Conditions, c)
			st.Warnings = append(st.Warnings, w)
		}
	}
	if w := s.openInboundWarning(j); w != "" {
		st.Warnings = append(st.Warnings, w)
	}
	st.Drift = rt.drift
	st.ProvisionedAt = s.provisionedAt(name)
	st.Conditions = append(st.Conditions, rt.conditions...)
	shellConds, shellSetup := s.shellCondition(name)
	st.Conditions = append(st.Conditions, shellConds...)
	st.ShellSetup = shellSetup
	if e.unsupported {
		st.Conditions = append(st.Conditions, api.Condition{Type: api.ConditionUnsupportedLayout, Status: api.ConditionTrue, Reason: "EarlierRelease", Since: s.now().UTC(),
			Message: unsupportedLayoutMessage(j.Metadata.Name)})
	}
	if e.incomplete != "" {
		st.Conditions = append(st.Conditions, api.Condition{Type: "UpgradeIncomplete", Status: api.ConditionTrue, Reason: "RollbackFailed", Since: s.now().UTC(),
			Message: e.incomplete + "; recover it with `jailops jail rollback " + j.Metadata.Name + " --project " + j.Metadata.Project + " --confirm-data-loss`"})
	}
	if e.suspected != "" {
		st.Conditions = append(st.Conditions, api.Condition{Type: "SuspectedCompromised", Status: api.ConditionTrue, Reason: "PreflightFinding", Since: s.now().UTC(),
			Message: "the root holds what only an attack on host-side pkg puts there: " + e.suspected + "; the jail is not upgraded until a check finds nothing"})
	}
	if len(e.problems) > 0 {
		st.Conditions = append(st.Conditions, api.Condition{Type: "Healthy", Status: api.ConditionFalse, Reason: "Degraded",
			Message: strings.Join(e.problems, "; "), Since: s.now().UTC()})
		if !up {
			st.Power = api.PowerError
		}
	}
	s.mu.RLock()
	locked := append([]string(nil), e.locked...)
	s.mu.RUnlock()
	if !up && len(locked) > 0 {
		st.Locked = locked
		if st.Power == api.PowerStopped {
			st.Power = api.PowerLocked
		}
	}
	j.Status = st
	return j
}

func (s *Service) runningJails(ctx context.Context) map[string]int {
	out := map[string]int{}
	if s.d.Jails == nil {
		return out
	}
	list, err := s.d.Jails.List(ctx)
	if err != nil {
		return out
	}
	for _, j := range list {
		if !j.Dying {
			out[j.Name] = j.JID
		}
	}
	return out
}

// ---- reads ----

// ListJails returns the jails of the request's project.
func (s *Service) ListJails(ctx context.Context, labels map[string]string) ([]api.Jail, error) {
	p, err := s.access(ctx)
	if err != nil {
		return nil, err
	}
	run := s.runningJails(ctx)
	s.mu.RLock()
	names := sortedKeys(s.jails)
	entries := make([]*jailEntry, 0, len(names))
	for _, n := range names {
		if e := s.jails[n]; e.jail.Metadata.Project == p.Meta.Name {
			entries = append(entries, e)
		}
	}
	s.mu.RUnlock()
	out := []api.Jail{}
	var keys []string
	for _, e := range entries {
		if MatchLabels(e.jail.Metadata.Labels, labels) {
			out = append(out, s.view(ctx, e, run))
			keys = append(keys, jailKey(e.jail))
		}
	}
	used := s.jailsDiskUsed(ctx, keys)
	refs, refsErr := s.allRefs(p.Meta.Name)
	for i, k := range keys {
		out[i].Status.DiskUsed = used[k]
		if refsErr == nil {
			out[i].Status.Exposed = s.exposedOf(ctx, out[i], refs)
		}
	}
	return out, nil
}

// GetJail returns one jail of the request's project.
func (s *Service) GetJail(ctx context.Context, name string) (api.Jail, error) {
	_, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Jail{}, err
	}
	j, err := s.getByKey(ctx, key)
	if err == nil {
		j.Status.DiskUsed = s.jailsDiskUsed(ctx, []string{key})[key]
		if refs, rerr := s.allRefs(j.Metadata.Project); rerr == nil {
			j.Status.Exposed = s.exposedOf(ctx, j, refs)
		}
	}
	return j, err
}

func (s *Service) getByKey(ctx context.Context, key string) (api.Jail, error) {
	s.mu.RLock()
	e, ok := s.jails[key]
	s.mu.RUnlock()
	if !ok {
		_, plain := validate.SplitQualified(key)
		return api.Jail{}, notFound("jail", plain)
	}
	return s.view(ctx, e, nil), nil
}

// JailState returns the observed status.
func (s *Service) JailState(ctx context.Context, name string) (api.JailStatus, error) {
	j, err := s.GetJail(ctx, name)
	return j.Status, err
}

// ---- create ----

func (s *Service) imageFor(spec api.JailSpec) (object[api.ImageSpec], validate.ImageRef, error) {
	ref, err := validate.ParseImageRef(spec.Image)
	if err != nil {
		return object[api.ImageSpec]{}, ref, err
	}
	im, err := s.imgKind.get(ref.Name)
	if err != nil {
		return im, ref, api.Errorf(api.CodeNotFound, "image %q not found", ref.Name).WithHint(api.ImageCreateHint("it", ref.Name, s.hostBaseRelease()))
	}
	return im, ref, nil
}

// checkJailRefs checks what a jail of a project refers to: its networks, and
// its firewall policy in the same project. Grants were checked before.
func (s *Service) checkJailRefs(project string, spec api.JailSpec) error {
	for _, a := range spec.Network {
		if !s.netKind.exists(a.Network) {
			return notFound("network", a.Network)
		}
	}
	pr, perr := s.projKind.get(project)
	for _, p := range spec.Firewall.Policies {
		o, err := s.polKind.p(project).get(p)
		if err != nil {
			if api.IsCode(err, api.CodeNotFound) {
				return notFound("firewall-policy", p)
			}
			return err
		}
		if o.Meta.ManagedBy != "" {
			return invalid("firewall policy %q is managed by %s: it belongs to its own jail and is not a policy to attach", p, o.Meta.ManagedBy).
				WithHint("write a policy of your own (jailops firewall policy create); app access keeps app-JAIL")
		}
		if perr == nil && slices.Contains(pr.Spec.FirewallPolicies, p) {
			return invalid("firewall policy %q is a mandatory policy of project %s: it is rendered for every jail already", p, project).
				WithHint("name policies of the jail's own, which add to it")
		}
	}
	return nil
}

func netLocks(spec api.JailSpec) []string {
	var out []string
	for _, a := range spec.Network {
		out = append(out, networkLock(a.Network))
	}
	return out
}

// imageLocks serialises the first use of an image on a pool: the generation
// is built or received there.
func (s *Service) imageLocks(ctx context.Context, ref validate.ImageRef, storage string) []string {
	si, ok := s.storage(storage)
	if !ok || si.Images == nil {
		return nil
	}
	if obs, err := si.Images.Observe(ctx, ref.Name); err != nil || len(obs.Status.Generations) == 0 {
		return []string{imageLock(ref.Name)}
	}
	return nil
}

// CreateJail creates a jail in the request's project as an operation whose
// result is the Jail. Grants, the image allow-list and the quotas are checked
// before anything is created.
func (s *Service) CreateJail(ctx context.Context, j api.Jail) (api.Operation, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.Operation{}, err
	}
	j.Spec.Shell = api.ShellPath(j.Spec.Shell) // the names bash and sh
	p, err := s.access(ctx)
	if err != nil {
		return api.Operation{}, err
	}
	if err := bodyProject(p.Meta.Name, j.Metadata.Project); err != nil {
		return api.Operation{}, err
	}
	name, proj := j.Metadata.Name, p.Meta.Name
	if err := s.validateJail(proj, name, j.Spec); err != nil {
		return api.Operation{}, err
	}
	if err := api.ValidLabels(j.Metadata.Labels); err != nil {
		return api.Operation{}, err
	}
	key, err := validate.QualifiedName(proj, name)
	if err != nil {
		return api.Operation{}, err
	}
	s.mu.RLock()
	_, exists := s.jails[key]
	s.mu.RUnlock()
	if exists {
		return api.Operation{}, api.Errorf(api.CodeAlreadyExists, "jail %q already exists", name)
	}
	spec := j.Spec
	spec.Network = append([]api.NetworkAttach(nil), spec.Network...)
	im, ref, err := s.imageFor(spec)
	if err != nil {
		return api.Operation{}, err
	}
	release, err := s.admit(ctx, p, key, &spec, nil)
	if err != nil {
		return api.Operation{}, err
	}
	submitted := false
	defer func() {
		if !submitted {
			release()
		}
	}()
	if err := s.validateJail(proj, name, spec); err != nil {
		return api.Operation{}, err
	}
	if err := canonicalJailSizes(&spec); err != nil {
		return api.Operation{}, err
	}
	if err := s.checkNewJail(proj, name, spec); err != nil {
		return api.Operation{}, err
	}
	if spec.Volumes, err = s.checkAttachments(proj, key, spec.Volumes); err != nil {
		return api.Operation{}, err
	}
	spec.OS = im.Spec.OS
	if spec.OS == api.OSLinux && spec.Prompt != "" {
		return api.Operation{}, invalid("the prompt key is for FreeBSD jails; a Linux jail keeps the prompt of its distribution")
	}
	if err := checkLinuxUpdatePolicy(spec, nil); err != nil {
		return api.Operation{}, err
	}
	if spec.Shell == "" && spec.OS == api.OSFreeBSD && p.Spec.Shell != "" {
		spec.Shell = p.Spec.Shell // the project's shell for jails that name none (sh or bash)
	}
	profs, _ := s.resolveProfiles(proj, spec.Profiles)
	s.shellDefaults(&spec, profs)
	if spec.Hostname == "" {
		spec.Hostname = name
	}
	meta := api.ObjectMeta{Name: name, Project: proj, UUID: state_uuid(), Scope: api.ScopeHost, Host: s.d.HostUUID, Generation: 1,
		Labels: j.Metadata.Labels, ManagedBy: j.Metadata.ManagedBy, CreatedAt: s.now().UTC(), UpdatedAt: s.now().UTC()}
	locks := append([]string{jailLock(key)}, s.netLocksEff(proj, spec)...)
	locks = append(locks, s.imageLocks(ctx, ref, spec.Storage)...)
	op, err := s.Submit(ctx, OpSpec{
		Action: api.OpJailCreate, Description: "Create jail " + name, Locks: locks, Project: proj,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			defer release()
			s.place(key, spec.Storage)
			ok := false
			defer func() {
				if !ok {
					s.unplace(key)
				}
			}()
			si, _ := s.storage(spec.Storage)
			g, _ := specStorageGrant(p.Spec, spec.Storage)
			if err := s.ensureProjectDatasets(ctx, proj, si, g); err != nil {
				return nil, err
			}
			if err := s.assignOwn(proj, key, &spec); err != nil {
				return nil, err
			}
			nj := api.Jail{Metadata: meta, Spec: spec}
			if err := h.Bind("create/"+key, &opRequest{Kind: "create", Jail: key, Create: &nj}); err != nil {
				return nil, err
			}
			if _, err := s.life.CreateJail(ctx, nj, false); err != nil {
				return nil, err
			}
			if err := s.refreshJail(ctx, key); err != nil {
				return nil, err
			}
			ok = true
			s.emitIn(ctx, proj, "jail-created", "/api/v1/jails/"+name)
			s.kick()
			return s.getByKey(ctx, key)
		},
	})
	if err == nil {
		submitted = true
	}
	return op, err
}

// ---- update ----

func (s *Service) writeJailRecord(ctx context.Context, j api.Jail) error {
	rec := lifecycle.Record{Version: 1, Metadata: j.Metadata, Spec: j.Spec}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	f := s.d.Files
	key := jailKey(j)
	sp := path.Join(s.jailMount(key), "spec.json")
	if err := f.WriteAtomic(ctx, sp, append(b, '\n')); err != nil {
		return err
	}
	bp := path.Join(s.jailMount(key), "bundle.json")
	var bd lifecycle.Bundle
	if raw, err := f.ReadFile(ctx, bp); err == nil {
		_ = json.Unmarshal(raw, &bd)
	}
	bd.Version, bd.Metadata, bd.Spec, bd.Volumes, bd.Written = 1, j.Metadata, j.Spec, j.Spec.Volumes, s.now().UTC()
	nb, err := json.MarshalIndent(bd, "", "  ")
	if err != nil {
		return err
	}
	return f.WriteAtomic(ctx, bp, append(nb, '\n'))
}

// UpdateJail replaces a jail's metadata labels and spec. Identity, scope,
// host and status are server-owned. Image, layout, OS, storage, encryption and
// volumes cannot change here: use upgrade for the image generation.
func (s *Service) UpdateJail(ctx context.Context, name string, in api.Jail, expect int64) (api.Jail, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.Jail{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Jail{}, err
	}
	return s.updateJail(ctx, p, key, name, in, expect, false)
}

// updateLocked is UpdateJail for a caller that holds the locks of the jail
// and of its networks (an operation).
func (s *Service) updateLocked(ctx context.Context, p project, key, name string, in api.Jail, expect int64) (api.Jail, error) {
	return s.updateJail(ctx, p, key, name, in, expect, true)
}

func (s *Service) updateJail(ctx context.Context, p project, key, name string, in api.Jail, expect int64, locked bool) (api.Jail, error) {
	if in.Metadata.Name != "" && in.Metadata.Name != name {
		return api.Jail{}, renameByAction("jail", "jails")
	}
	if err := bodyProject(p.Meta.Name, in.Metadata.Project); err != nil {
		return api.Jail{}, err
	}
	old, err := s.jailSnapshot(key)
	if err != nil {
		return api.Jail{}, notFound("jail", name)
	}
	spec := in.Spec
	spec.Network = append([]api.NetworkAttach(nil), spec.Network...)
	spec.Shell = api.ShellPath(spec.Shell) // the names bash and sh
	if spec.OS == "" {
		spec.OS = old.Spec.OS
	}
	if spec.Hostname == "" {
		spec.Hostname = old.Spec.Hostname
	}
	if spec.Storage == "" {
		spec.Storage = old.Spec.Storage
	}
	if err := s.validateJail(p.Meta.Name, name, spec); err != nil {
		return api.Jail{}, err
	}
	if err := canonicalJailSizes(&spec); err != nil {
		return api.Jail{}, err
	}
	oref, _ := validate.ParseImageRef(old.Spec.Image)
	nref, _ := validate.ParseImageRef(spec.Image)
	switch {
	case nref.Name != oref.Name:
		return api.Jail{}, invalid("the image of a jail cannot change").WithHint("create a new jail from the other image")
	case spec.OS != old.Spec.OS:
		return api.Jail{}, invalid("the os of a jail cannot change")
	case spec.Encrypted != old.Spec.Encrypted:
		return api.Jail{}, invalid("encryption cannot change")
	case spec.Storage != old.Spec.Storage:
		return api.Jail{}, invalid("the storage of a jail cannot change").WithHint("copy the jail to move it")
	case spec.OS == api.OSLinux && spec.Prompt != "":
		return api.Jail{}, invalid("the prompt key is for FreeBSD jails; a Linux jail keeps the prompt of its distribution")
	}
	if err := checkLinuxUpdatePolicy(spec, &old.Spec); err != nil {
		return api.Jail{}, err
	}
	if !volumesEqual(spec.Volumes, old.Spec.Volumes) {
		if spec.Volumes, err = s.attachmentsChanged(ctx, p.Meta.Name, key, spec.Volumes); err != nil {
			return api.Jail{}, err
		}
	}
	oldSpec := old.Spec
	if _, err := s.admit(ctx, p, key, &spec, &oldSpec); err != nil {
		return api.Jail{}, err
	}
	if !locked {
		locks := append([]string{jailLock(key)}, s.netLocksEff(p.Meta.Name, spec)...)
		unlock, err := s.locks.lockPatient(ctx, "update jail "+name, locks...)
		if err != nil {
			return api.Jail{}, err
		}
		defer unlock()
	}
	// Re-read under the lock: the generation check must see the latest state.
	old, err = s.jailSnapshot(key)
	if err != nil {
		return api.Jail{}, notFound("jail", name)
	}
	if err := checkIncarnation(ctx, "jail", name, old.Metadata.UUID); err != nil {
		return api.Jail{}, err
	}
	if expect != AnyGeneration && expect != old.Metadata.Generation {
		return api.Jail{}, api.Errorf(api.CodePreconditionFailed, "jail %q changed: current generation is %d", name, old.Metadata.Generation).
			WithHint("read it again and re-apply your change").WithDetail("generation", strconv.FormatInt(old.Metadata.Generation, 10))
	}
	// "auto" keeps the address the attachment already has.
	for i := range spec.Network {
		if i < len(old.Spec.Network) && old.Spec.Network[i].Network == spec.Network[i].Network {
			if spec.Network[i].IPv4 == "auto" && old.Spec.Network[i].IPv4 != "" {
				spec.Network[i].IPv4 = old.Spec.Network[i].IPv4
			}
			if spec.Network[i].IPv6 == "auto" && old.Spec.Network[i].IPv6 != "" {
				spec.Network[i].IPv6 = old.Spec.Network[i].IPv6
			}
		}
	}
	if err := s.assignOwn(p.Meta.Name, key, &spec); err != nil {
		return api.Jail{}, err
	}
	mb := old.Metadata.ManagedBy
	if in.Metadata.ManagedBy != "" {
		mb = in.Metadata.ManagedBy
	}
	if jsonEq(spec, old.Spec) && labelsEqual(in.Metadata.Labels, old.Metadata.Labels) && mb == old.Metadata.ManagedBy {
		return s.getByKey(ctx, key)
	}
	if !labelsEqual(in.Metadata.Labels, old.Metadata.Labels) {
		if err := api.ValidLabels(in.Metadata.Labels); err != nil {
			return api.Jail{}, err
		}
	}
	if err := s.applyDiskLimits(ctx, key, old.Spec, spec); err != nil {
		return api.Jail{}, err
	}
	if spec.Delegate != old.Spec.Delegate {
		// the dataset is made and given the jailed property (or the property cleared) in the request of the global
		// administrator who changed it; the jail attaches it at its next start
		if err := s.life.SetDelegation(ctx, key, spec.Delegate); err != nil {
			return api.Jail{}, err
		}
	}
	nj := old
	nj.Spec, nj.Status = spec, api.JailStatus{}
	nj.Metadata.Labels, nj.Metadata.ManagedBy = in.Metadata.Labels, mb
	nj.Metadata.Generation = old.Metadata.Generation + 1
	nj.Metadata.UpdatedAt = s.now().UTC()
	// A running jail gets its volumes mounted or unmounted now. The record and the rendered jail.conf and fstab come
	// first, so the helper's checks of the fstab (plain mount points, nothing below a volume) refuse a bad attachment
	// before anything is mounted; a mount or a detach that is refused puts the record and the files back as they were.
	liveVolumes := false
	if !volumesEqual(spec.Volumes, old.Spec.Volumes) {
		if cur, err := s.getByKey(ctx, key); err == nil && cur.Status.Power == api.PowerRunning {
			liveVolumes = true
		}
	}
	setMemory := func(j api.Jail) {
		s.mu.Lock()
		if e := s.jails[key]; e != nil {
			j.Status.ImageGeneration = e.jail.Status.ImageGeneration
			j.Status.FreeBSDVersion = e.jail.Status.FreeBSDVersion
			e.jail = j
		}
		s.mu.Unlock()
	}
	if err := s.writeJailRecord(ctx, nj); err != nil {
		return api.Jail{}, internal(err)
	}
	if liveVolumes {
		restore := func() {
			if err := s.writeJailRecord(ctx, old); err != nil {
				s.log.Warn("put the jail record back after a refused live volume change", "jail", key, "err", err)
			}
			setMemory(old)
			if err := s.renderConf(ctx, key); err != nil {
				s.log.Warn("render the old jail.conf after a refused live volume change", "jail", key, "err", err)
			}
		}
		setMemory(nj)
		if err := s.renderConf(ctx, key); err != nil {
			restore()
			return api.Jail{}, err
		}
		if err := s.applyLiveVolumes(ctx, p.Meta.Name, key, name, old.Spec.Volumes, spec.Volumes); err != nil {
			restore()
			return api.Jail{}, err
		}
	}
	setMemory(nj)
	s.promptChanged(ctx, key, old, nj)
	s.emitIn(ctx, p.Meta.Name, "jail-updated", "/api/v1/jails/"+name)
	if !onlyText(old, nj) {
		s.kick()
	}
	return s.getByKey(ctx, key)
}

// onlyText reports an update that changed nothing but the comment and the notes of a jail: they are not rendered into
// jail.conf, fstab or the firewall, so the reconcile loop has nothing to do for it.
func onlyText(old, nj api.Jail) bool {
	a, b := old.Spec, nj.Spec
	a.Comment, a.Notes, b.Comment, b.Notes = "", "", "", ""
	return jsonEq(a, b) && labelsEqual(old.Metadata.Labels, nj.Metadata.Labels) && old.Metadata.ManagedBy == nj.Metadata.ManagedBy
}

func volumesEqual(a, b []api.VolumeAttach) bool {
	return jsonEq(a, b) || (len(a) == 0 && len(b) == 0)
}

func jsonEq(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// PatchJail applies a merge patch to metadata labels and spec.
func (s *Service) PatchJail(ctx context.Context, name string, patch map[string]any, expect int64) (api.Jail, error) {
	cur, err := s.GetJail(ctx, name)
	if err != nil {
		return api.Jail{}, err
	}
	if err := checkIncarnation(ctx, "jail", name, cur.Metadata.UUID); err != nil {
		return api.Jail{}, err
	}
	if expect != AnyGeneration && expect != cur.Metadata.Generation {
		return api.Jail{}, api.Errorf(api.CodePreconditionFailed, "jail %q changed: current generation is %d", name, cur.Metadata.Generation)
	}
	var out api.Jail
	if err := MergePatch(cur, patch, &out); err != nil {
		return api.Jail{}, err
	}
	return s.UpdateJail(ctx, name, out, cur.Metadata.Generation)
}

// ---- delete and power ----

// DeleteJail destroys a stopped jail as an operation. expect is the If-Match generation or AnyGeneration; a caller that
// names the UUID it read (WithExpectedUUID) deletes only that incarnation.
func (s *Service) DeleteJail(ctx context.Context, name string, expect int64) (api.Operation, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	j, err := s.getByKey(ctx, key)
	if err != nil {
		return api.Operation{}, err
	}
	if err := checkIncarnation(ctx, "jail", name, j.Metadata.UUID); err != nil {
		return api.Operation{}, err
	}
	if expect != AnyGeneration && expect != j.Metadata.Generation {
		return api.Operation{}, api.Errorf(api.CodePreconditionFailed, "jail %q changed: current generation is %d", name, j.Metadata.Generation).
			WithHint("read it again before you delete it").WithDetail("generation", strconv.FormatInt(j.Metadata.Generation, 10))
	}
	if j.Status.Power == api.PowerRunning {
		return api.Operation{}, api.Errorf(api.CodeBusy, "jail %q is running", name).WithHint("stop it first")
	}
	if err := s.noRuleNamesJail(p.Meta.Name, name, true); err != nil {
		return api.Operation{}, err
	}
	if err := s.backupRunning("jail", name, j.Metadata.UUID); err != nil {
		return api.Operation{}, err
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpJailDelete, Description: "Delete jail " + name, Locks: []string{jailLock(key), anchorLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			_ = h.Bind("destroy/"+key, &opRequest{Kind: "destroy", Jail: key})
			if _, err := s.life.DestroyJail(ctx, key, false, false); err != nil {
				return nil, err
			}
			s.mu.Lock()
			delete(s.jails, key)
			s.forgetProvisioned(key)
			delete(s.status, key)
			s.mu.Unlock()
			s.forgetKeys(key)
			s.emitIn(ctx, p.Meta.Name, "jail-deleted", "/api/v1/jails/"+name)
			s.accessJailGone(ctx, p.Meta.Name, name)
			s.kick()
			return nil, nil
		},
	})
}

// JailAction starts, stops or restarts a jail as an operation.
func (s *Service) JailAction(ctx context.Context, name string, a api.JailAction) (api.Operation, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	if _, err := s.getByKey(ctx, key); err != nil {
		return api.Operation{}, err
	}
	switch a.Action {
	case "start", "stop", "restart":
	default:
		return api.Operation{}, invalid("unknown action %q (start, stop or restart)", a.Action)
	}
	if a.TimeoutSeconds < 0 {
		return api.Operation{}, invalid("timeout must not be negative")
	}
	if a.Action != "stop" {
		// refused before anything runs: a restart must not stop a jail that cannot start again
		if err := s.refuseUnsupported(key); err != nil {
			return api.Operation{}, err
		}
	}
	if l := s.lockedOf(key); a.Action != "stop" && len(l) > 0 {
		return api.Operation{}, lifecycle.LockedError(key, l)
	}
	done := map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}[a.Action]
	return s.Submit(ctx, OpSpec{
		Action:      map[string]api.OperationAction{"start": api.OpJailStart, "stop": api.OpJailStop, "restart": api.OpJailRestart}[a.Action],
		Description: strings.ToUpper(a.Action[:1]) + a.Action[1:] + " jail " + name, Locks: []string{jailLock(key), anchorLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			if a.TimeoutSeconds > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(a.TimeoutSeconds)*time.Second)
				defer cancel()
			}
			if a.Action != "start" {
				_ = h.Bind("stop/"+key, &opRequest{Kind: "stop", Jail: key})
				if err := s.life.StopJail(ctx, key); err != nil {
					return nil, err
				}
			}
			if a.Action != "stop" {
				_ = h.Bind("start/"+key, &opRequest{Kind: "start", Jail: key})
				if err := s.life.StartJail(ctx, key); err != nil {
					if api.IsCode(err, api.CodeLocked) {
						s.observeLocked(ctx, key)
					}
					return nil, err
				}
				s.setLocked(key, nil)
				s.markStartDue(key, false)
				// A new jail gets its prompt at its first start, before its own provisioning (nothing is installed).
				_ = h.Timed("shell-setup", func() error { s.shellAtStart(ctx, key); return nil })
				// A jail that declares provisioning is provisioned at its first start.
				if err := h.Timed("provisioning", func() error { return s.provisionPending(ctx, key) }); err != nil {
					s.kick()
					return nil, err
				}
			}
			s.emitIn(ctx, p.Meta.Name, "jail-"+done, "/api/v1/jails/"+name)
			s.kick()
			return s.getByKey(ctx, key)
		},
	})
}

// StartJail starts a jail synchronously the way an operator's start does. The
// reconciler uses it for autostart; key is the project-qualified jail key.
func (s *Service) StartJail(ctx context.Context, key string) error {
	unlock, err := s.locks.lockPatient(ctx, "autostart "+key, jailLock(key), anchorLock(key))
	if err != nil {
		return err
	}
	defer unlock()
	project, name := validate.SplitQualified(key)
	err = s.life.StartJail(ctx, key)
	if api.IsCode(err, api.CodeLocked) {
		// Skipped, not failed: the unlock starts it (volkeys.go).
		s.observeLocked(ctx, key)
		s.bootLocked(ctx, key, err)
		return err
	}
	if err == nil {
		s.setLocked(key, nil)
		s.shellAtStart(ctx, key)
	}
	res := "ok"
	if err != nil {
		res = "error"
	}
	s.Audit(ctx, store.AuditEntry{Actor: ActorFrom(ctx).Name, ActorKind: ActorFrom(ctx).Kind, Method: "AUTOSTART", Route: "reconcile",
		Object: "/api/v1/jails/" + name, Project: project, Result: res, Detail: "start at boot"})
	if err != nil {
		return err
	}
	s.emitIn(ctx, project, "jail-started", "/api/v1/jails/"+name)
	return nil
}

// Housekeeping does the periodic chores: it destroys root.old datasets whose
// retention has passed. Jails with a running operation are skipped.
func (s *Service) Housekeeping(ctx context.Context) {
	if s.life == nil {
		return
	}
	s.mu.RLock()
	names := sortedKeys(s.jails)
	s.mu.RUnlock()
	for _, n := range names {
		unlock, err := s.locks.tryLock("housekeeping "+n, jailLock(n))
		if err != nil {
			continue
		}
		if done, err := s.life.CleanupRootOld(ctx, n); err != nil {
			s.log.Warn("removing expired upgrade snapshots or a root.old failed", "jail", n, "err", err)
		} else if done {
			s.log.Info("removed expired upgrade snapshots or a root.old", "jail", n)
		}
		unlock()
	}
	s.PruneTrust(ctx)
}

// TryLock and IsLocked let the reconciler share the lock set.
func (s *Service) TryLock(holder string, keys ...string) (func(), error) {
	return s.locks.tryLock(holder, keys...)
}

// IsLocked reports whether a lock key is held.
func (s *Service) IsLocked(key string) bool { return s.locks.isHeld(key) }

// ---- snapshots ----

func (s *Service) snapToAPI(ctx context.Context, project, key string, r lifecycle.SnapshotResult) api.JailSnapshot {
	_, jail := validate.SplitQualified(key)
	sn := api.JailSnapshot{
		Metadata: api.ObjectMeta{Name: r.Name, Project: project, Scope: api.ScopeHost, Host: s.d.HostUUID, Generation: 1},
		Spec:     api.JailSnapshotSpec{Jail: jail},
		Status:   api.JailSnapshotStatus{Written: r.TotalWritten},
	}
	sn.Metadata.CreatedAt = r.Created
	if c, err := s.d.ZFS.Get(ctx, s.lcfg.ContainerDS(key)+"@"+r.Name, lifecycle.PropComment); err == nil {
		sn.Spec.Comment = c
	}
	return sn
}

// ListSnapshots lists the snapshots of a jail.
func (s *Service) ListSnapshots(ctx context.Context, jail string) ([]api.JailSnapshot, error) {
	p, key, err := s.jailRef(ctx, jail)
	if err != nil {
		return nil, err
	}
	if _, err := s.getByKey(ctx, key); err != nil {
		return nil, err
	}
	rs, err := s.life.ListSnapshots(ctx, key)
	if err != nil {
		return nil, err
	}
	out := []api.JailSnapshot{}
	for _, r := range rs {
		out = append(out, s.snapToAPI(ctx, p.Meta.Name, key, r))
	}
	return out, nil
}

// GetSnapshot returns one snapshot.
func (s *Service) GetSnapshot(ctx context.Context, jail, snap string) (api.JailSnapshot, error) {
	l, err := s.ListSnapshots(ctx, jail)
	if err != nil {
		return api.JailSnapshot{}, err
	}
	for _, x := range l {
		if x.Metadata.Name == snap {
			return x, nil
		}
	}
	return api.JailSnapshot{}, notFound("snapshot", snap)
}

// CreateSnapshot snapshots every dataset of a jail atomically, as an operation.
func (s *Service) CreateSnapshot(ctx context.Context, jail string, req api.JailSnapshot) (api.Operation, error) {
	if err := s.requireHost("snapshots"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, jail)
	if err != nil {
		return api.Operation{}, err
	}
	if _, err := s.getByKey(ctx, key); err != nil {
		return api.Operation{}, err
	}
	if err := api.ValidFreeText("comment", req.Spec.Comment, false); err != nil {
		return api.Operation{}, err
	}
	name := req.Metadata.Name
	if name != "" {
		if err := validate.SnapshotName("name", name); err != nil {
			return api.Operation{}, err
		}
		if err := s.checkSnapshotBudget(key, name); err != nil {
			return api.Operation{}, err
		}
		if _, err := s.GetSnapshot(ctx, jail, name); err == nil {
			return api.Operation{}, api.Errorf(api.CodeAlreadyExists, "snapshot %q already exists", name)
		}
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpJailSnapshot, Description: "Snapshot jail " + jail, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + jail}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			r, err := s.life.SnapshotJail(ctx, key, name, req.Spec.Comment)
			if err != nil {
				return nil, err
			}
			s.emitIn(ctx, p.Meta.Name, "jail-snapshot-created", "/api/v1/jails/"+jail+"/snapshots/"+r.Name)
			return s.snapToAPI(ctx, p.Meta.Name, key, r), nil
		},
	})
}

// DeleteSnapshot destroys a snapshot on every dataset of the jail.
func (s *Service) DeleteSnapshot(ctx context.Context, jail, snap string) (api.Operation, error) {
	if err := s.requireHost("snapshots"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, jail)
	if err != nil {
		return api.Operation{}, err
	}
	if _, err := s.GetSnapshot(ctx, jail, snap); err != nil {
		return api.Operation{}, err
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpJailSnapshotDelete, Description: "Delete snapshot " + snap, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + jail}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			container := s.lcfg.ContainerDS(key)
			ds, err := s.d.ZFS.Descendants(ctx, container)
			if err != nil {
				return nil, err
			}
			for _, d := range append([]string{container}, ds...) {
				ok, err := s.d.ZFS.Exists(ctx, d+"@"+snap)
				if err != nil {
					return nil, err
				}
				if ok {
					if err := s.d.ZFS.Destroy(ctx, d+"@"+snap); err != nil {
						return nil, err
					}
				}
			}
			s.emitIn(ctx, p.Meta.Name, "jail-snapshot-deleted", "/api/v1/jails/"+jail+"/snapshots/"+snap)
			return nil, nil
		},
	})
}

// ---- rollback, upgrade, copy ----

// ActionResult is an operation result that is a Jail plus what a plan did.
type ActionResult struct {
	api.Jail
	Plan     []string `json:"plan,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	DryRun   bool     `json:"dry_run,omitempty"`
	// Upgrade says what an upgrade moves, for a dry run and for the result of the operation.
	Upgrade *UpgradeInfo `json:"upgrade,omitempty"`
}

// UpgradeInfo is what an in-place upgrade changes: what is installed before and after, the releases, and what it keeps.
type UpgradeInfo struct {
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
	FromRelease string   `json:"from_release,omitempty"`
	ToRelease   string   `json:"to_release,omitempty"`
	Snapshot    string   `json:"snapshot,omitempty"`
	Kept        []string `json:"kept,omitempty"`
	// Nothing says the jail has nothing newer (nothing was stopped, snapshotted or written); Installed is the
	// release installed in it, BasePending and PortsPending the packages that were newer.
	Nothing      bool     `json:"nothing_to_upgrade,omitempty"`
	Installed    string   `json:"installed,omitempty"`
	BasePending  []string `json:"base_packages,omitempty"`
	PortsPending []string `json:"ports_packages,omitempty"`
	// BaseChanges are the base packages that change ("name old -> new"), Checks the pre-flight checks that passed and
	// HealthCheck the command that decides whether the upgraded jail is good.
	BaseChanges []string `json:"base_changes,omitempty"`
	Checks      []string `json:"checks,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
	// Failed are the pre-flight checks that failed (a dry run lists them all), Download and DiskDelta the bytes pkg said
	// it fetches (0 when it did not say) and the growth of the root, Pkgsave the *.pkgsave files the upgrade left
	// (information).
	Failed    []string `json:"failed_checks,omitempty"`
	Download  int64    `json:"download_bytes,omitempty"`
	DiskDelta int64    `json:"disk_delta_bytes,omitempty"`
	Pkgsave   []string `json:"pkgsave,omitempty"`
	// RestartPending are the processes that still run files the upgrade replaced; nothing restarts them.
	RestartPending []string `json:"restart_pending,omitempty"`
}

func upgradeInfo(res *lifecycle.UpgradeResult) *UpgradeInfo {
	if res != nil && res.Nothing {
		return &UpgradeInfo{Nothing: true, Installed: res.Installed, FromRelease: res.FromRelease}
	}
	if res == nil || !res.Changed {
		return nil
	}
	u := &UpgradeInfo{From: res.From, To: res.To, FromRelease: res.FromRelease, ToRelease: res.ToRelease, Snapshot: res.Snapshot, Kept: res.Kept,
		Installed: res.Installed, BasePending: res.BasePending, PortsPending: res.PortsPending, BaseChanges: res.BaseChanges, Checks: res.Checks, HealthCheck: res.HealthCheck, Failed: res.Failed, Download: res.Download, DiskDelta: res.DiskDelta, Pkgsave: res.Pkgsave, RestartPending: res.RestartPending}
	return u
}

func (s *Service) stamp() string { return strconv.FormatInt(s.now().Unix(), 10) }

// RollbackJail rolls a jail back as an operation.
func (s *Service) RollbackJail(ctx context.Context, name string, rb api.JailRollback) (api.Operation, error) {
	if err := s.requireHost("rollback"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	if _, err := s.getByKey(ctx, key); err != nil {
		return api.Operation{}, err
	}
	switch rb.Scope {
	case "":
		rb.Scope = api.RollbackRoot
	case api.RollbackRoot, api.RollbackData, api.RollbackAll:
	default:
		return api.Operation{}, invalid("unknown rollback scope %q", rb.Scope)
	}
	if rb.Scope != api.RollbackRoot && !rb.ConfirmDataLoss {
		return api.Operation{}, api.Errorf(api.CodeDataLoss, "scope %q discards data written since the snapshot", rb.Scope).
			WithHint("check what was written, then set confirm_data_loss")
	}
	opts := lifecycle.RollbackOptions{JailRollback: rb, Stamp: s.stamp()}
	return s.Submit(ctx, OpSpec{
		Action: api.OpJailRollback, Description: "Rollback jail " + name, Locks: []string{jailLock(key), anchorLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			var ar ActionResult
			started := s.now().UTC()
			if opts.Scope == api.RollbackRoot {
				pl, ur, err := s.life.PlanRollbackRoot(ctx, key, opts)
				if err != nil {
					return nil, err
				}
				if pl != nil {
					st := ur.State
					if err := h.Bind("rollback/"+key, &opRequest{Kind: "rollback", Jail: key, Stamp: opts.Stamp, Rollback: &opts, State: &st}); err != nil {
						return nil, err
					}
					if err := s.life.Executor.Run(ctx, "rollback/"+key, pl); err != nil {
						return nil, err
					}
					s.life.FinishSwitch(ctx, key, ur)
					ar.Plan, ar.Warnings = ur.Plan, ur.Warnings
					now := s.now().UTC()
					s.appendHistory(api.UpdateRecord{ID: "upd-" + s.stamp() + "-" + randHex(3), Jail: name, Project: p.Meta.Name, Kind: api.UpdateKindOS,
						StartedAt: started, FinishedAt: now, Snapshot: ur.Snapshot, From: ur.From, To: ur.To, Result: api.UpdateSuccess,
						Note: "rolled back the in-place upgrade to the state of snapshot " + ur.Snapshot})
				}
			} else {
				_ = h.Bind("rollback/"+key, &opRequest{Kind: "rollback", Jail: key, Stamp: opts.Stamp, Rollback: &opts})
				res, err := s.life.RollbackJail(ctx, key, opts, false)
				if err != nil {
					return nil, err
				}
				ar.Plan = res.Plan
			}
			if err := s.refreshJail(ctx, key); err != nil {
				s.log.Warn("refresh after rollback", "jail", key, "err", err)
			}
			s.emitIn(ctx, p.Meta.Name, "jail-rolled-back", "/api/v1/jails/"+name)
			ar.Jail, _ = s.getByKey(ctx, key)
			return ar, nil
		},
	})
}

// requireUpgradeRights: the in-place upgrade of a FreeBSD jail runs pkg on the host as root against the jail's own root
// (package scripts and all), and that root belongs to whoever holds the jail: a project administrator could plant what
// the next upgrade runs as root of the host. Only a global administrator, and the system itself (the nightly upgrade,
// which a global administrator turned on), may do it.
func (s *Service) requireUpgradeRights(ctx context.Context, j api.Jail) error {
	if j.Spec.OS == api.OSLinux {
		return nil
	}
	if !isGlobalAdmin(ActorFrom(ctx)) {
		return api.Errorf(api.CodeForbidden, "upgrading a jail in place (pkg runs as root of the host against the jail's own root) needs a global administrator")
	}
	return nil
}

// UpgradeJail upgrades a jail in place as an operation: its FreeBSD base from the host and its ports packages inside
// it. With DryRun the operation returns the plan and changes nothing.
func (s *Service) UpgradeJail(ctx context.Context, name string, up api.JailUpgrade) (api.Operation, error) {
	if err := s.requireHost("upgrade"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	cur0, err := s.getByKey(ctx, key)
	if err != nil {
		return api.Operation{}, err
	}
	if err := s.requireUpgradeRights(ctx, cur0); err != nil {
		return api.Operation{}, err
	}
	req := lifecycle.UpgradeRequest{JailUpgrade: up, Stamp: s.stamp()}
	return s.Submit(ctx, OpSpec{
		Action: api.OpJailUpgrade, Description: "Upgrade jail " + name, Locks: []string{jailLock(key), anchorLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			// the upgrade (and the addition of base packages) is written to the update history
			out, _, _, err := s.upgradeRecorded(ctx, h, p.Meta.Name, name, key, up, req)
			return out, err
		},
	})
}

// BaseList lists the base packages (FreeBSD-*) installed in a FreeBSD jail, running or stopped, from a copy of its
// package database: pkg on the host never reads the jail's own files (lifecycle.BasePackages).
func (s *Service) BaseList(ctx context.Context, name string) (api.JailBase, error) {
	_, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.JailBase{}, err
	}
	if _, err := s.getByKey(ctx, key); err != nil {
		return api.JailBase{}, err
	}
	// a stopped jail's root is mounted for the read: no operation may start or change the jail meanwhile
	unlock, err := s.locks.lockPatient(ctx, "base list "+key, jailLock(key))
	if err != nil {
		return api.JailBase{}, err
	}
	defer unlock()
	pk, err := s.life.BasePackages(ctx, key)
	if err != nil {
		return api.JailBase{}, err
	}
	res := api.JailBase{Jail: name, Packages: []api.JailBasePackage{}}
	for _, p := range pk {
		res.Packages = append(res.Packages, api.JailBasePackage{Name: p[0], Version: p[1]})
	}
	return res, nil
}

// upgradeRecorded runs an upgrade and writes it to the update history of the jail as an OS update. It returns the
// result, what the plan was for and the id of the history record (empty for a dry run and for an upgrade that did
// nothing).
func (s *Service) upgradeRecorded(ctx context.Context, h *OpHandle, project, name, key string, up api.JailUpgrade, req lifecycle.UpgradeRequest) (any, *lifecycle.UpgradeResult, string, error) {
	started := s.now().UTC()
	out, res, err := s.upgradeRun(ctx, h, project, name, key, up, req)
	var se *lifecycle.SuspectedError
	if errors.As(err, &se) && !up.DryRun {
		s.noteSuspected(ctx, project, name, key, se, started)
		return out, res, "", err
	}
	if up.DryRun || res == nil || !res.Changed {
		return out, res, "", err
	}
	rec := api.UpdateRecord{ID: "upd-" + s.stamp() + "-" + randHex(3), Jail: name, Project: project, Kind: api.UpdateKindOS, StartedAt: started,
		FinishedAt: s.now().UTC(), Snapshot: res.Snapshot, From: res.From, To: res.To, Result: api.UpdateSuccess}
	if err != nil {
		rec.Error = err.Error()
		rec.Result, rec.Note = upgradeOutcome(err, res.From)
	} else if len(up.AddPackages) > 0 {
		rec.Note = "added the base packages " + strings.Join(up.AddPackages, ", ")
	}
	if len(res.RestartPending) > 0 {
		// information: these processes use the new files once someone restarts them; JailOps never does
		rec.Note = strings.TrimPrefix(rec.Note+"; restart pending: "+strings.Join(res.RestartPending, ", "), "; ")
	}
	s.appendHistory(rec)
	if rec.Result == api.UpdateSuccess {
		s.emitIn(ctx, project, "update.applied", "/api/v1/jails/"+name+"/updates")
	} else {
		s.emitIn(ctx, project, "update.failed", "/api/v1/jails/"+name+"/updates")
	}
	return out, res, rec.ID, err
}

// upgradeRun plans, journals and runs an in-place upgrade, and returns what the plan was for, also when the run fails.
// The caller holds the jail and anchor locks.
func (s *Service) upgradeRun(ctx context.Context, h *OpHandle, project, name, key string, up api.JailUpgrade, req lifecycle.UpgradeRequest) (any, *lifecycle.UpgradeResult, error) {
	if cur, err := s.getByKey(ctx, key); err == nil {
		if err := s.requireUpgradeRights(ctx, cur); err != nil {
			return nil, nil, err
		}
	}
	var portsWarn string
	if cur, err := s.getByKey(ctx, key); err == nil && s.upgradesInPlace(cur) {
		if !up.DryRun {
			// one operation changes the packages of the jail at a time, for the whole upgrade: no other package
			// operation or session starts until the upgrade ends
			release, err := s.holdJailFor(key, "an in-place upgrade")
			if err != nil {
				return nil, nil, err
			}
			defer release()
		}
		if !up.NoPackages && len(up.AddPackages) == 0 {
			req.Ports, portsWarn = s.thickPorts(ctx, key, cur, up.DryRun)
		}
	}
	pl, res, err := s.life.PlanUpgrade(ctx, key, req)
	if err != nil {
		return nil, res, err
	}
	// the check of the root passed: a finding of an earlier run no longer holds (the lifecycle cleared it)
	if sus := s.life.Suspected(ctx, key); sus == "" {
		s.mu.Lock()
		if e := s.jails[key]; e != nil {
			e.suspected = ""
		}
		s.mu.Unlock()
	}
	if portsWarn != "" && res != nil {
		res.Warnings = append(res.Warnings, portsWarn)
	}
	j, _ := s.getByKey(ctx, key)
	if pl == nil || up.DryRun {
		ar := ActionResult{Jail: j, Warnings: res.Warnings, DryRun: up.DryRun, Upgrade: upgradeInfo(res)}
		if pl != nil {
			ar.Plan = pl.Describe()
		}
		return ar, res, nil
	}
	st := res.State
	if err := h.Bind("upgrade/"+key, &opRequest{Kind: "upgrade", Jail: key, Stamp: req.Stamp, Upgrade: &req, State: &st}); err != nil {
		return nil, res, err
	}
	if err := s.life.Executor.Run(ctx, "upgrade/"+key, pl); err != nil {
		if i := strings.Index(err.Error(), "suspected_compromised: "); i >= 0 {
			// pkg's trees changed between the check and a later pkg step: the jail is suspected as at the check
			return nil, res, &lifecycle.SuspectedError{Jail: name, Found: strings.TrimPrefix(err.Error()[i:], "suspected_compromised: "), During: true}
		}
		if note := s.life.NoteUpgradeFailure(ctx, key, err); note != "" {
			if rerr := s.refreshJail(ctx, key); rerr != nil {
				s.log.Warn("refresh after a failed rollback", "jail", key, "err", rerr)
			}
		}
		return nil, res, thickHealthFailure(err, res)
	}
	s.life.FinishSwitch(ctx, key, res)
	if err := s.refreshJail(ctx, key); err != nil {
		s.log.Warn("refresh after upgrade", "jail", key, "err", err)
	}
	s.emitIn(ctx, project, "jail-upgraded", "/api/v1/jails/"+name)
	s.kick()
	j, _ = s.getByKey(ctx, key)
	return ActionResult{Jail: j, Plan: res.Plan, Warnings: res.Warnings, Upgrade: upgradeInfo(res)}, res, nil
}

// upgradesInPlace says whether a jail is a FreeBSD jail: the one an in-place upgrade applies to.
func (s *Service) upgradesInPlace(j api.Jail) bool {
	return !s.jailIsLinux(j)
}

// thickPorts finds out, inside the running jail and without changing anything but the package catalogue, which ports
// packages have a newer version, and gives the plan the way to upgrade them (base packages are excluded by name, as in
// every package update). A jail without ports packages fetches no catalogue. A dry run changes nothing at all: it
// reads the catalogue the jail has. A stopped jail has no ports part: pkg -j runs inside it. A catalogue that cannot be read leaves the ports alone and says so. The caller
// holds the jail (upgradeRun).
func (s *Service) thickPorts(ctx context.Context, key string, j api.Jail, dryRun bool) (*lifecycle.PortsUpgrade, string) {
	if !s.jailRunning(ctx, key) || s.jailIsLinux(j) {
		return nil, ""
	}
	// a jail without ports packages has no ports part: the ports catalogue (tens of MiB, written into the root) is
	// not fetched, and the upgrade says so
	if names, _, err := s.portsPackages(ctx, key); err != nil {
		return nil, "the ports packages were not checked: " + tailText(err.Error(), 200)
	} else if len(names) == 0 {
		return &lifecycle.PortsUpgrade{None: true}, ""
	}
	if !dryRun {
		if _, err := s.pkgExec(ctx, key, 10*time.Minute, "pkg", "update", "-q"); err != nil {
			return nil, "the ports packages were not checked: " + tailText(err.Error(), 200)
		}
	}
	out, err := s.pkgUpgradeCheck(ctx, key, dryRun)
	if err != nil {
		return nil, "the ports packages were not checked: " + tailText(err.Error(), 200)
	}
	var names []string
	for _, p := range parsePackageUpdates(out) {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return nil, ""
	}
	return &lifecycle.PortsUpgrade{Pending: names, Run: func(ctx context.Context) error {
		if _, err := s.pkgUpgrade(ctx, key, 60*time.Minute, true); err != nil {
			return fmt.Errorf("pkg upgrade of the ports packages: %w", err)
		}
		return nil
	}}, ""
}

// thickHealthFailure says what happened when the health check of an in-place upgrade failed. The plan's
// error is not an api.Error, so the operation record would show "internal error" only; the health check's command and
// the tail of its output are written for the reader (like the app health errors), so they are kept. Any other error,
// goes through unchanged.
func thickHealthFailure(err error, res *lifecycle.UpgradeResult) error {
	if res == nil {
		return err
	}
	var pe *plan.Error
	isPlan := errors.As(err, &pe)
	// nothing is rolled back on its own and the jail is never stopped: it runs as the upgrade left it, and the snapshot
	// taken before the first change is there for a person
	end := "the jail runs as the upgrade left it (running the upgrade again completes it); snapshot " + res.Snapshot + " is kept: `jailops jail rollback <jail> --confirm-data-loss` returns the root to it, and what the jail wrote since is lost"
	if isPlan && (pe.Step == "prefetch" || pe.Step == "check-scripts" || pe.Step == "snapshot") {
		end = "nothing in the jail was changed (the failure came before the snapshot)"
	}
	var he *lifecycle.HealthError
	if errors.As(err, &he) {
		what := he.Msg
		if he.Command != "" {
			what = he.Command
			if he.Err != nil {
				what += ": " + he.Err.Error()
			}
			if o := tailText(he.Output, 400); o != "" {
				what += ": " + o
			}
		}
		return publicf(err, "the health check failed: %s; %s", what, end)
	}
	// another failure of a plan step (pkg, no room, a start that fails): typed errors keep their message, any other is
	// shown with the step and the tail of what it said
	var ae *api.Error
	if !isPlan {
		return err
	}
	if errors.As(err, &ae) {
		out := *ae
		out.Message += "; " + end
		// the plan's error stays in the chain: the history tells a rollback from a refusal by it (thickOutcome)
		return &publicError{ae: &out, cause: err}
	}
	return publicf(err, "the upgrade failed at step %s: %s; %s", pe.Step, tailText(pe.Err.Error(), 500), end)
}

// upgradeOutcome classifies the failure of an in-place upgrade for the update history: a failure before the snapshot
// changed nothing; any later failure is left as it is (nothing rolls back on its own; the jail runs on, and the snapshot
// is kept for a manual rollback).
func upgradeOutcome(err error, from string) (result api.UpdateResult, note string) {
	var pe *plan.Error
	var se *lifecycle.SuspectedError
	switch {
	case errors.As(err, &se) && se.During:
		return api.UpdateFailure, "suspected_compromised: " + se.Found + "; found during the upgrade, which stopped there: look at the jail before anything else; the snapshot of the upgrade is kept"
	case errors.As(err, &se):
		return api.UpdatePreflightFailed, "suspected_compromised: " + se.Found + "; the jail was not upgraded and nothing in it was changed: look at it before anything else"
	case errors.As(err, &pe) && (pe.Step == "prefetch" || pe.Step == "check-scripts" || pe.Step == "snapshot"):
		return api.UpdatePreflightFailed, "nothing in the jail was changed: the upgrade failed before its snapshot (" + from + " is installed)"
	case errors.As(err, &pe):
		return api.UpdateFailure, "the jail runs as the upgrade left it; run the upgrade again to complete it, or roll the root back to the snapshot (jailops jail rollback, which loses what the jail wrote since)"
	}
	return api.UpdatePreflightFailed, "nothing was changed: the upgrade stopped in its checks (" + from + " is installed)"
}

// tailText is the last n bytes of s (on a line start when there is one), marked when cut.
func tailText(s string, n int) string {
	s = strings.TrimSpace(imagegen.CleanOutput(s))
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return "..." + s
}

// CopyJail makes an independent copy with the data of the source (send and receive, never a clone) in the same
// project and on the same storage. The source may be running: the copy is made from one atomic snapshot of its
// datasets, or from req.Snapshot, a snapshot of the source that exists.
func (s *Service) CopyJail(ctx context.Context, src string, req api.JailCopy) (api.Operation, error) {
	dst := api.Jail{Metadata: api.ObjectMeta{Name: req.Name, Labels: req.Labels}}
	if err := s.requireHost("copy"); err != nil {
		return api.Operation{}, err
	}
	p, srcKey, err := s.jailRef(ctx, src)
	if err != nil {
		return api.Operation{}, err
	}
	if err := api.ValidLabels(dst.Metadata.Labels); err != nil {
		return api.Operation{}, err
	}
	j, err := s.jailSnapshot(srcKey)
	if err != nil {
		return api.Operation{}, notFound("jail", src)
	}
	name := dst.Metadata.Name
	if err := validate.JailName(name); err != nil {
		return api.Operation{}, err
	}
	key, err := validate.QualifiedName(p.Meta.Name, name)
	if err != nil {
		return api.Operation{}, err
	}
	s.mu.RLock()
	_, exists := s.jails[key]
	s.mu.RUnlock()
	if exists {
		return api.Operation{}, api.Errorf(api.CodeAlreadyExists, "jail %q already exists", name)
	}
	cspec := j.Spec
	// A copy attaches no volume: a volume belongs to the jails it is attached to, and two jails writing one by
	// accident is not what a copy is for.
	cspec.Volumes = nil
	cspec.Expose = nil
	dropLinuxUpdatePolicy(&cspec)
	cspec.Network = append([]api.NetworkAttach(nil), cspec.Network...)
	for i := range cspec.Network {
		if cspec.Network[i].IPv4 != "none" && cspec.Network[i].IPv4 != "" {
			cspec.Network[i].IPv4 = "auto"
		}
		if cspec.Network[i].IPv6 != "none" && cspec.Network[i].IPv6 != "" {
			cspec.Network[i].IPv6 = "auto"
		}
	}
	release, err := s.admit(ctx, p, key, &cspec, nil)
	if err != nil {
		return api.Operation{}, err
	}
	if err := s.checkNewJail(p.Meta.Name, name, cspec); err != nil {
		release()
		return api.Operation{}, err
	}
	submitted := false
	defer func() {
		if !submitted {
			release()
		}
	}()
	newID := state_uuid()
	stamp := s.stamp()
	locks := append([]string{jailLock(srcKey), jailLock(key)}, s.netLocksEff(p.Meta.Name, j.Spec)...)
	op, err := s.Submit(ctx, OpSpec{
		Action: api.OpJailCopy, Description: "Copy jail " + src + " to " + name, Locks: locks, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + src, "/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			defer release()
			s.place(key, j.Spec.Storage)
			ok := false
			defer func() {
				if !ok {
					s.unplace(key)
				}
			}()
			_ = h.Bind("copy/"+key, &opRequest{Kind: "copy", Jail: key, Stamp: stamp, Copy: &copyRequest{Src: srcKey, Dst: key, UUID: newID, Snapshot: req.Snapshot}})
			if _, err := s.life.CopyJailFrom(ctx, srcKey, key, newID, stamp, req.Snapshot, false); err != nil {
				return nil, err
			}
			if err := s.refreshJail(ctx, key); err != nil {
				return nil, err
			}
			// The copy's addresses were reset to "auto": allocate them now.
			nj, _ := s.jailSnapshot(key)
			if err := s.assignOwn(p.Meta.Name, key, &nj.Spec); err != nil {
				return nil, fmt.Errorf("copy created, but allocating addresses failed: %w", err)
			}
			if len(dst.Metadata.Labels) > 0 {
				nj.Metadata.Labels = dst.Metadata.Labels
			}
			nj.Metadata.Generation++
			nj.Metadata.UpdatedAt = s.now().UTC()
			if err := s.writeJailRecord(ctx, nj); err != nil {
				return nil, err
			}
			s.mu.Lock()
			if e := s.jails[key]; e != nil {
				nj.Status.ImageGeneration = e.jail.Status.ImageGeneration
				e.jail = nj
			}
			s.mu.Unlock()
			ok = true
			s.emitIn(ctx, p.Meta.Name, "jail-created", "/api/v1/jails/"+name)
			s.kick()
			return s.getByKey(ctx, key)
		},
	})
	if err == nil {
		submitted = true
	}
	return op, err
}

// noteSuspected records what the preflight detection of an upgrade found in the root of a jail: on its container (so
// that it survives a restart), in the registry (condition SuspectedCompromised, doctor), in the update history and as
// the event jail-suspected-compromised. The other jails are not affected.
func (s *Service) noteSuspected(ctx context.Context, project, name, key string, se *lifecycle.SuspectedError, started time.Time) {
	s.life.NoteSuspected(ctx, key, se.Found)
	s.mu.Lock()
	if e := s.jails[key]; e != nil {
		e.suspected = se.Found
	}
	s.mu.Unlock()
	s.log.Warn("suspected_compromised: the root of the jail holds what an attack on host-side pkg plants; it was not upgraded", "jail", key, "found", se.Found)
	res, note := upgradeOutcome(se, "")
	s.appendHistory(api.UpdateRecord{ID: "upd-" + s.stamp() + "-" + randHex(3), Jail: name, Project: project, Kind: api.UpdateKindOS, StartedAt: started,
		FinishedAt: s.now().UTC(), Result: res, Error: "suspected_compromised: " + se.Found, Note: "(severity high) " + note})
	s.emitIn(ctx, project, "jail-suspected-compromised", "/api/v1/jails/"+name)
}

// unsupportedLayoutMessage says what a jail with the root layout of JailOps 0.9.2 and before is to this release, and
// the fix.
func unsupportedLayoutMessage(name string) string {
	return lifecycle.UnsupportedLayoutMessage(name) + "; it was made by an earlier release: create a new jail and move its data over, " +
		"or convert it with `jailops jail convert " + name + " --layout thick` on that earlier release before upgrading the host"
}

// hasUnsupportedLayout says whether the jail of key has the root layout of JailOps 0.9.2 and before.
func (s *Service) hasUnsupportedLayout(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.jails[key]
	return e != nil && e.unsupported
}

// RefuseUnsupportedLayout is the one guard of the HTTP layer: every request that changes a jail of the request's
// project, other than the power actions (the start and restart are refused by JailAction) and delete, is refused for a
// jail with an unsupported root layout (refuseUnsupported). A name that is no jail passes:
// the handler answers it.
func (s *Service) RefuseUnsupportedLayout(ctx context.Context, name string) error {
	_, key, err := s.jailRef(ctx, name)
	if err != nil {
		return nil
	}
	return s.refuseUnsupported(key)
}

// refuseUnsupported refuses an operation other than stop and delete on a jail with an unsupported root layout.
func (s *Service) refuseUnsupported(key string) error {
	if !s.hasUnsupportedLayout(key) {
		return nil
	}
	_, name := validate.SplitQualified(key)
	return lifecycle.UnsupportedLayoutError(name)
}
