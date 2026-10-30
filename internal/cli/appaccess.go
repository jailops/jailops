package cli

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Who may connect to an installed app: 'app allow', 'app deny' and 'app access'
// (docs/apps.md, "Who may connect").

// installedNames are the jails of the project an app was installed as.
func installedNames(ctx context.Context, c *sdk.Client) ([]string, error) {
	js, err := c.Apps().Installed(ctx)
	var out []string
	for _, j := range js {
		out = append(out, j.Metadata.Name)
	}
	return out, err
}

// completeSource completes the SOURCE of allow and deny: the jails of the
// project and its address sets as @name, the jails of another project after
// @project/, and host.
func (a *app) completeSource() completer {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		defer cancel()
		c, err := a.connect(ctx)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []string
		if p, _, ok := strings.Cut(strings.TrimPrefix(prefix, "@"), "/"); ok && strings.HasPrefix(prefix, "@") {
			pc := c.Project(p)
			for j, err := range pc.Jails().List(ctx) {
				if err != nil {
					break
				}
				out = append(out, "@"+p+"/"+j.Metadata.Name)
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		}
		for j, err := range c.Jails().List(ctx) {
			if err != nil {
				break
			}
			if len(args) == 0 || j.Metadata.Name != args[0] {
				out = append(out, "@"+j.Metadata.Name)
			}
		}
		for s, err := range c.Firewall().AddressSets().List(ctx) {
			if err != nil {
				break
			}
			out = append(out, "@"+s.Metadata.Name)
		}
		out = append(out, "host")
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeAppFromSource completes APP, then the word from, then SOURCE.
func (a *app) completeAppFromSource() completer {
	apps := a.completeNames(installedNames)
	src := a.completeSource()
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return apps(cmd, args, prefix)
		case 1:
			return []string{"from"}, cobra.ShellCompDirectiveNoFileComp
		case 2:
			return src(cmd, args, prefix)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// appFromArgs checks APP from SOURCE.
func appFromArgs(cmd *cobra.Command, args []string) error {
	if len(args) != 3 || args[1] != "from" {
		return usagef("%s APP from SOURCE", cmd.CommandPath())
	}
	return nil
}

func (a *app) appAllow() *cobra.Command {
	var ports []string
	var db string
	var rotate bool
	cmd := &cobra.Command{
		Use:   "allow APP from SOURCE",
		Short: "Let a jail, an address set or an address reach an installed app",
		Long: `An installed app is closed: nothing but the jail itself reaches it until a
source is allowed. 'allow' adds a rule to the app's firewall policy app-APP
(managed by app access) for the ports of the definition, or those of --port.

APP is the jail the app was installed as. SOURCE is one of:
  @JAIL           a jail of the project (its addresses)
  @PROJECT/JAIL   a jail of another project you may use; its own policy gets the
                  rule that lets it out to the app
  @SET            an address set of the project (firewall address-set)
  ADDRESS, PREFIX an address or a prefix, such as 192.0.2.7 or 192.0.2.0/24
  host            the host itself (the gateway addresses of the app's networks)

For an app whose definition has an access block (postgresql), 'allow' also
makes a role and a database named --db (default: the source jail's name, with
- turned into _) with psql run as postgres in the jail, adds one line per
source address to a managed block of pg_hba.conf (scram-sha-256), sets
listen_addresses once to the jail's addresses (a restart of the server) and
prints the connection string with a generated password. The password is shown
once and stored nowhere; --rotate makes a new one. JailOps never changes a role
or a database it did not make.

Allowing again changes nothing unless the ports or the source's addresses
changed. --dry-run prints the rule, the pg_hba line and the SQL, without the
password, and changes nothing.`,
		Example: `jailops app allow db from @web
jailops app allow db from @web --db shop
jailops app allow db from @shop/api --db api
jailops app allow cache from 192.0.2.0/24 --port 6379
jailops app allow web from host
jailops app allow db from @web --rotate
jailops app allow db from @web --dry-run`,
		Args:              appFromArgs,
		ValidArgsFunction: a.completeAppFromSource(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			res, err := c.Apps().Allow(ctx, args[0], api.AppAllow{Source: args[2], Ports: ports, Database: db, Rotate: rotate, DryRun: a.dryRun})
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(res, nil, nil, func() []string { return []string{res.Grant.Source} })
			}
			printAllowed(a.env.Stdout, args[0], res)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&ports, "port", nil, "port N or N/tcp or N/udp to open, repeatable (default: the ports of the definition)")
	f.StringVar(&db, "db", "", "database and role of a postgresql grant (default: the source jail's name)")
	f.BoolVar(&rotate, "rotate", false, "give the grant's role a new password, printed once")
	return cmd
}

func printAllowed(w io.Writer, jail string, res api.AppAllowResult) {
	g := res.Grant
	switch {
	case res.DryRun:
		fprintf(w, "Dry run: nothing was changed. Allowing %s to reach %s would do:\n", g.Source, jail)
	case res.Changed:
		fprintf(w, "Allowed %s to reach %s on %s.\n", g.Source, jail, strings.Join(g.Ports, ", "))
	default:
		fprintf(w, "%s may already reach %s on %s.\n", g.Source, jail, strings.Join(g.Ports, ", "))
	}
	kv := func(k, v string) { fprintf(w, "  %-10s %s\n", k+":", v) }
	for i, r := range g.Rules {
		k := ""
		if i == 0 {
			k = "Rule"
		}
		if k == "" {
			fprintf(w, "  %-10s %s\n", "", r)
		} else {
			kv(k, r+" (policy app-"+jail+")")
		}
	}
	for i, l := range g.HBA {
		if i == 0 {
			kv("pg_hba", l)
		} else {
			fprintf(w, "  %-10s %s\n", "", l)
		}
	}
	if g.Role != "" {
		kv("Database", g.Database+" (role "+g.Role+")")
	}
	for i, s := range res.SQL {
		if i == 0 {
			kv("SQL", s)
		} else {
			fprintf(w, "  %-10s %s\n", "", s)
		}
	}
	if res.DryRun {
		for _, s := range res.Steps {
			fprintln(w, "  - "+s)
		}
	}
	if res.ConnectionString != "" {
		fprintln(w, "Connection string (shown once, stored nowhere):")
		fprintln(w, "  "+res.ConnectionString)
	}
	for _, n := range res.Notes {
		fprintln(w, "Note: "+n)
	}
}

func (a *app) appDeny() *cobra.Command {
	var dropRole bool
	cmd := &cobra.Command{
		Use:   "deny APP from SOURCE",
		Short: "Take back what 'app allow' gave a source",
		Long: `Remove the firewall rule of a grant from the app's policy and, for a
postgresql app, its lines from the managed block of pg_hba.conf. The role
JailOps made for it loses its login and password and keeps its database;
--drop-role drops the role instead (what it owned goes to postgres, the
database is kept). A role another grant uses, or one JailOps did not make, is
left as it is. Denying a source that has no grant changes nothing.`,
		Example: `jailops app deny db from @web
jailops app deny db from @web --drop-role
jailops app deny cache from 192.0.2.0/24 --dry-run`,
		Args:              appFromArgs,
		ValidArgsFunction: a.completeAppFromSource(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			if dropRole && !a.dryRun {
				if err := a.confirm("Drop the role of " + args[2] + " in " + args[0] + "? Its objects are given to postgres."); err != nil {
					return err
				}
			}
			res, err := c.Apps().Deny(ctx, args[0], api.AppDeny{Source: args[2], DropRole: dropRole, DryRun: a.dryRun})
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(res, nil, nil, func() []string { return []string{res.Removed.Source} })
			}
			w := a.env.Stdout
			switch {
			case !res.Changed:
			case res.DryRun:
				fprintf(w, "Dry run: nothing was changed. Denying %s on %s would do:\n", res.Removed.Source, args[0])
			default:
				fprintf(w, "Denied %s on %s.\n", res.Removed.Source, args[0])
			}
			for _, s := range res.Steps {
				fprintln(w, "  - "+s)
			}
			for _, s := range res.SQL {
				fprintln(w, "  SQL: "+s)
			}
			for _, n := range res.Notes {
				fprintln(w, "Note: "+n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dropRole, "drop-role", false, "drop the role JailOps made for the grant (its objects go to postgres; the database is kept)")
	return cmd
}

func (a *app) appAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access [APP]",
		Short: "Show who may connect to installed apps",
		Long: `Without APP, list the grants of every installed app of the project: the app,
the source, the ports and the database. With APP, show the app's grants in full:
each with the firewall rules it made, the lines of the jail's anchor they became,
its pg_hba.conf lines, and what of that is missing on the host (drift, also
reported by jailops doctor as apps.access). A password is never shown here.`,
		Example:           "jailops app access\njailops app access db\njailops app access db -o json",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeArgs(a.completeNames(installedNames), completeNone),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				acc, err := c.Apps().Access(ctx, args[0])
				if err != nil {
					return err
				}
				if a.format != fmtTable {
					return a.emit(acc, nil, nil, func() []string {
						var n []string
						for _, g := range acc.Grants {
							n = append(n, g.Source)
						}
						return n
					})
				}
				printAccess(a.env.Stdout, acc)
				return nil
			}
			js, err := c.Apps().Installed(ctx)
			if err != nil {
				return err
			}
			all := []api.AppAccess{}
			for _, j := range js {
				acc, err := c.Apps().Access(ctx, j.Metadata.Name)
				if err != nil {
					return err
				}
				all = append(all, acc)
			}
			return a.emit(all, nil, func() ([]string, [][]string) {
				var rows [][]string
				for _, acc := range all {
					if len(acc.Grants) == 0 {
						state := "closed"
						if !acc.Closed {
							state = "open (installed before app access)"
						}
						rows = append(rows, []string{acc.Jail, acc.App, "-", "-", "-", state})
					}
					for _, g := range acc.Grants {
						state := "ok"
						if len(g.Problems) > 0 {
							state = "drift"
						}
						rows = append(rows, []string{acc.Jail, acc.App, g.Source, strings.Join(g.Ports, ","), dash(g.Database), state})
					}
				}
				return []string{"APP", "DEFINITION", "SOURCE", "PORTS", "DATABASE", "STATE"}, rows
			}, nil)
		},
	}
	return cmd
}

func printAccess(w io.Writer, acc api.AppAccess) {
	state := "closed: only the sources below may connect"
	if !acc.Closed {
		state = "open to its network"
	}
	fprintf(w, "App %s (%s), jail %s of project %s: %s\n", acc.Jail, acc.App, acc.Jail, acc.Project, state)
	kv := func(k, v string) { fprintf(w, "  %-10s %s\n", k+":", v) }
	kv("Policy", acc.Policy)
	kv("Ports", dash(strings.Join(acc.Ports, ", ")))
	if acc.Type != "" {
		kv("Access", acc.Type+" (a role, a database and pg_hba.conf lines per grant)")
	}
	for _, x := range acc.Warnings {
		fprintln(w, "Warning: "+x)
	}
	if len(acc.Grants) == 0 {
		fprintln(w, "No grants. Allow one with: jailops app allow "+acc.Jail+" from SOURCE")
		return
	}
	for _, g := range acc.Grants {
		fprintf(w, "\n%s (%s)\n", g.Source, g.SourceKind)
		kv("Ports", strings.Join(g.Ports, ", "))
		if len(g.Addresses) > 0 {
			kv("Addresses", strings.Join(g.Addresses, ", "))
		}
		if g.Role != "" {
			made := "made by JailOps"
			if !g.RoleCreated {
				made = "not made by JailOps"
			}
			kv("Database", g.Database+" (role "+g.Role+", "+made+")")
		}
		kv("Created", g.CreatedAt.UTC().Format(time.RFC3339)+" by "+dash(g.CreatedBy))
		lines := func(k string, v []string) {
			for i, l := range v {
				if i == 0 {
					kv(k, l)
				} else {
					fprintf(w, "  %-10s %s\n", "", l)
				}
			}
		}
		lines("Rule", g.Rules)
		lines("PF", g.PF)
		lines("pg_hba", g.HBA)
		lines("Problem", g.Problems)
	}
}
