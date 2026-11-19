package lifecycle

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/hostdir"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// A volume is a dataset of a project, <storage root>/projects/<project>/volumes/<name>, mounted on the host at its own
// mountpoint with canmount=on and devices=off. A jail that attaches it reaches it through nullfs (the jail's fstab):
// the volume outlives the jails, can be attached to several of them, and is never part of a jail's snapshots,
// rollbacks or copies. The root of the volume has the owner and mode it was made with.

// VolumeRequest is what CreateVolume makes a volume from.
type VolumeRequest struct {
	Storage Storage
	Project string
	Name    string
	UUID    string
	// Size is the ZFS quota ("" for none).
	Size string
	// UID, GID and Mode are the owner and the mode of the root of the volume.
	UID, GID  int
	Mode      uint32
	Encrypted bool
	// Restoring makes the volume as the target of a restore: it is created incomplete, so that the helper lets a file
	// agent write it until CompleteVolume says the restore is done (the same protection as for the datasets of a
	// jail that is being restored).
	Restoring bool
}

// VolumeInfo is what the datasets say about a volume.
type VolumeInfo struct {
	Dataset    string
	Mountpoint string
	Used       int64
	Mounted    bool
	// Locked is true for an encrypted volume whose key is not loaded.
	Locked bool
	// Snapshots are the volume's snapshots, oldest first.
	Snapshots []VolumeSnap
}

// VolumeSnap is a snapshot of a volume: what has been written to the volume since it was taken is what a rollback to
// it would discard.
type VolumeSnap struct {
	Name    string
	Written int64
	Comment string
}

func (s *Service) validateVolume(r VolumeRequest) error {
	if err := validName("project", r.Project); err != nil {
		return err
	}
	if err := validName("volume", r.Name); err != nil {
		return err
	}
	if r.Size != "" && !sizeRe.MatchString(r.Size) {
		return api.Errorf(api.CodeInvalid, "bad volume size %q", r.Size)
	}
	if r.UID < 0 || r.GID < 0 || r.UID > 0x7fffffff || r.GID > 0x7fffffff {
		return api.Errorf(api.CodeInvalid, "uid and gid of a volume are numbers from 0")
	}
	if r.Mode&^uint32(0o777) != 0 {
		return api.Errorf(api.CodeInvalid, "mode of a volume is at most 0777: no setuid, setgid or sticky bit")
	}
	if r.Encrypted && s.Keys == nil {
		return api.Errorf(api.CodeUnsupported, "volume %q is encrypted and this host has no key operations", r.Name)
	}
	return nil
}

// volumeKey is the name a volume's plans and journal entries carry.
func volumeKey(project, name string) string { return project + "_" + name }

func (s *Service) volumeProps(r VolumeRequest) map[string]string {
	m := map[string]string{PropManaged: "on", PropID: r.UUID, PropKind: KindVolume, PropName: r.Name, PropProject: r.Project,
		"mountpoint": VolumeMP(r.Storage, r.Project, r.Name), "canmount": "on", "devices": "off"}
	if r.Size != "" {
		m["quota"] = r.Size
	}
	if r.Restoring {
		m[PropComplete] = "off"
	}
	return m
}

// CompleteVolume ends the restore of a volume made with VolumeRequest.Restoring: from now on no file agent writes it.
func (s *Service) CompleteVolume(ctx context.Context, st Storage, project, name string) error {
	ds := VolumeDS(st, project, name)
	if s.prop(ctx, ds, PropManaged) != "on" || s.prop(ctx, ds, PropKind) != KindVolume {
		return api.Errorf(api.CodeNotFound, "volume %q is not there", name)
	}
	return s.ZFS.Set(ctx, ds, map[string]string{PropComplete: "on"})
}

// PlanCreateVolume builds the plan that makes a volume: the project's volumes parent (once), the dataset with its
// properties, and the owner and mode of its root. The passphrase of an encrypted volume goes to the key operation of
// the creating step and is not kept anywhere.
func (s *Service) PlanCreateVolume(ctx context.Context, r VolumeRequest, passphrase []byte) (*plan.Plan, error) {
	if err := s.validateVolume(r); err != nil {
		return nil, err
	}
	switch {
	case r.Encrypted && len(passphrase) == 0:
		return nil, api.Errorf(api.CodeInvalid, "an encrypted volume needs a passphrase")
	case !r.Encrypted && len(passphrase) > 0:
		return nil, api.Errorf(api.CodeInvalid, "a passphrase was given but the volume is not encrypted")
	case r.Encrypted:
		if err := api.CheckPassphrase(string(passphrase)); err != nil {
			return nil, err
		}
	}
	return s.volumePlan(r, passphrase), nil
}

// RecoverCreateVolume rolls back an interrupted CreateVolume: the dataset the run made is destroyed and the volumes
// parent it made is removed again. It reports whether a run was found.
func (s *Service) RecoverCreateVolume(ctx context.Context, r VolumeRequest) (bool, error) {
	return s.RecoverRun(ctx, "create-volume", volumeKey(r.Project, r.Name), s.volumePlan(r, nil))
}

// volumePlan is the plan of a volume's creation; the passphrase is only used by the step that makes an encrypted
// volume (a recovery needs the undo steps and runs none of the others).
func (s *Service) volumePlan(r VolumeRequest, passphrase []byte) *plan.Plan {
	z := s.ZFS
	parent := ProjectVolumesDS(r.Storage, r.Project)
	ds := VolumeDS(r.Storage, r.Project, r.Name)
	mp := VolumeMP(r.Storage, r.Project, r.Name)
	props := s.volumeProps(r)
	pl := plan.New("create-volume " + volumeKey(r.Project, r.Name))
	madeParent := false
	pl.Add(plan.Step{Name: "create-volumes-parent", Describe: "zfs create " + parent + " (once per project)",
		Do: func(ctx context.Context) error {
			if ok, err := z.Exists(ctx, parent); err != nil || ok {
				return err
			}
			madeParent = true
			return z.Create(ctx, parent, map[string]string{PropManaged: "on", PropKind: KindVolumes, PropProject: r.Project, "canmount": "off", "mountpoint": "none"})
		},
		Undo: func(ctx context.Context) error {
			if !madeParent {
				return nil
			}
			return s.destroyIfExists(parent)(ctx)
		}})
	if r.Encrypted {
		props[PropEncrypted] = "on"
		pl.Add(plan.Step{Name: "create-volume", Describe: "zfs create -o encryption=aes-256-gcm -o keyformat=passphrase -o keylocation=prompt " + ds,
			Do:   func(ctx context.Context) error { return s.Keys.CreateEncrypted(ctx, ds, props, passphrase) },
			Undo: s.destroyIfExists(ds)})
	} else {
		pl.Add(plan.Step{Name: "create-volume", Describe: "zfs create " + ds,
			Do:   func(ctx context.Context) error { return z.Create(ctx, ds, props) },
			Undo: s.destroyIfExists(ds)})
	}
	mode := r.Mode
	pl.Add(plan.Step{Name: "own-root", Describe: fmt.Sprintf("chown %d:%d and chmod %04o the root of %s", r.UID, r.GID, mode, mp),
		Do: func(ctx context.Context) error {
			// A dataset that is mounted is needed here: ZFS mounts a new one at once; mounting again is a no-op.
			if err := z.Mount(ctx, ds); err != nil {
				return err
			}
			return s.Files.SetOwner(ctx, mp, r.UID, r.GID, mode)
		}})
	return pl
}

// CreateVolume makes a volume (see PlanCreateVolume).
func (s *Service) CreateVolume(ctx context.Context, r VolumeRequest, passphrase []byte, dryRun bool) ([]string, error) {
	pl, err := s.PlanCreateVolume(ctx, r, passphrase)
	if err != nil {
		return nil, err
	}
	desc := pl.Describe()
	if dryRun {
		return desc, nil
	}
	ds := VolumeDS(r.Storage, r.Project, r.Name)
	if ok, err := s.ZFS.Exists(ctx, ds); err != nil {
		return desc, err
	} else if ok {
		return desc, api.Errorf(api.CodeAlreadyExists, "volume %q already exists", r.Name)
	}
	return desc, s.run(ctx, "create-volume", volumeKey(r.Project, r.Name), pl)
}

// DestroyVolume removes a volume with its snapshots. The caller has checked that no jail attaches it.
func (s *Service) DestroyVolume(ctx context.Context, st Storage, project, name string) error {
	ds := VolumeDS(st, project, name)
	ok, err := s.ZFS.Exists(ctx, ds)
	if err != nil || !ok {
		return err
	}
	if s.prop(ctx, ds, PropManaged) != "on" || s.prop(ctx, ds, PropKind) != KindVolume {
		return api.Errorf(api.CodeForbidden, "%s is not a volume of JailOps", ds)
	}
	if s.Keys != nil && s.prop(ctx, ds, PropEncrypted) == "on" {
		_ = s.ZFS.Unmount(ctx, ds)
		_ = s.Keys.UnloadKey(ctx, ds)
	}
	return s.ZFS.DestroyRecursive(ctx, ds)
}

// ResizeVolume changes the quota of a volume; an empty size removes it. ZFS refuses a quota below what is used.
func (s *Service) ResizeVolume(ctx context.Context, st Storage, project, name, size string) error {
	if size != "" && !sizeRe.MatchString(size) {
		return api.Errorf(api.CodeInvalid, "bad volume size %q", size)
	}
	ds := VolumeDS(st, project, name)
	if size == "" {
		return s.ZFS.Inherit(ctx, ds, "quota")
	}
	return s.ZFS.Set(ctx, ds, map[string]string{"quota": size})
}

// VolumeInfo reads a volume from its dataset.
func (s *Service) VolumeInfo(ctx context.Context, st Storage, project, name string) (VolumeInfo, error) {
	ds := VolumeDS(st, project, name)
	ok, err := s.ZFS.Exists(ctx, ds)
	if err != nil {
		return VolumeInfo{}, err
	}
	if !ok {
		return VolumeInfo{}, api.Errorf(api.CodeNotFound, "the dataset of volume %q is missing: %s", name, ds)
	}
	info := VolumeInfo{Dataset: ds, Mountpoint: s.prop(ctx, ds, "mountpoint"), Mounted: s.prop(ctx, ds, "mounted") == "yes"}
	info.Used, _ = strconv.ParseInt(s.prop(ctx, ds, "used"), 10, 64)
	if s.prop(ctx, ds, PropEncrypted) == "on" {
		if s.Keys == nil {
			info.Locked = true
		} else if ks, err := s.Keys.KeyStatus(ctx, ds); err != nil {
			return info, err
		} else {
			info.Locked = ks != "available"
		}
	}
	snaps, err := s.ZFS.Snapshots(ctx, ds)
	if err != nil {
		return info, err
	}
	for _, sn := range snaps {
		if i := strings.LastIndexByte(sn, '@'); i >= 0 {
			sn = sn[i+1:]
		}
		w, err := s.ZFS.Written(ctx, ds+"@"+sn)
		if err != nil {
			return info, err
		}
		info.Snapshots = append(info.Snapshots, VolumeSnap{Name: sn, Written: w, Comment: s.prop(ctx, ds+"@"+sn, PropComment)})
	}
	return info, nil
}

// SnapshotVolume takes a snapshot of a volume.
func (s *Service) SnapshotVolume(ctx context.Context, st Storage, project, name, snap string, props map[string]string) error {
	if err := validName("snapshot", snap); err != nil {
		return err
	}
	ds := VolumeDS(st, project, name)
	if ok, err := s.ZFS.Exists(ctx, ds+"@"+snap); err != nil {
		return err
	} else if ok {
		return api.Errorf(api.CodeAlreadyExists, "snapshot %q already exists", snap)
	}
	return s.ZFS.Snapshot(ctx, props, ds+"@"+snap)
}

// DeleteVolumeSnapshot deletes a snapshot of a volume.
func (s *Service) DeleteVolumeSnapshot(ctx context.Context, st Storage, project, name, snap string) error {
	return s.ZFS.Destroy(ctx, VolumeDS(st, project, name)+"@"+snap)
}

// RollbackVolume returns a volume to a snapshot, destroying the snapshots after it. The caller has checked that no
// running jail has the volume attached: a rollback would pull the files out from under the nullfs mounts.
func (s *Service) RollbackVolume(ctx context.Context, st Storage, project, name, snap string) error {
	return s.ZFS.Rollback(ctx, VolumeDS(st, project, name)+"@"+snap, true)
}

// CopyIntoVolume copies the files a stopped jail has at mountPath into a volume, so that attaching the volume there
// does not hide them. The root and the persistent datasets of the jail are mounted for the copy and unmounted again;
// nothing is removed from the jail. A path the jail does not have copies nothing.
func (s *Service) CopyIntoVolume(ctx context.Context, name, mountPath string, st Storage, project, volume string) error {
	js, err := s.observe(ctx, name)
	if err != nil {
		return err
	}
	if running, err := s.Jails.Running(ctx, name); err != nil {
		return err
	} else if running {
		return api.Errorf(api.CodeBusy, "jail %q is running", name)
	}
	mount, ok := cleanMount(mountPath)
	if !ok {
		return api.Errorf(api.CodeInvalid, "bad mount path %q", mountPath)
	}
	vds := VolumeDS(st, project, volume)
	if ok, err := s.ZFS.Exists(ctx, vds); err != nil {
		return err
	} else if !ok {
		return api.Errorf(api.CodeNotFound, "the dataset of volume %q is missing: %s", volume, vds)
	}
	if err := s.ZFS.Mount(ctx, vds); err != nil {
		return err
	}
	order := append([]string{js.Root}, dsNames(js.Persist)...)
	for _, ds := range order {
		if err := s.ZFS.Mount(ctx, ds); err != nil {
			return err
		}
	}
	defer func() {
		for i := len(order) - 1; i >= 0; i-- {
			_ = s.ZFS.Unmount(ctx, order[i])
		}
	}()
	src := s.Cfg.at(name).rootMP() + mount
	if ok, err := s.Files.Exists(ctx, src); err != nil || !ok {
		return err
	}
	return s.Files.CopyTree(ctx, src, VolumeMP(st, project, volume))
}

// CheckHostSource resolves and opens a directory of the host that is about to be copied into a volume, so that a path the
// helper will not read is refused before the dataset of the volume is made.
func (s *Service) CheckHostSource(ctx context.Context, src string) error {
	_, err := s.Files.ImportHostDir(ctx, src, "", true)
	return err
}

// ImportIntoVolume copies the files of a directory of the host into the (new, empty) volume: owners, modes, times and
// links are kept; no setuid or setgid bit and no device node comes across.
func (s *Service) ImportIntoVolume(ctx context.Context, st Storage, project, volume, src string) (hostdir.Import, error) {
	vds := VolumeDS(st, project, volume)
	if ok, err := s.ZFS.Exists(ctx, vds); err != nil {
		return hostdir.Import{}, err
	} else if !ok {
		return hostdir.Import{}, api.Errorf(api.CodeNotFound, "the dataset of volume %q is missing: %s", volume, vds)
	}
	if err := s.ZFS.Mount(ctx, vds); err != nil {
		return hostdir.Import{}, err
	}
	return s.Files.ImportHostDir(ctx, src, VolumeMP(st, project, volume), false)
}

func dsNames(l []dsInfo) []string {
	out := make([]string, len(l))
	for i, d := range l {
		out[i] = d.DS
	}
	return out
}

// RemountHostDir gives a running jail the mount of a host directory again once the directory is back: a nullfs mount
// over a mount that went away does not see the one that came back, so the old mount is removed and made again, with
// the same options the jail's fstab has. A Linux jail has the second mount below its mirror, which goes the same way.
func (s *Service) RemountHostDir(ctx context.Context, name string, a Attached) error {
	if err := s.checkHostDir(ctx, a); err != nil {
		return err
	}
	targets := []string{s.Cfg.at(name).rootMP() + a.Path}
	if a.Linux {
		targets = append(targets, s.compatTarget(name, a))
	}
	for i := len(targets) - 1; i >= 0; i-- {
		_, _ = s.Exec.Run(ctx, []string{"umount", "-f", targets[i]}) // a mount that is not there is fine
	}
	for i, target := range targets {
		argv := []string{"mount", "-t", "nullfs"}
		if a.ReadOnly {
			argv = append(argv, "-o", "ro")
		}
		argv = append(argv, a.Mountpoint, target)
		out, err := s.Exec.Run(ctx, argv)
		if err == nil {
			continue
		}
		// the old mount is gone and the new one is not there: the jail would write into the empty directory of its
		// own root, which a rollback or an upgrade of the root throws away. A small read-only tmpfs stands in until
		// the mount can be made, on this target and on those not mounted yet.
		note := ""
		for _, left := range targets[i:] {
			if pout, perr := s.Exec.Run(ctx, []string{"mount", "-t", "tmpfs", "-o", "size=1m,ro", "tmpfs", left}); perr != nil {
				note = fmt.Sprintf(" (and no stand-in mount: %v: %s)", perr, strings.TrimSpace(string(pout)))
			}
		}
		return fmt.Errorf("mount %s on %s: %w: %s%s", a.Mountpoint, target, err, strings.TrimSpace(string(out)), note)
	}
	return nil
}

// BindVolumes mounts the volumes a stopped jail attaches into its root with nullfs, as its fstab does when it starts, so
// that what the host installs into the root lands where the jail will see it: the files of a package in a directory a
// volume covers go into the volume, as they do when the jail runs. UnbindVolumes takes them off before the start
// mounts them itself. The volumes are mounted on the host first (MountTree); the mounts are read-write.
func (s *Service) BindVolumes(ctx context.Context, name string) error {
	atts, err := s.Cfg.attached(name)
	if err != nil {
		return err
	}
	// the helper mounts only below the root of a jail it manages, which a jail that never started is not yet: its
	// configuration is rendered, as the start does first
	if len(atts) > 0 {
		if err := s.Hooks.PreStart(ctx, name); err != nil {
			return err
		}
	}
	for i, a := range atts {
		target := s.Cfg.at(name).rootMP() + a.Path
		if err := s.Files.MkdirAll(ctx, target); err != nil {
			return err
		}
		if out, err := s.Exec.Run(ctx, []string{"mount", "-t", "nullfs", a.Mountpoint, target}); err != nil {
			_ = s.unbind(ctx, name, atts[:i])
			return fmt.Errorf("mount %s on %s: %w: %s", a.Mountpoint, target, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// UnbindVolumes undoes BindVolumes.
func (s *Service) UnbindVolumes(ctx context.Context, name string) error {
	atts, err := s.Cfg.attached(name)
	if err != nil {
		return err
	}
	return s.unbind(ctx, name, atts)
}

func (s *Service) unbind(ctx context.Context, name string, atts []Attached) error {
	var first error
	for i := len(atts) - 1; i >= 0; i-- {
		target := s.Cfg.at(name).rootMP() + atts[i].Path
		if out, err := s.Exec.Run(ctx, []string{"umount", target}); err != nil && first == nil {
			first = fmt.Errorf("unmount %s: %w: %s", target, err, strings.TrimSpace(string(out)))
		}
	}
	return first
}

// VolumeBusyError is the refusal of UnmountLive: a process of the jail still uses the mount.
type VolumeBusyError struct {
	Path   string // the path in the jail
	Detail string // what umount said
}

func (e *VolumeBusyError) Error() string {
	return fmt.Sprintf("%s is in use in the jail: %s", e.Path, e.Detail)
}

// MountLive gives a RUNNING jail volumes: each is mounted on the host at its own place first (idempotent), the mount
// point directory is made in the jail's root, and the volume is bound there with nullfs, which the jail sees at once.
// The jail's fstab (rendered from its specification) holds the same mounts, so the next start makes them again and
// the stop of jail(8) takes them off. A failure takes the mounts this call made off again.
func (s *Service) MountLive(ctx context.Context, name string, atts []Attached) error {
	var done []Attached
	for _, a := range atts {
		if err := s.mountLive(ctx, name, a); err != nil {
			_ = s.UnmountLive(ctx, name, done)
			return err
		}
		done = append(done, a)
	}
	return nil
}

func (s *Service) mountLive(ctx context.Context, name string, a Attached) error {
	if a.HostDir {
		if err := s.checkHostDir(ctx, a); err != nil {
			return err
		}
	} else if err := s.ZFS.Mount(ctx, a.Dataset); err != nil {
		if low := strings.ToLower(err.Error()); strings.Contains(low, "key") && (strings.Contains(low, "not loaded") || strings.Contains(low, "unavailable")) {
			return fmt.Errorf("volume %s is locked: unlock it first (jailops volume unlock %s): %w", a.Name, a.Name, err)
		}
		return fmt.Errorf("volume %s: %w", a.Name, err)
	}
	target := s.Cfg.at(name).rootMP() + a.Path
	if err := s.Files.MkdirAll(ctx, target); err != nil {
		return err
	}
	argv := []string{"mount", "-t", "nullfs"}
	if a.ReadOnly {
		argv = append(argv, "-o", "ro")
	}
	argv = append(argv, a.Mountpoint, target)
	if out, err := s.Exec.Run(ctx, argv); err != nil {
		return fmt.Errorf("mount %s on %s: %w: %s", a.Mountpoint, target, err, strings.TrimSpace(string(out)))
	}
	if a.Linux {
		compat := s.compatTarget(name, a)
		argv = append(argv[:len(argv)-2:len(argv)-2], a.Mountpoint, compat)
		if out, err := s.Exec.Run(ctx, argv); err != nil {
			_, _ = s.Exec.Run(ctx, []string{"umount", target})
			return fmt.Errorf("mount %s on %s: %w: %s", a.Mountpoint, compat, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// compatTarget is where a Linux jail sees a volume: the path below the nullfs mirror of its root.
func (s *Service) compatTarget(name string, a Attached) string {
	return s.Cfg.at(name).rootMP() + "/" + render.LinuxCompat + a.Path
}

// UnmountLive takes volumes off a RUNNING jail, deepest path first. A Linux jail has two mounts per volume, and the one
// below the compat/linux mirror comes off first. A mount that is in use is a *VolumeBusyError and everything this call
// unmounted is mounted again, so a refused detach changes nothing. A mount that is not there is fine. The volume and its
// files stay.
func (s *Service) UnmountLive(ctx context.Context, name string, atts []Attached) error {
	order := slices.Clone(atts)
	slices.SortStableFunc(order, func(x, y Attached) int { return depth(y.Path) - depth(x.Path) })
	var done []Attached
	for _, a := range order {
		changed, err := s.unmountLive(ctx, name, a)
		if err != nil {
			for i := len(done) - 1; i >= 0; i-- {
				_ = s.mountLive(ctx, name, done[i])
			}
			return err
		}
		if changed {
			done = append(done, a)
		}
	}
	return nil
}

// unmountLive takes one volume off: the mount below the mirror of a Linux jail first, then the one in the root. changed
// says whether a mount was taken off.
func (s *Service) unmountLive(ctx context.Context, name string, a Attached) (bool, error) {
	changed := false
	var compat string
	if a.Linux {
		compat = s.compatTarget(name, a)
		did, err := s.umountOne(ctx, compat, a.Path)
		if err != nil {
			return false, err
		}
		changed = did
	}
	did, err := s.umountOne(ctx, s.Cfg.at(name).rootMP()+a.Path, a.Path)
	if err != nil {
		if changed {
			argv := []string{"mount", "-t", "nullfs"}
			if a.ReadOnly {
				argv = append(argv, "-o", "ro")
			}
			_, _ = s.Exec.Run(ctx, append(argv, a.Mountpoint, compat))
		}
		return false, err
	}
	return changed || did, nil
}

// umountOne unmounts target. A mount that is not there is fine (false, nil); a mount in use is a *VolumeBusyError.
func (s *Service) umountOne(ctx context.Context, target, path string) (bool, error) {
	out, err := s.Exec.Run(ctx, []string{"umount", target})
	if err == nil {
		return true, nil
	}
	text := strings.TrimSpace(string(out))
	// the helper refuses a path that is not a mount point with an error of its own ("umount: P is not a mount point"):
	// a Linux jail that started under an older version has no mount below the mirror, and there is nothing to take off
	switch low := strings.ToLower(text + " " + err.Error()); {
	case strings.Contains(low, "not a file system root directory"), strings.Contains(low, "not mounted"), strings.Contains(low, "is not a mount point"):
		return false, nil
	case strings.Contains(low, "busy"):
		return false, &VolumeBusyError{Path: path, Detail: text}
	}
	return false, fmt.Errorf("unmount %s: %w: %s", target, err, text)
}
