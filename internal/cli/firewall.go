package cli

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func policyDesc() *kindDesc[api.FirewallPolicy] {
	d := newDesc("firewall policy", "firewall policies", api.KindFirewallPolicy,
		func(p *api.FirewallPolicy) *api.ObjectMeta { return &p.Metadata },
		func(p *api.FirewallPolicy) *api.FirewallPolicySpec { return &p.Spec })
	fromResource(&d, func(c *sdk.Client) *sdk.Resource[api.FirewallPolicy] { return c.Firewall().Policies() })
	renameFrom(&d, func(c *sdk.Client) *sdk.Resource[api.FirewallPolicy] { return c.Firewall().Policies() })
	d.renameLong = renameLong("firewall policy", `the jails and the profiles of the project that use it and, for the
project's mandatory policy (renamed by a global administrator), the project`)
	d.show = func(a *app, p api.FirewallPolicy) error { return a.showPolicy(p) }
	d.head = []string{"NAME", "DEFAULT-IN", "DEFAULT-OUT", "RULES", "RAW"}
	d.row = func(_ time.Time, p api.FirewallPolicy) []string {
		raw := "no"
		if p.Spec.Raw != "" {
			raw = "yes"
		}
		return []string{p.Metadata.Name, dash(p.Spec.DefaultIn), dash(p.Spec.DefaultOut), strconv.Itoa(len(p.Spec.Rules)), raw}
	}
	return &d
}

func addrSetDesc() *kindDesc[api.AddressSet] {
	d := newDesc("address set", "address sets", api.KindAddressSet,
		func(p *api.AddressSet) *api.ObjectMeta { return &p.Metadata },
		func(p *api.AddressSet) *api.AddressSetSpec { return &p.Spec })
	fromResource(&d, func(c *sdk.Client) *sdk.Resource[api.AddressSet] { return c.Firewall().AddressSets() })
	renameFrom(&d, func(c *sdk.Client) *sdk.Resource[api.AddressSet] { return c.Firewall().AddressSets() })
	d.renameLong = renameLong("address set", `the rules (@NAME) of the policies of the project, the sets that
include it and the app access grants made from it. Its PF table is renamed too, which ends the
connections that rules using it passed once; they are made again`)
	d.show = func(a *app, p api.AddressSet) error { return a.showAddressSet(p) }
	d.head = []string{"NAME", "ADDRESSES", "INCLUDES"}
	d.row = func(_ time.Time, p api.AddressSet) []string {
		return []string{p.Metadata.Name, strconv.Itoa(len(p.Spec.Addresses)), joinOrDash(p.Spec.Includes)}
	}
	return &d
}

func (a *app) firewallCmd() *cobra.Command {
	fc := group("firewall", "Manage firewall policies and address sets",
		`A firewall policy is a declarative list of rules rendered into one PF anchor per
jail; jailops never owns the host's pf.conf. Rules may refer to an address set or
to a jail of the project as @name (a jail by its addresses), and to jails by tag
as tag:name. 'jailops firewall validate' checks files offline before they are
applied.`,
		`jailops firewall policy list
jailops firewall address-set create office --address 192.0.2.0/24
jailops firewall validate -f policy.yaml`, "fw")
	pd := policyDesc()
	pc := group("policy", "Manage firewall policies", "Create, inspect, edit and delete firewall policies.",
		"jailops firewall policy list\njailops firewall policy show web", "policies")
	crudCommands(a, pc, pd, `Create a firewall policy from rule flags or a file. Each --rule has the form
"ACTION DIRECTION [PROTO] [from ADDR,..] [to ADDR,..] [port PORT,..] [log] [rdr HOSTPORT:JAILPORT] [comment TEXT]";
comment is last and takes the rest of the rule: why the rule is there, which a reviewer of the firewall reads.
--comment is what the policy is for.`,
		`jailops firewall policy create web --default-in deny --default-out allow --comment "the public web server" --rule "allow in tcp port 80,443 comment the site" --rule "allow in tcp from @office port 22 comment ssh from the office"
jailops firewall policy create -f web.yaml`,
		func(cmd *cobra.Command) func(string) (api.FirewallPolicy, error) {
			var din, dout, comment string
			var rules []string
			cmd.Flags().StringVar(&comment, "comment", "", "what the policy is for (one line, at most 200 characters, no double quotes or backslashes)")
			cmd.Flags().StringVar(&din, "default-in", "", "default for inbound traffic: allow or deny")
			cmd.Flags().StringVar(&dout, "default-out", "", "default for outbound traffic: allow or deny")
			cmd.Flags().StringArrayVar(&rules, "rule", nil, "a rule (repeatable)")
			return func(name string) (api.FirewallPolicy, error) {
				p := api.FirewallPolicy{Metadata: api.ObjectMeta{Name: name}, Spec: api.FirewallPolicySpec{DefaultIn: din, DefaultOut: dout, Comment: comment}}
				for _, r := range rules {
					fr, err := parseRule(r)
					if err != nil {
						return p, err
					}
					p.Spec.Rules = append(p.Spec.Rules, fr)
				}
				return p, nil
			}
		})
	ad := addrSetDesc()
	ac := group("address-set", "Manage address sets", "Create, inspect, edit and delete address sets referenced as @name in rules. A member is an address, a CIDR or @jail. Every entry, set, rule and policy has a `comment`: what it is or why it is there. `show` lists them and the rendered rules carry them as comment lines: comments are what a reviewer of the firewall reads.",
		"jailops firewall address-set list\njailops firewall address-set show office", "address-sets", "set")
	crudCommands(a, ac, ad, "Create an address set from --address flags or a file. Sets may include other sets; cycles are rejected.",
		"jailops firewall address-set create office --address 192.0.2.0/24 --comment \"the office\"\njailops firewall address-set create -f office.yaml",
		func(cmd *cobra.Command) func(string) (api.AddressSet, error) {
			var addrs, incl []string
			var comment string
			cmd.Flags().StringArrayVar(&addrs, "address", nil, "address, CIDR or @jail (repeatable)")
			cmd.Flags().StringVar(&comment, "comment", "", "what the address is (with a single --address; several entries with comments come from a file, -f, or address-set edit)")
			cmd.Flags().StringArrayVar(&incl, "include", nil, "name of another address set to include (repeatable)")
			return func(name string) (api.AddressSet, error) {
				if len(addrs) == 0 && len(incl) == 0 {
					return api.AddressSet{}, usagef("give at least one --address or --include (or use -f FILE)")
				}
				spec := api.AddressSetSpec{Addresses: api.Entries(addrs...), Includes: incl}
				if comment != "" {
					if len(addrs) != 1 {
						return api.AddressSet{}, usagef("--comment describes a single --address; for several entries with comments use a file (-f FILE) or 'address-set edit'")
					}
					spec.Addresses[0].Comment = comment
				}
				return api.AddressSet{Metadata: api.ObjectMeta{Name: name}, Spec: spec}, nil
			}
		})
	fc.AddCommand(pc, ac, a.firewallValidate())
	return fc
}

func parseRule(s string) (api.FirewallRule, error) {
	t := strings.Fields(s)
	var r api.FirewallRule
	if len(t) < 2 {
		return r, usagef("rule %q needs at least ACTION and DIRECTION", s)
	}
	r.Action, r.Direction = t[0], t[1]
	t = t[2:]
	if len(t) > 0 && slices.Contains([]string{"tcp", "udp", "icmp", "icmp6"}, t[0]) {
		r.Proto, t = t[0], t[1:]
	}
	list := func(v string) []string { return strings.Split(v, ",") }
	for len(t) > 0 {
		switch t[0] {
		case "log":
			r.Log, t = true, t[1:]
			continue
		case "comment": // the rest of the rule
			if len(t) < 2 {
				return r, usagef("rule %q: comment needs a text", s)
			}
			r.Comment, t = strings.Join(t[1:], " "), nil
			continue
		}
		if len(t) < 2 {
			return r, usagef("rule %q: %q needs a value", s, t[0])
		}
		switch t[0] {
		case "from":
			r.From = list(t[1])
		case "to":
			r.To = list(t[1])
		case "port":
			r.Ports = list(t[1])
		case "name":
			r.Name = t[1]
		case "rdr":
			h, j, ok := strings.Cut(t[1], ":")
			hp, e1 := strconv.Atoi(h)
			jp, e2 := strconv.Atoi(j)
			if !ok || e1 != nil || e2 != nil {
				return r, usagef("rule %q: rdr must be HOSTPORT:JAILPORT", s)
			}
			r.Redirect = &api.Redirect{HostPort: hp, JailPort: jp}
		default:
			return r, usagef("rule %q: unknown word %q", s, t[0])
		}
		t = t[2:]
	}
	return r, nil
}

// validation ---------------------------------------------------------------

type finding struct {
	Doc     string `json:"document"`
	Level   string `json:"level"` // error or warning
	Message string `json:"message"`
}

func validAddr(s string) bool {
	if s == "any" || strings.HasPrefix(s, "@") && len(s) > 1 || strings.HasPrefix(s, "tag:") && len(s) > 4 {
		return true
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

func validPort(p string) bool {
	lo, hi, isRange := strings.Cut(p, "-")
	l, err := strconv.Atoi(lo)
	if err != nil || l < 1 || l > 65535 {
		return false
	}
	if !isRange {
		return true
	}
	h, err := strconv.Atoi(hi)
	return err == nil && h >= l && h <= 65535
}

func validatePolicy(doc string, p api.FirewallPolicySpec, sets func(string) bool) []finding {
	var out []finding
	bad := func(f string, a ...any) { out = append(out, finding{doc, "error", fmt.Sprintf(f, a...)}) }
	for _, d := range [][2]string{{"default_in", p.DefaultIn}, {"default_out", p.DefaultOut}} {
		if d[1] != "" && d[1] != "allow" && d[1] != "deny" {
			bad("%s must be allow or deny, not %q", d[0], d[1])
		}
	}
	for i, r := range p.Rules {
		at := fmt.Sprintf("rules[%d]", i)
		if r.Name != "" {
			at += " (" + r.Name + ")"
		}
		if r.Action != "allow" && r.Action != "deny" {
			bad("%s: action must be allow or deny, not %q", at, r.Action)
		}
		if r.Direction != "in" && r.Direction != "out" {
			bad("%s: direction must be in or out, not %q", at, r.Direction)
		}
		if r.Proto != "" && !slices.Contains([]string{"tcp", "udp", "icmp", "icmp6"}, r.Proto) {
			bad("%s: unknown protocol %q", at, r.Proto)
		}
		if len(r.Ports) > 0 && r.Proto != "tcp" && r.Proto != "udp" {
			bad("%s: ports need proto tcp or udp", at)
		}
		for _, port := range r.Ports {
			if !validPort(port) {
				bad("%s: invalid port %q", at, port)
			}
		}
		for _, addr := range slices.Concat(r.From, r.To) {
			if !validAddr(addr) {
				bad("%s: invalid address %q", at, addr)
			} else if n, ok := strings.CutPrefix(addr, "@"); ok && !sets(n) {
				bad("%s: unknown address set %q", at, n)
			}
		}
		if r.Redirect != nil {
			if r.Direction != "in" {
				bad("%s: a redirect is allowed on inbound rules only", at)
			}
			if r.Redirect.HostPort < 1 || r.Redirect.HostPort > 65535 || r.Redirect.JailPort < 1 || r.Redirect.JailPort > 65535 {
				bad("%s: redirect ports must be 1-65535", at)
			}
		}
	}
	if p.Raw != "" {
		out = append(out, finding{doc, "warning", "raw PF needs the admin role; the server checks it with pfctl -n when applied"})
	}
	return out
}

func validateAddrSets(sets map[string]api.AddressSetSpec, docs map[string]bool) []finding {
	var out []finding
	for _, name := range sortedKeys(sets) {
		doc := "address-set/" + name
		if !docs[name] {
			continue
		}
		s := sets[name]
		for _, e := range s.Addresses {
			a := e.Address
			if strings.HasPrefix(a, "@") { // a jail or a set: checked where the objects are known
				continue
			}
			if _, err := netip.ParsePrefix(a); err != nil {
				if _, err := netip.ParseAddr(a); err != nil {
					out = append(out, finding{doc, "error", fmt.Sprintf("invalid address %q", a)})
				}
			}
		}
		for _, inc := range s.Includes {
			if _, ok := sets[inc]; !ok {
				out = append(out, finding{doc, "error", fmt.Sprintf("includes unknown address set %q", inc)})
			}
		}
		// cycle search from this set
		var walk func(n string, path []string) []string
		walk = func(n string, path []string) []string {
			if slices.Contains(path, n) {
				return append(path, n)
			}
			for _, inc := range sets[n].Includes {
				if _, ok := sets[inc]; ok {
					if p := walk(inc, append(slices.Clone(path), n)); p != nil {
						return p
					}
				}
			}
			return nil
		}
		if p := walk(name, nil); p != nil && p[len(p)-1] == name {
			out = append(out, finding{doc, "error", "include cycle: " + strings.Join(p, " -> ")})
		}
	}
	return out
}

func (a *app) firewallValidate() *cobra.Command {
	var file string
	var offline bool
	cmd := &cobra.Command{
		Use:   "validate -f FILE",
		Short: "Check firewall policies and address sets without applying them",
		Long: `Check the FirewallPolicy and AddressSet documents of a file: actions, directions,
protocols, ports, address syntax, redirects, references to @address-sets and
include cycles. Address sets defined in the same file count, and the ones on
the server are looked up unless --offline is given.

The exit status is 2 when an error was found. Raw PF fragments are not parsed
here; the server checks them with pfctl -n when they are applied.`,
		Example: "jailops firewall validate -f policy.yaml\ncat policy.yaml | jailops firewall validate -f - --offline",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return usagef("-f FILE is required")
			}
			b, err := a.readInput(file)
			if err != nil {
				return err
			}
			docs, err := readDocs(b)
			if err != nil {
				return err
			}
			type pol struct {
				name string
				spec api.FirewallPolicySpec
			}
			var pols []pol
			sets := map[string]api.AddressSetSpec{}
			inFile := map[string]bool{}
			for _, doc := range docs {
				switch k, _ := doc["kind"].(string); api.Kind(k) {
				case api.KindFirewallPolicy:
					m, s, err := parseObject[api.FirewallPolicySpec](doc, api.KindFirewallPolicy, "")
					if err != nil {
						return err
					}
					pols = append(pols, pol{m.Name, s})
				case api.KindAddressSet:
					m, s, err := parseObject[api.AddressSetSpec](doc, api.KindAddressSet, "")
					if err != nil {
						return err
					}
					sets[m.Name], inFile[m.Name] = s, true
				default:
					return usagef("validate handles FirewallPolicy and AddressSet documents, not %q", k)
				}
			}
			if !offline {
				if c, err := a.connect(cmd.Context()); err == nil {
					if remote, err := collect(c.Firewall().AddressSets().List(cmd.Context())); err == nil {
						for _, r := range remote {
							if _, dup := sets[r.Metadata.Name]; !dup {
								sets[r.Metadata.Name] = r.Spec
							}
						}
					}
				}
			}
			known := func(n string) bool { _, ok := sets[n]; return ok }
			if offline {
				known = func(string) bool { return true }
			}
			var all []finding
			for _, p := range pols {
				all = append(all, validatePolicy("policy/"+p.name, p.spec, known)...)
			}
			all = append(all, validateAddrSets(sets, inFile)...)
			errs := 0
			for _, f := range all {
				if f.Level == "error" {
					errs++
				}
			}
			if a.format != fmtTable {
				if err := a.emit(nonNil(all), nil, nil, nil); err != nil {
					return err
				}
			} else if len(all) == 0 {
				fprintf(a.env.Stdout, "valid: %d documents checked\n", len(docs))
			} else {
				a.printTable(a.env.Stdout, []string{"DOCUMENT", "LEVEL", "PROBLEM"}, func() [][]string {
					var rows [][]string
					for _, f := range all {
						rows = append(rows, []string{f.Doc, f.Level, f.Message})
					}
					return rows
				}(), nil)
			}
			if errs > 0 {
				return api.Errorf(api.CodeInvalid, "%d problem(s) found in %s", errs, file).WithHint("fix the listed rules and run validate again")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "file with FirewallPolicy and AddressSet documents, or - for standard input")
	cmd.Flags().BoolVar(&offline, "offline", false, "do not look up address sets on the server")
	return cmd
}
