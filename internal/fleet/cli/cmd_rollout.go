package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/applylog"
	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/rollout"
)

func (a *app) executor(f *config.Fleet, w interface{ Write([]byte) (int, error) }) (*rollout.Executor, func()) {
	s := a.session(f)
	ex := &rollout.Executor{
		Session: s,
		Store:   rollout.Store{Dir: filepath.Join(a.state(), "rollouts", f.Meta.ID)},
		Out:     w,
	}
	closeFn := func() { s.Close() }
	return ex, closeFn
}

func (a *app) cmdRollout() *cobra.Command {
	root := &cobra.Command{
		Use:   "rollout",
		Short: "Run staged upgrades declared as Rollout objects",
		Long: `A Rollout selects jails by label, upgrades a canary first and then batches,
waits for a health gate after each batch, and stops at the first failure
without touching the jails after it. Progress is saved after every step:
"rollout resume" continues where it stopped, waiting for an upgrade that was
already started instead of repeating it. Only jails owned by this fleet are
upgraded.`,
	}
	complete := func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.completeRollouts(cmd, args, "")
	}
	run := func(use, short string, mode rollout.Mode, restart *bool) *cobra.Command {
		var lockWait time.Duration
		c := &cobra.Command{
			Use: use + " <rollout>", Short: short, Args: cobra.ExactArgs(1), ValidArgsFunction: complete,
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := a.load()
				if err != nil {
					return err
				}
				m := mode
				if restart != nil && *restart {
					m = rollout.Restart
				}
				lock, err := applylog.Acquire(a.state(), f.Meta.ID, lockWait)
				if err != nil {
					return err
				}
				defer lock.Release()
				ex, closeFn := a.executor(f, cmd.OutOrStdout())
				defer closeFn()
				if lg, err := applylog.Open(a.state(), f.Meta.ID, a.red); err == nil {
					defer lg.Close()
					ex.Log = lg
				}
				st, err := ex.Run(cmd.Context(), args[0], m)
				if st != nil {
					printState(cmd.OutOrStdout(), st)
				}
				return err
			},
		}
		c.Flags().DurationVar(&lockWait, "lock-wait", 30*time.Second, "how long to wait for the fleet lock")
		return c
	}
	var restart bool
	runCmd := run("run", "Start a rollout", rollout.Start, &restart)
	runCmd.Flags().BoolVar(&restart, "restart", false, "discard an unfinished run of this rollout and start over")
	root.AddCommand(
		runCmd,
		run("resume", "Continue an unfinished rollout", rollout.Resume, nil),
		&cobra.Command{
			Use: "list", Short: "List the declared rollouts", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				f, err := a.load()
				if err != nil {
					return err
				}
				ex, closeFn := a.executor(f, nil)
				defer closeFn()
				for _, n := range f.RolloutNames() {
					status := "never run"
					if st, err := ex.Store.Load(n); err == nil {
						status = st.Status
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%-24s %s\n", n, status)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "status <rollout>", Short: "Show the recorded progress of a rollout", Args: cobra.ExactArgs(1),
			ValidArgsFunction: complete,
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := a.load()
				if err != nil {
					return err
				}
				ex, closeFn := a.executor(f, nil)
				defer closeFn()
				st, err := ex.Store.Load(args[0])
				if errors.Is(err, rollout.ErrNoState) {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: never run\n", args[0])
					return nil
				}
				if err != nil {
					return err
				}
				printState(cmd.OutOrStdout(), st)
				return nil
			},
		},
		&cobra.Command{
			Use: "abort <rollout>", Short: "Cancel in-flight upgrades and stop an unfinished rollout", Args: cobra.ExactArgs(1),
			ValidArgsFunction: complete,
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := a.load()
				if err != nil {
					return err
				}
				lock, err := applylog.Acquire(a.state(), f.Meta.ID, 30*time.Second)
				if err != nil {
					return err
				}
				defer lock.Release()
				ex, closeFn := a.executor(f, cmd.OutOrStdout())
				defer closeFn()
				st, err := ex.Abort(cmd.Context(), args[0])
				if st != nil {
					printState(cmd.OutOrStdout(), st)
				}
				return err
			},
		},
	)
	return root
}

func printState(w interface{ Write([]byte) (int, error) }, st *rollout.State) {
	c := st.Counts()
	fmt.Fprintf(w, "rollout %s: %s (%d done, %d failed, %d pending of %d)\n", st.Name, st.Status,
		c[rollout.ItemDone], c[rollout.ItemFailed], len(st.Items)-c[rollout.ItemDone]-c[rollout.ItemFailed], len(st.Items))
	if st.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", st.Error)
	}
	for _, it := range st.Items {
		line := fmt.Sprintf("  %-10s %s/%s %s -> %s", it.State, it.Host, it.Jail, it.From, it.To)
		if it.Error != "" {
			line += ": " + it.Error
		}
		fmt.Fprintln(w, line)
	}
}
