package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// TokenClient accesses /1.0/tokens. Create returns the secret once, in
// Token.Secret; it is never returned again.
type TokenClient struct {
	*Resource[api.Token]
}

// Tokens returns the tokens client.
func (c *Client) Tokens() *TokenClient {
	return &TokenClient{newResource[api.Token](c, "/1.0/tokens")}
}

// Certificates accesses /1.0/certificates.
func (c *Client) Certificates() *Resource[api.Certificate] {
	return newResource[api.Certificate](c, "/1.0/certificates")
}

// Remotes accesses /1.0/remotes.
func (c *Client) Remotes() *Resource[api.Remote] {
	return newResource[api.Remote](c, "/1.0/remotes")
}

// UserClient accesses /1.0/users.
type UserClient struct {
	*Resource[api.User]
}

// Users returns the users client.
func (c *Client) Users() *UserClient {
	return &UserClient{newResource[api.User](c, "/1.0/users")}
}

// Create creates a user. With an empty password the server generates one
// and returns it once in UserCreated.Password.
func (u *UserClient) Create(ctx context.Context, req api.UserCreate) (api.UserCreated, error) {
	out, _, err := send[api.UserCreated](ctx, u.c, request{method: http.MethodPost, path: u.path, body: req})
	return out, err
}

// SetPassword replaces a user's password (a global administrator's recovery action). With an empty password the
// server generates one and returns it once.
func (u *UserClient) SetPassword(ctx context.Context, name, password string) (api.PasswordSet, error) {
	out, _, err := send[api.PasswordSet](ctx, u.c, request{method: http.MethodPost, path: u.path + "/" + esc(name) + "/password", body: api.PasswordSet{Password: password}})
	return out, err
}
