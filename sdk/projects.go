package sdk

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// ProjectClient accesses /1.0/projects.
type ProjectClient struct {
	*Resource[api.Project]
}

// Projects returns the projects client. Projects are host-level objects: the
// request's project parameter is not needed.
func (c *Client) Projects() *ProjectClient {
	return &ProjectClient{newResource[api.Project](c, "/1.0/projects")}
}

// Aggregate asks a hub for the definition of a fleet project and its state on
// every host it lists (GET /1.0/projects/<name>?host=all).
func (p *ProjectClient) Aggregate(ctx context.Context, name string) (api.ProjectAggregate, error) {
	v, _, err := get[api.ProjectAggregate](ctx, p.c, p.item(name), url.Values{"host": {"all"}})
	return v, err
}

// Storage accesses /1.0/storage.
func (c *Client) Storage() *Resource[api.Storage] {
	return newResource[api.Storage](c, "/1.0/storage")
}

// TrustClient accesses the trust workflow.
type TrustClient struct{ c *Client }

// Trust returns the trust client.
func (c *Client) Trust() *TrustClient { return &TrustClient{c} }

// CreateToken creates a single-use trust token (global admin). The token is
// returned once.
func (t *TrustClient) CreateToken(ctx context.Context, req api.TrustTokenRequest) (api.TrustToken, error) {
	out, _, err := send[api.TrustToken](ctx, t.c, request{method: http.MethodPost, path: "/1.0/trust/tokens", body: req})
	return out, err
}

// SessionClient accesses /1.0/sessions.
type SessionClient struct{ c *Client }

// Sessions returns the sessions client.
func (c *Client) Sessions() *SessionClient { return &SessionClient{c} }

// List iterates the caller's sessions (all of them for a global admin).
func (s *SessionClient) List(ctx context.Context) iter.Seq2[api.Session, error] {
	return list[api.Session](ctx, s.c, "/1.0/sessions", nil)
}

// Revoke ends a session; it takes effect on the session's next request.
func (s *SessionClient) Revoke(ctx context.Context, id string) error {
	_, _, err := send[None](ctx, s.c, request{method: http.MethodDelete, path: "/1.0/sessions/" + esc(id)})
	return err
}

// Whoami returns the caller as the server sees it (GET /1.0/auth/session).
func (c *Client) Whoami(ctx context.Context) (api.Identity, error) {
	v, _, err := get[api.Identity](ctx, c, "/1.0/auth/session", nil)
	return v, err
}

// TrustTokenPrefix starts every trust token.
const TrustTokenPrefix = "joc_"

// TrustTokenFingerprint reads the server certificate fingerprint carried by a
// trust token. It does not verify the token: only the server can.
func TrustTokenFingerprint(token string) (string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), TrustTokenPrefix)
	if !ok {
		return "", errors.New("jailops: not a trust token")
	}
	payload, _, ok := strings.Cut(rest, ".")
	if !ok {
		return "", errors.New("jailops: malformed trust token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("jailops: malformed trust token")
	}
	var c struct {
		Version int    `json:"v"`
		FP      string `json:"fp"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Version != 1 || c.FP == "" {
		return "", errors.New("jailops: malformed trust token")
	}
	return c.FP, nil
}

// RedeemResult is the outcome of RedeemTrustToken.
type RedeemResult struct {
	// ServerFingerprint is the pin to use for later connections.
	ServerFingerprint string
	// Certificate is the trust entry the server stored.
	Certificate api.Certificate
}

// RedeemTrustToken presents the client certificate to the server at
// serverURL with a trust token. The server is pinned to the fingerprint the
// token carries before anything secret is sent.
func RedeemTrustToken(ctx context.Context, serverURL, token string, cert tls.Certificate, opts ...Option) (RedeemResult, error) {
	var res RedeemResult
	fp, err := TrustTokenFingerprint(token)
	if err != nil {
		return res, err
	}
	if len(cert.Certificate) == 0 {
		return res, errors.New("jailops: client certificate is empty")
	}
	all := append([]Option{WithPinnedFingerprint(fp), WithRetries(0)}, opts...)
	all = append(all, WithPinnedFingerprint(fp))
	c, err := Connect(ctx, Remote(serverURL, all...))
	if err != nil {
		return res, err
	}
	defer c.Close()
	body := api.TrustClientRequest{Token: token,
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))}
	out, _, err := send[api.Certificate](ctx, c, request{method: http.MethodPost, path: "/1.0/trust/clients", body: body})
	if err != nil {
		return res, err
	}
	res.ServerFingerprint, res.Certificate = normalizeFP(fp), out
	return res, nil
}

// Profiles accesses /1.0/profiles of the client's project (?project=host
// selects the host-level profiles).
func (c *Client) Profiles() *Resource[api.Profile] {
	return newResource[api.Profile](c, "/1.0/profiles")
}

// ConfigKeys returns the registry of configuration keys of the server.
func (c *Client) ConfigKeys(ctx context.Context) ([]api.ConfigKey, error) {
	out, _, err := get[[]api.ConfigKey](ctx, c, "/1.0/config-keys", nil)
	return out, err
}
