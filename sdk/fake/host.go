package fake

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// The fake's host management: the identity module of an imaginary host. It is not managed unless WithHostManaged
// is given; then an apply keeps the identity before it as a snapshot that a restore puts back. The diff is a short
// summary of the changed values, not the files a real host renders.

// WithHostManaged enables the identity module of the fake host (jailopsd.yaml and priv.conf both list it).
func WithHostManaged() Option { return func(s *Server) { s.hostSt.managed = true } }

type fakeSnap struct {
	meta api.HostSnapshot
	spec api.HostIdentitySpec
}

type fakeHost struct {
	managed bool
	cur     *api.HostIdentitySpec
	applied *api.HostIdentitySpec
	snaps   []fakeSnap // newest first
	seq     int
}

func (s *Server) hostCur() api.HostIdentitySpec {
	if s.hostSt.cur == nil {
		s.hostSt.cur = &api.HostIdentitySpec{Hostname: s.hostname,
			Resolvers: &api.HostResolvers{Nameservers: []string{"198.51.100.1"}, Search: []string{"example.org"}},
			NTP:       &api.HostNTP{Servers: []string{}, Enable: true}}
	}
	return *s.hostSt.cur
}

func (s *Server) handleHostIdentity(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cur := s.hostCur()
	h := s.hostSt
	st := api.HostIdentityStatus{Managed: h.managed, ConfigEnabled: h.managed, HelperEnabled: h.managed, Applied: h.applied,
		RCHostname: cur.Hostname, ResolvConfManaged: h.applied != nil && h.applied.Resolvers != nil, NTPBlock: len(cur.NTP.Servers) > 0,
		NTPSync: &api.HostNTPSync{Synchronised: true, Peer: "192.0.2.123"}}
	s.mu.Unlock()
	writeSync(w, 200, api.HostIdentity{Kind: api.KindHostIdentity, Spec: cur, Status: st})
}

func notManagedErr() *api.Error {
	return api.Errorf(api.CodeNotManaged, "JailOps does not manage the host's identity").
		WithHint("add `host: {manage: {modules: [identity]}}` to jailopsd.yaml and `host-manage = identity` to priv.conf").WithDetail("module", "identity")
}

// hostDiff summarises what changes between two identities, one -/+ pair per value.
func hostDiff(a, b api.HostIdentitySpec) (string, []string) {
	var d, act []string
	add := func(what, x, y string) {
		if x == "" {
			x = "(none)"
		}
		if y == "" {
			y = "(none)"
		}
		if x != y {
			d = append(d, "-"+what+" "+x, "+"+what+" "+y)
			act = append(act, fmt.Sprintf("set %s to %s", what, y))
		}
	}
	add("hostname", a.Hostname, b.Hostname)
	add("nameservers", strings.Join(a.Resolvers.Nameservers, " "), strings.Join(b.Resolvers.Nameservers, " "))
	add("search", strings.Join(a.Resolvers.Search, " "), strings.Join(b.Resolvers.Search, " "))
	add("ntp servers", strings.Join(a.NTP.Servers, " "), strings.Join(b.NTP.Servers, " "))
	add("ntpd_enable", fmt.Sprint(a.NTP.Enable), fmt.Sprint(b.NTP.Enable))
	if len(d) == 0 {
		return "", nil
	}
	return "--- host\n+++ host\n" + strings.Join(d, "\n") + "\n", act
}

func (s *Server) hostSnapshot(reason string, spec api.HostIdentitySpec) string {
	s.hostSt.seq++
	t := s.now().UTC()
	name := fmt.Sprintf("%s-%d", t.Format("20060102T150405Z"), s.hostSt.seq)
	s.hostSt.snaps = append([]fakeSnap{{meta: api.HostSnapshot{Name: name, Module: api.HostModuleIdentity, Time: t, Reason: reason,
		Files: []string{"/etc/resolv.conf", "/etc/ntp.conf", "/etc/rc.conf"}}, spec: spec}}, s.hostSt.snaps...)
	return name
}

func (s *Server) handleHostIdentityPut(w http.ResponseWriter, r *http.Request) {
	in, e := decode[api.HostIdentityApply](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	if err := in.Spec.Validate(); err != nil {
		writeErr(w, err.(*api.Error))
		return
	}
	s.mu.Lock()
	rp := func() reply {
		if !s.hostSt.managed {
			return reply{err: notManagedErr()}
		}
		spec := in.Spec.Canonical()
		cur := s.hostCur()
		next := cur
		if spec.Hostname != "" {
			next.Hostname = spec.Hostname
		}
		if spec.Resolvers != nil {
			next.Resolvers = spec.Resolvers
		}
		if spec.NTP != nil {
			next.NTP = spec.NTP
		}
		diff, act := hostDiff(cur, next)
		out := api.HostChange{DryRun: in.DryRun, Changed: diff != "", Diff: diff, Actions: append([]string{}, act...)}
		if in.DryRun || diff == "" {
			return reply{status: 200, meta: out}
		}
		out.Snapshot = s.hostSnapshot("apply", cur)
		s.hostSt.cur, s.hostSt.applied = &next, &next
		return reply{status: 200, meta: out}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) handleHostSnapshots(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := []api.HostSnapshot{}
	for _, sn := range s.hostSt.snaps {
		out = append(out, sn.meta)
	}
	s.mu.Unlock()
	writeSync(w, 200, out)
}

func (s *Server) handleHostRestore(w http.ResponseWriter, r *http.Request) {
	in, e := decodeOptional[api.HostRestore](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	if err := api.ValidHostSnapshotName(name); err != nil {
		writeErr(w, err.(*api.Error))
		return
	}
	s.mu.Lock()
	rp := func() reply {
		if !s.hostSt.managed {
			return reply{err: notManagedErr()}
		}
		i := slices.IndexFunc(s.hostSt.snaps, func(sn fakeSnap) bool { return sn.meta.Name == name })
		if name == api.HostSnapshotLatest && len(s.hostSt.snaps) > 0 {
			i = 0
		}
		if i < 0 {
			return reply{err: api.Errorf(api.CodeNotFound, "host snapshot %q not found", name)}
		}
		cur, back := s.hostCur(), s.hostSt.snaps[i].spec
		diff, act := hostDiff(cur, back)
		out := api.HostChange{DryRun: in.DryRun, Changed: diff != "", Diff: diff, Actions: append([]string{}, act...)}
		if in.DryRun || diff == "" {
			return reply{status: 200, meta: out}
		}
		out.Snapshot = s.hostSnapshot("restore", cur)
		s.hostSt.cur = &back
		return reply{status: 200, meta: out}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}
