package server

import (
	"context"

	"jailops.net/jailops/internal/auth"
)

// WithPeerCred makes a request context look as if it came over the unix socket from the given peer, so tests can
// present uid 0 without running as root.
func WithPeerCred(ctx context.Context, c auth.Cred) context.Context {
	return context.WithValue(ctx, peerKey{}, c)
}
