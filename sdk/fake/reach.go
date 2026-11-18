package fake

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// fakeReach reads the exposure of a jail (jailops expose) and the policies (the project's, then its own) for the rules that
// let the Internet in: an enabled allow in rule with an external source is an entry over IPv6 (the uplink). The daemon walks
// the rendered anchor; the shape of the answer is the same.
func (s *Server) fakeReach(project string, j *api.Jail) api.JailReach {
	out := api.JailReach{Project: project, Jail: j.Metadata.Name, Rows: []api.ReachRow{}, Rules: []api.ReachRule{}, DefaultIn: "allow", DefaultOut: "allow"}
	var names []string
	if p := s.projects.items[s.projects.keyIn("", project)]; p != nil {
		names = append(names, p.Spec.FirewallPolicies...)
	}
	names = append(names, j.Spec.Firewall.Policies...)
	var projectNames []string
	if p := s.projects.items[s.projects.keyIn("", project)]; p != nil {
		projectNames = p.Spec.FirewallPolicies
	}
	for _, n := range names {
		src := "jail"
		if slices.Contains(projectNames, n) {
			src = "project"
		}
		rp := api.ReachPolicy{Name: n, Source: src}
		if pol := s.policies.items[s.policies.keyIn(project, n)]; pol != nil {
			rp.Rules, rp.DefaultIn, rp.DefaultOut = len(pol.Spec.Rules), pol.Spec.DefaultIn, pol.Spec.DefaultOut
		}
		out.Policies = append(out.Policies, rp)
	}
	out.DefaultIn, out.DefaultInBy = s.fakeDefault(project, names, projectNames, "in")
	out.DefaultOut, out.DefaultOutBy = s.fakeDefault(project, names, projectNames, "out")
	var v4, v6 string
	for _, n := range j.Spec.Network {
		if n.IPv4 != "" {
			v4 = n.IPv4
		}
		if n.IPv6 != "" {
			v6 = n.IPv6
		}
	}
	for _, pn := range names {
		pol := s.policies.items[s.policies.keyIn(project, pn)]
		if pol == nil {
			continue
		}
		for i, r := range pol.Spec.Rules {
			if r.Disabled || r.Direction != "in" || r.Action != "allow" {
				continue
			}
			from := r.From
			if len(from) == 0 {
				from = []string{"any"}
			}
			external := false
			for _, f := range from {
				if f == "any" || !strings.HasPrefix(f, "jail:") && !strings.HasPrefix(f, "tag:") && f != "host" {
					external = true
				}
			}
			if !external {
				continue
			}
			restricted := !(len(from) == 1 && from[0] == "any")
			by := "policy " + pn + " rule " + itoa(i+1)
			ports := strings.Join(r.Ports, ",")
			if r.Redirect != nil {
				if v4 != "" {
					out.Exposed = append(out.Exposed, api.ExposedEntry{Proto: r.Proto, Ports: itoa(r.Redirect.JailPort), Family: "4", Address: v4, HostPort: r.Redirect.HostPort,
						From: from, Restricted: restricted, Via: "forward", By: by})
				}
				continue
			}
			if v6 != "" {
				out.Exposed = append(out.Exposed, api.ExposedEntry{Proto: r.Proto, Ports: ports, Family: "6", Address: v6, From: from, Restricted: restricted, Via: "uplink", By: by})
			}
		}
	}
	for _, e := range j.Spec.Expose {
		from := e.From
		if len(from) == 0 {
			from = []string{"any"}
		}
		restricted := !(len(from) == 1 && from[0] == "any")
		ports := strings.Join(e.Ports, ",")
		for _, fam := range []string{"4", "6"} {
			if len(e.Families) > 0 && !slices.Contains(e.Families, fam) {
				continue
			}
			switch {
			case fam == "6" && v6 != "":
				out.Exposed = append(out.Exposed, api.ExposedEntry{Proto: e.Protocol(), Ports: ports, Family: "6", Address: v6, From: from, Restricted: restricted, Via: "uplink", By: "expose"})
			case fam == "4" && v4 != "":
				host := 0
				if e.HostPort > 0 {
					host = e.HostPort
				} else if len(e.Ports) == 1 {
					host, _ = strconv.Atoi(e.Ports[0])
				}
				out.Exposed = append(out.Exposed, api.ExposedEntry{Proto: e.Protocol(), Ports: ports, Family: "4", Address: v4, HostPort: host, From: from, Restricted: restricted, Via: "forward", By: "expose"})
			}
		}
	}
	sort.SliceStable(out.Exposed, func(a, b int) bool { return out.Exposed[a].Family > out.Exposed[b].Family })
	for _, e := range out.Exposed {
		d := "pass"
		if e.Via == "forward" {
			d = "forward"
		}
		p := e.Ports
		if p == "" {
			p = "all"
		}
		out.Rows = append(out.Rows, api.ReachRow{Source: api.ReachInternet, Family: e.Family, Proto: e.Proto, Ports: p, From: e.From, Restricted: e.Restricted, Decision: d, By: e.By,
			HostPort: e.HostPort})
	}
	if len(out.Exposed) == 0 {
		out.Rows = append(out.Rows, api.ReachRow{Source: api.ReachInternet, Decision: "none", By: "no allow in rule with an external source and no port forward"})
	}
	out.Out = api.ReachOut{Internet: "all", Host: "DNS and ping", Jails: "all", OtherProjects: "none"}
	return out
}

func (s *Server) jailReach(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	var out api.JailReach
	if j != nil {
		out = s.fakeReach(j.Metadata.Project, j)
	}
	s.mu.Unlock()
	if j == nil {
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	}
	writeSync(w, 200, out)
}

func (s *Server) exposed(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	rc := s.info(r)
	out := api.Exposed{}
	for _, j := range s.jails.items {
		if j.Metadata.Project != rc.project {
			continue
		}
		jj := j
		for _, e := range s.fakeReach(rc.project, jj).Exposed {
			out.Entries = append(out.Entries, api.ExposedJail{Project: rc.project, Jail: j.Metadata.Name, ExposedEntry: e})
		}
	}
	s.mu.Unlock()
	writeSync(w, 200, out)
}

// fakeDefault is the default of a jail in one direction from the policies it names: deny when any of them states deny
// (by lists them, "name [project]" for the project's), else allow.
func (s *Server) fakeDefault(project string, names, projectNames []string, dir string) (v string, by []string) {
	v = "allow"
	for _, pn := range names {
		pol := s.policies.items[s.policies.keyIn(project, pn)]
		if pol == nil {
			continue
		}
		d := pol.Spec.DefaultIn
		if dir == "out" {
			d = pol.Spec.DefaultOut
		}
		if d == "deny" {
			v = "deny"
			if containsStr(projectNames, pn) {
				pn += " [project]"
			}
			by = append(by, pn)
		}
	}
	return v, by
}
