package sdk

import (
	"context"
	"net/url"

	"jailops.net/jailops/sdk/api"
)

// Top measures the running jails the identity may see and, for a global identity, the host itself (GET
// /api/v1/top, extension top). The server measures a frame when it is asked for and compares it with the frame it
// measured before, so the rates (network, host CPU) are present from the second call on; a caller that watches
// calls it again every interval. Label narrows the jails, and a client made with WithProject or Project narrows
// them to that project, unless AllProjects asks for every project the identity may use.
func (c *Client) Top(ctx context.Context, opts ...ListOption) (api.Top, error) {
	if err := c.need("top"); err != nil {
		return api.Top{}, err
	}
	q := url.Values{}
	for _, o := range opts {
		o(q)
	}
	v, _, err := get[api.Top](ctx, c, "/api/v1/top", q)
	return v, err
}

// AllProjects asks for the jails of every project the identity may use, whatever project the client is scoped to.
func AllProjects() ListOption { return func(q url.Values) { q.Set("all_projects", "true") } }
