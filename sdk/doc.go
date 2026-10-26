// Package sdk is the Go client of the JailOps management API. It uses the
// standard library only and shares its wire types with the server through
// package api.
//
//	c, err := sdk.Connect(ctx, sdk.Local())                          // unix socket
//	c, err := sdk.Connect(ctx, sdk.Remote(url, sdk.WithToken(tok)))  // https
//	h := c.Host("host-b")                                            // through a hub
//	op, err := h.Jails().Create(ctx, sdk.NewJail("web", spec))
//	j, err := op.Wait(ctx)
//	for j, err := range h.Jails().List(ctx, sdk.Label("env", "prod")) { ... }
//	for ev, err := range c.Events(ctx, sdk.Types(api.EventLifecycle)) { ... }
//	err = sdk.Modify(ctx, h.Jails(), "web", func(j *api.Jail) { ... })
//
// # Wire contract
//
// The full contract is in PROTOCOL.md next to this file. In short:
//
//   - Base path /1.0. Every reply is an api.Response envelope of type sync,
//     async (HTTP 202, operation URL, the operation in metadata) or error.
//   - Lists return metadata as a JSON array of full objects; filters are
//     ?label=k=v (repeatable). Items live at /1.0/<resource>/<name>. GET
//     returns an ETag (the quoted generation); PUT replaces and takes
//     If-Match; PATCH applies a JSON merge patch.
//   - Hub forwarding uses the query parameter ?host=<uuid-or-name> on any
//     path, including /1.0/events.
//   - Authentication is "Authorization: Bearer <token>", mTLS, or the unix
//     socket's file permissions.
//
// # Behaviour
//
//   - Errors are *api.Error: errors.As, errors.Is(err, sdk.ErrUnsupported)
//     and api.IsCode work. A reply without a JSON body is mapped from its
//     HTTP status.
//   - Only GET, HEAD and OPTIONS are retried (transport errors, 429, 502,
//     503, 504) with jittered exponential backoff. Writes are never retried;
//     Modify re-reads and re-applies after a 412 instead.
//   - Response bodies are bounded (WithMaxResponseSize). A request carries
//     X-Request-ID and a User-Agent; the id is added to error details.
//   - Mutating calls return an *Operation whether the server answered sync or
//     async, so callers always write op.Wait.
//   - TLS: certificates are verified. WithPinnedFingerprint trusts exactly one
//     leaf certificate by its SHA-256 (the way a host with a self-signed
//     certificate is trusted); it is the only case in which the CA check is
//     replaced, and the pin is then mandatory. Plain http is accepted for
//     loopback addresses only.
//   - There is no global state. Close cancels requests and event streams.
//
// Package sdk/fake serves the same contract in memory for tests.
package sdk
