package service

import (
	"context"
	"fmt"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// rateLimitWarning is the warning of a jail whose spec sets a rate limit.
const rateLimitWarning = "rate limits (nic ingress/egress) are stored but not enforced yet: no shaper is wired"

// doctorAppsAccess reports grants of app access whose firewall rule or
// pg_hba.conf line is missing on the host (drift), or whose source jail is gone.
func (s *Service) doctorAppsAccess(ctx context.Context, c *checkSet) {
	problems, n, err := s.accessDrift(ctx)
	switch {
	case err != nil:
		c.add("apps.access", api.DoctorUnknown, "the grants of app access cannot be read: "+err.Error(), "")
	case n == 0:
		return
	case len(problems) > 0:
		msg := fmt.Sprintf("%d problem(s) in %d grant(s): %s", len(problems), n, strings.Join(problems, "; "))
		c.add("apps.access", api.DoctorWarn, msg,
			"jailops app access APP shows each grant with what is missing; jailops app allow APP from SOURCE writes it again, jailops app deny takes a grant back")
	default:
		c.add("apps.access", api.DoctorOK, fmt.Sprintf("%d grant(s); every firewall rule and pg_hba.conf line is in place", n), "")
	}
}

// doctorRateLimits says that rate limits are not enforced, when a jail has one.
func (s *Service) doctorRateLimits(c *checkSet) {
	s.mu.RLock()
	var all []api.Jail
	for _, k := range sortedKeys(s.jails) {
		all = append(all, s.jails[k].jail)
	}
	s.mu.RUnlock()
	var names []string
	for _, j := range all {
		for _, nd := range s.effective(j).Network {
			if nd.Ingress != "" || nd.Egress != "" {
				names = append(names, j.Metadata.Project+"/"+j.Metadata.Name)
				break
			}
		}
	}
	if len(names) == 0 {
		return
	}
	c.add("jails.rate_limits", api.DoctorInfo, fmt.Sprintf("%d jail(s) set a rate limit, which is not enforced yet: %s", len(names), strings.Join(names, ", ")),
		"the limits are stored and reported; traffic is not shaped until a later version wires a shaper")
}
