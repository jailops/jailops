package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// pkgActions are what `jailops pkg` runs: the action, the pkg(8) command it stands for, whether pkg is told to
// assume yes, and whether packages must be named.
var pkgActions = map[string]struct {
	cmd      string
	yes      bool
	packages bool
}{
	"install": {"install", true, true},
	"remove":  {"delete", true, true},
	"upgrade": {"upgrade", true, false},
	"info":    {"info", false, false},
}

// pkgCmd is `jailops pkg ACTION JAIL... -- PACKAGE...`: pkg(8) of the jail, on one jail or many, through exec.
func (a *app) pkgCmd() *cobra.Command {
	d := a.jailDesc()
	var o execOpts
	var parallel int
	cmd := &cobra.Command{
		Use:   "pkg install|upgrade|remove|info [NAME...] [flags] -- PACKAGE [PACKAGE...]",
		Short: "Run pkg in a jail, or in many",
		Long: `Run pkg(8) inside jails: install, upgrade, remove or show packages. It is exec of the jail's own pkg,
nothing more: the command is "pkg install -y PACKAGE..." (remove is "pkg delete -y", upgrade is "pkg upgrade -y", info has
no -y) run in each jail with the same rules as 'jailops exec' (running jails, the same audit, no pseudo-terminal, output
with [jail] in front when there are several, the same exit status). Name the jails, or select them with -l KEY=VALUE or --all;
the packages follow "--". These commands are for FreeBSD jails; a Linux jail is not part of a selection and manages its own packages.

The jail needs the network to reach its package repository. "install" lets FreeBSD's bootstrapper fetch pkg itself
in a jail that does not have it yet (ASSUME_ALWAYS_YES=yes). Packages of a jail that JailOps updates (its FreeBSD
base) are changed by 'jailops jail upgrade' and 'jailops update', not by this command.`,
		Example: `jailops pkg install web -- nginx
jailops pkg install web1 web2 -- curl rsync
jailops pkg upgrade -l role=web
jailops pkg remove --all -- vim-tiny
jailops pkg info web -- nginx`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: completeArgs(func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{"install", "upgrade", "remove", "info"}, cobra.ShellCompDirectiveNoFileComp
		}, completeKind(a, d)),
	}
	targets := a.jailTargetsOS(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		act, ok := pkgActions[args[0]]
		if !ok {
			return usagef("unknown action %q", args[0]).withHint("jailops pkg install|upgrade|remove|info [NAME...] [-- PACKAGE...]")
		}
		args = args[1:]
		var names, pkgs []string
		if dash := cmd.Flags().ArgsLenAtDash(); dash >= 1 {
			names, pkgs = args[:dash-1], args[dash-1:]
		} else {
			names = args
		}
		for _, p := range pkgs {
			if strings.HasPrefix(p, "-") {
				return usagef("%q is a package name that starts with \"-\"", p)
			}
		}
		if act.packages && len(pkgs) == 0 {
			return usagef("name the packages after --").withHint(fmt.Sprintf("jailops pkg %s NAME... -- PACKAGE...", args0(cmd)))
		}
		resolved, oses, bulk, err := targets(cmd.Context(), names)
		if err != nil {
			return err
		}
		several := bulk || len(resolved) > 1
		if resolved, err = a.freebsdJails(cmd.Context(), resolved, oses, bulk); err != nil {
			return err
		}
		argv := []string{"pkg", act.cmd}
		if act.yes {
			argv = append(argv, "-y")
		}
		argv = append(argv, pkgs...)
		o.noTTY = true
		if act.cmd == "install" {
			// an explicit install: the bootstrapper /usr/sbin/pkg fetches pkg first when the jail does not have it yet
			o.env = append(o.env, "ASSUME_ALWAYS_YES=yes")
		}
		if !several {
			return a.runExec(cmd.Context(), resolved[0], argv, o)
		}
		return a.runExecMany(cmd.Context(), resolved, bulk, argv, o, parallel)
	}
	cmd.Flags().IntVar(&parallel, "parallel", DefaultExecParallel, "jails that run pkg at once, when there are several")
	return cmd
}

func args0(cmd *cobra.Command) string {
	if len(cmd.Flags().Args()) > 0 {
		return cmd.Flags().Args()[0]
	}
	return "install"
}

// freebsdJails leaves the Linux jails out of the targets: the host features (pkg, jail upgrade) are for FreeBSD jails,
// and a Linux jail manages its own packages. oses is the OS of the jails a selector listed; named jails are looked up
// in one list of the project. One named Linux jail is refused; among several they are not part of the run.
func (a *app) freebsdJails(ctx context.Context, names []string, oses map[string]api.OS, bulk bool) ([]string, error) {
	if oses == nil {
		c, err := a.connect(ctx)
		if err != nil {
			return nil, err
		}
		jails, err := collect(c.Jails().List(ctx))
		if err != nil {
			return nil, err
		}
		oses = map[string]api.OS{}
		for _, j := range jails {
			oses[j.Metadata.Name] = j.Spec.OS
		}
	}
	if len(names) == 1 && !bulk {
		if oses[names[0]] == api.OSLinux {
			return nil, api.Errorf(api.CodePreconditionFailed, "jail %s is a Linux jail: pkg is FreeBSD's package manager; use the distribution's own with jailops exec", names[0])
		}
		return names, nil
	}
	out := []string{}
	for _, n := range names {
		if oses[n] != api.OSLinux {
			out = append(out, n)
		}
	}
	return out, nil
}
