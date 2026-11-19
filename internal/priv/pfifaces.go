package priv

import (
	"context"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Rules of an anchor decide for the whole host: a rule that stands on the uplink, or on no interface at all, with the
// verdict quick, passes or blocks traffic of the host itself before the host's own rules look at it. The helper therefore
// loads a rule only when it names an interface JailOps owns, or the uplink of a network in one of three shapes the
// renderer writes (a translation, a forward and the pass that belongs to it), with a destination that is no address of
// the host. A ruleset that stands on anything else is the operator's to allow (AdminPolicy.RawPF).

var (
	reOnIfaces = regexp.MustCompile(`\bon\s+(\{[^}]*\}|\S+)`)
	reTo       = regexp.MustCompile(`\bto\s+(\{[^}]*\}|\S+)`)
	reDnpipe   = regexp.MustCompile(`\bdnpipe\s*(\([0-9, ]+\)|[0-9]+)`)
)

// checkAnchorInterfaces checks the ruleset of an anchor the helper is asked to load.
func (l *Local) checkAnchorInterfaces(ctx context.Context, anchor, rs string) error {
	project, jail, isJail := jailOfAnchor(anchor)
	if l.cfg.Admin.RawPF {
		// admin-raw-pf is what the operator lets a global administrator write: rules on the uplink and on other
		// interfaces. It never lets the anchor of one jail stand on the link of another.
		return l.checkOwnLinks(ctx, rs, isJail, project, jail)
	}
	var uplinks map[string]bool // read when a rule needs it
	for k, ln := range strings.Split(rs, "\n") {
		t := strings.TrimSpace(ln)
		at := "ruleset line " + strconv.Itoa(k+1) // never the text of the line: it may hold what must not reach the audit log
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "table ") {
			continue
		}
		f := strings.Fields(t)
		switch f[0] {
		case "anchor":
			continue // the project anchor's own wildcard
		case "pass", "block", "match":
		default:
			return forbidden("%s: an anchor holds pass, block and match rules and tables only", at)
		}
		m := reOnIfaces.FindStringSubmatch(t)
		if m == nil {
			return forbidden("%s names no interface: every rule of an anchor stands on an interface of a jail (on IFACE)", at)
		}
		for _, d := range reDnpipe.FindAllStringSubmatch(t, -1) {
			for _, n := range strings.FieldsFunc(d[1], func(r rune) bool { return r < '0' || r > '9' }) {
				if v, err := strconv.Atoi(n); err != nil || (v != 0 && !render.IsManagedPipe(v)) { // 0: no pipe for that direction
					return forbidden("%s: dnpipe %s is not a pipe of JailOps's range", at, n)
				}
			}
		}
		names := strings.FieldsFunc(strings.Trim(m[1], "{}"), func(r rune) bool { return r == ',' || r == ' ' })
		for _, n := range names {
			if err := ifconfig.ValidateIfName(n); err != nil {
				return err
			}
			if l.ifOwned(ctx, n) == nil {
				// the anchor of a jail names the links of that jail: a rule on the link of another jail would decide
				// the traffic of that jail (a link that has no readable description yet, one the reconcile loop has not
				// described, is not another jail's)
				if isJail {
					if owner := l.ifJailOwner(ctx, n); owner != "" && owner != project+"/"+jail {
						return forbidden("%s stands on %s, which is the link of another jail: the anchor of a jail names its own links", at, n)
					}
				}
				continue
			}
			if strings.HasPrefix(anchor, AnchorPrefix+"wg.") && l.ifIsTunnel(ctx, n) {
				continue
			}
			if uplinks == nil {
				uplinks = l.networkUplinks()
			}
			// the uplink of a network, or the interface of a WireGuard server of JailOps (the peers are an external source like
			// the Internet): in both the rule must be of a shape the renderer writes for a jail
			if !uplinks[n] && !l.ifIsTunnel(ctx, n) {
				return forbidden("%s stands on %s, which is not an interface of JailOps, the uplink of a network or a tunnel", at, n)
			}
			if err := l.uplinkShape(ctx, at, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// ifIsTunnel says whether name is a WireGuard interface JailOps made (its description carries the marker).
func (l *Local) ifIsTunnel(ctx context.Context, name string) bool {
	rd, err := l.ifReader(name)
	if err != nil {
		return false
	}
	i, err := rd.Show(ctx, name)
	if err != nil || !ifconfig.HasMarker(i.Description) {
		return false
	}
	for _, g := range i.Groups {
		if g == "wg" {
			return true
		}
	}
	return false
}

// uplinkShape accepts a rule on an uplink only in the forms the renderer writes for a network's translation and a
// jail's port forwards.
func (l *Local) uplinkShape(ctx context.Context, at, t string) error {
	switch {
	case strings.HasPrefix(t, "match in on ") && strings.Contains(t, " rdr-to "):
		return l.checkForward(ctx, at, t)
	case strings.HasPrefix(t, "match out on ") && strings.Contains(t, " nat-to "):
		return nil
	case strings.HasPrefix(t, "pass in quick on "), strings.HasPrefix(t, "pass in log quick on "),
		strings.HasPrefix(t, "block drop in quick on "), strings.HasPrefix(t, "block drop in log quick on "):
		// What the renderer writes on the uplink for a jail: the pass of a forward, what the jail's policies admit from
		// outside, and the blocks that go with them (forged sources, a deny rule). All of them are inbound and name the
		// jail's own addresses as their destination (one address or a list), never `any` and never an address of the host.
		m := reTo.FindStringSubmatch(t)
		if m == nil {
			return forbidden("%s: a rule on the uplink names its destination", at)
		}
		dests := strings.FieldsFunc(strings.Trim(m[1], "{}"), func(r rune) bool { return r == ',' || r == ' ' })
		if len(dests) == 0 || strings.HasPrefix(m[1], "<") {
			return forbidden("%s: a rule on the uplink names the addresses of a jail as its destination, not a table", at)
		}
		local, _ := localAddrs()
		for _, d := range dests {
			a, err := netip.ParseAddr(d)
			if err != nil {
				return forbidden("%s: a rule on the uplink names addresses of a jail as its destination", at)
			}
			for _, h := range local {
				if h == a.Unmap() {
					return forbidden("%s: the destination %s is an address of the host", at, a)
				}
			}
			if l.isNetworkHostAddr(a.Unmap()) {
				return forbidden("%s: the destination %s is the address a network gives the host", at, a)
			}
			if err := l.checkStoredNetworkAddr(a.Unmap(), "the destination of a rule on the uplink"); err != nil {
				return forbidden("%s: %v", at, err)
			}
		}
		return nil
	}
	return forbidden("%s: a rule on the uplink must be a translation, a forward or the pass of a forward", at)
}

// networkUplinks are the uplinks of the networks JailOps has stored.
func (l *Local) networkUplinks() map[string]bool {
	out := map[string]bool{}
	dir := filepath.Join(l.cfg.StateDir, "objects", string(api.KindNetwork))
	names, err := l.listState(dir)
	if err != nil {
		return out
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := l.readState(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var d networkDoc
		if json.Unmarshal(b, &d) == nil && d.Spec.Uplink != "" {
			out[d.Spec.Uplink] = true
		}
	}
	return out
}

var reForward = regexp.MustCompile(`\bto\s+(\S+)\s+port\s+([0-9]+)\s+rdr-to\s+(\S+)\s+port\s+([0-9]+)\s*$`)

var reProto = regexp.MustCompile(`\bproto\s+(tcp|udp)\b`)

// checkForward accepts a forward (match in on UPLINK ... to ADDR port N rdr-to JAIL port M) when it leads to a jail and
// leaves the host's own ports alone: the target is an address of a network JailOps has stored, never one of the host,
// and the port it takes over on the host is neither a privileged port nor one a host process listens on, unless the
// request carries the admin assertion (the API asks for a global administrator for that). Without it a compromised
// daemon, or a project administrator, could send the host's ssh or console port into a jail of their own. The start hook
// of a jail has no caller: it carries the forwards of the jail's stored exposure (withStoredForwards), which a global
// administrator admitted when the exposure was stored, and a forward that is one of them needs no assertion again.
func (l *Local) checkForward(ctx context.Context, at, t string) error {
	m := reForward.FindStringSubmatch(t)
	if m == nil {
		return forbidden("%s: a forward names its port and its target as the renderer writes them", at)
	}
	target, err := netip.ParseAddr(strings.Trim(m[3], "{}"))
	if err != nil {
		return forbidden("%s: the target of a forward is one address of a jail", at)
	}
	local, _ := localAddrs()
	for _, h := range local {
		if h == target.Unmap() {
			return forbidden("%s: the target %s is an address of the host", at, target)
		}
	}
	if err := l.checkStoredNetworkAddr(target, "the target of a forward"); err != nil {
		return forbidden("%s: %v", at, err)
	}
	port, _ := strconv.Atoi(m[2])
	if isAdmin(ctx) {
		return nil
	}
	if pm := reProto.FindStringSubmatch(t); pm != nil {
		jailPort, _ := strconv.Atoi(m[4])
		if isStoredForward(ctx, pm[1], port, jailPort) {
			return nil
		}
	}
	if port < 1024 {
		return forbidden("%s: the host's port %d is a privileged port: forwarding it needs the admin assertion", at, port)
	}
	if hostListens(ctx, l, port) {
		return forbidden("%s: a process of the host listens on port %d: forwarding it needs the admin assertion", at, port)
	}
	return nil
}

// hostListens says whether a process of the host listens on the port (sockstat(1), any address, tcp or udp). A lookup
// that fails says no.
func hostListens(ctx context.Context, l *Local, port int) bool {
	res, err := l.d.Runner.Run(ctx, run.Cmd{Name: "sockstat", Args: []string{"-46l"}, Timeout: 10 * time.Second})
	if err != nil {
		return false
	}
	want := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) >= 6 && strings.HasSuffix(f[5], want) {
			return true
		}
	}
	return false
}

// isNetworkHostAddr says whether an address is one the host itself holds for a stored network: the gateway of its IPv4
// subnet or the resolver address of its IPv6 prefix. A rule on the uplink never leads there.
func (l *Local) isNetworkHostAddr(a netip.Addr) bool {
	nets, err := l.storedNetworks()
	if err != nil {
		return false
	}
	for _, n := range nets {
		if n.Spec.IPv4 != nil {
			if g, err := netip.ParseAddr(n.Spec.IPv4.Gateway); err == nil && g == a {
				return true
			}
		}
		if n.Spec.IPv6 != nil {
			if p, err := netip.ParsePrefix(n.Spec.IPv6.Subnet); err == nil && ipam.Resolver6(p) == a {
				return true
			}
		}
	}
	return false
}

// jailOfAnchor splits the anchor of a jail, jailops/PROJECT/JAIL; the anchors of a network, a tunnel and a project
// have one more or one less part.
func jailOfAnchor(anchor string) (project, jail string, ok bool) {
	rest, found := strings.CutPrefix(anchor, AnchorPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ifJailOwner reads the jail an epair was made for from its description ("jailops jail=PROJECT/JAIL net=NAME (host
// side)"): "" when the interface cannot be read or has no such text yet.
func (l *Local) ifJailOwner(ctx context.Context, name string) string {
	rd, err := l.ifReader(name)
	if err != nil {
		return ""
	}
	i, err := rd.Show(ctx, name)
	if err != nil {
		return ""
	}
	_, rest, ok := strings.Cut(i.Description, " jail=")
	if !ok {
		return ""
	}
	owner, _, _ := strings.Cut(rest, " ")
	return owner
}

// checkOwnLinks refuses a rule of the anchor of a jail that stands on the link of another jail.
func (l *Local) checkOwnLinks(ctx context.Context, rs string, isJail bool, project, jail string) error {
	if !isJail {
		return nil
	}
	for k, ln := range strings.Split(rs, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		m := reOnIfaces.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		for _, n := range strings.FieldsFunc(strings.Trim(m[1], "{}"), func(r rune) bool { return r == ',' || r == ' ' }) {
			if ifconfig.ValidateIfName(n) != nil || l.ifOwned(ctx, n) != nil {
				continue
			}
			if owner := l.ifJailOwner(ctx, n); owner != "" && owner != project+"/"+jail {
				return forbidden("ruleset line %d stands on %s, which is the link of another jail: the anchor of a jail names its own links", k+1, n)
			}
		}
	}
	return nil
}
