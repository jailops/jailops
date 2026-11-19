package lifecycle

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// ConvertRequest is api.JailConvert plus a Stamp that names the plan's snapshot. Persist the request with the journal
// record and pass the same Stamp (and State as Resume) when rebuilding the plan to roll an interrupted run back.
type ConvertRequest struct {
	api.JailConvert
	Stamp  string
	Resume *ConvertState
}

// ConvertState is what a plan needs that the pool no longer shows once it has started. Persist it with the journal
// record.
type ConvertState struct {
	From       api.Layout `json:"from"`
	Gen        int        `json:"gen"`
	Manifest   string     `json:"manifest"`
	WasRunning bool       `json:"was_running"`
	// Datasets are the datasets the snapshot covers; Persistent the persistent datasets thin to thick retires.
	Datasets   []string         `json:"datasets"`
	Persistent []ConvertPersist `json:"persistent,omitempty"`
	// Spec and Bundle are spec.json and bundle.json before the conversion, restored by undo.
	Spec   []byte `json:"spec,omitempty"`
	Bundle []byte `json:"bundle,omitempty"`
}

// ConvertPersist is a persistent dataset of the thin jail and the path inside the jail it holds.
type ConvertPersist struct {
	DS   string `json:"ds"`
	Path string `json:"path"`
}

// ConvertResult is the outcome of a conversion.
type ConvertResult struct {
	api.JailConvertResult
	// Snapshot is the atomic snapshot taken before the conversion.
	Snapshot string
	// Stamp names the plan's snapshot; State must be persisted with the journal record.
	Stamp string
	State ConvertState
}

// ConvertJail changes the layout of a FreeBSD jail's root. Thin to thick makes the root a full copy of its own with
// the persistent datasets folded in; thick to thin gives the jail a clone of an image generation as its root and moves
// the directories of the persistent paths into datasets. The old root stays as root.old for the retention period.
func (s *Service) ConvertJail(ctx context.Context, name string, req ConvertRequest) (ConvertResult, error) {
	p, res, err := s.PlanConvert(ctx, name, req)
	if err != nil {
		return *res, err
	}
	if p == nil || req.DryRun {
		return *res, nil
	}
	if err = s.run(ctx, "convert", name, p); err != nil {
		return *res, err
	}
	s.FinishConvert(ctx, name, res)
	return *res, nil
}

// FinishConvert is the best-effort work after a successful conversion: drop a parked root.old of an earlier switch and
// stop the jail again if it was stopped before. Failures become warnings. A caller that runs the plan itself must call
// it after the plan succeeds.
func (s *Service) FinishConvert(ctx context.Context, name string, res *ConvertResult) {
	ur := UpgradeResult{Stamp: res.Stamp, State: SwitchState{WasRunning: res.State.WasRunning}}
	s.FinishSwitch(ctx, name, &ur)
	res.Warnings = append(res.Warnings, ur.Warnings...)
}

// PlanConvert runs the preflight and builds the plan. The plan is nil when the jail has the layout already.
func (s *Service) PlanConvert(ctx context.Context, name string, req ConvertRequest) (*plan.Plan, *ConvertResult, error) {
	res := &ConvertResult{JailConvertResult: api.JailConvertResult{Jail: name, To: req.Layout, DryRun: req.DryRun}}
	if req.Layout != api.LayoutThin && req.Layout != api.LayoutThick {
		return nil, res, api.Errorf(api.CodeInvalid, "bad layout %q: thin or thick", req.Layout)
	}
	st, err := s.ready(ctx, name)
	if err != nil {
		return nil, res, err
	}
	if req.Resume == nil {
		// the conversion ends with a start and a health check: a locked jail cannot have either
		if err := s.refuseLocked(ctx, st); err != nil {
			return nil, res, err
		}
	} else {
		cp := *st
		cp.Layout, cp.Gen, cp.Manifest = req.Resume.From, req.Resume.Gen, req.Resume.Manifest
		st = &cp
	}
	res.From = st.Layout
	rec, err := s.readRecord(ctx, name)
	if err != nil {
		return nil, res, fmt.Errorf("read spec.json: %w", err)
	}
	osName := string(rec.Spec.OS)
	if g, gerr := s.Images.Resolve(ctx, s.Cfg.at(name).st.Root, genName(st.Image, st.Gen)); gerr == nil {
		if v := s.prop(ctx, g.Dataset, PropOS); v != "" {
			osName = v
		}
	}
	if osName == string(api.OSLinux) {
		return nil, res, api.Errorf(api.CodeUnsupported, "a Linux jail is thick and stays so: only FreeBSD jails change layout")
	}
	if req.Resume == nil && st.Layout == req.Layout {
		return nil, res, nil
	}
	res.Changed = true
	if req.Resume == nil {
		if st.hasRootNew() || st.hasRootStale() {
			return nil, res, api.Errorf(api.CodeBusy, "%s or %s exists from an interrupted operation", s.Cfg.at(name).rootNewDS(), s.Cfg.at(name).rootStaleDS()).WithHint("run recovery or cleanup first")
		}
		if q := s.prop(ctx, st.Container, "quota"); q != "" && q != "none" && q != "0" {
			res.Warnings = append(res.Warnings, "the disk limit of the jail ("+q+") counts the new root next to the old one until the conversion ends")
		}
	}
	wasRunning := false
	if req.Resume != nil {
		wasRunning = req.Resume.WasRunning
	} else if wasRunning, err = s.Jails.Running(ctx, name); err != nil {
		return nil, res, err
	}
	stamp := s.stamp(req.Stamp)
	res.Stamp, res.Snapshot = stamp, "jo-pre-convert-"+stamp
	datasets := st.all()
	var persistent []ConvertPersist
	for _, d := range st.Persist {
		persistent = append(persistent, ConvertPersist{DS: d.DS, Path: d.Path})
	}
	if req.Resume != nil {
		datasets, persistent = req.Resume.Datasets, req.Resume.Persistent
	}
	res.State = ConvertState{From: st.Layout, Gen: st.Gen, Manifest: st.Manifest, WasRunning: wasRunning, Datasets: datasets, Persistent: persistent}

	var oldSpec, oldBundle []byte
	if req.Resume != nil {
		oldSpec, oldBundle = req.Resume.Spec, req.Resume.Bundle
	} else {
		oldSpec, _ = s.Files.ReadFile(ctx, s.Cfg.at(name).specPath())
		if oldSpec == nil {
			oldSpec = marshal(rec)
		}
		if b, berr := s.Files.ReadFile(ctx, s.Cfg.at(name).bundlePath()); berr == nil {
			oldBundle = b
		} else {
			oldBundle = s.bundleBytes(rec, rec.Spec.Image, st.Image, st.Gen, st.Manifest, "", s.Cfg.now())
		}
	}
	res.State.Spec, res.State.Bundle = oldSpec, oldBundle

	pl := plan.New("convert-jail " + name)
	snaps := make([]string, len(datasets))
	for i, d := range datasets {
		snaps[i] = d + "@" + res.Snapshot
	}
	z := s.ZFS
	pl.Add(plan.Step{Name: "snapshot", Describe: "zfs snapshot " + strings.Join(snaps, " ") + "  (one atomic transaction)",
		Do: func(ctx context.Context) error {
			return z.Snapshot(ctx, map[string]string{PropGen: strconv.Itoa(st.Gen), PropImage: st.Image, PropComment: "before converting to " + string(req.Layout)}, snaps...)
		},
		Undo: s.destroySnaps(snaps)})
	pl.Add(plan.Step{Name: "stop", Describe: "jail -r " + name + ", unmount (was running: " + strconv.FormatBool(wasRunning) + ")",
		Do: func(ctx context.Context) error { return s.doStop(ctx, name) },
		Undo: func(ctx context.Context) error {
			if !wasRunning {
				return nil
			}
			return s.doStart(ctx, name)
		}, UndoOnFail: true})
	pl.Add(plan.Step{Name: "mount-old", Describe: "zfs mount root and persistent datasets of " + name,
		Do:   func(ctx context.Context) error { return s.mountTree(ctx, name) },
		Undo: func(ctx context.Context) error { return s.unmountTree(ctx, name) }, UndoOnFail: true})

	var target api.ImageGeneration
	if req.Layout == api.LayoutThick {
		if err := s.planToThick(st, res, pl, persistent); err != nil {
			return nil, res, err
		}
		target = api.ImageGeneration{Number: st.Gen, Manifest: st.Manifest, Name: genName(st.Image, st.Gen)}
		res.Image = target.Name
	} else {
		if target, err = s.convertTarget(ctx, st, req); err != nil {
			return nil, res, err
		}
		res.Image = target.Name
		s.planToThin(st, rec, target, res, pl)
	}
	newRec := rec
	newRec.Spec.Layout = req.Layout
	newRec.Metadata.UpdatedAt = s.Cfg.now()
	pl.Add(s.convertRecordsStep(st, newRec, req.Layout, oldSpec, oldBundle, target))
	pl.Add(plan.Step{Name: "start", Describe: "mount, render and jail -c " + name + " with the " + string(req.Layout) + " root",
		Do:   func(ctx context.Context) error { return s.doStart(ctx, name) },
		Undo: func(ctx context.Context) error { return s.doStop(ctx, name) }, UndoOnFail: true})
	pl.Add(s.healthStep(name, rec, "after the conversion"))
	pl.Add(s.scheduleCleanupStep(name))
	res.Steps = pl.Describe()
	return pl, res, nil
}

// convertTarget is the generation a jail converted to thin takes its root from: the one the thick root was copied
// from, or the generation the request names. It is a generation of the jail's own image.
func (s *Service) convertTarget(ctx context.Context, st *jailState, req ConvertRequest) (api.ImageGeneration, error) {
	root := s.Cfg.at(st.Name).st.Root
	var target api.ImageGeneration
	var err error
	if g := req.Image; g != "" {
		if genArgRe.MatchString(g) {
			g = st.Image + "#" + g
		}
		target, err = s.Images.Ensure(ctx, root, g)
	} else {
		target, err = s.Images.Resolve(ctx, root, genName(st.Image, st.Gen))
		if err != nil {
			return target, api.Errorf(api.CodeNotFound, "the image generation %s of the root of %s is gone (%v)", genName(st.Image, st.Gen), st.Name, err).WithHint("name an image generation to convert onto")
		}
	}
	if err != nil {
		return target, err
	}
	if img, _, _ := strings.Cut(target.Name, "#"); img != st.Image {
		return target, api.Errorf(api.CodeInvalid, "jail %s uses image %s; it cannot convert onto %s", st.Name, st.Image, target.Name)
	}
	if s.prop(ctx, target.Dataset, PropOS) == string(api.OSLinux) {
		return target, api.Errorf(api.CodeUnsupported, "a Linux jail is thick and stays so")
	}
	if ok, _ := s.ZFS.Exists(ctx, target.Dataset+"@"+roSnap); !ok {
		return target, api.Errorf(api.CodeNotFound, "generation %s has no @ro snapshot", target.Name)
	}
	return target, nil
}

// planToThick adds the steps that copy the root into root.new and fold the persistent datasets into the copy, swap it
// in and put the datasets away below root.old.
func (s *Service) planToThick(st *jailState, res *ConvertResult, pl *plan.Plan, persistent []ConvertPersist) error {
	name := st.Name
	z := s.ZFS
	p := s.Cfg.at(name)
	rootMP, newMP, newDS := p.rootMP(), p.rootNewMP(), p.rootNewDS()
	rootSnap := st.Root + "@" + res.Snapshot
	props := map[string]string{PropManaged: "on", PropID: st.ID, PropKind: KindRoot, PropName: name,
		"mountpoint": newMP, "canmount": "noauto", PropGen: strconv.Itoa(st.Gen), PropImage: st.Image, PropManifest: st.Manifest}
	pl.Add(plan.Step{Name: "copy-root", Describe: fmt.Sprintf("zfs send %s | zfs receive %s", rootSnap, newDS),
		Do:   func(ctx context.Context) error { return z.Replicate(ctx, rootSnap, newDS, props) },
		Undo: s.destroyIfExists(newDS)})
	pl.Add(plan.Step{Name: "drop-copied-snapshot", Describe: "zfs destroy " + newDS + "@" + res.Snapshot,
		Do: func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, newDS+"@"+res.Snapshot); !ok {
				return nil
			}
			return z.Destroy(ctx, newDS+"@"+res.Snapshot)
		}})
	pl.Add(plan.Step{Name: "mount-new", Describe: "zfs mount " + newDS,
		Do: func(ctx context.Context) error { return z.Mount(ctx, newDS) },
		Undo: func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, newDS); !ok {
				return nil
			}
			return z.Unmount(ctx, newDS)
		}, UndoOnFail: true})
	for _, d := range persistent {
		d := d
		from, to := path.Join(rootMP, d.Path), path.Join(newMP, d.Path)
		res.Kept = append(res.Kept, d.Path)
		pl.Add(plan.Step{Name: "fold-" + path.Base(d.DS), Describe: fmt.Sprintf("replace %s with the content of %s", to, from),
			Do: func(ctx context.Context) error {
				// the directory of the copy holds what the image had below the mount point: the dataset's content replaces it
				if err := s.Files.RemoveAll(ctx, to); err != nil {
					return err
				}
				if err := s.Files.MkdirAll(ctx, to); err != nil {
					return err
				}
				return s.Files.CopyTree(ctx, from, to)
			}})
	}
	pl.Add(plan.Step{Name: "unmount", Describe: "zfs unmount the old and the new root trees",
		Do: func(ctx context.Context) error {
			if err := z.Unmount(ctx, newDS); err != nil {
				return err
			}
			return s.unmountTree(ctx, name)
		}})
	pl.Add(s.swapSteps(st)...)
	oldDS := p.rootOldDS()
	for _, pd := range persistent {
		ds := pd.DS
		kept := oldDS + "/" + path.Base(ds)
		pl.Add(plan.Step{Name: "retire-" + path.Base(ds), Describe: fmt.Sprintf("zfs set canmount=noauto jailops:kind=retired %s; zfs rename %s %s", ds, ds, kept),
			Do: func(ctx context.Context) error {
				if err := z.Set(ctx, ds, map[string]string{"canmount": "noauto", PropKind: "retired"}); err != nil {
					return err
				}
				return z.Rename(ctx, ds, kept)
			},
			Undo: func(ctx context.Context) error {
				cur := ds
				if ok, _ := z.Exists(ctx, kept); ok {
					if ok, _ := z.Exists(ctx, ds); !ok {
						if err := z.Rename(ctx, kept, ds); err != nil {
							return err
						}
					}
				}
				if ok, _ := z.Exists(ctx, cur); !ok {
					return nil
				}
				if err := z.Set(ctx, cur, map[string]string{PropKind: KindPersistent}); err != nil {
					return err
				}
				return z.Set(ctx, cur, map[string]string{"canmount": "on"})
			}, UndoOnFail: true})
	}
	return nil
}

// planToThin adds the steps that clone the new root, make the persistent datasets from the directories of the thick
// root, swap the root in and turn the datasets on.
func (s *Service) planToThin(st *jailState, rec Record, target api.ImageGeneration, res *ConvertResult, pl *plan.Plan) {
	name := st.Name
	z := s.ZFS
	p := s.Cfg.at(name)
	rootMP, cMP, c := p.rootMP(), p.containerMP(), p.containerDS()
	pl.Add(s.cloneNewRoot(st, rec, target))
	var made []string
	for _, pp := range s.Cfg.persistent() {
		pp := pp
		ds := c + "/" + pp.Dataset
		made = append(made, ds)
		final := rootMP + path.Clean(pp.Path)
		stage := cMP + "/.seed-" + pp.Dataset
		res.Kept = append(res.Kept, path.Clean(pp.Path))
		// noauto: the dataset is mounted by hand until the root is the new one; a mount at its final path now would
		// hide the directory of the old root
		props := map[string]string{PropManaged: "on", PropID: st.ID, PropKind: KindPersistent, PropName: name, PropClass: string(pp.Class),
			PropPath: path.Clean(pp.Path), PropMerge: onOff(pp.Merge), "canmount": "noauto", "mountpoint": stage}
		pl.Add(plan.Step{Name: "create-" + pp.Dataset, Describe: fmt.Sprintf("zfs create -o mountpoint=%s -o canmount=noauto %s", stage, ds),
			Do:   func(ctx context.Context) error { return z.Create(ctx, ds, props) },
			Undo: s.destroyIfExists(ds)})
		pl.Add(plan.Step{Name: "seed-" + pp.Dataset, Describe: fmt.Sprintf("copy %s%s into %s, then set mountpoint %s", rootMP, pp.Path, ds, final),
			Do: func(ctx context.Context) error {
				if err := s.Files.MkdirAll(ctx, stage); err != nil {
					return err
				}
				if err := z.Mount(ctx, ds); err != nil {
					return err
				}
				if ok, err := s.Files.Exists(ctx, final); err != nil {
					return err
				} else if ok {
					if err := s.Files.CopyTree(ctx, final, stage); err != nil {
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
			},
			Undo: func(ctx context.Context) error {
				if ok, _ := z.Exists(ctx, ds); ok {
					_ = z.Unmount(ctx, ds)
				}
				return s.Files.RemoveAll(ctx, stage)
			}, UndoOnFail: true})
		pl.Add(plan.Step{Name: "baseline-" + pp.Dataset, Describe: "zfs snapshot " + ds + "@" + BaselineSnap,
			Do: func(ctx context.Context) error { return z.Snapshot(ctx, nil, ds+"@"+BaselineSnap) }})
	}
	res.Replaced = fmt.Sprintf("everything outside %s: the system comes from %s", strings.Join(res.Kept, ", "), target.Name)
	pl.Add(plan.Step{Name: "unmount", Describe: "zfs unmount the old root tree",
		Do: func(ctx context.Context) error { return s.unmountTree(ctx, name) }})
	pl.Add(s.swapSteps(st)...)
	pl.Add(plan.Step{Name: "enable-data", Describe: "mount the new root, then zfs set canmount=on on the persistent datasets",
		Do: func(ctx context.Context) error {
			if err := z.Mount(ctx, p.rootDS()); err != nil {
				return err
			}
			for _, ds := range made {
				if err := z.Mount(ctx, ds); err != nil {
					return err
				}
				// canmount cannot be inherited: it is set
				if err := z.Set(ctx, ds, map[string]string{"canmount": "on"}); err != nil {
					return err
				}
			}
			return nil
		},
		Undo: func(ctx context.Context) error {
			for i := len(made) - 1; i >= 0; i-- {
				if ok, _ := z.Exists(ctx, made[i]); !ok {
					continue
				}
				_ = z.Unmount(ctx, made[i])
				if err := z.Set(ctx, made[i], map[string]string{"canmount": "noauto"}); err != nil {
					return err
				}
			}
			if ok, _ := z.Exists(ctx, p.rootDS()); ok {
				_ = z.Unmount(ctx, p.rootDS())
			}
			return nil
		}, UndoOnFail: true})
}

// convertRecordsStep flips the layout in the container's property, spec.json and bundle.json together.
func (s *Service) convertRecordsStep(st *jailState, newRec Record, to api.Layout, oldSpec, oldBundle []byte, target api.ImageGeneration) plan.Step {
	name := st.Name
	pth := s.Cfg.at(name)
	from := st.Layout
	newSpec := marshal(newRec)
	ds := ""
	if to == api.LayoutThin {
		ds = target.Dataset
	}
	newBundle := s.bundleBytes(newRec, newRec.Spec.Image, st.Image, target.Number, target.Manifest, ds, s.Cfg.now())
	set := func(ctx context.Context, l api.Layout, spec, bundle []byte) error {
		if err := s.ZFS.Set(ctx, st.Container, map[string]string{PropLayout: string(l)}); err != nil {
			return err
		}
		if err := s.Files.WriteAtomic(ctx, pth.specPath(), spec); err != nil {
			return err
		}
		return s.Files.WriteAtomic(ctx, pth.bundlePath(), bundle)
	}
	return plan.Step{Name: "set-layout", Describe: fmt.Sprintf("zfs set %s=%s %s; write spec.json and bundle.json", PropLayout, to, st.Container),
		Do:   func(ctx context.Context) error { return set(ctx, to, newSpec, newBundle) },
		Undo: func(ctx context.Context) error { return set(ctx, from, oldSpec, oldBundle) }, UndoOnFail: true}
}
