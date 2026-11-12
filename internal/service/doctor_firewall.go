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

// danglingJailRef is a jail:NAME in a rule or in an address set whose jail does not exist: it renders as an empty table
// and matches nobody.
type danglingJailRef struct {
	Project, Policy, Set string // Policy and Set: where the reference stands
	Rule                 int    // rule number in the policy, from 1; 0 for a set member
	Jail                 string
}

func (d danglingJailRef) String() string {
	if d.Policy != "" {
		return fmt.Sprintf("%s/policy %s rule %d names jail:%s", d.Project, d.Policy, d.Rule, d.Jail)
	}
	return fmt.Sprintf("%s/address set %s has the member jail:%s", d.Project, d.Set, d.Jail)
}

// danglingJailRefs lists the references to jails that do not exist, in every project, or in one project when it is named.
func (s *Service) danglingJailRefs(project string) ([]danglingJailRef, error) {
	pols, err := s.polKind.listAll()
	if err != nil {
		return nil, err
	}
	sets, err := s.setKind.listAll()
	if err != nil {
		return nil, err
	}
	exists := map[string]bool{}
	s.mu.RLock()
	for _, e := range s.jails {
		exists[e.jail.Metadata.Project+"/"+e.jail.Metadata.Name] = true
	}
	s.mu.RUnlock()
	var out []danglingJailRef
	missing := func(proj, entry string) (string, bool) {
		n, ok := strings.CutPrefix(entry, "jail:")
		return n, ok && !exists[proj+"/"+n]
	}
	for _, o := range pols {
		if project != "" && o.Meta.Project != project {
			continue
		}
		for i, r := range o.Spec.Rules {
			for _, e := range append(append([]string{}, r.From...), r.To...) {
				if n, bad := missing(o.Meta.Project, e); bad {
					out = append(out, danglingJailRef{Project: o.Meta.Project, Policy: o.Meta.Name, Rule: i + 1, Jail: n})
				}
			}
		}
	}
	for _, o := range sets {
		if project != "" && o.Meta.Project != project {
			continue
		}
		for _, m := range o.Spec.Members() {
			if n, bad := missing(o.Meta.Project, m); bad {
				out = append(out, danglingJailRef{Project: o.Meta.Project, Set: o.Meta.Name, Jail: n})
			}
		}
	}
	return out, nil
}

// doctorJailRefs warns for every rule or address-set member that names a jail that does not exist.
func (s *Service) doctorJailRefs(c *checkSet) {
	refs, err := s.danglingJailRefs("")
	if err != nil {
		c.add("firewall.refs", api.DoctorUnknown, "the firewall rules cannot be read: "+err.Error(), "")
		return
	}
	if len(refs) == 0 {
		c.add("firewall.refs", api.DoctorOK, "every jail:NAME of the rules and the address sets names a jail", "")
		return
	}
	var bad []string
	for _, r := range refs {
		bad = append(bad, r.String())
	}
	c.add("firewall.refs", api.DoctorWarn, strings.Join(bad, "; ")+": a reference to a jail that does not exist matches nobody",
		"jailops firewall policy edit NAME, or address-set edit NAME, with the name of the jail (jailops list shows them), or make the jail")
}
