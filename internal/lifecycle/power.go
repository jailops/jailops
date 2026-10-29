package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

func (s *Service) ready(ctx context.Context, name string) (*jailState, error) {
	st, err := s.observe(ctx, name)
	if err != nil {
		return nil, err
	}
	if !st.Complete {
		return nil, api.Errorf(api.CodeBusy, "jail %q was not created completely", name).WithHint("destroy it or run recovery")
	}
	return st, nil
}

// PlanStart orders a start: mount the persistent datasets, render the
// configuration through the hooks, create the jail with jail(8) and check
// readiness. A failure undoes everything in reverse.
func (s *Service) PlanStart(ctx context.Context, name string) (*plan.Plan, error) {
	if _, err := s.ready(ctx, name); err != nil {
		return nil, err
	}
	return plan.New("start-jail "+name,
		plan.Step{Name: "mount", Describe: "zfs mount root, persistent datasets and volumes of " + name,
			Do:   func(ctx context.Context) error { return s.mountTree(ctx, name) },
			Undo: func(ctx context.Context) error { return s.unmountTree(ctx, name) }, UndoOnFail: true},
		plan.Step{Name: "render", Describe: "render jail.conf, rc.conf and firewall anchor for " + name,
			Do:   func(ctx context.Context) error { return s.Hooks.PreStart(ctx, name) },
			Undo: func(ctx context.Context) error { return s.Hooks.PostStop(ctx, name) }},
		plan.Step{Name: "jail-start", Describe: "jail -c " + name,
			Do: func(ctx context.Context) error { return s.Jails.Start(ctx, name) },
			// Jails.Stop succeeds when the jail is not running, so this is safe
			// even if jail(8) failed half-way.
			Undo: func(ctx context.Context) error { return s.Jails.Stop(ctx, name) }, UndoOnFail: true},
		plan.Step{Name: "ready", Describe: fmt.Sprintf("wait until %s runs (timeout %s)", name, s.Cfg.ReadyTimeout),
			Do: func(ctx context.Context) error { return s.waitReady(ctx, name) }},
	), nil
}

// StartJail starts a jail. Starting a running jail does nothing.
func (s *Service) StartJail(ctx context.Context, name string) error {
	p, err := s.PlanStart(ctx, name)
	if err != nil {
		return err
	}
	if up, err := s.Jails.Running(ctx, name); err != nil {
		return err
	} else if up {
		return nil
	}
	return s.run(ctx, "start", name, p)
}

// StopJail stops a jail, tears down its rendered state and unmounts its
// datasets. It is idempotent: each step is safe to repeat after a failure.
func (s *Service) StopJail(ctx context.Context, name string) error {
	if _, err := s.observe(ctx, name); err != nil {
		return err
	}
	p := plan.New("stop-jail "+name,
		plan.Step{Name: "jail-stop", Describe: "jail -r " + name + " (if running)",
			Do: func(ctx context.Context) error {
				if up, err := s.Jails.Running(ctx, name); err != nil || !up {
					return err
				}
				return s.Jails.Stop(ctx, name)
			}},
		plan.Step{Name: "teardown", Describe: "remove runtime state of " + name,
			Do: func(ctx context.Context) error { return s.Hooks.PostStop(ctx, name) }},
		plan.Step{Name: "unmount", Describe: "zfs unmount the tree of " + name,
			Do: func(ctx context.Context) error {
				// The jail is gone. A root that stays busy for a while after its processes died (seen right after an
				// app install) is not a failed stop: the datasets stay mounted until the next start or stop, which
				// are idempotent, and the helper has logged the refusal.
				if err := s.unmountTree(ctx, name); err != nil && !api.IsCode(err, api.CodeBusy) {
					return err
				}
				return nil
			}})
	return s.run(ctx, "stop", name, p)
}

// DestroyResult is the outcome of DestroyJail.
type DestroyResult struct {
	Plan     []string
	Warnings []string
}

// DestroyJail destroys a jail and every dataset below its container. A running
// jail is refused unless force is set. A snapshot of the jail that has a clone
// outside the jail refuses the destroy (the clone would lose its origin).
func (s *Service) DestroyJail(ctx context.Context, name string, force, dryRun bool) (DestroyResult, error) {
	var res DestroyResult
	st, err := s.observe(ctx, name)
	if err != nil {
		return res, err
	}
	up, err := s.Jails.Running(ctx, name)
	if err != nil {
		return res, err
	}
	if up && !force {
		return res, api.Errorf(api.CodeBusy, "jail %q is running", name).WithHint("stop it or use force")
	}
	inTree := func(ds string) bool { return ds == st.Container || strings.HasPrefix(ds, st.Container+"/") }
	all, err := s.ZFS.Descendants(ctx, st.Container)
	if err != nil {
		return res, err
	}
	for _, ds := range append([]string{st.Container}, all...) {
		snaps, err := s.ZFS.Snapshots(ctx, ds)
		if err != nil {
			return res, err
		}
		for _, sn := range snaps {
			cl, err := s.ZFS.Clones(ctx, ds+"@"+sn)
			if err != nil {
				return res, err
			}
			for _, c := range cl {
				if !inTree(c) {
					return res, api.Errorf(api.CodeBusy, "%s@%s has a clone %s outside the jail", ds, sn, c)
				}
			}
		}
	}
	pl := plan.New("destroy-jail " + name)
	if up {
		pl.Add(plan.Step{Name: "stop", Describe: "jail -r " + name,
			Do:   func(ctx context.Context) error { return s.doStop(ctx, name) },
			Undo: func(ctx context.Context) error { return s.doStart(ctx, name) }, UndoOnFail: true})
	}
	pl.Add(plan.Step{Name: "destroy-datasets", Describe: "zfs destroy -r " + st.Container,
		Do: func(ctx context.Context) error {
			if s.prop(ctx, st.Container, PropManaged) != "on" {
				return api.Errorf(api.CodeForbidden, "%s is not managed by JailOps", st.Container)
			}
			return s.ZFS.DestroyRecursive(ctx, st.Container)
		}})
	res.Plan = pl.Describe()
	if dryRun {
		return res, nil
	}
	if err := s.run(ctx, "destroy", name, pl); err != nil {
		return res, err
	}
	// Derived configuration and generation garbage collection happen after the
	// point of no return; failures are reported, they cannot undo the destroy.
	if err := s.Hooks.Remove(ctx, name); err != nil {
		res.Warnings = append(res.Warnings, "remove rendered configuration: "+err.Error())
	}
	if _, err := s.Images.Collect(ctx); err != nil {
		res.Warnings = append(res.Warnings, "collect parked generations: "+err.Error())
	}
	return res, nil
}
