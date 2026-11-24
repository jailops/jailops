package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// RollbackOptions is api.JailRollback plus the force flag and a Stamp, see UpgradeRequest.
type RollbackOptions struct {
	api.JailRollback
	// Force skips the datasets that do not have the snapshot.
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
	// Upgrade is set for scope root, the return to the snapshot of the latest upgrade.
	Upgrade *UpgradeResult
	// Snapshot, Destroyed: scope root returns the root to this pre-upgrade snapshot and destroys the snapshots taken
	// after it (Destroyed, as dataset@snapshot of the root).
	Snapshot  string
	Destroyed []string
}

// RollbackJail rolls a jail back.
//
//	root (default): the root returns to the snapshot of the latest upgrade (rollbackThick).
//	data: zfs rollback of the delegated dataset to a snapshot.
//	all: zfs rollback of the root and the delegated dataset to a snapshot.
//
// data and all need ConfirmDataLoss and report what `written` says would be
// lost. Every scope refuses when snapshots newer than the target exist on a dataset it rolls back, and names them,
// unless DestroyNewer (which needs ConfirmDataLoss): nothing is destroyed that the request did not name.
// Data is never rolled back automatically.
func (s *Service) RollbackJail(ctx context.Context, name string, req RollbackOptions, dryRun bool) (RollbackResult, error) {
	res := RollbackResult{Scope: req.Scope}
	if res.Scope == "" {
		res.Scope = api.RollbackRoot
	}
	st, err := s.own(ctx, name)
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
		return res, api.Errorf(api.CodeInvalid, "scope root returns to the snapshot of the latest upgrade; use scope all with a snapshot to restore a point in time")
	}
	return s.rollbackThick(ctx, st, req, dryRun, res)
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
	rootInSet := res.Scope == api.RollbackAll
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
		return res, api.Errorf(api.CodePreconditionFailed, "snapshot %q does not exist on %s", snap, strings.Join(res.Skipped, ", ")).
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
	if len(later) > 0 && !req.DestroyNewer {
		return res, newerSnapshotsError(snap, later)
	}

	// as for the root: datasets rolled back under running services kill them, and JailOps does not stop jails
	name := st.Name
	if up, err := s.Jails.Running(ctx, name); err != nil {
		return res, err
	} else if up && !dryRun {
		return res, api.Errorf(api.CodePreconditionFailed, "jail %s runs: datasets rolled back under running services break them, and JailOps does not stop jails", plainName(name)).
			WithHint("stop the jail (jailops stop " + plainName(name) + "), roll it back, start it again")
	}
	pl := plan.New("rollback-" + string(res.Scope) + " " + name)
	for _, d := range rolled {
		d := d
		pl.Add(plan.Step{Name: "rollback-" + d[strings.LastIndex(d, "/")+1:], Describe: fmt.Sprintf("zfs rollback -r %s@%s", d, snap),
			Do: func(ctx context.Context) error { return z.Rollback(ctx, d+"@"+snap, req.DestroyNewer) }})
	}
	res.Plan = pl.Describe()
	if dryRun {
		return res, nil
	}
	if err := s.run(ctx, "rollback", name, pl); err != nil {
		return res, err
	}
	return res, nil
}

func rootIf(c bool, root string) []string {
	if c {
		return []string{root}
	}
	return nil
}

// PlanRollbackRoot builds the plan of a root rollback without running it. The daemon uses it to finish an
// interrupted root rollback after a crash: req must carry the Stamp and the Resume state of the interrupted run.
func (s *Service) PlanRollbackRoot(ctx context.Context, name string, req RollbackOptions) (*plan.Plan, *UpgradeResult, error) {
	st, err := s.own(ctx, name)
	if err != nil {
		return nil, &UpgradeResult{}, err
	}
	p, ur, _, err := s.planThickRollback(ctx, st, req, false)
	return p, ur, err
}

type thickRollbackInfo struct {
	Snapshot string
	Written  int64
	Later    []string
}

func (s *Service) rollbackThick(ctx context.Context, st *jailState, req RollbackOptions, dryRun bool, res RollbackResult) (RollbackResult, error) {
	p, ur, info, err := s.planThickRollback(ctx, st, req, dryRun)
	res.Upgrade = ur
	res.Snapshot, res.TotalWritten, res.Destroyed = info.Snapshot, info.Written, info.Later
	if info.Snapshot != "" {
		res.Written = map[string]int64{st.Root: info.Written}
	}
	if err != nil || p == nil {
		return res, err
	}
	res.Plan = p.Describe()
	if dryRun {
		return res, nil
	}
	// the release is the one of the snapshot again: recorded on the snapshot by the upgrade (the snapshot is used up by
	// the rollback, so it is read first); a snapshot of an older version does not say, and the root is read
	was := ""
	if info.Snapshot != "" {
		was = s.prop(ctx, st.Root+"@"+info.Snapshot, PropFreeBSDVersion)
	}
	if err := s.run(ctx, "rollback", st.Name, p); err != nil {
		return res, err
	}
	if was != "" {
		_ = s.ZFS.Set(context.WithoutCancel(ctx), st.Root, map[string]string{PropFreeBSDVersion: was})
	} else {
		s.recordInstalledRelease(ctx, st.Name)
	}
	s.FinishSwitch(ctx, st.Name, ur)
	return res, nil
}

// newestUpgradeSnapshot is the newest pre-upgrade snapshot of a jail that its root still has.
func (s *Service) newestUpgradeSnapshot(ctx context.Context, st *jailState) (string, error) {
	names, err := s.ZFS.Snapshots(ctx, st.Container)
	if err != nil {
		return "", err
	}
	for i := len(names) - 1; i >= 0; i-- {
		if !strings.HasPrefix(names[i], upgradeSnapPrefix) {
			continue
		}
		if ok, _ := s.ZFS.Exists(ctx, st.Root+"@"+names[i]); ok {
			return names[i], nil
		}
	}
	return "", api.Errorf(api.CodeNotFound, "jail %s has no snapshot from an in-place upgrade to go back to", plainName(st.Name)).
		WithHint("the snapshot of an upgrade is kept for " + days(s.Cfg.RootOldRetention) + " and a successful rollback uses it up")
}

// planThickRollback builds the rollback of the last in-place upgrade (or base addition) of a FreeBSD jail: the
// root returns to the newest jo-pre-upgrade snapshot, and nothing else. The root holds the base, the ports packages,
// the package database and the data of the services, so all of it goes back; a volume attached to the jail is outside
// the root and stays. Everything written to the root since the snapshot is lost, so the request must confirm
// (ConfirmDataLoss). A snapshot of the root taken after it (a backup, a replication, one by hand) would be destroyed by
// the rollback, so the request refuses and names them unless it says DestroyNewer; snapshots of the jail's other
// datasets are never touched but for the used-up pre-upgrade one. dry skips both refusals so that a dry run can show
// what would be lost.
//
//	zfs rollback of the root, destroy the snapshot
//
// The jail must be stopped (JailOps does not stop it). Once the root is rolled back the plan only goes forward, and a
// retry finishes it (the snapshot is destroyed last).
func (s *Service) planThickRollback(ctx context.Context, st *jailState, req RollbackOptions, dry bool) (*plan.Plan, *UpgradeResult, thickRollbackInfo, error) {
	ur := &UpgradeResult{}
	var info thickRollbackInfo
	name := st.Name
	rec, err := s.readRecord(ctx, name)
	if err != nil {
		return nil, ur, info, fmt.Errorf("read spec.json: %w", err)
	}
	if rec.Spec.OS == api.OSLinux {
		return nil, ur, info, api.Errorf(api.CodeUnsupported, "a Linux jail has no in-place upgrade to roll back")
	}
	if req.Snapshot != "" {
		return nil, ur, info, api.Errorf(api.CodeInvalid, "the rollback of a jail returns to the newest pre-upgrade snapshot; use scope all with a snapshot to restore a point in time")
	}
	z := s.ZFS
	resume := req.Resume
	snap := ""
	if resume != nil {
		snap = resume.Snapshot
	}
	if snap == "" {
		if snap, err = s.newestUpgradeSnapshot(ctx, st); err != nil {
			return nil, ur, info, err
		}
	}
	info.Snapshot, ur.Snapshot = snap, snap
	rootSnap := st.Root + "@" + snap
	wasRunning := false
	if info.Later, err = s.laterSnapshots(ctx, st.Root, snap); err != nil {
		return nil, ur, info, err
	}
	if resume != nil {
		wasRunning = resume.WasRunning
	} else {
		if wasRunning, err = s.Jails.Running(ctx, name); err != nil {
			return nil, ur, info, err
		}
		if info.Written, err = z.Written(ctx, rootSnap); err != nil {
			return nil, ur, info, err
		}
		if s.Cfg.Replicating != nil {
			if busy, err := s.Cfg.Replicating(ctx, name); err != nil {
				return nil, ur, info, err
			} else if busy {
				return nil, ur, info, api.Errorf(api.CodeBusy, "replication of jail %s is running", name)
			}
		}
		if !dry && !req.ConfirmDataLoss {
			e := api.Errorf(api.CodeDataLoss, "rolling jail %s back to snapshot %s discards what was written to its root since then (%d bytes)", plainName(name), snap, info.Written).
				WithDetail("written_bytes", strconv.FormatInt(info.Written, 10)).
				WithHint("set confirm_data_loss to proceed")
			if len(info.Later) > 0 {
				e = e.WithDetail("later_snapshots", strings.Join(info.Later, ","))
			}
			return nil, ur, info, e
		}
		if !dry && len(info.Later) > 0 && !req.DestroyNewer {
			return nil, ur, info, newerSnapshotsError(snap, info.Later)
		}
	}
	// JailOps never stops a jail, and a root rolled back under a running jail kills its services: a program that maps a
	// file the rollback changes dies of SIGBUS (VERIFIED on FreeBSD 16: local-unbound, syslogd, sh and then cron of the
	// jail did, and the jail ended). So the administrator stops the jail first; the rollback refuses a running one.
	if wasRunning && resume == nil && !dry {
		return nil, ur, info, api.Errorf(api.CodePreconditionFailed, "jail %s runs: a root rolled back under running services kills them (SIGBUS), and JailOps does not stop jails", plainName(name)).
			WithHint("stop the jail (jailops stop " + plainName(name) + "), roll it back, start it again")
	}
	ur.State = SwitchState{WasRunning: wasRunning, Snapshot: snap}
	ur.Changed = true
	if len(st.Volumes)+len(st.HostDirs) > 0 {
		ur.Warnings = append(ur.Warnings, "the volumes attached to the jail are outside its root and are not rolled back")
	}

	// the later snapshots go only when the request named them (DestroyNewer; a dry run shows what that would do). A
	// resumed run that finds a snapshot taken after the crash without DestroyNewer fails in zfs rollback and destroys
	// nothing.
	later := info.Later
	if !req.DestroyNewer && !dry {
		later = nil
	}
	pl := plan.New("rollback-jail " + name)
	pl.Add(plan.Step{Name: "rollback-root", Describe: fmt.Sprintf("zfs rollback %s%s  (what was written to the root since is lost%s)", map[bool]string{true: "-r ", false: ""}[len(later) > 0], rootSnap, laterNote(later)),
		Do: func(ctx context.Context) error { return z.Rollback(ctx, rootSnap, len(later) > 0) }})
	all := st.all()
	// best effort: the root is restored, so a snapshot that stays is no reason to fail (the retention removes the used
	// one, it keeps its cleanup mark). A later snapshot the request named goes from every dataset of the jail, as a
	// jail snapshot is one name across them; any other is not the rollback's to destroy.
	pl.Add(plan.Step{Name: "drop-snapshots", Describe: "zfs destroy " + strings.Join(append([]string{snap}, laterNames(later)...), ", ") + " on " + strings.Join(all, ", ") + " (used up)",
		Do: func(ctx context.Context) error {
			for _, d := range all {
				for _, n := range append([]string{snap}, laterNames(later)...) {
					if ok, _ := z.Exists(ctx, d+"@"+n); ok {
						_ = z.Destroy(ctx, d+"@"+n)
					}
				}
			}
			return nil
		}})
	return pl, ur, info, nil
}

// laterSnapshots lists the snapshots of ds taken after snap, as ds@name.
func (s *Service) laterSnapshots(ctx context.Context, ds, snap string) ([]string, error) {
	names, err := s.ZFS.Snapshots(ctx, ds)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := false
	for _, n := range names {
		if seen {
			out = append(out, ds+"@"+n)
		}
		if n == snap {
			seen = true
		}
	}
	return out, nil
}

// newerSnapshotsError is the refusal of a rollback over snapshots taken after its target: zfs rollback destroys them,
// and they are someone's (a backup, a replication, a snapshot by hand), so the request has to name that it wants them
// gone.
func newerSnapshotsError(snap string, later []string) error {
	return api.Errorf(api.CodePreconditionFailed, "snapshots taken after %q exist: %s; a rollback to %q destroys them", snap, strings.Join(later, ", "), snap).
		WithDetail("later_snapshots", strings.Join(later, ",")).
		WithHint("keep what you need from them (a clone, a send), then pass --destroy-newer with --confirm-data-loss (destroy_newer and confirm_data_loss in the API)")
}

// laterNames are the snapshot names of a list of dataset@name.
func laterNames(l []string) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = x[strings.LastIndex(x, "@")+1:]
	}
	return out
}

func laterNote(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return "; destroys the later snapshots " + strings.Join(laterNames(l), ", ")
}

// days writes a retention as people read it: "14 days", "1 day", or the duration when it is not whole days.
func days(d time.Duration) string {
	if d <= 0 || d%(24*time.Hour) != 0 {
		return d.String()
	}
	if n := int(d / (24 * time.Hour)); n != 1 {
		return strconv.Itoa(n) + " days"
	}
	return "1 day"
}
