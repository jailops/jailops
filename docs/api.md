# The API, for programs

JailOps is an API first: `jailops`, `jailops-sync` and the web console are clients of the same HTTP API, `/1.0/...`, JSON, with
operations for long work and an event stream.

| What | Where |
|---|---|
| The Go SDK (`jailops.net/jailops/sdk`): typed clients for jails, networks, storage, projects, images, firewall, backups, tokens | `sdk/` and its `doc.go` |
| The wire types (`sdk/api`) and the protocol, route by route, with error codes | [../sdk/PROTOCOL.md](../sdk/PROTOCOL.md) |
| An in-memory server with the same routes, for tests | `sdk/fake` |
| Error codes and what to do | `jailops explain CODE` |

There is no OpenAPI document today.

## Reaching it

- **Local**: the unix socket. Root (uid 0) is a global admin, the host's recovery access; every other local user is anonymous, like an unauthenticated remote client, and authenticates with a token or a login. The same identities, roles, scopes and audit as the network listener.
- **Network**: a TLS 1.3 listener you switch on, a certificate you choose, and pinned client certificates, tokens or logins. Every
  option, the recipes and what is not implemented are in [exposure.md](exposure.md).

    c, err := sdk.Connect(ctx, sdk.Remote("https://host.example.org:8443",
        sdk.WithPinnedFingerprint(fp),          // a self-signed certificate; leave it out for a CA-signed one
        sdk.WithClientCert(cert)))              // pinned mTLS; or sdk.WithToken(token)

Every request about something a project owns names the project (`?project=`, `sdk.WithProject`); there is no default project.
The SDK refuses plain `http://` to anything but loopback.

Declarative management of everything the API can create is `jailops-sync` ([fleet-sync.md](fleet-sync.md)).
