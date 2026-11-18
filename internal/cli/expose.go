package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// exposeResult prints the answer of expose and unexpose as data (-o json, yaml, name).
func (a *app) exposeResult(r api.ExposeResult) error {
	return a.emit(r, nil, nil, func() []string { return []string{r.Jail} })
}

// printExpose says what the Internet reaches afterwards, one line per address of the jail.
func (a *app) printExpose(r api.ExposeResult, did string) {
	w := a.env.Stdout
	if !r.Changed {
		fprintf(w, "jail/%s: the exposure is already what you asked for\n", r.Jail)
	} else {
		fprintf(w, "jail/%s %s\n", r.Jail, did)
	}
	for _, f := range r.Families {
		switch {
		case f.Note != "":
			fprintf(w, "  IPv%s  %s\n", f.Family, f.Note)
		case f.Via == "forward":
			on := ""
			if f.Listen != "" {
				on = " on " + f.Listen
			}
			fprintf(w, "  IPv%s  %s   forwarded: host %s%s (NAT)\n", f.Family, f.Address, strings.Join(f.Forwards, ", "), on)
		case f.Via == "uplink":
			fprintf(w, "  IPv%s  %s   open: no NAT, the jail's own address answers on %s\n", f.Family, f.Address, f.Ports)
		default:
			fprintf(w, "  IPv%s  %s   network %s has no uplink, nothing arrives from outside\n", f.Family, f.Address, f.Network)
		}
	}
	for _, n := range r.Notes {
		fprintf(w, "note: %s\n", n)
	}
	for _, x := range r.Warnings {
		fprintf(a.env.Stderr, "warning: %s\n", x)
	}
}

// printUnexpose says what is left of the exposure.
func (a *app) printUnexpose(r api.ExposeResult) {
	w := a.env.Stdout
	switch {
	case !r.Changed:
		fprintf(w, "jail/%s: nothing to remove\n", r.Jail)
	case a.dryRun:
		fprintf(w, "jail/%s: would be changed (dry run, nothing stored)\n", r.Jail)
	default:
		fprintf(w, "jail/%s: no longer exposed\n", r.Jail)
	}
	for _, n := range r.Notes {
		fprintf(w, "note: %s\n", n)
	}
	for _, e := range r.Entries {
		fams := "IPv4 and IPv6"
		switch {
		case len(e.Families) == 1 && e.Families[0] == "4":
			fams = "IPv4"
		case len(e.Families) == 1 && e.Families[0] == "6":
			fams = "IPv6"
		}
		fprintf(w, "  still exposed: %s %s (%s)\n", e.Protocol(), strings.Join(e.Ports, ","), fams)
	}
}

func (a *app) exposeCmd() *cobra.Command {
	var req api.ExposeRequest
	var ports string
	var protos []string
	cmd := &cobra.Command{
		Use:   "expose JAIL PORTS",
		Short: "Let the Internet reach ports of a jail",
		Long: `Opens the ports of a jail to the Internet in one step. Over IPv6, and over IPv4 on a network
without NAT, the ports are let in to the jail's own address. Over IPv4 on a network with NAT, the same ports of the
host are forwarded to the jail. The jail's firewall policies are not changed: the exposure is stored with the jail and
rendered as rules the firewall views name "expose". See it with 'jailops list' (EXPOSED column) and 'jailops firewall exposed'.

PORTS is a port, a list 80,443, a range 8000-8100 or a mix of them. A range is let in; a jail behind NAT forwards single
ports only, so a range there needs --family 6. Without --from anyone may connect; --from takes addresses, prefixes and
set:NAME. Without --family every family the jail has is exposed. The IPv4 forward needs a global administrator (it opens
a port of the host); IPv6 does not. A port is exposed once per protocol: exposing it again replaces what the jail said
about it before.`,
		Example: `jailops expose web 80,443
jailops expose mail 25,465,587,993 --comment "mail"
jailops expose web 443 --from set:office
jailops expose web 80 --family 6
jailops expose app 3000 --host-port 8080
jailops expose --dry-run web 80,443`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeArgs(completeKind(a, a.jailDesc()), portCompleter),
		RunE: func(cmd *cobra.Command, args []string) error {
			ports = args[1]
			for _, p := range strings.Split(ports, ",") {
				if p = strings.TrimSpace(p); p != "" {
					req.Ports = append(req.Ports, p)
				}
			}
			req.DryRun = a.dryRun
			req.Proto = strings.Join(protos, ",")
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			r, err := c.Jails().Expose(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.exposeResult(r)
			}
			what := r.Exposed
			if what == "" {
				what = strings.Join(req.Ports, ",")
			}
			did := "exposed: " + what
			if a.dryRun {
				did = "would expose: " + what + " (dry run, nothing stored)"
			}
			a.printExpose(r, did)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&protos, "proto", nil, "tcp (default), udp or tcp,udp (repeatable, or comma separated)")
	f.StringSliceVar(&req.From, "from", nil, "who may connect: an address, a CIDR or set:NAME (repeatable, or comma separated; default: anyone)")
	f.StringSliceVar(&req.Families, "family", nil, "4 or 6 (default: every family the jail has)")
	f.IntVar(&req.HostPort, "host-port", 0, "host port of the IPv4 forward when it differs from the jail's port (one port only)")
	f.StringVar(&req.Listen, "listen", "", "host address of the IPv4 forward (default: the uplink's address)")
	f.StringVar(&req.Comment, "comment", "", "what it is for (one line): what a reviewer of the firewall reads")
	enumFlag(cmd, "proto", "tcp", "udp", "tcp,udp")
	enumFlag(cmd, "family", "4\tIPv4", "6\tIPv6")
	_ = cmd.RegisterFlagCompletionFunc("listen", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return cobra.AppendActiveHelp(nil, "an address of the host, for the IPv4 forward (default: the uplink's address)"), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("host-port", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return cobra.AppendActiveHelp(nil, "the host's port, when it differs from the jail's"), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("comment", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return cobra.AppendActiveHelp(nil, "what it is for, in one line"), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("from", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return cobra.AppendActiveHelp(nil, "an address, a CIDR or set:NAME"), cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func (a *app) unexposeCmd() *cobra.Command {
	var req api.UnexposeRequest
	var protos []string
	cmd := &cobra.Command{
		Use:   "unexpose JAIL [PORTS]",
		Short: "Stop letting the Internet reach ports of a jail",
		Long: `Takes ports out of the exposure of a jail (the IPv4 forwards of them go too). Without PORTS the whole
exposure goes; --proto and --family narrow what is removed. Removing a forward needs a global administrator.
Only the exposure goes: a rule of a policy that lets the port in stays, and the answer names it.`,
		Example: `jailops unexpose web 80
jailops unexpose web
jailops unexpose web 443 --family 6`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeArgs(completeKind(a, a.jailDesc()), portCompleter),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				for _, p := range strings.Split(args[1], ",") {
					if p = strings.TrimSpace(p); p != "" {
						req.Ports = append(req.Ports, p)
					}
				}
			}
			req.DryRun = a.dryRun
			req.Proto = strings.Join(protos, ",")
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			r, err := c.Jails().Unexpose(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.exposeResult(r)
			}
			a.printUnexpose(r)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&protos, "proto", nil, "tcp, udp or tcp,udp (default: every protocol; repeatable, or comma separated)")
	f.StringSliceVar(&req.Families, "family", nil, "4 or 6 (default: every family)")
	enumFlag(cmd, "proto", "tcp", "udp", "tcp,udp")
	enumFlag(cmd, "family", "4\tIPv4", "6\tIPv6")
	return cmd
}
