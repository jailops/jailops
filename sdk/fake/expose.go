package fake

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// fakeFamilies describes how the fake jail's addresses are reached: IPv6 directly, IPv4 through a forward. only limits it
// to the families named.
func fakeFamilies(j *api.Jail, entries []api.ExposeEntry, only []string) []api.ExposeFamily {
	var ports, fwd []string
	for _, e := range entries {
		for _, p := range e.Ports {
			if !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
			h := p
			if e.HostPort > 0 {
				h = itoa(e.HostPort)
			}
			if pair := h + "->" + p; !slices.Contains(fwd, pair) {
				fwd = append(fwd, pair)
			}
		}
	}
	sortPorts(ports)
	var out []api.ExposeFamily
	for _, n := range j.Spec.Network {
		if n.IPv6 != "" && (len(only) == 0 || slices.Contains(only, "6")) {
			out = append(out, api.ExposeFamily{Family: "6", Address: n.IPv6, Network: n.Network, Via: "uplink", Ports: strings.Join(ports, ", ")})
		}
		if n.IPv4 != "" && (len(only) == 0 || slices.Contains(only, "4")) {
			out = append(out, api.ExposeFamily{Family: "4", Address: n.IPv4, Network: n.Network, Via: "forward", Ports: strings.Join(ports, ", "), Forwards: fwd})
		}
	}
	return out
}

// sortPorts orders ports and ranges by their first port.
func sortPorts(p []string) {
	first := func(s string) int { n, _ := strconv.Atoi(strings.SplitN(s, "-", 2)[0]); return n }
	sort.Slice(p, func(i, j int) bool { return first(p[i]) < first(p[j]) })
}

// exposedOf names the ports and protocols of entries as the daemon does: "80,443/tcp", "53/tcp,udp".
func exposedOf(entries []api.ExposeEntry) string {
	var ports, protos []string
	for _, e := range entries {
		if !slices.Contains(protos, e.Protocol()) {
			protos = append(protos, e.Protocol())
		}
		for _, p := range e.Ports {
			if !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
		}
	}
	sortPorts(ports)
	sort.Strings(protos)
	return strings.Join(ports, ",") + "/" + strings.Join(protos, ",")
}

// handleExpose answers like the daemon: the entry of the same protocol and ports is replaced, an unknown family is refused.
func (s *Server) handleExpose(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.ExposeRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	protos := []string{}
	for _, x := range strings.Split(req.Proto, ",") {
		switch x = strings.TrimSpace(x); {
		case x == "" && req.Proto == "":
			protos = []string{"tcp"}
		case x == "tcp" || x == "udp":
			if !slices.Contains(protos, x) {
				protos = append(protos, x)
			}
		default:
			writeErr(w, api.Errorf(api.CodeInvalid, "proto %q must be tcp, udp or tcp,udp", req.Proto))
			return
		}
	}
	sort.Strings(protos)
	if len(req.Ports) == 0 {
		writeErr(w, api.Errorf(api.CodeInvalid, "name the ports to expose"))
		return
	}
	if req.HostPort > 0 && len(req.Ports) > 1 {
		writeErr(w, api.Errorf(api.CodeInvalid, "--host-port takes one port: jailops expose %s %s --host-port %d, then jailops expose %s %s",
			r.PathValue("name"), req.Ports[0], req.HostPort, r.PathValue("name"), strings.Join(req.Ports[1:], ",")))
		return
	}
	s.mu.Lock()
	_, j := s.jails.find(r, name)
	if j == nil {
		s.mu.Unlock()
		writeErr(w, notFound("jail", name))
		return
	}
	var added []api.ExposeEntry
	for _, p := range protos {
		added = append(added, api.ExposeEntry{Proto: p, Ports: slices.Clone(req.Ports), Families: slices.Clone(req.Families), From: slices.Clone(req.From),
			HostPort: req.HostPort, Listen: req.Listen, Comment: req.Comment})
	}
	next := slices.Clone(j.Spec.Expose)
	changed := false
	for _, a := range added {
		// a port is exposed once per protocol: the new entry takes its ports from the entries that name them
		held := slices.ContainsFunc(next, func(x api.ExposeEntry) bool {
			return x.Protocol() == a.Protocol() && slices.Equal(x.From, a.From) && x.HostPort == a.HostPort && x.Listen == a.Listen && x.Comment == a.Comment &&
				slices.Equal(x.Families, a.Families) && !slices.ContainsFunc(a.Ports, func(p string) bool { return !slices.Contains(x.Ports, p) })
		})
		if held {
			continue
		}
		var kept []api.ExposeEntry
		for _, x := range next {
			if x.Protocol() == a.Protocol() {
				x.Ports = slices.DeleteFunc(slices.Clone(x.Ports), func(p string) bool { return slices.Contains(a.Ports, p) })
				if len(x.Ports) == 0 {
					continue
				}
			}
			kept = append(kept, x)
		}
		next = append(kept, a)
		changed = true
	}
	if changed && !req.DryRun {
		j.Spec.Expose = next
		s.emitIn(s.info(r).project, "jail-exposed", "/api/v1/jails/"+name)
	}
	res := api.ExposeResult{Jail: name, Changed: changed, Entries: next, Families: fakeFamilies(j, added, req.Families), Exposed: exposedOf(added)}
	s.mu.Unlock()
	writeSync(w, 200, res)
}

// handleUnexpose removes the ports named from the exposure; without ports it removes everything of the protocol.
func (s *Server) handleUnexpose(w http.ResponseWriter, r *http.Request) {
	req, e := decodeOptional[api.UnexposeRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	s.mu.Lock()
	_, j := s.jails.find(r, name)
	if j == nil {
		s.mu.Unlock()
		writeErr(w, notFound("jail", name))
		return
	}
	var next, gone []api.ExposeEntry
	for _, x := range j.Spec.Expose {
		if req.Proto != "" && req.Proto != x.Proto {
			next = append(next, x)
			continue
		}
		var rest, hit []string
		for _, p := range x.Ports {
			if len(req.Ports) == 0 || slices.Contains(req.Ports, p) {
				hit = append(hit, p)
			} else {
				rest = append(rest, p)
			}
		}
		if len(hit) > 0 {
			g := x
			g.Ports = hit
			gone = append(gone, g)
		}
		if len(rest) > 0 {
			k := x
			k.Ports = rest
			next = append(next, k)
		}
	}
	if len(gone) > 0 && !req.DryRun {
		j.Spec.Expose = next
		s.emitIn(s.info(r).project, "jail-unexposed", "/api/v1/jails/"+name)
	}
	res := api.ExposeResult{Jail: name, Changed: len(gone) > 0, Entries: next, Families: fakeFamilies(j, gone, req.Families)}
	if len(gone) > 0 {
		res.Exposed = exposedOf(gone)
	} else {
		for _, p := range req.Ports {
			n, err := strconv.Atoi(p)
			for _, x := range j.Spec.Expose {
				for _, item := range x.Ports {
					lo, hi, ok := strings.Cut(item, "-")
					l, _ := strconv.Atoi(lo)
					h, _ := strconv.Atoi(hi)
					if ok && err == nil && l <= n && n <= h && (req.Proto == "" || strings.Contains(req.Proto, x.Protocol())) {
						res.Notes = append(res.Notes, p+" is inside the exposed range "+item+": unexpose the range to remove it")
					}
				}
			}
		}
	}
	if res.Entries == nil {
		res.Entries = []api.ExposeEntry{}
	}
	s.mu.Unlock()
	writeSync(w, 200, res)
}
