package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/internal/fleet/vault"
)

func (a *app) hostFlag(cmd *cobra.Command, dst *[]string) {
	cmd.Flags().StringSliceVar(dst, "host", nil, "limit to these hosts (names or @groups, repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("host", a.completeHosts)
}

func (a *app) cmdValidate() *cobra.Command {
	return &cobra.Command{
		Use:     "validate",
		Aliases: []string{"check"},
		Short:   "Check the repository: layout, names, references, values (offline)",
		Long: `Loads the repository, enforces the filename-equals-name rule, expands
templates and host groups, and checks every cross-reference and value. It
contacts no host and decrypts no secret. It also checks that every SOPS file is
encrypted, parses, and is covered by a creation_rule of .sops.yaml, and that
every "secret:<dotted.path>" names a value of shared/secrets.sops.yaml (the
keys of that file are readable without a key). Valid but risky configuration is
printed as RISK warnings and does not fail the command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if f != nil {
				for _, w := range f.Warnings {
					fmt.Fprintln(cmd.OutOrStdout(), "warning:", w)
				}
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok: fleet %q, %d host(s), %d object(s), %d group(s), %d template(s), %d rollout(s), %d secret(s)\n",
				f.Meta.ID, len(f.Hosts), len(f.Objects), len(f.Groups), len(f.Templates), len(f.Rollouts), len(f.Secrets))
			if f.SopsSecrets != nil || len(f.Remotes) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "sops: %d secret value(s) in %s, %d remote file(s); all encrypted, all covered by %s\n",
					countLeaves(f.SopsSecrets), vault.SecretsRel, len(f.Remotes), vault.SopsPolicyFilename)
			}
			return nil
		},
	}
}

func countLeaves(m map[string]any) int {
	n := 0
	for _, v := range m {
		if sub, ok := v.(map[string]any); ok {
			n += countLeaves(sub)
		} else {
			n++
		}
	}
	return n
}

func (a *app) cmdRender() *cobra.Command {
	var hosts []string
	c := &cobra.Command{
		Use:   "render",
		Short: "Print the resolved documents that sync would send (offline)",
		Long: `Prints, per host, every object after templates are merged and host references
are expanded. Secret references stay references; nothing is decrypted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return err
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				return err
			}
			for _, h := range hs {
				fmt.Fprintf(cmd.OutOrStdout(), "# ===== host %s =====\n", h)
				b, err := f.Render(h)
				if err != nil {
					return err
				}
				cmd.OutOrStdout().Write(b)
			}
			return nil
		},
	}
	a.hostFlag(c, &hosts)
	return c
}

func (a *app) cmdPlan() *cobra.Command {
	var hosts []string
	var all bool
	c := &cobra.Command{
		Use:   "plan",
		Short: "Show what sync would change (reads hosts, writes nothing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return err
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				return err
			}
			s := a.session(f)
			defer s.Close()
			p := plan.Compute(cmd.Context(), s, plan.Options{Hosts: hs})
			a.printPlan(cmd.OutOrStdout(), p, all)
			return p.Err()
		},
	}
	a.hostFlag(c, &hosts)
	c.Flags().BoolVar(&all, "all", false, "also list unchanged objects")
	return c
}

func (a *app) cmdDrift() *cobra.Command {
	var hosts []string
	c := &cobra.Command{
		Use:   "drift",
		Short: "Exit non-zero when live hosts differ from the repository (for CI and cron)",
		Long: `Computes the plan and prints only what differs. Exit status: 0 the live
fleet matches the repository, 1 it differs (create, update, delete of an
owned object, or an object this fleet may not touch), 2 the check itself
failed (invalid repository, unreachable host). Unmanaged objects are not drift.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return &ExitError{ExitFatal, err.Error()}
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				return &ExitError{ExitFatal, err.Error()}
			}
			s := a.session(f)
			defer s.Close()
			p := plan.Compute(cmd.Context(), s, plan.Options{Hosts: hs})
			if len(p.Errors) > 0 {
				a.printPlan(cmd.OutOrStdout(), p, false)
				return &ExitError{ExitFatal, p.Err().Error()}
			}
			if p.Drifted() {
				a.printPlan(cmd.OutOrStdout(), p, false)
				return &ExitError{ExitFailure, "drift detected"}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "no drift: %s\n", p.Summary())
			return nil
		},
	}
	a.hostFlag(c, &hosts)
	return c
}

func (a *app) cmdPruneCheck() *cobra.Command {
	var hosts []string
	c := &cobra.Command{
		Use:   "prune-check",
		Short: "List live objects that the repository does not declare (never deletes)",
		Long: `Reports, per host:

  owned, undeclared   created by this fleet and removed from the repository;
                      "sync --apply --prune" would delete them
  blocked             owned and undeclared but not deletable: a project that still
                      holds objects the plan does not delete, or is not empty
  unmanaged           not owned by this fleet; sync never changes or deletes them
  refused             declared, but a live object of that name belongs to someone else

prune-check only reads. Deleting is always an explicit, separate step.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return err
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				return err
			}
			s := a.session(f)
			defer s.Close()
			p := plan.Compute(cmd.Context(), s, plan.Options{Hosts: hs})
			w := cmd.OutOrStdout()
			n := 0
			for _, h := range hs {
				fmt.Fprintf(w, "host %s\n", h)
				for _, it := range p.Items {
					if it.Host != h {
						continue
					}
					switch it.Action {
					case plan.ActionDelete:
						fmt.Fprintf(w, "  owned, undeclared  %s  (sync --apply --prune deletes it)\n", it.Ref())
					case plan.ActionBlocked:
						fmt.Fprintf(w, "  blocked            %s  (%s)\n", it.Ref(), strings.Join(it.Details, "; "))
					case plan.ActionUnmanaged:
						fmt.Fprintf(w, "  unmanaged          %s  (%s; never touched)\n", it.Ref(), strings.Join(it.Details, "; "))
					case plan.ActionRefuse:
						fmt.Fprintf(w, "  refused            %s  (%s)\n", it.Ref(), strings.Join(it.Details, "; "))
					default:
						continue
					}
					n++
				}
			}
			if n == 0 {
				fmt.Fprintln(w, "nothing outside the repository")
			}
			if len(p.Errors) > 0 {
				return errors.New(p.Err().Error())
			}
			return nil
		},
	}
	a.hostFlag(c, &hosts)
	return c
}
