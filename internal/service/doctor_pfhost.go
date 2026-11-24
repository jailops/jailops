package service

import (
	"context"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// exposedPort is one port a jail is exposed on, as the uplink sees it: the host port of an IPv4 forward, the jail's own
// port on a routed address.
type exposedPort struct {
	jail, proto string
	lo, hi      int
}

func (e exposedPort) String() string {
	if e.lo == e.hi {
		return fmt.Sprintf("%s %s/%d", e.jail, e.proto, e.lo)
	}
	return fmt.Sprintf("%s %s/%d-%d", e.jail, e.proto, e.lo, e.hi)
}

// exposedByUplink lists the ports of `jailops expose` by the uplink they come in on.
func (s *Service) exposedByUplink() map[string][]exposedPort {
	s.mu.RLock()
	var jails []api.Jail
	for _, k := range sortedKeys(s.jails) {
		jails = append(jails, s.jails[k].jail)
	}
	s.mu.RUnlock()
	out := map[string][]exposedPort{}
	for _, j := range jails {
		eff := s.effective(j)
		if len(eff.Expose) == 0 {
			continue
		}
		for _, r := range s.exposeRoutes(eff) {
			if r.Uplink == "" || !r.NAT && !r.Routed {
				continue
			}
			for _, e := range eff.Expose {
				if !exposesFamily(e, r.Family) {
					continue
				}
				for _, p := range e.Ports {
					lo, hi, ok := portRange(p)
					if !ok {
						continue
					}
					if r.NAT && e.HostPort != 0 {
						lo, hi = e.HostPort, e.HostPort
					}
					ep := exposedPort{jail: j.Metadata.Project + "/" + j.Metadata.Name, proto: e.Protocol(), lo: lo, hi: hi}
					if !slices.Contains(out[r.Uplink], ep) {
						out[r.Uplink] = append(out[r.Uplink], ep)
					}
				}
			}
		}
	}
	return out
}

// portRange reads "80" or "8000-8100".
func portRange(p string) (int, int, bool) {
	a, b, isRange := strings.Cut(p, "-")
	lo, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false
	}
	if !isRange {
		return lo, lo, true
	}
	hi, err := strconv.Atoi(b)
	return lo, hi, err == nil
}

// legacyRdr is one translation rule of the host's own ruleset (`pfctl -s nat`): an `rdr` or `binat` rule, which pf
// evaluates in its translation step, before the filter rules where the `match ... rdr-to` of `jailops expose` stand.
type legacyRdr struct {
	text, iface, proto string
	// ports are the destination ports the rule catches; nil means every port (or ports this reader does not follow).
	ports [][2]int
}

// parseLegacyRdr reads one line of `pfctl -s nat`; false for anything but an rdr or binat rule (nat, the anchors,
// a `no rdr`).
func parseLegacyRdr(ln string) (legacyRdr, bool) {
	ln = strings.TrimSpace(ln)
	f := strings.Fields(ln)
	if len(f) < 2 || f[0] != "rdr" && f[0] != "binat" {
		return legacyRdr{}, false
	}
	r := legacyRdr{text: ln}
	if i := slices.Index(f, "->"); i >= 0 {
		f = f[:i]
	}
	afterTo := false
	for i := 1; i < len(f); i++ {
		switch f[i] {
		case "on":
			if i+1 < len(f) {
				i++
				r.iface = f[i]
			}
		case "proto":
			if i+1 < len(f) {
				i++
				r.proto = f[i]
			}
		case "to":
			afterTo = true
		case "port":
			if !afterTo || i+1 >= len(f) {
				continue
			}
			i++
			op := f[i]
			if (op == "=" || op == "<=" || op == ">=" || op == "<" || op == ">" || op == "!=") && i+1 < len(f) {
				i++
				if op != "=" {
					return r, true // a comparison this reader does not follow: every port, to be safe
				}
			}
			if lo, hi, ok := pfPort(f[i], r.proto); ok {
				r.ports = append(r.ports, [2]int{lo, hi})
			} else {
				r.ports = nil
				return r, true
			}
		}
	}
	return r, true
}

// pfPort reads a port as pfctl prints it: a number, a range "a:b", or a service name of /etc/services.
func pfPort(s, proto string) (int, int, bool) {
	if a, b, ok := strings.Cut(s, ":"); ok {
		lo, e1 := strconv.Atoi(a)
		hi, e2 := strconv.Atoi(b)
		return lo, hi, e1 == nil && e2 == nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, n, true
	}
	if proto != "udp" {
		proto = "tcp"
	}
	if n, err := net.LookupPort(proto, s); err == nil {
		return n, n, true
	}
	return 0, 0, false
}

// catches says whether the rule takes the traffic of an exposed port on uplink up.
func (r legacyRdr) catches(up string, e exposedPort) bool {
	if r.iface != "" && r.iface != up && r.iface != "egress" {
		if !strings.HasPrefix(r.iface, "!") || strings.TrimPrefix(r.iface, "!") == up {
			return false
		}
	}
	if r.proto != "" && r.proto != e.proto {
		return false
	}
	if r.ports == nil {
		return true
	}
	for _, p := range r.ports {
		if p[0] <= e.hi && e.lo <= p[1] {
			return true
		}
	}
	return false
}

// doctorLegacyRdr warns when the host's own pf.conf redirects traffic that `jailops expose` forwards: an rdr or binat
// rule of the main ruleset is evaluated in pf's translation step, before the filter rules where the forwards of the
// exposures stand, so it wins and the exposure (which `jailops expose` lists) never takes effect for those ports.
func (s *Service) doctorLegacyRdr(ctx context.Context, c *checkSet) {
	const name = "pf.legacy_rdr"
	byUplink := s.exposedByUplink()
	if len(byUplink) == 0 {
		return
	}
	ok, _, out := s.runOK(ctx, "pfctl", "-s", "nat")
	if !ok {
		return
	}
	var hits []string
	for _, ln := range strings.Split(out, "\n") {
		r, ok := parseLegacyRdr(ln)
		if !ok {
			continue
		}
		var caught []string
		for _, up := range slices.Sorted(maps.Keys(byUplink)) {
			for _, e := range byUplink[up] {
				if r.catches(up, e) {
					caught = append(caught, e.String()+" on "+up)
				}
			}
		}
		if len(caught) > 0 {
			hits = append(hits, fmt.Sprintf("`%s` takes %s", r.text, strings.Join(caught, ", ")))
		}
	}
	if len(hits) == 0 {
		c.add(name, api.DoctorOK, "no rdr or binat rule of pf.conf takes the ports of an exposed jail", "")
		return
	}
	c.add(name, api.DoctorWarn, "an rdr or binat rule of pf.conf is evaluated before the forwards of jailops expose and wins, so these exposures do not take effect: "+strings.Join(hits, "; "),
		"remove the rule from /etc/pf.conf (jailops expose makes the forward), or expose the jail on another port; then pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf")
}

// defaultRouteIfaces reads the interfaces the host's default routes leave through (`netstat -rnW`, both families).
func defaultRouteIfaces(text string) []string {
	var out []string
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 || f[0] != "default" {
			continue
		}
		iface := f[len(f)-1]
		if len(f) >= 6 {
			iface = f[5]
		}
		if !slices.Contains(out, iface) {
			out = append(out, iface)
		}
	}
	slices.Sort(out)
	return out
}

// doctorURPF warns when pf.conf drops packets that fail the unicast reverse-path check (`from urpf-failed`) on an uplink
// of the jails while a default route leaves through another interface. uRPF passes a packet only when the route back to
// its source goes out where it came in: on a host with more than one uplink, a client of a jail on the uplink that is
// not the default route's fails it, and an exposure or an allow in rule there silently does nothing.
func (s *Service) doctorURPF(ctx context.Context, c *checkSet, rules string) {
	const name = "pf.urpf"
	uplinks := s.jailUplinks()
	if len(uplinks) == 0 {
		return
	}
	var urpf []pfRule
	for _, ln := range strings.Split(rules, "\n") {
		if !strings.Contains(ln, "urpf-failed") {
			continue
		}
		if r, ok := parsePFRule(ln); ok && r.action == "block" && r.dir != "out" {
			urpf = append(urpf, r)
		}
	}
	var covered []string
	for _, up := range uplinks {
		if slices.ContainsFunc(urpf, func(r pfRule) bool { return onUplink(r, up) }) {
			covered = append(covered, up)
		}
	}
	if len(covered) == 0 {
		return
	}
	var defaults []string
	if s.d.HostExec != nil {
		if out, err := s.d.HostExec(ctx, "netstat", "-rnW"); err == nil {
			defaults = defaultRouteIfaces(string(out))
		}
	}
	if defaults == nil {
		c.add(name, api.DoctorInfo, "pf.conf drops packets that fail uRPF (urpf-failed) on "+strings.Join(covered, ", ")+"; the default routes could not be read to tell whether a reply would leave through another interface",
			"on a host with more than one uplink, uRPF drops every client that the default route does not lead back to: drop the urpf-failed rule for an uplink that is not the default route's")
		return
	}
	var asym []string
	for _, up := range covered {
		for _, d := range defaults {
			if d != up {
				asym = append(asym, fmt.Sprintf("%s (a default route leaves through %s)", up, d))
				break
			}
		}
	}
	if len(asym) == 0 {
		c.add(name, api.DoctorOK, "pf.conf uses uRPF (urpf-failed) on "+strings.Join(covered, ", ")+", which carries the default route", "")
		return
	}
	c.add(name, api.DoctorWarn, "pf.conf drops packets that fail uRPF (urpf-failed) on "+strings.Join(asym, ", ")+": a client on the Internet comes in there and its route back leaves elsewhere, so it fails the check, and the exposures and allow in rules of the jails there silently do nothing",
		"limit the urpf-failed rule to the interface of the default route (block in quick on IF from urpf-failed), then pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf")
}
