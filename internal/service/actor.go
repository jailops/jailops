package service

import (
	"context"
	"slices"

	"jailops.net/jailops/sdk/api"
)

// Actor identifies the caller for authorization, audit and event purposes.
type Actor struct {
	Name string
	// Kind is "user", "token", "certificate", "hub", "unix" or "system".
	Kind   string
	Perm   api.Permission
	Remote string
	// Projects and Global are the scope of the caller. An actor with neither
	// can do nothing: an empty Projects list never means every project.
	Projects []string
	Global   bool
	// SessionRef identifies the web session the request came with.
	SessionRef string
}

// HasScope reports whether the actor may act in any project.
func (a Actor) HasScope() bool { return a.Global || len(a.Projects) > 0 }

// CanProject reports whether the actor may use the project.
func (a Actor) CanProject(p string) bool { return a.Global || slices.Contains(a.Projects, p) }

type actorKey struct{}
type projectKey struct{}

// WithActor attaches the caller to ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the caller, or the system actor (a global admin).
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Name: "jailopsd", Kind: "system", Perm: api.PermAdmin, Global: true}
}

// ExplicitActorFrom returns the caller only when a request attached one (the
// system actor ActorFrom falls back to does not count).
func ExplicitActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// WithProject attaches the project a request selected (?project=) to ctx.
func WithProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, projectKey{}, project)
}

// ProjectFrom returns the project the request named, "" when it named none.
func ProjectFrom(ctx context.Context) string {
	p, _ := ctx.Value(projectKey{}).(string)
	return p
}

// ErrProjectRequired is the answer to a request that needs a project and names none. There is no default project.
func ErrProjectRequired() *api.Error {
	return api.Errorf(api.CodeInvalid, "a project is required").
		WithHint("name one with --project NAME (list them with 'jailops project list')")
}

// requestProject is the project a request names; a request that names none is refused.
func requestProject(ctx context.Context) (string, error) {
	if p := ProjectFrom(ctx); p != "" {
		return p, nil
	}
	return "", ErrProjectRequired()
}

// requireGlobal is the service-layer check of host-level writes: only a
// global identity may change networks, storage, images, projects, users,
// certificates, tokens and remotes. The role is checked by the permission
// table; this checks the scope.
func requireGlobal(ctx context.Context, what string) error {
	if !ActorFrom(ctx).Global {
		return api.Errorf(api.CodeForbidden, "%s needs a global administrator", what)
	}
	return nil
}
