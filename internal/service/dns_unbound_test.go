package service

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"jailops.net/jailops/internal/mounts"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/sdk/api"
)

// upJails is a jail runtime that lists the jails it is given as running.
type upJails struct {
	jailos.Jails
	up []string
}

func (u upJails) List(context.Context) ([]jailos.Jail, error) {
	var out []jailos.Jail
	for i, n := range u.up {
		out = append(out, jailos.Jail{Name: n, JID: i + 1})
	}
	return out, nil
}

// twoProjects is a host with the networks lan (project shop) and back (project office), each with a running jail.
func twoProjects(t *testing.T) *Service {
	t.Helper()
	s := newSvc(t)
	nets := map[string]api.NetworkSpec{
		"lan":  {IPv4: &api.IPv4Net{Subnet: "10.77.0.0/24", Gateway: "10.77.0.1"}},
		"back": {IPv4: &api.IPv4Net{Subnet: "10.77.1.0/24", Gateway: "10.77.1.1"}, IPv6: &api.IPv6Net{Subnet: "2001:db8:1:2:100::/80"}},
	}
	for n, spec := range nets {
		if _, err := s.netKind.create(bg, api.ObjectMeta{Name: n}, spec); err != nil {
			t.Fatalf("network %s: %v", n, err)
		}
	}
	add := func(project, name, network, v4 string) {
		j := api.Jail{Metadata: api.ObjectMeta{Name: name, Project: project},
			Spec: api.JailSpec{Network: []api.NetworkAttach{{Network: network, IPv4: v4}}}}
		s.jails[jailKey(j)] = &jailEntry{jail: j}
	}
	add("shop", "web", "lan", "10.77.0.2")
	add("office", "mail", "back", "10.77.1.2")
	s.d.Jails = upJails{up: []string{"shop_web", "office_mail"}}
	return s
}

// The views of the resolver hold the names of one project: each network is asked for its own jail, so that the
// gateway of one project's network does not have to know the names of another's.
func TestUnboundProbesAreOnePerNetwork(t *testing.T) {
	s := twoProjects(t)
	got := s.unboundProbes(bg, "internal")
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if p := got[0]; p.network != "back" || p.name != "mail.office.internal" || p.want != "10.77.1.2" || strings.Join(p.addrs, ",") != "10.77.1.1,2001:db8:1:2:100::1" {
		t.Errorf("%+v", p)
	}
	if p := got[1]; p.network != "lan" || p.name != "web.shop.internal" || p.want != "10.77.0.2" || strings.Join(p.addrs, ",") != "10.77.0.1" {
		t.Errorf("%+v", p)
	}
}

func TestUnboundProbesSkipANetworkWithoutARunningJail(t *testing.T) {
	s := twoProjects(t)
	s.d.Jails = upJails{up: []string{"shop_web"}}
	got := s.unboundProbes(bg, "internal")
	if len(got) != 1 || got[0].network != "lan" {
		t.Fatalf("%+v", got)
	}
}

// datagram makes a pipe a packet connection: the Go resolver frames a stream connection with a length, a datagram not.
type datagram struct{ net.Conn }

func (d datagram) ReadFrom(b []byte) (int, net.Addr, error)  { n, err := d.Read(b); return n, nil, err }
func (d datagram) WriteTo(b []byte, _ net.Addr) (int, error) { return d.Write(b) }

// serveDNS answers one query on conn the way the view of a network does: the name of its own project's jail gets its
// address, any other name is NXDOMAIN.
func serveDNS(conn net.Conn, own, addr string) {
	defer conn.Close()
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil || n < 17 {
		return
	}
	q := buf[:n]
	i := 12
	var name strings.Builder
	for i < n && q[i] != 0 {
		l := int(q[i])
		name.WriteString(string(q[i+1:i+1+l]) + ".")
		i += l + 1
	}
	qend := i + 5 // the zero byte, the type and the class
	qtype := int(q[i+1])<<8 | int(q[i+2])
	resp := append([]byte{}, q[:2]...)
	known := name.String() == own
	flags := []byte{0x81, 0x80}
	if !known {
		flags[1] |= 3 // NXDOMAIN
	}
	resp = append(resp, flags...)
	an := byte(0)
	if known && qtype == 1 {
		an = 1
	}
	resp = append(resp, 0, 1, 0, an, 0, 0, 0, 0)
	resp = append(resp, q[12:qend]...)
	if an == 1 {
		ip := net.ParseIP(addr).To4()
		resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, ip[0], ip[1], ip[2], ip[3])
	}
	conn.Write(resp)
}

// answering makes the views answer as unbound does: at each gateway the jail of that network's project; the address in
// broken refuses the connection.
func answering(s *Service, broken string) (asked func() []string) {
	views := map[string][2]string{
		"10.77.0.1":           {"web.shop.internal.", "10.77.0.2"},
		"10.77.1.1":           {"mail.office.internal.", "10.77.1.2"},
		"2001:db8:1:2:100::1": {"mail.office.internal.", "10.77.1.2"},
	}
	var mu sync.Mutex
	var got []string
	s.d.DialJail = func(_ context.Context, _, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		mu.Lock()
		got = append(got, host)
		mu.Unlock()
		if host == broken {
			return nil, errors.New("connection refused")
		}
		c, srv := net.Pipe()
		go serveDNS(srv, views[host][0], views[host][1])
		return datagram{c}, nil
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(got)
		slices.Sort(out)
		return slices.Compact(out)
	}
}

// stateFiles is the helper's answer for the doctor: the names are on and current.
type stateFiles struct {
	FS
}

func (stateFiles) DNSUnbound(context.Context, bool) (mounts.UnboundState, error) {
	return mounts.UnboundState{Enabled: true, Current: true, Path: "/etc/unbound/jailops.conf", Suffix: "internal", Names: 4, Jails: 2, Projects: 2, Recurse: true}, nil
}

func dnsCheck(s *Service) api.DoctorCheck {
	c := &checkSet{}
	s.doctorUnbound(bg, c)
	for _, ck := range c.list {
		if ck.Name == "dns.unbound" {
			return ck
		}
	}
	return api.DoctorCheck{}
}

// Two projects on one host: the gateway of each network answers for the names of its own project only, and that is what
// the doctor asks for; the gateway of the other network is not asked for a jail that is not on it.
func TestDoctorAsksEachNetworkForItsOwnJail(t *testing.T) {
	s := twoProjects(t)
	s.d.Files = stateFiles{}
	asked := answering(s, "")
	ck := dnsCheck(s)
	if ck.Status != api.DoctorOK {
		t.Fatalf("%+v", ck)
	}
	if got := asked(); strings.Join(got, " ") != "10.77.0.1 10.77.1.1 2001:db8:1:2:100::1" {
		t.Fatalf("asked %v", got)
	}
}

// A network whose gateway does not answer is named in the warning.
func TestDoctorNamesTheNetworkThatDoesNotAnswer(t *testing.T) {
	s := twoProjects(t)
	s.d.Files = stateFiles{}
	answering(s, "10.77.1.1")
	ck := dnsCheck(s)
	if ck.Status != api.DoctorWarn || !strings.Contains(ck.Message, "network back: asking 10.77.1.1 for the name of its running jail mail.office.internal") || strings.Contains(ck.Message, "127.0.0.1") {
		t.Fatalf("%+v", ck)
	}
}

func TestLookupAtNamesWhatWentWrongNotTheResolvConf(t *testing.T) {
	s := newSvc(t)
	answering(s, "10.77.1.1")
	_, why := s.lookupAt(bg, "10.77.1.1", "mail.office.internal")
	if why == "" || strings.Contains(why, "127.0.0.1") || strings.Contains(why, "lookup mail.office") {
		t.Fatalf("the text names a nameserver that was not asked: %q", why)
	}
	got, why := s.lookupAt(bg, "10.77.0.1", "mail.office.internal")
	if len(got) != 0 || !strings.Contains(why, "no such host") || strings.Contains(why, "127.0.0.1") {
		t.Fatalf("%v %q", got, why)
	}
}
