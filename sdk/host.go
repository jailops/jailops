package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// HostClient manages the host itself (appliance mode, /api/v1/host). Nothing changes on the host unless the
// administrator enabled the module in jailopsd.yaml and in the helper's priv.conf; otherwise a change answers
// api.CodeNotManaged with the lines to add. Reading works always.
type HostClient struct{ c *Client }

// HostSystem returns the host management client. (Host, the older method, scopes a hub client to one host.)
func (c *Client) HostSystem() *HostClient { return &HostClient{c} }

// IdentityClient is the identity module: host name, resolvers and NTP (extension "host_identity").
type IdentityClient struct{ c *Client }

// Identity returns the client of the identity module.
func (h *HostClient) Identity() *IdentityClient { return &IdentityClient{h.c} }

// Get reads the host name, /etc/resolv.conf, the NTP servers JailOps manages and whether ntpd is enabled, with the
// management state and drift (GET /api/v1/host/identity).
func (i *IdentityClient) Get(ctx context.Context) (api.HostIdentity, error) {
	if err := i.c.need("host_identity"); err != nil {
		return api.HostIdentity{}, err
	}
	v, _, err := get[api.HostIdentity](ctx, i.c, "/api/v1/host/identity", nil)
	return v, err
}

// Apply changes the parts of spec that are given (PUT /api/v1/host/identity, admin). With dryRun the result holds
// the diff and the actions and nothing changes; otherwise it names the snapshot taken first.
func (i *IdentityClient) Apply(ctx context.Context, spec api.HostIdentitySpec, dryRun bool) (api.HostChange, error) {
	if err := i.c.need("host_identity"); err != nil {
		return api.HostChange{}, err
	}
	v, _, err := send[api.HostChange](ctx, i.c, request{method: http.MethodPut, path: "/api/v1/host/identity", body: api.HostIdentityApply{Spec: spec, DryRun: dryRun}})
	return v, err
}

// Snapshots lists the copies of host files taken before each change, newest first (GET /api/v1/host/snapshots).
func (h *HostClient) Snapshots(ctx context.Context) ([]api.HostSnapshot, error) {
	if err := h.c.need("host_identity"); err != nil {
		return nil, err
	}
	v, _, err := get[[]api.HostSnapshot](ctx, h.c, "/api/v1/host/snapshots", nil)
	return v, err
}

// Restore puts a snapshot back (name or api.HostSnapshotLatest; admin). The result names the snapshot of what it
// replaced, so a restore can be undone too.
func (h *HostClient) Restore(ctx context.Context, name string, dryRun bool) (api.HostChange, error) {
	if err := h.c.need("host_identity"); err != nil {
		return api.HostChange{}, err
	}
	v, _, err := send[api.HostChange](ctx, h.c, request{method: http.MethodPost, path: "/api/v1/host/snapshots/" + esc(name) + "/restore", body: api.HostRestore{DryRun: dryRun}})
	return v, err
}
