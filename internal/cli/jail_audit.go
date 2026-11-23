package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// jailAudit is 'jail audit NAME': the read-only audit of a FreeBSD jail (docs/audit.md).
func (a *app) jailAudit(d *kindDesc[api.Jail]) *cobra.Command {
	return &cobra.Command{
		Use:   "audit NAME",
		Short: "Look at a jail for security problems and better settings, read-only",
		Long: `Look at a FreeBSD jail, running or stopped, and print one line per check: PASS, INFO, WARN or CRITICAL, what
was found and the fix. Nothing in the jail changes, and the jail is not stopped or started: the root helper reads the
jail's root on the host, and in a running jail sockstat lists the listening sockets.

The security checks: a link out of the root where the host's pkg writes, a link among pkg's own files, a
pkg.conf with plugins (CRITICAL, as for an upgrade); an account with uid 0 besides root and toor (CRITICAL); root not
locked or an account with an empty password; set-uid or set-gid files no package installed; package repositories that
are not FreeBSD's; FreeBSD-base enabled inside the jail; programs or libraries of the base that differ from their
packages; sshd allowing root logins or passwords (keyboard-interactive through PAM included). The practice checks
never go beyond WARN: periodic's world/kernel comparison and its reports by mail, cron jitter, rc.conf knobs that do
nothing in a jail, root's shell and prompt, the JailOps block of /etc/hosts, volumes mounted nosuid, the disk limit,
the size of the root and the ports listening without being exposed.

The exit status is 0 when no check is CRITICAL (WARN and INFO included) and 1 when one is. Operator in the project.`,
		Example: `jailops jail audit web
jailops jail audit web -o json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			r, err := c.Jails().Audit(ctx, args[0])
			if err != nil {
				return err
			}
			if err := a.emit(r, nil, func() ([]string, [][]string) {
				rows := make([][]string, len(r.Checks))
				for i, ch := range r.Checks {
					rows[i] = []string{ch.ID, strings.ToUpper(string(ch.Result)), ch.Detail}
				}
				return []string{"CHECK", "RESULT", "DETAIL"}, rows
			}, func() []string {
				n := make([]string, 0, len(r.Checks))
				for _, ch := range r.Checks {
					if ch.Result == api.AuditWarn || ch.Result == api.AuditCritical {
						n = append(n, ch.ID)
					}
				}
				return n
			}); err != nil {
				return err
			}
			if a.format == fmtTable {
				var fixes []string
				for _, ch := range r.Checks {
					if ch.Result != api.AuditPass && ch.Fix != "" {
						fixes = append(fixes, "  "+ch.ID+": "+ch.Fix)
					}
				}
				if len(fixes) > 0 {
					fprintf(a.env.Stdout, "\nFix:\n%s\n", strings.Join(fixes, "\n"))
				}
				fprintf(a.env.Stdout, "\njail/%s: %d critical, %d warning(s)\n", r.Jail, r.Critical, r.Warnings)
			}
			if r.Critical > 0 {
				return &exitError{code: ExitFailure}
			}
			return nil
		},
	}
}
