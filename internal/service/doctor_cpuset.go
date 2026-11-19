package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// doctorCPUSet checks the CPUs that jails are pinned to (resources.cpuset) against the host's. A jail whose cpuset names a
// CPU the host does not have does not start (a profile written for a larger host does that when it reaches a smaller one).
// No jail with a cpuset adds no line.
func (s *Service) doctorCPUSet(ctx context.Context, c *checkSet) {
	const name = "jails.cpuset"
	s.mu.RLock()
	var jails []api.Jail
	for _, k := range sortedKeys(s.jails) {
		jails = append(jails, s.jails[k].jail)
	}
	s.mu.RUnlock()
	type pinned struct {
		jail string
		cpus []int
		set  string
	}
	var all []pinned
	for _, j := range jails {
		set := s.effective(j).Resources.CPUSet
		if set == "" {
			continue
		}
		cpus, err := api.ParseCPUSet(set)
		if err != nil {
			c.add(name, api.DoctorFail, fmt.Sprintf("%s/%s: resources.cpuset %q: %v", j.Metadata.Project, j.Metadata.Name, set, err), "jailops config set "+j.Metadata.Name+" resources.cpuset 2,3")
			continue
		}
		all = append(all, pinned{j.Metadata.Project + "/" + j.Metadata.Name, cpus, set})
	}
	if len(all) == 0 {
		return
	}
	v, err := s.sysctl(ctx, "hw.ncpu")
	n, perr := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || perr != nil || n < 1 {
		c.add(name, api.DoctorUnknown, "the number of CPUs of the host could not be read", "")
		return
	}
	var bad []string
	for _, p := range all {
		if top := p.cpus[len(p.cpus)-1]; top >= n {
			bad = append(bad, fmt.Sprintf("%s (%s names CPU %d)", p.jail, p.set, top))
		}
	}
	if len(bad) == 0 {
		c.add(name, api.DoctorOK, fmt.Sprintf("%d jail(s) are pinned to CPUs this host has (hw.ncpu=%d)", len(all), n), "")
		return
	}
	c.add(name, api.DoctorWarn, fmt.Sprintf("this host has %d CPUs (0-%d); these jails do not start: %s", n, n-1, strings.Join(bad, "; ")),
		"jailops config set JAIL resources.cpuset 0-"+strconv.Itoa(n-1)+" (or unset it) for a jail, or change the profile that sets it")
}
