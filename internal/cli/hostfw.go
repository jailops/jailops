package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// The host firewall module and commit confirmed (docs/appliance.md): 'host firewall show|set|apply|diff' and
// 'host pending|confirm|revert'. Every firewall change waits for a confirmation and reverts itself without one.

// confirmWithin turns --confirm-within (seconds, or a duration such as 90s or 5m) into seconds.
func confirmWithin(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		d, derr := time.ParseDuration(v)
		if derr != nil || d%time.Second != 0 {
			return 0, usagef("--confirm-within is seconds or a duration such as 120s or 5m, not %q", v)
		}
		n = int(d / time.Second)
	}
	if _, err := api.ConfirmWindow(n); err != nil || n == 0 {
		return 0, usagef("--confirm-within is %d to %d seconds, not %q", api.MinConfirmWithin, api.MaxConfirmWithin, v)
	}
	return n, nil
}

// parseAllowRow reads one --allow: "PROTO PORTS [from SOURCES] [comment TEXT]", PORTS and SOURCES comma separated.
func parseAllowRow(v string) (api.HostFirewallAllow, error) {
	f := strings.Fields(v)
	bad := usagef("--allow is \"PROTO PORTS [from SOURCE,...] [comment TEXT]\", for example \"tcp 80,443 from 192.0.2.0/24 comment web\", not %q", v)
	if len(f) < 2 {
		return api.HostFirewallAllow{}, bad
	}
	row := api.HostFirewallAllow{Proto: f[0], Ports: strings.Split(f[1], ",")}
	rest := f[2:]
	if len(rest) >= 2 && rest[0] == "from" {
		row.From = strings.Split(rest[1], ",")
		rest = rest[2:]
	}
	if len(rest) > 0 {
		if rest[0] != "comment" || len(rest) < 2 {
			return api.HostFirewallAllow{}, bad
		}
		row.Comment = strings.Join(rest[1:], " ")
	}
	return row, nil
}

// sources reads a repeated source flag; "any" alone means anywhere.
func sources(v []string) []string {
	var out []string
	for _, s := range v {
		for _, x := range strings.Split(s, ",") {
			if x = strings.TrimSpace(x); x != "" && x != "any" {
				out = append(out, x)
			}
		}
	}
	return out
}

func (a *app) hostFirewallCmd() *cobra.Command {
	fc := group("firewall", "Manage this host's packet filter (/etc/pf.conf)",
		`The firewall module: with it enabled in jailopsd.yaml and priv.conf, JailOps owns
/etc/pf.conf as a whole file, rendered by the privileged helper from a few
settings: a template (default, open or custom-rules), the ssh ports and who may
use them, who may reach the console, more ports to open (custom-rules), address
sets, ping and logging. The jails' anchor lines are always in it, as are
neighbour discovery and path MTU discovery. Every change reverts itself unless
it is confirmed in time ('jailops host confirm'); a change that would shut out
the address the request comes from is refused. Without the module JailOps never
touches pf.conf.`,
		"jailops host firewall show\njailops host firewall set --ssh-from 192.0.2.0/24 --dry-run\njailops host firewall apply -f firewall.yaml --confirm-within 5m")
	fc.AddCommand(a.hostFirewallShowCmd(), a.hostFirewallSetCmd(), a.hostFirewallApplyCmd(), a.hostFirewallDiffCmd())
	return fc
}

func (a *app) hostFirewallShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the host firewall: what was applied, its rules, drift and pf",
		Long: `Show the settings JailOps applied (or, before any apply, the default it would
apply), the rules they make in plain words, whether pf runs, drift and the
change that waits for a confirmation. -o json and -o yaml print the object.`,
		Example: "jailops host firewall show\njailops host firewall show -o yaml",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			fw, err := c.HostSystem().Firewall().Get(cmd.Context())
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(fw, nil, nil, func() []string { return []string{fw.Spec.Template} })
			}
			w, st := a.env.Stdout, fw.Status
			a.printTable(w, []string{"ACTION", "DIR", "PROTO", "PORTS", "FROM", "WHAT"}, fwRuleRows(fw.Status.Rules), nil)
			fprintln(w)
			managed := "no"
			switch {
			case st.Managed:
				managed = "yes (module firewall)"
			case st.ConfigEnabled:
				managed = "no: jailopsd.yaml enables firewall, priv.conf does not (host-manage = firewall)"
			case st.HelperEnabled:
				managed = "no: priv.conf enables firewall, jailopsd.yaml does not (host.manage.modules)"
			}
			fprintf(w, "Managed:     %s\n", managed)
			fprintf(w, "Template:    %s\n", fw.Spec.Template)
			pf := "unknown (pfctl did not answer)"
			if st.PFEnabled != nil {
				pf = map[bool]string{true: "running", false: "not running"}[*st.PFEnabled]
			}
			fprintf(w, "pf:          %s\n", pf)
			file := "absent"
			if st.FileExists {
				file = "the administrator's"
				if st.FileManaged {
					file = "managed by jailops"
				}
			}
			fprintf(w, "pf.conf:     %s\n", file)
			if st.AppliedAt != nil {
				fprintf(w, "Applied:     %s\n", st.AppliedAt.UTC().Format("2006-01-02 15:04:05Z"))
			}
			if len(st.Drift) > 0 {
				fprintf(w, "Drift:       %s (changed outside JailOps; see 'jailops host firewall diff')\n", strings.Join(st.Drift, ", "))
			} else if st.Applied != nil {
				fprintf(w, "Drift:       none\n")
			}
			if p := st.Pending; p != nil {
				fprintf(w, "Pending:     %s change %s reverts at %s unless confirmed (jailops host confirm)\n", p.Module, p.ID, p.Deadline.UTC().Format("15:04:05Z"))
			}
			for _, warn := range st.Warnings {
				fprintf(w, "Warning:     %s\n", warn)
			}
			if !st.Managed {
				fprintln(w, "\nJailOps does not touch /etc/pf.conf. To let it manage the firewall, add to")
				fprintln(w, "/usr/local/etc/jailops/jailopsd.yaml:")
				fprintln(w, "  host:\n    manage:\n      modules: [firewall]")
				fprintln(w, "and to /usr/local/etc/jailops/priv.conf:")
				fprintln(w, "  host-manage = firewall")
				fprintln(w, "then: service jailops_priv restart && service jailopsd restart")
			}
			return nil
		},
	}
}

func fwRuleRows(rules []api.HostFirewallRule) [][]string {
	var rows [][]string
	for _, r := range rules {
		what := r.Comment
		if r.Fixed {
			what += " (always)"
		}
		rows = append(rows, []string{r.Action, r.Direction, dash(r.Proto), dash(r.Ports), dash(r.From), dash(what)})
	}
	return rows
}

// fwApplyFlags are the flags every firewall change has.
type fwApplyFlags struct {
	within string
	iKnow  bool
}

func (f *fwApplyFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.within, "confirm-within", "", fmt.Sprintf("the change reverts unless confirmed within this time (seconds or 90s, 5m; default %ds, at most %ds)", api.DefaultConfirmWithin, api.MaxConfirmWithin))
	cmd.Flags().BoolVar(&f.iKnow, "i-know", false, "apply although the address of this request is unknown (the unix socket, a hub), so the lock-out check cannot run")
}

// applyFirewall sends a spec and prints the result.
func (a *app) applyFirewall(ctx context.Context, c *sdk.Client, spec api.HostFirewallSpec, f fwApplyFlags) error {
	within, err := confirmWithin(f.within)
	if err != nil {
		return err
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	ch, err := c.HostSystem().Firewall().Apply(ctx, api.HostFirewallApply{Spec: spec, DryRun: a.dryRun, ConfirmWithin: within, IKnow: f.iKnow})
	if err != nil {
		return err
	}
	return a.printHostChange("host firewall", ch)
}

func (a *app) hostFirewallSetCmd() *cobra.Command {
	var template, icmp, logging string
	var sshPorts []int
	var sshFrom, consoleFrom, allow, sets []string
	var clearAllow bool
	var af fwApplyFlags
	cmd := &cobra.Command{
		Use:   "set [flags]",
		Short: "Change the host firewall from flags (admin)",
		Long: `Start from what was applied last (or the default) and change what the flags
name: --template, --ssh-port and --ssh-from, --console-from (sources are
addresses, prefixes or @set; "any" means anywhere), --allow rows for
custom-rules ("PROTO PORTS [from SOURCE,...] [comment TEXT]"; the flag replaces
all rows, --clear-allow removes them), --set NAME=MEMBER,... (replaces all sets),
--icmp and --logging. --dry-run shows the diff of /etc/pf.conf and the rules.
The change reverts itself unless 'jailops host confirm' follows within
--confirm-within.`,
		Example: `jailops host firewall set --ssh-from 192.0.2.0/24 --ssh-from 2001:db8:a::/48 --dry-run
jailops host firewall set --template custom-rules --allow "tcp 80,443 comment web" --confirm-within 5m
jailops host firewall set --set admins=192.0.2.0/24,2001:db8:a::/48 --ssh-from @admins --console-from @admins`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl := cmd.Flags()
			for _, x := range []struct{ name, v string }{{"icmp", icmp}, {"logging", logging}} {
				if x.v != "" && x.v != "enable" && x.v != "disable" {
					return usagef("--%s is enable or disable, not %q", x.name, x.v)
				}
			}
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			cur, err := c.HostSystem().Firewall().Get(ctx)
			if err != nil {
				return err
			}
			spec := cur.Spec
			if fl.Changed("template") {
				spec.Template = template
			}
			if spec.SSH == nil {
				spec.SSH = &api.HostFirewallSSH{}
			}
			if spec.Console == nil {
				spec.Console = &api.HostFirewallConsole{}
			}
			spec.Console.Port = 0 // the daemon's own
			if fl.Changed("ssh-port") {
				spec.SSH.Ports = sshPorts
			}
			if fl.Changed("ssh-from") {
				spec.SSH.AllowFrom = sources(sshFrom)
			}
			if fl.Changed("console-from") {
				spec.Console.AllowFrom = sources(consoleFrom)
			}
			if clearAllow {
				spec.Allow = nil
			}
			if fl.Changed("allow") {
				spec.Allow = nil
				for _, v := range allow {
					row, err := parseAllowRow(v)
					if err != nil {
						return err
					}
					spec.Allow = append(spec.Allow, row)
				}
			}
			if fl.Changed("set") {
				spec.Sets = nil
				for _, v := range sets {
					name, members, ok := strings.Cut(v, "=")
					if !ok || members == "" {
						return usagef("--set is NAME=MEMBER,MEMBER..., not %q", v)
					}
					spec.Sets = append(spec.Sets, api.HostFirewallSet{Name: name, Members: strings.Split(members, ",")})
				}
			}
			if icmp != "" {
				spec.ICMP = icmp == "enable"
			}
			if logging != "" {
				spec.Logging = logging == "enable"
			}
			return a.applyFirewall(ctx, c, spec, af)
		},
	}
	f := cmd.Flags()
	f.StringVar(&template, "template", "", "default, open or custom-rules")
	enumFlag(cmd, "template", api.HostFirewallTemplates...)
	f.IntSliceVar(&sshPorts, "ssh-port", nil, "a port sshd listens on (repeatable, at most 4; replaces the list)")
	f.StringArrayVar(&sshFrom, "ssh-from", nil, "who may use ssh: an address, a prefix, @set or any (repeatable; replaces the list)")
	f.StringArrayVar(&consoleFrom, "console-from", nil, "who may reach the console and the API (repeatable; replaces the list)")
	f.StringArrayVar(&allow, "allow", nil, `a row of custom-rules: "PROTO PORTS [from SOURCE,...] [comment TEXT]" (repeatable; replaces the rows)`)
	f.BoolVar(&clearAllow, "clear-allow", false, "remove every allow row")
	f.StringArrayVar(&sets, "set", nil, "an address set: NAME=MEMBER,MEMBER... (repeatable; replaces the sets)")
	f.StringVar(&icmp, "icmp", "", "enable or disable ping (neighbour discovery and path MTU discovery stay on)")
	enumFlag(cmd, "icmp", "enable", "disable")
	f.StringVar(&logging, "logging", "", "enable or disable logging what the final block rule drops")
	enumFlag(cmd, "logging", "enable", "disable")
	af.add(cmd)
	return cmd
}

func (a *app) hostFirewallApplyCmd() *cobra.Command {
	var file string
	var af fwApplyFlags
	cmd := &cobra.Command{
		Use:   "apply -f FILE",
		Short: "Apply a host firewall from a YAML or JSON file (admin)",
		Long: `Apply the HostFirewallSpec in FILE (a spec, or a document with a spec key such as
'jailops host firewall show -o yaml' prints). The whole spec is applied: a part
left out takes its default. The change reverts itself unless 'jailops host
confirm' follows within --confirm-within.`,
		Example: "jailops host firewall show -o yaml > fw.yaml\njailops host firewall apply -f fw.yaml --dry-run\njailops host firewall apply -f fw.yaml --confirm-within 300",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return usagef("-f FILE is required")
			}
			spec, err := a.readFirewallSpec(file)
			if err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			return a.applyFirewall(cmd.Context(), c, spec, af)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "YAML or JSON file with the spec, or - for standard input")
	af.add(cmd)
	return cmd
}

// readFirewallSpec reads a spec, or a HostFirewall document, from a file.
func (a *app) readFirewallSpec(file string) (api.HostFirewallSpec, error) {
	b, err := a.readInput(file)
	if err != nil {
		return api.HostFirewallSpec{}, err
	}
	docs, err := readDocs(b)
	if err != nil {
		return api.HostFirewallSpec{}, err
	}
	if len(docs) != 1 {
		return api.HostFirewallSpec{}, usagef("the file holds %d documents, not one firewall spec", len(docs))
	}
	doc := docs[0]
	if s, ok := doc["spec"]; ok {
		if k, ok := doc["kind"]; ok && k != string(api.KindHostFirewall) {
			return api.HostFirewallSpec{}, usagef("the document is a %v, not a HostFirewall", k)
		}
		doc, _ = s.(map[string]any)
	}
	var spec api.HostFirewallSpec
	if err := decodeStrict(doc, &spec); err != nil {
		return spec, err
	}
	if spec.Console != nil {
		spec.Console.Port = 0 // the daemon's own
	}
	return spec, nil
}

func (a *app) hostFirewallDiffCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "diff [-f FILE]",
		Short: "Show how /etc/pf.conf differs from what JailOps would write",
		Long: `Without -f: render what was applied last again and show how /etc/pf.conf differs
from it (a hand edit shows as drift). With -f: the same for the spec in FILE.
Nothing changes; it is the dry run of an apply.`,
		Example: "jailops host firewall diff\njailops host firewall diff -f fw.yaml",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			var spec api.HostFirewallSpec
			if file != "" {
				if spec, err = a.readFirewallSpec(file); err != nil {
					return err
				}
			} else {
				cur, err := c.HostSystem().Firewall().Get(ctx)
				if err != nil {
					return err
				}
				spec = cur.Spec
				if spec.Console != nil {
					spec.Console.Port = 0
				}
			}
			ch, err := c.HostSystem().Firewall().Apply(ctx, api.HostFirewallApply{Spec: spec, DryRun: true, IKnow: true})
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(ch, nil, nil, nil)
			}
			if !ch.Changed {
				a.say("host firewall: /etc/pf.conf is what JailOps would write\n")
				return nil
			}
			fprintf(a.env.Stdout, "%s", ch.Diff)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "YAML or JSON file with the spec, or - for standard input")
	return cmd
}

func (a *app) printPending(p api.HostPending) error {
	if a.format != fmtTable {
		return a.emit(p, nil, nil, func() []string {
			if p.Pending != nil {
				return []string{p.Pending.ID}
			}
			return nil
		})
	}
	w := a.env.Stdout
	if c := p.Pending; c != nil {
		fprintf(w, "Pending:     %s change %s (snapshot %s)\n", c.Module, c.ID, c.Snapshot)
		fprintf(w, "Reverts at:  %s, in %d s, unless confirmed: jailops host confirm --id %s\n", c.Deadline.UTC().Format("2006-01-02 15:04:05Z"), c.SecondsLeft, c.ID)
	} else {
		fprintln(w, "Pending:     nothing waits for a confirmation")
	}
	if o := p.Last; o != nil {
		fprintf(w, "Last:        %s change %s was %s at %s", o.Module, o.ID, strings.ReplaceAll(o.Outcome, "_", " "), o.Time.UTC().Format("2006-01-02 15:04:05Z"))
		if o.Detail != "" {
			fprintf(w, " (%s)", o.Detail)
		}
		fprintln(w)
	}
	return nil
}

func (a *app) completePendingID() completer {
	return a.completeWith(func(ctx context.Context, c *sdk.Client, _ []string) ([]string, error) {
		p, err := c.HostSystem().Pending(ctx)
		if err != nil || p.Pending == nil {
			return nil, err
		}
		return []string{p.Pending.ID}, nil
	})
}

func (a *app) hostPendingCmds() []*cobra.Command {
	pending := &cobra.Command{
		Use:   "pending",
		Short: "Show the host change that waits for its confirmation",
		Long: `Show the host change (module, id, snapshot, deadline) that reverts itself unless
it is confirmed, and how the last one ended. The deadline is kept by the
privileged helper: neither a restart of the daemon nor a lost connection stops
the revert.`,
		Example: "jailops host pending\njailops host pending -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			p, err := c.HostSystem().Pending(cmd.Context())
			if err != nil {
				return err
			}
			return a.printPending(p)
		},
	}
	act := func(use, short, long, verb string, confirm bool) *cobra.Command {
		var id string
		cmd := &cobra.Command{
			Use: use, Short: short, Long: long,
			Example: "jailops host " + verb + "\njailops host " + verb + " --id 0123456789abcdef\njailops --dry-run host " + verb,
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := api.ValidHostPendingID(id); err != nil {
					return err
				}
				ctx := cmd.Context()
				c, err := a.connect(ctx)
				if err != nil {
					return err
				}
				if a.dryRun {
					p, err := c.HostSystem().Pending(ctx)
					if err != nil {
						return err
					}
					if p.Pending == nil {
						return api.Errorf(api.CodeNotFound, "no host change waits for a confirmation")
					}
					fprintf(a.env.Stdout, "dry-run: would %s the %s change %s\n", verb, p.Pending.Module, p.Pending.ID)
					return nil
				}
				var p api.HostPending
				if confirm {
					p, err = c.HostSystem().Confirm(ctx, id)
				} else {
					if err := a.confirm("Put the snapshot of the pending host change back now?"); err != nil {
						return err
					}
					p, err = c.HostSystem().Revert(ctx, id)
				}
				if err != nil {
					return err
				}
				return a.printPending(p)
			},
		}
		cmd.Flags().StringVar(&id, "id", "", "the id of the pending change (default: whichever waits)")
		_ = cmd.RegisterFlagCompletionFunc("id", a.completePendingID())
		return cmd
	}
	return []*cobra.Command{pending,
		act("confirm [--id ID]", "Keep the host change that waits for its confirmation (admin)",
			`Keep the pending change: the helper stops its timer and the change stays. Send it
through the new configuration (over the new firewall rules), which proves that
you can still reach the host.`, "confirm", true),
		act("revert [--id ID]", "Undo the host change that waits for its confirmation, now (admin)",
			`Put the snapshot of the pending change back now instead of at its deadline, and
make it take effect (pfctl -f).`, "revert", false),
	}
}
