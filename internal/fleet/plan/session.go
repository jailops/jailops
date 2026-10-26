package plan

import (
	"context"
	"fmt"
	"sync"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/sdk"
)

// Session binds a fleet to its connections for one command run.
type Session struct {
	Fleet *config.Fleet
	Conn  Connector
	Store *secrets.Store

	mu      sync.Mutex
	clients map[string]*sdk.Client
}

// NewSession creates a session.
func NewSession(f *config.Fleet, conn Connector, store *secrets.Store) *Session {
	return &Session{Fleet: f, Conn: conn, Store: store, clients: map[string]*sdk.Client{}}
}

// Client returns the (cached) client of a host.
func (s *Session) Client(ctx context.Context, host string) (*sdk.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[host]; c != nil {
		return c, nil
	}
	h, ok := s.Fleet.Hosts[host]
	if !ok {
		return nil, fmt.Errorf("unknown host %q", host)
	}
	c, err := s.Conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	s.clients[host] = c
	return c, nil
}

// Close releases every connection.
func (s *Session) Close() { s.Conn.Close() }
