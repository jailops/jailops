package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// The exit status of 'firewall test': 0 when the connection would pass, ExitBlocked when it would be blocked, and
// ExitUndecided when the test cannot say (a raw PF fragment, an ICMP type, a packet no rule of the anchor matches).
// Every other status is an error of the command, as for every other command, so that a script can tell a blocked
// connection from a failed test.
const (
	ExitUndecided = 8
	ExitBlocked   = 9
)

func (a *app) firewallTest() *cobra.Command {
	var from, to, proto, icmpType, family string
	var verbose bool
	var port int
	cmd := &cobra.Command{
		Use:   "test --from SOURCE --to DESTINATION [--port N]",
		Short: "Say whether a connection would pass the firewall, and which rule decides",
		Long: `Say whether a new connection from one end to the other would pass the firewall, and
which rule decides at each anchor it meets. SOURCE and DESTINATION are a jail of the
project (jail:web), one address, internet (a source only: any address outside your networks, in IPv4
and IPv6), or host (the host's address on the gateway of the other end's
network). A set is not an end: name a jail or an address. An address that belongs to a
jail of the project is that jail. Another project is an external host: its jails are
addresses here (a global administrator who names one gets that jail's own anchor evaluated too).

The test walks the anchors JailOps renders and loads: what the source jail sends is judged
on its own port, what reaches the destination jail leaves the host on the gateway interface
of its network. Each hop prints the rule that decided: the policy, the number of the rule,
its name and comment, and the rendered pf line. For a connection between two jails both
hops must pass. A connection from an address outside every network of JailOps to a jail's
routed address meets the jail's anchor on the uplink first: when no rule of the jail admits it
there, it is blocked (an "allow in" rule with an external source opens the uplink).
--from internet asks what the whole Internet reaches: an IPv6 address needs such a rule, an IPv4 address
is behind NAT and needs a forward of a port of the host ('jailops expose').

It does not cover what it cannot know, and says so: a connection that is already open (pf
keeps its state), the host's own pf.conf (a quick rule of yours before the anchor line wins:
jailops doctor, pf.inbound), outbound NAT, and a raw PF fragment, which makes the answer unknown. The ICMP tests default to an echo request (ping); --icmp-type names another.

Exit status: 0 the connection would pass, 9 it would be blocked, 8 the test cannot say; 1 to 7 are errors, as for every command.`,
		Example: `jailops firewall test --from jail:web --to jail:db --port 5432
jailops firewall test --from jail:web --to jail:db --port 5433
jailops firewall test --from 192.0.2.7 --to jail:web --port 443
jailops firewall test --from internet --to jail:web --port 443
jailops firewall test --from host --to @db --proto icmp
jailops firewall test --from jail:web --to 198.51.100.9 --proto udp --port 53 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" || to == "" {
				return usagef("--from and --to are required")
			}
			if (proto == "tcp" || proto == "udp") && port == 0 {
				return usagef("--port is required: the protocol is %s unless --proto says otherwise (for example --port 5432, or --proto icmp to test ping)", proto)
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			res, err := c.Firewall().Test(cmd.Context(), api.FirewallTestRequest{From: from, To: to, Proto: proto, Port: port, ICMPType: icmpType, Family: family})
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				if err := a.emit(res, nil, nil, nil); err != nil {
					return err
				}
			} else {
				printFirewallTest(a, res, verbose)
			}
			switch res.Verdict {
			case api.TestPass:
				return nil
			case api.TestBlock:
				return &exitError{code: ExitBlocked}
			}
			return &exitError{code: ExitUndecided}
		},
	}
	f := cmd.Flags()
	f.StringVar(&from, "from", "", "where the connection starts: jail:NAME, an address or host")
	f.StringVar(&to, "to", "", "where it goes: jail:NAME, an address or host")
	f.StringVar(&proto, "proto", "tcp", "tcp, udp, icmp or icmp6")
	f.IntVar(&port, "port", 0, "destination port of tcp and udp")
	f.StringVar(&icmpType, "icmp-type", "", "ICMP type as pf names it, such as echoreq (default for icmp and icmp6)")
	f.BoolVarP(&verbose, "verbose", "v", false, "show the rendered pf rules, the rules that were skipped and what the test does not cover")
	f.StringVar(&family, "family", "", "test only IPv4 (4) or IPv6 (6); default: every family the two ends have in common")
	enumFlag(cmd, "proto", "tcp", "udp", "icmp", "icmp6")
	enumFlag(cmd, "family", "4", "6")
	ends := a.refCompleter(placeEnd)
	for _, name := range []string{"from", "to"} {
		_ = cmd.RegisterFlagCompletionFunc(name, func(c *cobra.Command, args []string, t string) ([]string, cobra.ShellCompDirective) {
			return ends(c, args, t)
		})
	}
	return cmd
}

// stageText names what a rule of a stage is, for the line of a hop.
var stageText = map[string]string{
	"ipv6-control":  "the IPv6 control rules",
	"antispoof":     "the anti-spoofing rule",
	"neighbour":     "the neighbour discovery rules",
	"own-addresses": "the rule that only the jail's own addresses reach it",
	"isolation":     "the isolation of projects",
	"default":       "the default of the jail's policies",
	"closed":        "the closed link of a jail without an address",
	"gateway":       "the rules of the gateway interface",
	"uplink":        "the rule against forged sources on the uplink",
	"raw":           "a raw PF fragment",
}

// printFirewallTest says whether the connection passes and which rule decides at each end, in a few lines; verbose
// adds the rendered pf rules, the rules that were skipped and what the test does not cover.
func printFirewallTest(a *app, res api.FirewallTestResult, verbose bool) {
	if verbose {
		printFirewallTestVerbose(a, res)
		return
	}
	w := a.env.Stdout
	for i, c := range res.Cases {
		if i > 0 {
			fprintf(w, "\n")
		}
		what := c.Proto
		if c.Port > 0 {
			what += " port " + fmt.Sprint(c.Port)
		}
		if c.ICMPType != "" {
			what += " " + c.ICMPType
		}
		fprintf(w, "%s %s -> %s (%s): %s\n", what, c.From, c.To, c.Family, strings.ToUpper(c.Verdict))
		for _, h := range c.Hops {
			who := "takes"
			switch h.Side {
			case "source":
				who = "sends"
			case "uplink":
				who = "on the uplink"
			case "tunnel":
				who = "on the tunnel"
			case "forward":
				who = "forwarded"
			}
			switch {
			case h.Side == "forward" && h.Why != "":
				by := ""
				if h.DecidedBy != nil {
					by = " (" + describeRule(h.DecidedBy) + ")"
				}
				fprintf(w, "  %s %s: %s: %s%s\n", h.Jail, who, h.Verdict, h.Why, by)
			case h.DecidedBy != nil:
				word := map[string]string{"pass": "allowed", "block": "blocked"}[h.Verdict]
				if word == "" {
					word = h.Verdict
				}
				fprintf(w, "  %s %s: %s %s\n", h.Jail, who, word, decidedText(h))
			case h.Why != "":
				fprintf(w, "  %s %s: %s: %s\n", h.Jail, who, h.Verdict, h.Why)
			default:
				fprintf(w, "  %s %s: %s\n", h.Jail, who, h.Verdict)
			}
			if h.LoadedMatchesRendered == "false" {
				fprintf(w, "  note: the rules loaded in pf for %s differ from the rendered ones; the daemon will load them again\n", h.Anchor)
			}
		}
	}
	if len(res.Cases) > 1 {
		fprintf(w, "\nVerdict: %s\n", strings.ToUpper(res.Verdict))
	}
	fprintf(w, "\n(-v shows the rendered pf rules and what the test does not cover)\n")
}

func printFirewallTestVerbose(a *app, res api.FirewallTestResult) {
	w := a.env.Stdout
	for i, c := range res.Cases {
		if i > 0 {
			fprintf(w, "\n")
		}
		what := c.Proto
		if c.Port > 0 {
			what += " port " + fmt.Sprint(c.Port)
		}
		if c.ICMPType != "" {
			what += " " + c.ICMPType
		}
		fprintf(w, "%s %s -> %s (%s)\n", what, c.From, c.To, c.Family)
		for _, h := range c.Hops {
			side := "into"
			switch h.Side {
			case "source":
				side = "out of"
			case "uplink":
				side = "from the uplink into"
			case "tunnel":
				side = "from the tunnel into"
			case "forward":
				side = "port forward of the host to"
			}
			fprintf(w, "  %s %s (%s, %s): %s", side, h.Jail, h.Anchor, h.Direction, strings.ToUpper(h.Verdict))
			switch {
			case h.Side == "forward" && h.Why != "":
				fprintf(w, ": %s", h.Why)
				if h.DecidedBy != nil {
					fprintf(w, " (%s)", describeRule(h.DecidedBy))
				}
				fprintf(w, "\n")
			case h.DecidedBy != nil:
				fprintf(w, " %s\n", decidedText(h))
				fprintf(w, "      %s\n", h.DecidedBy.PF)
			case h.Why != "":
				fprintf(w, ": %s\n", h.Why)
			default:
				fprintf(w, "\n")
			}
			for _, sk := range h.Skipped {
				fprintf(w, "      skipped: %s: %s\n", describeRule(&sk.Rule), sk.Why)
			}
			if h.LoadedMatchesRendered == "false" {
				fprintf(w, "      note: the rules loaded in pf for %s differ from the rendered ones; the daemon will load them again\n", h.Anchor)
			}
		}
		fprintf(w, "  Verdict: %s\n", strings.ToUpper(c.Verdict))
	}
	if len(res.Cases) > 1 {
		fprintf(w, "\nVerdict: %s\n", strings.ToUpper(res.Verdict))
	}
	if len(res.Caveats) > 0 {
		fprintf(w, "\nNot covered:\n")
		for _, c := range res.Caveats {
			fprintf(w, "  - %s\n", c)
		}
	}
}

// decidedText says what decided a hop: "by" and the rule, or, when no rule matched, the default and which policies set
// it: a default is deny when any policy states deny.
func decidedText(h api.FirewallTestHop) string {
	r := h.DecidedBy
	if r.Stage != "default" {
		return "by " + describeRule(r)
	}
	dir, v := "in", "allow"
	if h.Direction == "in" { // on the jail's link, what comes in is what the jail sends
		dir = "out"
	}
	if h.Verdict == "block" {
		v = "deny"
	}
	if len(r.SetBy) == 0 {
		return fmt.Sprintf("(no rule matched: default %s %s, no policy says deny)", dir, v)
	}
	return fmt.Sprintf("(no rule matched: default %s %s, set by: %s)", dir, v, strings.Join(r.SetBy, ", "))
}

func describeRule(r *api.FirewallTestRule) string {
	if r.Stage != "policy" && r.Stage != "raw" {
		if s, ok := stageText[r.Stage]; ok {
			return s
		}
		return r.Stage
	}
	kind := "policy"
	switch {
	case r.ProjectPolicy:
		kind = "project policy"
	}
	s := fmt.Sprintf("%s %s", kind, r.Policy)
	if r.Rule > 0 {
		s += fmt.Sprintf(" rule %d", r.Rule)
		if r.Name != "" {
			s += " " + r.Name
		}
		if r.Comment != "" {
			s += fmt.Sprintf(" %q", r.Comment)
		}
	}
	return s
}
