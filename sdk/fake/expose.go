package fake

import (
	"net/http"
	"slices"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// fakeFamilies describes how the fake jail's addresses are reached: IPv6 directly, IPv4 through a forward. only limits it
// to the families named.
func fakeFamilies(j *api.Jail, entries []api.ExposeEntry, only []string) []api.ExposeFamily {
	var ports []string
	var fwd []string
	for _, e := range entries {
		ports = append(ports, e.Ports...)
		for _, p := range e.Ports {
			h := p
			if e.HostPort > 0 {
				h = itoa(e.HostPort)
			}
			fwd = append(fwd, h+"->"+p)
		}
	}
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

// handleExpose answers like the daemon: the entry of the same protocol and ports is replaced, an unknown family is refused.
func (s *Server) handleExpose(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.ExposeRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	var protos []string
	switch req.Proto {
	case "", "tcp":
		protos = []string{"tcp"}
	case "udp":
		protos = []string{"udp"}
	case "tcp,udp":
		protos = []string{"tcp", "udp"}
	default:
		writeErr(w, api.Errorf(api.CodeInvalid, "proto %q must be tcp, udp or tcp,udp", req.Proto))
		return
	}
	if len(req.Ports) == 0 {
		writeErr(w, api.Errorf(api.CodeInvalid, "name the ports to expose"))
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
		i := slices.IndexFunc(next, func(x api.ExposeEntry) bool { return x.Proto == a.Proto && slices.Equal(x.Ports, a.Ports) })
		switch {
		case i < 0:
			next = append(next, a)
			changed = true
		case !slices.Equal(next[i].From, a.From) || next[i].HostPort != a.HostPort || next[i].Comment != a.Comment || !slices.Equal(next[i].Families, a.Families):
			next[i] = a
			changed = true
		}
	}
	if changed && !req.DryRun {
		j.Spec.Expose = next
		s.emitIn(s.info(r).project, "jail-exposed", "/api/v1/jails/"+name)
	}
	res := api.ExposeResult{Jail: name, Changed: changed, Entries: next, Families: fakeFamilies(j, added, req.Families)}
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
	if res.Entries == nil {
		res.Entries = []api.ExposeEntry{}
	}
	s.mu.Unlock()
	writeSync(w, 200, res)
}
