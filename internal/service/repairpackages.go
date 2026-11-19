package service

import (
	"context"
	"fmt"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// setBasePackages records how the pass over the base package records ended at a start: the BasePackages condition
// of the jail says why they are not in step, until a pass succeeds.
func (s *Service) setBasePackages(_ context.Context, key string, err error) {
	msg := ""
	if err != nil {
		msg = "the base package records could not be brought in step with the image: " + err.Error()
	}
	s.mu.Lock()
	rt := s.status[key]
	rt.basePackages = msg
	s.status[key] = rt
	s.mu.Unlock()
}

// RepairPackagesJail brings the base package records of a stopped thin jail in step with the image
// generation of its root: the pass a start makes, asked for at once. The jail is stopped (the helper opens its package
// database as root, which it does only in a root no jail runs in). Only the database changes.
func (s *Service) RepairPackagesJail(ctx context.Context, name string, _ api.RepairPackagesRequest) (api.Operation, error) {
	if err := s.requireHost("jails"); err != nil {
		return api.Operation{}, err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return api.Operation{}, err
	}
	if j.Spec.OS != api.OSFreeBSD {
		return api.Operation{}, invalid("jail %q is a %s jail: the base package records are those of a FreeBSD jail", name, j.Spec.OS)
	}
	if s.jailRunning(ctx, key) {
		return api.Operation{}, api.Errorf(api.CodeBusy, "jail %q is running; its base package records are repaired while it is stopped, and by every start", name).
			WithHint("restart it: jailops restart " + name)
	}
	return s.Submit(ctx, OpSpec{Action: api.OpJailRepairPackages, Description: "Repair the base package records of jail " + name, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			res, err := s.life.RepairBasePackages(ctx, key)
			s.setBasePackages(ctx, key, err)
			if err != nil {
				return nil, err
			}
			s.emitIn(ctx, p.Meta.Name, "jail-base-packages-repaired", "/api/v1/jails/"+name)
			return api.RepairPackagesResult{Jail: name, Base: res.Base, Replaced: res.Replaced, Added: res.Added}, nil
		}})
}

// doctorBasePackages reports the thin jails whose last pass over the base package records failed.
func (s *Service) doctorBasePackages(c *checkSet) {
	const name = "jails.base-packages"
	var out []string
	s.mu.RLock()
	for _, k := range sortedKeys(s.jails) {
		if s.status[k].basePackages != "" {
			j := s.jails[k].jail
			out = append(out, j.Metadata.Project+"/"+j.Metadata.Name)
		}
	}
	s.mu.RUnlock()
	if len(out) == 0 {
		c.add(name, api.DoctorOK, "the base package records of every jail are in step with its image", "")
		return
	}
	c.add(name, api.DoctorWarn, fmt.Sprintf("%d jail(s) have base package records that are not in step with their image: %s", len(out), strings.Join(out, ", ")),
		"run jailops jail repair-packages NAME; the BasePackages condition of the jail says what failed")
}
