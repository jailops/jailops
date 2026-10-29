package lifecycle

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// CreateResult is the outcome of CreateJail.
type CreateResult struct {
	Plan []string
	Jail api.Jail
}

func cleanMount(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n") {
		return "", false
	}
	c := path.Clean(p)
	if c == "/" || strings.Contains(p, "..") {
		return "", false
	}
	return c, true
}

// KeyOf returns the project-qualified key of a jail, "<project>_<name>". The key is the OS-level
// jail name, the lifecycle's handle and, with the project, the path of the dataset.
func KeyOf(j api.Jail) (string, error) { return qualify(j.Metadata.Project, j.Metadata.Name) }

func (s *Service) validateCreate(j *api.Jail) error {
	if _, err := KeyOf(*j); err != nil {
		return err
	}
	if j.Metadata.UUID == "" {
		return api.Errorf(api.CodeInvalid, "the proposer must supply a jail uuid")
	}
	sp := &j.Spec
	if sp.Image == "" {
		return api.Errorf(api.CodeInvalid, "spec.image is required")
	}
	if sp.Encrypted {
		return api.Errorf(api.CodeUnsupported, "native ZFS encryption of jails is not implemented").
			WithHint("put the jail on a storage whose dataset is already an encryption root: create it with 'zfs create -o encryption=on -o keyformat=passphrase <pool>/jailops', register it with 'jailops storage create', and load its key with 'zfs load-key' after every boot")
	}
	if len(sp.Packages) > 0 {
		return api.Errorf(api.CodeUnsupported, "installing packages at creation (spec.packages) is not implemented").
			WithHint("declare them as provisioning steps: provision.packages, applied once at the first start ('jailops provision --help')")
	}
	if sp.Resources.Disk != "" && !sizeRe.MatchString(sp.Resources.Disk) {
		return api.Errorf(api.CodeInvalid, "bad disk quota %q", sp.Resources.Disk)
	}
	if sp.Resources.DiskReservation != "" && !sizeRe.MatchString(sp.Resources.DiskReservation) {
		return api.Errorf(api.CodeInvalid, "bad disk reservation %q", sp.Resources.DiskReservation)
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, p := range s.Cfg.persistent() {
		used[path.Clean(p.Path)] = true
	}
	for i, v := range sp.Volumes {
		if err := validName("volume", v.Name); err != nil {
			return err
		}
		if seen[v.Name] {
			return api.Errorf(api.CodeInvalid, "volume %q is declared twice", v.Name)
		}
		seen[v.Name] = true
		m, ok := cleanMount(v.Mount)
		if !ok {
			return api.Errorf(api.CodeInvalid, "volume %q has a bad mount path %q", v.Name, v.Mount)
		}
		if used[m] {
			return api.Errorf(api.CodeInvalid, "volume %q mounts over a persistent path (%s)", v.Name, m)
		}
		used[m] = true
		sp.Volumes[i].Mount = m
		if v.Quota != "" && !sizeRe.MatchString(v.Quota) {
			return api.Errorf(api.CodeInvalid, "volume %q has a bad quota %q", v.Name, v.Quota)
		}
	}
	return nil
}

// CreateJail creates a jail from an image generation. Thin jails get a clone
// of the generation as root plus the persistent datasets, thick jails a full
// copy. The whole jail lives below one container dataset, so undoing the
// first step removes everything a failed creation made.
func (s *Service) CreateJail(ctx context.Context, j api.Jail, dryRun bool) (CreateResult, error) {
	p, res, err := s.PlanCreate(ctx, j, false)
	if err != nil {
		return res, err
	}
	res.Plan = p.Describe()
	if dryRun {
		return res, nil
	}
	key, _ := KeyOf(j)
	if err := s.run(ctx, "create", key, p); err != nil {
		return res, err
	}
	return res, nil
}

// PlanCreate builds the create plan without executing it. With recovering
// set, the existence checks are skipped so the plan can be rebuilt after a
// crash to roll the interrupted creation back; j must be the same request.
func (s *Service) PlanCreate(ctx context.Context, j api.Jail, recovering bool) (*plan.Plan, CreateResult, error) {
	var res CreateResult
	if err := s.validateCreate(&j); err != nil {
		return nil, res, err
	}
	key, _ := KeyOf(j)
	name, id := key, j.Metadata.UUID
	z := s.ZFS
	c := s.Cfg.at(name).containerDS()
	if ok, err := z.Exists(ctx, c); err != nil {
		return nil, res, err
	} else if ok && !recovering {
		return nil, res, api.Errorf(api.CodeAlreadyExists, "jail %q already exists", j.Metadata.Name)
	}
	if ok, err := z.Exists(ctx, s.Cfg.at(name).jailsDS()); err != nil {
		return nil, res, err
	} else if !ok {
		return nil, res, api.Errorf(api.CodeNotFound, "dataset %s does not exist; the project must be set up on this storage first", s.Cfg.at(name).jailsDS())
	}
	gen, err := s.Images.Ensure(ctx, s.Cfg.at(name).st.Root, j.Spec.Image)
	if err != nil {
		return nil, res, err
	}
	if ok, _ := z.Exists(ctx, gen.Dataset+"@"+roSnap); !ok {
		return nil, res, api.Errorf(api.CodeNotFound, "generation %s has no @ro snapshot", gen.Name)
	}
	imageName, _, _ := strings.Cut(gen.Name, "#")
	osName := s.prop(ctx, gen.Dataset, PropOS)
	layout := j.Spec.Layout
	if layout == "" {
		layout = api.LayoutThin
		if osName == string(api.OSLinux) {
			layout = api.LayoutThick
		}
	}
	if layout == api.LayoutThin && osName == string(api.OSLinux) {
		return nil, res, api.Errorf(api.CodeInvalid, "Linux jails are thick; the thin layout is for FreeBSD")
	}
	j.Spec.Layout = layout
	now := s.Cfg.now() // with a fixed Config.Now the rebuilt records are identical
	j.Metadata.Scope, j.Metadata.Generation = api.ScopeHost, 1
	j.Metadata.CreatedAt, j.Metadata.UpdatedAt = now, now
	j.Status = api.JailStatus{Power: api.PowerStopped, Generation: gen.Name}
	rec := Record{Version: 1, Metadata: j.Metadata, Spec: j.Spec}

	rootDS, rootMP, cMP := s.Cfg.at(name).rootDS(), s.Cfg.at(name).rootMP(), s.Cfg.at(name).containerMP()
	owned := func(ctx context.Context) error {
		ok, err := z.Exists(ctx, c)
		if err != nil || !ok {
			return err
		}
		if s.prop(ctx, c, PropID) != id {
			return nil // not ours, never touch it
		}
		return z.DestroyRecursive(ctx, c)
	}
	base := func(kind string, extra map[string]string) map[string]string {
		m := map[string]string{PropManaged: "on", PropID: id, PropKind: kind, PropName: j.Metadata.Name, PropProject: j.Metadata.Project}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	pl := plan.New("create-jail " + name)
	pl.Add(plan.Step{Name: "create-container", Describe: fmt.Sprintf("zfs create -o mountpoint=%s %s", cMP, c),
		Do: func(ctx context.Context) error {
			return z.Create(ctx, c, base(KindContainer, map[string]string{"mountpoint": cMP, PropLayout: string(layout), PropImage: imageName, PropComplete: "off"}))
		}, Undo: owned})

	rootProps := base(KindRoot, map[string]string{"mountpoint": rootMP, "canmount": "on", PropGen: strconv.Itoa(gen.Number),
		PropImage: imageName, PropManifest: gen.Manifest})
	if q := j.Spec.Resources.Disk; q != "" {
		rootProps["quota"] = q
	}
	if r := j.Spec.Resources.DiskReservation; r != "" {
		rootProps["reservation"] = r
	}
	genRO := gen.Dataset + "@" + roSnap
	if layout == api.LayoutThin {
		pl.Add(plan.Step{Name: "clone-root", Describe: fmt.Sprintf("zfs clone %s %s", genRO, rootDS),
			Do: func(ctx context.Context) error { return z.Clone(ctx, genRO, rootDS, rootProps) }})
	} else {
		pl.Add(plan.Step{Name: "copy-root", Describe: fmt.Sprintf("zfs send -R %s | zfs receive %s", genRO, rootDS),
			Do: func(ctx context.Context) error { return z.Replicate(ctx, genRO, rootDS, rootProps) }},
			plan.Step{Name: "drop-copied-ro", Describe: "zfs destroy " + rootDS + "@" + roSnap,
				Do: func(ctx context.Context) error {
					if ok, _ := z.Exists(ctx, rootDS+"@"+roSnap); !ok {
						return nil
					}
					return z.Destroy(ctx, rootDS+"@"+roSnap)
				}})
	}

	if osName == string(api.OSLinux) || j.Spec.OS == api.OSLinux {
		// The Linuxulator mounts a nullfs mirror of the root on compat/linux
		// and a tmpfs on its dev/shm. The directory must exist in the real
		// root before the first start: devfs refuses to create it.
		shm := path.Join(rootMP, render.LinuxShmBacking)
		pl.Add(plan.Step{Name: "create-linux-shm", Describe: "mkdir -p " + shm,
			Do: func(ctx context.Context) error {
				if err := z.Mount(ctx, rootDS); err != nil {
					return err
				}
				// the mount point of the mirror, and the directory behind /dev/shm
				if err := s.Files.MkdirAll(ctx, path.Join(rootMP, render.LinuxCompat)); err != nil {
					return err
				}
				return s.Files.MkdirAll(ctx, shm)
			}})
	}

	if layout == api.LayoutThin {
		var mountedBelow []string
		for _, pp := range s.Cfg.persistent() {
			pp := pp
			ds := c + "/" + pp.Dataset
			mountedBelow = append(mountedBelow, ds)
			final := rootMP + path.Clean(pp.Path)
			stage := cMP + "/.seed-" + pp.Dataset
			props := base(KindPersistent, map[string]string{PropClass: string(pp.Class), PropPath: path.Clean(pp.Path), PropMerge: onOff(pp.Merge), "mountpoint": final})
			if pp.NoSeed {
				pl.Add(plan.Step{Name: "create-" + pp.Dataset, Describe: fmt.Sprintf("zfs create -o mountpoint=%s %s", final, ds),
					Do: func(ctx context.Context) error { return z.Create(ctx, ds, props) }})
				continue
			}
			seedProps := map[string]string{}
			for k, v := range props {
				seedProps[k] = v
			}
			seedProps["mountpoint"] = stage
			pl.Add(plan.Step{Name: "create-" + pp.Dataset, Describe: fmt.Sprintf("zfs create -o mountpoint=%s %s", stage, ds),
				Do: func(ctx context.Context) error { return z.Create(ctx, ds, seedProps) }})
			pl.Add(plan.Step{Name: "seed-" + pp.Dataset, Describe: fmt.Sprintf("copy %s%s into %s, then set mountpoint %s", rootMP, pp.Path, ds, final),
				Do: func(ctx context.Context) error {
					// UNVERIFIED: the image's directory is copied into the new
					// dataset before the dataset is mounted over it.
					if err := z.Mount(ctx, rootDS); err != nil {
						return err
					}
					if err := s.Files.MkdirAll(ctx, stage); err != nil {
						return err
					}
					if err := z.Mount(ctx, ds); err != nil {
						return err
					}
					// An image need not ship the directory (pkgbase has no
					// /usr/local): the dataset then starts empty.
					if ok, err := s.Files.Exists(ctx, rootMP+path.Clean(pp.Path)); err != nil {
						return err
					} else if ok {
						if err := s.Files.CopyTree(ctx, rootMP+path.Clean(pp.Path), stage); err != nil {
							return err
						}
					}
					if err := z.Unmount(ctx, ds); err != nil {
						return err
					}
					if err := z.Set(ctx, ds, map[string]string{"mountpoint": final}); err != nil {
						return err
					}
					return s.Files.RemoveAll(ctx, stage)
				}})
		}
		pl.Add(plan.Step{Name: "unmount-root", Describe: "zfs unmount " + rootDS,
			Do: func(ctx context.Context) error {
				// Setting the final mountpoint mounted each persistent dataset
				// over the root: they go first.
				for i := len(mountedBelow) - 1; i >= 0; i-- {
					if err := z.Unmount(ctx, mountedBelow[i]); err != nil {
						return err
					}
				}
				return z.Unmount(ctx, rootDS)
			}})
	}

	if len(j.Spec.Volumes) > 0 {
		vp := s.Cfg.at(name).volumesDS()
		pl.Add(plan.Step{Name: "create-volumes", Describe: "zfs create " + vp,
			Do: func(ctx context.Context) error {
				return z.Create(ctx, vp, base(KindVolumes, map[string]string{"canmount": "off", "mountpoint": "none"}))
			}})
		for _, v := range j.Spec.Volumes {
			v := v
			ds := s.Cfg.at(name).volumeDS(v.Name)
			// canmount=noauto: the dataset is mounted by mountTree after the root and the persistent datasets. Created
			// mounted, it would land in the container's directory below the root before the root is mounted, and the
			// root would then hide it (a start would find it mounted already and the jail would write underneath).
			props := base(KindVolume, map[string]string{PropClass: string(ClassData), PropPath: v.Mount, "mountpoint": rootMP + v.Mount, "canmount": "noauto"})
			if v.Quota != "" {
				props["quota"] = v.Quota
			}
			if v.ReadOnly {
				props["readonly"] = "on"
			}
			pl.Add(plan.Step{Name: "create-volume-" + v.Name, Describe: "zfs create " + ds,
				Do: func(ctx context.Context) error { return z.Create(ctx, ds, props) }})
		}
	}

	specB := marshal(rec)
	bundleB := s.bundleBytes(rec, j.Spec.Image, imageName, gen.Number, gen.Manifest, gen.Dataset, now)
	pl.Add(plan.Step{Name: "mount-container", Describe: "zfs mount " + c,
		Do: func(ctx context.Context) error { return z.Mount(ctx, c) }},
		plan.Step{Name: "write-spec", Describe: "write " + s.Cfg.at(name).specPath() + " atomically",
			Do: func(ctx context.Context) error { return s.Files.WriteAtomic(ctx, s.Cfg.at(name).specPath(), specB) }},
		plan.Step{Name: "write-bundle", Describe: "write " + s.Cfg.at(name).bundlePath() + " atomically",
			Do: func(ctx context.Context) error { return s.Files.WriteAtomic(ctx, s.Cfg.at(name).bundlePath(), bundleB) }},
		plan.Step{Name: "mark-complete", Describe: "zfs set jailops:complete=on " + c,
			Do: func(ctx context.Context) error { return z.Set(ctx, c, map[string]string{PropComplete: "on"}) }})
	res.Jail = j
	return pl, res, nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
