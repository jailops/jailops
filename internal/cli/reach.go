package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// exposedCell is the EXPOSED column of 'jailops list': what the Internet reaches, "tcp 80,443 v4,v6", "-" when
// nothing. A forward shows the port of the host. A restricted source (not "any") is marked with a star.
func exposedCell(j api.Jail) string {
	return exposedText(j.Status.Exposed)
}

// exposedWidth is the longest EXPOSED cell of 'jailops list'; what does not fit is counted as "+N" groups.
const exposedWidth = 22

// exposedText writes what the Internet reaches in the compact form "PORTS/PROTO FAMILIES": "80,443/tcp 4,6", "22/tcp 6*"
// (a star: a source restriction applies), "8080→80/tcp 4" (a forward whose host port differs from the jail's port). The
// groups are joined by a space and cut at exposedWidth characters with "+N"; "-" when nothing is reachable. The full
// list is in 'firewall show' and in the JSON of the jail.
func exposedText(es []api.ExposedEntry) string {
	type key struct {
		proto, ports string
		star         bool
	}
	var order []key
	fams := map[key][]string{}
	for _, e := range es {
		ports := e.Ports
		if e.Via == "forward" && e.HostPort > 0 && fmt.Sprint(e.HostPort) != e.Ports {
			ports = fmt.Sprintf("%d→%s", e.HostPort, e.Ports)
		}
		k := key{e.Proto, ports, e.Restricted}
		if _, ok := fams[k]; !ok {
			order = append(order, k)
		}
		if !slices.Contains(fams[k], e.Family) {
			fams[k] = append(fams[k], e.Family)
		}
	}
	var parts []string
	for _, k := range order {
		f := fams[k]
		slices.Sort(f)
		p := k.proto
		if k.ports != "" {
			p = k.ports + "/" + k.proto
		}
		p += " " + strings.Join(f, ",")
		if k.star {
			p += "*"
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "-"
	}
	out, used := "", 0
	for i, p := range parts {
		next := p
		if out != "" {
			next = out + " " + p
		}
		if utf8.RuneCountInString(next) > exposedWidth && i > 0 {
			return out + fmt.Sprintf(" +%d", len(parts)-used)
		}
		if utf8.RuneCountInString(next) > exposedWidth {
			r := []rune(p)
			return string(r[:exposedWidth-1]) + "…"
		}
		out, used = next, used+1
	}
	return out
}

func fromText(from []string, restricted bool) string {
	if !restricted || len(from) == 0 {
		return "any"
	}
	return strings.Join(from, ",")
}

func portsText(p string) string {
	if p == "" {
		return "-"
	}
	return p
}

func sourceText(r api.ReachRow) string {
	switch r.Source {
	case api.ReachInternet:
		return "Internet"
	case api.ReachTunnel:
		if r.SourceName != "" {
			return "tunnel " + r.SourceName
		}
		return "tunnel"
	case api.ReachJails:
		return "other jails"
	case api.ReachHost:
		return "host"
	case api.ReachOther:
		return "other projects"
	}
	return r.Source
}

func reachDecision(r api.ReachRow) string {
	switch r.Decision {
	case "pass":
		return "open"
	case "forward":
		return "forward " + hostPortText(r.HostAddress, r.HostPort)
	}
	return "closed"
}

// reachTable prints the rows of what reaches a jail.
func (a *app) reachTable(w io.Writer, rows []api.ReachRow) {
	var out [][]string
	for _, r := range rows {
		if r.Decision == "none" {
			out = append(out, []string{sourceText(r), "-", "-", "-", "-", "closed", r.By})
			continue
		}
		out = append(out, []string{sourceText(r), dash(r.Family), dash(r.Proto), portsText(r.Ports), fromText(r.From, r.Restricted), reachDecision(r), r.By})
	}
	a.printTable(w, []string{"SOURCE", "FAMILY", "PROTO", "PORTS", "LIMITED TO", "REACH", "BY"}, out, func(col int, v string) string {
		if col == 5 {
			switch {
			case v == "closed":
				return "2"
			case v == "open" || strings.HasPrefix(v, "forward"):
				return "32"
			}
		}
		return ""
	})
}

func (a *app) printReachOut(w io.Writer, o api.ReachOut) {
	fprintf(w, "  Internet:       %s\n  Host:           %s\n  Other jails:    %s\n  Other projects: %s\n", dash(o.Internet), dash(o.Host), dash(o.Jails), dash(o.OtherProjects))
}

func (a *app) firewallShow() *cobra.Command {
	var rules, pf bool
	cmd := &cobra.Command{
		Use:   "show JAIL [--lines] [--pf]",
		Short: "Show what reaches a jail, what it reaches, and why",
		Long: `Show what can reach a jail and what the jail can reach, computed from the anchor JailOps loads
for it: the Internet over IPv4 and IPv6, WireGuard tunnels, the other jails of the project, the
host and other projects. Every row names the policy rule that gives the reach, or what stops it.

An IPv4 jail address is private (NAT): the Internet reaches it through a forward of a port of the
host. An IPv6 address is routed: it is reached by an allow in rule with an external source.
--lines lists every line of the anchor in the order pf decides (the first match wins); --pf adds the
rendered pf text of each. What the computation cannot know (a raw PF fragment, the host's own pf.conf)
is listed below the table.`,
		Example: `jailops firewall show web
jailops firewall show web --lines
jailops firewall show web -o json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, a.jailDesc()),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			r, err := c.Jails().Reach(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(r, nil, nil, nil)
			}
			w := a.env.Stdout
			if r.Error != "" {
				return fmt.Errorf("the firewall of jail/%s cannot be computed: %s", args[0], r.Error)
			}
			a.printPolicies(w, r)
			fprintf(w, "\nReaches jail/%s\n\n", r.Jail)
			a.reachTable(w, r.Rows)
			fprintf(w, "\nThe jail reaches (default out: %s)\n", defaultText(r.DefaultOut, r.DefaultOutBy))
			a.printReachOut(w, r.Out)
			if rules || pf {
				fprintf(w, "\nRules, in the order pf decides (default in: %s)\n", defaultText(r.DefaultIn, r.DefaultInBy))
				var out [][]string
				for i, ru := range r.Rules {
					row := []string{fmt.Sprint(i + 1), ru.Where, ru.Direction, dash(ru.Family), dash(ru.From), dash(ru.To), dash(ru.Proto), portsText(ru.Ports), string(ru.Decision), ru.By}
					if pf {
						row = append(row, ru.PF)
					}
					out = append(out, row)
				}
				head := []string{"#", "WHERE", "DIR", "FAMILY", "FROM", "TO", "PROTO", "PORTS", "DECISION", "BY"}
				if pf {
					head = append(head, "PF")
				}
				a.printTable(w, head, out, nil)
			}
			for _, n := range r.Notes {
				fprintf(w, "\nNote: %s\n", n)
			}
			if len(r.Policies) == 0 {
				fprintf(w, "\n%s\n", attachHint(r.Jail))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&rules, "lines", false, "also list every line of the anchor in the order pf decides")
	cmd.Flags().BoolVar(&pf, "pf", false, "also show the rendered pf text of each line (implies --lines)")
	return cmd
}

func (a *app) firewallExposed() *cobra.Command {
	var tunnels bool
	cmd := &cobra.Command{
		Use:   "exposed [--tunnels]",
		Short: "List everything the Internet reaches, across the jails",
		Long: `List every way the Internet reaches a jail: an IPv6 allow in rule with an external source, and an
IPv4 port forward of the host. One line per jail, protocol and port list; FROM says which sources the
rule admits ("any" is the whole Internet). --tunnels adds what WireGuard peers reach.

It is the review of 'jailops expose': what is open to the world, and which rule opens it.`,
		Example: `jailops firewall exposed
jailops firewall exposed --tunnels
jailops firewall exposed -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			ex, err := c.Firewall().Exposed(cmd.Context(), tunnels)
			if err != nil {
				return err
			}
			ex.Entries = nonNil(ex.Entries)
			if a.format != fmtTable {
				return a.emit(ex, nil, nil, nil)
			}
			w := a.env.Stdout
			rows := func(es []api.ExposedJail) [][]string {
				var out [][]string
				for _, e := range es {
					to := e.Address
					if e.Via == "forward" {
						to = hostPortText(e.HostAddress, e.HostPort) + " -> " + e.Address
					}
					out = append(out, []string{e.Jail, dash(e.Proto), portsText(e.Ports), "v" + e.Family, to, fromText(e.From, e.Restricted), e.By})
				}
				return out
			}
			head := []string{"JAIL", "PROTO", "PORTS", "FAMILY", "ADDRESS", "FROM", "BY"}
			if len(ex.Entries) == 0 {
				fprintln(w, "Nothing is reachable from the Internet.")
			} else {
				a.printTable(w, head, rows(ex.Entries), nil)
			}
			if tunnels {
				fprintln(w, "\nFrom WireGuard tunnels")
				if len(ex.Tunnels) == 0 {
					fprintln(w, "Nothing is reachable from a tunnel.")
				} else {
					a.printTable(w, head, rows(ex.Tunnels), nil)
				}
			}
			for _, e := range ex.Errors {
				fprintf(w, "\nNote: %s\n", e)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&tunnels, "tunnels", false, "also list what WireGuard peers reach")
	return cmd
}

// hostPort names a port of the host: "host:8080", or "192.0.2.1:8080" when the forward names the host address.
func hostPortText(addr string, port int) string {
	if addr == "" {
		addr = "host"
	}
	return fmt.Sprintf("%s:%d", addr, port)
}

// defaultText says a default of the anchor and which policies set it: a default is deny when any policy states deny
// and allow when none does.
func defaultText(v string, by []string) string {
	switch {
	case v == "":
		return "-"
	case len(by) == 0:
		return v + " (no policy says deny)"
	}
	return v + " (" + strings.Join(by, ", ") + ")"
}

// attachHint is the line that says how to give a jail without a policy one.
func attachHint(jail string) string {
	return "attach one: jailops firewall policy attach POLICY " + jail
}

// policyLabel is the name of a policy with where it comes from: the project's mandatory ones and the one that app access
// keeps are marked, the jail's own are not.
func policyLabel(p api.ReachPolicy) string {
	switch p.Source {
	case "project":
		return p.Name + " (project)"
	case "managed":
		return p.Name + " (managed)"
	}
	return p.Name
}

// policyDefaults says the defaults a policy states, "-" when it states none.
func policyDefaults(p api.ReachPolicy) string {
	var d []string
	if p.DefaultIn != "" {
		d = append(d, "in "+p.DefaultIn)
	}
	if p.DefaultOut != "" {
		d = append(d, "out "+p.DefaultOut)
	}
	if len(d) == 0 {
		return "-"
	}
	return strings.Join(d, ", ")
}

// printPolicies lists the policies of the jail in the order they are rendered, before what they make of the jail.
func (a *app) printPolicies(w io.Writer, r api.JailReach) {
	if len(r.Policies) == 0 {
		fprintf(w, "Policies of jail/%s: none (the host answers ping and DNS only)\n", r.Jail)
		return
	}
	fprintf(w, "Policies of jail/%s, in the order they are rendered\n", r.Jail)
	var rows [][]string
	for i, p := range r.Policies {
		rows = append(rows, []string{fmt.Sprint(i + 1), policyLabel(p), fmt.Sprint(p.Rules), policyDefaults(p)})
	}
	a.printTable(w, []string{"#", "POLICY", "RULES", "STATES DEFAULT"}, rows, nil)
}
