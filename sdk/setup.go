package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// SetupStatus says whether the host still waits for its first administrator (GET /api/v1/setup, extension "setup").
// Anyone may ask; once an administrator exists the answer is api.CodeNotFound.
func (c *Client) SetupStatus(ctx context.Context) (api.SetupStatus, error) {
	v, _, err := get[api.SetupStatus](ctx, c, "/api/v1/setup", nil)
	return v, err
}

// Setup creates the first administrator with the one-time setup token (POST /api/v1/setup). It needs no identity:
// the token is the credential. Wrong tokens are counted and locked out like failed logins.
func (c *Client) Setup(ctx context.Context, in api.SetupRequest) (api.SetupResult, error) {
	v, _, err := send[api.SetupResult](ctx, c, request{method: http.MethodPost, path: "/api/v1/setup", body: in})
	return v, err
}

// SetupToken issues a new setup token, replacing the one before (POST /api/v1/setup/token): root on the host's
// socket, while no administrator exists. The token is in this answer only.
func (c *Client) SetupToken(ctx context.Context) (api.SetupToken, error) {
	v, _, err := send[api.SetupToken](ctx, c, request{method: http.MethodPost, path: "/api/v1/setup/token", body: struct{}{}})
	return v, err
}
