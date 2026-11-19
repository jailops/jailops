package api

import "time"

// First-run setup (extension setup; docs/appliance.md). A host without an administrator accepts one request that
// creates the first one: POST /api/v1/setup with the one-time setup token. The token is issued by root (the rc script
// of jailopsd with jailops_appliance, or `jailops-priv setup-token`) through POST /api/v1/setup/token, is valid for
// SetupTokenTTL and is consumed by the first administrator it creates. Once an administrator exists, every setup route
// answers not_found.

// SetupTokenTTL is how long a setup token is valid.
const SetupTokenTTL = 24 * time.Hour

// SetupStatus is GET /api/v1/setup, answered to anyone while no administrator exists.
type SetupStatus struct {
	// Required is true: the host has no administrator yet.
	Required bool `json:"required"`
	// TokenIssued is true while a setup token is valid; ExpiresAt is when it expires.
	TokenIssued bool       `json:"token_issued"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// SetupRequest is the body of POST /api/v1/setup: the setup token and the first administrator's name and password.
type SetupRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// SetupResult is the answer of POST /api/v1/setup.
type SetupResult struct {
	User User `json:"user"`
	// ClientAddress is the address the request came from as the daemon saw it ("" through the unix socket): the
	// firewall step of the wizard keeps ssh open to it.
	ClientAddress string `json:"client_address,omitempty"`
}

// SetupToken is the answer of POST /api/v1/setup/token (root only): the token, shown once, and where to use it.
type SetupToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// URLs are the addresses of the console (https://HOST:PORT/), empty when the daemon has no TLS listener.
	URLs []string `json:"urls"`
	// Hint says what to do when there is no console to open.
	Hint string `json:"hint,omitempty"`
}
