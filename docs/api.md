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

**Logs** ([logging.md](logging.md)): `GET /api/v1/logs` (extension `logs_unified`, operator) merges the end of one file
(`file`, default `messages`) of the jails of `?project=` (or of every project the caller may use with `all_projects=true`)
by time: `jail` (repeatable), `label=k=v` (repeatable), `app`, `app_logs=true`, `tail`, `since`, `grep`, and `cursor`,
the `next` map of the previous reply as JSON, to follow. Reply `LogsRead`: `lines` (`time`, `project`, `jail`, `app`,
`file`, `line`), `sources` (with `error` for a file that could not be read), `next`, `truncated`; at most 50 files and
2000 lines. Log forwarding (extension `log_forward`, global admin): `GET|PUT|DELETE /api/v1/logs/forward` (kind
`LogForward`: `spec.sinks` and `status` with `origin` config or api, `generation` as ETag and each sink's state; `PUT`
takes `LogForwardSpec` and `If-Match`, `DELETE` returns to `jailopsd.yaml`) and `POST /api/v1/logs/forward/test`
(`{"sink": NAME}`, one tagged line). SDK: `c.Logs()` (`Read`, `Forward`, `SetForward`, `ResetForward`, `TestForward`).
**Encrypted volumes** (extension `volume_unlock`, [storage.md](storage.md#encrypted-volumes)): a volume with `encrypted:
true` is created when `POST /api/v1/jails` carries `volume_passphrase` next to `metadata` and `spec` (`JailCreate`).
`POST /api/v1/jails/{name}/unlock` (`JailUnlock`: `passphrase`, `start`; operator), `POST .../lock` (operator) and
`POST .../passphrase` (`JailPassphraseChange`: `old_passphrase`, `new_passphrase`; admin) answer operations whose result
is the `Jail`. A locked jail has `status.power` `locked` and `status.locked` (the volume names); starting it answers
`locked` (409), a wrong passphrase fails the operation with `bad_passphrase` (403), five in a row make the jail answer
`rate_limited` (429) for 30 seconds. A passphrase is never in a reply. SDK: `Jails().CreateWithPassphrase`, `Unlock`,
`Lock`, `ChangePassphrase`; the fake serves them and records the bodies with the passphrases replaced.

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
per module, off until the administrator enables the module in `jailopsd.yaml` and in `priv.conf`. Two modules are built:
`identity` (extension `host_identity`) and `firewall` (below). Module `identity`:

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

### The host firewall and commit confirmed

Module `firewall` (extension `host_firewall`) owns `/etc/pf.conf` as a whole file, rendered by the privileged helper from
a spec; every change of it goes through **commit confirmed** (extension `host_pending`): it reverts itself at a deadline
unless it is confirmed. The timer is the helper's own; the daemon only reads it.

| Route | Role | What |
|---|---|---|
| `GET /api/v1/host/firewall` | readonly, global | kind `HostFirewall`: `spec` is what was applied last, or the default before any apply (a hand-written `pf.conf` is not read back into fields); `status`: `managed`, `config_enabled`, `helper_enabled`, `file_exists`, `file_managed`, `drift` (`pf.conf`, `pf_enable`, `pf_rules`), `applied`, `pf_enabled`, `rules` (in plain words), `warnings` (ssh open to the world), `pending`, `console_port`. Always readable |
| `PUT /api/v1/host/firewall` | admin, global | body `{"spec": {...}, "dry_run": true, "confirm_within": 120, "i_know": false}`; reply `HostChange` with `diff`, `rules`, `actions`, `warnings` and, after a real change, `snapshot` and `pending` |
| `GET /api/v1/host/pending` | readonly, global | `HostPending`: `pending` (`id`, `module`, `snapshot`, `created`, `deadline`, `seconds_left`, `confirm_within`) and `last`, how the last one ended (`confirmed`, `reverted`, `timed_out`, `reverted_at_boot`, `revert_failed`) |
| `POST /api/v1/host/pending/confirm` | admin, global | body `{"id": "..."}` optional; keeps the change; reply `HostPending` |
| `POST /api/v1/host/pending/revert` | admin, global | the same; puts the snapshot back now |

The spec:

    {"template": "custom-rules",
     "ssh": {"ports": [22], "allow_from": ["@admins"]},
     "console": {"allow_from": ["@admins", "198.51.100.7"]},
     "sets": [{"name": "admins", "members": ["192.0.2.0/24", "2001:db8:a::/48"]}],
     "allow": [{"proto": "tcp", "ports": ["80", "443"], "comment": "web"},
               {"proto": "udp", "ports": ["51820"], "from": ["2001:db8:1::/56"]}],
     "icmp": true, "logging": false, "nat": "none"}

The spec is applied whole: a part left out takes its default (template `default`, ssh port 22, sources anywhere), not the
value applied before. `template` is `default` (block inbound except ssh, the console and what is always passed), `open`
(pass everything) or `custom-rules` (`default` plus the `allow` rows, at most 64; `allow` with another template is
`invalid`). Sources are addresses, prefixes without host bits or `@set` of a set defined in `sets` (at most 16 sets of 1 to
256 members, at most 32 sources per list); ports `N` or `N-M`, at most 16 per row; `proto` `tcp` or `udp`; a `comment` has
at most 80 characters and no line break, quote or backslash. `console.port` is the daemon's TLS port and is filled in by
the daemon. `nat` other than `none` is `unsupported`. `confirm_within` is 10 to 1800 seconds (default 120).

A real change is refused with `forbidden` (detail `guard: lockout`) when the address the request came from could reach
neither an ssh port nor the console under the new rules, and with `invalid` (detail `guard: unknown_peer`) when that address
is unknown (the unix socket, a request through a hub) unless `i_know` is set; a dry run reports both as `warnings`. While a
change waits for its confirmation every other change of the host (`PUT` of a module, a restore) answers `busy` with the id
of the pending change in `detail.pending`. In Go:

    fw, err := c.HostSystem().Firewall().Get(ctx)
    ch, err := c.HostSystem().Firewall().Apply(ctx, api.HostFirewallApply{Spec: spec, ConfirmWithin: 300})
    p, err := c.HostSystem().Confirm(ctx, ch.Pending.ID) // or Revert, or Pending
