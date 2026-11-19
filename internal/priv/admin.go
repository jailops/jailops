package priv

import (
	"context"
	"slices"
	"strconv"

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

// storedForward is one IPv4 forward of the exposure that a jail's own stored record lists: the protocol, the host port
// and the jail's port.
type storedForward struct {
	proto    string
	hostPort int
	jailPort int
}

type storedForwardsKey struct{}

// withStoredForwards marks ctx as acting for the stored record of one jail: the forwards in the list were admitted when
// a global administrator stored the exposure (the API asks for one when the exposure has an IPv4 forward), so a load of
// the jail's anchor that has none besides them needs no assertion of its own. The start hook is the caller: jail(8)
// runs it as root, with no API caller behind it, whenever the jail starts, restarts or comes up at boot.
func withStoredForwards(ctx context.Context, f []storedForward) context.Context {
	return context.WithValue(ctx, storedForwardsKey{}, f)
}

// isStoredForward says whether ctx carries the forward: the protocol, the host port and the jail's port are all as the
// jail's stored exposure has them.
func isStoredForward(ctx context.Context, proto string, hostPort, jailPort int) bool {
	f, _ := ctx.Value(storedForwardsKey{}).([]storedForward)
	return slices.Contains(f, storedForward{proto: proto, hostPort: hostPort, jailPort: jailPort})
}

// storedForwards are the IPv4 forwards the stored exposure of a jail makes, as the service renders them: each single
// port of an entry that covers IPv4, at the host port the entry names or else at the jail's port (a range is let in,
// never forwarded).
func storedForwards(spec api.JailSpec) []storedForward {
	var out []storedForward
	for _, e := range spec.Expose {
		if len(e.Families) > 0 && !slices.Contains(e.Families, "4") {
			continue
		}
		for _, p := range e.Ports {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			host := n
			if e.HostPort > 0 {
				host = e.HostPort
			}
			out = append(out, storedForward{proto: e.Protocol(), hostPort: host, jailPort: n})
		}
	}
	return out
}
