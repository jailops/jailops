package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// jailBase is 'jail base': the base packages of a FreeBSD jail, added from the host while the FreeBSD-base
// repository stays disabled inside the jail.
func (a *app) jailBase(d *kindDesc[api.Jail]) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "base",
		Short: "Add base packages to a jail, or list them",
		Long: `The base system of a FreeBSD jail is made of packages (FreeBSD-runtime, FreeBSD-ssh, ...).
Inside the jail the FreeBSD-base repository stays disabled, as FreeBSD ships it, so 'pkg install' there sees
the ports only. Base packages are added from the host with 'jailops jail base add'.
They are listed with 'jailops jail base list'. Only a FreeBSD jail has them to manage.`,
		Example: `jailops jail base add web FreeBSD-ssh
jailops jail base list web`,
	}
	cmd.AddCommand(a.jailBaseAdd(d), a.jailBaseList(d))
	return cmd
}

func (a *app) jailBaseAdd(d *kindDesc[api.Jail]) *cobra.Command {
	return &cobra.Command{
		Use:   "add NAME FreeBSD-PACKAGE...",
		Short: "Install base packages into a jail from the host",
		Long: `Install base packages (FreeBSD-ssh, FreeBSD-ee, ...) into a FreeBSD jail, from the repository of the release
installed in it, on the host, while the jail runs: it is never stopped. Unknown names are refused before anything is
touched, and names that are installed already are nothing to add. It is the in-place upgrade's machinery: the packages
are fetched, the root is snapshotted (jo-pre-upgrade-<time>, kept 14 days, for a manual rollback), pkg installs them on
the running root, what the scripts of the base packages do runs inside the jail, and the health check runs. A failure
is reported and left. The change is written to the update history; 'jailops jail rollback NAME', with the jail stopped,
undoes it. Only a global administrator can do it, and a jail whose root holds what an attack on host-side pkg plants is
refused (suspected_compromised).

With --dry-run the steps are shown and nothing changes. 'jailops jail base list' shows what is installed.`,
		Example: `jailops jail base add web FreeBSD-ssh
jailops jail base add web FreeBSD-ee FreeBSD-ftp --dry-run`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, pkgs := args[0], args[1:]
			for _, p := range pkgs {
				if !strings.HasPrefix(p, "FreeBSD-") {
					return usagef("%q is not a base package: base packages are named FreeBSD-<name>; install other packages with pkg inside the jail", p)
				}
			}
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			up := api.JailUpgrade{AddPackages: pkgs, DryRun: a.dryRun}
			h, err := handle(c.Jails().Upgrade(ctx, name, up))
			if err != nil {
				return err
			}
			if !a.dryRun && !a.waiting() {
				return a.finishOp(ctx, h, "jail", name, "adding the base packages "+strings.Join(pkgs, ", ")+" started", nil)
			}
			_, op, err := a.await(ctx, h)
			if err != nil {
				return err
			}
			m := decodeMove(op)
			if m.Upgrade != nil && m.Upgrade.Nothing {
				a.say("jail/%s: nothing to add: pkg finds %s installed\n", name, strings.Join(pkgs, ", "))
				return nil
			}
			if !a.dryRun {
				a.say("jail/%s got the base packages %s\n", name, strings.Join(pkgs, ", "))
				return nil
			}
			if a.format == fmtJSON || a.format == fmtYAML {
				return a.preview("add the base packages "+strings.Join(pkgs, ", ")+" to jail "+name, up)
			}
			fprintf(a.env.Stdout, "dry-run: would add the base packages %s to jail %s\n", strings.Join(pkgs, ", "), name)
			if u := m.Upgrade; u != nil {
				for _, c := range u.BaseChanges {
					fprintf(a.env.Stdout, "  pkg: %s\n", c)
				}
				for _, f := range u.Failed {
					fprintf(a.env.Stdout, "  check FAILED: %s\n", f)
				}
				if u.Snapshot != "" {
					fprintf(a.env.Stdout, "  rollback point: snapshot %s, taken while the jail runs ('jailops jail rollback %s', with the jail stopped, undoes it)\n", u.Snapshot, name)
				}
			}
			for i, st := range m.Plan {
				fprintf(a.env.Stdout, "  %2d. %s\n", i+1, st)
			}
			return nil
		},
	}
}

func (a *app) jailBaseList(d *kindDesc[api.Jail]) *cobra.Command {
	return &cobra.Command{
		Use:               "list NAME",
		Short:             "List the base packages installed in a jail",
		Long:              "List the FreeBSD-* packages installed in a FreeBSD jail with their versions, running or stopped. They are read from a copy of the jail's package database.",
		Example:           "jailops jail base list web",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			b, err := c.Jails().Base(ctx, args[0])
			if err != nil {
				return err
			}
			return a.emit(b, nil, func() ([]string, [][]string) {
				rows := make([][]string, len(b.Packages))
				for i, p := range b.Packages {
					rows[i] = []string{p.Name, p.Version}
				}
				return []string{"NAME", "VERSION"}, rows
			}, func() []string {
				n := make([]string, len(b.Packages))
				for i, p := range b.Packages {
					n[i] = p.Name
				}
				return n
			})
		},
	}
}
