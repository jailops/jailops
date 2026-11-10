package service

import (
	"context"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// SetRenameCrash makes the next renames stop at step n as if the daemon died there (0 never).
func SetRenameCrash(s *Service, n int) { s.renameCrashAt = n }

// SetPolicyRaw stores the spec of a firewall policy as it is, without the validation of the API: what a policy that was
// stored without its defaults looks like.
func SetPolicyRaw(s *Service, project, name string, spec api.FirewallPolicySpec) error {
	_, err := s.polKind.p(project).update(context.Background(), name, AnyGeneration, nil, nil, spec)
	return err
}

// RenderJail renders the anchor of a jail the way the reconcile loop and the start hooks do.
func RenderJail(s *Service, project, name string) error {
	key, err := validate.QualifiedName(project, name)
	if err != nil {
		return err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return err
	}
	sets, err := s.allSetsWithJails(project)
	if err != nil {
		return err
	}
	_, err = s.buildJail(context.Background(), j, sets)
	return err
}

// SetManagedPolicy stores a firewall policy that something else manages, as app access does for app-JAIL.
func SetManagedPolicy(s *Service, project, name, managedBy string, spec api.FirewallPolicySpec) error {
	_, err := s.polKind.p(project).create(context.Background(), api.ObjectMeta{Name: name, ManagedBy: managedBy}, spec)
	return err
}
