package service

import (
	"context"
	"sort"

	"jailops.net/jailops/sdk/api"
)

// grantedTo lists, sorted, the projects whose spec holds a grant (granted says so) and that the caller may see: every
// project for a global identity, the projects of the caller for any other (the selected one when a project is
// selected). The wildcard grant of a storage counts as a grant.
func (s *Service) grantedTo(ctx context.Context, granted func(api.ProjectSpec) bool) []string {
	a := ActorFrom(ctx)
	var out []string
	if a.Global {
		objs, err := s.projKind.list()
		if err != nil {
			return nil
		}
		for _, o := range objs {
			if granted(o.Spec) {
				out = append(out, o.Meta.Name)
			}
		}
	} else {
		projects := a.Projects
		if p := ProjectFrom(ctx); p != "" {
			if !a.CanProject(p) {
				return nil
			}
			projects = []string{p}
		}
		for _, p := range projects {
			if o, err := s.projKind.get(p); err == nil && granted(o.Spec) {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// listScoped says whether a network or a storage belongs in a list: what the caller may see, and for a global identity
// with a project selected only what that project holds, unless the request asked for everything (all). A global
// identity without a project sees everything. visible is the check of a caller that is not global; granted is the
// grant of a project spec.
func (s *Service) listScoped(ctx context.Context, name string, visible func(string) bool, granted func(api.ProjectSpec, string) bool) bool {
	a := ActorFrom(ctx)
	if !a.Global {
		return visible(name)
	}
	p := ProjectFrom(ctx)
	if p == "" || AllProjectsFrom(ctx) {
		return true
	}
	o, err := s.projKind.get(p)
	return err == nil && granted(o.Spec, name)
}

// checkAll refuses a request for everything from an identity that is not global: it may see the projects it belongs to.
func checkAll(ctx context.Context) error {
	if AllProjectsFrom(ctx) && !ActorFrom(ctx).Global {
		return api.Errorf(api.CodeForbidden, "only a global administrator lists everything").
			WithHint("without --all the list holds what the selected project is granted")
	}
	return nil
}
