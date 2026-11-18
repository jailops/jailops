package service

import (
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// A forward is a host port that an exposed IPv4 port of a jail sends to the jail (jailops expose, on a network with NAT). Two
// jails that forward the same port leave the second one dead without a word (pf takes the first anchor that matches), so
// the second is refused when it is made, naming the first.
type forward struct {
	key    string // proto/listen/port, as forwardKey writes it
	sig    string // the whole forward: key, jail port and sources; a change of any of them is a change of the forward
	jail   string // the qualified key of the jail that has it
	source string // "expose tcp 80"
}

// forwardKey is the key of a forward: protocol, listen address (empty is the uplink's) and host port.
func forwardKey(proto, listen string, port int) string {
	return proto + "/" + listen + "/" + strconv.Itoa(port)
}

// forwardsOf lists the forwards a jail with the effective spec eff has: one for each single port of an exposure that
// reaches an IPv4 address behind NAT. A range of ports, an IPv6 address and an IPv4 address the upstream routes are
// reached without a forward.
func (s *Service) forwardsOf(jailKey string, eff api.JailSpec) []forward {
	if !natIPv4(s.exposeRoutes(eff)) {
		return nil
	}
	var out []forward
	for _, e := range eff.Expose {
		if !exposesFamily(e, "4") {
			continue
		}
		for _, port := range e.Ports {
			if strings.Contains(port, "-") {
				continue
			}
			n, err := strconv.Atoi(port)
			if err != nil {
				continue
			}
			host := n
			if e.HostPort > 0 {
				host = e.HostPort
			}
			k := forwardKey(e.Protocol(), e.Listen, host)
			out = append(out, forward{key: k, sig: k + "->" + port + "<" + strings.Join(e.From, ","), jail: jailKey, source: "expose " + e.Protocol() + " " + port})
		}
	}
	return out
}

// forwardsChange says whether the forwards of a jail differ between two effective specs: one added, removed or changed
// in its jail port or its sources, not only in its host port. Giving or taking a port of the host is a global administrator's.
func (s *Service) forwardsChange(jailKey string, was, now api.JailSpec) bool {
	a, b := map[string]bool{}, map[string]bool{}
	for _, f := range s.forwardsOf(jailKey, was) {
		a[f.sig] = true
	}
	for _, f := range s.forwardsOf(jailKey, now) {
		b[f.sig] = true
	}
	if len(a) != len(b) {
		return true
	}
	for k := range a {
		if !b[k] {
			return true
		}
	}
	return false
}

// clashOf returns the first forward of mine that another jail's forward takes, as an error that names both.
func clashOf(mine, others []forward) error {
	for _, m := range mine {
		for _, o := range others {
			if o.jail == m.jail || !portClash(m.key, o.key) {
				continue
			}
			_, name := validate.SplitQualified(o.jail)
			_, me := validate.SplitQualified(m.jail)
			return api.Errorf(api.CodeAlreadyExists, "host port %s is already forwarded to jail %q (%s): the forward of jail %q (%s) would be dead",
				hostPortOf(m.key), name, o.source, me, m.source).
				WithHint("jailops unexpose "+name+" "+strings.TrimPrefix(o.source[strings.LastIndex(o.source, " ")+1:], " ")+" --family 4, or give the other jail another port: --host-port N").
				WithDetail("jail", name).WithDetail("forward", o.source)
		}
	}
	return nil
}

// hostPortOf prints the key proto/listen/port as "tcp 8080" or "tcp 192.0.2.1:8080".
func hostPortOf(key string) string {
	first, last := strings.Index(key, "/"), strings.LastIndex(key, "/")
	if first < 0 || last <= first {
		return key
	}
	proto, host, port := key[:first], key[first+1:last], key[last+1:]
	if host == "" {
		return proto + " " + port
	}
	return proto + " " + host + ":" + port
}

// checkForwardClash refuses a jail whose forwards another jail already has.
func (s *Service) checkForwardClash(jailKey string, eff api.JailSpec) error {
	mine := s.forwardsOf(jailKey, eff)
	if len(mine) == 0 {
		return nil
	}
	return clashOf(mine, s.otherForwards(jailKey))
}

// otherForwards are the forwards of every jail but self, every project included (the uplink is one).
func (s *Service) otherForwards(self string) []forward {
	s.mu.RLock()
	jails := make(map[string]api.Jail, len(s.jails))
	for k, e := range s.jails {
		if k != self {
			jails[k] = e.jail
		}
	}
	s.mu.RUnlock()
	keys := make([]string, 0, len(jails))
	for k := range jails {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []forward
	for _, k := range keys {
		j := jails[k]
		out = append(out, s.forwardsOf(k, s.effective(j))...)
	}
	return out
}
