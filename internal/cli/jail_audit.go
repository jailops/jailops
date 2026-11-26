package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// jailAudit is 'jail audit NAME': the read-only audit of a FreeBSD jail (docs/audit.md).
func (a *app) jailAudit(d *kindDesc[api.Jail]) *cobra.Command {
	var suggest bool
	cmd := &cobra.Command{
		Use:   "audit NAME",
		Short: "Look at a jail for security problems, read-only",
		Long: `Look at a FreeBSD jail, running or stopped, and print one line per check: PASS, WARN or CRITICAL, what
was found and the fix (INFO says a check could not be made, and why). Nothing in the jail changes, and the jail is
not stopped or started: the root helper reads the jail's root on the host.

The checks say what the jail shows and what FreeBSD's documentation names as practice (the Handbook's chapters on
jails and security, the CIS FreeBSD Benchmark): a link out of the root where the host's pkg writes, a link among
pkg's own files, a pkg.conf with plugins (CRITICAL, as for an upgrade); an account with uid 0 besides root and toor
(CRITICAL); an account with an empty password; set-uid or set-gid files no package installed; directories anyone may
write to without the sticky bit; files anyone may write to outside /tmp; files whose owner or group the jail does
not have; an enabled package repository that is neither signed nor fetched over https; FreeBSD-base enabled inside the jail; packages with known
vulnerabilities (pkg audit, with the host's vulnerability database); newer base packages the last update check
found; programs or libraries of the base that differ from their packages; sshd allowing root logins or passwords;
enforce_statfs below 2.

--suggest adds the suggestions, marked as such and never counted as a finding: periodic's reports and its
world/kernel comparison, cron jitter, rc.conf knobs that do nothing in a jail, root locked, root's shell and prompt,
the JailOps block of /etc/hosts, volumes mounted nosuid, the disk limit and the size of the root, securelevel, the
allow.* parameters and the devfs ruleset.

The exit status is 0 when no check is CRITICAL (WARN included) and 1 when one is. Operator in the project.`,
		Example: `jailops jail audit web
jailops jail audit web --suggest
jailops jail audit web -o json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			r, err := c.Jails().AuditWith(ctx, args[0], sdk.AuditOptions{Suggest: suggest})
			if err != nil {
				return err
			}
			if err := a.emit(r, nil, func() ([]string, [][]string) {
				rows := make([][]string, len(r.Checks))
				for i, ch := range r.Checks {
					detail := ch.Detail
					if ch.Suggestion {
						detail = "suggestion: " + detail
					}
					rows[i] = []string{ch.ID, strings.ToUpper(string(ch.Result)), detail}
				}
				return []string{"CHECK", "RESULT", "DETAIL"}, rows
			}, func() []string {
				n := make([]string, 0, len(r.Checks))
				for _, ch := range r.Checks {
					if !ch.Suggestion && (ch.Result == api.AuditWarn || ch.Result == api.AuditCritical) {
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
						id := ch.ID
						if ch.Suggestion {
							id += " (suggestion)"
						}
						fixes = append(fixes, "  "+id+": "+ch.Fix)
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
	cmd.Flags().BoolVar(&suggest, "suggest", false, "add the suggestions: notes on how the jail runs better on JailOps, marked as such")
	return cmd
}
