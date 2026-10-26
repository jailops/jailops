package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/apply"
	"jailops.net/jailops/internal/fleet/applylog"
	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/gitx"
	"jailops.net/jailops/internal/fleet/plan"
)

type syncOpts struct {
	hosts    []string
	apply    bool
	force    bool
	prune    bool
	dirtyOK  bool
	lockWait time.Duration
	all      bool
}

func (a *app) cmdSync() *cobra.Command {
	var o syncOpts
	c := &cobra.Command{
		Use:   "sync",
		Short: "Plan, and with --apply make the hosts match the repository",
		Long: `Without --apply, sync prints the plan and changes nothing. With --apply it
takes the fleet lock, plans again, and executes the plan through the API.

  --force   allow refuse-worthy changes (emptying a firewall policy or address
            set, opening default_in, detaching a policy, renumbering a network,
            any delete, ...)
  --prune   also delete objects this fleet created and the repository no longer
            declares. Objects it does not own are never deleted.
  --dirty-ok  apply although the fleet repository is dirty or behind its upstream

An object that exists but is not owned by this fleet is refused, not updated;
take it over deliberately with "adopt --claim". Every apply writes a JSONL log
to the state directory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return err
			}
			return a.runSync(cmd.Context(), cmd.OutOrStdout(), f, o)
		},
	}
	a.hostFlag(c, &o.hosts)
	c.Flags().BoolVar(&o.apply, "apply", false, "apply the plan (default: plan only)")
	c.Flags().BoolVar(&o.force, "force", false, "allow refuse-worthy changes")
	c.Flags().BoolVar(&o.prune, "prune", false, "delete owned objects the repository no longer declares")
	c.Flags().BoolVar(&o.dirtyOK, "dirty-ok", false, "apply even if the repository is dirty or behind its upstream")
	c.Flags().DurationVar(&o.lockWait, "lock-wait", 30*time.Second, "how long to wait for the fleet lock")
	c.Flags().BoolVar(&o.all, "all", false, "also list unchanged objects")
	return c
}

// runSync is the body of sync and of watch --apply.
func (a *app) runSync(ctx context.Context, out io.Writer, f *config.Fleet, o syncOpts) error {
	hs, err := hostsOf(f, o.hosts)
	if err != nil {
		return err
	}
	s := a.session(f)
	defer s.Close()
	if !o.apply {
		p := plan.Compute(ctx, s, plan.Options{Hosts: hs})
		a.printPlan(out, p, o.all)
		if err := p.Err(); err != nil {
			return err
		}
		if p.Drifted() {
			fmt.Fprintln(out, "\n(plan only; re-run with --apply to change the hosts)")
		}
		return nil
	}
	if !o.dirtyOK {
		if st := (gitx.Runner{Dir: f.Path}).Inspect(ctx); len(st.Warnings()) > 0 {
			for _, w := range st.Warnings() {
				fmt.Fprintln(a.err, "git:", w)
			}
			return fmt.Errorf("refusing to apply from a dirty or stale repository; commit or pull, or pass --dirty-ok")
		}
	}
	a.printWarnings(f)
	lock, err := applylog.Acquire(a.state(), f.Meta.ID, o.lockWait)
	if err != nil {
		return err
	}
	defer lock.Release()
	p := plan.Compute(ctx, s, plan.Options{Hosts: hs})
	fmt.Fprintln(out, "== PLAN ==")
	a.printPlan(out, p, o.all)
	ao := apply.Options{Prune: o.prune, Force: o.force, Out: out}
	if err := apply.Check(p, ao); err != nil {
		return err
	}
	lg, err := applylog.Open(a.state(), f.Meta.ID, a.red)
	if err != nil {
		fmt.Fprintln(a.err, "warning: no apply log:", err)
	} else {
		defer lg.Close()
		ao.Log = lg
		fmt.Fprintf(out, "\nlogging to %s\n", lg.Path())
	}
	fmt.Fprintln(out, "\n== APPLYING ==")
	res, err := apply.Run(ctx, s, p, ao)
	ok := 0
	for _, oc := range res.Outcomes {
		if oc.Status == "ok" {
			ok++
		}
	}
	fmt.Fprintf(out, "\n%d change(s) applied, %d failed\n", ok, len(res.Failed()))
	return err
}
