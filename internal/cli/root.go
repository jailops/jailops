package cli

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

const helpTemplate = `NAME
    {{.CommandPath}} - {{.Short}}

SYNOPSIS
{{- if .Runnable}}
    {{.UseLine}}{{end}}
{{- if .HasAvailableSubCommands}}
    {{.CommandPath}} <command> [flags]{{end}}

DESCRIPTION
{{indent .Long}}
{{- if .HasAvailableSubCommands}}

COMMANDS
{{- range .Commands}}{{if .IsAvailableCommand}}
    {{rpad .Name .NamePadding}}  {{.Short}}{{end}}{{end}}
{{- end}}
{{- if .HasAvailableLocalFlags}}

OPTIONS
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}
{{- end}}
{{- if .HasAvailableInheritedFlags}}

GLOBAL OPTIONS
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}
{{- end}}
{{- if .HasExample}}

EXAMPLES
{{indent .Example}}
{{- end}}
`

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "    " + l
		}
	}
	return strings.Join(lines, "\n")
}

func (a *app) newRoot() *cobra.Command {
	cobra.AddTemplateFunc("indent", indent)
	root := &cobra.Command{
		Use:   "jailops",
		Short: "Manage jails, images, networks and backups",
		Long: `jailops is the command line client of JailOps, a ZFS-native FreeBSD jail platform.
It talks to the jailopsd daemon of this host over its unix socket, or to a remote
daemon or hub over HTTPS with an API token or a client certificate.

Connection settings come, strongest first, from the flags --remote and --host,
the environment (JAILOPS_REMOTE, JAILOPS_URL, JAILOPS_TOKEN, JAILOPS_FINGERPRINT,
JAILOPS_CERT, JAILOPS_KEY, JAILOPS_CA, JAILOPS_HOST, JAILOPS_SOCKET, JAILOPS_CONFIG),
the user file ~/.config/jailops/config.yaml, the host file
/usr/local/etc/jailops/client.yaml, and last the default socket
/var/run/jailops/jailops.sock.

Output is a table for people: short, with borders, on a terminal (-o wide shows
every column, -o compact the plain form), plain and one line per row anywhere
else. Use -o json, -o yaml, -o name or -o csv in scripts; -q leaves out the
confirmations such as "jail/web started". Errors
print as "code: message" with a hint on the next line; 'jailops explain CODE' tells
more. Exit codes: 0 ok, 1 failure, 2 usage or invalid, 3 auth, 4 not found,
5 conflict, 6 unsupported, 7 internal. Colour is used on a terminal only and
NO_COLOR turns it off.`,
		Example: `jailops project create web --new-network
jailops project switch web
jailops jail create web --image freebsd-15@stable --network web --storage default
jailops --remote prod --host host-b jail start web --project web
jailops events --type lifecycle -o json`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.started = true
			a.cmdPath = cmd.CommandPath()
			if !cmd.Flag("output").Changed {
				if v := a.env.Getenv("JAILOPS_OUTPUT"); v != "" {
					a.output = v
				}
			}
			f, err := parseFormat(a.output)
			if err != nil {
				a.started = false
				return err
			}
			switch f {
			case fmtWide: // a table like any other, with every column
				f, a.wide = fmtTable, true
			case fmtCompact: // the plain table, on a terminal too
				f, a.compact = fmtTable, true
			}
			a.format = f
			if projectScoped(cmd) {
				if err := a.requireProject(); err != nil {
					a.started = false
					return err
				}
			}
			return nil
		},
	}
	root.SetHelpTemplate(helpTemplate)
	root.CompletionOptions.DisableDefaultCmd = true
	pf := root.PersistentFlags()
	pf.StringVar(&a.remote, "remote", "", "named remote, or an https URL, to talk to (default: the current remote, else the local socket)")
	pf.StringVar(&a.host, "host", "", "scope requests to this host (name or UUID) behind a hub")
	pf.StringVar(&a.project, "project", "", "project to act in (default: the current project of the remote, chosen with 'project switch'; there is no default project)")
	pf.StringVarP(&a.output, "output", "o", "table", "output format: table, wide, compact, json, yaml, name or csv")
	pf.BoolVar(&a.dryRun, "dry-run", false, "show what would change and send nothing that changes state")
	pf.BoolVar(&a.wait, "wait", true, "wait for asynchronous operations to finish")
	pf.BoolVar(&a.noWait, "no-wait", false, "do not wait; print the operation id")
	pf.DurationVar(&a.timeout, "timeout", 0, "how long to wait for an operation (default 10m)")
	pf.BoolVarP(&a.yes, "yes", "y", false, "answer yes to confirmations (never to data-loss confirmations)")
	pf.StringVar(&a.configPath, "config", "", "user configuration file (default ~/.config/jailops/config.yaml)")
	pf.BoolVar(&a.noHeaders, "no-headers", false, "omit table headers")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "print no confirmations (errors and requested output still print)")
	enumFlag(root, "output", formatNames...)
	_ = root.RegisterFlagCompletionFunc("project", a.completeProjects())
	_ = root.RegisterFlagCompletionFunc("remote", a.remoteNames())
	_ = root.RegisterFlagCompletionFunc("host", a.completeHosts())
	root.AddCommand(
		a.jailCmd(), a.imageCmd(), a.networkCmd(), a.firewallCmd(), a.backupCmd(),
		a.projectCmd(), a.storageCmd(), a.trustCmd(), a.sessionsCmd(), a.whoamiCmd(), a.opCmd(), a.eventsCmd(), a.tokenCmd(), a.certCmd(), a.userCmd(), a.hostCmd(),
		a.remoteCmd(), a.auditCmd(), a.doctorCmd(), a.serverCmd(), a.explainCmd(), a.completionCmd(), a.versionCmd(), a.manCmd(),
	)
	a.addJailShortcuts(root)
	root.AddCommand(a.configCmd(), a.profileCmd(), a.updateCmd(), a.setAlias(a.jailDesc()), a.applyCmd())
	markProjectScoped(root)
	expandExamples(root)
	return root
}

// expandExamples replaces @P@ in examples with the path of the command's
// parent, so shared builders can write examples for any group.
func expandExamples(c *cobra.Command) {
	if strings.Contains(c.Example, "@P@") && c.Parent() != nil {
		p := strings.TrimPrefix(c.Parent().CommandPath(), "jailops")
		p = strings.TrimPrefix(p, " ")
		if p == "" {
			c.Example = strings.ReplaceAll(c.Example, "@P@ ", "")
		}
		c.Example = strings.ReplaceAll(c.Example, "@P@", p)
	}
	for _, s := range c.Commands() {
		expandExamples(s)
	}
}

func group(use, short, long, example string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Long: long, Example: example, Aliases: aliases,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
}

// timeoutSeconds converts a duration to whole seconds, at least 1 when set.
func seconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return max(int(d/time.Second), 1)
}

// projectScopedCommands are the commands that act inside a project. They stop before anything is sent when no
// project was chosen: there is no default project. Host-level commands (images, networks, storage, projects,
// trust, backup targets and keys, the server) are not here.
var projectScopedCommands = []string{
	"jail", "create", "delete", "list", "info", "start", "stop", "restart", "exec", "shell", "label", "provision", "set",
	"firewall", "profile", "update", "config", "backup policy", "backup restore",
}

const projectAnnotation = "jailops/project"

// markProjectScoped tags the commands above (and everything below them).
func markProjectScoped(root *cobra.Command) {
	for _, path := range projectScopedCommands {
		if c, _, err := root.Find(strings.Fields(path)); err == nil && c != root {
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[projectAnnotation] = "required"
		}
	}
	// The list of configuration keys is the same for every project.
	if c, _, err := root.Find([]string{"config", "keys"}); err == nil && c.Name() == "keys" {
		c.Annotations = map[string]string{projectAnnotation: "none"}
	}
}

// projectScoped reports whether cmd (or the group it is in) acts inside a project.
func projectScoped(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Annotations[projectAnnotation] {
		case "none":
			return false
		case "required":
			return true
		}
	}
	return false
}

// requireProject refuses, before anything is sent, a command that needs a project when none was chosen.
func (a *app) requireProject() error {
	conn, err := a.resolveConnection()
	if err != nil {
		return err
	}
	if conn.Project != "" {
		return nil
	}
	return api.Errorf(api.CodeInvalid, "a project is required").
		WithHint("name one with --project NAME, or choose one with 'jailops project switch NAME' (list them with 'jailops project list'; create one with 'jailops project create NAME --new-network')")
}
