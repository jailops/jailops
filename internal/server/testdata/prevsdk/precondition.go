package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type idemKey struct{}

// WithIdempotencyKey sets the idempotency key of the creating requests sent with ctx (a jail create, a restore). A
// request repeated with the same key and the same body returns the operation the first one started instead of
// starting another; the server remembers a key for 24 hours. Without a key the client makes a fresh one per call.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, idemKey{}, key)
}

// idempotencyHeader is the Idempotency-Key header of a creating call: the key of ctx, else a new random one.
func idempotencyHeader(ctx context.Context) http.Header {
	k, _ := ctx.Value(idemKey{}).(string)
	if k == "" {
		var b [16]byte
		_, _ = rand.Read(b[:])
		k = hex.EncodeToString(b[:])
	}
	return http.Header{"Idempotency-Key": {k}}
}
