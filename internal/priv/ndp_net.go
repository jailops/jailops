package priv

import (
	"net"
	"net/netip"
	"os/exec"
	"strings"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

func hardwareAddr(name string) ([6]byte, error) {
	var mac [6]byte
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return mac, api.Errorf(api.CodeNotFound, "interface %s: %v", name, err)
	}
	if len(ifi.HardwareAddr) != 6 {
		return mac, api.Errorf(api.CodeInvalid, "interface %s has no Ethernet address", name)
	}
	copy(mac[:], ifi.HardwareAddr)
	return mac, nil
}

// hostRoutes reads the IPv6 routing table with netstat(1) and returns its /128 host routes. UNVERIFIED output format
// on other systems than FreeBSD 15.1, where a host route is printed as "<address>  link#N  UHS  <interface>".
func hostRoutes() (map[netip.Addr]string, error) { return ReadHostRoutes() }

// ReadHostRoutes is hostRoutes for callers outside the package: netstat(1) needs no privilege, so the daemon reads
// the table itself to find host routes of jails that no longer exist.
func ReadHostRoutes() (map[netip.Addr]string, error) {
	out, err := exec.Command("/usr/bin/netstat", "-rnW", "-f", "inet6").Output()
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "netstat: %v", err)
	}
	return parseHostRoutes(string(out)), nil
}

// parseHostRoutes picks the host routes (flag H) out of netstat -rn output: the first column is the destination, the
// last the interface.
func parseHostRoutes(text string) map[netip.Addr]string {
	routes := map[netip.Addr]string{}
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 || !strings.Contains(f[2], "H") {
			continue
		}
		dest := strings.TrimSuffix(f[0], "/128")
		if i := strings.IndexByte(dest, '%'); i >= 0 {
			continue // a link-local destination: not an address of a jail
		}
		a, err := netip.ParseAddr(dest)
		if err != nil || !a.Is6() {
			continue
		}
		routes[a] = f[len(f)-1]
	}
	return routes
}

func localAddrs() ([]netip.Addr, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range as {
		if ipn, ok := a.(*net.IPNet); ok {
			if ad, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ad.Unmap())
			}
		}
	}
	return out, nil
}

// ReadStaticNeighbors reads the permanent entries of the neighbor tables with ndp(8) and arp(8) (no privilege needed):
// the link-local IPv6 entries and the IPv4 ones. UNVERIFIED output format on other systems than FreeBSD 15.1:
// "fe80::1%jo30gb  58:9c:fc:10:5e:2c  jo30gb  permanent R".
func ReadStaticNeighbors() ([]ifconfig.Neighbor, error) {
	out, err := exec.Command("/usr/sbin/ndp", "-an").Output()
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "ndp: %v", err)
	}
	list := parseStaticNeighbors(string(out))
	arp, err := exec.Command("/usr/sbin/arp", "-an").Output()
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "arp: %v", err)
	}
	return append(list, parseStaticARP(string(arp))...), nil
}

// parseStaticARP reads the permanent entries of `arp -an`:
// "? (10.94.0.1) at 58:9c:fc:10:5e:2c on jo0gb permanent [ethernet]".
func parseStaticARP(text string) []ifconfig.Neighbor {
	var out []ifconfig.Neighbor
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 7 || f[2] != "at" || f[4] != "on" || f[6] != "permanent" {
			continue
		}
		a, err := netip.ParseAddr(strings.Trim(f[1], "()"))
		if err != nil || !a.Is4() {
			continue
		}
		out = append(out, ifconfig.Neighbor{Addr: a, Iface: f[5], MAC: f[3]})
	}
	return out
}

func parseStaticNeighbors(text string) []ifconfig.Neighbor {
	var out []ifconfig.Neighbor
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 || f[3] != "permanent" {
			continue
		}
		addr, _, _ := strings.Cut(f[0], "%")
		a, err := netip.ParseAddr(addr)
		if err != nil || !a.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ifconfig.Neighbor{Addr: a, Iface: f[2], MAC: f[1]})
	}
	return out
}
