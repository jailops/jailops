package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Fleet projects (design 8.1): a project that lists hosts is defined on the
// hub and implemented by each host. The hub applies the definition to every
// listed host through the SDK client (same UUID, managed_by hub:<hub uuid>),
// checks first that the host has the networks and storage the project names,
// retries hosts that are offline, and never changes a project it does not
// own. Each host stores and enforces its own copy: there is no consensus.

// FleetInterval is how often the hub retries hosts that did not take the
// definition.
const FleetInterval = 30 * time.Second

// Reasons of the Host:<host> conditions.
const (
	FleetApplied        = "Applied"
	FleetOffline        = "Offline"
	FleetMissingNetwork = "MissingNetwork"
	FleetMissingStorage = "MissingStorage"
	FleetConflict       = "Conflict"
	FleetForbidden      = "Forbidden"
	FleetUnknownHost    = "UnknownHost"
	FleetApplyFailed    = "ApplyFailed"
	fleetPendingKey     = "fleet/pending-delete"
)

type fleetState struct {
	// pass serialises reconcile passes: the timer, a project change and a manual call must not apply the same
	// project at the same time (the second would meet the first one's new generation and fail with a conflict).
	pass  sync.Mutex
	mu    sync.Mutex
	conds map[string]map[string]api.Condition // project -> host -> condition
	kick  chan struct{}
}

func (f *fleetState) init() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conds == nil {
		f.conds = map[string]map[string]api.Condition{}
		f.kick = make(chan struct{}, 1)
	}
}

// FleetKick asks the fleet loop for a pass soon.
func (s *Service) FleetKick() {
	s.fleet.init()
	select {
	case s.fleet.kick <- struct{}{}:
	default:
	}
}

// FleetKicks is the channel the daemon's fleet loop waits on.
func (s *Service) FleetKicks() <-chan struct{} {
	s.fleet.init()
	return s.fleet.kick
}

// FleetConditions returns the hub's per-host conditions of a project.
func (s *Service) FleetConditions(project string) []api.Condition {
	s.fleet.init()
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	hosts := make([]string, 0, len(s.fleet.conds[project]))
	for h := range s.fleet.conds[project] {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	out := make([]api.Condition, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, s.fleet.conds[project][h])
	}
	return out
}

func (s *Service) setFleetCond(project, host string, c api.Condition) {
	s.fleet.init()
	c.Type = "Host:" + host
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.conds[project] == nil {
		s.fleet.conds[project] = map[string]api.Condition{}
	}
	if old, ok := s.fleet.conds[project][host]; ok && old.Status == c.Status && old.Reason == c.Reason && !old.Since.IsZero() {
		c.Since = old.Since
	}
	s.fleet.conds[project][host] = c
}

func (s *Service) fleetCond(ok bool, reason, msg string) api.Condition {
	st := api.ConditionTrue
	if !ok {
		st = api.ConditionFalse
	}
	return api.Condition{Status: st, Reason: reason, Message: msg, Since: s.now().UTC()}
}

// hubTag is the managed_by value of projects this hub applies.
func (s *Service) hubTag() string { return "hub:" + s.d.HostUUID }

// fleetProjects returns the projects this hub defines for hosts.
func (s *Service) fleetProjects() []project {
	objs, err := s.projKind.list()
	if err != nil {
		return nil
	}
	var out []project
	for _, o := range objs {
		if len(o.Spec.Hosts) > 0 && (o.Meta.ManagedBy == "" || o.Meta.ManagedBy == s.hubTag()) {
			out = append(out, o)
		}
	}
	return out
}

// isSelfHost reports whether a name in a project's host list is this daemon.
func (s *Service) isSelfHost(name string) bool { return name == s.d.HostUUID || name == s.d.Hostname }

// FleetReconcile applies every fleet project to its hosts once and retries
// deletions that could not be sent. The daemon calls it on a timer and when a
// project changes; a hub that is not configured to dial remotes does nothing.
func (s *Service) FleetReconcile(ctx context.Context) {
	if !s.d.Hub || s.d.Dial == nil {
		return
	}
	s.fleet.pass.Lock()
	defer s.fleet.pass.Unlock()
	for _, p := range s.fleetProjects() {
		s.fleetApply(ctx, p)
	}
	s.fleetRetryDeletes(ctx)
}

func (s *Service) fleetApply(ctx context.Context, p project) {
	listed := map[string]bool{}
	for _, h := range p.Spec.Hosts {
		listed[h] = true
		s.setFleetCond(p.Meta.Name, h, s.applyToHost(ctx, p, h))
	}
	s.fleet.mu.Lock()
	for h := range s.fleet.conds[p.Meta.Name] {
		if !listed[h] {
			delete(s.fleet.conds[p.Meta.Name], h)
		}
	}
	s.fleet.mu.Unlock()
}

func offlineErr(err error) bool {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code == api.CodeBusy
	}
	return true // dial, TLS and network errors
}

// applyToHost checks the host and brings its copy of the project in line.
func (s *Service) applyToHost(ctx context.Context, p project, host string) api.Condition {
	if s.isSelfHost(host) {
		return s.fleetCond(true, FleetApplied, "this host holds the definition")
	}
	rem, err := s.ResolveRemote(host)
	if err != nil {
		return s.fleetCond(false, FleetUnknownHost, "no remote named "+host+" is registered")
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := s.d.Dial(cctx, rem)
	if err != nil {
		return s.fleetCond(false, FleetOffline, "host "+host+" cannot be reached: "+err.Error())
	}
	defer c.Close()
	// The named networks and storage must exist on the host before anything is applied.
	nets := map[string]bool{}
	for n, err := range c.Networks().List(cctx) {
		if err != nil {
			return s.hostError(host, err, "listing networks")
		}
		nets[n.Metadata.Name] = true
	}
	var missing []string
	for _, g := range p.Spec.Networks {
		if g.Network != "*" && !nets[g.Network] {
			missing = append(missing, g.Network)
		}
	}
	if len(missing) > 0 {
		return s.fleetCond(false, FleetMissingNetwork, "host "+host+" has no network "+joinComma(missing))
	}
	stos := map[string]bool{}
	for st, err := range c.Storage().List(cctx) {
		if err != nil {
			return s.hostError(host, err, "listing storage")
		}
		stos[st.Metadata.Name] = true
	}
	missing = nil
	for _, g := range p.Spec.Storage {
		if g.Storage != "*" && !stos[g.Storage] {
			missing = append(missing, g.Storage)
		}
	}
	if len(missing) > 0 {
		return s.fleetCond(false, FleetMissingStorage, "host "+host+" has no storage "+joinComma(missing))
	}
	spec := p.Spec
	cur, etag, err := c.Projects().GetETag(cctx, p.Meta.Name)
	switch {
	case err == nil:
		if cur.Metadata.ManagedBy != s.hubTag() {
			return s.fleetCond(false, FleetConflict, "host "+host+" has a project "+p.Meta.Name+" that this hub does not manage")
		}
		if jsonEq(cur.Spec, spec) {
			return s.fleetCond(true, FleetApplied, "host "+host+" is in sync")
		}
		in := api.Project{Metadata: api.ObjectMeta{Name: p.Meta.Name, Labels: p.Meta.Labels, ManagedBy: s.hubTag()}, Spec: spec}
		if _, err := c.Projects().Update(cctx, p.Meta.Name, in, etag); err != nil {
			return s.hostError(host, err, "updating the project")
		}
	case api.IsCode(err, api.CodeNotFound):
		in := api.Project{Metadata: api.ObjectMeta{Name: p.Meta.Name, UUID: p.Meta.UUID, Labels: p.Meta.Labels, ManagedBy: s.hubTag()}, Spec: spec}
		if _, err := c.Projects().Create(cctx, in); err != nil {
			return s.hostError(host, err, "creating the project")
		}
	default:
		return s.hostError(host, err, "reading the project")
	}
	return s.fleetCond(true, FleetApplied, "host "+host+" holds the project")
}

func (s *Service) hostError(host string, err error, what string) api.Condition {
	switch {
	case api.IsCode(err, api.CodeForbidden), api.IsCode(err, api.CodeUnauthorized):
		return s.fleetCond(false, FleetForbidden, "host "+host+" refused "+what+": "+err.Error()+" (the hub certificate needs global access there)")
	case offlineErr(err):
		return s.fleetCond(false, FleetOffline, "host "+host+" did not answer while "+what+": "+err.Error())
	}
	return s.fleetCond(false, FleetApplyFailed, "host "+host+" failed "+what+": "+err.Error())
}

func joinComma(l []string) string {
	slices.Sort(l)
	out := ""
	for i, x := range l {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// ---- deletion ----

type pendingDelete struct {
	Project string `json:"project"`
	Host    string `json:"host"`
}

func (s *Service) loadPending(ctx context.Context) []pendingDelete {
	v, err := s.d.DB.Meta.Get(ctx, fleetPendingKey)
	if err != nil {
		return nil
	}
	var l []pendingDelete
	_ = json.Unmarshal([]byte(v), &l)
	return l
}

func (s *Service) savePending(ctx context.Context, l []pendingDelete) {
	b, _ := json.Marshal(l)
	_ = s.d.DB.Meta.Set(ctx, fleetPendingKey, string(b))
}

// fleetForget removes a deleted fleet project from its hosts; hosts that
// cannot be reached are remembered and retried.
func (s *Service) fleetForget(ctx context.Context, p project) {
	if !s.d.Hub || s.d.Dial == nil || len(p.Spec.Hosts) == 0 || (p.Meta.ManagedBy != "" && p.Meta.ManagedBy != s.hubTag()) {
		return
	}
	s.fleet.init()
	s.fleet.mu.Lock()
	delete(s.fleet.conds, p.Meta.Name)
	s.fleet.mu.Unlock()
	pend := s.loadPending(ctx)
	for _, h := range p.Spec.Hosts {
		if s.isSelfHost(h) {
			continue
		}
		if !s.deleteOnHost(ctx, p.Meta.Name, h) {
			pend = append(pend, pendingDelete{Project: p.Meta.Name, Host: h})
		}
	}
	s.savePending(ctx, pend)
}

// deleteOnHost deletes a project this hub applied; it reports false when the
// host should be tried again later.
func (s *Service) deleteOnHost(ctx context.Context, project, host string) bool {
	rem, err := s.ResolveRemote(host)
	if err != nil {
		return true // the remote was removed: nothing left to do
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := s.d.Dial(cctx, rem)
	if err != nil {
		return false
	}
	defer c.Close()
	cur, err := c.Projects().Get(cctx, project)
	if api.IsCode(err, api.CodeNotFound) {
		return true
	}
	if err != nil {
		return !offlineErr(err)
	}
	if cur.Metadata.ManagedBy != s.hubTag() {
		return true // never delete what this hub does not own
	}
	if err := c.Projects().Delete(cctx, project); err != nil {
		return !offlineErr(err)
	}
	return true
}

func (s *Service) fleetRetryDeletes(ctx context.Context) {
	pend := s.loadPending(ctx)
	if len(pend) == 0 {
		return
	}
	var keep []pendingDelete
	for _, pd := range pend {
		if !s.deleteOnHost(ctx, pd.Project, pd.Host) {
			keep = append(keep, pd)
		}
	}
	if len(keep) != len(pend) {
		s.savePending(ctx, keep)
	}
}

var _ = sdk.Version
