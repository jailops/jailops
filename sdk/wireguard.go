package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// WireGuardClient accesses /api/v1/wireguard.
type WireGuardClient struct {
	*Resource[api.WireGuard]
}

// WireGuard returns the WireGuard client.
func (c *Client) WireGuard() *WireGuardClient {
	return &WireGuardClient{newResource[api.WireGuard](c, "/api/v1/wireguard")}
}

// AddPeer enrolls a peer of a server. The answer holds the peer's join token and configuration: secrets that
// are returned once.
func (w *WireGuardClient) AddPeer(ctx context.Context, server string, q api.WireGuardPeerAdd) (api.WireGuardPeerCreated, error) {
	out, _, err := send[api.WireGuardPeerCreated](ctx, w.c, request{method: http.MethodPost, path: w.item(server) + "/peers", body: q})
	return out, err
}

// RemovePeer removes a peer of a server.
func (w *WireGuardClient) RemovePeer(ctx context.Context, server, peer string) (api.WireGuard, error) {
	out, _, err := send[api.WireGuard](ctx, w.c, request{method: http.MethodDelete, path: w.item(server) + "/peers/" + esc(peer)})
	return out, err
}

// Join makes a client tunnel from a join token.
func (w *WireGuardClient) Join(ctx context.Context, q api.WireGuardJoin) (api.WireGuard, error) {
	out, _, err := send[api.WireGuard](ctx, w.c, request{method: http.MethodPost, path: w.path + "/join", body: q})
	return out, err
}

// Up brings a tunnel up; Down takes it down (the keys stay).
func (w *WireGuardClient) Up(ctx context.Context, name string) (api.WireGuard, error) {
	out, _, err := send[api.WireGuard](ctx, w.c, request{method: http.MethodPost, path: w.item(name) + "/up"})
	return out, err
}

// Down takes a tunnel down.
func (w *WireGuardClient) Down(ctx context.Context, name string) (api.WireGuard, error) {
	out, _, err := send[api.WireGuard](ctx, w.c, request{method: http.MethodPost, path: w.item(name) + "/down"})
	return out, err
}

// RotateKeys gives a tunnel a new key pair and returns the new public key.
func (w *WireGuardClient) RotateKeys(ctx context.Context, name string) (api.WireGuardKeys, error) {
	out, _, err := send[api.WireGuardKeys](ctx, w.c, request{method: http.MethodPost, path: w.item(name) + "/rotate-keys"})
	return out, err
}
