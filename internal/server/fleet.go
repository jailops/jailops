package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk/api"
)

// aggregateProject answers GET /api/v1/projects/<name>?host=all on a hub: the
// definition, and for each listed host what that host reports. The hub reads
// each host with the caller's signed requestor claim, so every host applies
// its own project scoping to what the caller may see.
func (s *Server) aggregateProject(c *rc) error {
	if !s.svc.Hub() {
		return api.Errorf(api.CodeUnsupported, "this daemon is not a hub").WithHint("host=all works only on a hub")
	}
	ctx := c.r.Context()
	name := c.pathArg("name")
	// The definition goes through the service: a project the caller may not use is not_found.
	def, err := s.svc.GetProject(withActor(c), name)
	if err != nil {
		return err
	}
	agg := api.ProjectAggregate{Project: def, Hosts: []api.ProjectHostStatus{}}
	conds := map[string]api.Condition{}
	for _, cd := range def.Status.Conditions {
		conds[cd.Type] = cd
	}
	for _, h := range def.Spec.Hosts {
		hs := api.ProjectHostStatus{Host: h}
		if cd, ok := conds["Host:"+h]; ok {
			hs.Conditions = []api.Condition{cd}
		}
		if h == s.svc.HostUUID() || h == s.svc.Info("").Hostname {
			st := def.Status
			st.Conditions = nil
			hs.HostUUID, hs.Online, hs.Applied, hs.InSync, hs.Generation, hs.Status = s.svc.HostUUID(), true, true, true, def.Metadata.Generation, &st
		} else {
			s.readHostProject(c, name, def, &hs)
		}
		if hs.Status != nil {
			agg.Total.Jails += hs.Status.Jails
			agg.Total.CPU += hs.Status.CPU
			agg.Total.MemoryUsed += hs.Status.MemoryUsed
			agg.Total.DiskUsed += hs.Status.DiskUsed
			agg.Total.Addresses += hs.Status.Addresses
		}
		agg.Hosts = append(agg.Hosts, hs)
	}
	c.sync(200, agg, def.Metadata.Generation)
	_ = ctx
	return nil
}

// readHostProject fetches one host's copy of the project.
func (s *Server) readHostProject(c *rc, name string, def api.Project, hs *api.ProjectHostStatus) {
	rem, err := s.svc.ResolveRemote(hs.Host)
	if err != nil {
		hs.Error = asAPIError(err)
		return
	}
	hs.HostUUID = rem.Spec.HostUUID
	ctx, cancel := contextTimeout(c, 15*time.Second)
	defer cancel()
	tr, err := s.transportFor(ctx, rem)
	if err != nil {
		hs.Error = asAPIError(err)
		return
	}
	addr, err := remoteHostPort(rem.Spec.Address)
	if err != nil {
		hs.Error = asAPIError(err)
		return
	}
	path := "/api/v1/projects/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+path, nil)
	if err != nil {
		hs.Error = asAPIError(err)
		return
	}
	claim, err := s.signClaim(&rc{r: req.Clone(ctx), actor: c.actor}, rem)
	if err != nil {
		hs.Error = asAPIError(err)
		return
	}
	req.Header.Set(auth.RequestorHeader, claim)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		s.dropTransport(rem)
		hs.Error = api.Errorf(api.CodeBusy, "host %s did not answer: %v", hs.Host, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	hs.Online = true
	var env api.Response
	if err := json.Unmarshal(body, &env); err != nil {
		hs.Error = api.Errorf(api.CodeInternal, "host %s sent a reply that is not an API envelope", hs.Host)
		return
	}
	if env.Type == api.ResponseError || env.Error != nil {
		if env.Error != nil {
			hs.Error = env.Error
		}
		return // not applied (not_found) or not allowed
	}
	var p api.Project
	if err := json.Unmarshal(env.Metadata, &p); err != nil {
		hs.Error = api.Errorf(api.CodeInternal, "host %s sent an unreadable project", hs.Host)
		return
	}
	hs.Applied, hs.Generation = true, p.Metadata.Generation
	hs.InSync = jsonSame(p.Spec, def.Spec)
	st := p.Status
	st.Conditions = nil
	hs.Status = &st
}

func jsonSame(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
