package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/dnsprobe"
	"jailops.net/jailops/internal/mounts"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// Names for the jails through the host's resolver, the base system's local-unbound (docs/dns.md). Nothing is configured:
// when local_unbound is enabled and its conf.d is root's, the helper writes the names of the running jails there,
// NAME.PROJECT.internal, together with the lines unbound needs to answer the jails, and the jails ask the gateway with
// a search domain of their project. `unbound-conf =` (empty) in priv.conf turns it off, a path points it at another
// file (a plain unbound with unbound-control). Off or not set up, nothing here does anything.

// unboundState is what the service knows of it: whether it is on, and what the last write found.
type unboundState struct {
	mu      sync.Mutex
	known   bool
	enabled bool
	suffix  string   // the zone suffix of the names, as the helper has it
	forward []string // where the host resolver sends every other name
	recurse bool     // or it resolves them itself
	checked time.Time
	sig     string    // what the names were last written for
	synced  time.Time // and when
	offDone bool      // the helper was asked once to take its own file away while the names are off
}

// unboundRecheck is how long the answer "is it on" is kept, and the longest the names go without a look.
const unboundRecheck = 10 * time.Minute

// unboundCached says whether the names are on, as last found (false until the first look): the render of a jail's
// resolv.conf asks it and must not wait for the helper.
func (s *Service) unboundCached() bool {
	s.unbound.mu.Lock()
	defer s.unbound.mu.Unlock()
	return s.unbound.known && s.unbound.enabled
}

// localUnboundEnabled says whether the host runs the base system's local-unbound (local_unbound_enable in rc.conf):
// the jails ask the gateway only when something answers there.
func (s *Service) localUnboundEnabled() bool {
	for _, f := range []string{"/etc/rc.conf.local", "/etc/rc.conf"} {
		b, err := s.hostFile(f)
		if err != nil {
			continue
		}
		on := false
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if v, ok := strings.CutPrefix(l, "local_unbound_enable="); ok {
				on = strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"'`), "YES")
			}
		}
		if on {
			return true
		}
	}
	return false
}

// namesOn says whether the names are on for what the helper found: it is available, and when it is the file of
// local-unbound that unbound runs.
func (s *Service) namesOn(st mounts.UnboundState) bool {
	return st.Enabled && (!st.Base || s.localUnboundEnabled())
}

// unboundOn asks the helper whether unbound-conf is set (every ten minutes at most). When the answer changes, the
// jails that run get the resolv.conf that goes with it.
func (s *Service) unboundOn(ctx context.Context) bool {
	u := &s.unbound
	u.mu.Lock()
	if u.known && time.Since(u.checked) < unboundRecheck {
		on := u.enabled
		u.mu.Unlock()
		return on
	}
	was, wasKnown := u.enabled, u.known
	u.mu.Unlock()
	st, err := s.d.Files.DNSUnbound(ctx, false)
	if err != nil {
		return was
	}
	u.mu.Lock()
	on := s.namesOn(st)
	u.known, u.enabled, u.checked = true, on, time.Now()
	u.suffix, u.forward, u.recurse = st.Suffix, st.Forward, st.Recurse
	u.mu.Unlock()
	if wasKnown && was != on {
		go s.liveResolvAll(s.baseCtx)
	}
	return on
}

// liveResolvAll writes the resolv.conf of every running jail again (the names went on or off).
func (s *Service) liveResolvAll(ctx context.Context) {
	nets, _ := s.netKind.list()
	for _, o := range nets {
		if up, failed := s.liveResolv(ctx, o.Meta.Name); len(up)+len(failed) > 0 {
			s.log.Info("resolv.conf written again for the names of the jails", "network", o.Meta.Name, "jails", len(up), "failed", len(failed))
		}
	}
}

// SyncDNS writes the names of the running jails for unbound when they changed: it is called after each pass of the
// reconcile loop, and does nothing but compare a short signature of what runs unless something changed (or ten
// minutes passed). A failure is logged and shown by doctor; it never stops a jail from starting.
func (s *Service) SyncDNS(ctx context.Context) {
	if s.d.Files == nil {
		return
	}
	if !s.unboundOn(ctx) {
		s.dnsOffCleanup(ctx)
		return
	}
	sig := s.dnsSignature(ctx)
	u := &s.unbound
	u.mu.Lock()
	if sig == u.sig && time.Since(u.synced) < unboundRecheck {
		u.mu.Unlock()
		return
	}
	u.mu.Unlock()
	st, err := s.d.Files.DNSUnbound(ctx, true)
	switch {
	case err != nil:
		s.log.Warn("the names of the jails could not be written for unbound", "err", err)
		return
	case st.Control != "":
		s.log.Warn("unbound did not take the names of the jails", "file", st.Path, "unbound-control", st.Control)
	case st.Changed:
		s.log.Info("the names of the jails were written for unbound", "file", st.Path, "addresses", st.Names)
	}
	if st.Warn != "" {
		s.log.Warn("the resolver of the host after the names were written", "note", st.Warn)
	}
	u.mu.Lock()
	if st.Control == "" {
		u.sig, u.synced = sig, time.Now()
	}
	u.mu.Unlock()
}

// dnsOffCleanup asks the helper once to take away the file of the names it wrote earlier while the names are off (the
// interface lines of that file stay in effect and keep the loopback from the host's own resolver). It does nothing
// when the helper has no such file.
func (s *Service) dnsOffCleanup(ctx context.Context) {
	u := &s.unbound
	u.mu.Lock()
	if u.offDone || !u.known {
		u.mu.Unlock()
		return
	}
	u.offDone = true
	u.mu.Unlock()
	if st, err := s.d.Files.DNSUnbound(ctx, true); err != nil {
		s.log.Warn("the file of the names of the jails could not be taken away", "err", err)
	} else if st.Control != "" {
		s.log.Warn("the file of the names of the jails could not be taken away", "note", st.Control)
	}
}

// dnsSignature is a short text of what the names depend on: the running jails with their addresses, and the networks
// they use. Two equal signatures mean the file the helper would write is the same.
func (s *Service) dnsSignature(ctx context.Context) string {
	run := s.runningJails(ctx)
	var parts []string
	s.mu.RLock()
	for k, e := range s.jails {
		if _, up := run[k]; !up {
			continue
		}
		for _, a := range e.jail.Spec.Network {
			parts = append(parts, k+" "+a.Network+" "+a.IPv4+" "+a.IPv6)
		}
	}
	s.mu.RUnlock()
	nets, _ := s.netKind.list()
	for _, o := range nets { // every network: the resolver listens on all of them, running jail or not
		line := "net " + o.Meta.Name
		if v4 := o.Spec.IPv4; v4 != nil {
			line += " " + v4.Subnet + " " + v4.Gateway
		}
		if v6 := o.Spec.IPv6; v6 != nil {
			line += " " + v6.Subnet + " " + strconv.FormatBool(v6.NDPProxy)
		}
		parts = append(parts, line)
	}
	slices.Sort(parts)
	return strings.Join(parts, "\n")
}

// doctorUnbound reports the names of the jails: not used (the host resolver is not local-unbound, or is not set up),
// or on: the file is current, the control program answers and a lookup of a running jail's name at the gateway gives
// the jail's address; otherwise a warning with the one thing to do.
func (s *Service) doctorUnbound(ctx context.Context, c *checkSet) {
	if s.d.Files == nil {
		return
	}
	st, err := s.d.Files.DNSUnbound(ctx, false)
	if err != nil {
		c.add("dns.unbound", api.DoctorWarn, "the helper could not say: "+err.Error(), "")
		return
	}
	const notUsed = "the jails have no names: run jailops-setup, or sysrc local_unbound_enable=YES, local-unbound-setup and service local_unbound start"
	switch {
	case st.Path == "":
		c.add("dns.unbound", api.DoctorOK, "off (unbound-conf is empty in priv.conf)", "")
		return
	case st.Base && !s.localUnboundEnabled():
		if st.Reason != "" && !st.Enabled && strings.Contains(st.Reason, "not closed to others") {
			c.add("dns.unbound", api.DoctorWarn, "local-unbound is not enabled and "+st.Reason, "")
			return
		}
		c.add("dns.unbound", api.DoctorWarn, notUsed, "")
		return
	case !st.Enabled:
		c.add("dns.unbound", api.DoctorWarn, "the names are not on: "+st.Reason, "")
		return
	case s.hostResolverDown(ctx, c, st):
		return
	case !st.Current:
		c.add("dns.unbound", api.DoctorWarn, "the names in "+st.Path+" are not those of the running jails; the daemon writes them after its next pass", "wait a minute, or restart jailopsd")
		return
	case st.Control != "":
		hint := "unbound-control must answer: for local-unbound run local-unbound-setup and service local_unbound restart"
		c.add("dns.unbound", api.DoctorWarn, "the names are in "+st.Path+", but the resolver's control program does not answer: "+st.Control, hint)
		return
	}
	probes := s.unboundProbes(ctx, st.Suffix)
	if len(probes) == 0 {
		c.add("dns.unbound", api.DoctorOK, "on: no jail with an address runs yet; suffix "+st.Suffix+"; "+upstreamText(st), "")
		return
	}
	// The views of the resolver answer by the network a question comes from and hold the names of that network's
	// project only: each network is asked for a jail that runs on it, at the addresses the jails of that network ask (the
	// IPv4 gateway and the resolver address of an IPv6 network).
	for _, p := range probes {
		for _, at := range p.addrs {
			got, why := s.lookupAt(ctx, at, p.name)
			if slices.Contains(got, p.want) {
				continue
			}
			msg := "network " + p.network + ": asking " + at + " for the name of its running jail " + p.name + " does not give its address"
			if why != "" {
				msg += ": " + why
			}
			c.add("dns.unbound", api.DoctorWarn, msg, "the resolver has to listen on the gateway and on the resolver address of an IPv6 network: service local_unbound restart (a new network needs one restart; JailOps does it itself when it writes the file); when another resolver than local-unbound owns port 53 there, stop it or give the network nameservers of its own")
			return
		}
	}
	c.add("dns.unbound", api.DoctorOK, fmt.Sprintf("%d names for %d jails in %d projects; suffix %s; %s", st.Names, st.Jails, st.Projects, st.Suffix, upstreamText(st)), "")
}

// unboundProbe is one test lookup of the doctor: a running jail of a network, asked at the addresses of the host that
// the jails of that network use as nameserver.
type unboundProbe struct {
	network, name, want string
	addrs               []string
}

// unboundProbes makes one probe for each network that has a running jail with an address: the jail's name in the zone
// of its project, the address it has on that network, and the addresses of the host on the network (the IPv4 gateway,
// the resolver address of an IPv6 network).
func (s *Service) unboundProbes(ctx context.Context, suffix string) []unboundProbe {
	run := s.runningJails(ctx)
	type pick struct{ name, want string }
	picks := map[string]pick{}
	s.mu.RLock()
	keys := make([]string, 0, len(s.jails))
	for k := range s.jails {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if _, up := run[k]; !up {
			continue
		}
		j := s.jails[k].jail
		for _, a := range j.Spec.Network {
			if _, done := picks[a.Network]; done {
				continue
			}
			for _, raw := range []string{a.IPv4, a.IPv6} {
				if addr, err := netip.ParseAddr(raw); err == nil {
					picks[a.Network] = pick{name: j.Metadata.Name + "." + render.UnboundZone(j.Metadata.Project, suffix), want: addr.Unmap().String()}
					break
				}
			}
		}
	}
	s.mu.RUnlock()
	nets, _ := s.netKind.list()
	var out []unboundProbe
	for _, o := range nets {
		p, ok := picks[o.Meta.Name]
		if !ok {
			continue
		}
		var addrs []string
		for _, a := range gatewayResolvers(o.Spec, true) {
			addrs = append(addrs, a.String())
		}
		if len(addrs) > 0 {
			out = append(out, unboundProbe{network: o.Meta.Name, name: p.name, want: p.want, addrs: addrs})
		}
	}
	slices.SortFunc(out, func(a, b unboundProbe) int { return strings.Compare(a.network, b.network) })
	return out
}

// hostLoopbackNS are the loopback nameservers of the host's resolv.conf, the ones the host asks itself: an unspecified
// address is the loopback, and a file with no nameserver line is the loopback too.
func (s *Service) hostLoopbackNS() []string {
	b, err := s.hostFile(hostResolvPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var out []string
	seen := 0
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		seen++
		a, err := netip.ParseAddr(f[1])
		switch {
		case err != nil:
		case a.IsUnspecified() && a.Is6():
			out = append(out, "::1")
		case a.IsUnspecified():
			out = append(out, "127.0.0.1")
		case a.IsLoopback():
			out = append(out, a.Unmap().String())
		}
	}
	if seen == 0 {
		out = append(out, "127.0.0.1")
	}
	return slices.Compact(out)
}

// hostResolverDown reports, as a failure of dns.unbound, a loopback nameserver of the host that does not answer: the
// host's own lookups (pkg, everything) go through it, and the file of the names can be what took it away (a resolver
// listens on the loopback by itself only while no interface line exists). It asks for `localhost`, which unbound
// answers from its own zone, so that an upstream that is gone does not look like a resolver that is down; a resolver
// that answers but cannot resolve names outside is dns.upstream's warning. It returns true when it reported.
func (s *Service) hostResolverDown(ctx context.Context, c *checkSet, st mounts.UnboundState) bool {
	probe := s.d.ProbeDNS
	if probe == nil {
		return false
	}
	first := ""
	for _, ns := range s.hostLoopbackNS() {
		r := dnsprobe.Wait(ctx, probe, ns, "localhost.", dnsprobe.TypeA, 3, 500*time.Millisecond)
		if r.Up() {
			if first == "" {
				first = ns
			}
			continue
		}
		why := "nothing answers"
		if r.Status == dnsprobe.Refused {
			why = "it refuses the host's own queries"
		}
		c.add("dns.unbound", api.DoctorFail, "the host's own resolver does not answer at "+ns+" ("+why+"): pkg and every lookup of the host fail",
			"look at: sockstat -46l | grep :53 and service local_unbound status; the names of the jails are in "+st.Path+": remove that file and run service local_unbound restart")
		return true
	}
	if first != "" {
		if r := probe(ctx, first, ".", dnsprobe.TypeNS, 2*time.Second); r.Status == dnsprobe.ServFail {
			c.add("dns.upstream", api.DoctorWarn, "the host's resolver at "+first+" answers, but cannot resolve names outside (SERVFAIL)",
				"its upstream does not answer: "+upstreamText(st)+"; check the forward zone of the resolver, or let it resolve from the root servers")
		}
	}
	return false
}

// upstreamText says where the host's unbound sends every name that is not a jail's.
func upstreamText(st mounts.UnboundState) string {
	switch {
	case len(st.Forward) > 0:
		return "unbound forwards everything else to " + strings.Join(st.Forward, ", ")
	case st.Recurse:
		return "unbound resolves everything else from the root servers"
	}
	return "unbound's upstream is not known"
}

// lookupAt asks the resolver at addr (port 53) for the addresses of a name, with a short timeout.
func (s *Service) lookupAt(ctx context.Context, addr, name string) ([]string, string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	dial := s.d.DialJail
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dial(ctx, network, net.JoinHostPort(addr, "53"))
	}}
	got, err := r.LookupHost(ctx, name)
	if err != nil {
		// the Go resolver names the nameserver of resolv.conf in its text, not the address that was asked
		var de *net.DNSError
		if errors.As(err, &de) && de.Err != "" {
			return nil, de.Err
		}
		return nil, err.Error()
	}
	return got, ""
}

// dnsSearch is the search domain of a jail's resolv.conf: its project, when the names are on and the jail asks the
// gateway (a network with nameservers of its own is the administrator's choice and gets none).
func (s *Service) dnsSearch(src api.DNSSource, project string) []string {
	if src == api.DNSFromGateway && s.unboundCached() {
		return []string{render.UnboundZone(project, s.unbound.suffixNow())}
	}
	return nil
}

// suffixNow is the zone suffix of the names, as last reported by the helper.
func (u *unboundState) suffixNow() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.suffix
}

// fillNames puts what the names look like into the status of a network whose jails ask the gateway.
func (s *Service) fillNames(d *api.NetworkDNS) {
	u := &s.unbound
	u.mu.Lock()
	defer u.mu.Unlock()
	d.Suffix, d.Forward, d.Recurse = u.suffix, slices.Clone(u.forward), u.recurse
}
