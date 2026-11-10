package service

import (
	"fmt"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// doctorPolicyDefaults fails for every stored firewall policy without default_in or default_out: the jails that use
// it (or, for the project's mandatory policy, every jail of the project) keep the anchor they have and are not
// rendered again until the policy says both.
func (s *Service) doctorPolicyDefaults(c *checkSet) {
	pols, err := s.polKind.listAll()
	if err != nil {
		c.add("firewall.defaults", api.DoctorUnknown, "the firewall policies cannot be read: "+err.Error(), "")
		return
	}
	var bad []string
	for _, o := range pols {
		if err := policyDefaults(o.Meta.Name, o.Spec); err != nil {
			var missing []string
			for _, d := range []struct{ field, v string }{{"default_in", o.Spec.DefaultIn}, {"default_out", o.Spec.DefaultOut}} {
				if d.v != "allow" && d.v != "deny" {
					missing = append(missing, d.field)
				}
			}
			bad = append(bad, fmt.Sprintf("%s/%s lacks %s", o.Meta.Project, o.Meta.Name, strings.Join(missing, " and ")))
		}
	}
	if len(bad) == 0 {
		return
	}
	c.add("firewall.defaults", api.DoctorFail, strings.Join(bad, "; "),
		"jailops firewall policy edit NAME --project PROJECT: set default_in and default_out; until then the jails that use the policy are not rendered again")
}
