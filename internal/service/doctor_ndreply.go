package service

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// ndReplyPathRecent is how long a repeated solicitation answered to the wrong MAC keeps the check warning.
const ndReplyPathRecent = 24 * time.Hour

// doctorNDReplyPath reports a router of the provider that asked for an address of the host three or more times within
// five seconds while the host sent its answer to another MAC than the router's (the gateway's): the host holds its
// address as a /128 and has no on-link route for the routers, so FreeBSD sends the advertisement through its default
// route, and the router that asked (the VRRP backup) never gets it. Traffic through it stalls a few seconds after each
// idle period. The capture of the NDP proxy observes it; nothing on the host is changed. The jails' addresses are not
// part of it: the proxy answers for them to the asker's MAC itself.
func (s *Service) doctorNDReplyPath(c *checkSet) {
	ndReplyPathCheck(c, s.NDPStatus(context.Background()), s.now())
}

func ndReplyPathCheck(c *checkSet, st api.NDPStatus, now time.Time) {
	if len(st.Uplinks) == 0 {
		return // no capture: nothing is observed on this host
	}
	var seen uint64
	for _, u := range st.Uplinks {
		seen += u.HostSolicits
	}
	type fix struct{ prefix, iface string }
	var bad []string
	var fixes []fix
	have := map[fix]bool{}
	for _, rp := range st.ReplyPath {
		if !rp.Misdirected || now.Sub(rp.LastSeen) > ndReplyPathRecent {
			continue
		}
		bad = append(bad, fmt.Sprintf("the router %s (MAC %s) asked %d time(s) again for the host's %s on %s; the host answered to %s",
			rp.Router, rp.RouterMAC, rp.Repeats, rp.HostAddress, rp.Uplink, rp.NADstMAC))
		if a, err := netip.ParseAddr(rp.Router); err == nil {
			p, _ := a.Prefix(64)
			f := fix{p.String(), rp.Uplink}
			if !have[f] {
				have[f] = true
				fixes = append(fixes, f)
			}
		}
	}
	switch {
	case len(bad) > 0:
		sort.Slice(fixes, func(i, j int) bool { return fixes[i].prefix < fixes[j].prefix })
		names := make([]string, 0, len(fixes))
		lines, apply := []string{}, []string{}
		for i, f := range fixes {
			name := "infra"
			if len(fixes) > 1 {
				name = fmt.Sprintf("infra%d", i+1)
			}
			names = append(names, name)
			lines = append(lines, fmt.Sprintf(`ipv6_route_%s="-net %s -interface %s"`, name, f.prefix, f.iface))
			apply = append(apply, fmt.Sprintf("route -6 add -net %s -interface %s", f.prefix, f.iface))
		}
		hint := fmt.Sprintf(`give the host an on-link route for the routers' prefix in /etc/rc.conf: ipv6_static_routes="%s" (add the name to the list if it has others) and %s; to apply it now without a reboot: %s; see docs/networking-ipv6.md, "Answers to the provider's routers"`,
			strings.Join(names, " "), strings.Join(lines, " and "), strings.Join(apply, " and "))
		c.add("network.nd_reply_path", api.DoctorWarn, "the host sends its neighbor advertisements to the default gateway, not to the router that asked, which asks again and stalls the traffic through it for a few seconds: "+strings.Join(bad, "; "), hint)
	case seen == 0:
		c.add("network.nd_reply_path", api.DoctorInfo, "no data yet: no neighbor solicitation for an address of the host was seen on the uplink since jailopsd started", "")
	default:
		c.add("network.nd_reply_path", api.DoctorOK, "the host's answers to neighbor solicitations reach the routers that ask", "")
	}
}
