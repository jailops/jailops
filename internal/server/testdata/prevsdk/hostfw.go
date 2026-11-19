package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// HostFirewallClient is the firewall module: the host's /etc/pf.conf (extension "host_firewall").
type HostFirewallClient struct{ c *Client }

// Firewall returns the client of the firewall module.
func (h *HostClient) Firewall() *HostFirewallClient { return &HostFirewallClient{h.c} }

// Get reads the state of the host's firewall: what was applied, drift, whether pf runs and the rules in plain words
// (GET /api/v1/host/firewall).
func (f *HostFirewallClient) Get(ctx context.Context) (api.HostFirewall, error) {
	if err := f.c.need("host_firewall"); err != nil {
		return api.HostFirewall{}, err
	}
	v, _, err := get[api.HostFirewall](ctx, f.c, "/api/v1/host/firewall", nil)
	return v, err
}

// Apply renders and loads a firewall (PUT /api/v1/host/firewall, admin). A real change waits for its confirmation
// (the result's Pending): it reverts itself unless HostClient.Confirm is called in time.
func (f *HostFirewallClient) Apply(ctx context.Context, in api.HostFirewallApply) (api.HostChange, error) {
	if err := f.c.need("host_firewall"); err != nil {
		return api.HostChange{}, err
	}
	v, _, err := send[api.HostChange](ctx, f.c, request{method: http.MethodPut, path: "/api/v1/host/firewall", body: in})
	return v, err
}

// Pending says which host change waits for its confirmation, and how the last one ended (GET /api/v1/host/pending).
func (h *HostClient) Pending(ctx context.Context) (api.HostPending, error) {
	if err := h.c.need("host_pending"); err != nil {
		return api.HostPending{}, err
	}
	v, _, err := get[api.HostPending](ctx, h.c, "/api/v1/host/pending", nil)
	return v, err
}

// Confirm keeps the pending change (POST /api/v1/host/pending/confirm, admin). id "" means whichever is pending.
func (h *HostClient) Confirm(ctx context.Context, id string) (api.HostPending, error) {
	return h.pendingAction(ctx, "confirm", id)
}

// Revert puts the snapshot of the pending change back now (POST /api/v1/host/pending/revert, admin).
func (h *HostClient) Revert(ctx context.Context, id string) (api.HostPending, error) {
	return h.pendingAction(ctx, "revert", id)
}

func (h *HostClient) pendingAction(ctx context.Context, what, id string) (api.HostPending, error) {
	if err := h.c.need("host_pending"); err != nil {
		return api.HostPending{}, err
	}
	if err := api.ValidHostPendingID(id); err != nil {
		return api.HostPending{}, err
	}
	v, _, err := send[api.HostPending](ctx, h.c, request{method: http.MethodPost, path: "/api/v1/host/pending/" + what, body: api.HostPendingAction{ID: id}})
	return v, err
}
