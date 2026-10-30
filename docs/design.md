# JailOps design

> JailOps is a convention-driven, ZFS-native Jail as a Service platform for FreeBSD.
> Every host is independently manageable with a local API and web UI, while
> supporting unified multi-host management and dedicated backup nodes.
> Complexity belongs in the platform, not in the user's configuration.

This document is the contract. Code follows it; when they disagree, one of them is
wrong and the disagreement is fixed in the same change.

Claims are tagged. **[verified]** means read from code or run. **[unverified]** means it
is FreeBSD knowledge that has not been run in this project and must be proven on a real
FreeBSD 15.1 host before it is relied on. Section 16 is the register: what was assumed, what settled it, and what is
still open.

## Contents

- [1. Principles](#1-principles)
- [2. Components](#2-components)
- [3. Storage: ZFS layout](#3-storage-zfs-layout)
- [4. Thin jails and image generations](#4-thin-jails-and-image-generations)
- [5. Networking](#5-networking)
- [6. Jails, Linuxulator and Alpine](#6-jails-linuxulator-and-alpine)
- [7. State, storage of data and reconcile](#7-state-storage-of-data-and-reconcile)
- [8. API](#8-api)
- [9. Backup](#9-backup)
- [10. Multi-host](#10-multi-host)
- [11. Cluster mode (Raft)](#11-cluster-mode-raft)
- [12. Fleet sync](#12-fleet-sync)
- [13. SDK](#13-sdk)
- [14. Build, test, release](#14-build-test-release)
- [15. Milestones](#15-milestones)
- [16. Status register: what was assumed, and what is settled](#16-status-register-what-was-assumed-and-what-is-settled)
- [17. Acknowledgements](#17-acknowledgements)
- [18. Verified on FreeBSD 15.1 (arm64 and amd64 test hosts)](#18-verified-on-freebsd-151-arm64-and-amd64-test-hosts)
- [19. Implementation notes](#19-implementation-notes)

Operator documents: [docs/README.md](README.md).

## 1. Principles

**Supported releases.** JailOps targets FreeBSD 15.1 and later and does not look backwards: pkgbase is the only way a
userland is built, and the pf, VNET and ZFS behaviour it relies on is that of 15.1 (OpenZFS 2.4). Each new minor release
is a row in the acceptance matrix (the PF traffic matrix and the clean-install end-to-end run) before it is called
supported. `jailops doctor` fails on anything older than 15.1.

1. **ZFS-native by design.** All jail filesystems, images, snapshots, clones, volumes,
   backups, replication and recovery are built on native ZFS. ZFS is the authoritative
   storage layer. SQLite and Raft serve management and coordination only. There is no
   alternative filesystem backend and no nullfs-based thin-jail model.
2. **Jails only, with laser focus.** No virtual machines, no bhyve, no hypervisor, and no OCI or
   Docker-style application containers. A jail is a system container: it runs services under its own init
   or rc, and there is no image format other than image generations. Linux guests run in jails through the
   Linuxulator, with documented compatibility limits. Anything that would make JailOps a general container
   platform is out of scope.
3. **Convention over configuration.** One way to do networking (managed VNET bridges with
   deterministic addresses), one way to do thin jails, one base source (pkgbase). Options
   that FreeBSD supports but JailOps does not need are not offered.
4. **A single host is complete.** No cluster setup is needed to start. Hubs and clusters are
   additive.
5. **One API.** CLI, web UI, `jailops-sync` and automation use the same REST API and the same
   service layer. Nothing is second-class.
6. **FreeBSD does the work.** jail(8), ZFS, PF and if_bridge do the work. JailOps renders
   configuration, orders operations and verifies results. It does not reimplement them.
7. **Do not fake.** Where something cannot be verified without FreeBSD, the code says so.
   A feature that is not implemented returns `unsupported`, it does not pretend.
8. **Pure Go.** `CGO_ENABLED=0`. The one place CGO may be admitted is the Raft library
   decision (section 11), isolated behind a build tag.

## 2. Components

```
jailops          CLI (cobra). Uses only sdk/api and sdk, plus internal/apps (the pure catalog validator) for `app validate`.
jailopsd         API daemon. Unprivileged. API, auth, state, reconcile, operations.
jailops-priv     Root helper. Typed RPC over a unix socket. No shell, no free-form commands.
jailops-sync     Fleet sync. Uses only sdk/api and sdk.
jailops-backupd  Backup node: keyless repository server with its own management API and web UI.
                A separate binary. It links no jail, ZFS, privileged-helper or decryption code.
sdk             Go client module (separate go.mod). sdk/api holds the wire types.
sdk/fake        In-memory server implementing the SDK interface, for tests.
web/shared      Design tokens, theme, components and API client base shared by both UIs.
web/host        Console of the per-host daemon (and hub), embedded in jailopsd.
web/backup      Console of the backup node, embedded in jailops-backupd. Vue 3 + Vite + TypeScript.
web/apps.json  Registry of UI apps: binary, embed package, API role and allowed URL prefixes of each. A UI app
                imports only web/shared and calls only its own prefixes; new apps are added from a template.
```

`jailopsd` runs as an unprivileged user. Everything that needs root (`jail`, `zfs`, `ifconfig`,
`pfctl`, `/dev/bpf`, mounts) is requested from `jailops-priv` through a fixed list of typed
operations. The peer is checked with `LOCAL_PEERCRED` **[verified]** (section 18).

Dangerous parameters (raw jail parameters, host mounts, `allow.*` flags, devfs rulesets, raw
PF, hooks) require the `admin` role, are allow-listed, and are audited. An API admin is root on
the host if those are allowed, and the documentation says so.

## 3. Storage: ZFS layout

```
<pool>/jailops                        jailops:managed=on
  images/<image>/g<N>                immutable generation, snapshot @ro
  projects/<project>/jails/<name>    container dataset, holds spec.json and bundle.json
    root                             clone of images/<image>/g<N>@ro (thin) or full copy (thick)
    etc  local  var  home            persistent datasets (thin layout)
    data/<volume>                    user volumes
  state                              host state files (section 7), mounted at /var/db/jailops/state
  deleted/                           generations still referenced by clones
```

The `state` dataset is a child of the root dataset but is mounted outside the storage root, at
`/var/db/jailops/state` (`jailops-priv init` creates it). The storage root belongs to the helper (root-owned, mode
0700) and the daemon cannot write there; the daemon's state, operational database and keys live under
`/var/db/jailops`. The backup node is keyless and needs no ZFS, so there is no receive area on a host.

Ownership and identity are ZFS user properties: `jailops:managed`, `jailops:id`, `jailops:kind`,
`jailops:gen`, `jailops:manifest`. Objects without `jailops:managed=on` are never touched.

ZFS commands run with `-H -p` parseable output. All calls go through `internal/sys/zfs`
behind an interface, so tests use a fake.

Feature compatibility is checked like this: compare the pool's active features against the vendored OpenZFS `compatibility.d` for the
target release line, return VERIFIED, NO or UNKNOWN, and block only on NO. JailOps never runs
`zpool upgrade`.

## 4. Thin jails and image generations

The thin jail lifecycle is the most important design problem. A ZFS clone is not an image
system: a clone keeps the snapshot it was made from and does not follow later changes to the
origin. Thin-jail systems that share one mutable release directory through nullfs change every jail at once and cannot
roll back. JailOps uses immutable generations instead.

### 4.1 Generations

An image is a line of generations. A generation is a dataset built from pkgbase with a
`@ro` snapshot that never changes. A new generation is built in a staging clone of the previous
one, upgraded, tested and snapshotted. Generations are never edited.

### 4.2 Jail layout

A thin jail's root is a clone of one generation. State that must survive an update is in
separate datasets mounted under the root: `/etc`, `/usr/local`, selected `/var` paths
(`/var/db`, `/var/log`, `/var/mail`, `/var/spool`), `/home`, `/root`, and user volumes. This list is the default of
`lifecycle.persistent` in `jailopsd.yaml` and can be replaced there. Prototype 2 settled the mechanism (section 18); one
question is open: how `/var/db/pkg` behaves after a root swap.

### 4.3 Upgrade (strategy `rebase`)

1. Build the target generation if it does not exist.
2. Preflight: refuse if the jail has snapshots that other clones depend on, if replication is
   running, or if the image `osrelease` is newer than the host kernel.
3. One atomic `zfs snapshot` over all datasets of the jail (the rollback point).
4. Stop the jail.
5. `zfs clone <gen>@ro <jail>/root.new`.
6. Three-way merge of `/etc` with `etcupdate` between the old and new generation. A conflict
   that cannot be merged leaves the jail on the old generation and raises an event. It is
   never guessed.
7. `zfs rename` swaps `root` and `root.new`.
8. Start and run the health check. On failure swap back and start the old root.
   `POST /api/v1/images/{name}/upgrade` (`jailops image upgrade`, extension `image_upgrade`, global admin) does steps 1 to 8
   for a whole image: it builds the next generation from the newest one (keeping nothing when the package set did not
   change), then moves every thin jail that uses the image, in every project or one named project, one at a time through
   the OS update of 8.4, and stops at the first jail that fails or needs a human, so a bad generation is not rolled across
   the host. Pinned jails, thick jails and strategies other than `rebase` are skipped and listed; a stopped jail is moved and
   left stopped.
9. A retired root is parked as `root.stale` until the upgrade has succeeded, then destroyed after the retention period (`root.old` during the upgrade). A generation is destroyed only when no jail
   references it; until then it is renamed into `deleted/` if clones exist, and its origin is
   garbage-collected when the last clone goes. Aliases (`stable`, `latest`) are not references: deleting a
   generation that only aliases point at moves each alias to the newest remaining generation, or removes it
   when none remains. Deleting an image removes all its generations and aliases, and is refused (`image_in_use`,
   naming the jails) while any jail references one of them. Aliases are managed explicitly with
   `PUT|DELETE /api/v1/images/{name}/aliases/{alias}` (`jailops image alias set|delete`, extension `image_aliases`, admin).

### 4.4 Rollback scopes

OS root and application data are different decisions.

| Scope | Effect | Default |
|---|---|---|
| `root` | Swap the root back to the previous generation. Data untouched. | yes |
| `data` | `zfs rollback` of the data datasets. Discards everything written since. | no |
| `all` | Both. | no |

`data` and `all` require `confirm_data_loss` and show `zfs get written` for the datasets.
Data is never rolled back automatically.

### 4.5 Copies

A copy of a jail is a full `zfs send | zfs receive`, not a clone, whenever the source has
snapshots, which avoids dependent-snapshot entanglement. Clones of jails exist only for ephemeral jails.

### 4.6 Big pools and existing data

A host usually has more than one pool. Three things, with different owners:

- **Storage on the big pool.** A dataset of the pool is registered as a storage (`jailops storage create bulk --dataset
  tank/jailops`), granted to a project, and a jail is created on it (`--storage bulk`). Roots, snapshots, clones,
  backups and rollback work as everywhere, under the project's own subtree and quota.
- **Volumes.** A dataset JailOps creates under the jail (`--volume NAME:MOUNT[:ro][:QUOTA]`), snapshotted, rolled back,
  copied and backed up with the jail.
- **Host mounts.** Data that already exists and that JailOps does not own: `host_mounts` in the jail spec
  (`--host-mount SOURCE:TARGET[:ro]`), mounted with nullfs from a host directory. Only a global administrator may set
  them; the helper writes a jail fstab only when every nullfs source lies below an `admin-host-mount` directory the
  operator listed in `priv.conf` and the request carries the admin assertion. The assertion is self-declared by
  the daemon (section 18.6), so **the allow-list is the boundary, not the assertion**: a compromised daemon cannot
  mount anything the operator did not name. Profiles cannot carry them. The mount point is created in the jail's root on every
  render, because a thin root is replaced by an update. JailOps neither snapshots nor backs up such data; a read-write
  mount lets the jail change it, so use `:ro` or ZFS's own snapshots and replication for what matters. Handing a dataset
  to a jail to manage itself (`zfs_delegation`: `jailed=on`, `zfs jail`) is accepted by the API and rendered into
  `jail.conf`, but the helper does not hand the dataset over yet.

### 4.7 What was proven, and what is open

The hardest parts are the `/etc` and `/var` handling and the interaction between pkgbase, `etcupdate`, clones and
rollback. Prototype 2 passed on FreeBSD 15.1 (section 18): upgrade, write new data, `root` rollback leaves the data
intact, `all` rollback warns about data loss. Open: how `/var/db/pkg` behaves after a root swap, and an upgrade of a
debootstrap (Debian) generation, which is refused.

## 5. Networking

One model: **managed VNET bridges with deterministic address allocation**.

- Every jail uses VNET and one or more epair interfaces on a bridge that JailOps created
  (`jo<n>`, description `jailops:<id>`). Shared-IP and host-networked jails are not supported.
- A network defines subnets, a **gateway that is a real address owned by the host**, and an allocation **range**.
  The gateway lives on the host end (`<bridge>gb`) of a gateway epair whose other end (`<bridge>ga`) is a port of
  the bridge; the bridge itself carries no address (section 18.9 explains why: the host's pf sees a jail's traffic
  on the bridge ports, and frames addressed to the bridge's own MAC skip them). JailOps allocates from the range, writes the allocation
  into the jail spec, and configures addresses and the default route inside the jail.
- **No DHCP, no router advertisement, no SLAAC.** The jail gets `accept_rtadv` disabled.
  Ordinary IPv6 neighbor discovery between the jail and the gateway still works; it is part of
  IPv6, not a service.
- Epair names derive from the jail id, MAC addresses derive from the jail UUID. Reconcile
  removes epairs that belong to no jail.
- JailOps never modifies a bridge it did not create.

IPv6 has two modes:

| Mode | When | Behaviour |
|---|---|---|
| `routed` | The upstream routes a prefix to the host | The bridge owns the prefix. No proxy. **Must work fully without the proxy.** |
| `proxy` | The upstream treats the addresses as on-link (switched /64) | JailOps answers neighbor solicitations for every address it has allocated to a running jail on the uplink with the host's MAC, announces each one when it appears, and routes `/128` host routes through the bridge. The proxy always runs for a network in this mode; there is no switch for it. What it may do on the uplink is the operator's approval in `priv.conf` (§18.6, §18.10). |

In `proxy` mode the uplink is never a member of the jail bridge, and the network gets no on-link copy of the
upstream /64: the gateway is a link-local address (`fe80::1`) on the gateway interface, and each jail address is
reached through a `/128` host route. **[verified]** on FreeBSD 15.1 amd64 (section 18.10).

The proxy is built in, pure Go, BPF on the uplink with a session state machine
(incomplete, valid, stale, invalid) and a bounded subscriber queue. It is designed from RFC 4861
and from published descriptions of the unicast neighbor unreachability problem. No code is copied from any existing NDP proxy.
JailOps knows every address it allocates, so it can answer without learning.

Acceptance of `proxy` mode, with the result on the amd64 test host (a shared switched /64, jails in a `/80`; section
18.10 and 18.11 have the measurements):

1. `route -n get -inet6 <jail>` selects the gateway interface: **met**.
2. The host never sends a neighbor solicitation for a jail address out of the uplink: **not measured**; the routes
   point at the gateway interface and nothing suggests otherwise.
3. The upstream router keeps reaching the jail after its neighbor entry expires and NUD runs: **met**. A 50 minute
   ping from another machine lost only the first minute's packets, while the entry went stale and was refreshed many times.
4. Replies leave with the right source and are not blocked by PF: **met**; the PF traffic matrix passed with the proxy running.
5. The host's own connectivity is unchanged throughout: **met** for its address and SSH sessions; not tested: removing
   and recreating the uplink while the proxy runs.

Not verified: providers that filter by MAC address or limit neighbor discovery, hundreds of proxied addresses on one
uplink, MLD-snooping switches.

### 5.1 Address pools and carving networks

`project create NAME --new-network` draws a subnet for the project's own network from a host-level pool, so nobody
computes addresses by hand:

- **IPv4 (implemented):** `projects.network_pool` in `jailopsd.yaml` (for example `10.64.0.0/12`) is carved into
  `/24` networks (`network_bits`), one per project, behind NAT if `projects.nat` is set. Without a pool the option
  answers `unsupported`.
- **IPv6:** a routed prefix (for example the `/64` a cloud provider routes to the host) is carved by the operator into
  `/80` networks, one per bridge or project (`jailops network create ... --ipv6 <addr>/80`): 65,536 networks per `/64`,
  2^48 addresses each. A prefix longer than `/64` is fine because JailOps never uses SLAAC or router advertisements;
  addresses are allocated and configured statically. Automatic carving of IPv6 and a reject route for the unallocated
  rest of a routed prefix (so packets to addresses no network uses are dropped instead of looping between the host and
  the provider) are **not implemented**.
- The gateway is a real address on the gateway interface (see above).
- The host must forward (`net.inet6.ip6.forwarding=1`, `net.inet.ip.forwarding=1`). **JailOps never sets forwarding**:
  the administrator enables `gateway_enable` and `ipv6_gateway_enable` in `rc.conf`, and `jailops doctor` checks that
  forwarding is on and persistent. (The daemon sets only the bridge `pfil_*` sysctls and `kern.elf64.fallback_brand`.)
- A host with a proxy-mode network should answer the unicast neighbor solicitations the segment's routers send from
  their global, off-link addresses (`net.inet6.icmp6.nd6_onlink_ns_rfc4861=1`; the default drops them and the router
  marks the host unreachable for minutes at a time). JailOps never sets it either: `jailops doctor` warns (the setting is recommended)
  about `sysctl.net.inet6.icmp6.nd6_onlink_ns_rfc4861` while a proxy-mode network exists, `sysctl.persistent` warns until it
  is in `/etc/sysctl.conf`, and the answer of `network create` carries the same warning.
- Verified on a cloud test host: the provider routes the whole `/64` to the host, and a source address taken from
  a second `/80` makes a round trip.

### 5.2 Firewall

PF only. One anchor per jail, `jailops/<project>/<jail>`. Tables are scoped to their anchor: each anchor defines
every table it references, and there are no shared tables (a reference to another anchor's table passes validation
and resolves to nothing; verified on FreeBSD 15.1). `pfctl -n` also accepts a rule that uses an undefined table, so
the renderer proves that every referenced table is defined. Runtime table entries are lost when an anchor is
reloaded, so contents are rendered into the anchor. The
administrator adds three lines to `pf.conf` once (`nat-anchor`, `rdr-anchor`, `anchor`). pf insists on an order: `nat-anchor` and `rdr-anchor` belong with the translation rules, before the first `block` or `pass` rule, and `anchor` among the filter rules (appending all three to the end of a file that already has filter rules fails with "Rules must be in order"; found on a host with a real ruleset). Reload with `pfctl -f /etc/pf.conf`. JailOps
never owns `pf.conf`. A ruleset is rendered, validated with `pfctl -n`, loaded in one operation
and read back. There is no read-modify-write. Raw PF requires `admin`.

## 6. Jails, Linuxulator and Alpine

`jail(8)` creates and removes jails from a rendered per-jail `jail.conf` (every value escaped). Besides the path,
the hostname, the vnet interfaces, devfs and the hook commands, every `jail.conf` sets
`exec.consolelog = "/var/log/jail_${name}_console.log"` (a file on the host, FreeBSD and Linux jails alike): jail(8)
then sends the output of the `exec.*` commands (the hooks, `/etc/rc` and `/etc/rc.shutdown`) to that file instead of the
pipes of the helper that ran `jail -c` or `jail -r`. Without it a daemon that an rc script starts and that keeps its
descriptors (daemon(8) without `-f`) holds the helper's pipes open after `jail -c` has exited, and the start fails or
hangs waiting for the end of that output. The command runner also treats a command that exited successfully while a
leftover process still held its output as a success (the output after its exit is dropped). The file is not rotated
and stays when the jail is deleted. A jail with `sysvipc` (a key of the jail spec, set by an app definition that asks for
it) gets `sysvmsg = new; sysvsem = new; sysvshm = new;`: System V message queues, semaphores and shared memory of its
own, separate from the host's and every other jail's (never `inherit`, which would share the host's; `allow.sysvipc`,
which means the same, stays an admin allow-list flag and is refused together with `sysvipc`). The helper accepts
exactly these lines and nothing else for those parameters. **[UNVERIFIED on FreeBSD: both, see section 16]**
Running state is read with `jail_get(2)` **[unverified from pure Go: SYS_JAIL_GET in x/sys]**.

Linux jails use the Linuxulator. Alpine is the first Linux target, Debian follows. What runs in them is bounded by
the Linuxulator, with a compatibility matrix that is a set of automated tests, not a promise:

- No systemd as PID 1. Services are started directly or by a small init wrapper.
- No netlink: no `ip`, no DHCP client. The host configures the network.
- Required mounts: devfs with a ruleset, linprocfs, linsysfs, fdescfs, tmpfs on `/dev/shm`.
- Update strategy is `inplace` (apk or apt inside the jail), snapshot first. Whether Alpine can
  use `rebase` (rebuilding from `/etc/apk/world`, or a derived image) is open.
- Addresses set on an epair **before** it is moved into the jail's vnet are lost. The host configures the interface
  and routes inside the jail after the move with `ifconfig -j <jail>` and `route -j <jail>` (verified, section 18).

## 7. State, storage of data and reconcile

Four layers, one duty each:

| Layer | What | Where |
|---|---|---|
| Jail | Data, `spec.json`, `bundle.json`, `jailops:*` properties | The jail's own datasets |
| Host configuration | Networks, policies, address sets, backup policies, remotes | JSON files in `<state_dir>/objects/<kind>/<id>.json` (`/var/db/jailops/state`) |
| Operational data | Users, token hashes, sessions, operations, events, audit log, backup catalog cache | SQLite (`modernc.org/sqlite`, WAL, one writer) |
| Operating system | `jail.conf`, in-jail `rc.conf`, PF anchors, bridges, epairs | Derived. Never edited by hand. |

SQLite holds nothing required to restore a jail. Users, tokens and the audit log are the only
non-rebuildable rows; they are copied with `VACUUM INTO` before upgrades and on a schedule.

Files are written with temp file, `fsync`, `rename`, `fsync` of the directory, keeping a
`.previous` copy. One writer per state directory
(`flock`). Every object has a `generation` used as its ETag. A ZFS snapshot of the state dataset
is taken before destructive operations.

Everything that changes state goes through `state.Apply(Command)`. A command is deterministic:
the proposer generates ids, timestamps and random values and puts them in the command. This keeps
a later Raft log possible without a rewrite.

Reconcile has two inputs, desired and observed, and one rule: **desired state never contains
results from `ifconfig`, `zfs` or `pfctl`.** The reconcile loop observes the system, renders the
desired artifacts (pure functions, golden-tested), computes a plan, applies it with per-resource
locks and verifies the result. Failures are recorded as `status.conditions`, never by changing the
spec.

### 7.1 Authority

| Scope | Authoritative | Local files |
|---|---|---|
| `host` and jail manifests | ZFS on the host | truth |
| `fleet`, cluster mode | Raft | projection, rebuilt from the Raft snapshot |
| `fleet`, no cluster (single host or hub) | local files | truth |

An object has exactly one authority at a time. Enabling cluster mode imports fleet files as
initial commands; leaving the cluster exports them back.

## 8. API

REST under `/api/v1`: sync and async responses, an operations resource with
`wait`, `ETag`/`If-Match` on updates, `/api/v1/events` as Server-Sent Events (the SDK is standard library only, and a websocket needs a third-party package; exec uses an HTTP upgrade; the browser console uses a session of POST requests and server-sent events), capability negotiation by
`api_extensions` strings instead of version numbers. Types are in
`sdk/api`; the TypeScript types of the web consoles are generated from them (`just gen-ts`). There is no OpenAPI document and no JSON Schema file today (planned); the Go types and `sdk/PROTOCOL.md` are the specification.

Authentication: one identity system over every transport. Unix socket: peer uid 0 (root) is a global admin (the host's recovery
access), every other local peer is anonymous like an unauthenticated remote client; no Unix user or group maps to a role.
Users, tokens and certificates are the identities. mTLS for hosts and hubs (certificate fingerprints pinned), API tokens `jot_...` (hash stored,
shown once), sessions with CSRF for the web UI (argon2id, TOTP, recovery codes; WebAuthn and OIDC after 1.0). People and
hosts are never mixed. Every route has an entry in `perms.go`; a route without one makes the
daemon refuse to start.

Resources: server, projects, storage, images and generations, jails, snapshots, volumes, networks, firewall
policies and address sets, backup targets, policies and backups, operations, events, remotes,
tokens, trust tokens, certificates, users, storage, groups, rollouts and the application catalog (read-only;
section 8.7).

### 8.1 Projects, networks and storage

Three separate resources. **A project is a logical namespace; networks and storage are independent,
host-managed resources. A project does not create them, it is granted access to them.**

- **Network** (section 5): a managed bridge with address ranges.
- **Storage**: a named ZFS location on a host (`jailops storage create fast --dataset tank/jailops`). Jail and
  volume datasets are created under it. ZFS only; there is no storage abstraction layer. Clones need the
  image generation on the same pool, so each pool that holds thin jails gets its own copy of the generations
  it uses (built or received once per pool).
- **Project**: owns jails, volumes, firewall policies, address sets, snapshots and backup policies. Its spec
  grants networks (a network is granted to one project only) and storage (with a quota per storage), and sets quotas
  for jails, CPU, memory, PIDs and addresses, an optional mandatory firewall policy and an image allow-list.

```
jailops network create br-web --ipv4 10.50.0.1/24 --ipv6 fd50::1/64
jailops storage create fast --dataset tank/jailops
jailops project create web
jailops project set web --network br-web --storage fast
jailops create nginx --project web --image freebsd@stable --network br-web --storage fast
```

**There is no default project, no default network and no default storage.** A fresh host has no project at all.
Every request about something a project owns (jails and everything under a jail, project profiles, address sets,
firewall policies, backup policies) names its project with `?project=<name>`; without one the server answers
`400 a project is required`. Only host-level objects (images, networks, storage, projects, identities, host
profiles, audit) need none. A jail is created with a network and a storage named explicitly, in the jail or in
one of its profiles; nothing is filled in from the project. Explicit is the rule because a default is state
somebody has to remember, and the first thing a second project would break. (The storage a host is set up with
is named `default`; it is an ordinary storage, granted to projects and named by jails like any other.)

**A network belongs to one project.** Granting a network that another project already holds is refused
(`network br-web is already granted to project db`), and a grant is the whole network: there are no shared bridges
and no per-project sub-ranges. Many projects may share one ZFS pool: each gets its own dataset tree under
`<storage root>/projects/<project>/` and its own quota. Images and host profiles are shared by every project;
they are immutable or administrator-owned. `jailops project create web --new-network` is the convenience that
creates a dedicated bridge and subnet.

On the client, `jailops project switch web` stores the current project per remote in the client configuration,
`--project` and `JAILOPS_PROJECT` override it, and `jailops project list` marks the current one. With none chosen
a command that needs a project stops before it sends anything. Names are unique per project and kind, UUIDs are
global.

**A fresh host, from nothing to a jail:**

```
jailops network create br-web --ipv4 10.50.0.1/24 --uplink em0 --nat
jailops project create web
jailops project set web --network br-web --storage default
jailops project switch web
jailops create nginx --image freebsd@stable --network br-web --storage default
jailops start nginx
```

Before `project create` and `project switch`, `jailops create nginx` fails with `a project is required`; after the
project exists it fails with `a jail needs a network` until one is named.

**Projects across hosts.** A project may span hosts: `hosts: [freebsd01, freebsd02]`. The project definition
and its access rules belong to the fleet (the hub, or a fleet repository applied by `jailops-sync`); the
resources are local. `web` on both hosts is one project, but its network `web` and storage `fast` are
implemented separately on each host, and neither L2 networks nor storage are shared by that. The hub
reconciles the definition onto each listed host (same UUID, `managed_by: hub`); each host stores and
enforces its own copy, and an offline host catches up when it returns, so no consensus is needed. In
cluster mode the definition is replicated like any other fleet object. Every host named in a project must
have the networks and storage the project names; the plan (hub or `jailops-sync`) reports a missing one
before anything is applied. Quotas are per host. Fleet-wide aggregate quotas need coordination and are not
part of 1.0.

**Isolation is enforced, not just filtered in the API.** Projects share a pool and nothing else on the network side;
they are kept apart by mechanisms that exist in the system:

| Layer | Mechanism |
|---|---|
| Network | A network belongs to one project. Its jails never exchange frames: every jail port is a private bridge member and every flow goes through the host (design 18.13). Each jail's anchor binds its addresses (anti-spoofing), and the host pins the jail's MAC address, ARP and neighbor entries, so a jail cannot take another's address. A project's mandatory policy is rendered before the jail's own. Cross-project traffic is denied unless a policy allows it. |
| Storage | Each project has its own dataset under the storage root with a quota; each jail a quota and optionally a reservation. ZFS delegation (`zfs_delegation`) is never granted to project users unless a global admin allows it per jail, and the helper does not hand datasets over yet (section 4.6). |
| Compute | Per-jail limits through jail parameters and rctl; project quotas are checked before creation. What a jail uses is measured when asked (`GET /api/v1/jails/<n>/usage`, `jailops jail info`): processes, CPU time and memory from racct through `rctl -u jail:<key>` (a read-only form on the helper's command allow-list, for managed jails only; "racct is off" when `kern.racct.enable` is not 1), and the space of its datasets from ZFS `used`/`referenced`. Nothing of it is stored, and the jail list does not carry it. |
| Identity | Section 8.2. |

A separate dataset is **not** a full security boundary. Jails share the host kernel, the sysctl state, device
rulesets and pool I/O. For a high-security tenant use a dedicated pool, a dedicated host, or both; the
documentation states the isolation levels plainly.

ZFS layout: `<storage root>/projects/<project>/jails/<name>`. PF anchors are
`jailops/<project>/<jail>`. Backup groups are keyed by project and jail identity. Deleting a project requires
it to be empty.

### 8.2 Trust and authorization

Identities are **certificates** (pinned client or host certificates), **tokens** and **users** (people
using the web UI). Every identity has a role and a project scope:

| Field | Meaning |
|---|---|
| `permission` | `readonly` < `operator` < `admin` (plus the narrow backup roles) |
| `projects` | The projects the identity may act in |
| `global` | Explicit access to every project and to host-level objects (with `admin`). Granted only by a global admin and shown in the UI and the audit log |

An identity must have a non-empty `projects` list **or** `global: true`. **An empty project list never means
access to everything:** creating an identity with neither is refused, and a stored identity with neither can do
nothing. This is deliberately different from systems where an empty list means "all".

Rules:

- Host-level objects (networks, storage, images, projects, users, certificates, tokens, remotes, server
  configuration) need a **global admin**. A project-restricted identity can use only the
  project-level objects of its own projects, and only the networks and images granted to them.
- Authorization is checked twice: the route needs the role (the `perms.go` table, which still makes the
  daemon refuse to start when a route has no entry), and **the object must belong to a project the
  identity may access**. Lists, events, operations, audit entries and backups are filtered by project; an
  identity never learns that an object in another project exists (it gets `not_found`).
- **Trust workflow** (no trust on first use, no manual certificate copying):
  1. An unrestricted admin runs `jailops trust add --name laptop --role operator --projects web` (or `--global`).
     The server returns a single-use **trust token** bound to that name, with an expiry (default 1 hour),
     carrying the server's certificate fingerprint and a secret.
  2. On the client, `jailops remote add host.example --token <token>` pins the server fingerprint from the
     token, generates a client key pair, and presents the token with the client certificate.
  3. The server consumes the token and records the certificate fingerprint with the role and projects.
  4. `jailops trust list`, `trust revoke <fingerprint>` manage the store, and `jailops cert` edits a certificate. Revocation takes
     effect on the next request. Certificates may carry an expiry.
- The unix socket: root (uid 0) is a global admin, the host's break-glass and recovery access (`sudo jailops user create NAME --admin`,
  `user set-password`, `user set-role`). Every other local peer is anonymous: it reaches what an unauthenticated remote client reaches
  (`GET /api/v1`, `GET /healthz`, login, trust redemption, the static console) and may authenticate with a token or a login over the
  socket. The socket is mode 0666 because connecting confers nothing. A test requires the same answer for every route from an
  anonymous remote and an anonymous local caller. `jailops-priv` keeps Unix peer credentials: it is a service boundary.
- **Hub forwarding:** the hub authenticates and authorizes the caller (role and projects), then forwards
  over pinned mTLS with a signed requestor claim (identity, role, projects). The host checks the claim
  against its own project scoping. A compromised hub certificate can claim any identity, so hub
  certificates are scoped as `hub`, never accepted on the unix socket and never given global
  rights by default; this trade-off is documented.
- Failed authentication is rate-limited and audited. Sessions can be listed and revoked. WebAuthn and OIDC
  come after 1.0.
- Authorization tests are generated from the route table and the object model: for every route, every
  role and both an in-project and a cross-project identity, the expected result is derived and checked.

### 8.3 Configuration keys, devices and profiles

The CLI is small and predictable, and it is the same for one host and a fleet:

```
jailops project switch web          # client-side: the current project of the current remote
jailops list                        # also: list 'web*' -c name,state --sort age, -o csv, -o compact
jailops create nginx --image freebsd@stable --network br-web --storage fast --start
jailops stop -l env=prod            # by label selector; --all for every jail of the project
jailops exec nginx -- /bin/sh       # also: jailops shell nginx
jailops config show nginx
jailops config set nginx limits.memory 2G limits.cpu 2
jailops network list ; jailops storage list
jailops apply -f site.yaml          # create or update every document of a file
```

- **Client-side state.** `project switch` sets the current project of the current remote in the client
  configuration, never state in the daemon; with none set, project-scoped commands ask for `--project`. `remote switch` and `project switch` are independent and each
  remote remembers its last project. `--project` and `--remote` always override.
- **Configuration keys.** Dotted keys (`limits.memory`, `limits.cpu`, `boot.autostart`, `boot.priority`,
  `network.eth0.mtu`, ...) are the interface of `config set/get/unset/show/edit`. They are not a second
  data model: a registry in `sdk/api` maps every key to a field of the typed spec, with its type,
  validation, documentation and whether a change needs a restart. The CLI, the web UI, `jailops-sync` and
  the daemon all use that registry, so a key exists in exactly one place.
- **Declarative.** `config set` updates the desired state through the API and the daemon enforces it. If a
  change needs a restart the operation says so (`restart_required`) and the CLI prints it; JailOps never
  restarts a jail silently, and `--restart` is an explicit request.
- **Devices.** A jail's hardware-like attachments are named devices with their own configuration:
  `nic` (a network attachment: network, addresses, MAC, MTU, optional rate limit), `disk` (a volume mount
  of a ZFS dataset, quota, read-only; host path mounts need admin), `devfs` (exposure of selected device
  nodes through an allow-listed devfs ruleset, for example `tun` or `pf`), and `proxy` (a port forward
  through the jail's PF anchor). Per-device configuration is changed like any key
  (`jailops config set nginx nic.eth0.mtu 1400`) and an override replaces only the named key of the named device.
- **Profiles.** A profile is a named, reusable set of configuration keys and devices (limits, devices,
  boot policy). A jail lists profiles in order; later profiles override earlier ones and the jail's own
  keys and devices override all of them, device by device and name by name. `jailops config show --expanded`
  shows the merged result and where each value came from. Profiles belong to a project or to the host.
  They are the host-side counterpart of fleet templates.
- **Exec and shell.** `jailops exec nginx -- cmd` and `jailops shell nginx` run `jexec` through the daemon,
  with a pseudo-terminal when requested, over a framed stream carried on an HTTP upgrade (the SDK is
  standard library only). It works locally and through a hub with the same trust and project rules, and it
  needs no SSH server inside the jail. Exec is audited.
- **Logs.** `GET /api/v1/jails/<n>/logs` lists the files below `/var/log` of a jail root (one directory deep;
  rotated copies included, compressed ones listed but not readable; at most 4096 files from 64 subdirectories, and the
  header `X-Jailops-Truncated: true` when the listing was cut) and `GET .../logs/<file>` reads the end of one
  (`tail`, `since`, `grep`, `offset` for following; at most 1 MiB per request), running or stopped, through two
  read-only helper operations (`fs.list_logs`, `fs.tail_file`) that accept only the log directory of a jail root
  and plain names below it, follow no symlink below the jail root and read regular files only. A stopped jail's
  root is mounted only while it runs, so its logs answer `busy` ("not mounted") with the dataset to mount. Logs
  hold what services print, so reading them needs the operator role (the one that may exec) and is audited;
  `jailops jail logs NAME [FILE]` (also `jailops logs`) is the client, `-f` follows. Extension `jail_logs`.
- **Manual changes and GitOps.** `config set` on an object that `jailops-sync` owns is allowed and audited,
  and `jailops-sync plan` reports it as drift and reverts it on apply when the repository declares that key. Only
  what the repository declares is compared (verified on the real host: a changed `hostname` that the file declares is
  reported and reverted, a `limits.memory` the file does not mention is left alone). To have a value enforced,
  declare it.

### 8.4 Managed updates

Updates are a lifecycle feature, not a cron script. Each jail has an update policy:

```
updates:
  mode: managed        # manual | notify | managed   (default: manual)
  schedule: "0 3 * * 0"
  packages: true       # pkg upgrade inside the jail
  os: true             # a newer image generation (root swap, design 4.3)
  reboot: manual       # whether a jail restart that an update needs is applied automatically
```

- **manual**: nothing happens unless the user runs it. **notify**: JailOps checks on the schedule and reports available
  updates in the CLI and web UI. **managed**: JailOps applies updates on the schedule. Automatic updates are off
  until a user chooses `managed`.
- Two kinds of update are kept apart. *Package updates* run `pkg upgrade` inside the jail from the host. *OS updates*
  move the jail to a newer image generation by swapping its ZFS root (section 4). Root rollback is not data rollback
  (4.4), and the update report says so.
- The update pipeline is always: policy, schedule, atomic ZFS snapshot, update, health check, result and history,
  with automatic root rollback when the health check fails and an `update.failed` event.
- Commands: `jailops update list`, `update check [--all|--project p|jail]`, `update apply <jail>...|--project p`,
  `update history <jail>`. `jailops image upgrade IMAGE` (4.3) runs the OS update of every thin jail of an image after
  building its next generation. Fleet rollouts (section 12) apply updates in batches with a health gate; a project is
  the natural batch.
- `jailops-sync` can declare the policy. A policy is part of the jail spec and shows as drift when changed by hand.

### 8.5 Shell and provisioning

- **Shell.** `jailops shell <jail>` runs `jexec` with a shell chosen in this order: `--shell`, the jail's `shell`
  setting (a path inside the jail, a management setting that does not touch `/etc/passwd`), then `/bin/sh`. If the
  chosen shell does not exist in the jail the command fails with a clear error; it never falls back silently.
  `jailops set <jail> shell /usr/local/bin/bash` is `config set` for that key and `jailops-sync` can declare it.
- **Provisioning** is a short, repeatable creation step, not a configuration-management system:

```
provision:
  packages: [bash, nginx]
  files:
    - source: templates/nginx.conf
      destination: /usr/local/etc/nginx/nginx.conf
  services:
    enable: [nginx]
    start: [nginx]
  commands: []          # run once, as argv, no shell
```

  JailOps knows how to do each step on FreeBSD (`pkg`, files with owner and mode, `sysrc`, `service`) so the
  user does not write those commands. Alpine uses an OS provider that understands `apk` and the init of that
  jail. Steps run in order, once per jail, and provisioning is then marked complete in the status; there is no
  agent in the jail and no continuous convergence. Changing the declaration later does nothing until the user asks
  for `jailops provision <jail> --again`. Files come from the project, never from arbitrary host paths.

### 8.6 High availability, deliberately last

CARP gives hosts on one layer-2 network a shared address. Many target environments (routed IPv6, cloud networks)
have no shared layer 2, and CARP restarts no jails and moves no data. JailOps therefore does not build failover on
CARP; it may offer CARP as an optional networking feature later. Failure recovery is designed around explicit
health, placement and fencing rules, and no automatic promotion of stateful workloads happens without fencing. This
is a direction under discussion, not a capability.

### 8.7 The application catalog

An **app** is a definition of a jail that runs packages: which packages (FreeBSD repository or `pkg.jailops.net`),
which rc.d services and knobs, which ports, where the data, the configuration and the logs live, how to check that it
runs, what to tell the person afterwards. The catalog is a directory of strict YAML files installed by the signed
`jailops-apps` package (`apps.catalog_dir`, default `/usr/local/share/jailops/apps`); **the daemon reads local files and
never fetches a catalog.** An invalid definition is listed with its reason and is never installable; that includes a
definition written for a newer daemon (an unknown key, format version or category). The definitions are not in this
repository: they live in the packages repository (`main/jailops-apps/apps/`), and `jailops-apps` has a version of its own,
released by a pull request there without a JailOps release; `jailops` depends on it by name, any version. That CI checks
them with `jailops app validate`, the daemon's validator run offline by the client. A reference copy for the tests is in
`internal/apps/testdata/catalog/`. `GET /api/v1/apps`
and `/api/v1/apps/<name>` are readable by any authenticated identity (route class `self`). The format, the rules and the
installation (`POST /api/v1/apps/<name>/install`, operator in the project, extension `app_install`: one operation,
a thin jail from the only FreeBSD image with the alias `stable` unless one is named, with the data paths as volumes, `pkg -r` from the host through the helper, rc.conf
written through the helper's file operations, setup commands and services through the in-jail exec, health check,
snapshot `app-installed`, labels `jailops.app`, `jailops.app.hash`, `jailops.app.version`; a failure after the first
start keeps the jail stopped for inspection) are in [apps.md](apps.md). A definition may ask for System V IPC
(`sysvipc: true`, carried onto the jail spec, section 6); a service is started through a fixed `/bin/sh -c` wrapper
that sends the rc script's output to a temporary file in the jail, so a daemon the script leaves behind does not hold
the in-jail exec's pipes. UNVERIFIED on FreeBSD: the installation of the apps other than nginx; the end-to-end test
installs nginx, and the others when `JAILOPS_E2E_APPS` names them.

The host's own definitions live in a second directory, `apps.local_dir` (default `/usr/local/etc/jailops/apps`),
read with the catalog on each request: a local definition is listed with `source: local` and wins over a packaged one
of the same name (`overrides: catalog`), so an author tries an app on a host without a package. Two tools help write
one (apps.md, "Write your own app"): `jailops app new` writes a commented starter offline, and `jailops app export`
reads `GET /api/v1/jails/<name>/app-draft` (operator, extension `app_draft`, audited), a draft made from a running
jail: the packages installed on purpose (`pkg -r <root> query -e "%a = 0" "%n %v %R %w"` on the host, one exact form
on the helper's allow-list, `RestExact` of the `pkg` rule), the services and knobs of its `rc.conf`, and, through the
in-jail exec, `sockstat`, the names of files below `/usr/local/etc` and `/var/log` newer than pkg's database (`find`)
and the owners of the app's directories (`stat`). It never reads a configuration file and leaves a secret-looking
rc.conf knob out. Both write files that validate, with `TODO` markers that `jailops app validate` reports per line.

## 9. Backup

First-class and a release gate for 1.0. Backup and replication are different features with different
trust models and are built separately.

- **Backup** stores a jail on an untrusted server as an encrypted, deduplicated chunk repository.
  The host chunks, compresses and encrypts; the key never leaves the host; the server holds no key.
- **Replication** (section 9.8) moves ZFS streams between hosts you trust, for migration and warm standby.

### 9.1 Model

Each backup is a **self-contained snapshot made of references to chunks**. There are no incremental
chains to break. A new backup uploads only chunks the repository does not have yet. ZFS is used to get
a consistent view (an atomic recursive snapshot), not as the transport.

Repository objects (same key space on a directory and on S3):

```
datastore.json                 format version, id, created        create-if-absent
owner.json                     the backup node that owns it        create-if-absent
chunks/aa/bb/<id>              one encrypted chunk blob            immutable, write-once
quarantine/<id>                a corrupt blob moved out of chunks/
escrow/<domain>.<ext>          the master key wrapped to offline recovery recipients
groups/<group>/<snap>/index    plaintext list of (end offset, chunk id) with a checksum trailer
groups/<group>/<snap>/manifest encrypted JSON: jail spec, restore bundle, per-dataset stats, bases
groups/<group>/<snap>/commit.json  receipt, written last and create-only
groups/<group>/<snap>/{protected,verify.json,notes}
```

A snapshot exists if and only if its `commit.json` exists. Commit order: pin every chunk of the index,
check that every chunk exists on the backend itself (never from a cache), write index, then manifest,
then `commit.json` last. A retried commit with identical bytes is success. Writes are create-only
(`If-None-Match: *` on S3, link or exclusive rename on a filesystem), so a host cannot overwrite.

Group ids are keyed hashes of the jail identity, so the tree carries no names. A snapshot has one
concatenated index; its manifest records where each dataset's range starts, so one commit covers all
datasets of the jail and garbage collection and the receipt stay single-object.

### 9.2 Chunks

- **Chunk id** = HMAC-SHA256 of the plaintext chunk with a key derived from the master key, computed
  before compression and encryption. It is keyed, so the server cannot confirm that it holds known data.
- **Files** are cut with content-defined chunking (FastCDC, 256 KiB minimum, 2 MiB average, 8 MiB
  maximum, a fixed gear table that is part of the format). Files smaller than 256 KiB are packed in walk
  order; pack boundaries depend only on file names so that adding a file does not shift later packs.
  The catalog is cut separately from the data.
- Block-device chunks (fixed 4 MiB) are for ZFS volumes, after 1.0.
- Compression is zstd, applied before encryption and skipped when it does not help. All-zero ranges are
  recorded in the index without a blob. Decompressed size is capped.

### 9.3 Encryption and keys

- One **master key** per repository. HKDF-SHA256 derives the chunk-encryption key, the chunk-id key,
  the manifest key and a non-secret domain id. Chunks use XChaCha20-Poly1305 with a random 24-byte nonce
  and associated data that binds the chunk id and the domain. Blob layout: magic, version, flags, nonce,
  ciphertext, then an unkeyed SHA-256 trailer.
- The server verifies blobs with the **trailer only**. That detects corruption and torn writes without a
  key. A deep verify run by a host that has the key decrypts and recomputes the chunk id, which also
  detects a self-consistent blob planted under a wrong id (chunk-id poisoning). Deep verify is scheduled
  on a host that holds the key or on the recovery host.
- The key is a file on the host (mode 0600), optionally wrapped with Argon2id. At repository creation a
  recovery key is shown once. `jailops backup key escrow` wraps the master key to one or more offline recovery
  recipients (age public keys); the blob is not secret and is stored in the repository. Restoring on a new
  host needs only a recovery key.
- No in-place master-key rotation: rotating means a new repository (the old one stays readable).
- **What the server sees**: object counts and sizes, the deduplication graph, chunk lengths, snapshot
  times, and host and jail identity in the control plane. It never sees file names, contents or keys.
  The documentation says so.
- Anything that needs plaintext (restore, restore tests, deep verify, browsing) is a job on a host that
  holds the key.

### 9.4 Capture

1. `zfs snapshot -r` over the jail's data datasets (atomic), the jail is not stopped (crash-consistent;
   an optional pre-hook can quiesce an application).
2. Read through `<mount>/.zfs/snapshot/<name>`.
3. **L0 change detection**: compare the live tree with the previous catalog (kind, mode, uid, gid, size,
   mtime and ctime in ns, inode, link target, extended attributes and ACLs). A run of files is reused only if
   every file matches and the old bytes are contiguous; the old chunk ids are copied and the files are not
   read.
4. **L1 accelerator (later)**: `zfs diff -FHt <previous> <current>`, valid only when the previous
   snapshot's guid matches the previous manifest. Any doubt falls back to L0. A fuzz test requires that the
   diff-driven result equals a full walk.
5. Any doubt means a full read, which is still deduplicated. A full read is also forced when there is no
   usable previous catalog, the previous snapshot failed verification, or the last full read is older than a
   configured age.
6. The catalog (per entry: kind, name, mode, owner, times, inode, extended attributes and ACLs, size,
   payload offset and SHA-256) is chunked and encrypted like data. It drives change detection, browsing,
   single-file restore and per-file hash checks.

FreeBSD specifics are written here, not borrowed: extended attributes through `extattr_*`, NFSv4 ACLs,
file flags. Whether ctime and inode are stable across ZFS snapshots is **[unverified]** and decides how far L0
can be trusted.

### 9.5 Roles and scopes

- **Jail host**: `backup` scope. May check for chunks and upload chunks, indexes, manifests and request a
  commit. Writes are create-only, so it cannot overwrite or delete.
- **Backup node** (`jailops-backupd`, a separate binary): keyless. Owns sessions, commit, retention, garbage collection,
  verification and quarantine. Its storage is a directory or S3 and does not need ZFS. Administration is
  through a local socket or CLI only.
- **Recovery host**: read-only scope plus the recovery key. Runs restore tests and deep verify.
- Sessions are leases (15 minutes, renewed by activity), one open session per jail. A session pins every
  chunk it saw present or uploaded; the previous snapshot is pinned while a session is open.

### 9.6 Retention, garbage collection, verification

- Retention is a pure function over snapshot times and the policy (last, hourly, daily, weekly, monthly,
  yearly), run only by the backup node. The newest snapshot is never removed and protected snapshots are
  kept. An unknown policy keeps everything.
- GC marks by reading every committed index and aborts if any index is unreadable. It sweeps only
  unreferenced chunks older than a grace period (default 24 h), re-checking pins and age per batch under a
  write lock. Nothing is touched to keep it alive.
- Verification is keyless (trailers, index and manifest digests against the receipt, chunk existence), each
  chunk read once per job. A corrupt chunk marks every snapshot that references it as failed. Corrupt blobs
  are quarantined and healed by the next upload. A failed snapshot is never a base for an incremental run.
- Maintenance modes (online, read-only, offline) are enforced by a backend wrapper beneath the object model.
- The backup node's control plane is **PostgreSQL** (pgx/v5), the only control-plane store of `jailops-backupd`:
  hosts and enrolments, sessions, the snapshot index, jobs and their results, policies, maintenance mode, users,
  tokens and the audit log. The repository (directory or S3) stays the source of truth for payload and for the
  snapshot and chunk relations, and **reconcile rebuilds the snapshot rows from it**. Users, policies, enrolments
  and the audit log are not recoverable from the repository and are backed up with the database. One open
  session per jail group is enforced by a partial unique index. The per-host daemon keeps SQLite; there is no
  SQLite or in-memory fallback in the backup node.

### 9.7 Restore

- Whole jail: the manifest says which datasets to create; chunks stream through a read-ahead window with
  parallel workers; each blob is decrypted and its id recomputed; memory does not depend on jail size.
- Single file or directory: fetch only the catalog sub-range, map the file's payload range through the index
  to a few chunks and check its SHA-256. Extraction never follows symlinks and refuses unsafe paths.
- **Restore closure**: the manifest carries the bundle (jail spec, image generation and manifest hash,
  volumes, frozen copies of the networks, policies and address sets the jail referenced, sealed secret
  references). Restore starts with a preflight that lists what is missing and what would change and does
  nothing until accepted. `network_map` and `keep_ip` are explicit.
- **Restore tests** on a schedule (`deep`: decrypt every chunk; `extract`: restore to a scratch directory and
  check hashes; `full`: restore into a scratch dataset on an isolated network and run the jail's health check).
- A restored jail on a new host gets a new UUID unless `replace` is set.
- Release gate: a jail is restored on a different host from a backup node, with data, spec, network,
  firewall and secrets, by an automated test.

### 9.8 Replication (trusted hosts)

ZFS send and receive between hosts that trust each other, for cold migration and a warm standby. It uses
incremental chains with guid continuity, bookmarks, holds and resume tokens, never `recv -F` over existing
snapshots, and the ZFS feature compatibility check before each send. Raw sends of encrypted datasets keep
data encrypted in transit and at rest on the target. This is not backup: a compromised source can corrupt
a replica, which is why backup is a separate, keyless, append-only repository.

### 9.9 Remote storage

The node's datastore is a backend behind one interface (memory, directory, S3-compatible). S3 relies on
atomic create-only writes, listing with prefix and continuation, range reads and idempotent delete; the
node probes these at repository creation and refuses a service that overwrites despite the condition.
The S3 client is written against the standard library (SigV4), tested against an in-process strict fake with
fault injection and a conformance suite that is also run against a real S3 service on demand.

## 10. Multi-host

Hosts are independent and authoritative for their own jails. A **hub** is a mode of `jailopsd`:
it keeps a registry of remotes (address, pinned fingerprint), forwards requests over mTLS and
aggregates the UI. If the hub is down the hosts keep working. Moving a jail between hosts is cold:
snapshot, send, stop, incremental send, start, verify. There is no live migration, and JailOps
never assumes shared storage.

## 11. Cluster mode (Raft)

Raft is part of 1.0, as milestone M7, after the single-node lifecycle is solid. The data model is
cluster-aware from the first milestone: UUIDs, `scope`, `generation`, versioned commands.

- Raft replicates `scope: fleet` metadata only. Jail execution, ZFS and the network stay native
  to each node.
- Raft is authoritative for fleet objects in cluster mode (section 7).
- The FSM applies commands deterministically and does no I/O toward the operating system.
  Observed state travels outside the log.
- Local-only operations keep working in a minority partition: start, stop, restart, exec,
  snapshot and rollback of own datasets, scheduled backups under an accepted policy, reconcile
  from the last accepted state. Operations that change shared state are rejected: create, delete,
  rename or move a jail, allocate from a shared range, change fleet networks, policies, users,
  tokens, roles, rollouts, and join or leave.
- No automatic failover of jails. Healing that moves jails off local storage without fencing risks split brain. JailOps offers `jailops recover <jail> --from <backup|replica> --on <host>`
  with an explicit operator statement that the source host is dead.
- Design rules: single-use join tokens bound to a member name with an
  expiry and the cluster certificate fingerprint; a separate cluster certificate; `pending` members
  skipped by heartbeats; join rejected on schema or extension mismatch; the older member waits
  during upgrades; offline is not dead; quorum loss is recovered by an explicit command on one
  surviving node.
- **The Raft library is hashicorp/raft with raft-boltdb (prototype 7, 18.5); the rest of this paragraph is how it was chosen.** Candidates: hashicorp/raft with raft-boltdb (pure Go), etcd-io/raft, other pure-Go libraries, and cowsql (CGO). Prototype 7
  runs a three-node FSM with the leading candidates through crash, restart, partition, leader change,
  compaction, recovery after quorum loss, and member add and removal. Correctness, recovery and
  maintainability outweigh avoiding CGO; if CGO is needed it is isolated behind a build tag and the
  default binary stays `CGO_ENABLED=0`.

## 12. Fleet sync

`jailops-sync` is a declarative layer: a git repository of YAML and the commands `plan`, `sync` (`--apply` makes the
hosts match), `drift`, `watch`, `rollout`, `adopt`, `prune-check` (never deletes), `render`, `validate`, `doctor`,
`secrets`, `remote` and `vault`, a JSONL apply log, and the rule that
objects not owned by sync are never changed or deleted. It is built only on `sdk` and `sdk/api`;
a test enforces that it imports nothing from `internal/` outside the fleet layer. There is no
scheduler: a jail is declared on a named host.

**Secrets.** Two mechanisms, which can be mixed:

- `secrets/<name>.age`: one age-encrypted secret per file, for the API tokens of hosts and the hub
  (`token_secret`) and for jail environment values `secret:<name>`. The ciphertext is sent to the host
  as it is stored (`sealed:age:...`), so the host that holds the key opens it.
- SOPS: `shared/secrets.sops.yaml` (any YAML; every value encrypted, the keys stay readable) and
  `hosts/<host>/remote.sops.yaml`, both governed by `.sops.yaml` at the repository root, which lists
  the age public keys that can decrypt (the recipients) and which fields each rule encrypts. A jail
  environment value `secret:app.db_password` (a dotted path; an age secret name never has a dot) is a
  value of the SOPS file. Sync decrypts it in memory only when it applies and sends it sealed to the
  age recipients of `secrets/recipients.txt` (the host's key), never in clear; the host must advertise
  `sealed_secrets`. Planning needs no key: a declaration is hashed with a digest of the stored
  ciphertext of that value, so a plan sees a changed secret and an unchanged one without decrypting
  (sealing is randomised and could not be compared).

The key for SOPS files is found through SOPS's own variables only: `SOPS_AGE_KEY` (key text),
`SOPS_AGE_KEY_FILE` (an identity file, which may hold an age plugin identity so that a hardware token,
an agent or a secret manager supplies the key) and `SOPS_AGE_KEY_CMD` (a command that prints the key).
Sync has no cryptography of its own and no other source; the implicit `~/.config/sops/age/keys.txt` is
not counted as configured. `jailops-sync vault status|list-recipients|add-recipient|remove-recipient`
edits the recipients in `.sops.yaml` and re-wraps the encrypted files for them (removal is revocation
only after the re-wrap, and a removed key may already have read the current values: rotate them).

**Reaching a host.** A host is reached with a token (`token_secret` in `host.yaml`), through a hub
(`via_hub`), or with a client certificate from `hosts/<host>/remote.sops.yaml`: `url` and
`server_fingerprint` readable, `client_cert` and `client_key` encrypted. The connection is pinned
mTLS; there is no trust on first use. `jailops-sync remote bootstrap <host> --url ... --fingerprint ...`
makes the key pair, writes the file encrypted (the private key never touches the disk), and prints the
`jailops cert create` command an administrator runs on the host to trust the certificate. Both
credentials may be present for one host.

`validate` checks, offline and without a key, that every SOPS file is encrypted, that `.sops.yaml` has a
rule for it, that remote files are well formed and agree with `host.yaml`, and that every
`secret:<path>` names an existing path. `doctor` shows which key variable is set and, with `--deep`,
proves that the key opens every SOPS file.

## 13. SDK

`jailops.net/jailops/sdk` is a separate Go module with no third-party dependencies. It holds
the wire types (`sdk/api`), a typed client with `context` everywhere, async operations with `Wait`,
range-over-func iterators for lists and event streams, ETag-aware `Modify`, stable error codes
(`errors.As(err, &api.Error{})`), capability checks from `api_extensions`, and `sdk/fake`. The CLI
and `jailops-sync` use nothing else.

## 14. Build, test, release

- Go 1.27 or newer, `CGO_ENABLED=0`, cobra for the CLI, `version.txt` and `-ldflags -X main.version`.
- Justfile recipes: `build`, `build-freebsd`, `build-all`, `fmt`, `test`, `test-race`, `test-freebsd`, `web-all`,
  `web-test`, `gen-ts`, `check-ts`, `check`, `ci`, `snapshot`, `clean` (see the Justfile for the rest).
- Three test layers: unit tests with fake `zfs`, `jail` and `pfctl`; integration tests on a real
  FreeBSD host or VM behind a build tag (they fail, they never skip, when FreeBSD is absent in an
  integration run); fuzzing of parsers.
- Packaging: FreeBSD `pkg`, three packages (`jailops-client`, `jailops`, `jailops-backupd`) with an `rc.d` script each, for the
  FreeBSD 15 ABI (section 18.4).
- License: BSD-2-Clause. No code is copied from GPL or Apache projects; designs are reused,
  code is written here.

## 15. Milestones

| | Scope | Done when |
|---|---|---|
| 0 | Repo, CI, nine prototypes (`prototypes/README.md`) | All nine have an answer |
| M1 | Core: generations, thin and thick jails, lifecycle, snapshots, rollback, state, daemon and privileged helper, API and SDK, CLI, pkg and rc.d | Create, start, snapshot, upgrade and roll back a jail on a clean FreeBSD host |
| M2 | Network and firewall: bridges, VNET, IPAM, routed and proxy IPv6, PF anchors, doctor, web UI | A jail with IPv4 and IPv6 on a switched network, behind a firewall |
| M3 | Alpine and Linux jails | Alpine with sshd and nginx survives restart and rollback |
| M4 | Backup: chunk repository, host-side encryption, backup node, retention, GC, verify, restore, restore tests, S3; replication between trusted hosts | Restore on another host. A compromised host cannot delete or overwrite backups |
| M5 | Hub: mTLS, remotes, extensions negotiation, cold move, shared UI | One UI manages ten hosts; one host failing affects no other |
| M6 | Fleet: jailops-sync, rollouts, age secrets, adoption of live JailOps objects (`adopt --claim`) | A fleet repository applies to three hosts with a zero-diff plan after |
| M7 | Cluster mode (Raft) | Three nodes; one isolated node keeps running jails; quorum loss and recovery tested; rolling upgrade |

1.0 ships when M1 to M7 pass their tests. Each milestone is released as 0.x.

## 16. Status register: what was assumed, and what is settled

Every assumption of the design, with what settled it and where the evidence is (section 18). **Settled** means it ran on
FreeBSD 15.1; the evidence section says on which architecture and with what limits. Anything still open is in the second
table. UNVERIFIED markers elsewhere in this document stay until the item they name moves here as settled.

| # | Assumption | Settled by | Status |
|---|---|---|---|
| 1 | `jail_get`/`jail_set` are callable from pure Go through `x/sys/unix` | Prototype 1 | Settled (section 18, 1) |
| 2 | pkgbase + `etcupdate` + clones + rollback work together; the persistent path list is sufficient | Prototype 2 | Settled, except `/var/db/pkg` after a root swap (open, below) |
| 3 | PF anchors load atomically and coexist with the administrator's `pf.conf` | Prototype 3 | Settled (18, 3; order of the three `pf.conf` lines in 5.2) |
| 4 | `zfs send -R` of clones, raw send of encrypted datasets and bookmarks work for replication | Prototype 4 | Settled for the primitives (18, 4); replication between hosts has no API or CLI yet (open, below) |
| 5 | NDP proxy on a switched /64 (MAC choice, unicast NUD, MLD, routing, host connectivity) | Prototype 5, redone through the helper | Settled on amd64 (18.10, 18.11); limits in section 5 |
| 6 | Alpine runs under the Linuxulator; addresses survive the epair's vnet move | Prototype 6 | Settled on arm64 (18, 6); Debian in 18.7 |
| 7 | Choice of Raft library | Prototype 7 | Settled: hashicorp/raft (18.5); cluster mode itself is not built (M7) |
| 8 | `LOCAL_PEERCRED` peer checks, BPF ioctls and `x/net/route` work from pure Go on FreeBSD | Prototypes 1 and 5 | Settled (18, 1; 18.10) |
| 9 | ctime and inode are stable across ZFS snapshots of a live dataset | Prototype 9 | Settled (18, 9 capture) |
| 10 | `zfs diff -FHt` is reliable enough to drive the accelerator | Prototype 9 | Settled with limits (hard links are not reported by name); the accelerator is open, below |
| 11 | Extended attributes, NFSv4 ACLs and file flags round-trip through capture and restore | Prototype 9 | Settled, with the dataset properties named in 18 |
| 12 | A restored thin jail comes up from the repository | Restore tests | Settled (18.3, 18.4) |
| 13 | Per-jail PF anchors isolate jails of different projects on a bridge | Prototype 8 and the PF traffic matrix | Settled for IP traffic (18, 8; 18.9). Ethernet-level protection (ARP, NDP and MAC spoofing, rogue router advertisements) is open, below |
| 14 | The capture assumptions of 9.4 hold | Prototype 9 | Settled (18, 9) |

Open:

| Item | What is missing |
|---|---|
| Native ZFS encryption (`--encrypted`) | Not implemented: safe key handling (a root-owned key file in a helper-approved directory, loaded at start through the helper) is unbuilt, so the flags were removed from `jailops jail create` and `jailops storage create`. `spec.encrypted` / `storage.spec.encrypted` in a file or API call answer `unsupported` ("native ZFS encryption of jails is not implemented" / "encrypted storage is not implemented") with the manual alternative in the hint |
| Packages at creation (`--package`, `spec.packages`) | Not implemented: the flag was removed; the field answers `unsupported` ("installing packages at creation (spec.packages) is not implemented"). Use the provisioning `packages` step (8.5), which runs once at the first start |
| Jail freeze | Removed. FreeBSD has no jail freeze; the `freeze` action no longer exists in the SDK, the fake or the service (an unknown action answers `invalid`) |
| Volumes of an existing jail | `GET/POST /api/v1/jails/<n>/volumes` and `DELETE .../volumes/<v>` answer `unsupported` ("volume management is not part of this version"); `jailops config` on `disk.<name>` keys of an existing jail answers `unsupported` ("changing volumes of an existing jail is not implemented"). Declare volumes at creation |
| `inplace` update strategy for thin jails | `jailops jail upgrade` answers `unsupported` ("update strategy inplace is not implemented for thin jails"); thick and Linux jails are updated through managed updates, not rebased ("rebase upgrades apply to thin jails; ... jails update in place, which is not implemented") |
| Routes that answer `unsupported` | `PUT /api/v1/server`, `GET /api/v1/server/config`, cluster, `GET .../console` (listing sessions), `.../files/<path>`, `POST .../move`, `.../snapshots/<s>` (rename), `.../bundle`, jail templates, groups, rollouts and the backup transport routes. Each answers 501 `unsupported` ("<feature> is not part of this version"); the list is `unsupportedRoutes` in `internal/server/routes.go` and `sdk/PROTOCOL.md` |
| `/var/db/pkg` after a root swap | A dedicated experiment with a genuine package change |
| Ethernet-level spoofing | `ether` rules did not evaluate on the test host; ARP, NDP and MAC spoofing are not blocked below IP |
| amd64 beyond the proxy host | The clean-install end-to-end run passes on amd64; Linux jails, backups and most prototypes ran on arm64 only |
| FreeBSD 15.2 and later | Each new minor release must pass the PF traffic matrix and the clean-install run before it is called supported |
| The `zfs diff` accelerator | Not built (L1 capture); the stat walk is used |
| Replication between hosts | The primitives work (`internal/replicate` is a library); no API route or CLI command exists, so nothing a user can type replicates a jail |
| Upgrades between JailOps versions | Not tried |
| Rate-limit shaper | Limits are stored and reported; nothing enforces them, and no error says so |
| Devfs ruleset loading by the helper | Rendered; the load through the helper is not verified |
| Jail logs through the helper | `fs.list_logs` and `fs.tail_file` are tested against real directories, symlinks and a fifo on the development host and over the socket; reading a running jail's `/var/log/messages` on a FreeBSD host, a stopped jail (unmounted root: `busy`), a jail with `/var` on its own persistent dataset, and a rotation under `-f` have not been run there |
| Jail usage (`rctl -u`) | The parser follows the `resource=amount` format of rctl(8) (`maxproc`, `cputime`, `memoryuse`) and the helper rule is tested with fakes; neither has run against a host with `kern.racct.enable=1`. Interface counters are not reported: FreeBSD's `ifconfig` prints none, and `netstat -I` would need a rule of its own |
| Proxy (port-forward) redirects on the uplink | Rendered; not verified |
| `zfs_delegation` | Accepted (global admin only) and rendered into `jail.conf`; the helper does not hand datasets over, so the jail does not see them and no error says so |
| `sysvipc` and the console log | `sysvmsg/sysvsem/sysvshm = new` and `exec.consolelog` are rendered, accepted by the helper and tested against fakes; PostgreSQL's `initdb` in a jail with them, and a forgejo start (daemon(8) without `-f`) no longer holding `jail -c`, have not been run on a host |
| App service start wrapper and package ownership | The `/bin/sh -c` start wrapper is tested with a real `sh` on the development host, not in a jail; the `chown` setup lines of the catalog (`pkg -r` leaves the directories of a package's user root-owned) are written from the ports' layout and not yet run |
| App drafts (`jailops app export`) | The parsers of `sockstat -4 -6 -l -q`, `find ... -newer /var/db/pkg/local.sqlite`, `stat -f "%Su:%Sg %N"` and `pkg query -e "%a = 0" "%n %v %R %w"` (that `%R` is the repository a `pkg -r` installation recorded) are tested on written samples and the helper rule against the real allow-list; none of them has run in a jail on a host. The worked example of apps.md (memcached: the `memcached_flags` knob and the pid directory `/var/run/memcached` given to `nobody`) has not been installed on a host |
| Catalog apps `isms`, `nsd`, `mail` | Validated by the catalog test (on the reference copy) and dry-run against the daemon with fakes; never installed on a host. isms: port 8080 and its answer, and an install from the `jailops` repository with `pkg -r`; nsd: package paths, rc.d name and the starter configuration; mail: package paths, Dovecot 2.4 syntax, PAM logins, Postfix's `set-permissions` after `pkg -r` (docs/apps.md, the catalog) |
| Helper audit pid | The helper's audit lines do not carry the daemon's pid (`pid: 0`) |
| Generic `zfs send`/`receive` through the helper socket | About 0.4 MB/s; cause not found, bypassed by `zfs.replicate` |
| `pkg.jailops.net` | The repository host is not live; packages were installed from a local repository |
| Cluster mode (M7) | Not built |
| IPv6 into a jail from the internet over a routed prefix | The test host has no IPv6 route to it |
| Providers that filter by MAC address or limit neighbor discovery; hundreds of proxied addresses | Not tried |
| Fleet examples on a real host | `examples/` are applied to in-memory hosts and checked with the daemon's validators; no example has been applied to a real host. `build: true` (first image build through `jailops-sync`) and an IPv6 network created by sync are fake-tested only |
| `examples/provisioning/bootstrap.sh`, `nuageinit` user-data | Syntax-checked; not run on a fresh VM or a cloud provider. Provisioning `pkg install` inside a pkgbase jail is unverified, and the fixed order packages, files, services, commands means a private pkg repository must be installed with commands |
| Unix socket identity (root = global admin, other peers anonymous) | Tested in process with fabricated peer credentials and a real socket as a non-root user. Not yet run on FreeBSD: the uid that `LOCAL_PEERCRED` reports for `sudo jailops ...`, for a non-root user, and for an ssh-forwarded socket; `temp/verify-identity.sh` is the prepared check |
| API exposure on a real network | TLS 1.3 only, certificate reload, a configured certificate never generated over and the web switch are tested in process; a public listener, a CA-signed certificate with renewal, and a proxy or Cloudflare tunnel in front are not tried. ACME, a require-client-certificate mode and the caller's address behind a proxy are not implemented (docs/exposure.md) |

## 17. Acknowledgements

The backup repository (section 9) and `jailops-sync` (section 12) build on ideas and code from
`incus-backup` and `incus-sync`, earlier projects by the same author. Code adopted from them has been
adapted to JailOps's names, scope and rules, and the parts specific to virtual machines have been dropped.

## 18. Verified on FreeBSD 15.1 (arm64 and amd64 test hosts)

Results of the prototypes and runs so far. The architecture is named where it matters (most of this ran on arm64; the
proxy and the amd64 clean-install run on amd64). Everything not listed here is unverified.

| Prototype | Result |
|---|---|
| 1 jail API | `jail_set`, `jail_get` and `jail_remove` work from pure Go through `unix.Syscall` with `SYS_JAIL_SET/GET/REMOVE` from `x/sys`; `persist` is a zero-length value; results equal `jls`; enumeration with `lastjid` equals `jls`; `LOCAL_PEERCRED` works through `GetsockoptXucred`. Lowering a jail's `securelevel` from the host is not refused by the kernel (do not rely on it). |
| 2 thin upgrade | **The thin-jail design works on FreeBSD 15.1.** `pkg -r <dir> install FreeBSD-set-minimal-jail` builds a usable userland in an empty ZFS dataset, provided the signing key directories (`/usr/share/keys/pkgbase-<major>`) are copied into the new root first, because `pkg -r` resolves fingerprint paths inside the root. The repository for a RELEASE is `pkg+https://pkg.FreeBSD.org/${ABI}/base_release_<minor>` (the minor of the image's release is written out: pkg expands `${VERSION_MINOR}` from an ABI that has no minor and answers 0) (`base_latest` is for -STABLE and -CURRENT), the ABI comes from `pkg config abi` (`FreeBSD:15:aarch64` on arm64), and the keys path from `pkgbase-${VERSION_MAJOR}`. A thin jail whose root is a clone of `g1@ro` with persistent datasets (`/etc`, `/usr/local`, `/var/db`, `/var/log`, `/var/mail`, `/var/spool`, `/home`, `/root`) seeded from the image starts, runs `pkg install` and passes a health check. `etcupdate` works on pkgbase generations through tarballs: `etcupdate extract -d <work> -t <old-etc.tar> -D <jail root>` then `etcupdate -d <work> -t <new-etc.tar> -D <jail root> -L <log>`, where each tarball holds `./etc` of a generation; it prints `M <path>` for merged and `C <path>` for conflicting files and exits non-zero on conflict. Disjoint edits merge cleanly. Swapping roots with `zfs rename` and starting on the new generation works. **Root rollback keeps data written after the upgrade; `all` rollback restores root and every persistent dataset from the single atomic snapshot and discards the data written since (`zfs get written` reports it).** Writes outside the persistent paths vanish with the root. |
| 3 PF anchors | Anchors load, replace and flush without touching the administrator's ruleset. Replacement is atomic (200 reloads under sampling, never empty or mixed). A syntax error is rejected by `pfctl -n` and by the load, and the old ruleset stays. **Not** rejected: a rule that uses an undefined table. Tables are anchor-local and runtime entries are lost on reload. |
| 4 ZFS replication | A plain send of a clone is self-contained. `send -R` of a tree recreates the clone relationship. A bookmark works as the incremental base after the source snapshot is destroyed. Guid comparison detects forks. `recv` refuses an existing destination without `-F` and an incremental from a forked base. **An incremental on top of a receiver that has a newer snapshot is accepted without `-F`**, so JailOps must enforce guid continuity itself and never rely on `recv` to refuse. `zfs hold` blocks destroy and `destroy -d` defers. Resume tokens (`recv -s`, `send -t`, `recv -A`) work. A raw send of an encrypted dataset needs no key on the receiver and the key can be loaded later. A non-raw send of an encrypted dataset arrives as plaintext. ZFS is 2.4.2. |
| 6 Alpine | **Alpine runs as a jail on FreeBSD 15.1 arm64.** The Linuxulator reports Linux 5.15.0. With the minirootfs (v3.22, aarch64, musl) as the jail root: `apk update` and `apk add openssh nginx` work, nginx serves a page and sshd answers with its banner to the host, and ZFS snapshot, breaking the root and `zfs rollback` restores a working system (`apk --version` works again). Details that shape the design: (1) **ELF branding.** Alpine's binaries are built for musl and carry no Linux note; busybox happens to be branded Linux but `apk`, `nginx`, `sshd` and others carry OS/ABI 0. The kernel refuses unbranded binaries, and `execvp` then falls back to `/bin/sh`, which is busybox, hence the confusing `applet not found` and `Exec format error`. Branding files at build time is not enough, because `apk add` installs new unbranded binaries at any time. **JailOps therefore manages `kern.elf64.fallback_brand=3`** (unbranded ELF runs as Linux) whenever a Linux jail exists, and `doctor` checks it. Native FreeBSD binaries carry a FreeBSD brand and are unaffected (verified on the host). A pure-Go equivalent of `brandelf -t Linux` exists as a fallback for build time. (2) **`/dev/shm` and the emulation path.** devfs refuses to create `/dev/shm`. The Linuxulator resolves paths through `/compat/linux` first, and as soon as that directory exists in the jail root a file create below it fails with `ENOENT` (apk could not lock its database). The working layout mounts a **nullfs mirror of the jail root on `<root>/compat/linux`**, creates `dev/shm` through the mirror and mounts a tmpfs there. Mount order: devfs (with a private ruleset), fdescfs, the nullfs mirror, tmpfs on `compat/linux/dev/shm`, linprocfs, linsysfs. (3) **Network.** A Linux userland has no netlink-based tooling that works here; the host configures the jail: `ifconfig -j <jail> <if> inet <addr>/<n> up`, `ifconfig -j <jail> <if> inet6 <addr> prefixlen <n>` and `route -j <jail> add default <gw>`. Linux sees the interface as `eth0` (names are mapped, `compat.linux.use_real_ifnames=0`) with the right address. (4) **Bridges and epairs are created under generic names and renamed**: `ifconfig bridge create` then `ifconfig bridgeN name <name>`; `ifconfig <name> create` fails with `Invalid argument`. (5) `inet6` takes `<addr> prefixlen <n>`; `inet6 <addr>/<n>` in the same command as `inet` was rejected. Not yet verified: amd64 (the test host is arm64), glibc distributions (Debian), `apk` running as a provisioning step from the host, behaviour under load. |
| 8 isolation | Verified on FreeBSD 15.1 with ordinary tools (no packet forging; active attacks such as forged ARP, NDP, router advertisements and DHCP are intentionally not automated). (1) **The `private` bridge-member flag blocks all direct member-to-member traffic, including between jails of the same project on the same subnet** (ICMP, ICMPv6, TCP all fail with it on and all pass with it off). It is therefore only usable for networks where every flow goes through the host (separate subnets, routed addressing); a shared bridge that lets a project's jails talk to each other leaves it off and relies on PF. (2) **Nested anchors are evaluated only when every intermediate anchor contains `anchor "*"`.** The administrator's `pf.conf` has `anchor "jailops/*"`; JailOps loads `anchor "*"` into `jailops/<project>` and the project rules there. Without it a rule in `jailops/<project>/<jail>` is never evaluated (reachable before and after; blocked as soon as the parent has the wildcard). (3) **PF filters traffic between projects on a shared bridge**: with `private` off and project anchors of the form `block return quick from <A_own> to <B_own>` / `pass quick from <own> to <own>`, cross-project traffic was blocked and intra-project traffic allowed under all four `pfil_*` combinations tried (member 0 or 1, bridge 0 or 1, onlyip 0 or 1). (4) **Source-address enforcement per jail works with a table, at the member interface.** Without a rule a jail that aliased another address and sourced `ping -S` from it reached a neighbouring jail; with `table <jail_addrs> persist { <v4>, <v6>, fe80::/10, :: }` and `block in quick on <epair> from ! <jail_addrs> to any` loaded into the jail's anchor it was dropped and the rule's counters showed the hits. A negated list (`from ! { a b }`) is a syntax error, so the renderer always emits a table; the table must include the link-local prefix and `::` or IPv6 neighbor discovery and duplicate address detection break. A rule on the bridge interface (`block quick from <addr>`) also worked with `pfil_bridge=1`, but cannot say which jail sent the packet, so per-jail enforcement needs **`pfil_member=1`**. (5) Ethernet-level protection (ARP, NDP and MAC spoofing) is **not** covered by these tests and needs `ether` rules; it stays open. (6) Link-local multicast and broadcast probes were inconclusive. |
| 9 capture | **Snapshot metadata is trustworthy.** Mode, uid, gid, size, mtime, ctime, inode, extended attributes, NFSv4 ACLs and file flags read through `.zfs/snapshot/<name>` equal a live `stat` taken just before the snapshot (80 of 80 files, with a writer running in the same dataset). **inode and ctime are stable** between two successive snapshots, across `zfs rollback`, and (observed) across a clone and a `send \| recv`, so the stat-walk change detection of design 9.4 may rely on both. The first read of a new snapshot waits for the automount (0.5 to 1.7 s observed): capture polls until the directory is readable. `snapdir` is `hidden` by default; listing 60 snapshots takes microseconds. **A file opened under a snapshot that is then destroyed fails with `ENXIO` ("device not configured")**: capture holds the snapshot (`zfs hold`) until it is done and treats `ENXIO` as a failed run. |
| 9 zfs diff | `zfs diff -FHt` prints **absolute paths**, reports the dataset root `.` as modified whenever its entries change, and is keyed by object, not by name: **a new hard link name is not reported as an addition, and removing one of several link names is not reported as a removal** (observed as 4 of 20 disagreeing batches after normalising paths and the root). The L1 accelerator therefore rescans every directory it reports as modified (which finds new and removed names, including hard links) and always rescans entries with a link count above one. Everything else compared equal. |
| 9 round trip | A snapshot tree archived and extracted by an independent archiver reproduces content and hard-link groups exactly. NFSv4 ACLs round-trip **only when the target dataset has `acltype=nfsv4`, `aclmode=passthrough` and `aclinherit=passthrough`** (with the default properties the extra ACL entries are lost): the manifest records these properties and restore creates the datasets with them. Directory mtimes are changed by extracting their children, so extraction ends with a final pass that sets directory times, deepest first. ctime and inode necessarily differ in the restored tree. |

Design consequences of prototype 2:

- Image building copies the host's `/usr/share/keys` into the new root before the first `pkg -r` call, derives the repository URL from the host release (`base_release_<minor>` for a RELEASE, `base_latest` otherwise) and the ABI from `pkg config abi`.
- `etcupdate` cannot merge the account databases when both sides add a user (conflicts in `passwd`, `master.passwd` and `group`). JailOps merges those three files itself with a keyed three-way merge by user and group name (a conflict only when the same key changed differently on both sides), then runs `pwd_mkdb`; `etcupdate` is told to leave them alone. `rc.conf` is rendered by JailOps and is never merged.
- A root rollback leaves `/etc` in the state merged for the newer generation while the root is the older one. Root rollback is therefore the upgrade plan run towards the previous generation, which merges `/etc` in the reverse direction.
- Open: `/var/db/pkg` holds the package database of base and application packages. If it is persistent, `pkg` reports the old base set after a root swap; if it is not, application packages disappear from the database. Needs a dedicated experiment with a genuinely newer base (prototype 2b) before the persistent path list is final.

Design consequences of prototype 8:

- The daemon-managed bridge sysctls default to `net.link.bridge.pfil_member=1`, `net.link.bridge.pfil_bridge=0` and `net.link.bridge.pfil_onlyip=1` (configurable). With member filtering per-jail rules are written `on <epair>` and project rules stay interface-independent.
- The PF renderer never emits a negated address list, always defines the tables an anchor references, includes `fe80::/10` and `::` in a jail's allowed sources, and emits `anchor "*"` in every project anchor.
- `private` is a per-network option, off by default, documented as "all traffic goes through the host".

### 18.1 First end-to-end run of the daemon (FreeBSD 15.1 arm64)

`jailops image build freebsd15` (pkgbase), `jailops create nginx --project web --image freebsd15@stable
--network br-web`, `jailops start nginx --project web` and `jailops exec --project web nginx -- uname -a` ran
through jailopsd in dev mode with the in-process helper, and the jail started through `jail(8)` with the
`jailops-priv hook` scripts. The jail has 10.50.0.2/24 on the managed bridge, reaches the gateway and, through
the NAT anchor, the internet; stop and start repeat cleanly. What the run found, all fixed:

- A pkgbase image has no `/usr/local`; seeding a persistent dataset from a directory the image does not ship
  now starts the dataset empty.
- Setting the final mountpoint of the persistent datasets mounts them over the root; the create plan unmounts
  them (reverse order) before it unmounts the root.
- `pfctl` refuses to modify anchors whose name begins with an underscore: the reserved anchors are now
  `jailops/net.<network>` and `jailops/shared.pf` (a dot cannot be part of a project name).
- `pfctl -sn -sr` in one call prints only the last listing: NAT and filter rules are read separately.
- `pfctl -a <anchor> -F Tables` fails with "Operation not supported by device" once the anchor is empty and gone:
  tables are flushed first and that one error is accepted.
- `pkg` prints `Setting ABI requires setting OSVERSION` on stderr: the package list parser skips `pkg:` lines.
- The helper accepts the daemon's work directory (image builds) and `pkg install -r <repo>`, `pkg check -d`.
- The hook runs the helper as a separate process: it needs `/usr/local/etc/jailops/priv.conf` (a sample is in
  `packaging/`) and the binary at `helper-path`.

A second run built the Alpine 3.22.1 minirootfs image (arm64) through the daemon and helper, created and started
an Alpine jail on `br-web` (`uname -sr` inside: `Linux 5.15.0`; it reaches the gateway) and ran commands in it
through `jailops exec`. It found: the `/dev/shm` backing directory belongs in the real tree (`<root>/dev/shm`,
reached through the nullfs mirror), not below `compat/linux`; the `compat/linux` mount point must exist before the
mirror is mounted; `fdescfs` takes `linrdlnk` and `late`; the private devfs ruleset range is accepted in a
`jail.conf` without an allow-list entry; the build jail has fixed command forms in the helper (admin only); the
helper's admin assertion now follows an authenticated global admin actor.

This first run used dev mode (in-process helper), so the helper socket, `kldstat` from the daemon (doctor reported
"cannot be checked here"), the exec TTY path and the Alpine flow through the daemon were left for 18.2.

### 18.2 Helper socket mode, exec with a terminal, rc.d

`jailops-priv` ran as root on its socket (`group = operator`, `mode = 0660`: the helper refuses a mode that gives
"others" access) and `jailopsd` ran as the unprivileged user `operator`, not in dev mode. List, stop, start,
`exec` and `shell` worked in a FreeBSD and in an Alpine jail through it, and the `rc.d/jailops_priv` script's
`checkconfig`, `start`, `status`, `reload` and `stop` worked. It found:

- FreeBSD has no `/dev/ptmx`: a pseudo-terminal is opened with the `posix_openpt` system call and the slave is
  named with `FIODGNAME` (`/dev/pts/<n>`). `exec -t` and an interactive `shell` (tty, window size, exit status
  of the command) work in both jails.
- Doctor needed read-only helper forms it did not have: `kldstat -q -n <module>`, `pfctl -s rules|nat` of the main
  ruleset (to look for the `jailops/*` anchor lines) and the `kern.racct.enable` sysctl. They are on the
  allow-lists now; doctor is clean on the test host except for the backup extensions (no `backup:` section) and the
  missing zfs compatibility file.
- The helper's peer credentials log `pid 0` (the pid is not read from the socket): the uid is what the
  allow-list uses. UNVERIFIED: whether the pid can be had on FreeBSD for the audit log.

Settled later: glibc distributions (18.7), prototype 5 (18.10), prototype 7 (18.5), the backup flow (18.3, 18.4) and
amd64 (18.10 and the clean-install run). Open: see section 16.

### 18.3 Backups with an unprivileged daemon: file agents

A backup reads every file of a ZFS snapshot (some are readable by root only) and a restore writes files of any
owner, but `jailopsd` is not root. The helper therefore starts a **file agent** for the daemon (`fs.agent`, admin
only): a copy of the helper binary (`jailops-priv fsagent -mode read|write -root DIR`) that changes its root
directory to `DIR` and serves a small framed protocol (internal/fsagent) on a socket pair whose other end the
daemon receives by descriptor passing. The backup engine and the extraction run unchanged on top of it: the client
is a `catalog.ScanFS` (read) or a `catalog.Sink` (write).

What the helper checks before it starts one:

- **read:** `DIR` is `<mountpoint>/.zfs/snapshot/jailops-backup-*`, the mountpoint is that of a dataset inside a
  storage root that carries `jailops:managed=on` *locally*, and the snapshot carries it too (the backup made it).
- **write:** `DIR` is the mountpoint of such a dataset whose `jailops:complete` is `off`, that is a jail that is
  being restored. A finished jail and a storage root are never written through an agent.
- at most 16 agents run at once; an agent has no core file, can only do the operations of its mode, and sees
  nothing but `DIR`.

Verified on the FreeBSD test host with the daemon as an ordinary user: a backup of a running jail through read
agents (the second one uploads 1.3 KiB), and a restore through write agents that gave the restored jail the owners,
modes and the 0600 `master.passwd` of the original; the restored jail started and reached its gateway. The same
operations also ran as root in development mode. Findings of that work:

- A backup policy is looked up by name **and project**; two projects may use the same policy name (before, the
  first one found was run).
- The target address `https://host:port` is accepted as documented (the node client wants `host:port`).
- A restored jail needs `jailops:layout` and `jailops:image` on its container, `jailops:complete=on` before it is
  registered (registering observes it), and its `auto` addresses allocated; all three were missing.
- `jailops label JAIL KEY=VALUE... KEY-` sets the labels a backup policy selects by.

UNVERIFIED: the agents under a hostile client beyond the unit tests and the fuzz run (the chroot is the confinement
of last resort, there is no Capsicum yet), amd64.

### 18.4 Packages and a fresh install

The FreeBSD packages (`jailops-client`, `jailops`, `jailops-backupd`) are built and signed by the separate repository
`jailops-packages` with PkgOps (no FreeBSD builder: Go cross-compiles on Linux), published to
`pkg.jailops.net/<ABI>/latest` through GitHub Pages and archived in GitHub Releases. `scripts/pkg-build.sh` and
`scripts/pkg-repo.sh` (FreeBSD, `pkg create` and `pkg repo`) are the old path, kept for a transition and removable once
the first JailOps packages have been published from there. GoReleaser (`.goreleaser.yaml`) makes the release archives
with the static binaries and the Linux packages of the client and the backup node.

`scripts/ci-freebsd-e2e.sh` is the proof, and it is what CI runs in a FreeBSD VM: on a clean machine it builds the
packages, installs them, runs `jailops-priv init -pool POOL`, starts `jailops_priv` and `jailopsd` through rc.d (the
daemon as the unprivileged user `jailops`), builds an image, creates a network and a project, starts a jail and
checks it from inside, installs PostgreSQL, sets up the backup node and enrols the host, backs the jail up, verifies
the backup (deep and extract), stops the jail and restores the backup as a new jail that has the file, a new address
and starts. It passed on the FreeBSD 15.1 arm64 test host. What it and the trial installs found:

- `rc.subr` treats `<name>_user` as "run the command as this user with su": the daemon's variable is
  `jailopsd_runas`, and the variables rc.conf may override are read before `command_args` is built.
- The helper's file operations must reach the daemon's work directory (image builds write their repository
  configuration there), and removing a build's tree is a recursive `fs.remove_all` that the helper allows only for
  scratch trees (below the work directory, and `.seed-*` and `.merge-*` directories of a jail container).
- `/var/run/jailops` is shared by the helper's socket (group only) and the daemon's (anyone may connect; the
  daemon decides by peer credentials): mode 1771, group `jailops`, the sticky bit keeps the daemon from touching the
  helper's files.
- The snapshot directory of a dataset is not looked at by the unprivileged daemon (a jail's `/root` is closed to
  it); the agent opens it.
- `init` creates the jail.conf directory.
- A **full restore test** (`backup verify --depth full`) restores the backup as a jail of its own (`jodrill-<id>`,
  in the backup's project, without network interfaces and without creating the bundle's networks), starts it, runs
  the jail's `update.healthcheck` (else a shell that exits 0) until it passes or `HealthTimeout` ends, then stops and
  deletes it, also when the check fails. The isolated drill network of the design is therefore not needed: the jail
  has a loopback only. It passed in the scripted flow and left nothing behind.

The signed repository was also served over HTTPS by nginx with `nginx/pkg.jailops.net.conf` of the website
repository, and `pkg update`, `rquery` and `fetch` used it (the signature is checked against the public key).
UNVERIFIED: the real host name and certificate of `pkg.jailops.net`, upgrades between versions.

### 18.5 Prototype 7: the Raft library, on FreeBSD

Run on the FreeBSD 15.1 arm64 test host in `processes` mode (three real processes, bbolt with fsync, TCP on
127.0.0.1, SIGKILL of the children), the same eight scenarios for both libraries:

| scenario | hashicorp/raft + raft-boltdb | etcd raft |
|---|---|---|
| election and replication | pass (91 ms) | pass (145 ms) |
| crash and restart | pass | pass |
| leader change | pass (365 ms) | pass (1328 ms) |
| partition | pass | pass |
| snapshot and compaction | pass | pass |
| quorum loss and recovery | pass | pass |
| member add and removal | pass (4.0 s) | pass (8.8 s) |
| throughput (fsync on, 3 nodes) | pass: 143 sequential ops/s, p99 68 ms | **fail**: a follower had not converged after 15 s under 16 writers (112 of 608 commands) |

**Decision: hashicorp/raft with raft-boltdb.** It passed everything, its member changes and leader changes are faster,
and it is pure Go, so the default binary stays `CGO_ENABLED=0`. Cluster mode itself (M7) is not built yet; nothing
else in the product depends on it. UNVERIFIED: the etcd throughput failure may be the harness's flow control rather
than the library; it was not investigated because the other library passed.

### 18.6 Security model of the helper (audit)

The daemon is untrusted by the helper. Consequences that the code now enforces:

- Authority is never read back from something the daemon can write. A restore may write only into a dataset the helper
  itself created incomplete; a backup may read only a snapshot the helper made for it. Both are held in the helper's
  memory with a lifetime, not in ZFS properties.
- Every directory the helper writes in must be unwritable by the daemon, with one named exception. The daemon's state
  directory is a dataset outside the storage root, written by the daemon directly; the configuration directory and the
  storage root belong to root, and the helper refuses to work in one that does not. The exception is the work
  directory (`work_dir`), which the daemon may write (it holds scratch for restore tests, 18.3); the helper treats what
  it finds there as untrusted input.
- Inputs that root-run tools read later (pkg repository files, jail fstabs, devfs rules) are validated when they are
  written or just before use, with allow-lists rather than deny-lists.
- File agents are chrooted children with a lifetime limit and a global cap.

**What the helper does not defend against.** The helper takes the admin assertion of a request from the request itself:
the daemon states which authenticated API user an operation is for, and the helper cannot check that, because the
daemon is the only thing that knows. A compromised daemon can therefore ask for everything an administrator may ask for
(read any backup snapshot of a managed dataset, write into a dataset marked as being restored, build an image, ...), but
only inside the storage roots and the allow-lists above: it cannot name a path outside them, run a program that is not
on the allow-list, or turn a user property into authority. Separating that further would need a second credential the
daemon cannot hold, such as an administrator confirming at a terminal, which is not how the API works. The operator can
shrink the exposure with the `admin-*` lines of `priv.conf` (`admin-raw-param`, `admin-allow-flag`, `admin-devfs-ruleset`,
`admin-host-mount`, `admin-zfs-prop`, `admin-routes`, `admin-raw-pf`, `admin-resume-send`), which are all empty or off by
default. There is no Capsicum confinement of the helper or its file agents yet (18.3). (Found by an independent review of this change.)

Other review findings fixed: a backslash is no longer accepted in a quoted `jail.conf` value; `zfs receive -F` and a
resumable receive need the admin assertion; while `pkg` runs, the repository directory it was checked against cannot be
changed through the helper.

Browsers cannot perform the `jailops-exec/1` HTTP upgrade, so the web console uses a bridge: a session created with
`POST /api/v1/jails/{name}/console`, input by POST, output as server-sent events (API extension `jail_console`). The
session is owned by the creating actor and ends with the process or after an idle period.

**Mounts, links and the start-time recheck (October 2026 audit).** `mount(8)` runs on the host and resolves symbolic links
in an fstab. A jail's root user owns its tree, so it could replace a directory of it with a link and have a mount point
(or, through a shared source, a nullfs source) land on the host. The helper therefore checks, when a file is written and
again when the jail is started (the jail is stopped then), that no component below the root a path belongs to is a
symbolic link: a mount point below the jail's own root, a source below a storage mount or an approved host-mount
directory. A mount point that does not exist yet is accepted if the part that exists is plain. The recheck is the same
`WithAdmin("recheck")` pass that jail.conf gets, so the operator's allow-lists, not the daemon's say-so, remain the
boundary. An approved `admin-host-mount` directory must not be writable by jails that are not trusted equally: two jails
that share it can still change what is below it between two starts. A host mount an administrator admitted stays when
an operator starts, restarts or stops the jail, because the helper accepts an fstab line that is unchanged from the
file on disk (still inside the allow-list); a new or changed host-mount line needs the administrator again. Setting
`host_mounts` or `zfs_delegation` needs a global identity with the admin permission, not just a global one.

### 18.7 IPv6 on a routed /64, and a glibc distribution (FreeBSD 15.1 arm64)

**IPv6.** The test host has a routed /64 (the provider routes the whole prefix to the host). `jailops network create v6
--bridge jo9 --ipv4 10.88.0.1/24 --ipv6 <prefix>:1::1/80 --uplink vtnet0 --nat --dns 1.1.1.1 --dns 2606:4700:4700::1111`
makes a dual-stack managed bridge: a /24 for IPv4 (NAT) and a /80 carved out of the routed /64 for IPv6 (routed, no NAT).
A jail attached with plain `--network v6` now gets an address of every family the network has; it has the v4 and the v6
address, the default route through the bridge, and `resolv.conf` from the network. Verified: the jail reaches the
internet over IPv4 and IPv6 (`curl -6` and `curl -4` return 200), the host reaches the jail, and the same for Alpine and
Debian jails. UNVERIFIED: traffic into the jail from the internet (the test machine has no IPv6 route to it). Switched IPv6 (an
upstream that is not routed to the host) needs the NDP proxy and is covered in 18.10.

**Debian (glibc).** `jailops image create debian --os linux --release trixie --arch arm64 --source-type debootstrap
--keys /usr/local/share/keyrings/debian-archive-keyring.gpg --alias stable` builds Debian 13 with `debootstrap` (80
packages, 140 MB) and the jail runs under the Linuxulator: `ldd` reports glibc 2.41, `uname` Linux 5.15.0, `apt-get
update` and `apt-get install` work, and the jail has v4 and v6 connectivity. What it took:
- The release signature is always checked: a keyring is required (`--keys`) and the mirror must be https. The first run
  without a keyring showed that debootstrap only warns and goes on; the helper's `debootstrap` rule refuses that form.
- Host prerequisites: `pkg install debootstrap` and a Debian keyring file (the port ships only Ubuntu's). The port's
  `debootstrap` insists on a program named `gpgv2` although it uses `gpgv`: `ln -s gpgv /usr/local/bin/gpgv2`.
- debootstrap needs `/usr/local/bin` in its PATH (it is a perl and shell program); the rule gives it a fixed environment.
- debootstrap leaves `proc` mounted inside the build root when it ends badly; the build now unmounts what it mounted.
- Linux jails leave `devfs.rules` beside their configuration; the helper accepts that file only when every line is a
  rule JailOps renders.
- Creating a full-copy jail went through `zfs send | zfs receive` with the stream relayed through the daemon and the
  helper's socket, which ran at about 0.4 MB/s while both processes burned CPU (the unit tests show 100 MB/s, so the
  cause was not found). The copy now happens inside the helper over a kernel pipe (`zfs.replicate`): 140 MB in 2 s. The
  generic stream operations (`zfs.send`, `zfs.receive`) keep their old path and are not used by anything that moves
  large data. UNVERIFIED: why that path is slow on the real host.

Not done for Debian: `jailops` managed updates and package provisioning run `apk` only; an upgrade of a debootstrap
generation is refused (build a new generation).

### 18.8 Sealed secrets for jails

A jail's `env` is stored in the spec and, until now, nothing used it. It now reaches every process the host starts in
the jail (`jailops exec`, `shell`, console sessions); a value the caller names on the command line wins over the jail's.
A value that starts with `sealed:age:` is an age ciphertext (base64) sealed to the host: the daemon creates an age
identity in `<data_dir>/secrets.key` (mode 0600) on first start and publishes the public half as `secrets_recipient` in
`GET /api/v1`, with the API extension `sealed_secrets`. Admission refuses a sealed value this host cannot open (another
host's key, bad base64). The API returns the ciphertext, never the plaintext; the daemon opens it only when it starts a
process, and the plaintext lives in the argument list of that call only. `jailops-sync secrets add-hosts` reads the
recipients from the hosts and lists them in `secrets/recipients.txt`. Services started by the jail's own rc scripts do
not get the environment (there is no agent in the jail); for those, put the value in a file through provisioning.

### 18.9 Where pf sees a jail's traffic (FreeBSD 15.1 arm64, `pfil_member=1`, `pfil_bridge=0`)

Measured with real traffic (probe, `pfctl -F states` first because states are floating and let old flows through,
probe at once, since the reconciler restores a hand-edited anchor within seconds):

1. **Jail to jail on one bridge** is seen on the sending jail's port as `in` and on the receiving jail's port as
   `out`. Per-jail rules on the epair ends filter it, in both directions.
2. **Frames addressed to the bridge's own MAC bypass the pf hook of the bridge's ports.** With the host's address on
   the bridge itself, a jail's traffic to the host, or routed through the host to another network or the internet,
   was never seen by `block in on <jail epair>`; pf saw it only as `in on <bridge>`. So `default_out: deny` and every
   egress rule did nothing towards the host and the internet, and a jail could claim another jail's address towards
   them (the anti-spoofing rule on its port never ran). pf `ether` rules did not evaluate at all on this setup, so
   they are no way out.
3. **The fix is structural:** the bridge carries no address. The network's gateway addresses are on the `gb` end of a
   gateway epair whose `ga` end is a port of the bridge (`jo21`, `jo21ga`, `jo21gb`; bridge names have at most 13
   characters). The host is then just another port: a jail's traffic to it is forwarded between ports, so it meets
   the jail's own rules on its own port, `block in quick ... from ! <jail_addrs>` included. Verified: the same rule
   that did nothing before blocks jail to host once the gateway is a port.
4. **Traffic from the host to a jail** (the host's own, and routed from other networks or the internet) leaves the
   host on the gateway's `gb` end and is not seen on the jail's port as `out`. The renderer therefore also emits, in
   the jail's anchor, the rules for traffic into the jail on the gateway interface, restricted to the jail's own
   addresses (`out on <gateway> ... to <jail>`), with the same order (project policy, jail policy, project isolation,
   defaults).
5. **Projects are isolated by default.** Every jail has an anchor, with or without a policy. It closes the subnets
   of the networks of every other project (`table <foreign>`) in both directions after the jail's own rules, so a
   rule of a policy can open one flow and nothing else is open. A project policy's defaults are a floor: a jail's own
   policy can open ports, it cannot turn a project's default deny into an allow.
6. The tests: `scripts/ci-freebsd-pf.sh` (also run by the e2e script) sends real traffic between a host, four jails
   in two projects and three networks, IPv4 and IPv6, TCP, UDP and ICMP, with expected outcome per probe; with
   `FORGE_BIN` set it also runs forged ARP, ICMPv6 and router-advertisement frames sent from inside a jail with
   `scripts/forge` (18.13), asserting real effects (unchanged host caches, continuous victim traffic, no leak to the
   uplink), not rule presence. (The exact probe count is not quoted here: it changes with the matrix and goes stale
   in prose faster than in the script; run it to see the number.)
   `TestPFGoldensParseWithPfctl` parses every rendered golden with the real `pfctl -n`, which found
   two grammar errors (`icmp6-type` and `tagged` must follow `from .. to ..`). The optional "bridge placement"
   (`network.pf_placement: bridge`) was removed: it was unverified and the facts above make it unsound.

**Isolation limits to know (October 2026 audit).** (1) (Closed since: see 18.13. pf cannot name the advertised target,
so jails hold /32 and /128 addresses, every port is private and the host pins their addresses.) (2) A policy rule `allow out to any` or `allow in from any` also opens the networks of other projects, because the
closed `<foreign>` block comes after the jail's own rules; both sides must open for a flow to pass, so it is a choice
and not an accident, but "any" includes other projects. (3) Neither `scrub` nor `set reassemble` is rendered: how
non-first IPv6 fragments meet port rules depends on the host's own pf.conf. The doctor does not check it yet (how
`pfctl` shows `set reassemble yes` is not verified).

### 18.10 IPv6 proxy mode through the helper (FreeBSD 15.1 amd64, a switched /64)

The proxy answers neighbor solicitations on the uplink for the addresses of jails on a `proxy` network. It needs two
things only root has, and the daemon is not root and is not trusted, so the helper offers exactly two typed operations
(`internal/priv/ndp.go`) and no packet device:

- `ndp.open`: the helper opens `/dev/bpf` **read-only**, binds it to the uplink, installs the neighbor discovery filter
  (ICMPv6 NS and NA, hop limit 255), sets immediate mode and direction "in", and **locks** it (`BIOCLOCK`) before it
  passes the descriptor over the socket. Measured as the daemon's user on the test host: `write` fails with `EBADF`,
  `BIOCSETIF`, `BIOCSETF`, `BIOCPROMISC` and `BIOCSHDRCMPLT` fail with `EPERM`, a request for another interface is
  refused by the helper, and reading works. The daemon can listen to neighbor discovery on the approved uplink and
  nothing else.
- `ndp.answer {uplink, target, requester_ip, requester_mac}`: the helper builds the Neighbor Advertisement itself
  (S and O flags, source address = target, the uplink's own MAC as target link-layer address) and sends it on a
  write descriptor of its own. The daemon never sends a frame. The helper refuses: an uplink the operator did not
  approve; a target outside the approved prefixes or that is an address of this host (its kernel answers for those);
  an unspecified or multicast requester (Duplicate Address Detection is not answered, by design); a zero, multicast or
  own MAC; and more than 200 advertisements a second per uplink. With `announce` set it sends one unsolicited
  advertisement (override flag only) of an approved target to all nodes, under the same limits; the proxy does that
  when an address is added and whenever the capture is reopened, so a router that has not asked yet, or holds an old
  entry, learns at once.

**The authority is the operator's.** `ndp-proxy = <uplink>=<prefix>[,<prefix>...]` in the root-owned `priv.conf`
(one line per uplink, prefixes between /48 and /120) is the only thing that lets a request through. Without it the
operations answer `forbidden` with the line to add, the proxy does not start, and the doctor and the network status
show that message. A compromised daemon can at most make the helper advertise addresses inside an approved prefix on an
approved uplink, at a bounded rate; it cannot sniff other interfaces or inject other frames. (Every request is
audited: since the rc script now passes `-log syslog`, the helper's lines reach syslog, where before they went to
/dev/null under `daemon -f`.)

The multicast group joins (MLD) stay in the daemon: an ordinary `IPV6_JOIN_GROUP` works unprivileged (`ifmcstat` shows
the solicited-node group of each address). The `/128` host routes through the network's gateway interface are installed
by the reconcile loop as part of the network steps, and removed when the jail stops.

Measured on the amd64 test host (FreeBSD 15.1 amd64, upstream a shared switched /64, the jails in `2a0a:4cc0:c1:3bb8:100::/80`):
the upstream routers send multicast NS for a jail address and the proxy answers within a millisecond with the host's MAC
(`tcpdump`: NA with source = target); a jail reaches IPv6 destinations; another host on the internet reaches the jail;
stopping a jail removes its `/128` route and the others stay reachable. The first packets to a new address can be lost
while the upstream router resolves it; the proxy answers within a millisecond once it asks, and announces new
addresses unasked. A jail address that is given to another jail later is forgotten by the host's neighbor cache when the
host route goes (`ndp -d`), or the host could not reach the new jail for a day. The doctor fails (`network.ndproxy`)
when a proxy-mode network is not answering, with the `priv.conf` line to add.

Behaviour of the proxy under load and when it cannot work (October 2026 audit): announcements have a budget of their own
(10 a second) and are sent for new addresses only, three times, a second apart, as RFC 4861 7.2.6 allows, and again for all
addresses when the capture is reopened; what the budget does not cover stays pending, and answers never share it. A
repeated solicitation from one requester for one target within 100 ms is answered once, and one target gets at most 20
answers a second **per requester MAC address** (October 2026: the budget was per target alone, shared by every
requester; it is now keyed by `(target, requester MAC)`), so a flood at one jail cannot use up the answers of the
others, and cannot use up what a *different* requester's own address needs for the very same target either
(`TestPerTargetFloodDoesNotStarveAnotherRequestersMAC`). The proxy reports whether its capture is open: a closed
capture (the uplink down, the helper refusing `ndp.open`) makes the network's `NDPProxy` condition and
`network.ndproxy` fail with the reason, and the retries back off from 2 to 30 seconds. The helper answers and announces
only for an address that has a `/128` host route through one of its own interfaces (read from the routing table), so a
compromised daemon cannot make it speak for the upstream router or another machine of the segment even when the operator
approved the whole /64.

**The per-target flood, analysed (October 2026).** Who can exploit it: another host already on the shared segment
(a neighbour tenant with raw-frame access, not a remote attacker). What it buys: one proxied address stops being
answered for the length of the flood — availability only, and only for that address; the attacker learns and changes
nothing, and every other address keeps working. An attacker already on the segment with raw-frame access can do
worse things to the same victim directly (flood its bandwidth, forge ARP or ND for it on the shared L2 — the subject
of 18.13's defences on the *jail* side, not the uplink), so this was never the easiest way to hurt one tenant; it was
specific to how the proxy itself rate-limits. The fix above (key the per-target budget by the requester's MAC, not the
target alone) closes the form of it that costs an attacker nothing: varying only the claimed IPv6 source address (free,
no state) no longer drains what another MAC needs. **What it does not close:** a flood from many *distinct real MAC
addresses* (not just spoofed IPs) still adds up against the proxy's single system-wide answer budget
(`MaxAnswersPerSecond`, 100/s in the daemon's default), which could in principle still delay an answer to the real
router if its packet lands in a saturated second. Reserving part of that global budget for a "learned" router address
was considered and rejected: nothing on a shared segment tells the proxy which MAC is the router rather than another
tenant (or the attacker claiming to be it), so a reservation keyed by "whoever asked first" or "whoever asks about the
most addresses" is itself a thing an adjacent attacker can race for or imitate, trading a real gap for a fake sense of
one closed. Needing several distinct, real MAC addresses (most switched segments apply some form of port security or
MAC-count limits) to reach the remaining gap is a materially higher bar than today's single spoofed-IP flood, and the
fix is small (one existing field, no new state source) and testable with fakes, which is why it was made; a
router-identity reservation is not, which is why it was not. **UNTESTED live**: this needs a second, genuinely
L2-adjacent host on the uplink segment; the test hosts have no such neighbour, so the fix above is verified with
fakes only (`internal/ndproxy`), not against a live flood.

### 18.11 Host mounts and the switched-IPv6 soak (October 2026)

- **Host mounts.** On the arm64 host, a dataset of a second pool mounted at `/archive` with `admin-host-mount = /archive`
  in `priv.conf`: `jailops jail create ... --host-mount /archive:/mnt/archive:ro` created the mount point, the jail saw the
  files, a write failed with "Read-only file system", the nullfs mount was gone after the jail stopped, and a source the
  operator had not listed (`/etc`) was refused by the helper when the jail was started (the API accepts the spec, because
  only the helper knows the operator's directories).
- **Switched IPv6 soak.** On the amd64 host (shared switched /64, jails in a /80): one jail pinged the internet for 50
  minutes (3,000 packets, none lost), and another machine on the internet pinged the jail for 50 minutes (3,000 sent,
  2,992 received; the eight lost were in the first minute, while the upstream router resolved a new address). A fresh jail
  was reachable from outside within about three seconds of its start, the proxy answering in under a millisecond once
  asked. Deleting a jail removed its `/128` route and its anchor.
- **After the fixes of this revision** (announce on add, neighbor-cache cleanup, flush of a network's anchor on delete,
  pending-announce set, helper refusing targets without a jail host route, symlink-safe fstab checks, label prefix
  hashing): the clean-install end-to-end run with the PF matrix passed in full on both hosts (arm64 and amd64; the exact probe count is not quoted here, see 18.13);
  on amd64 a proxy-mode jail answered pings from the internet and from the host and reached the internet, the doctor's
  `network.ndproxy` and `host.release` checks passed, and a jail address reused by a new jail was reachable at once. The
  announcement was seen on the wire (unsolicited advertisement to `ff02::1`, override flag only). The host-route parser
  (`netstat -rn`) of the helper therefore works on real output.
- **Symlink attacks on host mounts (arm64).** With the jail stopped, its mount point's parent was replaced by a symlink to
  the host's `/usr/local/etc`: the start was refused ("path escapes the allowed root") and nothing was mounted on the
  host. A source below the approved directory that was a symlink to `/` was refused ("resolves through a symbolic link").
  The ordinary mount worked again afterwards. Not run on a real host: the operator-start case (a non-admin starting an
  admin-configured jail) and the 150-jail mass start; both are covered by tests with fakes only.

### 18.13 Neighbour discovery between jails and the host (October 2026, FreeBSD 15.1 arm64 and amd64)

Audit of the generated PF rules for neighbour-cache poisoning, checked with forged packets (a small raw-socket tool in
`temp/nasend`; ICMPv6 neighbour messages cannot be forged with ordinary tools).

- pf cannot read the target of a neighbour message and `ether` rules cannot see the ICMPv6 body, so a rule cannot say
  "this jail may only advertise its own address". What can be said is said: a jail's source addresses are its own
  (the table holds its own link-local address, derived from the MAC JailOps sets; `fe80::/10` only when that is unknown),
  `::` passes only as a DAD solicitation to a solicited-node group, router advertisements and redirects are dropped,
  and only neighbour messages to the jail's own addresses and groups reach it.
- **The model (October 2026): bind every address to its jail, trust nothing a jail says.** A jail holds IPv4 as `/32`
  and IPv6 as `/128` in both IPv6 modes; its default routes lead to the host (an on-link host route to the IPv4
  gateway, rc `static_routes` on FreeBSD and a `route -j` step for Linux guests; `fe80::1` on the gateway interface for
  IPv6). So it has no on-link neighbour and everything, other jails included, goes through the host and PF. Every jail
  port is a private bridge member. At attach the host installs permanent entries on the gateway interface (`arp -s`,
  and `ndp -s` for the global and link-local IPv6 addresses, deleted first), sets `staticarp` on it (no ARP request is
  ever sent, so an address a jail gives itself is never resolved), and adds a static forwarding entry for the jail's
  MAC address on its port (a learned entry would move to a jail that uses someone else's MAC address); detach and the
  stale-entry collection remove the entries. Every neighbour advertisement from a jail is blocked, as are router
  advertisements and redirects (also from the host towards a jail, on the gateway interface). pf `ether` rules did
  not work on the bridge path here (a block rule for ARP on the jail's port had no effect), so none is used.
- **Verified** with forged frames sent from inside the jails (a small raw-socket/bpf tool, `temp/nasend`) on the amd64 host
  (proxy mode) and the arm64 host (routed mode): neighbour advertisements for another jail's address or the gateway, ARP
  replies and requests claiming another jail's address or the gateway, a router advertisement with an autonomous prefix
  towards a jail, a DHCP discover, an extra IPv4 or IPv6 address and another jail's MAC address on a jail's interface.
  None changed any cache, none configured an address, none took another jail's traffic; host to jail, jail to jail
  (through the host), jail to internet and internet to jail (ICMP, and TCP in proxy mode) kept working through 20
  restarts of a jail and the reuse of an address by a new jail.
- Observed on the way: a host with pf forwarding refragments an oversized IPv6 packet instead of answering with
  packet-too-big, so an ordinary oversized echo produces no error; the error was tested with a forged one. pf also
  passes the answer to a solicitation by state, so a rule against advertisements does not stop a reply to a solicitation
  that pf let in.
- The reserved IPv6 addresses (all-ones end, RFC 2526 identifiers in a /64) are not allocated; proxy subnets are
  /48 to /126 and routed /32 to /126; overlapping subnets are refused; a proxy uplink must be a physical or vlan
  interface (doctor).

### 18.14 The boot-time window before PF is up (October 2026)

Read of the boot order, before any live test: `jailops_priv` (`REQUIRE: zfs`, `BEFORE: jailopsd`) starts the helper;
`jailopsd` (`REQUIRE: jailops_priv NETWORKING`) then creates the bridges and gateway epairs of every network, loads
the network anchors, and autostarts jails (each hook attaches its epair, sets the static ARP/ND entries and the
private bridge flag, *then* runs `jail -c`, whose `exec.start = "/bin/sh /etc/rc"` runs the guest's whole boot
synchronously; the jail's own PF anchor is loaded only in `hookPostStart`, **after** that finishes). So there is a
real window, for every jail, between its interface coming up (with real connectivity) and its own anchor existing:
the length of the guest's `/etc/rc` is attacker-relevant time. The project anchor is also loaded in `hookPostStart`,
so the first jail of a project has the same gap for the project-level rules.

**What already closes most of it, by the architecture that was there before this audit.** An anchor call
(`anchor "jailops/*"`) that resolves to nothing yet matches nothing and falls through to whatever comes after it in
the administrator's own `pf.conf`; on a host that follows the install guide (a default-deny base ruleset, the anchor
line placed before any broad `quick pass`, both checked by `jailops doctor`'s `pf.anchors` and `pf.order`), a packet
that arrives before the jail's own anchor exists is dropped by that default, not passed. The static ARP/ND entries
and the private bridge flag are both set in `hookPreStart`, before the jail's interface exists at all, so by the time
`exec.start` runs the jail already cannot be ARP/ND-poisoned by, or poison, another jail. The gap this section is
about is therefore specifically: **can the jail's own traffic itself escape** before its anchor exists, on a
correctly configured host? The honest answer needs pf actually running at that moment, which was not guaranteed:
`jailopsd`'s `REQUIRE` line did not name `pf`, so rcorder had no reason to start the PF service before `jailopsd`.
Without PF running at all there is no default-deny to fall back on — nothing filters anything. **Fixed**: added `pf`
to `packaging/rc.d/jailopsd`'s `REQUIRE` line (`REQUIRE: jailops_priv NETWORKING pf`); a disabled `pf` service still
runs its rc script and exits at once, so this adds no delay when pf is not used, and guarantees the ordering when it
is.

**Measured (October 2026, the amd64 host).** Two genuine power-cycle reboots were run, each with a test jail whose `exec.start`
sends, from the instant the jail starts, a continuous loop of spoofed-source pings (bind fails every time: a jail
cannot bind a raw socket to an address it does not own, `PRIV_NETINET_BINDANY` is not granted inside a jail, so this
particular vector never even leaves the kernel) and forged ARP requests (via `/dev/bpf`, which does leave the jail),
observed from the arm64 host's uplink (filtered on the marker address and on the amd64 host's own address) and from a local capture on
the amd64 host itself. **Both reboots lost the test host's own ZFS pool** (`jopsci`, a loopback-file pool created by
`scripts/ci-freebsd-e2e.sh` under `/tmp` for disposable CI runs): the first time because FreeBSD's default
`clear_tmp_enable` wipes `/tmp` at every boot (the backing file was gone); moving it to `/var/tmp` (not cleared) fixed
that but exposed the next layer: `zpool import -a`, what `/etc/rc.d/zfs` runs at boot, only scans `/dev`, never
arbitrary file paths, so a loopback-file pool is not auto-importable at all regardless of where the file lives. Both
are properties of the **disposable test harness's storage**, not of JailOps or of the `pf` ordering fix: a production
pool on real disks (as the install guide assumes) imports normally through the standard mechanism. With the pool gone,
`jailopsd` itself never got past its own dataset check (`forbidden: dataset "jopsci/jailops" is outside every
registered storage root`) on either attempt, so there was nothing after it to observe — the spoofing jail never ran.
**This is why a literal power-cycle measurement is UNVERIFIED on this host**: the test infrastructure cannot currently
survive a reboot, independently of anything this audit changed.

As the closest honest substitute, the same measurement was run at the daemon level: `jailops_priv` and `jailopsd`
stopped and started together (the pool manually re-imported first), which exercises the identical code path a real
boot does from `jailops_priv`'s start onward (host pass, network pass, the jail's `hookPreStart` static entries and
private flag, `jail -c` running the jail's full `exec.start`, then `hookPostStart` loading its anchor) — everything
*after* pool import, which is the part this section and the `pf` ordering fix are actually about. The daemon came back
and the spoofing jail's `exec.start` loop was running about 4 seconds after `jailopsd` started. Result: **zero packets
with the spoofed source or the forged ARP reached either capture** (the amd64 host's own uplink or the arm64 host's), and a
continuous ping of the production jail from the arm64 host (95 of 95 during this window) showed **no downtime**. UNVERIFIED:
the literal power-cycle case (blocked by the test harness, above) and whether a cold boot's much longer window before
`jailopsd` starts (BIOS, loader, kernel, mounting, `zfs_enable`) changes anything; nothing in that window runs any
JailOps code or has network interfaces up at all, so there is no additional exposure reasoned, but it was not measured.

### 18.12 Where FreeBSD is going (checked October 2026)

What the release notes, the status reports and the release schedule say, and what it means here. None of it is a promise
about FreeBSD 16, whose plans are thin: 16.0 is targeted for December 2027.

- **pkgbase becomes the way.** 15.0 shipped it as a technology preview and 15.1 makes `bsdinstall` install it by default and
  blocks `installworld` on packaged systems. Distribution sets and `freebsd-update` are planned for removal in 16. JailOps
  builds image generations from pkgbase and never uses `freebsd-update`, so this is the direction we already took. Watch the
  package split: 15.1 moved OpenPAM and zstd into their own packages (part of the minimal set), and more such moves are
  likely. UNVERIFIED: that an image built on a later release still contains everything the jail start needs; building one
  on each new release is the test.
- **pf keeps changing.** The NAT code was partly replaced by OpenBSD's (NAT after rule evaluation, `nat-to`/`rdr-to`, `af-to`
  for NAT64); advisory SA-26:09 is "pf silently ignores certain rules"; a forum thread reports NAT rules that loaded without
  an error on 15.0 and did nothing on 15.1. Our answer stays the same: render, `pfctl -n`, load in one operation and **read
  back** what is loaded, and the traffic matrix (`scripts/ci-freebsd-pf.sh`) is the acceptance test on every release. Run it
  on each new minor release before saying it is supported.
- **Neighbor discovery is moving.** RFC 9131 (gratuitous neighbor advertisements), RFC 7217 stable addresses (`stableaddr`,
  15.1) and optimistic DAD are landing in CURRENT. The proxy's unsolicited advertisements follow the same idea as RFC 9131,
  and nothing here depends on SLAAC or on the host's own address generation.
- **`if_bridge` and `if_epair` MAC addresses changed in 15.0** (the old stable derivation could collide). The NDP proxy reads the
  uplink's MAC from the interface rather than assuming it; UNVERIFIED: whether anything else in JailOps assumes a stable
  epair or bridge MAC.
- **ZFS.** 15.1 has OpenZFS 2.4 (libzfs SONAME bump, legacy sysctls warn, `zfs clone -u`). The compatibility check already
  compares pool features with a replication target; `zfs clone -u` could replace our "clone then set canmount" steps.
- **Releases come about every quarter** with four-year support for stable branches; 15.2 is due in December 2026. Plan for a
  matrix of 15.1, 15.2 and later, not for one version.
- **Out of scope but visible:** OCI containers (`podman`) on FreeBSD and jail-aware VM tooling are being built by others.
  JailOps stays a jail manager; its API is the place to integrate, not to grow a second runtime.

## 19. Implementation notes

How the daemon implements sections 5, 6, 8 and 9. Section 16 is the register of what is proven. Everything marked
UNVERIFIED here runs only against fakes until it is tried on FreeBSD.

- **Configuration registry, devices, profiles (8.3).** `sdk/api` holds the registry (`GET /api/v1/config-keys`);
  `POST /api/v1/jails/<n>/config` applies keys atomically and returns `restart_required`; a jail is restarted only with
  `?restart=true`. Devices are named (`nic` MTU and rate limits, `disk`, `devfs` from an allow-list, `proxy`);
  profiles are project-level or host-level (`?project=host`). The effective spec (jail over profiles) is computed
  where it is used: render, admission, quotas, locks. Rate limits are stored and reported as a `RateLimit` condition;
  no shaper enforces them yet. UNVERIFIED: loading the devfs ruleset by the helper, proxy redirects on the uplink.
- **Exec (8.3).** `POST /api/v1/jails/<n>/exec` upgrades to a framed stream (`PROTOCOL.md`, Exec). The daemon takes the
  backend as `service.JailExecer`; the helper client provides it, `--dev` uses `jexec -l` in process. Limits per
  jail, idle and total time are configuration (`limits.exec_*`). The helper side and TTY handling ran on FreeBSD 15.1 (18.2).
- **Host tuning (5, 6).** The reconcile loop loads the modules it needs (if_bridge, if_epair, pf; linux64, linprocfs,
  linsysfs and fdescfs only while a Linux jail exists) before it touches their sysctls, sets the bridge sysctls
  at runtime (`network.sysctl`, defaults `pfil_member=1`, `pfil_bridge=0`, `pfil_onlyip=1`; `pfil_member` must stay
  1, the configuration refuses 0) and `kern.elf64.fallback_brand=3` while a Linux jail exists, and re-asserts
  them on every pass. It never writes `/etc/sysctl.conf`. The brand goes back to -1 only when this daemon set it.
  `doctor` reports modules, the sysctls against the configuration, forwarding (and, with a proxy-mode network,
  `net.inet6.icmp6.nd6_onlink_ns_rfc4861`) that is not persistent in `/etc/sysctl.conf` (a warning), and private
  networks.
- **PF (5).** Rules stand on the jail's own port (the host-side epair end) and on the network's gateway interface
  (see 18.9). Per-jail source enforcement is `table <jail_addrs> persist { addrs, fe80::/10 }`,
  `pass in quick on <epair> inet6 from :: to ff02::/16` (the unspecified address cannot be a table member: pf accepts
  it and stores nothing) and `block in quick on <epair> from ! <jail_addrs> to any`;
  no negated address list is ever emitted, and every anchor defines the tables it uses. Project anchors end with
  `anchor "*"`. The project anchor is loaded before the jail anchors and flushed after them; the reconcile loop reads
  every loaded anchor back and treats a load that did not take effect as drift.
- **Linux jails (6).** Mounts: devfs with a private ruleset, fdescfs, a nullfs mirror of the root on
  `compat/linux`, tmpfs on `compat/linux/dev/shm`, linprocfs, linsysfs (`render.LinuxMounts`, used by the fstab and
  by the image builder). The lifecycle creates `compat/linux/dev/shm` before the first start. Addresses of a Linux
  jail are set from the host after the epair moved, with the `-j` forms. The Alpine image source downloads the
  minirootfs from the mirror, checks its sha256 (published or pinned), writes `resolv.conf` and the repositories
  and runs `apk` through `jexec` in a short-lived build jail with the fallback brand set. The Linux jail path through the
  root helper ran on FreeBSD 15.1 arm64 (18.2); amd64 is open.
- **pkgbase and /etc merges (4.3).** The pkgbase source copies the host's key directories into the root before the
  first `pkg -r`, takes the ABI from `pkg config abi` and uses `fingerprints: "/usr/share/keys/pkgbase-${VERSION_MAJOR}"`.
  A generation switch merges `/etc` with `etcupdate extract` and `etcupdate` on tarballs of the two generations;
  `master.passwd` and `group` are merged by key (`Merge3Keyed`) and `pwd_mkdb -p` follows; `rc.conf` is never merged;
  conflicts in the account files that etcupdate reports are ignored. A root rollback runs the same merge with the
  generations swapped. `/var/db` in the persistent list is UNVERIFIED: pkg database persistence.
- **Backups (9).** With a `backup:` section the daemon builds `internal/hostbackup` and passes it to the service
  layer. The key routes are global-admin only; the recovery key is in the creation reply once and nowhere else.
  `POST /api/v1/backups/run` and `/verify` are operations. A restore names the project it goes into (there is no
  default), is admitted like a new jail, and the adopt step writes `spec.json` and `bundle.json` itself.
- **Updates and provisioning (8.4, 8.5).** Off for every jail until `updates.mode` is set. A check runs `pkg update`
  and `pkg upgrade -n` (`apk update`, `apk upgrade --simulate` on Alpine) inside the jail and compares the image
  generation; apply takes one atomic snapshot, updates, health-checks and records the result. A failed OS update is
  swapped back by the root-swap plan. A failed package update is **not** rolled back by itself: package files live
  in the jail's datasets, a root rollback does not undo them, and the way back is `jail rollback --all` to the
  recorded snapshot, which discards what the jail wrote since. The record says so. The schedule is evaluated by the
  daemon (`updates.tick`); a jail that is busy is tried again at the next tick. Provisioning files come from
  `<state_dir>/project-files/<project>/` (a symlink that leaves it is refused), run in the declared order, once per
  jail, at the first start or with `provision --again`. The history and the provisioned marker are files below the
  state directory. UNVERIFIED: the exact output of the dry runs and every command as run through `jexec`.
- **Authentication hardening.** A TOTP step is accepted once: the read of the last accepted step, the validation and
  the write are one critical section. Verifying a secret for a signed-in user (current password, TOTP confirmation)
  is rate-limited like a login. A hub never forwards `/api/v1/auth/*` or `/api/v1/sessions*`, and a requestor claim must
  name its audience. Client certificates are checked for validity period and key usage after pinning. Request
  bodies must be `application/json`; the session cookie is the `__Host-` one over TLS and the plain one over plain
  HTTP, never the other; responses carry `base-uri 'none'` and `form-action 'self'`, HSTS over TLS. On the backup
  node a login is limited per user and source and per source, a browser login must come from the site itself, bad
  bearer tokens are limited per source, and a changed password ends the user's other sessions. UNVERIFIED /
  format changes left for a later format version: a distinct key for GroupID derivation and a keyed gear table for
  chunking (both change chunk identities, so they need a repository format bump).
