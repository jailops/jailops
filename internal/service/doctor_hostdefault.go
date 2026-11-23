package service

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// jailUplinks are the uplinks of the networks the jails use, sorted.
func (s *Service) jailUplinks() []string {
	used := map[string]bool{}
	s.mu.RLock()
	for _, e := range s.jails {
		for _, a := range e.jail.Spec.Network {
			used[a.Network] = true
		}
	}
	s.mu.RUnlock()
	nets, _ := s.netKind.list()
	var out []string
	for _, o := range nets {
		if used[o.Meta.Name] && o.Spec.Uplink != "" && !slices.Contains(out, o.Spec.Uplink) {
			out = append(out, o.Spec.Uplink)
		}
	}
	slices.Sort(out)
	return out
}

// openUplinks returns the uplinks on which the loaded main ruleset (pfctl -s rules) has no inbound default block: no
// `block [drop|return] [in] [log] [on <uplink>|on ! <other>] all` (quick or not), or one that a later pass of everything
// in on that interface undoes. pf's own default is pass, so a host without such a rule lets the Internet reach every
// address the anchors do not close: the routed addresses of the jails, and the NAT subnets for whoever routes them to
// the host. Rules the reader does not understand (a source, a port, a table, an interface group) count as neither.
//
// Each address family is decided on its own: `block in inet all` leaves IPv6 open (the routed case the check is for),
// and `pass in inet all` after `block in all` opens IPv4 only. An uplink open in one family only is named with it:
// "vtnet0 (IPv6)".
func openUplinks(rules string, uplinks []string) []string {
	var open []string
	lines := strings.Split(rules, "\n")
	for _, up := range uplinks {
		var fams []string
		for _, fam := range []string{"inet", "inet6"} {
			blocked := false
			for _, ln := range lines {
				r, ok := parsePFRule(ln)
				if !ok || r.dir == "out" || r.from != "any" || r.to != "any" || r.proto != "" || r.hasPort || !onUplink(r, up) || r.family != "" && r.family != fam {
					continue
				}
				blocked = r.action == "block" // the last match decides: a pass in all after the block passes everything again
				if r.quick {
					break // the first quick match decides
				}
			}
			if !blocked {
				fams = append(fams, map[string]string{"inet": "IPv4", "inet6": "IPv6"}[fam])
			}
		}
		switch len(fams) {
		case 2:
			open = append(open, up)
		case 1:
			open = append(open, up+" ("+fams[0]+")")
		}
	}
	return open
}

// onUplink says whether a rule read by parsePFRule applies to everything that comes in on up: no interface, the uplink
// itself, or "on ! X" for another X. The group "egress" holds the interface of the default route, the usual uplink.
func onUplink(r pfRule, up string) bool {
	switch {
	case r.iface == "" || r.iface == up || r.iface == "egress":
		return true
	case r.iface == "!" || strings.HasPrefix(r.iface, "!"):
		f := strings.Fields(r.text)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "on" {
				other := strings.TrimPrefix(f[i+1], "!")
				if other == "" && i+2 < len(f) {
					other = f[i+2]
				}
				return other != "" && other != up
			}
		}
	}
	return false
}

// doctorHostDefaultDeny warns when the host's pf.conf does not block inbound traffic on an uplink of the jails by
// default. The anchors open the uplink for what the policies admit and never close it: that is the host's pf.conf's
// part, and "closed" in firewall test, firewall show and firewall exposed assumes it. Unreadable rules say nothing.
func (s *Service) doctorHostDefaultDeny(c *checkSet, rules string) {
	const name = "pf.host_default_deny"
	uplinks := s.jailUplinks()
	if len(uplinks) == 0 || strings.TrimSpace(rules) == "" {
		return
	}
	open := openUplinks(rules, uplinks)
	if len(open) == 0 {
		c.add(name, api.DoctorOK, "pf.conf blocks inbound traffic by default on "+strings.Join(uplinks, ", ")+": what the policies of the jails do not admit from outside stays out", "")
		return
	}
	c.add(name, api.DoctorWarn, "pf.conf has no inbound default block on "+strings.Join(open, ", ")+": pf passes what no rule decides, so the routed addresses of the jails (IPv6, IPv4 without NAT) are reachable from the Internet on every port their policies do not deny, and their NAT subnets from whoever routes them to the host. \"closed\" in jailops firewall test, firewall show and firewall exposed assumes that block",
		"add a default block to /etc/pf.conf, such as `block in all` (or `block in on "+strings.Fields(open[0])[0]+" all`, for both address families) after the set and scrub lines, with a pass for what the host itself serves (ssh first), then pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf; see docs/install.md")
}

// pfStatesLimit reads the hard limit of the state table from `pfctl -s memory`; false when it does not say.
func pfStatesLimit(out string) (int, bool) {
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) == 4 && f[0] == "states" && f[1] == "hard" && f[2] == "limit" {
			n, err := strconv.Atoi(f[3])
			return n, err == nil && n > 0
		}
	}
	return 0, false
}

// doctorPFStates looks at the host's pf state table against what the running jails may put in it: each pass rule of a
// jail may create jails.max_states states (or the jail's resources.states), and the table of the host is shared by the
// host itself (its sshd, the API) and every jail. A jail without a limit, or one whose limit is more than half of the
// table, can fill it on its own: that is a warning. That the running jails together may create more than the table
// holds is information: each jail's own limit keeps one from doing it alone, and a host with many jails is expected to
// be in that state. Stopped jails create no state. Unreadable limits say nothing.
func (s *Service) doctorPFStates(ctx context.Context, c *checkSet) {
	const name = "pf.states"
	_, ok, out := s.runOK(ctx, "pfctl", "-s", "memory")
	if !ok {
		return
	}
	limit, ok := pfStatesLimit(out)
	if !ok {
		return
	}
	s.mu.RLock()
	var jails []api.Jail
	for _, e := range s.jails {
		if len(e.jail.Spec.Network) > 0 {
			jails = append(jails, e.jail)
		}
	}
	s.mu.RUnlock()
	var running []api.Jail
	for _, j := range jails {
		if s.jailRunning(ctx, jailKey(j)) {
			running = append(running, j)
		}
	}
	if len(running) == 0 {
		return
	}
	sum, per := 0, map[int]int{}
	var unlimited, big []string
	for _, j := range running {
		n := s.statesLimit(j)
		who := j.Metadata.Project + "/" + j.Metadata.Name
		if n <= 0 {
			unlimited = append(unlimited, who)
			continue
		}
		if n > limit/2 {
			big = append(big, who)
		}
		sum += n
		per[n]++
	}
	slices.Sort(unlimited)
	slices.Sort(big)
	var parts []string
	for _, n := range slices.Sorted(maps.Keys(per)) {
		parts = append(parts, fmt.Sprintf("%d jail(s) x %d", per[n], n))
	}
	raise := fmt.Sprintf("raise the table with `set limit states %d` (or more: each jail has several pass rules, and the host needs its own) in /etc/pf.conf, then pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf; or lower jails.max_states in the daemon's configuration (resources.states per jail)", 2*max(sum, limit))
	switch {
	case len(unlimited) > 0:
		c.add(name, api.DoctorWarn, fmt.Sprintf("%d running jail(s) have no pf state limit (jails.max_states or resources.states is 0): one of them can fill the host's state table of %d states, and new connections then fail host-wide, the host's ssh included: %s", len(unlimited), limit, strings.Join(unlimited, ", ")),
			"give them a limit: jails.max_states in the daemon's configuration, or resources.states of the jail")
	case len(big) > 0:
		c.add(name, api.DoctorWarn, fmt.Sprintf("a running jail may create more than half of the host's state table with one pass rule (states hard limit %d, pfctl -s memory; %s states per rule): one busy jail can fill it, and new connections then fail host-wide, the host's ssh included: %s", limit, strings.Join(parts, " + "), strings.Join(big, ", ")), raise)
	case sum > limit:
		c.add(name, api.DoctorInfo, fmt.Sprintf("the %d running jail(s) may create %d states together with one pass rule each (%s), more than the host's state table holds (states hard limit %d): each jail's own limit keeps one from filling it alone", len(running), sum, strings.Join(parts, " + "), limit), raise)
	default:
		c.add(name, api.DoctorOK, fmt.Sprintf("the host's state table (%d states) holds what the %d running jail(s) with a link may create with one pass rule each (%d)", limit, len(running), sum), "")
	}
}
