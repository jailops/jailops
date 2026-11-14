package service

import (
	"context"
	"slices"
	"sync"
	"time"

	"jailops.net/jailops/internal/hostdir"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// A volume with a path is a directory of the host: a mount (a storage box, a share, a disk of its own) that the host
// operator allows with admin-host-mount in priv.conf and that jails attach with nullfs like any volume. JailOps makes
// no dataset for it and keeps nothing of its files: it is not snapshotted, rolled back, resized, encrypted or backed
// up. What it adds is to know when the mount is gone (docs/storage.md, "Host directories").

// refuseHostDir is what an operation that needs a dataset answers for a volume of a host directory.
func refuseHostDir(name, what string) error {
	return invalid("volume %q is a directory of the host: %s", name, what).
		WithHint("its files are the host's; snapshot, copy or back them up there")
}

// requireGlobalAdminFor refuses an actor that is not a global administrator.
func requireGlobalAdminFor(ctx context.Context, what string) error {
	if isGlobalAdmin(ActorFrom(ctx)) {
		return nil
	}
	return api.Errorf(api.CodeForbidden, "%s is granted by a global administrator only", what).
		WithHint("the host operator also lists the allowed directories with admin-host-mount in priv.conf")
}

// probeHostDir looks at the directory of a host-directory volume through the helper.
func (s *Service) probeHostDir(ctx context.Context, spec api.VolumeSpec) (usable bool, detail string, err error) {
	if s.d.Files == nil {
		return false, "", api.Errorf(api.CodeUnsupported, "host directories are not available on this host")
	}
	write := spec.Watch && spec.Probe == api.VolumeProbeWrite
	p, err := hostdir.Within(ctx, hostdir.Timeout, func(ctx context.Context) (hostdir.Probe, error) {
		return s.d.Files.ProbeSource(ctx, spec.Path, write)
	})
	if err != nil {
		return false, "", err
	}
	return p.Usable(write), p.Lost(write), nil
}

// hostDirState is the state a volume's directory is in now: "ok" or "lost" and, when it is lost, why. A directory the
// helper will not look at (it is not below an admin-host-mount directory any more) is lost, with that as the reason.
func (s *Service) hostDirState(ctx context.Context, spec api.VolumeSpec) (state, detail string) {
	ok, detail, err := s.probeHostDir(ctx, spec)
	switch {
	case err != nil:
		return api.VolumeStateLost, err.Error()
	case ok:
		return api.VolumeStateOK, ""
	}
	return api.VolumeStateLost, detail
}

// hostDirUsers lists the jails of a project that attach a volume, with the way they attach it.
func (s *Service) hostDirUsers(project, volume string) []string {
	var out []string
	for _, u := range s.volumeUses(project, volume) {
		out = append(out, u.Jail)
	}
	return out
}

// ---- the watch ----

// hostDirWatch remembers the state the watch saw last, to act on a change and not on a state.
type hostDirWatch struct {
	mu   sync.Mutex
	last map[string]string // project/volume -> state
}

const defaultHostDirWatchTick = 15 * time.Second

// hostDirLoop checks every watched volume of a host directory every few seconds.
func (s *Service) hostDirLoop(ctx context.Context) {
	tick := s.d.VolumeWatchTick
	if tick <= 0 {
		tick = defaultHostDirWatchTick
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
		s.WatchHostDirs(ctx)
	}
}

// WatchHostDirs is one round of the watch (the loop runs it; tests call it): it looks at every watched volume of a host directory and acts on the ones
// whose state changed since the last round.
func (s *Service) WatchHostDirs(ctx context.Context) {
	if s.requireHost("volumes") != nil {
		return
	}
	objs, err := s.volKind.listAll()
	if err != nil {
		return
	}
	sys := WithActor(ctx, Actor{Name: "volume-watch", Kind: "system", Perm: api.PermAdmin, Global: true})
	seen := map[string]bool{}
	for _, o := range objs {
		if !o.Spec.IsHostDir() || !o.Spec.Watch {
			continue
		}
		key := o.Meta.Project + "/" + o.Meta.Name
		seen[key] = true
		state, detail := s.hostDirState(sys, o.Spec)
		s.hostDirs.mu.Lock()
		if s.hostDirs.last == nil {
			s.hostDirs.last = map[string]string{}
		}
		prev, known := s.hostDirs.last[key]
		s.hostDirs.last[key] = state
		s.hostDirs.mu.Unlock()
		switch {
		case known && prev == state:
		case state == api.VolumeStateLost:
			s.log.Warn("the mount of a volume is lost", "project", o.Meta.Project, "volume", o.Meta.Name, "path", o.Spec.Path, "detail", detail)
			s.emitIn(sys, o.Meta.Project, "volume-lost", "/api/v1/volumes/"+o.Meta.Name)
			if o.Spec.OnLoss == api.VolumeOnLossStop {
				s.stopHostDirUsers(sys, o)
			}
		case known:
			s.log.Info("the mount of a volume is back", "project", o.Meta.Project, "volume", o.Meta.Name, "path", o.Spec.Path)
			s.emitIn(sys, o.Meta.Project, "volume-restored", "/api/v1/volumes/"+o.Meta.Name)
			s.remountHostDirUsers(sys, o)
		default:
			// the first look after the daemon started: a volume that is lost now is lost, and a jail that runs on a
			// dead mount is as well off with the mount again, so a state that is ok is handled like a return
			if state == api.VolumeStateLost {
				s.log.Warn("the mount of a volume is lost", "project", o.Meta.Project, "volume", o.Meta.Name, "path", o.Spec.Path, "detail", detail)
				s.emitIn(sys, o.Meta.Project, "volume-lost", "/api/v1/volumes/"+o.Meta.Name)
				if o.Spec.OnLoss == api.VolumeOnLossStop {
					s.stopHostDirUsers(sys, o)
				}
			}
		}
	}
	s.hostDirs.mu.Lock()
	for k := range s.hostDirs.last {
		if !seen[k] {
			delete(s.hostDirs.last, k)
		}
	}
	s.hostDirs.mu.Unlock()
}

// runningUsers lists the keys of the running jails of a project that attach a volume.
func (s *Service) runningHostDirUsers(ctx context.Context, project, volume string) []string {
	var keys []string
	for _, jail := range s.hostDirUsers(project, volume) {
		k, _ := validate.QualifiedName(project, jail)
		if j, err := s.getByKey(ctx, k); err == nil && j.Status.Power == api.PowerRunning {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// stopHostDirUsers stops the running jails that attach a volume whose mount is lost. They stay stopped: a start is
// refused while the mount is lost, and the operator starts them when it is back.
func (s *Service) stopHostDirUsers(ctx context.Context, o object[api.VolumeSpec]) {
	for _, key := range s.runningHostDirUsers(ctx, o.Meta.Project, o.Meta.Name) {
		_, jail := validate.SplitQualified(key)
		if err := s.stopForLoss(ctx, o.Meta.Project, jail); err != nil {
			s.log.Warn("stopping a jail whose volume lost its mount failed", "project", o.Meta.Project, "jail", jail, "volume", o.Meta.Name, "err", err)
			continue
		}
		s.log.Warn("a jail was stopped: its volume lost its mount", "project", o.Meta.Project, "jail", jail, "volume", o.Meta.Name)
	}
}

// remountHostDirUsers gives the running jails that attach a volume their mount again once the directory is back: a
// nullfs mount over a mount that went away does not see the new one, so the old mount is removed and made again.
func (s *Service) remountHostDirUsers(ctx context.Context, o object[api.VolumeSpec]) {
	for _, key := range s.runningHostDirUsers(ctx, o.Meta.Project, o.Meta.Name) {
		_, jail := validate.SplitQualified(key)
		j, err := s.jailSnapshot(key)
		if err != nil {
			continue
		}
		for _, v := range s.effective(j).Volumes {
			if v.Name != o.Meta.Name {
				continue
			}
			if err := s.life.RemountHostDir(ctx, key, lifecycle.Attached{Name: v.Name, HostDir: true, Mountpoint: o.Spec.Path, Path: v.Mount, ReadOnly: v.ReadOnly}); err != nil {
				s.log.Warn("giving a jail its host directory again failed", "project", o.Meta.Project, "jail", jail, "volume", o.Meta.Name, "err", err)
				continue
			}
			s.emitIn(ctx, o.Meta.Project, "volume-remounted", "/api/v1/jails/"+jail)
		}
	}
}

// hostDirAdmission refuses a project operator that attaches a volume of a host directory the jail did not have: only a
// global administrator gives jails the host's directories. The volumes already attached stay.
func (s *Service) hostDirAdmission(ctx context.Context, project string, now, was []api.VolumeAttach) error {
	if isGlobalAdmin(ActorFrom(ctx)) {
		return nil
	}
	for _, v := range now {
		if slices.ContainsFunc(was, func(w api.VolumeAttach) bool { return w.Name == v.Name }) {
			continue
		}
		if o, err := s.volKind.p(project).get(v.Name); err == nil && o.Spec.IsHostDir() {
			return api.Errorf(api.CodeForbidden, "volume %q is a directory of the host: attaching it is granted by a global administrator only", v.Name).
				WithHint("the volume's files are the host's, outside the project's storage")
		}
	}
	return nil
}

// stopForLoss stops a jail the way an operator's stop does, and waits for it.
func (s *Service) stopForLoss(ctx context.Context, project, jail string) error {
	ctx = WithProject(ctx, project)
	op, err := s.JailAction(ctx, jail, api.JailAction{Action: "stop"})
	if err != nil {
		return err
	}
	return s.waitOp(ctx, op, "stopping "+jail)
}

// createHostDirVolume makes a volume of a host directory, as an operation whose result is the Volume. Only a global
// administrator does, and only for a directory that is on a mount now: a volume of a lost mount is a mistake.
func (s *Service) createHostDirVolume(ctx context.Context, p project, in api.VolumeCreate, spec api.VolumeSpec) (api.Operation, error) {
	if in.Passphrase != "" {
		return api.Operation{}, invalid("a volume of a host directory is not encrypted: it has no passphrase")
	}
	if err := requireGlobalAdminFor(ctx, "a volume that is a directory of the host"); err != nil {
		return api.Operation{}, err
	}
	proj, name := p.Meta.Name, in.Metadata.Name
	ks := s.volKind.p(proj)
	if err := ks.notTaken(name); err != nil {
		return api.Operation{}, err
	}
	meta := api.ObjectMeta{Name: name, Project: proj, Labels: in.Metadata.Labels, ManagedBy: in.Metadata.ManagedBy}
	return s.Submit(ctx, OpSpec{
		Description: "Create volume " + name, Locks: []string{volumeLock(proj, name)}, Project: proj,
		Resources: map[string][]string{"volumes": {"/api/v1/volumes/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			ok, detail, err := s.probeHostDir(ctx, spec)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, invalid("%s is not usable: %s", spec.Path, detail).
					WithHint("mount it first; the mount point has to lie below a directory of admin-host-mount in priv.conf")
			}
			o, err := ks.create(ctx, meta, spec)
			if err != nil {
				return nil, err
			}
			return s.volumeView(ctx, proj, o), nil
		},
	})
}
