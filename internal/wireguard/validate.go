// Package wireguard holds the pure parts of the WireGuard feature: validation of names, keys, addresses and
// endpoints, the join token, the configuration files (the one `wg syncconf` reads and the one people import on a
// phone), the parser of `wg show <if> dump`, the pool allocator, and the operations the privileged helper offers
// (Host). Nothing here runs a command or touches a file.
//
// Secrets: a private key or a preshared key is accepted by Host operations that store it, returned only by the
// enrollment of a peer (for its token) and otherwise never leaves the helper. Errors never repeat a key.
package wireguard

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Limits.
const (
	MaxPeers     = 1024
	MaxPrefixes  = 64
	MaxDNS       = 8
	MaxKeepalive = 3600
	// MaxIfaceName is the longest interface name FreeBSD takes (IFNAMSIZ is 16 with the NUL).
	MaxIfaceName = 15
)

func invalid(field, format string, a ...any) *api.Error { return validate.Invalid(field, format, a...) }

// ValidKey reports whether s is a WireGuard key: standard base64 of 32 bytes (44 characters with one '=').
func ValidKey(s string) bool {
	if len(s) != 44 || s[43] != '=' {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// CheckKey validates a key without ever putting it into the error.
func CheckKey(field, s string) error {
	if !ValidKey(s) {
		return invalid(field, "%s is not a WireGuard key (base64 of 32 bytes)", field)
	}
	return nil
}

var ifaceRE = regexp.MustCompile(`^wg[A-Za-z0-9_.-]{1,13}$`)

// ValidIface validates the interface name of a tunnel: "wg" and then letters, digits, '_', '.' or '-', at most 15
// characters. Starting with "wg" keeps the tunnels recognisable and away from the names of the other interfaces
// JailOps manages.
func ValidIface(s string) error {
	if !ifaceRE.MatchString(s) {
		return invalid("interface", "interface %q must start with \"wg\" and have at most %d letters, digits, '_', '.' or '-'", s, MaxIfaceName)
	}
	return nil
}

// DefaultIface is the interface name of an object without one: "wg-<name>".
func DefaultIface(name string) string { return "wg-" + name }

// ValidName validates the name of a WireGuard object or of a peer.
func ValidName(field, s string) error { return validate.Name(field, s) }

// ParseEndpoint validates host:port. The host is an address or a DNS name; the port 1-65535.
func ParseEndpoint(field, s string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil || h == "" {
		return "", 0, invalid(field, "%s %q must be host:port (an IPv6 address in brackets)", field, s)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
		return "", 0, invalid(field, "%s %q has an invalid port", field, s)
	}
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() {
			return "", 0, invalid(field, "%s %q is not a usable address", field, s)
		}
		return h, n, nil
	}
	if err := validate.Hostname(field, h); err != nil {
		return "", 0, err
	}
	return h, n, nil
}

// ParsePrefixes parses CIDRs, keeping the host bits ("10.99.0.1/24" stays as it is). host says whether a bare
// address is taken as a host prefix (/32 or /128).
func ParsePrefixes(field string, l []string, host bool) ([]netip.Prefix, error) {
	if len(l) > MaxPrefixes {
		return nil, invalid(field, "too many %s (at most %d)", field, MaxPrefixes)
	}
	out := make([]netip.Prefix, 0, len(l))
	for _, s := range l {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil && host {
			if a, aerr := netip.ParseAddr(strings.TrimSpace(s)); aerr == nil {
				p, err = netip.PrefixFrom(a, a.BitLen()), nil
			}
		}
		if err != nil || p.Addr().Zone() != "" || p.Addr().Is4In6() {
			return nil, invalid(field, "%s %q is not an address with a prefix length", field, s)
		}
		out = append(out, p)
	}
	return out, nil
}

// Masked returns the networks of prefixes, without duplicates, in order.
func Masked(l []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range l {
		m := p.Masked()
		if !containsPrefix(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func containsPrefix(l []netip.Prefix, p netip.Prefix) bool {
	for _, x := range l {
		if x == p {
			return true
		}
	}
	return false
}

// Overlaps reports whether two prefixes share an address.
func Overlaps(a, b netip.Prefix) bool { return a.Overlaps(b) }

// ValidateSpec checks a spec as stored (defaults already filled). It needs no other objects; the service checks
// what depends on them (networks exist, ports are not taken twice).
func ValidateSpec(name string, s api.WireGuardSpec) error {
	if err := ValidName("name", name); err != nil {
		return err
	}
	if err := ValidIface(s.Interface); err != nil {
		return err
	}
	addrs, err := ParsePrefixes("address", s.Address, false)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return invalid("address", "a tunnel needs at least one address with a prefix, for example 10.99.0.1/24")
	}
	v4, v6 := 0, 0
	for _, a := range addrs {
		if a.Addr().Is4() {
			v4++
		} else {
			v6++
		}
		if a.Addr().IsLoopback() || a.Addr().IsMulticast() || a.Addr().IsUnspecified() || a.Addr().IsLinkLocalUnicast() {
			return invalid("address", "address %s cannot be used for a tunnel", a)
		}
	}
	if v4 > 1 || v6 > 1 {
		return invalid("address", "a tunnel has at most one IPv4 and one IPv6 address")
	}
	allowed, err := ParsePrefixes("allowed_ips", s.AllowedIPs, false)
	if err != nil {
		return err
	}
	for _, d := range s.DNS {
		if a, err := netip.ParseAddr(d); err != nil || a.Zone() != "" {
			return invalid("dns", "dns server %q is not an address", d)
		}
	}
	if len(s.DNS) > MaxDNS {
		return invalid("dns", "at most %d dns servers", MaxDNS)
	}
	if s.Keepalive < 0 || s.Keepalive > MaxKeepalive {
		return invalid("keepalive", "keepalive must be 0 to %d seconds", MaxKeepalive)
	}
	if s.ListenPort < 0 || s.ListenPort > 65535 {
		return invalid("listen_port", "listen port must be 1 to 65535")
	}
	switch s.Mode {
	case api.WireGuardServer:
		if s.ListenPort == 0 {
			return invalid("listen_port", "a server needs a listen port")
		}
		if s.PeerPublicKey != "" {
			return invalid("peer_public_key", "a server has peers, not a peer public key")
		}
		if s.Endpoint != "" {
			if _, _, err := ParseEndpoint("endpoint", s.Endpoint); err != nil {
				return err
			}
		}
		if len(s.Peers) > MaxPeers {
			return invalid("peers", "at most %d peers", MaxPeers)
		}
		for _, p := range allowed {
			if p != p.Masked() {
				return invalid("allowed_ips", "the pool %s must be a network (%s)", p, p.Masked())
			}
		}
		seenName, seenKey := map[string]bool{}, map[string]bool{}
		var used []netip.Prefix
		for i, p := range s.Peers {
			f := fmt.Sprintf("peers[%d]", i)
			if err := ValidName(f+".name", p.Name); err != nil {
				return err
			}
			if seenName[p.Name] {
				return invalid(f+".name", "peer %q is listed twice", p.Name)
			}
			seenName[p.Name] = true
			if err := CheckKey(f+".public_key", p.PublicKey); err != nil {
				return err
			}
			if seenKey[p.PublicKey] {
				return invalid(f+".public_key", "peer %q has the public key of another peer", p.Name)
			}
			seenKey[p.PublicKey] = true
			pa, err := ParsePrefixes(f+".allowed_ips", p.AllowedIPs, true)
			if err != nil {
				return err
			}
			if len(pa) == 0 {
				return invalid(f+".allowed_ips", "peer %q needs its tunnel address", p.Name)
			}
			for _, x := range pa {
				if x.Bits() == 0 {
					return invalid(f+".allowed_ips", "peer %q: a default route (%s) is not a peer address", p.Name, x)
				}
				for _, u := range used {
					if u.Overlaps(x) {
						return invalid(f+".allowed_ips", "peer %q: %s overlaps the address of another peer", p.Name, x)
					}
				}
				for _, a := range addrs {
					if x.Contains(a.Addr()) {
						return invalid(f+".allowed_ips", "peer %q: %s holds the server's own address %s", p.Name, x, a.Addr())
					}
				}
				used = append(used, x)
			}
			if p.Keepalive < 0 || p.Keepalive > MaxKeepalive {
				return invalid(f+".keepalive", "keepalive must be 0 to %d seconds", MaxKeepalive)
			}
		}
	case api.WireGuardClient:
		if len(s.Peers) > 0 {
			return invalid("peers", "a client has one server, not peers")
		}
		if len(s.Networks) > 0 {
			return invalid("networks", "networks are reached through a server; a client routes allowed_ips")
		}
		if s.Endpoint == "" {
			return invalid("endpoint", "a client needs the server's endpoint (host:port)")
		}
		if _, _, err := ParseEndpoint("endpoint", s.Endpoint); err != nil {
			return err
		}
		if err := CheckKey("peer_public_key", s.PeerPublicKey); err != nil {
			return err
		}
		if len(allowed) == 0 {
			return invalid("allowed_ips", "a client needs the prefixes it routes into the tunnel")
		}
		for _, p := range allowed {
			if p.Bits() == 0 {
				return invalid("allowed_ips", "a full tunnel (%s) is not supported: it would take the host's default route", p)
			}
			if p != p.Masked() {
				return invalid("allowed_ips", "%s must be a network (%s)", p, p.Masked())
			}
		}
	default:
		return invalid("mode", "mode must be server or client")
	}
	for _, n := range s.Networks {
		if err := validate.Name("networks", n); err != nil {
			return err
		}
	}
	return nil
}

// Defaults fills what a spec may leave out.
func Defaults(name, hostname string, s *api.WireGuardSpec) {
	if s.Interface == "" {
		s.Interface = DefaultIface(name)
	}
	switch s.Mode {
	case api.WireGuardServer:
		if s.ListenPort == 0 {
			s.ListenPort = api.DefaultWireGuardPort
		}
		if len(s.AllowedIPs) == 0 {
			if addrs, err := ParsePrefixes("address", s.Address, false); err == nil {
				for _, p := range Masked(addrs) {
					s.AllowedIPs = append(s.AllowedIPs, p.String())
				}
			}
		}
		if s.Endpoint == "" && hostname != "" {
			s.Endpoint = net.JoinHostPort(hostname, strconv.Itoa(s.ListenPort))
		}
	case api.WireGuardClient:
		if s.Keepalive == 0 {
			s.Keepalive = api.DefaultWireGuardKeepalive
		}
	}
}
