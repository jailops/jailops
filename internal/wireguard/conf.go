package wireguard

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Conf is the configuration of one interface in the form `wg setconf` and `wg syncconf` read. The helper builds it:
// the private key and the preshared keys come from its own files, the rest from the request.
type Conf struct {
	PrivateKey string
	// ListenPort zero leaves the port to the kernel (a client).
	ListenPort int
	Peers      []ConfPeer
}

// ConfPeer is one [Peer] section.
type ConfPeer struct {
	PublicKey    string
	PresharedKey string
	Endpoint     string
	AllowedIPs   []netip.Prefix
	Keepalive    int
}

// Check validates a configuration. Errors never repeat a key.
func (c Conf) Check() error {
	if err := CheckKey("private_key", c.PrivateKey); err != nil {
		return err
	}
	if c.ListenPort < 0 || c.ListenPort > 65535 {
		return invalid("listen_port", "listen port must be 0 to 65535")
	}
	if len(c.Peers) > MaxPeers {
		return invalid("peers", "at most %d peers", MaxPeers)
	}
	seen := map[string]bool{}
	for _, p := range c.Peers {
		if err := CheckKey("public_key", p.PublicKey); err != nil {
			return err
		}
		if seen[p.PublicKey] {
			return invalid("public_key", "a peer is listed twice")
		}
		seen[p.PublicKey] = true
		if p.PresharedKey != "" {
			if err := CheckKey("preshared_key", p.PresharedKey); err != nil {
				return err
			}
		}
		if p.Endpoint != "" {
			if _, _, err := ParseEndpoint("endpoint", p.Endpoint); err != nil {
				return err
			}
		}
		if len(p.AllowedIPs) > MaxPrefixes {
			return invalid("allowed_ips", "too many allowed IPs")
		}
		for _, a := range p.AllowedIPs {
			if !a.IsValid() || a.Addr().Zone() != "" {
				return invalid("allowed_ips", "invalid allowed IP %s", a)
			}
		}
		if p.Keepalive < 0 || p.Keepalive > MaxKeepalive {
			return invalid("keepalive", "keepalive must be 0 to %d seconds", MaxKeepalive)
		}
	}
	return nil
}

// Render returns the file. Every value was validated by Check, so nothing needs quoting; keys are base64 and
// endpoints are host:port.
func (c Conf) Render() (string, error) {
	if err := c.Check(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = " + c.PrivateKey + "\n")
	if c.ListenPort > 0 {
		b.WriteString("ListenPort = " + strconv.Itoa(c.ListenPort) + "\n")
	}
	for _, p := range c.Peers {
		b.WriteString("\n[Peer]\n")
		b.WriteString("PublicKey = " + p.PublicKey + "\n")
		if p.PresharedKey != "" {
			b.WriteString("PresharedKey = " + p.PresharedKey + "\n")
		}
		if len(p.AllowedIPs) > 0 {
			l := make([]string, len(p.AllowedIPs))
			for i, a := range p.AllowedIPs {
				l[i] = a.String()
			}
			b.WriteString("AllowedIPs = " + strings.Join(l, ", ") + "\n")
		}
		if p.Endpoint != "" {
			b.WriteString("Endpoint = " + p.Endpoint + "\n")
		}
		if p.Keepalive > 0 {
			b.WriteString("PersistentKeepalive = " + strconv.Itoa(p.Keepalive) + "\n")
		}
	}
	return b.String(), nil
}

// Matches reports whether what the kernel holds (a parsed dump) is the configuration: the public key of the
// private key, the listen port (when one is set), and per peer the allowed IPs, the keepalive, whether a
// preshared key is set, and the endpoint when the configuration names one (a server learns its peers' endpoints,
// so an endpoint it did not configure is not a difference). A name endpoint is never compared: the kernel holds
// the address it resolved to.
func (c Conf) Matches(publicKey string, d Dump) bool {
	if d.PublicKey != publicKey || (c.ListenPort != 0 && d.ListenPort != c.ListenPort) || len(d.Peers) != len(c.Peers) {
		return false
	}
	have := map[string]DumpPeer{}
	for _, p := range d.Peers {
		have[p.PublicKey] = p
	}
	for _, p := range c.Peers {
		h, ok := have[p.PublicKey]
		if !ok || h.Keepalive != p.Keepalive || h.PresharedKey != (p.PresharedKey != "") {
			return false
		}
		want := make([]string, len(p.AllowedIPs))
		for i, a := range p.AllowedIPs {
			want[i] = a.Masked().String()
		}
		got := slices.Clone(h.AllowedIPs)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			return false
		}
		if p.Endpoint != "" {
			if host, _, err := ParseEndpoint("endpoint", p.Endpoint); err == nil {
				if _, err := netip.ParseAddr(host); err == nil && h.Endpoint != p.Endpoint {
					return false
				}
			}
		}
	}
	return true
}
