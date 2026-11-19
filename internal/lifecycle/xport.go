package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// The export and import of a jail (docs/export-import.md): the datasets that are unique to the jail leave as zfs
// streams and come back as a new jail. What is the same on every jail of an image (the root of a thin jail) is not
// sent: the target makes it from its own copy of the image.

// ExportStream is one dataset of a jail that an export sends, with the parts (streams) it is sent in.
type ExportStream struct {
	// Dataset is the source dataset.
	Dataset string
	// Snapshot is the snapshot the stream ends at, when it exists already (a snapshot of the jail); empty means that
	// ExportJail takes a snapshot of its own.
	Snapshot string
	Parts    []api.ExportPart
	// Volume is the name of a volume (a dataset of the project, outside the jail).
	Volume string
}

// ExportPlan is what an export of one jail sends.
type ExportPlan struct {
	Key      string
	UUID     string
	Layout   api.Layout
	Image    string
	Gen      int
	Manifest string
	Streams  []ExportStream
	Warnings []string
}

// ExportSnapName is the name of the snapshot an export made with this stamp takes of the datasets it sends.
func ExportSnapName(stamp string) string { return "jo-export-" + stamp }

func (s *Service) exportSnapName(stamp string) string { return ExportSnapName(s.stamp(stamp)) }

// PlanExport lists what an export of the jail sends. snapshot, when set, is a snapshot of the jail (the name given to
// `jail snapshot create`): the datasets are sent at that point. withVolumes adds the volumes of the jail that are
// datasets of the project; a volume is always sent at a snapshot of its own, because a jail's snapshot does not hold
// the volumes it attaches.
func (s *Service) PlanExport(ctx context.Context, key, snapshot string, withVolumes bool) (ExportPlan, error) {
	st, err := s.ready(ctx, key)
	if err != nil {
		return ExportPlan{}, err
	}
	if snapshot != "" {
		for _, d := range st.all() {
			if ok, err := s.ZFS.Exists(ctx, d+"@"+snapshot); err != nil {
				return ExportPlan{}, err
			} else if !ok {
				return ExportPlan{}, api.Errorf(api.CodeNotFound, "jail %q has no snapshot %q", key, snapshot)
			}
		}
	}
	p := ExportPlan{Key: key, UUID: st.ID, Layout: st.Layout, Image: st.Image, Gen: st.Gen, Manifest: st.Manifest}
	if st.Layout == api.LayoutThick {
		p.Streams = append(p.Streams, ExportStream{Dataset: st.Root, Snapshot: snapshot, Parts: []api.ExportPart{
			{Dataset: "root", Kind: api.PartRoot}}})
	}
	for _, d := range st.Persist {
		rel := strings.TrimPrefix(d.DS, st.Container+"/")
		props := map[string]string{PropPath: d.Path, PropClass: string(d.Class), PropMerge: onOff(d.Merge)}
		parts := []api.ExportPart{{Dataset: rel, Kind: api.PartPersistent, Props: props}}
		if ok, _ := s.ZFS.Exists(ctx, d.DS+"@"+BaselineSnap); ok {
			// the baseline goes first, in full; the export snapshot then follows incrementally
			parts = []api.ExportPart{
				{Dataset: rel, Kind: api.PartPersistent, Snapshot: BaselineSnap, Props: props},
				{Dataset: rel, Kind: api.PartPersistent, From: BaselineSnap, Props: props}}
		}
		p.Streams = append(p.Streams, ExportStream{Dataset: d.DS, Snapshot: snapshot, Parts: parts})
	}
	if st.Delegated != "" {
		p.Warnings = append(p.Warnings, "the dataset the jail manages itself (zfs_delegation) is not exported: the jail on the other host gets an empty one")
	}
	if len(st.HostDirs) > 0 {
		var names []string
		for _, h := range st.HostDirs {
			names = append(names, h.Name)
		}
		p.Warnings = append(p.Warnings, "the volumes that are directories of the host are not exported: "+strings.Join(names, ", "))
	}
	if withVolumes {
		for _, v := range st.Volumes {
			p.Streams = append(p.Streams, ExportStream{Dataset: v.DS, Volume: volumeName(v.DS), Parts: []api.ExportPart{
				{Dataset: volumeName(v.DS), Kind: api.PartVolume, Raw: v.Encrypted}}})
		}
	} else if len(st.Volumes) > 0 {
		var names []string
		for _, v := range st.Volumes {
			names = append(names, volumeName(v.DS))
		}
		p.Warnings = append(p.Warnings, "the volumes are not exported (use --with-volumes): "+strings.Join(names, ", "))
	}
	return p, nil
}

func streamsOf(s *Service) (Streams, error) {
	st, ok := s.ZFS.(Streams)
	if !ok {
		return nil, api.Errorf(api.CodeUnsupported, "this host's ZFS layer cannot stream datasets")
	}
	return st, nil
}

// ExportJail sends the datasets of the plan. open gives the writer of each part (and is told its description); the
// part is closed here. The snapshots the export took are removed again, whatever happens.
func (s *Service) ExportJail(ctx context.Context, p ExportPlan, stamp string, open func(api.ExportPart) (io.WriteCloser, error)) (err error) {
	str, err := streamsOf(s)
	if err != nil {
		return err
	}
	snap := s.exportSnapName(stamp)
	var taken []string
	for _, x := range p.Streams {
		if x.Snapshot == "" {
			taken = append(taken, x.Dataset+"@"+snap)
		}
	}
	if len(taken) > 0 {
		if err := s.ZFS.Snapshot(ctx, nil, taken...); err != nil {
			return err
		}
		defer func() {
			if derr := s.destroySnaps(taken)(context.WithoutCancel(ctx)); derr != nil && err == nil {
				err = derr
			}
		}()
	}
	for _, x := range p.Streams {
		for _, part := range x.Parts {
			name := part.Snapshot
			if name == "" {
				name = x.Snapshot
			}
			if name == "" {
				name = snap
			}
			part.Snapshot = name
			w, err := open(part)
			if err != nil {
				return err
			}
			serr := str.SendStream(ctx, x.Dataset+"@"+name, part.From, part.Raw, w)
			if cerr := w.Close(); serr == nil {
				serr = cerr
			}
			if serr != nil {
				return fmt.Errorf("sending %s: %w", part.Dataset, serr)
			}
		}
	}
	return nil
}

// ImportInput is what ImportJail needs; the service has checked the request, chosen the name and the uuid, resolved
// the image and made the objects the jail refers to.
type ImportInput struct {
	// Jail is the jail as it will be: its name, project and new uuid, the spec with the addresses the service decided.
	Jail api.Jail
	// Image is the generation a thin jail's root is cloned from; for a thick jail it only names the image in the
	// records (its Dataset may be empty).
	Image api.ImageGeneration
	// OS is the guest family ("freebsd", "linux").
	OS string
	// Volume receives a part of kind volume; the service makes the volume. nil refuses volume parts.
	Volume func(ctx context.Context, part api.ExportPart, r io.Reader) error
}

// ImportResult is what an import did.
type ImportResult struct {
	Plan     []string
	Warnings []string
}

// partTarget is the dataset a part is received into, and the properties it gets the first time.
func (s *Service) partTarget(in ImportInput, p api.ExportPart) (string, map[string]string, error) {
	name := KeyOfJail(in.Jail)
	c := s.Cfg.at(name).containerDS()
	id := in.Jail.Metadata.UUID
	base := func(kind string, extra map[string]string) map[string]string {
		m := map[string]string{PropManaged: "on", PropID: id, PropKind: kind, PropName: in.Jail.Metadata.Name, PropProject: in.Jail.Metadata.Project}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	switch p.Kind {
	case api.PartRoot:
		if in.Jail.Spec.Layout != api.LayoutThick || p.Dataset != "root" {
			return "", nil, api.Errorf(api.CodeInvalid, "the file holds a root, and a thin jail is made from its image")
		}
		return c + "/root", base(KindRoot, map[string]string{"mountpoint": s.Cfg.at(name).rootMP(), "canmount": "on",
			PropGen: strconv.Itoa(in.Image.Number), PropImage: imageOf(in.Image), PropManifest: in.Image.Manifest}), nil
	case api.PartPersistent:
		if in.Jail.Spec.Layout == api.LayoutThick {
			return "", nil, api.Errorf(api.CodeInvalid, "a thick jail has no persistent datasets: the file does not match its layout")
		}
		// what a persistent dataset is on this host is decided here: the file names the dataset, not its place
		for _, pp := range s.Cfg.persistent() {
			if pp.Dataset == p.Dataset {
				final := s.Cfg.at(name).rootMP() + path.Clean(pp.Path)
				return c + "/" + pp.Dataset, base(KindPersistent, map[string]string{PropClass: string(pp.Class), PropPath: path.Clean(pp.Path),
					PropMerge: onOff(pp.Merge), "mountpoint": final}), nil
			}
		}
		return "", nil, api.Errorf(api.CodeInvalid, "this host has no persistent dataset %q: its configuration differs from the exporting host's", p.Dataset)
	}
	return "", nil, api.Errorf(api.CodeInvalid, "unknown kind %q of a part", p.Kind)
}

func imageOf(g api.ImageGeneration) string {
	n, _, _ := strings.Cut(g.Name, "#")
	return n
}

// KeyOfJail is the key of a jail value whose metadata is complete (the service checked the names).
func KeyOfJail(j api.Jail) string {
	k, _ := KeyOf(j)
	return k
}

// PlanImport builds the import plan. rd gives the parts of the file one after the other and returns io.EOF after the
// last (the reader has then verified the footer). recovering skips the existence check so that a plan can be rebuilt to
// roll an interrupted import back: only the first step has an undo, and it removes everything.
func (s *Service) PlanImport(ctx context.Context, in ImportInput, rd func() (api.ExportPart, io.Reader, error), recovering bool) (*plan.Plan, *ImportResult, error) {
	res := &ImportResult{}
	j := in.Jail
	key, err := KeyOf(j)
	if err != nil {
		return nil, res, err
	}
	if j.Metadata.UUID == "" {
		return nil, res, api.Errorf(api.CodeInvalid, "the import needs the uuid of the new jail")
	}
	str, err := streamsOf(s)
	if err != nil {
		return nil, res, err
	}
	z := s.ZFS
	c := s.Cfg.at(key).containerDS()
	if ok, err := z.Exists(ctx, c); err != nil {
		return nil, res, err
	} else if ok && !recovering {
		return nil, res, api.Errorf(api.CodeAlreadyExists, "jail %q already exists", j.Metadata.Name)
	}
	if ok, err := z.Exists(ctx, s.Cfg.at(key).jailsDS()); err != nil {
		return nil, res, err
	} else if !ok && !recovering {
		return nil, res, api.Errorf(api.CodeNotFound, "dataset %s does not exist; the project must be set up on this storage first", s.Cfg.at(key).jailsDS())
	}
	layout := j.Spec.Layout
	if layout == "" {
		return nil, res, api.Errorf(api.CodeInvalid, "the jail has no layout")
	}
	id := j.Metadata.UUID
	rootDS, rootMP, cMP := s.Cfg.at(key).rootDS(), s.Cfg.at(key).rootMP(), s.Cfg.at(key).containerMP()
	imageName := imageOf(in.Image)
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
	pl := plan.New("import-jail " + key)
	contProps := map[string]string{"mountpoint": cMP, PropLayout: string(layout), PropImage: imageName, PropComplete: "off",
		PropManaged: "on", PropID: id, PropKind: KindContainer, PropName: j.Metadata.Name, PropProject: j.Metadata.Project}
	if q := j.Spec.Resources.Disk; q != "" {
		contProps["quota"] = q
	}
	if r := j.Spec.Resources.DiskReservation; r != "" {
		contProps["reservation"] = r
	}
	pl.Add(plan.Step{Name: "create-container", Describe: fmt.Sprintf("zfs create -o mountpoint=%s %s", cMP, c),
		Do: func(ctx context.Context) error { return z.Create(ctx, c, contProps) }, Undo: owned})
	if layout == api.LayoutThin {
		genRO := in.Image.Dataset + "@" + roSnap
		rootProps := map[string]string{PropManaged: "on", PropID: id, PropKind: KindRoot, PropName: j.Metadata.Name, PropProject: j.Metadata.Project,
			"mountpoint": rootMP, "canmount": "on", PropGen: strconv.Itoa(in.Image.Number), PropImage: imageName, PropManifest: in.Image.Manifest}
		pl.Add(plan.Step{Name: "clone-root", Describe: fmt.Sprintf("zfs clone %s %s", genRO, rootDS),
			Do: func(ctx context.Context) error {
				if ok, _ := z.Exists(ctx, genRO); !ok {
					return api.Errorf(api.CodeNotFound, "generation %s has no @ro snapshot", in.Image.Name)
				}
				return z.Clone(ctx, genRO, rootDS, rootProps)
			}})
	}
	var received []string // snapshots the import received besides the baseline
	pl.Add(plan.Step{Name: "receive-streams", Describe: "zfs receive every part of the file into " + c,
		Do: func(ctx context.Context) error {
			seen := map[string]bool{}
			for {
				part, r, err := rd()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				if part.Kind == api.PartVolume {
					if in.Volume == nil {
						return api.Errorf(api.CodeInvalid, "the file holds volumes and this import was asked for none")
					}
					if err := in.Volume(ctx, part, r); err != nil {
						return fmt.Errorf("importing volume %s: %w", part.Dataset, err)
					}
					continue
				}
				target, props, err := s.partTarget(in, part)
				if err != nil {
					return err
				}
				if seen[target] != (part.From != "") {
					return api.Errorf(api.CodeInvalid, "the parts of %s are not in order", part.Dataset)
				}
				if part.From != "" {
					props = nil
				}
				if err := str.ReceiveStream(ctx, target, props, r); err != nil {
					return fmt.Errorf("receiving %s: %w", part.Dataset, err)
				}
				seen[target] = true
				if part.Snapshot != BaselineSnap {
					received = append(received, target+"@"+part.Snapshot)
				}
			}
			return nil
		}})
	if layout == api.LayoutThin {
		pl.Add(plan.Step{Name: "check-persistent", Describe: "every persistent dataset of this host is in the file",
			Do: func(ctx context.Context) error {
				for _, pp := range s.Cfg.persistent() {
					if ok, _ := z.Exists(ctx, c+"/"+pp.Dataset); !ok {
						return api.Errorf(api.CodeInvalid, "the file lacks the dataset %s of the jail", pp.Dataset)
					}
				}
				return nil
			}})
	} else {
		pl.Add(plan.Step{Name: "check-root", Describe: "the root of the thick jail is in the file",
			Do: func(ctx context.Context) error {
				if ok, _ := z.Exists(ctx, rootDS); !ok {
					return api.Errorf(api.CodeInvalid, "the file holds no root for a thick jail")
				}
				return nil
			}})
	}
	pl.Add(plan.Step{Name: "drop-received-snapshots", Describe: "zfs destroy the snapshots the export left on the new datasets",
		Do: func(ctx context.Context) error {
			if err := s.destroySnaps(received)(ctx); err != nil {
				res.Warnings = append(res.Warnings, err.Error())
			}
			return nil
		}})
	if in.OS == string(api.OSLinux) || j.Spec.OS == api.OSLinux {
		shm := path.Join(rootMP, render.LinuxShmBacking)
		pl.Add(plan.Step{Name: "create-linux-shm", Describe: "mkdir -p " + shm,
			Do: func(ctx context.Context) error {
				if err := z.Mount(ctx, rootDS); err != nil {
					return err
				}
				if err := s.Files.MkdirAll(ctx, path.Join(rootMP, render.LinuxCompat)); err != nil {
					return err
				}
				return s.Files.MkdirAll(ctx, shm)
			}})
	}
	if j.Spec.Delegate {
		ds := s.Cfg.at(key).delegatedDS()
		pl.Add(plan.Step{Name: "create-delegated", Describe: "zfs create " + ds,
			Do: func(ctx context.Context) error {
				return z.Create(ctx, ds, map[string]string{PropManaged: "on", PropID: id, PropKind: KindDelegated, PropName: j.Metadata.Name,
					PropProject: j.Metadata.Project, "mountpoint": "none", "canmount": "noauto"})
			}},
			plan.Step{Name: "delegate-dataset", Describe: "zfs set jailed=on " + ds,
				Do: func(ctx context.Context) error { return z.Set(ctx, ds, map[string]string{"jailed": "on"}) }})
	}
	pl.Add(plan.Step{Name: "write-records", Describe: "write spec.json and bundle.json of the new jail",
		Do: func(ctx context.Context) error { return s.WriteRecords(ctx, key, j, in.Image) }},
		plan.Step{Name: "mark-complete", Describe: "zfs set jailops:complete=on " + c,
			Do: func(ctx context.Context) error { return z.Set(ctx, c, map[string]string{PropComplete: "on"}) }})
	return pl, res, nil
}

// ImportJail makes the jail of in from the parts rd gives. A failure at any step removes what the import made.
func (s *Service) ImportJail(ctx context.Context, in ImportInput, rd func() (api.ExportPart, io.Reader, error)) (ImportResult, error) {
	p, res, err := s.PlanImport(ctx, in, rd, false)
	if err != nil {
		return *res, err
	}
	res.Plan = p.Describe()
	key := KeyOfJail(in.Jail)
	err = s.run(ctx, "import", key, p)
	return *res, err
}

// RecoverImport removes what an import that did not finish made (a daemon that died in the middle of it). The jail is
// only touched when it carries the uuid of the run.
func (s *Service) RecoverImport(ctx context.Context, key, uuid string) (bool, error) {
	c := s.Cfg.at(key).containerDS()
	ok, err := s.ZFS.Exists(ctx, c)
	if err != nil || !ok {
		return false, err
	}
	if s.prop(ctx, c, PropID) != uuid || s.prop(ctx, c, PropComplete) == "on" {
		return false, nil
	}
	return true, s.ZFS.DestroyRecursive(ctx, c)
}

// ReceiveVolume makes a volume of the project from a stream of an export: the dataset gets the properties of a volume
// the way CreateVolume makes them, and the stream's data. An encrypted volume arrives as it was stored (a raw stream):
// the same passphrase protects it, and its key is not loaded, so it stays locked until someone unlocks it. The caller
// registers the volume and undoes this (DestroyVolume) when a later step fails.
func (s *Service) ReceiveVolume(ctx context.Context, r VolumeRequest, rd io.Reader) error {
	if err := s.validateVolume(r); err != nil {
		return err
	}
	str, err := streamsOf(s)
	if err != nil {
		return err
	}
	z := s.ZFS
	parent := ProjectVolumesDS(r.Storage, r.Project)
	ds := VolumeDS(r.Storage, r.Project, r.Name)
	if ok, err := z.Exists(ctx, ds); err != nil {
		return err
	} else if ok {
		return api.Errorf(api.CodeAlreadyExists, "volume %q already exists", r.Name)
	}
	if ok, err := z.Exists(ctx, parent); err != nil {
		return err
	} else if !ok {
		if err := z.Create(ctx, parent, map[string]string{PropManaged: "on", PropKind: KindVolumes, PropProject: r.Project, "canmount": "off", "mountpoint": "none"}); err != nil {
			return err
		}
	}
	props := s.volumeProps(r)
	if r.Encrypted {
		props[PropEncrypted] = "on"
	}
	if err := str.ReceiveStream(ctx, ds, props, rd); err != nil {
		_ = s.destroyIfExists(ds)(context.WithoutCancel(ctx))
		return err
	}
	// the snapshot the export took is not the volume's own history
	snaps, _ := z.Snapshots(ctx, ds)
	var drop []string
	for _, sn := range snaps {
		drop = append(drop, ds+"@"+sn)
	}
	return s.destroySnaps(drop)(ctx)
}
