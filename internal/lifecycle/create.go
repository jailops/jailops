package lifecycle

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// CreateResult is the outcome of CreateJail.
type CreateResult struct {
	Plan []string
	Jail api.Jail
}

// jailOwnDirs are the directories the jail's own mounts use (devfs, the Linux file systems and the mirror of a Linux
// jail's root): no volume goes there.
var jailOwnDirs = []string{"/dev", "/proc", "/sys", "/compat"}

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
		return api.Errorf(api.CodeInvalid, "a jail needs an image: name one with --image NAME (spec.image)")
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
	vols, err := s.Cfg.CheckVolumes(sp.Volumes)
	if err != nil {
		return err
	}
	sp.Volumes = vols
	return nil
}

// CreateJail creates a jail from an image generation: its root is a dataset of its own with a copy of the
// generation's files (no origin: the jail does not depend on the image afterwards). The whole jail lives below one container dataset, so undoing the
// first step removes everything a failed creation made. The volumes the jail attaches exist already: they are
// made with CreateVolume and are not part of the jail's datasets.
func (s *Service) CreateJail(ctx context.Context, j api.Jail, dryRun bool) (CreateResult, error) {
	p, res, err := s.planCreate(ctx, j, false)
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
	return s.planCreate(ctx, j, recovering)
}

func (s *Service) planCreate(ctx context.Context, j api.Jail, recovering bool) (*plan.Plan, CreateResult, error) {
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
	now := s.Cfg.now() // with a fixed Config.Now the rebuilt records are identical
	j.Metadata.Scope, j.Metadata.Generation = api.ScopeHost, 1
	j.Metadata.CreatedAt, j.Metadata.UpdatedAt = now, now
	j.Status = api.JailStatus{Power: api.PowerStopped, ImageGeneration: gen.Name}
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
	// The disk limit is the quota of the container: it covers the root, the delegated dataset and the snapshots of
	// all of them. The volumes a jail attaches are datasets of the project, outside the container.
	contProps := map[string]string{"mountpoint": cMP, PropImage: imageName, PropComplete: "off"}
	if q := j.Spec.Resources.Disk; q != "" {
		contProps["quota"] = q
	}
	if r := j.Spec.Resources.DiskReservation; r != "" {
		contProps["reservation"] = r
	}
	pl.Add(plan.Step{Name: "create-container", Describe: fmt.Sprintf("zfs create -o mountpoint=%s %s", cMP, c),
		Do: func(ctx context.Context) error {
			return z.Create(ctx, c, base(KindContainer, contProps))
		}, Undo: owned})

	rootProps := base(KindRoot, map[string]string{"mountpoint": rootMP, "canmount": "on", PropGen: strconv.Itoa(gen.Number),
		PropImage: imageName, PropManifest: gen.Manifest})
	{
		// The root is a dataset of its own with the files of the generation copied into it: no origin, so the image
		// can go and nothing of the root depends on it. The kernel does the copy (copy_file_range): inside one pool with
		// block cloning (OpenZFS 2.2 and later) it clones the blocks and the root takes next to no space and little time;
		// without it the files are copied for real.
		pl.Add(plan.Step{Name: "create-root", Describe: fmt.Sprintf("zfs create -o mountpoint=%s %s", rootMP, rootDS),
			Do: func(ctx context.Context) error { return z.Create(ctx, rootDS, rootProps) }},
			plan.Step{Name: "copy-root", Describe: fmt.Sprintf("copy the files of %s into %s", gen.Dataset, rootDS),
				Do: func(ctx context.Context) error {
					genMP := s.prop(ctx, gen.Dataset, "mountpoint")
					if !path.IsAbs(genMP) {
						return fmt.Errorf("the generation %s has no mountpoint (%q)", gen.Name, genMP)
					}
					// A generation is canmount=noauto: it is mounted only while something works on it, so mount it for
					// the copy and put it back after (a generation that was already mounted stays so).
					if s.prop(ctx, gen.Dataset, "mounted") != "yes" {
						if err := z.Mount(ctx, gen.Dataset); err != nil {
							return fmt.Errorf("mount the generation %s: %w", gen.Name, err)
						}
						defer func() { _ = z.Unmount(context.WithoutCancel(ctx), gen.Dataset) }()
					}
					if err := s.Files.CopyTree(ctx, genMP, rootMP); err != nil {
						return err
					}
					// FreeBSD-base stays disabled inside a jail, also when the image is older than the file
					// (contract rule 2; FreeBSD 16 ships the repository enabled)
					if osName != string(api.OSLinux) && j.Spec.OS != api.OSLinux {
						off := path.Join(rootMP, imagegen.BaseRepoOffPath)
						if ok, err := s.Files.Exists(ctx, off); err != nil {
							return err
						} else if !ok {
							if err := s.Files.MkdirAll(ctx, path.Dir(off)); err != nil {
								return err
							}
							if err := s.Files.WriteAtomic(ctx, off, []byte(imagegen.BaseRepoOff)); err != nil {
								return err
							}
						}
					}
					// the release the jail starts at; an upgrade in place changes it (PropFreeBSDVersion)
					if b, err := s.Files.ReadFile(ctx, path.Join(rootMP, "bin/freebsd-version")); err == nil {
						if m := reUserland.FindSubmatch(b); m != nil {
							return z.Set(ctx, rootDS, map[string]string{PropFreeBSDVersion: string(m[1])})
						}
					}
					return nil
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

	if j.Spec.Delegate {
		// The jail manages this dataset itself: it is never mounted on the host (jailed=on) and jail(8) attaches it at the
		// start (zfs.dataset). Not under JailOps' mountpoint rules: root in the jail sets its own.
		ds := s.Cfg.at(name).delegatedDS()
		pl.Add(plan.Step{Name: "create-delegated", Describe: "zfs create " + ds,
			Do: func(ctx context.Context) error {
				return z.Create(ctx, ds, base(KindDelegated, map[string]string{"mountpoint": "none", "canmount": "noauto"}))
			}},
			plan.Step{Name: "delegate-dataset", Describe: "zfs set jailed=on " + ds,
				Do: func(ctx context.Context) error { return z.Set(ctx, ds, map[string]string{"jailed": "on"}) }})
	}

	specB := marshal(rec)
	bundleB := s.bundleBytes(rec, j.Spec.Image, imageName, gen.Number, gen.Manifest, gen.Dataset, now)
	pl.Add(plan.Step{Name: "mount-container", Describe: "zfs mount " + c,
		Do: func(ctx context.Context) error { return z.Mount(ctx, c) }},
		plan.Step{Name: "write-spec", Describe: "write " + s.Cfg.at(name).specPath() + " atomically",
			Do: func(ctx context.Context) error { return s.Files.WriteAtomic(ctx, s.Cfg.at(name).specPath(), specB) }},
		plan.Step{Name: "write-bundle", Describe: "write " + s.Cfg.at(name).bundlePath() + " atomically",
			Do: func(ctx context.Context) error {
				if err := s.Files.WriteAtomic(ctx, s.Cfg.at(name).bundlePath(), bundleB); err != nil {
					return err
				}
				// no jail has run in the root yet: the copy of the image (NeverStartedFile)
				return s.Files.WriteAtomic(ctx, cMP+"/"+NeverStartedFile, nil)
			}},
		plan.Step{Name: "mark-complete", Describe: "zfs set jailops:complete=on " + c,
			Do: func(ctx context.Context) error { return z.Set(ctx, c, map[string]string{PropComplete: "on"}) }})
	res.Jail = j
	return pl, res, nil
}

// systemDirs are the directories of a jail's root that a volume does not replace: the system and its package
// database would be hidden. A volume below one of them (/var/db/postgres, /usr/local/www) is fine.
var systemDirs = []string{"/bin", "/etc", "/lib", "/libexec", "/sbin", "/usr", "/usr/local", "/var", "/var/db", "/var/db/pkg"}

// SystemDir says whether a volume mount path is one of the directories of a jail's system (systemDirs). A jail of
// JailOps 0.9.2 may still have such a volume: it starts, doctor warns, and an in-place upgrade refuses it.
func SystemDir(mount string) bool {
	m, ok := cleanMount(mount)
	return ok && slices.Contains(systemDirs, m)
}

// CheckVolumes checks the volumes a jail attaches against each other and against the root: one attachment per
// volume, one volume per path, no path that is a system directory (systemDirs), and no path at or below the file
// systems the jail makes itself (/dev, /proc, /sys, /compat). The live attach of the helper refuses the same paths;
// here the answer comes with the request and not at the next start. It returns the attachments with clean mount paths.
func (c Config) CheckVolumes(vs []api.VolumeAttach) ([]api.VolumeAttach, error) {
	seen := map[string]bool{}
	used := map[string]bool{}
	out := slices.Clone(vs)
	for i, v := range out {
		if err := validName("volume", v.Name); err != nil {
			return nil, err
		}
		if seen[v.Name] {
			return nil, api.Errorf(api.CodeInvalid, "volume %q is attached twice", v.Name)
		}
		seen[v.Name] = true
		m, ok := cleanMount(v.Mount)
		if !ok {
			return nil, api.Errorf(api.CodeInvalid, "volume %q has a bad mount path %q", v.Name, v.Mount)
		}
		if used[m] {
			return nil, api.Errorf(api.CodeInvalid, "volume %q mounts over another volume (%s)", v.Name, m)
		}
		if SystemDir(m) {
			return nil, api.Errorf(api.CodeInvalid, "volume %q mounts over %s, a directory of the jail's system", v.Name, m).
				WithHint("mount it below, for example " + m + "/data")
		}
		for _, sys := range jailOwnDirs {
			if m == sys || strings.HasPrefix(m, sys+"/") {
				return nil, api.Errorf(api.CodeInvalid, "volume %q: %s is a directory the jail's own file systems use", v.Name, m)
			}
		}
		used[m] = true
		out[i].Mount = m
	}
	return out, nil
}
