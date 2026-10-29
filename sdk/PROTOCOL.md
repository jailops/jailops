# JailOps wire protocol (SDK contract)

The SDK client (`sdk`) and the in-memory server (`sdk/fake`) implement this document.
The daemon must match it. Types are in `sdk/api`. Everything is JSON under `/api/v1`.

## Envelope

Every reply is an `api.Response`:

| type | HTTP | body |
|---|---|---|
| `sync` | 200, 201 (create), 204 | `metadata` holds the result |
| `async` | 202 + `Location` | `operation` is `/api/v1/operations/<id>`, `metadata` is the `api.Operation` |
| `error` | per code (`api.Code.HTTPStatus`) | `error` is `api.Error` (code, message, hint, detail) |

A server may answer a mutating call synchronously or asynchronously; the client handles both. A
non-JSON error body (a proxy, a crash) is mapped from the status: 400 invalid, 401 unauthorized,
403 forbidden, 404 not_found, 405/501 unsupported, 409 already_exists, 412 conflict,
429/502/503/504 busy, other 4xx invalid, other 5xx internal.

## Conventions

- `GET /api/v1` returns `api.ServerInfo` (allowed unauthenticated; `auth` says how the caller was seen).
  `api_extensions` drives `Client.Has`.
- Lists: `GET /api/v1/<resource>` returns `metadata` as an array of full objects (never URLs). Optional
  `?label=k=v` (repeatable, AND) and `?recursion=n` (reserved).
- Items: `/api/v1/<resource>/<name>`. `GET` sends `ETag: "<generation>"`.
  `PUT` replaces the object and may carry `If-Match: "<generation>"`; a mismatch is `412 conflict`.
  The server owns `uuid`, `scope`, `host`, `created_at` and all `status`; clients cannot change them.
  `PATCH` applies a JSON merge patch (RFC 7386) with the same `If-Match` rule.
  Responses to `PUT`/`PATCH` carry the new `ETag`. `generation` increases when the stored object changes.
- Creates are `POST /api/v1/<resource>` with the full object. Names: lowercase letters, digits, `-_.`.
- Operation results: on success `operation.metadata` is the resulting object (for example the `Jail`);
  when it is empty the client fetches the object by name where it knows it.
- Multi-host (hub): add `?host=<uuid-or-name>` to any request, including `/api/v1/events`. The hub
  forwards the request unchanged to that host over mTLS. This form was chosen over a
  `/api/v1/hosts/{h}/...` prefix because it leaves every route and its permission entry untouched.
- Auth: `Authorization: Bearer <token>`, or mTLS, or the unix socket. Every request carries
  `X-Request-ID` (16 hex) and `User-Agent: jailops-sdk/<version> ...`.
- Only GET/HEAD/OPTIONS are retried by the client; the server must keep them side-effect free.

## Routes

| Method and path | Request | Reply |
|---|---|---|
| `GET /api/v1` | | `ServerInfo` |
| `/api/v1/jails` GET, POST | `Jail` | list; POST async, result `Jail` |
| `/api/v1/jails/<n>` GET, PUT, PATCH, DELETE | | `Jail`; DELETE async |
| `GET /api/v1/jails/<n>/state` | | `JailStatus` |
| `GET /api/v1/jails/<n>/usage` | | `JailUsage`, measured for this reply and never stored: `accounting` (processes, `cpu_seconds`, `memory_bytes` from racct through `rctl -u`) only while the jail runs, with `error` instead of numbers when racct is off; `disk` (`root_bytes` and `root_referenced_bytes` of the root dataset, `data_bytes` of the persistent datasets and volumes) always, with `error` when a dataset cannot be read. Readonly; the jail list does not carry it (`jail_usage`) |
| `PUT /api/v1/jails/<n>/state` | `JailAction` (start, stop, restart) | async, result `Jail` |
| `/api/v1/jails/<n>/snapshots` GET, POST | `JailSnapshot` (name optional) | list; POST async, result `JailSnapshot` |
| `/api/v1/jails/<n>/snapshots/<s>` GET, DELETE | | `JailSnapshot`; DELETE async |
| `POST /api/v1/jails/<n>/rollback` | `JailRollback` | async, result `Jail`; data/all without `confirm_data_loss` is `data_loss_unconfirmed` |
| `POST /api/v1/jails/<n>/upgrade` | `JailUpgrade` | async, result `Jail` |
| `GET /api/v1/jails/<n>?expanded=true` | | `JailExpanded`: the jail with its profiles merged in (`spec` is the effective spec) and `sources`, the origin of every key (`jail` or `profile:<name>`) |
| `POST /api/v1/jails/<n>/config?restart=true` | `ConfigChange` (`set`, `unset`) | async, result `{...Jail, restart_required: [keys], restarted}`; **never restarts the jail unless `restart=true`**, and then only when a changed key needs it |
| `POST /api/v1/jails/<n>/exec` | `ExecRequest` | upgrade to a framed stream, see Exec |
| `GET /api/v1/jails/<n>/updates` | | `[]UpdateRecord`, newest first (`updates`) |
| `POST /api/v1/jails/<n>/provision` | `ProvisionRequest` (`again`) | async, result `ProvisionResult`; `already_exists` when it ran before and `again` is not set (`provisioning`) |
| `GET /api/v1/config-keys` | | `[]ConfigKey`: the key registry (name, type, values, description, example, `restart_required`, `profile`, `admin`, `path` into the spec); any authenticated identity |
| `/api/v1/profiles` and `/<n>` | `Profile` | CRUD; project-level, or host-level with `?project=host` (admin writes) |
| `GET /api/v1/updates` | | `[]UpdateReport`: the latest check of every visible jail |
| `POST /api/v1/updates/check` | `UpdateCheckRequest` (`jails`, `all`) | async, result `[]UpdateReport`; changes nothing but the package catalog in the jail |
| `POST /api/v1/updates/apply` | `UpdateApplyRequest` (`jails`, `all`, `packages`, `os`) | async, result `[]UpdateRecord` |
| `/api/v1/images` and `/<n>` | `Image` | CRUD |
| `/api/v1/images/<n>/generations` GET, POST | `ImageBuild` | list of `ImageGeneration`; POST async, result `ImageGeneration` |
| `DELETE /api/v1/images/<n>/generations/<number>` | | async; `image_in_use` while referenced |
| `POST /api/v1/images/<n>/upgrade` | `ImageUpgrade` (`no_jails`, `project`, `dry_run`, `storage`) | async, result `ImageUpgradeResult`: the next generation built from the newest one (none, `unchanged`, when the package set did not change), then every thin jail of the image moved to it one at a time through the OS update of the update pipeline (history records, `update.applied`/`update.failed` events), stopping at the first jail that fails; `jails` lists each with `upgraded`, `skipped`, `failed`, `not_attempted` or `planned` (dry run). Global admin; `not_found` with a hint when the image has no generation yet (`image_upgrade`) |
| `/api/v1/networks` and `/<n>` | `Network` | CRUD; `GET /api/v1/networks/<n>/leases` returns `[]Lease` |
| `/api/v1/firewall/policies`, `/api/v1/firewall/address-sets` and `/<n>` | | CRUD |
| `/api/v1/backup-targets`, `/api/v1/backup-policies` and `/<n>` | | CRUD |
| `/api/v1/backups`, `/<n>` | | GET list/item; DELETE async |
| `POST /api/v1/restores` | `RestoreRequest` | `dry_run`: sync `RestorePreflight`; otherwise async, result `Jail`; a failing preflight is `invalid`. `project` restores into that project (else the backup's); the caller needs access to it (`not_found` otherwise) and its grants and quotas apply |
| `GET /api/v1/backup/key` | | `BackupKeyStatus` (`none`, `present`, `escrowed`); global admin (`backup_key`) |
| `POST /api/v1/backup/key` | `BackupKeyCreate` | 201 sync `BackupKeyCreated`. The `recovery_key` is in this reply **once**: it is never an operation result, never in the audit detail and never logged; the reply is `Cache-Control: no-store`. A second key is `already_exists` |
| `POST /api/v1/backup/key/escrow` | `BackupKeyEscrow` | sync `BackupKeyStatus` |
| `POST /api/v1/backups/run` | `BackupRunRequest` (`policy`) | async, result `[]BackupRun`; global admin (`backup_run`) |
| `POST /api/v1/backups/<id>/verify` | `BackupVerifyRequest` (`depth` deep, extract, full) | async, result `BackupRun` (`backup_verify`) |
| `GET /api/v1/operations`, `/<id>`, `DELETE /<id>` | | `[]Operation`, `Operation`; DELETE cancels |
| `GET /api/v1/operations/<id>/wait?timeout=<s>` | | sync `Operation`, returned when final or after the timeout (the client re-polls) |
| `GET /api/v1/events?type=a,b` | | `text/event-stream` |
| `/api/v1/remotes`, `/api/v1/certificates` and `/<n>` | | CRUD; `/api/v1/certificates/<fingerprint-or-name>`; revocation (`DELETE`) and edits (`PUT`, `PATCH`) take effect on the next request |
| `/api/v1/tokens` | `Token` | `POST` returns the `Token` with `secret` set, once; never again |
| `/api/v1/users` | `UserCreate` | `POST` returns `UserCreated`; `password` only when generated |
| `/api/v1/users/{name}/password` | `PasswordSet` | `POST` (global admin): replaces the password without the old one and ends the user's sessions; an empty `password` asks for a generated one, returned once in the same type |
| `/api/v1/projects` and `/<n>` | `Project` | CRUD; see Projects |
| `GET /api/v1/projects/<n>?host=all` | | hub only: `ProjectAggregate` |
| `/api/v1/storage` and `/<n>` | `Storage` | CRUD; see Storage |
| `POST /api/v1/trust/tokens` | `TrustTokenRequest` | `TrustToken`, once (201); global admin |
| `POST /api/v1/trust/clients` | `TrustClientRequest` | `Certificate` (201); unauthenticated, authorised by the token only |
| `GET /api/v1/sessions`, `DELETE /api/v1/sessions/<id>` | | `[]Session`; DELETE revokes |

| `GET /api/v1/audit` | | `[]AuditEntry`, newest first; admin (`audit_log`) |
| `GET /api/v1/doctor` | | `DoctorReport`: checks with `ok`, `warn`, `fail` or `unknown` (`doctor`) |
| `GET /api/v1/resources` | | host resources and usage (`resources`) |
| `POST /api/v1/jails/<n>` | copy or rename request | async, result `Jail` (`jail_copy`) |

### Console

The browser console cannot open an HTTP upgrade, so it uses a session (extension `jail_console`), with the same
project rules, per-jail limits and audit entries as exec:

- `POST /api/v1/jails/<n>/console` starts a command with a terminal and returns a session `id`; a session belongs to the
  identity that opened it (at most 8 per identity, 64 in total).
- `GET /api/v1/jails/<n>/console/<id>/output` streams what the command prints as server-sent events and honours
  `Last-Event-ID` (the last 1 MiB is kept for a client that reconnects).
- `POST .../input` (at most 64 KiB per request) and `POST .../resize` feed it; `DELETE .../console/<id>` closes it.

The Go SDK wraps the session as `Jails().Console(ctx, name, ExecOptions)` (a `ConsoleSession` with `Output`, `Write`,
`Resize`, `Wait`, `Close` and `Run`), and `jailops jail console NAME` uses it with a raw local terminal and resize
forwarding. Input is UTF-8 text; use exec (`jailops shell`) for binary input. Verified against the fake and the
in-process daemon only; a run on a real FreeBSD host is UNVERIFIED.

### Routes that exist and answer `unsupported`

These routes have a permission entry but their feature is not in this version, so they answer `unsupported` (501),
never success: `PUT /api/v1/server` and `GET /api/v1/server/config`; `/api/v1/cluster` and its sub-routes (cluster mode);
`GET /api/v1/jails/<n>/console` (list), `.../logs`, `.../files/<path>`, `POST .../move`, `.../volumes`, `.../bundle`;
`POST .../snapshots/<s>` (rename); `/api/v1/jail-templates`; `/api/v1/groups`; `/api/v1/rollouts` (rollouts are a
`jailops-sync` feature); `POST /api/v1/backups`, `POST /api/v1/backups/<id>/restore` and `.../restore-preflight` (use
`POST /api/v1/restores`); and the backup transport routes `POST /api/v1/backups/receive`, `GET /api/v1/backups/<id>/stream`,
`GET /api/v1/backups/chains`, which are the job of the backup node.

## Exec

`POST /api/v1/jails/<n>/exec` takes an `ExecRequest` JSON body (`argv`, or `shell` and `shell_path` for the
jail's shell, `tty`, `rows`, `cols`, `env` as `NAME=value`, `user`) and the headers `Connection: Upgrade`
and `Upgrade: jailops-exec/1`. Errors that are known before the stream starts (`not_found`, `busy` for a
jail that does not run or has too many sessions, `invalid`, `forbidden`) are an ordinary JSON error. On
success the reply is `101 Switching Protocols` and the connection carries frames, both ways:

    +------+----------------+---------+
    | type | length (u32 BE) | payload |
    +------+----------------+---------+

The payload is at most 65536 bytes (a longer frame ends the session). Types: 1 `stdin` (an empty
frame is end of input), 2 `stdout`, 3 `stderr` (absent in TTY mode, where stderr is merged into
stdout), 4 `resize` (rows and cols, two u16 BE), 5 `signal` (an i32 BE signal number), 6 `exit` (an i32
BE exit status, the last frame). A session that is idle too long or runs too long is ended with exit
status -1. Closing the connection kills the command. HTTP/1.1 only: the client does not offer h2 on this
request. The same authentication, project and audit rules as every route apply, and the audit entry
carries the argv of the command; through a hub the upgrade is forwarded like any request.

## Projects

A project is a namespace. Project-level objects are jails (and their snapshots and volumes), firewall
policies, address sets and backup policies. Host-level objects are networks, storage, images, projects,
users, certificates, tokens, remotes and the server configuration.

- `?project=<name>` selects the project on every project-level route. There is no default project: a fresh host has
  none, and a project-level request without `?project=` is `400 invalid` (`a project is required`, with the hint
  `name one with --project NAME`). The server sets `metadata.project` of every project-level object; a body that
  names another project is rejected as `invalid`. Host-level kinds (images, networks, storage, projects,
  certificates, users, tokens, trust, server info, doctor, audit, operations, events, backup targets and keys) and
  host profiles (`?project=host`) never need one.
- Names are unique per kind and project; UUIDs are global. A jail's OS-level name and its datasets are
  qualified by the project, so `web/nginx` and `db/nginx` never collide.
- Lists (`GET /api/v1/jails`, policies, address sets, backup policies) return the objects of one project.
  `GET /api/v1/events`, `/api/v1/operations`, `/api/v1/audit` and `/api/v1/backups` return everything the caller may
  see, narrowed by `?project=` when given. Events, operations and audit entries of host-level objects
  (`metadata.project` empty) are visible to global identities only.
- An object of a project the caller may not use is `not_found`, never `forbidden`, on every verb:
  `GET`, `POST` (create), `PUT`, `PATCH`, `DELETE`, state changes, snapshots, and `GET /api/v1/operations/<id>`.
  The same is true of a project that does not exist. A role that is too low is checked first and is
  `forbidden`.
- Project grants. `spec.networks` lists the networks a jail may attach to (`"*"` is every network). A network
  is granted whole and to at most one project: granting one that another project holds is `conflict`
  (`network N is already granted to project P`), and there are no sub-ranges. `spec.storage` lists the storage a
  jail may be placed on with a quota for the project's dataset (`"*"` is every storage); storage may be granted to
  many projects, each getting its own `projects/<project>/` subtree. A jail naming a network or storage that is
  not granted fails with `forbidden`. Nothing is filled in: a jail whose own spec and profiles name no network
  is `invalid` (`a jail needs a network: name one with --network NAME or use a profile that has one`), and the
  same for storage.
- Quotas (`spec.quotas`) are checked before anything is created and fail with `quota_exceeded`:
  `jails`, `cpu`, `memory`, `pids` and `addresses` are sums over the project's jails. A project with a
  `cpu`, `memory` or `pids` quota requires every jail to set the matching `resources` limit (`invalid`
  otherwise). The per-storage `quota` is applied as a ZFS quota on the project dataset.
- `spec.firewall` names a policy of the project that is rendered before every jail's own policy in the
  jail's anchor `jailops/<project>/<jail>`; only a global admin may edit or delete that policy or change
  the field. `spec.images`, when not empty, restricts the images a jail may use.
- `POST /api/v1/projects` with `spec.new_network: true` creates a dedicated bridge and subnet from the
  server's pool of subnets, grants it to the project and stores the project with the flag cleared
  (`unsupported` when the server has no pool configured).
- A project is deleted only when it holds no jails, policies, address sets or backup policies
  (`busy` otherwise). Only global admins create, change or delete projects;
  a restricted identity lists and reads only its own projects.
- Fleet projects. `spec.hosts` lists the hosts a project spans (remote names or UUIDs on a hub). The hub
  applies the definition to each listed host through the SDK client (same UUID, `managed_by:
  hub:<hub uuid>`), after checking that the host has every named network and storage; a missing one is a
  condition `Host:<host>` with status `False` and reason `MissingNetwork` or `MissingStorage` on the
  hub's project status, and nothing is applied to that host. Hosts that cannot be reached
  (`reason: Offline`) are retried every 30 s and catch up when they return. A host project that exists but
  is not managed by this hub is `reason: Conflict` and is never changed. Each host stores and enforces its
  own copy; there is no consensus and no fleet-wide quota. The hub's certificate on each host must be of
  kind `hub` with the rights the definition needs (`global: true` to write projects).
- `GET /api/v1/projects/<n>?host=all` on a hub returns `ProjectAggregate`: the definition, one
  `ProjectHostStatus` per listed host (`online`, `applied`, `in_sync`, the host's `status`, or an `error`)
  and the sum of the usage of the hosts that answered. The hub reads each host with the caller's signed
  requestor claim, so every host applies its own scoping. A request with `host=all` to any other route is
  `invalid`.

## Storage

A storage names a ZFS dataset root (`spec.dataset`, for example `tank/jailops`) on an imported pool.
Datasets live below it: `<root>/projects/<project>/jails/<name>` and `<root>/images/<image>/g<N>`.
Creating a storage requires the dataset to exist; the server registers it with the root helper and adopts
it (only an empty dataset or one already owned by JailOps is accepted). `status` is read from ZFS: `pool`,
`health`, `size`, `used`, `available`. A storage cannot be deleted while a project grants it or a jail
uses it; deleting it never destroys data. The host creates a storage `default` (dataset `<pool>/jailops`)
at its first start. Image generations are built or received per pool: a jail's root is cloned from a
generation on its own pool, so the first jail of an image on a pool builds or receives that generation
there; `ImageGeneration.storages` lists the storage that hold a copy. Only global identities write
networks, storage and images; project users read the ones granted to their projects.

The extensions `projects`, `project_quotas`, `storage`, `trust_tokens`, `sessions` and `fleet_projects` name
this part of the contract. Only `<root>/projects/<project>/jails/<name>` exists on disk.

## Identity and authorization

Every identity (user, token, certificate) has a role and a scope: `projects` (a non-empty list) or
`global: true`. An empty list never means all projects; an identity with neither can do nothing except
call the `/api/v1/auth/*` self-service routes and the public routes. Creating an identity with neither is
`invalid`. Authorization has two checks, both in the service layer: the role from the permission table
(`forbidden`) and then the object's project (`not_found`). Host-level writes need a global admin
(`forbidden` otherwise). Unix socket: the peer uid 0 (root) is a global admin (kind `unix`, the host's recovery
access); every other local peer is anonymous, exactly like an unauthenticated remote client, and reaches only the public
routes (`GET /api/v1`, `GET /healthz`, `POST /api/v1/auth/login`, `POST /api/v1/trust/clients`, `POST /api/v1/cluster/join`, the static
console) until it authenticates with a token or a session cookie. No Unix user or group maps to a role.

- `GET /api/v1/auth/session` returns `Identity` (`user`, `kind`, `permission`, `projects`, `global`).
- Trust workflow. `POST /api/v1/trust/tokens` (`TrustTokenRequest`) returns `TrustToken` once. The token
  is `joc_<base64url JSON>.<base64url HMAC-SHA256>`; the JSON holds `v` (1), `id`, `name`, `fp` (the
  server certificate fingerprint, hex SHA-256), `exp` (unix seconds) and `sec` (random). The client pins
  `fp`, generates a key pair and sends `POST /api/v1/trust/clients` with the token and its PEM certificate.
  The server verifies the HMAC, the expiry and its own fingerprint, consumes the token atomically (a second
  use, an expired token and a forged token are all `unauthorized`), and stores the certificate
  fingerprint as a certificate named after the token with the requested role, scope and optional expiry.
  Failed attempts are rate limited per source (`rate_limited`, 429) and audited.
- Hub forwarding. A hub authorises the caller, then forwards the request over pinned mTLS with the header
  `X-JailOps-Requestor: <base64url payload>.<base64url signature>`. The payload is JSON: `v` (1), `name`,
  `kind`, `role`, `projects`, `global`, `iat`, `exp` (at most 60 s after `iat`), `aud` (the host UUID, when
  the hub knows it), `method` and `path` of the request. The signature is ASN.1 ECDSA over
  SHA-256(`jailops-requestor-v1\n` + payload) with the hub certificate key. The host accepts the header
  only on a TLS connection whose client certificate is a pinned certificate of kind `hub`, verifies the
  signature with that certificate's public key, and limits the claim by the certificate entry: the role
  cannot exceed its permission and the claimed scope must lie inside its `projects` (or it needs `global`).
  The header is rejected (`forbidden`) on the unix socket, with a token, with a session and with a
  non-hub certificate. Without a claim a hub certificate acts with the rights of its own entry only.
- `GET /api/v1/sessions` lists the caller's sessions (a global admin sees all); `DELETE /api/v1/sessions/<id>`
  revokes one (own sessions, or any for a global admin). Revocation ends the session on its next request.
- Failed authentication (bad token, unknown or revoked certificate on a non-public route, bad trust token)
  is rate limited per source and audited as `denied`.

## Events

`GET /api/v1/events` answers `200 text/event-stream`. Each event:

```
id: <monotonic integer>
event: <lifecycle|operation|logging>
data: <api.Event as one line of JSON>

```

`?type=` filters. A client that reconnects sends `Last-Event-ID`; the server replays retained events
with a larger id, then continues live. Without it the stream starts at "now". A `retry:` field sets
the reconnect delay. Lines beginning with `:` are comments (keep-alives). Lifecycle metadata is
`api.LifecycleEvent` with actions like `jail-created`, `jail-started`, `network-updated`.

## Auth failures and TLS

401 `unauthorized` for a missing or bad credential, 403 `forbidden` for a valid credential without the
permission. Hosts are pinned by the SHA-256 of the leaf certificate (hex, `sdk.Fingerprint`).

## Optional extensions used by the CLI

These routes are not part of the base contract. A client calls them only when `api_extensions`
lists the name, and otherwise reports `unsupported`.

| Extension | Method and path | Reply |
|---|---|---|
| `backup_key` | `GET`/`POST /api/v1/backup/key`, `POST /api/v1/backup/key/escrow` | see Routes; advertised when the daemon has a `backup:` configuration |
| `backup_run` | `POST /api/v1/backups/run` | async, result `[]BackupRun` |
| `backup_verify` | `POST /api/v1/backups/<n>/verify` | async, result `BackupRun` |
| `config_keys`, `jail_config`, `profiles`, `jail_devices` | config registry, config operation, profiles, devices (`nic` names, MTU and rate limits, `devfs`, `proxy`) | see Routes |
| `jail_exec` | `POST /api/v1/jails/<n>/exec` | see Exec |
| `updates` | `/api/v1/updates`, `/api/v1/jails/<n>/updates` | managed updates (design 8.4); off for every jail until `updates.mode` is set |
| `jail_shell` | the `shell` key | the shell `jailops shell` starts; no fallback when it is missing |
| `provisioning` | `POST /api/v1/jails/<n>/provision` | design 8.5 |
| `jail_console` | `POST /api/v1/jails/<n>/console` and the session routes | see Console |
| `sealed_secrets` | `secrets_recipient` in `ServerInfo` | the age public key of the host; a jail `env` value `sealed:age:<base64>` is opened by the daemon only when it starts a process in the jail (design 18.8) |
| `audit_log`, `doctor`, `resources` | `GET /api/v1/audit`, `/api/v1/doctor`, `/api/v1/resources` | see Routes |
| `jail_copy` | `POST /api/v1/jails/<n>` | copy or rename |
| `image_upgrade` | `POST /api/v1/images/<n>/upgrade` | build the next generation and move the jails that use the image; see Routes |
| `jail_usage` | `GET /api/v1/jails/<n>/usage` | what a jail uses now; `jailops jail info` reads it; see Routes |
| `backup_pull`, `backup_push` | the backup transport permissions of the backup node | not routes of the host daemon (see above) |

Update and provisioning keys of the registry: `shell`, `updates.mode` (manual, notify, managed),
`updates.schedule` (cron), `updates.packages`, `updates.os`, `updates.reboot` (manual, auto).
