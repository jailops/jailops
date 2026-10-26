package ifconfig

import (
	"net/netip"
	"strconv"
	"strings"
)

// ParseIfconfig parses the output of `ifconfig -a` defensively. Lines it
// does not understand are ignored; an interface header it cannot read ends
// the previous interface and is skipped with its body.
func ParseIfconfig(out string) []Iface {
	var res []Iface
	var cur *Iface
	flush := func() {
		if cur != nil {
			res = append(res, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "" {
			continue
		}
		if line[0] != '\t' && line[0] != ' ' {
			flush()
			if i, ok := parseHeader(line); ok {
				cur = &i
			}
			continue
		}
		if cur == nil {
			continue
		}
		line = strings.TrimSpace(line)
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		// Other lines (options, nd6, bridge member detail lines) are ignored.
		switch {
		case f[0] == "description:":
			cur.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		case f[0] == "ether" && len(f) >= 2:
			cur.MAC = f[1]
		case f[0] == "inet" && len(f) >= 2:
			if p, ok := parseInet(f); ok {
				cur.Inet = append(cur.Inet, p)
			}
		case f[0] == "inet6" && len(f) >= 2:
			if p, ok := parseInet6(f); ok {
				cur.Inet6 = append(cur.Inet6, p)
			}
		case f[0] == "vlan:" && len(f) >= 2:
			cur.VlanTag, _ = strconv.Atoi(f[1])
			if i := strings.Index(line, "parent interface:"); i >= 0 {
				cur.VlanParent = strings.TrimSpace(line[i+len("parent interface:"):])
			}
		case f[0] == "groups:":
			cur.Groups = append(cur.Groups, f[1:]...)
		case f[0] == "status:":
			cur.Status = strings.TrimSpace(strings.TrimPrefix(line, "status:"))
		case f[0] == "member:" && len(f) >= 2:
			m := Member{Name: f[1]}
			if fl := bracket(line); fl != nil {
				m.Flags = fl
				for _, x := range fl {
					if x == "PRIVATE" {
						m.Private = true
					}
				}
			}
			cur.Members = append(cur.Members, m)
		}
	}
	flush()
	return res
}

// bracket returns the comma separated words inside the first <...>.
func bracket(s string) []string {
	i := strings.IndexByte(s, '<')
	if i < 0 {
		return nil
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return nil
	}
	in := s[i+1 : i+j]
	if in == "" {
		return []string{}
	}
	return strings.Split(in, ",")
}

// parseHeader reads "em0: flags=8843<UP,...> metric 0 mtu 1500".
func parseHeader(line string) (Iface, bool) {
	name, rest, ok := strings.Cut(line, ": ")
	if !ok || name == "" || strings.ContainsAny(name, " \t") || ValidateIfName(name) != nil {
		return Iface{}, false
	}
	if !strings.HasPrefix(rest, "flags=") {
		return Iface{}, false
	}
	i := Iface{Name: name}
	i.Flags = bracket(rest)
	f := strings.Fields(rest)
	for k := 0; k+1 < len(f); k++ {
		if f[k] == "mtu" {
			i.MTU, _ = strconv.Atoi(f[k+1])
		}
	}
	return i, true
}

// parseInet reads "inet 10.0.0.2 netmask 0xffffff00 broadcast ...".
func parseInet(f []string) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(f[1])
	if err != nil || !a.Is4() {
		return netip.Prefix{}, false
	}
	bits := 32
	for k := 2; k+1 < len(f); k++ {
		if f[k] == "netmask" {
			if b, ok := maskBits(f[k+1]); ok {
				bits = b
			}
		}
	}
	return netip.PrefixFrom(a, bits), true
}

// parseInet6 reads "inet6 fe80::1%em0 prefixlen 64 scopeid 0x1".
func parseInet6(f []string) (netip.Prefix, bool) {
	s, _, _ := strings.Cut(f[1], "%")
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is6() {
		return netip.Prefix{}, false
	}
	bits := 128
	for k := 2; k+1 < len(f); k++ {
		if f[k] == "prefixlen" {
			if n, err := strconv.Atoi(f[k+1]); err == nil && n >= 0 && n <= 128 {
				bits = n
			}
		}
	}
	return netip.PrefixFrom(a, bits), true
}

// maskBits converts a hex (0xffffff00) or dotted netmask to a prefix length;
// non-contiguous masks are rejected.
func maskBits(s string) (int, bool) {
	var m uint32
	if h, ok := strings.CutPrefix(s, "0x"); ok {
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return 0, false
		}
		m = uint32(v)
	} else {
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() {
			return 0, false
		}
		b := a.As4()
		m = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	}
	n := 0
	for m&0x80000000 != 0 {
		n++
		m <<= 1
	}
	if m != 0 {
		return 0, false
	}
	return n, true
}
