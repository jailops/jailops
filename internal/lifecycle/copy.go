package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// CopyResult is the outcome of CopyJail.
type CopyResult struct {
	Plan     []string
	Warnings []string
}

// CopyJail makes an independent copy of a jail with a full send and receive of
// every dataset (design 4.5). It is never a clone: a clone would pin the
// source's snapshots. The copy gets a new name and uuid, and its fixed
// addresses and MACs are reset to "auto", because two jails must not share an
// address. stamp names the temporary snapshot (empty means "now").
func (s *Service) CopyJail(ctx context.Context, src, dst, newUUID, stamp string, dryRun bool) (CopyResult, error) {
	return s.CopyJailFrom(ctx, src, dst, newUUID, stamp, "", dryRun)
}

// CopyJailFrom is CopyJail from a snapshot of the source: snapshot names a snapshot every dataset of the source has
// (see SnapshotJail); empty takes a new, temporary one.
func (s *Service) CopyJailFrom(ctx context.Context, src, dst, newUUID, stamp, snapshot string, dryRun bool) (CopyResult, error) {
	p, res, err := s.PlanCopyFrom(ctx, src, dst, newUUID, stamp, snapshot, false)
	if err != nil {
		return *res, err
	}
	res.Plan = p.Describe()
	if dryRun {
		return *res, nil
	}
	err = s.run(ctx, "copy", dst, p)
	return *res, err
}

// PlanCopy builds the copy plan; recovering skips the destination existence check.
func (s *Service) PlanCopy(ctx context.Context, src, dst, newUUID, stamp string, recovering bool) (*plan.Plan, *CopyResult, error) {
	return s.PlanCopyFrom(ctx, src, dst, newUUID, stamp, "", recovering)
}

// PlanCopyFrom builds the copy plan from snapshot (empty: a new temporary snapshot of the source).
func (s *Service) PlanCopyFrom(ctx context.Context, src, dst, newUUID, stamp, snapshot string, recovering bool) (*plan.Plan, *CopyResult, error) {
	res := &CopyResult{}
	if err := validName("jail", dst); err != nil {
		return nil, res, err
	}
	if newUUID == "" {
		return nil, res, api.Errorf(api.CodeInvalid, "the proposer must supply the copy's uuid")
	}
	st, err := s.own(ctx, src)
	if err != nil {
		return nil, res, err
	}
	z := s.ZFS
	if ok, err := z.Exists(ctx, s.Cfg.at(dst).containerDS()); err != nil {
		return nil, res, err
	} else if ok && !recovering {
		return nil, res, api.Errorf(api.CodeAlreadyExists, "jail %q already exists", dst)
	}
	rec, err := s.readRecord(ctx, src)
	if err != nil {
		return nil, res, fmt.Errorf("read spec.json: %w", err)
	}
	snap := "jo-copy-" + s.stamp(stamp)
	own := snapshot == "" // the snapshot is the copy's own: it is taken and dropped by the plan
	if !own {
		if err := validate.SnapshotName("snapshot", snapshot); err != nil {
			return nil, res, err
		}
		snap = snapshot
	}
	dstProject, dstName := splitKey(dst)
	dc := s.Cfg.at(dst).containerDS()
	dstRoot, dstMP := s.Cfg.at(dst).rootMP(), s.Cfg.at(dst).containerMP()

	type item struct {
		from, to string
		props    map[string]string
	}
	// What the copy's datasets are told is explicit: a plain send carries no properties, so the kind and the image of
	// each dataset are read from the source and set on the copy.
	with := func(ds string, over map[string]string) map[string]string {
		m := s.copyProps(ctx, ds)
		m[PropID], m[PropName], m[PropProject] = newUUID, dstName, dstProject
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	items := []item{{st.Container, dc, with(st.Container, map[string]string{"mountpoint": dstMP, PropComplete: "off"})}}
	items = append(items, item{st.Root, dc + "/root", with(st.Root, map[string]string{"mountpoint": dstRoot})})
	if st.Delegated != "" {
		items = append(items, item{st.Delegated, s.Cfg.at(dst).delegatedDS(), with(st.Delegated, map[string]string{"mountpoint": "none"})})
	}
	srcSnaps := make([]string, len(st.all()))
	for i, d := range st.all() {
		srcSnaps[i] = d + "@" + snap
		if !own {
			if ok, err := z.Exists(ctx, srcSnaps[i]); err != nil {
				return nil, res, err
			} else if !ok {
				return nil, res, api.Errorf(api.CodeNotFound, "jail %s has no snapshot %q on %s", src, snapshot, d).
					WithHint("jailops jail snapshot list " + src)
			}
		}
	}
	owned := func(ctx context.Context) error {
		ok, err := z.Exists(ctx, dc)
		if err != nil || !ok {
			return err
		}
		if s.prop(ctx, dc, PropID) != newUUID {
			return nil
		}
		return z.DestroyRecursive(ctx, dc)
	}
	pl := plan.New("copy-jail " + src + " " + dst)
	if own {
		pl.Add(plan.Step{Name: "snapshot", Describe: "zfs snapshot " + strings.Join(srcSnaps, " ") + "  (one atomic transaction)",
			Do:   func(ctx context.Context) error { return z.Snapshot(ctx, nil, srcSnaps...) },
			Undo: s.destroySnaps(srcSnaps)})
	}
	for i, it := range items {
		it := it
		send := "zfs send -p"
		pl.Add(plan.Step{Name: "send-" + it.to[strings.LastIndex(it.to, "/")+1:],
			Describe: fmt.Sprintf("%s %s@%s | zfs receive %s", send, it.from, snap, it.to),
			Do: func(ctx context.Context) error {
				// UNVERIFIED: send of a clone gives a full, independent stream.
				return z.Replicate(ctx, it.from+"@"+snap, it.to, it.props)
			},
			Undo: owned, UndoOnFail: i == 0})
	}
	if st.Delegated != "" {
		// the helper receives without the jailed property: the copy's dataset is handed to its jail again
		dd := s.Cfg.at(dst).delegatedDS()
		pl.Add(plan.Step{Name: "delegate-dataset", Describe: "zfs set jailed=on " + dd,
			Do: func(ctx context.Context) error { return z.Set(ctx, dd, map[string]string{"jailed": "on"}) }})
	}
	// rewrite the records for the copy
	nrec := rec
	nrec.Metadata.Name, nrec.Metadata.Project, nrec.Metadata.UUID = dstName, dstProject, newUUID
	nrec.Metadata.Generation = 1
	nrec.Metadata.CreatedAt, nrec.Metadata.UpdatedAt = s.Cfg.now(), s.Cfg.now()
	// A copy attaches no volume: a volume belongs to the jails it is attached to, and two jails writing one volume
	// by accident is not what a copy should start with. Attach what the copy needs with `jailops volume attach`.
	nrec.Spec.Volumes = nil
	// ... and takes over no public port: the host port of an IPv4 forward belongs to the source's exposure.
	nrec.Spec.Expose = nil
	nrec.Spec.Network = append([]api.NetworkAttach(nil), rec.Spec.Network...)
	for i := range nrec.Spec.Network {
		n := &nrec.Spec.Network[i]
		// an address the source has gets a new one; a family it has none of stays without
		if n.IPv4 != "none" && n.IPv4 != "" {
			n.IPv4 = "auto"
		}
		if n.IPv6 != "none" && n.IPv6 != "" {
			n.IPv6 = "auto"
		}
		n.MAC = ""
	}
	if err := checkJSON(nrec); err != nil {
		return nil, res, err
	}
	specB := marshal(nrec)
	bundleB := s.bundleBytes(nrec, rec.Spec.Image, st.Image, st.Gen, st.Manifest, "", s.Cfg.now())
	pl.Add(plan.Step{Name: "mount-container", Describe: "zfs mount " + dc, Do: func(ctx context.Context) error { return z.Mount(ctx, dc) }},
		plan.Step{Name: "write-spec", Describe: "write " + s.Cfg.at(dst).specPath(), Do: func(ctx context.Context) error { return s.Files.WriteAtomic(ctx, s.Cfg.at(dst).specPath(), specB) }},
		plan.Step{Name: "write-bundle", Describe: "write " + s.Cfg.at(dst).bundlePath(), Do: func(ctx context.Context) error { return s.Files.WriteAtomic(ctx, s.Cfg.at(dst).bundlePath(), bundleB) }},
		plan.Step{Name: "mark-complete", Describe: "zfs set jailops:complete=on " + dc,
			Do: func(ctx context.Context) error { return z.Set(ctx, dc, map[string]string{PropComplete: "on"}) }})
	// the temporary snapshots are no longer needed; failures are warnings. A snapshot the user made stays on the source
	// and is dropped from the copy only.
	pl.Add(plan.Step{Name: "drop-temporary-snapshots", Describe: "zfs destroy the " + snap + " snapshots on " + map[bool]string{true: "source and copy", false: "the copy"}[own],
		Do: func(ctx context.Context) error {
			var all []string
			if own {
				all = append(all, srcSnaps...)
			}
			for _, it := range items {
				all = append(all, it.to+"@"+snap)
			}
			if err := s.destroySnaps(all)(ctx); err != nil {
				res.Warnings = append(res.Warnings, err.Error())
			}
			return nil
		}})
	return pl, res, nil
}

func checkJSON(v any) error {
	_, err := json.Marshal(v)
	return err
}

// copyPropNames are the properties of a jail's dataset that a copy keeps: what JailOps reads back (managed, kind,
// image, generation, the installed release) and the limits set on it.
var copyPropNames = []string{PropManaged, PropKind, PropImage, PropGen, PropManifest, PropFreeBSDVersion,
	"quota", "refquota", "reservation", "readonly", "compression", "recordsize", "atime"}

func (s *Service) copyProps(ctx context.Context, ds string) map[string]string {
	out := map[string]string{}
	for _, p := range copyPropNames {
		if v := s.prop(ctx, ds, p); v != "" {
			out[p] = v
		}
	}
	return out
}
