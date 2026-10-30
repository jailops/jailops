# The API, for programs

JailOps is an API first: `jailops`, `jailops-sync` and the web console are clients of the same HTTP API, `/api/v1/...`, JSON, with
operations for long work (a jail create, an image build, an image upgrade that also moves the jails) and an event stream.

| What | Where |
|---|---|
| The Go SDK (`jailops.net/jailops/sdk`): typed clients for jails, networks, storage, projects, images, firewall, backups, tokens, the application catalog, its installation, app drafts from a running jail, and host management (`HostSystem()`) | `sdk/` and its `doc.go` |
| The Go SDK (`jailops.net/jailops/sdk`): typed clients for jails, networks, WireGuard tunnels, storage, projects, images, firewall, backups, tokens, the application catalog, its installation and app drafts from a running jail | `sdk/` and its `doc.go` |
| The Go SDK (`jailops.net/jailops/sdk`): typed clients for jails, networks, storage, projects, images, firewall, backups, tokens, the application catalog, its installation, app drafts from a running jail and who may connect to an installed app (`Apps().Access`, `Allow`, `Deny`) | `sdk/` and its `doc.go` |
| The wire types (`sdk/api`) and the protocol, route by route, with error codes | [../sdk/PROTOCOL.md](../sdk/PROTOCOL.md) |
| An in-memory server with the same routes, for tests | `sdk/fake` |
| Error codes and what to do | `jailops explain CODE` |

There is no OpenAPI document today.

**WireGuard** (extension `wireguard`, [wireguard.md](wireguard.md)): `GET|POST /api/v1/wireguard`,
`GET|PUT|PATCH|DELETE /api/v1/wireguard/{name}`, `POST /api/v1/wireguard/{name}/peers` (answers `WireGuardPeerCreated`
with the join `token` and the `config` file: secrets, returned once, never stored), `DELETE .../peers/{peer}`,
`POST /api/v1/wireguard/join` (`WireGuardJoin`: a client from a token), `POST .../up`, `.../down`, `.../rotate-keys`.
Reads need the operator role and never carry a private or preshared key; changes need a global admin. SDK:
`c.WireGuard()`. The extension `interface_descriptions` says the host describes its interfaces (`net.descriptions`).

## Reaching it

- **Local**: the unix socket. Root (uid 0) is a global admin, the host's recovery access; every other local user is anonymous, like an unauthenticated remote client, and authenticates with a token or a login. The same identities, roles, scopes and audit as the network listener.
- **Network**: a TLS 1.3 listener you switch on, a certificate you choose, and pinned client certificates, tokens or logins. Every
  option, the recipes and what is not implemented are in [exposure.md](exposure.md).

    c, err := sdk.Connect(ctx, sdk.Remote("https://host.example.org:39110",
        sdk.WithPinnedFingerprint(fp),          // a self-signed certificate; leave it out for a CA-signed one
        sdk.WithClientCert(cert)))              // pinned mTLS; or sdk.WithToken(token)

Every request about something a project owns names the project (`?project=`, `sdk.WithProject`); there is no default project.
The SDK refuses plain `http://` to anything but loopback.

Declarative management of everything the API can create is `jailops-sync` ([fleet-sync.md](fleet-sync.md)).

## Host management

The host itself (appliance mode, [appliance.md](appliance.md)) is a set of singleton objects under `/api/v1/host/...`, one
per module, off until the administrator enables the module in `jailopsd.yaml` and in `priv.conf`. Version 0.3 has one
module, `identity` (extension `host_identity`):

| Route | Role | What |
|---|---|---|
| `GET /api/v1/host/identity` | readonly, global | kind `HostIdentity`: the host name, `/etc/resolv.conf`, the NTP servers JailOps manages and `ntpd_enable` as the host has them (`spec`), and `status`: `managed`, `config_enabled`, `helper_enabled`, `drift`, `applied`, `ntp_sync`. Always readable |
| `PUT /api/v1/host/identity` | admin, global | body `{"spec": {...}, "dry_run": true}`; reply `HostChange`: `changed`, `diff` (unified), `actions`, `snapshot` |
| `GET /api/v1/host/snapshots` | readonly, global | the copies of host files kept before each change, newest first |
| `POST /api/v1/host/snapshots/{name}/restore` | admin, global | `{name}` or `latest`; body `{"dry_run": true}` optional; reply `HostChange` |

The spec:

    {"hostname": "web1.example.org",
     "resolvers": {"nameservers": ["192.0.2.53", "2001:db8::53"], "search": ["example.org"]},
     "ntp": {"servers": ["192.0.2.123", "ntp.example.org"], "enable": true}}

A part left out is not changed. `hostname` is one label or a fully qualified name (RFC 1123, at most 253 characters, not an
address); 1 to 3 `nameservers`, IPv4 or IPv6 literals without a zone; at most 6 `search` domains, 256 characters together;
at most 4 NTP `servers`, host names or literals; no duplicates anywhere. The daemon checks the spec and the privileged
helper checks it again. While the module is off a change answers `not_managed` (HTTP 403) with the lines to add in the
hint. The operator role is not enough: changing the host needs a global admin. Every `PUT` and `POST` is in the audit log,
dry runs too. In Go:

    id, err := c.HostSystem().Identity().Get(ctx)
    ch, err := c.HostSystem().Identity().Apply(ctx, api.HostIdentitySpec{Hostname: "web1.example.org"}, true) // dry run
    ch, err = c.HostSystem().Restore(ctx, api.HostSnapshotLatest, false)

(`c.Host(name)` is the older method that scopes a hub's client to one of its hosts.)
