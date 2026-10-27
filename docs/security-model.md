# Security model

Read this before you give anyone an `admin` role or edit `priv.conf`. The full reasoning and the audit that produced it
are in [design.md](design.md) section 18.6; this page is what an operator needs.

## Two programs, one boundary

| | `jailopsd` | `jailops-priv` |
|---|---|---|
| Runs as | the unprivileged user `jailops` | root |
| Does | the API, authentication, the database, the reconcile loop, the web consoles | the privileged operations: `zfs`, `jail`, `pfctl`, `ifconfig`, `route`, mounts, packet capture for the IPv6 proxy, file access inside jails |
| Trusts | its clients, as far as their role and project scope allow | **the daemon not at all** |
| Configured by | `jailopsd.yaml` | `priv.conf`, owned by root |

The helper offers a fixed list of typed operations over a unix socket. It has no shell and no free-form command. A
compromised daemon can ask for anything on that list, and the helper checks every request again against `priv.conf`:
the storage roots the paths must lie below, the allow-lists, and the limits. The helper accepts connections only from root and from the
users and groups named in `priv.conf` (checked with `LOCAL_PEERCRED`). The helper is a service boundary between two
processes of JailOps, not a place where people sign in, so it keeps Unix peer credentials.

**What a compromised daemon cannot do:** name a path outside the storage roots, run a program that is not on the
allow-list, sniff or inject on an interface the operator did not approve, mount a directory the operator did not name,
make the helper advertise an IPv6 address outside the approved prefixes, or turn a ZFS user property into authority (the
helper keeps what it granted in its own memory, never in something the daemon can write).

**What it can do, and you should assume it can:** act as any API user for anything inside those bounds. The helper takes
the "this request is for an admin" assertion from the request itself, because only the daemon knows who authenticated.
So **the allow-lists in `priv.conf`, not the assertion, are the boundary.** Separating that further would need a second
credential the daemon cannot hold, such as an administrator confirming at a terminal, which is not how the API works.

There is no Capsicum confinement of the helper or of its file agents yet. The file agents are chrooted child processes with
a lifetime limit and a global cap.

## What you decide, in `priv.conf`

| Line | What it lets an admin do | Default |
|---|---|---|
| `storage-root` | where jails, images and their files may live | the root `init` made |
| `ndp-proxy = <uplink>=<prefix>` | the IPv6 proxy may capture neighbor discovery on that uplink and advertise addresses in that prefix, at a bounded rate | nothing |
| `admin-host-mount` | mount host directories below this path into a jail (nullfs) | nothing |
| `admin-raw-param`, `admin-allow-flag`, `admin-devfs-ruleset`, `admin-zfs-prop` | set these raw `jail.conf` parameters, `allow.*` flags, devfs rulesets or ZFS properties | nothing |
| `admin-routes`, `admin-raw-pf`, `admin-resume-send` | change routes, load raw PF, resume a ZFS send | off |
| `fetch-host` | download from these hosts, over https | the Alpine CDN and `pkg.freebsd.org` |

**An admin who may set these is root on the host.** Keep the lists short, and write nothing you would not give root.

## What listens, and how to expose it

By default only the unix socket listens. `listen.https.address` opens a TLS 1.3 listener (there is no plain-http TCP listener), with a
generated self-signed certificate that clients pin or one you provide (`listen.https.cert`/`key`, reloaded without a restart, never
generated over). Clients are pinned client certificates, API tokens, or console logins; the listener requests a client certificate and
does not require one. The web console is always built in and off by default (`web.enabled: true` or `jailopsd_web="YES"` turns it on; `-tags noweb` builds `jailopsd` without it). Three recipes (local only, behind a
proxy, direct with mTLS), what changes behind a proxy, and the **Not yet** list (ACME, a require-client-certificate mode, the caller's
address behind a proxy) are in [exposure.md](exposure.md).

## Roles and scopes of API users

Every identity (a user of the web console, an API token, a client certificate) has a **role** and a **scope**.

- Roles: `readonly`, `operator` (jail lifecycle, snapshots, exec, backups of its jails) and `admin` (configuration). There
  are also narrow roles for restores and for backup transport.
- Scope: a non-empty list of projects, or `global`. **An empty list never means everything.** An identity with neither can
  call nothing except its own session routes. Only a global admin may grant `global`.
- A request for an object of a project the caller may not use answers `not_found`, never `forbidden`, so that existence
  does not leak. A role that is too low is checked first and answers `forbidden`.
- Dangerous parameters (raw jail parameters, host mounts, device nodes, ZFS delegation, raw PF, provisioning commands)
  need `admin` or a global admin, are allow-listed by `priv.conf`, and are audited.
- Every route has a permission entry in the code, or the daemon refuses to start.

### One identity system, over every transport

There is one set of identities (JailOps users with a password and sessions, API tokens, client certificates), one
permission table, one scope rule and one audit trail, whether the request arrives over TLS or over the local unix socket.
**Unix users and groups are not mapped to roles.** The kernel-reported peer uid of the socket decides exactly one thing:

- **uid 0 (root) is a global admin.** This is the host's break-glass and recovery access, not an ordinary user: whoever is
  root on the host can read every secret the daemon holds anyway. Root's requests are audited under its user name, with
  kind `unix`. `sudo jailops user create admin --admin` creates the first administrator; `sudo jailops user set-password`
  and `set-role` recover a lost password or a wrong role. There is no separate reset mechanism.
- **Every other local user is anonymous**, exactly like an unauthenticated remote client. It reaches `GET /api/v1`,
  `GET /healthz`, the login (`POST /api/v1/auth/login`), trust-token redemption (`POST /api/v1/trust/clients`),
  `POST /api/v1/cluster/join` (answers 501) and the static web console, and nothing else. It can authenticate over the socket
  with an API token, or with a session cookie from a password login, like any remote client. A test runs every route as an
  anonymous remote client and as an anonymous local one and requires the same answer.

The socket stays mode `0666`. Being able to connect confers nothing, because a non-root peer has precisely the rights of an
anonymous remote client, who can connect to the HTTPS listener (when it is on) just as freely. A tighter mode would only
keep people from signing in locally with a token or a password; it would add no protection. The same rate limits and audit
apply to what an anonymous local caller tries (all local callers share the source `unix`).

The backup node (`jailops-backupd`) is stricter: its admin socket is mode `0600` and the peer check admits only root (uid 0) as
administrator; every other local user is refused. The former `admin_group` key is removed and stops the node with a message.

## How clients are trusted

There is no trust on first use. An admin creates a single-use token bound to a name, with an expiry (one hour by
default); the token carries the server certificate's fingerprint, so the new client pins the server before it sends
anything. The client then sends its own certificate, and the server stores it with the role and scope the token asked for.
A hub forwards requests to hosts over pinned mTLS with a signed claim of the caller's role and scope that a host only
accepts from a certificate of kind `hub` and limits by that certificate's own entry. Failed authentications are rate
limited per source and audited.

## Projects are not a security boundary against the kernel

A project separates names, quotas, networks and firewall policy. Jails share the host kernel. A high-security tenant needs
its own pool, its own host, or both. A jail with `zfs_delegation` or a host mount is not isolated from that storage.

## Network isolation

Every jail gets a PF anchor, and a project's jails cannot reach another project's unless a rule you wrote opens it. Per-jail
rules bind the jail's addresses (anti-spoofing). This filters **IP traffic**. A jail holds /32 and /128 addresses behind the host, every jail port is private, and the host pins each
jail's ARP and neighbor entries and MAC address; forged ARP, neighbor advertisements and router advertisements from a
jail change nothing (design 18.13). A bridge that carries
ports JailOps did not create is not covered. A bridge is not a full security boundary.

## Secrets

- A jail's `env` value `sealed:age:<ciphertext>` is sealed to the host's age key (`<data_dir>/secrets.key`, public half
  published as `secrets_recipient`). The API returns the ciphertext, never the plaintext; the daemon opens it only when it
  starts a process in the jail.
- The backup key is a file on the host; the recovery key is shown once and kept nowhere. The backup node never holds a key.
  See [backup-restore.md](backup-restore.md).
- Do not put tokens on a command line (visible to other users); use `--token-stdin` or `token_file`.
