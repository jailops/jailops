package ndp

import (
	"net/netip"
	"regexp"
	"strings"
)

// RouteGet is the parsed output of `route -n get -inet6 <addr>`.
type RouteGet struct {
	Dest, Gateway, Interface, Flags string
}

// ParseRouteGet parses "key: value" lines. UNVERIFIED: FreeBSD 15 prints "route to:",
// "destination:", "gateway:", "interface:", "flags:" in this form.
func ParseRouteGet(out string) RouteGet {
	var r RouteGet
	for _, ln := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(ln, ": ")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "route to":
			r.Dest = v
		case "gateway":
			r.Gateway = v
		case "interface":
			r.Interface = v
		case "flags":
			r.Flags = v
		}
	}
	return r
}

// Inet6 is one inet6 line of ifconfig.
type Inet6 struct {
	Addr      netip.Addr
	Zone      string
	PrefixLen int
	Flags     []string // tentative, duplicated, deprecated, ...
}

// IfInfo is the parsed output of `ifconfig <name>`.
type IfInfo struct {
	Name    string
	Flags   []string
	Ether   MAC
	Inet6   []Inet6
	Members []string
}

// HasFlag reports an interface flag such as PROMISC.
func (i IfInfo) HasFlag(f string) bool {
	for _, x := range i.Flags {
		if x == f {
			return true
		}
	}
	return false
}

var flagsRe = regexp.MustCompile(`^(\S+): flags=[0-9a-fA-F]+<([^>]*)>`)

// ParseIfconfig parses FreeBSD ifconfig output for one interface.
func ParseIfconfig(out string) IfInfo {
	var info IfInfo
	for _, ln := range strings.Split(out, "\n") {
		if m := flagsRe.FindStringSubmatch(ln); m != nil && info.Name == "" {
			info.Name = m[1]
			if m[2] != "" {
				info.Flags = strings.Split(m[2], ",")
			}
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "ether":
			if m, err := ParseMAC(f[1]); err == nil {
				info.Ether = m
			}
		case "member:":
			info.Members = append(info.Members, f[1])
		case "inet6":
			a, zone, _ := strings.Cut(f[1], "%")
			addr, err := netip.ParseAddr(a)
			if err != nil {
				continue
			}
			x := Inet6{Addr: addr, Zone: zone}
			for i := 2; i < len(f); i++ {
				switch f[i] {
				case "prefixlen":
					if i+1 < len(f) {
						n := 0
						for _, c := range f[i+1] {
							n = n*10 + int(c-'0')
						}
						x.PrefixLen = n
						i++
					}
				case "scopeid", "pltime", "vltime":
					i++
				default:
					x.Flags = append(x.Flags, f[i])
				}
			}
			info.Inet6 = append(info.Inet6, x)
		}
	}
	return info
}

// Global returns the non-link-local, non-loopback addresses.
func (i IfInfo) Global() []Inet6 {
	var g []Inet6
	for _, a := range i.Inet6 {
		if a.Addr.IsLinkLocalUnicast() || a.Addr.IsLoopback() {
			continue
		}
		g = append(g, a)
	}
	return g
}

// LinkLocal returns the first link-local address, if any.
func (i IfInfo) LinkLocal() (netip.Addr, bool) {
	for _, a := range i.Inet6 {
		if a.Addr.IsLinkLocalUnicast() {
			return a.Addr, true
		}
	}
	return netip.Addr{}, false
}

// NdpEntry is a row of `ndp -an`.
type NdpEntry struct {
	Addr   netip.Addr
	MAC    string // "(incomplete)" kept verbatim
	Iface  string
	Expire string
	State  string
	Flags  string
}

// ParseNdpAn parses `ndp -an`. UNVERIFIED: columns Neighbor, Linklayer Address, Netif, Expire, S, Flags.
func ParseNdpAn(out string) []NdpEntry {
	var es []NdpEntry
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 5 || f[0] == "Neighbor" {
			continue
		}
		a, _, _ := strings.Cut(f[0], "%")
		addr, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		e := NdpEntry{Addr: addr, MAC: f[1], Iface: f[2], Expire: f[3], State: f[4]}
		if len(f) > 5 {
			e.Flags = strings.Join(f[5:], " ")
		}
		es = append(es, e)
	}
	return es
}

// FindNdp returns the entries for addr.
func FindNdp(es []NdpEntry, addr netip.Addr) []NdpEntry {
	var r []NdpEntry
	for _, e := range es {
		if e.Addr == addr {
			r = append(r, e)
		}
	}
	return r
}

// TcpdumpEvent is one NS/NA line of `tcpdump -n -e`.
type TcpdumpEvent struct {
	Time           string
	EthSrc, EthDst string
	Src, Dst       netip.Addr
	NS, NA         bool
	Target         netip.Addr
}

var tcpRe = regexp.MustCompile(`^(\S+) ([0-9a-f:]{17}) > ([0-9a-f:]{17}), ethertype IPv6.*?: (\S+) > (\S+?): ICMP6, neighbor (solicitation|advertisement), (?:who has|tgt is) ([0-9a-f:.]+)`)

// ParseTcpdumpLine parses one line; ok is false for other lines. UNVERIFIED: tcpdump 4.99 text format.
func ParseTcpdumpLine(ln string) (TcpdumpEvent, bool) {
	m := tcpRe.FindStringSubmatch(ln)
	if m == nil {
		return TcpdumpEvent{}, false
	}
	var e TcpdumpEvent
	e.Time, e.EthSrc, e.EthDst = m[1], m[2], m[3]
	var err error
	if e.Src, err = netip.ParseAddr(m[4]); err != nil {
		return e, false
	}
	if e.Dst, err = netip.ParseAddr(m[5]); err != nil {
		return e, false
	}
	e.NS = m[6] == "solicitation"
	e.NA = !e.NS
	e.Target, _ = netip.ParseAddr(strings.TrimRight(m[7], ","))
	return e, true
}

// SummarizeTcpdump counts NS (multicast, unicast) and NA events for target seen in a capture,
// and how many NS for target were sent by hostMAC (the host asking for the jail address).
func SummarizeTcpdump(lines []string, target netip.Addr, hostMAC string) (mns, uns, na, hostNS int) {
	for _, ln := range lines {
		e, ok := ParseTcpdumpLine(ln)
		if !ok || e.Target != target {
			continue
		}
		switch {
		case e.NS && e.Dst.IsMulticast():
			mns++
		case e.NS:
			uns++
		case e.NA:
			na++
		}
		if e.NS && strings.EqualFold(e.EthSrc, hostMAC) {
			hostNS++
		}
	}
	return
}
