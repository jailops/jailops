package priv

import (
	"context"
	"slices"

	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// Admin is the assertion jailopsd attaches to a request that acts for an
// authenticated API user with the admin role. The helper trusts the assertion
// only as far as AdminPolicy allows: it unlocks items on an allow-list, never
// arbitrary behaviour. User is recorded in the audit log.
type Admin struct {
	User string `json:"user"`
}

type adminKey struct{}

// WithAdmin returns a context whose requests carry the admin assertion.
func WithAdmin(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, adminKey{}, &Admin{User: user})
}

// AdminFrom returns the assertion in ctx, or nil.
//
// A request made for an authenticated caller that holds the admin permission
// without a project limit (service.Actor: Global and PermAdmin) carries the
// assertion implicitly; nothing else does.
func AdminFrom(ctx context.Context) *Admin {
	if a, _ := ctx.Value(adminKey{}).(*Admin); a != nil {
		return a
	}
	if a, ok := service.ExplicitActorFrom(ctx); ok && a.Global && a.Perm == api.PermAdmin && a.Name != "" {
		return &Admin{User: a.Name}
	}
	return nil
}

func isAdmin(ctx context.Context) bool {
	a := AdminFrom(ctx)
	return a != nil && a.User != ""
}

func hasStr(list []string, s string) bool { return slices.Contains(list, s) }
