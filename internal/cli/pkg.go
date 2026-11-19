package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
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
the packages follow "--".

The jail needs the network to reach its package repository. Packages of a jail that JailOps updates (a thin jail's
base, an app) are changed by 'jailops update' and 'jailops app', not by this command.`,
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
	targets := a.jailTargets(cmd)
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
		resolved, bulk, err := targets(cmd.Context(), names)
		if err != nil {
			return err
		}
		argv := []string{"pkg", act.cmd}
		if act.yes {
			argv = append(argv, "-y")
		}
		argv = append(argv, pkgs...)
		o.noTTY = true
		if len(resolved) == 1 && !bulk {
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
