package render

import (
	"net/netip"
	"regexp"
	"sort"

	"jailops.net/jailops/internal/validate"
)

var wgIfaceRE = regexp.MustCompile(`^wg[A-Za-z0-9_.-]{1,13}$`)

// PFWireGuard renders the anchor jailops/wg.<name> of a WireGuard server. It holds only what protects the host from the
// tunnel: a source that is no address of the tunnel is dropped (the antispoof of the interface), and so is everything a
// peer sends to the host itself. What a peer reaches in a jail is not decided here: the peers of a tunnel are one more
// external source, and the jail's own `allow in` rules with their source (the tunnel's prefix, a set, an address) open
// the tunnel's interface for the jail's addresses, exactly as they open the uplink for the Internet (render.uplinkRules).
// A peer reaches nothing outside the networks the server serves (served): what is addressed anywhere else, the host's
// LAN or whatever its pf.conf would pass, is dropped here, so a tunnel is no way into the rest of the host's networks.
func PFWireGuard(name, iface string, tunnels []netip.Prefix, host []netip.Addr, served []netip.Prefix) (string, error) {
	if err := validate.Name("wireguard", name); err != nil {
		return "", err
	}
	if !wgIfaceRE.MatchString(iface) {
		return "", validate.Invalid("interface", "invalid wireguard interface %q", iface)
	}
	var b builder
	b.line("%s", Header)
	b.line("# anchor jailops/wg.%s: what protects the host from the tunnel; what the peers reach in a jail is decided by the jail's own allow in rules", name)
	b.line("%s", tableLine(HostTable, addrStrings(uniqAddrs(host))))
	var nets []string
	for _, p := range tunnels {
		if p.IsValid() {
			nets = append(nets, p.Masked().String())
		}
	}
	sort.Strings(nets)
	if len(nets) > 0 {
		b.line("%s", tableLine("tunnel_nets", nets))
		b.line("# a packet that comes in on the tunnel with a source that is no address of the tunnel is forged")
		b.line("block drop in quick on %s from ! <tunnel_nets> to any", iface)
	}
	b.line("# the host: closed to the peers (the host's own pf.conf decides what the host offers them, if anything)")
	b.line("block drop in quick on %s from any to <%s>", iface, HostTable)
	var sn []string
	for _, p := range served {
		if p.IsValid() {
			sn = append(sn, p.Masked().String())
		}
	}
	sort.Strings(sn)
	if len(sn) > 0 {
		b.line("%s", tableLine("served_nets", sn))
		b.line("# a peer reaches the networks this tunnel serves (their jails decide) and nothing else")
		b.line("block drop in quick on %s from any to ! <served_nets>", iface)
	} else {
		b.line("# this tunnel serves no network: a peer reaches nothing")
		b.line("block drop in quick on %s all", iface)
	}
	out, err := b.result()
	if err != nil {
		return "", err
	}
	return out, nil
}
