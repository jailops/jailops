package sdk

import (
	"context"
	"net/url"

	"jailops.net/jailops/sdk/api"
)

// Reach says what reaches a jail, what it reaches and the rules of its anchor in the order pf decides
// (GET /api/v1/jails/{name}/reach, extension jail_reach).
func (j *JailClient) Reach(ctx context.Context, name string) (api.JailReach, error) {
	if err := j.c.need("jail_reach"); err != nil {
		return api.JailReach{}, err
	}
	v, _, err := get[api.JailReach](ctx, j.c, j.r.item(name)+"/reach", nil)
	return v, err
}

// Exposed lists everything the Internet reaches across the jails the identity can see (GET /api/v1/exposed, extension
// jail_reach). With tunnels it also lists what WireGuard peers reach.
func (f *FirewallClient) Exposed(ctx context.Context, tunnels bool) (api.Exposed, error) {
	if err := f.c.need("jail_reach"); err != nil {
		return api.Exposed{}, err
	}
	q := url.Values{}
	if tunnels {
		q.Set("tunnels", "true")
	}
	v, _, err := get[api.Exposed](ctx, f.c, "/api/v1/exposed", q)
	return v, err
}
