package render

import (
	"errors"
	"net/netip"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func pfBase() PFInput {
	return PFInput{
		Jail: "alpha_web", Interfaces: []string{"jo0123456789a0a"},
		Addresses:  []netip.Addr{ip("2001:db8:1::100"), ip("10.20.0.10")},
		Tags:       []string{"web", "tier-1"},
		TagMembers: map[string][]netip.Addr{"admin": {ip("10.20.0.5"), ip("10.20.0.4"), ip("10.20.0.5")}},
		Sets: map[string]api.AddressSetSpec{
			"office": {Addresses: []string{"203.0.113.0/24", "2001:db8:ffff::/48"}, Includes: []string{"vpn"}},
			"vpn":    {Addresses: []string{"198.51.100.7"}},
		},
		Uplink:    "em0",
		Antispoof: true,
		Policy: api.FirewallPolicySpec{
			DefaultIn: "deny", DefaultOut: "allow",
			Rules: []api.FirewallRule{
				{Name: "ssh from office", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"@office", "tag:admin"}, Ports: []string{"22"}, Log: true},
				{Name: "web", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80", "8000-8100"}},
				{Name: "ping", Action: "allow", Direction: "in", Proto: "icmp"},
				{Name: "ping6", Action: "allow", Direction: "in", Proto: "icmp6"},
				{Name: "forward", Action: "allow", Direction: "in", Proto: "tcp", Redirect: &api.Redirect{HostPort: 8080, JailPort: 80}},
				{Name: "no smtp", Action: "deny", Direction: "out", Proto: "tcp", Ports: []string{"25"}},
				{Name: "dns", Action: "allow", Direction: "out", Proto: "udp", To: []string{"9.9.9.9", "2620:fe::fe"}, Ports: []string{"53"}},
			},
		},
	}
}

func TestPFGolden(t *testing.T) {
	s, err := PFAnchor(pfBase())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_anchor_full", s)

	in := pfBase()
	in.Addresses = []netip.Addr{ip("10.20.0.10")}
	in.Antispoof = false
	in.Policy = api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "deny"}
	if s, err = PFAnchor(in); err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_anchor_defaults", s)

	in = pfBase()
	in.Policy.Raw = "pass quick on lo0 all\n"
	in.AllowRaw = true
	if s, err = PFAnchor(in); err != nil {
		t.Fatal(err)
	}
	// (a raw fragment is the administrator's own text and stays as written)
	if !strings.Contains(s, "# raw fragment (admin)\npass quick on lo0 all\n# defaults") {
		t.Fatalf("raw fragment misplaced:\n%s", s)
	}
	golden(t, "pf_anchor_raw", s)

	nat, err := PFNet("lan", api.NetworkSpec{Bridge: "jo7", Uplink: "em0", IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.20", NAT: true}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_nat", nat)
	golden(t, "pf_anchor_lines", AnchorLines())
	// without NAT the anchor still passes the gateway pair, and has no translation rule
	if s, err := PFNet("lan", api.NetworkSpec{Bridge: "jo7", IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.20"}}); err != nil || strings.Contains(s, "nat on") || !strings.Contains(s, "pass quick on jo7ga all no state") {
		t.Fatalf("no nat: %q %v", s, err)
	}
	if JailAnchor("alpha_web") != "jailops/alpha/web" || NetAnchor("lan") != "jailops/net.lan" {
		t.Fatal("anchor names")
	}
}

func TestPFOrdering(t *testing.T) {
	s, _ := PFAnchor(pfBase())
	idx := func(sub string) int { return strings.Index(s, sub) }
	if !(idx("table <") < idx("rdr on") && idx("rdr on") < idx("pass out quick") && idx("pass out quick") < idx("# defaults")) {
		t.Fatalf("sections out of order:\n%s", s)
	}
	// icmp is IPv4 only, icmp6 is IPv6 only
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, "proto icmp ") && strings.Contains(ln, "inet6") || strings.Contains(ln, "proto icmp6") && strings.Contains(ln, " inet ") {
			t.Fatalf("wrong family: %s", ln)
		}
	}
	// mixed-family lists are split per family
	if strings.Contains(s, "9.9.9.9, 2620") {
		t.Fatal("mixed-family list")
	}
}

func TestPFErrors(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*PFInput)
	}{
		{"no ifaces", func(i *PFInput) { i.Interfaces = nil }},
		{"bad iface", func(i *PFInput) { i.Interfaces = []string{"em0 all"} }},
		{"no addrs", func(i *PFInput) { i.Addresses = nil }},
		{"jail name", func(i *PFInput) { i.Jail = "a\npass all" }},
		{"raw without admin", func(i *PFInput) { i.Policy.Raw = "pass all"; i.AllowRaw = false }},
		{"unknown set", func(i *PFInput) { i.Policy.Rules[0].From = []string{"@nope"} }},
		{"bad set ref", func(i *PFInput) { i.Policy.Rules[0].From = []string{"@a b"} }},
		{"cycle", func(i *PFInput) {
			i.Sets["vpn"] = api.AddressSetSpec{Includes: []string{"office"}}
		}},
		{"self cycle", func(i *PFInput) { i.Sets["vpn"] = api.AddressSetSpec{Includes: []string{"vpn"}} }},
		{"bad set member", func(i *PFInput) { i.Sets["vpn"] = api.AddressSetSpec{Addresses: []string{"1.2.3.4 pass all"}} }},
		{"rdr outbound", func(i *PFInput) {
			i.Policy.Rules = []api.FirewallRule{{Action: "allow", Direction: "out", Proto: "tcp", Redirect: &api.Redirect{HostPort: 1, JailPort: 2}}}
		}},
		{"rdr no uplink", func(i *PFInput) { i.Uplink = "" }},
		{"bad tag", func(i *PFInput) { i.Tags = []string{"a b"} }},
		{"bad rule name", func(i *PFInput) { i.Policy.Rules[0].Name = "x\npass all" }},
		{"bad port", func(i *PFInput) { i.Policy.Rules[1].Ports = []string{"80 }"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := pfBase()
			tc.mut(&in)
			if s, err := PFAnchor(in); err == nil {
				t.Fatalf("accepted:\n%s", s)
			}
		})
	}
	in := pfBase()
	in.Policy.Raw = "pass all"
	_, err := PFAnchor(in)
	if !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("raw error: %v", err)
	}
	var ae *api.Error
	if !errors.As(err, &ae) {
		t.Fatal("not api error")
	}
	in = pfBase()
	in.Sets["vpn"] = api.AddressSetSpec{Includes: []string{"office"}}
	if _, err := PFAnchor(in); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle message: %v", err)
	}
}

func TestPFSetDiamondIsNotACycle(t *testing.T) {
	in := pfBase()
	in.Sets = map[string]api.AddressSetSpec{
		"office": {Includes: []string{"b", "c"}},
		"b":      {Includes: []string{"d"}},
		"c":      {Includes: []string{"d"}},
		"d":      {Addresses: []string{"10.0.0.1"}},
	}
	if _, err := PFAnchor(in); err != nil {
		t.Fatal(err)
	}
}

const pfAddr = `(any|[0-9a-f:./]+|<[st]_[a-z0-9_]+>|\{ ([0-9a-f:./]+|<[st]_[a-z0-9_]+>)(, ([0-9a-f:./]+|<[st]_[a-z0-9_]+>))* \})`
const pfIfs = `([a-z0-9]+|\{ [a-z0-9]+(, [a-z0-9]+)* \})`
const pfPort = `([0-9]+(:[0-9]+)?|\{ [0-9]+(:[0-9]+)?(, [0-9]+(:[0-9]+)?)* \})`

var pfLineRE = regexp.MustCompile(`^(` +
	`# [A-Za-z0-9 :;._,()/-]*|` +
	`table <([st]_[a-z0-9_]+|jail_addrs)> persist( \{ [0-9a-f:./]+(, [0-9a-f:./]+)* \})?|` +
	`block in quick on ` + pfIfs + ` from ! <jail_addrs> to any|` +
	`rdr on [a-z0-9]+ inet6? proto (tcp|udp) from ` + pfAddr + ` to \([a-z0-9]+\) port [0-9]+ -> [0-9a-f:.]+ port [0-9]+|` +
	`pass quick on ` + pfIfs + ` inet6 proto icmp6 icmp6-type \{ neighbrsol, neighbradv \}|` +
	`(pass|block drop) (in|out)( log)? quick on ` + pfIfs + ` inet6? ( ?proto (tcp|udp|icmp|icmp6) )?from ` + pfAddr + ` to ` + pfAddr + `( port ` + pfPort + `)?|` +
	`(pass|block drop) (in|out) on ` + pfIfs + ` all` +
	`)$`)

func FuzzPFAnchor(f *testing.F) {
	f.Add("web", "em0", "x", "office", "10.0.0.0/8", "22", "admin")
	f.Add("a\npass all", "em0\n", "x\nblock all", "a b", "1.2.3.4\npass all", "22\n80", "t\n")
	f.Add("web", "e", "x", "@@", "any; pass", "1-2 }", "tag:")
	f.Fuzz(func(t *testing.T, jail, ifn, ruleName, set, ep, port, tag string) {
		for _, dir := range []string{"in", "out"} {
			in := PFInput{
				Jail: jail, Interfaces: []string{ifn}, Uplink: ifn, Antispoof: true,
				Addresses:  []netip.Addr{ip("10.0.0.5"), ip("2001:db8::5")},
				Tags:       []string{tag},
				TagMembers: map[string][]netip.Addr{tag: {ip("10.0.0.9")}},
				Sets:       map[string]api.AddressSetSpec{set: {Addresses: []string{ep}}},
				Policy: api.FirewallPolicySpec{Rules: []api.FirewallRule{
					{Name: ruleName, Action: "allow", Direction: dir, Proto: "tcp", From: []string{"@" + set, "tag:" + tag, ep}, To: []string{ep}, Ports: []string{port}},
					{Name: ruleName, Action: "deny", Direction: "in", Proto: "udp", Ports: []string{port}},
				}},
			}
			if dir == "in" {
				in.Policy.Rules = append(in.Policy.Rules, api.FirewallRule{Action: "allow", Direction: "in", Proto: "tcp", From: []string{ep}, Redirect: &api.Redirect{HostPort: 80, JailPort: 8080}})
			}
			out, err := PFAnchor(in)
			if err != nil {
				continue
			}
			for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
				if !pfLineRE.MatchString(ln) {
					t.Fatalf("unexpected PF line %q in\n%s", ln, out)
				}
			}
		}
	})
}

func FuzzPFNet(f *testing.F) {
	f.Add("lan", "em0", "10.0.0.0/24", "10.0.0.1")
	f.Add("lan\npass all", "em0\n", "10.0.0.0/24\n", "10.0.0.1")
	re := regexp.MustCompile(`^(# .*|nat on [a-z0-9]+ inet from [0-9./]+ to ! [0-9./]+ -> \([a-z0-9]+\)|pass quick on [a-z0-9]+ all no state)$`)
	f.Fuzz(func(t *testing.T, name, up, subnet, gw string) {
		out, err := PFNet(name, api.NetworkSpec{Bridge: "jo7", Uplink: up, IPv4: &api.IPv4Net{Subnet: subnet, Gateway: gw, Range: gw, NAT: true}})
		if err != nil || out == "" {
			return
		}
		for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if !re.MatchString(ln) {
				t.Fatalf("unexpected line %q", ln)
			}
		}
	})
}

func mandBase() *PFMandatory {
	return &PFMandatory{Name: "baseline", Policy: api.FirewallPolicySpec{DefaultIn: "allow", Rules: []api.FirewallRule{
		{Name: "no telnet", Action: "deny", Direction: "in", Proto: "tcp", Ports: []string{"23"}},
		{Name: "no smtp", Action: "deny", Direction: "out", Proto: "tcp", Ports: []string{"25"}},
		{Name: "mgmt", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"@office"}, Ports: []string{"22"}},
		{Name: "fwd", Action: "allow", Direction: "in", Proto: "tcp", Redirect: &api.Redirect{HostPort: 2222, JailPort: 22}},
	}}}
}

func TestPFMandatoryGolden(t *testing.T) {
	jailOnly := pfBase()
	jailOnly.Jail = "web_nginx"
	s, err := PFAnchor(jailOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "# anchor jailops/web/nginx\n") {
		t.Fatalf("anchor comment:\n%s", s)
	}
	golden(t, "pf_anchor_project_jail_only", s)

	only := pfBase()
	only.Mandatory = mandBase()
	only.Policy = api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}
	s, err = PFAnchor(only)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_anchor_mandatory_only", s)

	both := pfBase()
	both.Mandatory = mandBase()
	s, err = PFAnchor(both)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_anchor_mandatory_and_jail", s)
	lines := strings.Split(s, "\n")
	pos := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("%q not in output", sub)
		return -1
	}
	jailStart, defaults := pos("# jail policy"), pos("# defaults")
	for _, m := range []string{"# rule 1: no telnet", "# rule 2: no smtp", "# rule 3: mgmt", "# rule 4: fwd"} {
		if i := pos(m); i > jailStart {
			t.Fatalf("mandatory rule %q after the jail policy", m)
		}
	}
	if !(pos("# project policy baseline") < pos("# rule 1: no telnet") && jailStart < defaults) {
		t.Fatalf("block order:\n%s", s)
	}
	// every jail rule label comes after the jail policy header
	for i := jailStart; i < defaults; i++ {
		if strings.HasPrefix(lines[i], "# rule ") && i < jailStart {
			t.Fatal("jail rule before header")
		}
	}
	// the mandatory redirect is the first rdr line, and tables are defined once
	if !strings.Contains(s, "port 2222 ->") || strings.Index(s, "port 2222 ->") > strings.Index(s, "port 8080 ->") {
		t.Fatalf("rdr order:\n%s", s)
	}
	if strings.Count(s, "table <s_office>") != 1 {
		t.Fatalf("table defined %d times", strings.Count(s, "table <s_office>"))
	}
	// the mandatory policy's defaults are not used
	if !strings.Contains(s, "block drop out quick on") || strings.Contains(s, "pass out quick on jo0123456789a0a all") {
		t.Fatalf("defaults:\n%s", s)
	}
}

func TestPFMandatoryErrors(t *testing.T) {
	in := pfBase()
	in.Mandatory = mandBase()
	in.Mandatory.Policy.Raw = "pass all"
	if _, err := PFAnchor(in); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("raw without admin: %v", err)
	}
	in.Mandatory.AllowRaw = true
	s, err := PFAnchor(in)
	if err != nil || strings.Index(s, "pass all") > strings.Index(s, "# jail policy") || !strings.Contains(s, "# raw fragment (admin)") {
		t.Fatalf("raw with admin: %v\n%s", err, s)
	}
	for name, mut := range map[string]func(*PFInput){
		"name":       func(i *PFInput) { i.Mandatory.Name = "x\npass all" },
		"unknown":    func(i *PFInput) { i.Mandatory.Policy.Rules[2].From = []string{"@nope"} },
		"bad rule":   func(i *PFInput) { i.Mandatory.Policy.Rules[0].Name = "x\npass all" },
		"jail name":  func(i *PFInput) { i.Jail = "web_Nginx" },
		"no project": func(i *PFInput) { i.Jail = "nginx" },
	} {
		in := pfBase()
		in.Mandatory = mandBase()
		mut(&in)
		if s, err := PFAnchor(in); err == nil {
			t.Errorf("%s accepted:\n%s", name, s)
		}
	}
}

func TestProjectAnchors(t *testing.T) {
	for key, want := range map[string]string{"db_web": "jailops/db/web", "a-b_c-d": "jailops/a-b/c-d", "plain": "jailops/plain"} {
		if got := JailAnchor(key); got != want {
			t.Errorf("%s: %s", key, got)
		}
	}
	if JailAnchorFor("db", "web") != "jailops/db/web" || ProjectAnchor("db") != "jailops/db" {
		t.Fatal("anchor helpers")
	}
	golden(t, "pf_project_anchor_rules", ProjectAnchorRules())
}

func TestRedirectOnAListenAddress(t *testing.T) {
	out, err := PFAnchor(PFInput{Jail: "alpha_web", Interfaces: []string{"jo123456789012a"}, Uplink: "em0",
		Addresses: []netip.Addr{netip.MustParseAddr("10.20.0.10")},
		Policy: api.FirewallPolicySpec{Rules: []api.FirewallRule{
			{Name: "proxy http", Action: "allow", Direction: "in", Proto: "tcp", Redirect: &api.Redirect{Listen: "192.0.2.7", HostPort: 8080, JailPort: 80}},
			{Name: "proxy dns", Action: "allow", Direction: "in", Proto: "udp", Redirect: &api.Redirect{HostPort: 53, JailPort: 53}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rdr on em0 inet proto tcp from any to 192.0.2.7 port 8080 -> 10.20.0.10 port 80") ||
		!strings.Contains(out, "rdr on em0 inet proto udp from any to (em0) port 53 -> 10.20.0.10 port 53") {
		t.Fatal(out)
	}
	_, err = PFAnchor(PFInput{Jail: "alpha_web", Interfaces: []string{"jo123456789012a"}, Uplink: "em0", Addresses: []netip.Addr{netip.MustParseAddr("10.20.0.10")},
		Policy: api.FirewallPolicySpec{Rules: []api.FirewallRule{{Action: "allow", Direction: "in", Proto: "tcp", Redirect: &api.Redirect{Listen: "x; pass all", HostPort: 1, JailPort: 1}}}}})
	if err == nil {
		t.Fatal("an injected listen address was rendered")
	}
}

func TestDevfsRulesetIsAllowListedAndDeterministic(t *testing.T) {
	n1, txt, err := DevfsRuleset([]string{"tun", "dri", "tun"})
	if err != nil {
		t.Fatal(err)
	}
	n2, _, _ := DevfsRuleset([]string{"dri", "tun"})
	n3, _, _ := DevfsRuleset([]string{"tun"})
	if n1 != n2 || n1 == n3 || n1 < 5000 {
		t.Fatalf("%d %d %d", n1, n2, n3)
	}
	for _, want := range []string{"[jailops_devfs_", "add include $devfsrules_hide_all", "add path 'tun*' unhide", "add path 'dri/*' unhide"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "kmem") {
		t.Fatal(txt)
	}
	for _, bad := range []string{"mem", "../mem", "tun\nadd path mem unhide", ""} {
		if _, _, err := DevfsRuleset([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if n, txt, _ := DevfsRuleset(nil); n != DefaultDevfsRuleset || txt != "" {
		t.Fatal(n, txt)
	}
}

func TestAntispoofNeverNegatesAList(t *testing.T) {
	in := PFInput{Jail: "alpha_web", Interfaces: []string{"joe1a", "joe2a"}, Gateways: []string{"jo0gb"}, Antispoof: true,
		Addresses: []netip.Addr{netip.MustParseAddr("10.20.0.10"), netip.MustParseAddr("fd00::10")},
		Policy:    api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}}
	out, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "! {") || strings.Contains(out, "!{") {
		t.Errorf("a negated list is a pf syntax error:\n%s", out)
	}
	// The table is defined in the anchor that uses it and lets ND and DAD through.
	if !strings.Contains(out, "table <jail_addrs> persist { 10.20.0.10, fd00::10, fe80::/10 }") {
		t.Errorf("no allowed-source table:\n%s", out)
	}
	golden(t, "pf_antispoof_epair", out)
}

func TestEveryAnchorDefinesTheTablesItReferences(t *testing.T) {
	in := PFInput{Jail: "alpha_web", Interfaces: []string{"joe1a"}, Antispoof: true, Uplink: "em0",
		Addresses:  []netip.Addr{netip.MustParseAddr("10.20.0.10")},
		Tags:       []string{"app"},
		TagMembers: map[string][]netip.Addr{"db": {netip.MustParseAddr("10.20.0.20")}},
		Sets:       map[string]api.AddressSetSpec{"office": {Addresses: []string{"192.0.2.0/24"}}},
		Policy: api.FirewallPolicySpec{Rules: []api.FirewallRule{
			{Action: "allow", Direction: "in", Proto: "tcp", From: []string{"@office", "tag:db"}, Ports: []string{"22"}}}}}
	out, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	def := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^table <([a-z0-9_]+)>`).FindAllStringSubmatch(out, -1) {
		def[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`<([a-z0-9_]+)>`).FindAllStringSubmatch(out, -1) {
		if !def[m[1]] {
			t.Errorf("table <%s> is used but not defined in the anchor:\n%s", m[1], out)
		}
	}
	if len(def) != 3 {
		t.Errorf("tables %v", def)
	}
}

func TestProjectAnchorEndsWithAnchorWildcardAndAdminLines(t *testing.T) {
	rules := strings.Split(strings.TrimSpace(ProjectAnchorRules()), "\n")
	if rules[len(rules)-1] != `anchor "*"` {
		t.Fatalf("a project anchor must end with anchor \"*\": %v", rules)
	}
	if AnchorLines() != "nat-anchor \"jailops/*\"\nrdr-anchor \"jailops/*\"\nanchor \"jailops/*\"\n" {
		t.Fatalf("%q", AnchorLines())
	}
}

func TestPFRulesForTrafficIntoTheJailAlsoStandOnTheGateway(t *testing.T) {
	in := pfBase()
	in.Gateways = []string{"jo9gb"}
	in.Policy = api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow", Rules: []api.FirewallRule{
		{Name: "web", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}},
		{Name: "dns", Action: "allow", Direction: "out", Proto: "udp", Ports: []string{"53"}},
	}}
	s, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	sec := s[strings.Index(s, "# into the jail from the host and from routed networks"):]
	for _, want := range []string{
		"pass out quick on jo9gb inet6 proto icmp6 from any to { 2001:db8:1::100, fe80::/10, ff02::1:ff00:0/104 } icmp6-type { neighbrsol, neighbradv }",
		"pass out quick on jo9gb inet proto tcp from any to 10.20.0.10 port 80",
		"block drop out quick on jo9gb inet from any to 10.20.0.10",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("missing %q in\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "port 53") {
		t.Errorf("a rule for traffic out of the jail was repeated on the bridge:\n%s", sec)
	}
	golden(t, "pf_anchor_bridge_section", sec)
}

func TestPFProjectsAreIsolatedUnlessARuleOpensIt(t *testing.T) {
	in := pfBase()
	in.Gateways = []string{"jo9gb"}
	in.Foreign = []netip.Prefix{netip.MustParsePrefix("10.30.0.0/24"), netip.MustParsePrefix("fd30::/64")}
	in.Policy = api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "allow", Rules: []api.FirewallRule{
		{Name: "from-common", Action: "allow", Direction: "in", Proto: "tcp", From: []string{"10.30.0.5"}, Ports: []string{"5432"}},
	}}
	s, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	iAllow := strings.Index(s, "from 10.30.0.5")
	iBlock := strings.Index(s, "block drop out quick on jo0123456789a0a from <foreign> to any")
	if iAllow < 0 || iBlock < 0 || iAllow > iBlock {
		t.Fatalf("the explicit allow must come before the isolation:\n%s", s)
	}
	for _, want := range []string{
		"table <foreign> persist { 10.30.0.0/24, fd30::/64 }",
		"block drop in quick on jo0123456789a0a from any to <foreign>",
		"block drop out quick on jo9gb inet from <foreign> to 10.20.0.10",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	golden(t, "pf_anchor_foreign", s)
}

// Every rendered PF golden must be accepted by the real pfctl. It runs where pfctl exists (FreeBSD); elsewhere it is
// skipped, and scripts/ci-freebsd-pf.sh runs the same check in CI. (This is how misplaced icmp6-type and tagged
// keywords were found: the renderer's own tests cannot know the grammar of pf.)
func TestPFGoldensParseWithPfctl(t *testing.T) {
	pfctl, err := exec.LookPath("pfctl")
	if err != nil {
		t.Skip("pfctl is not installed")
	}
	files, _ := filepath.Glob("testdata/pf_*.golden")
	if len(files) == 0 {
		t.Fatal("no goldens")
	}
	for _, f := range files {
		out, err := exec.Command(pfctl, "-n", "-a", "jailops/x/y", "-f", f).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "Permission denied") {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
	}
}

func TestPassRuleLabelsAreStableAndSayWhenARuleChanged(t *testing.T) {
	render := func(mut func(*PFInput)) string {
		in := pfBase()
		if mut != nil {
			mut(&in)
		}
		s, err := PFAnchor(in)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	labels := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range regexp.MustCompile(`label "(jo-[0-9a-f]{12})"`).FindAllStringSubmatch(s, -1) {
			m[l[1]] = true
		}
		return m
	}
	a := render(nil)
	if la := labels(a); len(la) == 0 {
		t.Fatal("no labels")
	} else if lb := labels(render(nil)); len(lb) != len(la) {
		t.Fatal("rendering is not deterministic")
	}
	// the same rule in another jail's anchor has another label, so killing one jail's flows never touches another's
	b := render(func(in *PFInput) { in.Jail = "alpha_db" })
	for l := range labels(a) {
		if labels(b)[l] {
			t.Fatalf("label %s is shared by two jails", l)
		}
	}
	// a member removed from an address set changes the label of the rule that names the set, so its flows end
	c := render(func(in *PFInput) {
		in.Sets = map[string]api.AddressSetSpec{"office": {Addresses: []string{"203.0.113.0/24"}}, "vpn": {Addresses: []string{"198.51.100.7"}}}
	})
	if len(labels(a)) == len(labels(c)) {
		same := true
		for l := range labels(a) {
			same = same && labels(c)[l]
		}
		if same {
			t.Fatal("changing a set did not change any label")
		}
	}
	// rules without a state have no label
	for _, ln := range strings.Split(a, "\n") {
		if strings.Contains(ln, "no state") && strings.Contains(ln, "label") {
			t.Fatalf("a stateless rule has a label: %s", ln)
		}
	}
}

// A deny put in front of an unchanged pass rule changes what the pass rule lets through, so its label changes and the
// states of the flows it admitted end; a rule added below it leaves it alone.
func TestALabelChangesWhenARuleIsInsertedAbove(t *testing.T) {
	base := "pass out quick on jo0a inet proto tcp port 443\n" +
		"pass in quick on jo0a all\n"
	deny := "block drop out quick on jo0a inet from any to 10.0.0.0/8\n"
	label := func(text, rule string) string {
		for _, ln := range strings.Split(labelPassRules("jailops/alpha/web", text), "\n") {
			if strings.HasPrefix(ln, rule) {
				if i := strings.Index(ln, `label "`); i >= 0 {
					return ln[i:]
				}
			}
		}
		t.Fatalf("no labelled %q in %q", rule, text)
		return ""
	}
	first, second := "pass out quick on jo0a inet proto tcp port 443", "pass in quick on jo0a all"
	if label(base, second) != label(base, second) {
		t.Fatal("a label is not stable")
	}
	withDeny := first + "\n" + deny + second + "\n"
	if label(base, first) != label(withDeny, first) {
		t.Error("a rule that has nothing new above it changed its label")
	}
	if label(base, second) == label(withDeny, second) {
		t.Error("a deny inserted above a pass rule did not change its label: its established flows would go on")
	}
	// the members of a table used by a rule above count too
	tbl := func(members string) string {
		return "table <bad> persist { " + members + " }\n" + "block drop out quick on jo0a from any to <bad>\n" + second + "\n"
	}
	if label(tbl("10.0.0.0/8"), second) == label(tbl("10.0.0.0/8, 192.168.0.0/16"), second) {
		t.Error("an address added to a table used by a deny above did not change the label")
	}
	// a rule added below changes nothing above it
	extra := base + "pass out quick on jo0a inet proto udp port 53\n"
	if label(base, second) != label(extra, second) || label(base, first) != label(extra, first) {
		t.Error("a rule added at the end changed the labels above it")
	}
}

func TestPFNeighbourDiscoveryAndICMPv6(t *testing.T) {
	in := PFInput{Jail: "alpha_web", Interfaces: []string{"joe1a"}, Gateways: []string{"jo0gb"}, Antispoof: true,
		Addresses: []netip.Addr{ip("10.20.0.10"), ip("2001:db8:1::100")},
		LinkLocal: []netip.Addr{ip("fe80::2858:8aff:fe88:fcd2"), ip("10.1.1.1")}, // a non link-local entry is ignored
		Policy:    api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "deny"}}
	out, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_nd_icmp6", out)
	for _, want := range []string{
		"table <jail_addrs> persist { 10.20.0.10, 2001:db8:1::100, fe80::2858:8aff:fe88:fcd2 }",
		// a jail is not a router
		"block in quick on joe1a inet6 proto icmp6 from any to any icmp6-type { routeradv, redir }",
		// :: only for DAD, only to a solicited-node group
		"pass in quick on joe1a inet6 proto icmp6 from :: to ff02::1:ff00:0/104 icmp6-type neighbrsol",
		// no advertisement to a multicast group
		"block in quick on joe1a inet6 proto icmp6 from any to ff02::/16 icmp6-type neighbradv",
		// the jail's own addresses are the only sources
		"from { 2001:db8:1::100, fe80::2858:8aff:fe88:fcd2 } to any icmp6-type { neighbrsol, neighbradv }",
		// only its own addresses and groups reach the jail
		"to { 2001:db8:1::100, fe80::2858:8aff:fe88:fcd2, ff02::1:ff00:100, ff02::1:ff88:fcd2 } icmp6-type { neighbrsol, neighbradv }",
		// path MTU discovery: ICMPv6 errors pass both ways and on the gateway, also under a default deny
		"pass in quick on joe1a inet6 proto icmp6 from { 2001:db8:1::100, fe80::2858:8aff:fe88:fcd2 } to any icmp6-type { unreach, toobig, timex, paramprob }",
		"pass out quick on joe1a inet6 proto icmp6 from any to any icmp6-type { unreach, toobig, timex, paramprob }",
		"pass out quick on jo0gb inet6 proto icmp6 from any to 2001:db8:1::100 icmp6-type { unreach, toobig, timex, paramprob }",
		"to ff02::/16 icmp6-type { listenrep, listendone, 143 }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fe80::/10") {
		t.Errorf("the jail's own link-local address is known, fe80::/10 must not appear:\n%s", out)
	}
	// every ICMPv6 pass comes before the default deny
	if i, j := strings.Index(out, "toobig"), strings.Index(out, "block drop out quick on joe1a all"); i < 0 || j < i {
		t.Errorf("ICMPv6 errors must precede the defaults")
	}
	// unknown link-local address: the whole range stays allowed
	in.LinkLocal = nil
	out, _ = PFAnchor(in)
	if !strings.Contains(out, "fe80::/10") || !strings.Contains(out, "ff02::1:ff00:0/104") {
		t.Errorf("fallback without link-local addresses:\n%s", out)
	}
	// an IPv4-only jail has no IPv6 rules
	in.Addresses = []netip.Addr{ip("10.20.0.10")}
	if out, _ = PFAnchor(in); strings.Contains(out, "icmp6") {
		t.Errorf("IPv4-only jail has IPv6 rules:\n%s", out)
	}
}

// A jail with /128 addresses (proxy mode) never advertises: the host has a permanent entry for it and nobody asks.
func TestPFNoAdvertise(t *testing.T) {
	in := PFInput{Jail: "alpha_web", Interfaces: []string{"joe1a"}, Antispoof: true, NoAdvertise: true,
		Addresses: []netip.Addr{ip("2001:db8:1::100")}, LinkLocal: []netip.Addr{ip("fe80::2858:8aff:fe88:fcd2")},
		Policy: api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "allow"}}
	out, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_no_advertise", out)
	if !strings.Contains(out, "block in quick on joe1a inet6 proto icmp6 from any to any icmp6-type neighbradv") {
		t.Errorf("an advertisement from the jail is not blocked:\n%s", out)
	}
	if !strings.Contains(out, "to any icmp6-type neighbrsol label") {
		t.Errorf("the jail's own solicitations are not passed:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "pass in") && strings.Contains(ln, "neighbradv") {
			t.Errorf("the jail may still advertise: %s", ln)
		}
	}
	// the DAD solicitation from :: still passes
	if !strings.Contains(out, "from :: to ff02::1:ff00:0/104 icmp6-type neighbrsol") {
		t.Errorf("DAD is blocked:\n%s", out)
	}
	// without the flag the jail may advertise (on-link jails must answer each other)
	in.NoAdvertise = false
	out, _ = PFAnchor(in)
	if !strings.Contains(out, "icmp6-type { neighbrsol, neighbradv }") {
		t.Errorf("an on-link jail cannot answer:\n%s", out)
	}
}
