// Package isolation holds the pure, Linux-testable logic of prototype 8: naming, PF anchor
// rendering for project policy, classification of the bridge pfil sysctls, and the results
// matrix. Nothing here was run on FreeBSD by its author.
//
// Scope note: prototype 8 verifies CONFIGURATION and CONNECTIVITY only, with ordinary tools
// (ping, nc, ifconfig, pfctl, sysctl, tcpdump as a passive observer). It never forges, injects
// or crafts a packet (no raw frames, no crafted ARP/NDP/RA/DHCP). See prototypes/README.md for
// why, and for how an operator can run an active spoofing test by hand in an isolated lab.
package isolation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// AnchorRoot is the PF anchor namespace this prototype may touch. It needs the same three
// administrator lines in pf.conf as prototype 3 (nat-anchor/rdr-anchor/anchor "jailops/*"),
// because both prototypes load under the "jailops" parent anchor.
const AnchorRoot = "jailops/jop8"

var anchorRe = regexp.MustCompile(`^jailops/jop8(/[a-z0-9_]{1,16}){0,2}$`)

// ValidAnchor reports whether name is an anchor this prototype may load into: the root plus one
// or two path segments, or the namespace anchor itself, which only receives the wildcard line.
func ValidAnchor(name string) bool { return anchorRe.MatchString(name) }

// Projects and jails. Project A has two jails (intra-project pair); project B has one (the
// cross-project peer). All three share one bridge.
const (
	ProjectA = "jop8a"
	ProjectB = "jop8b"

	JailA1 = "jop8a1"
	JailA2 = "jop8a2"
	JailB1 = "jop8b1"

	Bridge = "jop8br0"
)

// Jails lists every jail this prototype creates, in creation order.
var Jails = []string{JailA1, JailA2, JailB1}

// ProjectOf returns the owning project of a jail name.
func ProjectOf(jail string) string {
	switch jail {
	case JailA1, JailA2:
		return ProjectA
	case JailB1:
		return ProjectB
	}
	return ""
}

// PeersOf returns the other jails of the same project (same-project reachability pairs).
func PeersOf(jail string) []string {
	var out []string
	for _, j := range Jails {
		if j != jail && ProjectOf(j) == ProjectOf(jail) {
			out = append(out, j)
		}
	}
	return out
}

// suffix strips the "jop8" prefix, e.g. jop8a1 -> a1.
func suffix(jail string) string { return strings.TrimPrefix(jail, "jop8") }

// EpairHost and EpairJail return the two halves of a jail's epair. By FreeBSD epair(4)
// convention the "a" half is created and kept on the host; the "b" half moves into the jail's
// vnet. Both names stay under 16 characters and carry the mandatory "jop" prefix.
func EpairHost(jail string) string { return "jop8e" + suffix(jail) + "a" }
func EpairJail(jail string) string { return "jop8e" + suffix(jail) + "b" }

// RFC 5737 (TEST-NET-1) and the documentation IPv6 prefix (RFC 3849) back this experiment's
// bridge: addresses that can never collide with a real network, same convention as prototype 3.
const (
	GatewayV4 = "192.0.2.1"
	GatewayV6 = "2001:db8:8::1"

	AddrA1V4 = "192.0.2.11"
	AddrA1V6 = "2001:db8:8::11"
	AddrA2V4 = "192.0.2.12"
	AddrA2V6 = "2001:db8:8::12"
	AddrB1V4 = "192.0.2.21"
	AddrB1V6 = "2001:db8:8::21"

	V4Prefix = "24"
	V6Prefix = "64"

	BroadcastV4 = "192.0.2.255"
	AllNodesV6  = "ff02::1"
)

// AddrOf returns the IPv4 and IPv6 address assigned to a jail.
func AddrOf(jail string) (v4, v6 string) {
	switch jail {
	case JailA1:
		return AddrA1V4, AddrA1V6
	case JailA2:
		return AddrA2V4, AddrA2V6
	case JailB1:
		return AddrB1V4, AddrB1V6
	}
	return "", ""
}

// AllAddrs returns every jail's v4 and v6 address.
func AllAddrs() []string {
	var out []string
	for _, j := range Jails {
		v4, v6 := AddrOf(j)
		out = append(out, v4, v6)
	}
	return out
}

// OtherAddrs returns the addresses of every jail not in project.
func OtherAddrs(project string) []string {
	var out []string
	for _, j := range Jails {
		if ProjectOf(j) != project {
			v4, v6 := AddrOf(j)
			out = append(out, v4, v6)
		}
	}
	return out
}

// OwnAddrs returns the addresses of every jail of project.
func OwnAddrs(project string) []string {
	var out []string
	for _, j := range Jails {
		if ProjectOf(j) == project {
			v4, v6 := AddrOf(j)
			out = append(out, v4, v6)
		}
	}
	return out
}

// JailAnchor and ProjectAnchor name the per-jail and per-project PF anchors.
func JailAnchor(jail string) string {
	return fmt.Sprintf("%s/%s/%s", AnchorRoot, ProjectOf(jail), jail)
}
func ProjectAnchor(project string) string { return AnchorRoot + "/" + project }

// RenderJailAnchor returns the PF ruleset for one jail's own anchor: traffic entering the host
// on that jail's epair must carry one of the jail's assigned addresses as source. This is the
// literal rule design 8.1 names ("block in on <epair> from !<addr>"); it is loaded with ordinary
// `pfctl -a <anchor> -f -`, never by forging a packet.
func RenderJailAnchor(epair string, addrs ...string) string {
	var b strings.Builder
	// Verified on FreeBSD 15.1: a negated list `from ! { a b }` is a syntax error (and pf would
	// expand it into one rule per element anyway). Use a table. IPv6 neighbor discovery needs the
	// unspecified address (duplicate address detection) and link-local sources, which are allowed
	// explicitly; everything else that is not one of the jail's own addresses is dropped.
	fmt.Fprintf(&b, "table <jail_addrs> persist { %s, fe80::/10, :: }\n", strings.Join(addrs, ", "))
	fmt.Fprintf(&b, "block in quick on %s from ! <jail_addrs> to any\n", epair)
	return b.String()
}

// RenderProjectAnchor returns the PF ruleset for one project's anchor: pass traffic between the
// project's own addresses, block it both ways between the project's addresses and every other
// project's addresses. own and other are address lists (v4 and v6 mixed; pf tables hold both).
func RenderProjectAnchor(project string, own, other []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table <%s_own> persist { %s }\n", project, strings.Join(own, " "))
	fmt.Fprintf(&b, "table <%s_other> persist { %s }\n", project, strings.Join(other, " "))
	fmt.Fprintf(&b, "block return quick from <%s_own> to <%s_other>\n", project, project)
	fmt.Fprintf(&b, "block return quick from <%s_other> to <%s_own>\n", project, project)
	fmt.Fprintf(&b, "pass quick from <%s_own> to <%s_own>\n", project, project)
	// Verified on FreeBSD 15.1: an anchor nested below another is evaluated only if every
	// intermediate anchor contains `anchor "*"`. The project anchor evaluates the jails' anchors.
	b.WriteString("anchor \"*\"\n")
	return b.String()
}

// RenderConnRateLimit returns a PF rule that caps new connections per second from one source as
// a "state limit" for test 5. PF alone has no bandwidth shaper without ALTQ or dummynet, which
// this prototype does not configure; the rule only bounds connection rate, and the README says so.
func RenderConnRateLimit(epair string, port int, perSec int) string {
	return fmt.Sprintf("pass in quick on %s proto tcp to any port %d flags S/SA keep state (max-src-conn-rate %d/1)\n",
		epair, port, perSec)
}

// Pfil sysctls that decide whether PF filters traffic exchanged directly between bridge
// members, rather than only traffic that crosses the bridge interface itself. Their FreeBSD 15
// defaults are UNVERIFIED; test 2 of prototype 8 settles which combination is required.
const (
	SysctlPfilBridge = "net.link.bridge.pfil_bridge"
	SysctlPfilMember = "net.link.bridge.pfil_member"
	SysctlPfilOnlyIP = "net.link.bridge.pfil_onlyip"
)

// PfilCombo is one trial setting of the three sysctls.
type PfilCombo struct{ Member, Bridge, OnlyIP string }

// PfilCombos enumerates the trials test 2 tries, starting from the design's documented
// expectation (member=1) and then varying the other two.
var PfilCombos = []PfilCombo{
	{Member: "0", Bridge: "1", OnlyIP: "1"},
	{Member: "1", Bridge: "1", OnlyIP: "1"},
	{Member: "1", Bridge: "0", OnlyIP: "1"},
	{Member: "1", Bridge: "1", OnlyIP: "0"},
}

func (c PfilCombo) String() string {
	return fmt.Sprintf("pfil_member=%s pfil_bridge=%s pfil_onlyip=%s", c.Member, c.Bridge, c.OnlyIP)
}

// ParseSysctlBool parses the trimmed output of `sysctl -n <name>` ("0" or "1").
func ParseSysctlBool(out string) (v bool, ok bool) {
	switch strings.TrimSpace(out) {
	case "0":
		return false, true
	case "1":
		return true, true
	}
	return false, false
}

// Flow is one recorded connectivity observation.
type Flow struct {
	Test      string `json:"test"`
	Condition string `json:"condition"`
	From      string `json:"from"`
	To        string `json:"to"`
	Proto     string `json:"proto"`
	Ok        bool   `json:"ok"`
	Detail    string `json:"detail"`
}

// Matrix collects every flow of a run plus narrative observations (sysctl states, throughput
// numbers) that are not naturally a from/to flow. It is rendered into one Check's detail field,
// so the fixed harness.Verdict JSON shape used by every prototype does not need to change.
type Matrix struct {
	Flows        []Flow            `json:"flows"`
	Observations map[string]string `json:"observations,omitempty"`
}

// Add records one flow.
func (m *Matrix) Add(test, condition, from, to, proto string, ok bool, detail string) {
	m.Flows = append(m.Flows, Flow{Test: test, Condition: condition, From: from, To: to, Proto: proto, Ok: ok, Detail: detail})
}

// Observe records one narrative finding.
func (m *Matrix) Observe(key, value string) {
	if m.Observations == nil {
		m.Observations = map[string]string{}
	}
	m.Observations[key] = value
}

// JSON renders the matrix compactly (for embedding as a Check detail).
func (m *Matrix) JSON() string {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

var anchorArg = regexp.MustCompile(`^jailops(/jop8(/[a-z0-9_]{1,16}){0,2})?$`)

// GuardPfctl refuses any pfctl invocation that could change state outside a valid prototype
// anchor, or read a foreign one. Modeled on prototype 3's guard (internal/pfanchor.GuardPfctl).
func GuardPfctl(args []string) error {
	anchor := ""
	mutating := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-a":
			if i+1 >= len(args) {
				return fmt.Errorf("pfctl: -a without anchor")
			}
			anchor = args[i+1]
			i++
		case "-F", "-T", "-k", "-K", "-O", "-R", "-z":
			mutating = true
		case "-e", "-d":
			return fmt.Errorf("pfctl: refusing to enable or disable pf: %v", args)
		case "-f":
			mutating = true
			if i+1 >= len(args) || args[i+1] != "-" {
				return fmt.Errorf("pfctl: -f must read stdin, got %v", args)
			}
		}
	}
	if mutating {
		if !ValidAnchor(anchor) {
			return fmt.Errorf("pfctl: mutating command needs an anchor under %s, got %q in %v", AnchorRoot, anchor, args)
		}
	} else if anchor != "" && !anchorArg.MatchString(anchor) {
		return fmt.Errorf("pfctl: query of foreign anchor %q", anchor)
	}
	return nil
}

// ValidSysctlName restricts SetSysctl (in the cmd package) to the three pfil sysctls and the two
// IP/IPv6 forwarding switches this prototype may toggle (restored by --cleanup and the watchdog).
var validSysctl = map[string]bool{
	SysctlPfilBridge:           true,
	SysctlPfilMember:           true,
	SysctlPfilOnlyIP:           true,
	"net.inet.ip.forwarding":   true,
	"net.inet6.ip6.forwarding": true,
}

// ValidSysctlName reports whether name is one this prototype may write.
func ValidSysctlName(name string) bool { return validSysctl[name] }
