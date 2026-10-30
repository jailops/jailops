package render

import (
	"net/netip"
	"regexp"

	"jailops.net/jailops/internal/validate"
)

var wgIfaceRE = regexp.MustCompile(`^wg[A-Za-z0-9_.-]{1,13}$`)

// PFWireGuard renders the anchor jailops/wg.<name> of a WireGuard server: traffic that comes in on the tunnel from
// the tunnel's own subnets may go to the subnets of the JailOps networks the server names, and nothing else is
// decided here (the host's ruleset and the jails' own policies still apply). Without networks the anchor is empty
// ("" is returned and the anchor is flushed). UNVERIFIED on FreeBSD: an interface name with '-' in a rule.
func PFWireGuard(name, iface string, tunnels, networks []netip.Prefix) (string, error) {
	if err := validate.Name("wireguard", name); err != nil {
		return "", err
	}
	if !wgIfaceRE.MatchString(iface) {
		return "", validate.Invalid("interface", "invalid wireguard interface %q", iface)
	}
	var b builder
	n := 0
	for _, to := range networks {
		for _, from := range tunnels {
			if !from.IsValid() || !to.IsValid() || from.Addr().Is4() != to.Addr().Is4() {
				continue
			}
			if n == 0 {
				b.line("%s", Header)
				b.line("# anchor jailops/wg.%s: the peers of the tunnel reach these JailOps networks", name)
			}
			fam := "inet"
			if from.Addr().Is6() {
				fam = "inet6"
			}
			b.line("pass in quick on %s %s from %s to %s keep state", iface, fam, from.Masked(), to.Masked())
			n++
		}
	}
	if n == 0 {
		return "", nil
	}
	return b.result()
}
