package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// RollbackOptions is api.JailRollback plus the force flag (destroy later
// snapshots) and a Stamp, see UpgradeRequest.
type RollbackOptions struct {
	api.JailRollback
	// Force allows destroying snapshots newer than the target and skipping
	// datasets that do not have the snapshot.
	Force bool
	Stamp string
	// Resume: see UpgradeRequest.Resume. Only scope root supports crash
	// recovery; data and all are forward-only and are retried instead.
	Resume *SwitchState
}

// RollbackResult is the outcome of RollbackJail. Written is filled even when
// the rollback is refused, so a caller can show what would be lost.
type RollbackResult struct {
	Scope        api.RollbackScope
	Plan         []string
	Written      map[string]int64
	TotalWritten int64
	Skipped      []string
	// Upgrade is set for scope root, which is a switch to the previous generation.
	Upgrade *UpgradeResult
}

// RollbackJail implements design 4.4.
//
//	root (default): swap the root back to the previous generation. Data is not
//	  touched. This is the same plan as an upgrade, walking to the older
//	  generation, so the /etc merge, snapshot and health check apply.
//	data: zfs rollback of the data datasets (not /etc, not the root).
//	all: both, plus the system datasets.
//
// data and all need ConfirmDataLoss and report what `written` says would be
// lost. They refuse when snapshots newer than the target exist, unless Force.
// Data is never rolled back automatically.
func (s *Service) RollbackJail(ctx context.Context, name string, req RollbackOptions, dryRun bool) (RollbackResult, error) {
	res := RollbackResult{Scope: req.Scope}
	if res.Scope == "" {
		res.Scope = api.RollbackRoot
	}
	st, err := s.ready(ctx, name)
	if err != nil {
		return res, err
	}
	switch res.Scope {
	case api.RollbackRoot:
		return s.rollbackRoot(ctx, st, req, dryRun, res)
	case api.RollbackData, api.RollbackAll:
		return s.rollbackData(ctx, st, req, dryRun, res)
	}
	return res, api.Errorf(api.CodeInvalid, "unknown rollback scope %q", req.Scope)
}

func (s *Service) rollbackRoot(ctx context.Context, st *jailState, req RollbackOptions, dryRun bool, res RollbackResult) (RollbackResult, error) {
	if req.Snapshot != "" {
		return res, api.Errorf(api.CodeInvalid, "scope root returns to the previous generation; use scope all with a snapshot to restore a point in time")
	}
	if st.Layout != api.LayoutThin {
		return res, api.Errorf(api.CodeUnsupported, "root rollback applies to thin jails")
	}
	if st.Prev == 0 {
		return res, api.Errorf(api.CodeNotFound, "jail %s has no previous generation", st.Name)
	}
	rec, err := s.readRecord(ctx, st.Name)
	if err != nil {
		return res, fmt.Errorf("read spec.json: %w", err)
	}
	target, err := s.Images.Resolve(ctx, s.Cfg.at(st.Name).st.Root, genName(st.Image, st.Prev))
	if err != nil {
		return res, err
	}
	ur := &UpgradeResult{}
	p, ur, err := s.planSwitch(ctx, st, rec, target, "rollback", req.Stamp, req.Resume, ur)
	res.Upgrade = ur
	if err != nil || p == nil {
		return res, err
	}
	res.Plan = p.Describe()
	if dryRun {
		return res, nil
	}
	if err := s.run(ctx, "rollback", st.Name, p); err != nil {
		return res, err
	}
	s.FinishSwitch(ctx, st.Name, ur)
	return res, nil
}

func (s *Service) rollbackData(ctx context.Context, st *jailState, req RollbackOptions, dryRun bool, res RollbackResult) (RollbackResult, error) {
	z := s.ZFS
	snap := req.Snapshot
	if snap == "" {
		return res, api.Errorf(api.CodeInvalid, "scope %s needs a snapshot", res.Scope)
	}
	if ok, err := z.Exists(ctx, st.Container+"@"+snap); err != nil {
		return res, err
	} else if !ok {
		return res, api.Errorf(api.CodeNotFound, "jail %s has no snapshot %q", st.Name, snap)
	}
	targets := dataDatasets(st)
	snapGen, _ := strconv.Atoi(s.prop(ctx, st.Container+"@"+snap, PropGen))
	switchRoot := false
	rootInSet := false
	if res.Scope == api.RollbackAll {
		targets = append(targets, s.systemDatasets(st)...)
		if st.Layout == api.LayoutThin && snapGen != 0 && snapGen != st.Gen {
			switchRoot = true
		} else {
			rootInSet = true
		}
	}
	// which datasets have the snapshot, and what would be lost
	res.Written = map[string]int64{}
	var rolled []string
	var later []string
	for _, d := range append(append([]string{}, targets...), rootIf(rootInSet, st.Root)...) {
		ok, err := z.Exists(ctx, d+"@"+snap)
		if err != nil {
			return res, err
		}
		if !ok {
			res.Skipped = append(res.Skipped, d)
			continue
		}
		w, err := z.Written(ctx, d+"@"+snap)
		if err != nil {
			return res, err
		}
		res.Written[d] = w
		res.TotalWritten += w
		rolled = append(rolled, d)
		all, err := z.Snapshots(ctx, d)
		if err != nil {
			return res, err
		}
		seen := false
		for _, n := range all {
			if seen {
				later = append(later, d+"@"+n)
			}
			if n == snap {
				seen = true
			}
		}
	}
	if len(res.Skipped) > 0 && !req.Force {
		return res, api.Errorf(api.CodeConflict, "snapshot %q does not exist on %s", snap, strings.Join(res.Skipped, ", ")).
			WithHint("use force to roll back the datasets that have it")
	}
	if !req.ConfirmDataLoss {
		keys := make([]string, 0, len(res.Written))
		for k := range res.Written {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s=%d", k, res.Written[k])
		}
		return res, api.Errorf(api.CodeDataLoss, "a %s rollback to %q discards %d bytes written since the snapshot", res.Scope, snap, res.TotalWritten).
			WithDetail("written_bytes", strconv.FormatInt(res.TotalWritten, 10)).
			WithDetail("datasets", strings.Join(parts, ",")).
			WithHint("set confirm_data_loss to proceed")
	}
	if len(later) > 0 && !req.Force {
		return res, api.Errorf(api.CodeConflict, "snapshots newer than %q exist: %s", snap, strings.Join(later, ", ")).
			WithDetail("later_snapshots", strings.Join(later, ",")).
			WithHint("roll back to the newest snapshot, or use force to destroy the newer ones")
	}

	wasRunning, err := s.Jails.Running(ctx, st.Name)
	if err != nil {
		return res, err
	}
	name := st.Name
	destroyed := false
	pl := plan.New("rollback-" + string(res.Scope) + " " + name)
	pl.Add(plan.Step{Name: "stop", Describe: "jail -r " + name + ", unmount",
		Do: func(ctx context.Context) error { return s.doStop(ctx, name) },
		Undo: func(ctx context.Context) error {
			// once a dataset has been rolled back the jail is not restarted on a mixed state
			if destroyed || !wasRunning {
				return nil
			}
			return s.doStart(ctx, name)
		}, UndoOnFail: true})
	if switchRoot {
		rec, err := s.readRecord(ctx, name)
		if err != nil {
			return res, fmt.Errorf("read spec.json: %w", err)
		}
		target, err := s.Images.Resolve(ctx, s.Cfg.at(st.Name).st.Root, genName(st.Image, snapGen))
		if err != nil {
			return res, err
		}
		if err := s.preflight(ctx, st, target); err != nil {
			return res, err
		}
		oldBundle, berr := s.Files.ReadFile(ctx, s.Cfg.at(name).bundlePath())
		if berr != nil {
			oldBundle = s.bundleBytes(rec, rec.Spec.Image, st.Image, st.Gen, st.Manifest, "", s.Cfg.now())
		}
		sw := []plan.Step{s.cloneNewRoot(st, rec, target)}
		sw = append(sw, s.swapSteps(st)...)
		sw = append(sw, s.bundleStep(name, oldBundle, s.bundleBytes(rec, rec.Spec.Image, st.Image, target.Number, target.Manifest, target.Dataset, s.Cfg.now())))
		// Once the first dataset is rolled back the operation is forward-only:
		// undoing the swap then would leave a mix of old root and rolled back
		// data. A retry completes it.
		for i := range sw {
			if undo := sw[i].Undo; undo != nil {
				sw[i].Undo = func(ctx context.Context) error {
					if destroyed {
						return nil
					}
					return undo(ctx)
				}
			}
		}
		pl.Add(sw...)
	}
	for _, d := range rolled {
		d := d
		pl.Add(plan.Step{Name: "rollback-" + d[strings.LastIndex(d, "/")+1:], Describe: fmt.Sprintf("zfs rollback -r %s@%s", d, snap),
			Do: func(ctx context.Context) error {
				destroyed = true
				return z.Rollback(ctx, d+"@"+snap, req.Force)
			}})
	}
	if wasRunning {
		pl.Add(plan.Step{Name: "start", Describe: "mount, render and jail -c " + name,
			Do: func(ctx context.Context) error { return s.doStart(ctx, name) }})
	}
	res.Plan = pl.Describe()
	if dryRun {
		return res, nil
	}
	if err := s.run(ctx, "rollback", name, pl); err != nil {
		return res, err
	}
	if switchRoot {
		if st2, err := s.observe(ctx, name); err == nil {
			_ = s.dropStale(ctx, st2) // best effort; CleanupRootOld retries
		}
	}
	return res, nil
}

func rootIf(c bool, root string) []string {
	if c {
		return []string{root}
	}
	return nil
}

// PlanRollbackRoot builds the plan of a root rollback without running it. The
// daemon uses it to roll an interrupted root rollback back after a crash:
// req must carry the Stamp and the Resume state of the interrupted run, and
// targetGen the generation the run was switching to (zero: the previous one).
func (s *Service) PlanRollbackRoot(ctx context.Context, name string, req RollbackOptions, targetGen int) (*plan.Plan, *UpgradeResult, error) {
	ur := &UpgradeResult{}
	st, err := s.ready(ctx, name)
	if err != nil {
		return nil, ur, err
	}
	if st.Layout != api.LayoutThin {
		return nil, ur, api.Errorf(api.CodeUnsupported, "root rollback applies to thin jails")
	}
	if st.Prev == 0 && req.Resume == nil {
		return nil, ur, api.Errorf(api.CodeNotFound, "jail %s has no previous generation", name)
	}
	rec, err := s.readRecord(ctx, name)
	if err != nil {
		return nil, ur, fmt.Errorf("read spec.json: %w", err)
	}
	prev := st.Prev
	if targetGen > 0 {
		prev = targetGen
	}
	target, err := s.Images.Resolve(ctx, s.Cfg.at(st.Name).st.Root, genName(st.Image, prev))
	if err != nil {
		return nil, ur, err
	}
	return s.planSwitch(ctx, st, rec, target, "rollback", req.Stamp, req.Resume, ur)
}
