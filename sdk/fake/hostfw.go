package fake

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// The fake's firewall module and commit confirmed. Off unless WithHostFirewall is given. The diff is the rules in
// plain words, not the pf.conf a real helper renders. A pending change reverts when the fake's clock passes its
// deadline, checked whenever the state is read (there is no timer).

// WithHostFirewall enables the firewall module of the fake host. The console port is 39110.
func WithHostFirewall() Option { return func(s *Server) { s.fwSt.managed = true } }

type fakeFirewall struct {
	managed bool
	applied *api.HostFirewallSpec
	pending *api.HostPendingChange
	prev    *api.HostFirewallSpec // what the pending change replaced
	last    *api.HostPendingOutcome
	seq     int
}

const fakeConsolePort = 39110

func fwNotManaged() *api.Error {
	return api.Errorf(api.CodeNotManaged, "JailOps does not manage the host's firewall").
		WithHint("add `host: {manage: {modules: [firewall]}}` to jailopsd.yaml and `host-manage = firewall` to priv.conf").WithDetail("module", "firewall")
}

// fwTick ends a pending change whose deadline passed (s.mu held).
func (s *Server) fwTick() {
	p := s.fwSt.pending
	if p == nil || s.now().Before(p.Deadline) {
		return
	}
	s.fwSt.applied, s.fwSt.pending = s.fwSt.prev, nil
	s.fwSt.last = &api.HostPendingOutcome{ID: p.ID, Module: p.Module, Snapshot: p.Snapshot, Outcome: api.HostPendingTimedOut, Time: p.Deadline,
		Detail: fmt.Sprintf("nobody confirmed it within %d seconds", p.ConfirmWithin)}
}

func (s *Server) fwPendingView() *api.HostPendingChange {
	p := s.fwSt.pending
	if p == nil {
		return nil
	}
	v := *p
	v.SecondsLeft = max(0, int(p.Deadline.Sub(s.now()).Round(time.Second)/time.Second))
	return &v
}

func (s *Server) handleHostFirewall(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.fwTick()
	h := s.fwSt
	spec := api.DefaultHostFirewall(fakeConsolePort)
	if h.applied != nil {
		spec = *h.applied
	}
	st := api.HostFirewallStatus{Managed: h.managed, ConfigEnabled: h.managed, HelperEnabled: h.managed, FileExists: true,
		FileManaged: h.applied != nil, Applied: h.applied, Rules: spec.Rules(), Pending: s.fwPendingView(), ConsolePort: fakeConsolePort}
	on := true
	st.PFEnabled = &on
	if h.managed || h.applied != nil {
		st.Warnings = spec.Warnings()
	}
	s.mu.Unlock()
	writeSync(w, 200, api.HostFirewall{Kind: api.KindHostFirewall, Spec: spec, Status: st})
}

func fwRulesText(spec *api.HostFirewallSpec) []string {
	if spec == nil {
		return []string{"(the administrator's own pf.conf)"}
	}
	var out []string
	for _, r := range spec.Rules() {
		line := strings.Join(strings.Fields(fmt.Sprintf("%s %s %s %s from %s", r.Action, r.Direction, r.Proto, r.Ports, r.From)), " ")
		if r.Comment != "" {
			line += "  # " + r.Comment
		}
		out = append(out, line)
	}
	return out
}

func (s *Server) handleHostFirewallPut(w http.ResponseWriter, r *http.Request) {
	in, e := decode[api.HostFirewallApply](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	spec := in.Spec
	if spec.Console == nil {
		spec.Console = &api.HostFirewallConsole{}
	}
	spec.Console.Port = fakeConsolePort
	if err := spec.Validate(); err != nil {
		writeErr(w, err.(*api.Error))
		return
	}
	within, err := api.ConfirmWindow(in.ConfirmWithin)
	if err != nil {
		writeErr(w, err.(*api.Error))
		return
	}
	spec = spec.Canonical()
	s.mu.Lock()
	rp := func() reply {
		s.fwTick()
		h := &s.fwSt
		if !h.managed {
			return reply{err: fwNotManaged()}
		}
		if h.pending != nil && !in.DryRun {
			return reply{err: api.Errorf(api.CodeBusy, "a firewall change (id %s) waits for its confirmation", h.pending.ID).WithDetail("pending", h.pending.ID).
				WithHint("confirm it with `jailops host confirm`, or undo it with `jailops host revert`, then try again")}
		}
		before, after := fwRulesText(h.applied), fwRulesText(&spec)
		var d []string
		for _, l := range before {
			d = append(d, "-"+l)
		}
		for _, l := range after {
			d = append(d, "+"+l)
		}
		changed := strings.Join(before, "\n") != strings.Join(after, "\n")
		out := api.HostChange{DryRun: in.DryRun, Changed: changed, Actions: []string{}, Rules: spec.Rules(), Warnings: spec.Warnings()}
		if !changed {
			return reply{status: 200, meta: out}
		}
		out.Diff = "--- /etc/pf.conf\n+++ /etc/pf.conf\n" + strings.Join(d, "\n") + "\n"
		out.Actions = []string{"check the rendered file with pfctl -n", "write /etc/pf.conf", "pfctl -f /etc/pf.conf",
			fmt.Sprintf("revert in %d seconds unless confirmed", within)}
		if in.DryRun {
			return reply{status: 200, meta: out}
		}
		h.seq++
		now := s.now().UTC()
		out.Snapshot = fmt.Sprintf("%s-%d", now.Format("20060102T150405Z"), 900+h.seq)
		h.prev, h.applied = h.applied, &spec
		h.pending = &api.HostPendingChange{ID: fmt.Sprintf("%016x", 0xf00d0000+h.seq), Module: api.HostModuleFirewall, Snapshot: out.Snapshot,
			Created: now, Deadline: now.Add(time.Duration(within) * time.Second), ConfirmWithin: within}
		out.Pending = s.fwPendingView()
		return reply{status: 200, meta: out}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) handleHostPending(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.fwTick()
	out := api.HostPending{Pending: s.fwPendingView(), Last: s.fwSt.last}
	s.mu.Unlock()
	writeSync(w, 200, out)
}

func (s *Server) handleHostPendingAction(confirm bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		in, e := decodeOptional[api.HostPendingAction](r)
		if e != nil {
			writeErr(w, e)
			return
		}
		s.mu.Lock()
		rp := func() reply {
			s.fwTick()
			h := &s.fwSt
			if h.pending == nil {
				return reply{err: api.Errorf(api.CodeNotFound, "no host change waits for a confirmation")}
			}
			if in.ID != "" && in.ID != h.pending.ID {
				return reply{err: api.Errorf(api.CodeConflict, "the pending change is %s, not %s", h.pending.ID, in.ID)}
			}
			p := h.pending
			o := &api.HostPendingOutcome{ID: p.ID, Module: p.Module, Snapshot: p.Snapshot, Outcome: api.HostPendingConfirmed, Time: s.now().UTC()}
			if !confirm {
				o.Outcome, o.Detail, h.applied = api.HostPendingReverted, "reverted on request", h.prev
			}
			h.pending, h.last = nil, o
			return reply{status: 200, meta: api.HostPending{Last: o}}
		}()
		s.mu.Unlock()
		s.respond(w, rp)
	}
}
