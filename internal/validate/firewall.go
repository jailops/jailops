package validate

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"

	"unicode"
	"unicode/utf8"
)

// MaxRawLen bounds a raw PF fragment.
const MaxRawLen = 64 << 10

// FirewallReport is what validation learned besides errors.
type FirewallReport struct {
	// RequiresAdmin is set when the policy contains a raw PF fragment.
	RequiresAdmin bool
	Reasons       []string
}

// FirewallPolicySpec validates a policy. A raw fragment is syntactically
// screened here only; pfctl -n has the last word before loading.
func FirewallPolicySpec(s api.FirewallPolicySpec) (FirewallReport, error) {
	var rep FirewallReport
	for _, d := range []struct{ f, v string }{{"default_in", s.DefaultIn}, {"default_out", s.DefaultOut}} {
		switch d.v {
		case "allow", "deny":
		case "":
			return rep, Invalid(d.f, "%s is required: allow or deny (a policy says its defaults)", d.f)
		default:
			return rep, Invalid(d.f, "%s %q must be allow or deny", d.f, d.v)
		}
	}
	if err := Comment("comment", s.Comment); err != nil {
		return rep, err
	}
	for i, r := range s.Rules {
		if err := rule(fmt.Sprintf("rules[%d]", i), r); err != nil {
			return rep, err
		}
	}
	if s.Raw != "" {
		if len(s.Raw) > MaxRawLen {
			return rep, Invalid("raw", "raw fragment is larger than %d bytes", MaxRawLen)
		}
		for i := 0; i < len(s.Raw); i++ {
			c := s.Raw[i]
			if c == 0 || c < 0x20 && c != '\n' && c != '\t' && c != '\r' {
				return rep, Invalid("raw", "raw fragment contains a control character")
			}
		}
		rep.RequiresAdmin = true
		rep.Reasons = append(rep.Reasons, "raw PF fragment")
	}
	return rep, nil
}

// CommentMax is the longest comment on a firewall object: a rule, a policy, an address set or one of its entries.
const CommentMax = 200

// Comment checks the comment of a firewall object: one line of printable text of at most CommentMax characters, valid
// UTF-8, without control, bidirectional or zero-width characters (a review tool shows what is there), line or paragraph
// separators, double quotes or backslashes. The comment becomes a comment line of the PF ruleset: a backslash at the end of
// a line would continue the comment onto the next line, which would then be part of it (a rule that is gone), and the
// helper refuses a ruleset line with `file "` in it, comment or not.
func Comment(field, c string) error {
	if n := utf8.RuneCountInString(c); n > CommentMax {
		return Invalid(field, "the comment is %d characters, at most %d", n, CommentMax)
	}
	if api.HasUnsafeText(c, "") || strings.ContainsFunc(c, func(r rune) bool { return unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) }) {
		return Invalid(field, "the comment must be one line of printable text")
	}
	if strings.ContainsAny(c, "\"\\") {
		return Invalid(field, "the comment may not contain a double quote or a backslash: it becomes a comment line of the PF ruleset")
	}
	return nil
}

func rule(f string, r api.FirewallRule) error {
	if err := Comment(f+".comment", r.Comment); err != nil {
		return err
	}
	if r.Name != "" {
		if err := RuleName(f+".name", r.Name); err != nil {
			return err
		}
	}
	switch r.Action {
	case "allow", "deny":
	default:
		return Invalid(f+".action", "action %q must be allow or deny", r.Action)
	}
	switch r.Direction {
	case "in", "out":
	default:
		return Invalid(f+".direction", "direction %q must be in or out", r.Direction)
	}
	switch r.Proto {
	case "", "tcp", "udp", "icmp", "icmp6":
	default:
		return Invalid(f+".proto", "proto %q must be tcp, udp, icmp or icmp6", r.Proto)
	}
	if len(r.Ports) > 0 && r.Proto != "tcp" && r.Proto != "udp" {
		return Invalid(f+".ports", "ports need proto tcp or udp")
	}
	for _, p := range r.Ports {
		if _, _, err := ParsePortSpec(p); err != nil {
			return Invalid(f+".ports", "port %q is invalid", p)
		}
	}
	for _, e := range []struct {
		n string
		l []string
	}{{"from", r.From}, {"to", r.To}} {
		for _, a := range e.l {
			if err := ValidateEndpoint(a); err != nil {
				return Invalid(f+"."+e.n, "%s", err.Error())
			}
		}
	}
	if rd := r.Redirect; rd != nil {
		if r.Direction != "in" {
			return Invalid(f+".redirect", "redirect is allowed on inbound rules only")
		}
		if r.Action != "allow" {
			return Invalid(f+".redirect", "redirect needs action allow")
		}
		if r.Proto != "tcp" && r.Proto != "udp" {
			return Invalid(f+".redirect", "redirect needs proto tcp or udp")
		}
		if len(r.Ports) > 0 {
			return Invalid(f+".ports", "ports must be empty when redirect is set; the redirect names the ports")
		}
		if len(r.To) > 0 {
			return Invalid(f+".to", "to must be empty when redirect is set; the destination is the jail")
		}
		if rd.HostPort < 1 || rd.HostPort > 65535 || rd.JailPort < 1 || rd.JailPort > 65535 {
			return Invalid(f+".redirect", "redirect ports must be between 1 and 65535")
		}
	}
	return nil
}

// ParsePortSpec parses "80" or "8000-8100".
func ParsePortSpec(s string) (lo, hi int, err error) {
	a, b, isRange := strings.Cut(s, "-")
	if !isRange {
		b = a
	}
	conv := func(x string) (int, error) {
		if x == "" || len(x) > 5 {
			return 0, fmt.Errorf("bad port")
		}
		for i := 0; i < len(x); i++ {
			if x[i] < '0' || x[i] > '9' {
				return 0, fmt.Errorf("bad port")
			}
		}
		n, _ := strconv.Atoi(x)
		if n < 1 || n > 65535 {
			return 0, fmt.Errorf("port out of range")
		}
		return n, nil
	}
	if lo, err = conv(a); err != nil {
		return
	}
	if hi, err = conv(b); err != nil {
		return
	}
	if hi < lo {
		return 0, 0, fmt.Errorf("range ends before it starts")
	}
	return lo, hi, nil
}

// EndpointKind classifies a from/to entry.
type EndpointKind int

const (
	EndpointAny EndpointKind = iota
	EndpointAddr
	EndpointSet
	EndpointTag
)

// ParseEndpoint classifies a from/to entry: "any", an address or CIDR,
// "@name" (an address set, or a jail of the project) or "tag:<tag>". For EndpointAddr the canonical text is in value.
func ParseEndpoint(s string) (kind EndpointKind, value string, err error) {
	switch {
	case s == "any":
		return EndpointAny, "any", nil
	case strings.HasPrefix(s, "@"):
		if !setRefRE.MatchString(s[1:]) || strings.HasSuffix(s, "-") {
			return 0, "", fmt.Errorf("reference %q is invalid: @name is an address set or a jail of the project", s)
		}
		return EndpointSet, s[1:], nil
	case strings.HasPrefix(s, "tag:"):
		if e := Tag("tag", s[4:]); e != nil {
			return 0, "", fmt.Errorf("tag reference %q is invalid", s)
		}
		return EndpointTag, s[4:], nil
	}
	if strings.Contains(s, "/") {
		p, e := netip.ParsePrefix(s)
		if e != nil || p.Addr().Zone() != "" || p.Addr().Is4In6() {
			return 0, "", fmt.Errorf("%q is not a valid CIDR", s)
		}
		return EndpointAddr, p.Masked().String(), nil
	}
	a, e := netip.ParseAddr(s)
	if e != nil || a.Zone() != "" || a.Is4In6() {
		return 0, "", fmt.Errorf("%q is not a valid address, @set or tag:<tag>", s)
	}
	return EndpointAddr, a.String(), nil
}

// ValidateEndpoint checks one from/to entry.
func ValidateEndpoint(s string) error {
	_, _, err := ParseEndpoint(s)
	return err
}

// AddressSetSpec validates an address set.
func AddressSetSpec(s api.AddressSetSpec) error {
	for _, e := range s.Addresses {
		k, _, err := ParseEndpoint(e.Address)
		if err != nil || (k != EndpointAddr && k != EndpointSet) {
			return Invalid("addresses", "address set member %q must be an address, a CIDR or @jail", e.Address)
		}
		if err := Comment("addresses", e.Comment); err != nil {
			return Invalid("addresses", "the comment of %q: %v", e.Address, err)
		}
	}
	if err := Comment("comment", s.Comment); err != nil {
		return err
	}
	return nil
}

// FlattenSet expands an address set with the sets and jails it names as @name into canonical,
// sorted, de-duplicated address/CIDR strings. A missing name or a cycle is an error.
func FlattenSet(name string, sets map[string]api.AddressSetSpec) ([]string, error) {
	seen := map[string]bool{}
	var walk func(n string, path []string) error
	var out []string
	walk = func(n string, path []string) error {
		for _, p := range path {
			if p == n {
				return Invalid("addresses", "address set cycle: %s -> %s", strings.Join(path, " -> "), n)
			}
		}
		s, ok := sets[n]
		if !ok {
			return Invalid("addresses", "address set or jail %q does not exist (a rule or a set names @%s)", n, n)
		}
		if seen[n] {
			return nil
		}
		path = append(path, n)
		for _, e := range s.Addresses {
			k, v, err := ParseEndpoint(e.Address)
			if err != nil || (k != EndpointAddr && k != EndpointSet) {
				return Invalid("addresses", "address set %q member %q must be an address, a CIDR or @jail", n, e.Address)
			}
			if k == EndpointSet { // @name: a jail of the project (its addresses), or another set
				if err := walk(v, path); err != nil {
					return err
				}
				continue
			}
			out = append(out, v)
		}
		// A set reached again through another path is flattened once; the
		// cycle check above only fires for sets on the current path.
		seen[n] = true
		return nil
	}
	if err := walk(name, nil); err != nil {
		return nil, err
	}
	sort.Strings(out)
	uniq := out[:0]
	for i, s := range out {
		if i == 0 || s != out[i-1] {
			uniq = append(uniq, s)
		}
	}
	return uniq, nil
}
