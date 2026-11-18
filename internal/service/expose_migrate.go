package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// applyMigratedForwards turns the port forwards that the state store took out of policies (redirect rules) and profiles
// (proxy keys) when it loaded them into entries of the exposure of the jails that used them, so the same ports stay forwarded.
// It runs once at start, after the jails are known. The store keeps the forwards on disk until every one is written to its
// jail, so a stop or a failed write loses none: the failure stops the start, loudly, and the next start does it again.
func (s *Service) applyMigratedForwards(ctx context.Context) error {
	if s.d.State == nil {
		return nil
	}
	fws := s.d.State.TakeMigratedForwards()
	if len(fws) == 0 {
		return nil
	}
	if err := s.applyForwards(ctx, fws); err != nil {
		return err
	}
	return s.d.State.ForwardsApplied()
}

// skipForward says, in the log and in an event, that a stored forward was not made an exposure, and why.
func (s *Service) skipForward(ctx context.Context, f state.MigratedForward, jail, why string) {
	s.log.Warn("stored port forward not migrated", "jail", jail, "forward", migratedComment(f), "host_port", f.HostPort, "jail_port", f.JailPort, "why", why)
	s.emitIn(ctx, f.Project, "forward-migrate-skipped", "/api/v1/jails/"+jail)
}

// externalFrom keeps the sources of a redirect that can arrive from outside; a jail, a tag and the host never come in on the uplink.
func externalFrom(from []string) (keep []string, dropped bool) {
	for _, x := range from {
		if k, _, err := validate.ParseEndpoint(x); err == nil && (k == validate.EndpointAny || k == validate.EndpointAddr || k == validate.EndpointSet) {
			keep = append(keep, x)
		} else {
			dropped = true
		}
	}
	return keep, dropped
}

// applyForwards gives each forward to the jails of its project that used the policy or the profile it came from. A forward
// whose host port another jail already takes, or whose protocol and port the jail exposes with another host port, goes to
// the first jail only and is reported for the rest; a rule that was switched off, or limited to sources that never arrive
// from outside, is reported and not made.
func (s *Service) applyForwards(ctx context.Context, fws []state.MigratedForward) error {
	s.mu.RLock()
	var keys []string
	for k := range s.jails {
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	slices.Sort(keys)
	for _, f := range fws {
		for _, key := range keys {
			j, err := s.jailSnapshot(key)
			if err != nil || j.Metadata.Project != f.Project || !usesMigrated(s, j, f) {
				continue
			}
			if f.Disabled {
				s.skipForward(ctx, f, j.Metadata.Name, "the rule was switched off and forwarded nothing")
				continue
			}
			from := f.From
			if len(from) == 1 && from[0] == "any" {
				from = nil
			}
			if len(from) > 0 {
				keep, dropped := externalFrom(from)
				if len(keep) == 0 {
					s.skipForward(ctx, f, j.Metadata.Name, "its sources are jails, tags or the host, which never come in on the uplink")
					continue
				}
				if dropped {
					s.log.Warn("stored port forward: sources that never come in on the uplink are left out", "jail", key, "forward", migratedComment(f))
				}
				from = keep
			}
			e := api.ExposeEntry{Proto: f.Proto, Ports: []string{strconv.Itoa(f.JailPort)}, Families: []string{"4"},
				From: from, Listen: f.Listen, Comment: migratedComment(f)}
			if e.Proto == "" {
				e.Proto = "tcp"
			}
			if f.HostPort != f.JailPort {
				e.HostPort = f.HostPort
			}
			// the whole forward: the same one already there (an earlier start, or a policy used twice) is not made again
			if slices.ContainsFunc(j.Spec.Expose, func(x api.ExposeEntry) bool {
				return exposeKey(x) == exposeKey(e) && x.HostPort == e.HostPort && x.Listen == e.Listen && slices.Equal(x.From, e.From) && exposesFamily(x, "4")
			}) {
				continue
			}
			if slices.ContainsFunc(j.Spec.Expose, func(x api.ExposeEntry) bool { return exposeKey(x) == exposeKey(e) }) {
				s.skipForward(ctx, f, j.Metadata.Name, "the jail already exposes that port with another host port or other sources")
				continue
			}
			j.Spec.Expose = append(j.Spec.Expose, e)
			if err := s.checkForwardClash(key, s.effective(j)); err != nil {
				s.skipForward(ctx, f, j.Metadata.Name, err.Error())
				continue
			}
			if err := s.writeJailRecord(ctx, j); err != nil {
				return fmt.Errorf("store the migrated port forward %q of jail %s: %w", migratedComment(f), key, err)
			}
			s.mu.Lock()
			if ent, ok := s.jails[key]; ok {
				ent.jail = j
			}
			s.mu.Unlock()
			s.emitIn(ctx, f.Project, "forward-migrated", "/api/v1/jails/"+j.Metadata.Name)
			s.log.Info("port forward migrated to the exposure of the jail", "jail", key, "forward", migratedComment(f))
		}
	}
	return nil
}

func migratedComment(f state.MigratedForward) string {
	if f.Profile != "" {
		return "migrated from proxy " + f.Device + " of profile " + f.Profile
	}
	return "migrated from redirect rule " + strconv.Itoa(f.Rule) + " of policy " + f.Policy
}

// usesMigrated reports whether the jail used the policy or profile the forward came from.
func usesMigrated(s *Service, j api.Jail, f state.MigratedForward) bool {
	if f.Profile != "" {
		return slices.Contains(j.Spec.Profiles, f.Profile)
	}
	if slices.Contains(j.Spec.Firewall.Policies, f.Policy) {
		return true
	}
	if pr, err := s.projKind.get(f.Project); err == nil {
		return slices.Contains(pr.Spec.FirewallPolicies, f.Policy)
	}
	return false
}
