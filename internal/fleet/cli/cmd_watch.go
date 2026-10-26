package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/gitx"
	"jailops.net/jailops/internal/fleet/plan"
)

func (a *app) cmdWatch() *cobra.Command {
	var o syncOpts
	var interval time.Duration
	var remote, branch string
	var once bool
	c := &cobra.Command{
		Use:   "watch",
		Short: "Pull the repository on an interval, plan, and optionally apply",
		Long: `Every interval watch fast-forwards the repository from its git remote (git
fetch and merge --ff-only; a dirty tree or a diverged branch is an error, never
a merge), loads and validates it, plans against the hosts and prints the
result. With --apply it applies what is drifted, under the same rules as
sync --apply: ownership refusals and dangerous changes stop the apply unless
--force is given. A bad commit or an unreachable host is reported and the next
interval tries again. Use --once for a single pass (cron, CI).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval <= 0 {
				return fmt.Errorf("--interval must be positive")
			}
			o.dirtyOK = false
			w := cmd.OutOrStdout()
			ctx := cmd.Context()
			for {
				err := a.watchTick(ctx, w, o, remote, branch)
				if err != nil {
					fmt.Fprintf(a.err, "%s watch: %v\n", time.Now().UTC().Format(time.RFC3339), err)
					if once {
						return err
					}
				}
				if once {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	a.hostFlag(c, &o.hosts)
	c.Flags().DurationVar(&interval, "interval", 5*time.Minute, "time between passes")
	c.Flags().StringVar(&remote, "remote", "", "git remote to fetch (default: the branch's upstream remote)")
	c.Flags().StringVar(&branch, "branch", "", "remote branch to follow (default: the branch's upstream)")
	c.Flags().BoolVar(&once, "once", false, "run a single pass and exit")
	c.Flags().BoolVar(&o.apply, "apply", false, "apply drifted changes (default: plan only)")
	c.Flags().BoolVar(&o.force, "force", false, "allow refuse-worthy changes")
	c.Flags().BoolVar(&o.prune, "prune", false, "delete owned objects the repository no longer declares")
	c.Flags().DurationVar(&o.lockWait, "lock-wait", 30*time.Second, "how long to wait for the fleet lock")
	return c
}

// watchTick is one pass of watch.
func (a *app) watchTick(ctx context.Context, w io.Writer, o syncOpts, remote, branch string) error {
	p, err := a.path()
	if err != nil {
		return err
	}
	g := gitx.Runner{Dir: p}
	if g.IsRepo(ctx) {
		changed, head, err := g.Pull(ctx, remote, branch)
		if err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		if changed {
			fmt.Fprintf(w, "repository updated to %s\n", head)
		}
	} else {
		fmt.Fprintln(w, "not a git repository; planning the directory as it is")
	}
	f, err := a.load()
	if err != nil {
		return err
	}
	hs, err := hostsOf(f, o.hosts)
	if err != nil {
		return err
	}
	if !o.apply {
		s := a.session(f)
		defer s.Close()
		pl := plan.Compute(ctx, s, plan.Options{Hosts: hs})
		if pl.Drifted() || pl.Err() != nil {
			a.printPlan(w, pl, false)
		} else {
			fmt.Fprintf(w, "no drift: %s\n", pl.Summary())
		}
		return pl.Err()
	}
	o.apply = true
	o.dirtyOK = false
	return a.runSync(ctx, w, f, o)
}
