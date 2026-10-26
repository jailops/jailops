package plan

import (
	"context"
	"fmt"
	"sync"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/internal/fleet/vault"
	"jailops.net/jailops/sdk"
)

// Connector opens an SDK client for a host of the fleet.
type Connector interface {
	Connect(ctx context.Context, h *config.Host) (*sdk.Client, error)
	Close()
}

// SDKConnector connects with the SDK: directly over https or the local
// socket, or through the fleet's hub. Tokens are decrypted from the secrets
// store only when a connection needs them.
type SDKConnector struct {
	Fleet *config.Fleet
	Store *secrets.Store
	// Getenv reads the SOPS key variables; nil means the process environment.
	Getenv func(string) string

	mu  sync.Mutex
	hub *sdk.Client
	all []*sdk.Client
}

func (c *SDKConnector) dial(ctx context.Context, e model.Endpoint, what string, extra ...sdk.Option) (*sdk.Client, error) {
	opts := append([]sdk.Option(nil), extra...)
	if e.TokenSecret != "" {
		tok, err := c.Store.Token(e.TokenSecret)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		opts = append(opts, sdk.WithToken(tok))
	}
	if e.Fingerprint != "" {
		opts = append(opts, sdk.WithPinnedFingerprint(e.Fingerprint))
	}
	opts = append(opts, sdk.WithUserAgent("jailops-sync"))
	var t sdk.Target
	switch {
	case e.URL != "":
		t = sdk.Remote(e.URL)
	case e.Socket != "":
		t = sdk.Local(sdk.WithSocket(e.Socket))
	default:
		t = sdk.Local()
	}
	cl, err := sdk.Connect(ctx, t, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	c.mu.Lock()
	c.all = append(c.all, cl)
	c.mu.Unlock()
	return cl, nil
}

// Connect implements Connector.
func (c *SDKConnector) Connect(ctx context.Context, h *config.Host) (*sdk.Client, error) {
	if !h.Spec.ViaHub {
		e := h.Spec.Endpoint
		// A remote file gives the host a client certificate: the address and the
		// pinned server fingerprint come from it, and the certificate is presented
		// over mTLS. It is decrypted here, in memory, for this connection only.
		if c.Fleet.Remotes[h.Name] != nil {
			r, err := vault.LoadRemote(c.Fleet.Path, h.Name, c.Getenv)
			if err != nil {
				return nil, fmt.Errorf("host %s: %w", h.Name, err)
			}
			cert, err := r.TLSCertificate()
			if err != nil {
				return nil, fmt.Errorf("host %s: %w", h.Name, err)
			}
			e.URL, e.Fingerprint = r.URL, r.ServerFingerprint
			return c.dial(ctx, e, "host "+h.Name, sdk.WithClientCert(cert))
		}
		return c.dial(ctx, e, "host "+h.Name)
	}
	c.mu.Lock()
	hub := c.hub
	c.mu.Unlock()
	if hub == nil {
		if c.Fleet.Meta.Hub == nil {
			return nil, fmt.Errorf("host %s: via_hub needs a hub in %s", h.Name, config.FleetFile)
		}
		var err error
		if hub, err = c.dial(ctx, *c.Fleet.Meta.Hub, "hub"); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.hub = hub
		c.mu.Unlock()
	}
	cl := hub.Host(h.Name)
	if _, err := cl.Info(ctx); err != nil {
		return nil, fmt.Errorf("host %s via hub: %w", h.Name, err)
	}
	return cl, nil
}

// Close closes every connection.
func (c *SDKConnector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.all {
		_ = cl.Close()
	}
	c.all, c.hub = nil, nil
}
