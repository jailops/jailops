# Fleets from git: `jailops-sync`

`jailops-sync` makes hosts match a **git repository of YAML**. It is a client of the API like the CLI (it imports only `sdk` and
`sdk/api`), so it needs no access to a host beyond a token or a client certificate. A complete small repository is
`examples/minimal-fleet`; `jailops-sync validate -C examples/minimal-fleet` checks it offline. Task-shaped, runnable examples
(IPv6 switched and routed, a web stack with backups, a 16 TB pool, two hosts with secrets, provisioning a fresh VM) are in
[../examples/README.md](../examples/README.md); the test suite applies every one of them to in-memory hosts, so none of them rots.

The rule that makes it safe: **sync changes only what it owns.** Every object it creates carries `managed_by: sync:<fleet id>`
(the `id` in `fleet.yaml`). Objects it did not create are never changed or deleted, `prune-check` lists what the repository does not
declare and never deletes, and there is no scheduler: a jail is declared on a named host.

## The repository

    fleet.yaml                      id: <fleet id>, the ownership marker
    .sops.yaml                      who can decrypt the SOPS files (age public keys) and which fields each rule encrypts
    hosts/<host>/host.yaml          kind Host: url, fingerprint, token_secret; labels select groups
    hosts/<host>/remote.sops.yaml   a client certificate for pinned mTLS (url and fingerprint readable, key encrypted)
    hosts/<host>/storage/*.yaml     storage that exists on that host (host-specific)
    groups/*.yaml                   kind HostGroup: a selector over host labels
    shared/images|networks|backup-targets/*.yaml     host-level objects applied to the hosts that need them
    profiles/*.yaml                 kind Profile: a fragment merged into the jails that list it (repository only)
    shared/secrets.sops.yaml        values for jail environment `secret:<dotted.path>`
    secrets/<name>.age, recipients.txt   one age secret per file; the recipients are the hosts' keys
    projects/<project>.yaml         kind Project: hosts, networks (one project each), storage with quotas, quotas, firewall, images
    projects/<project>/jails/*.yaml, profiles/*.yaml, firewall-policies/*.yaml   everything a project owns
    rollouts/*.yaml                 kind Rollout: staged upgrades

**There is no default project.** Every jail and every project-level object lives under `projects/<project>/`; a jail names its
network and storage (or a profile does). `validate` reports a jail without a project, a network named by two projects, and a
jail that has no network or storage after its profiles.

## What you can declare

Every declarative object a user can create with the CLI has a file kind. Nothing here needs a second tool.

| By CLI | In the repository | Notes |
|---|---|---|
| `jailops project create/set` | `projects/<name>.yaml` (Project) | networks granted, storage granted with quotas (`fast=500G` is `{storage: fast, quota: 500G}`), `quotas`, the mandatory `firewall` policy, the `images` allow-list, and `hosts` (a project spans hosts and has the same UUID on each). Removing a grant revokes it |
| `jailops network grant/revoke` | the project's `networks:` list | a network belongs to **one** project: `validate` refuses a second grant |
| `jailops network create` | `shared/networks/*.yaml` (or `hosts/<h>/networks/`) | every field of the API: IPv4, IPv6 `routed`, `proxy` and `none`, uplink, `private`, DNS, MTU, exclusions. See below for the CLI flags |
| `jailops storage create` | `hosts/<host>/storage/*.yaml` (Storage) | a dataset on any pool, a big HDD pool included; `compression`. Datasets may not be equal or nested |
| `jailops image create`, `image build` | `shared/images/*.yaml` (Image) | `aliases` are in the spec. `build: true` builds the **first** generation when the host has none (a fresh host has none and a jail cannot be created from an image without one). Later generations are an operation, not a declaration: `jailops image build`, or a rollout |
| `jailops create` | `projects/<p>/jails/*.yaml` (Jail) | the whole spec: network and storage (explicit), `volumes`, `host_mounts`, `proxy`, `devfs`, `resources`, `boot`, `update` schedule, `firewall`, `env` with `secret:` references, `provision`, `labels`. `start: true` and `after:` hooks (start, restart, snapshot, exec) run once, after creation |
| `jailops firewall policy/address-set create` | `projects/<p>/firewall-policies/`, `address-sets/` | the project's mandatory policy is set in a second write once the policy exists |
| `jailops backup target create` | `shared/backup-targets/*.yaml` (BackupTarget) | `kind: node` needs `address` and a pinned `fingerprint` |
| `jailops backup policy create` | `projects/<p>/backup-policies/*.yaml` (BackupPolicy) | selector, targets, `schedule`, retention, `verify_schedule`, `verify_mode` |
| `jailops profile create` | `profiles/` or `projects/<p>/profiles/` (Profile) | see "Profiles" below |
| secrets in a jail's environment | `secret:<name>` and `secret:<dotted.path>` | age and SOPS, see "Secrets" |

Because the specs are the wire types of the API (decoded strictly: a misspelled key is an error), a field the daemon gains is
declarable the day it exists. `validate` also checks, offline, what the daemon would refuse at apply time: the name rules of
jails, networks, policies and address sets, overlapping subnets and storage datasets, the IPv6 proxy and routed prefix limits,
the proxy gateway and uplink, reserved fields (`encrypted`, `packages`), and that a **profile cannot carry `host_mounts` or
`zfs_delegation`**.

**IPv6, CLI flags and YAML.** `--ipv6-mode routed` and `--ipv6-mode proxy` fill in what a file spells out:

| CLI | YAML (`spec.ipv6`) |
|---|---|
| `--ipv6 2001:db8:1:2:1::1/80` (routed) | `mode: routed`, `subnet: 2001:db8:1:2:1::/80`, `gateway: 2001:db8:1:2:1::1`, `range: <first>-<last>` |
| `--ipv6 2001:db8:1:2:100::/80 --ipv6-mode proxy` | `mode: proxy`, `subnet: 2001:db8:1:2:100::/80`, `gateway: fe80::1`, `range: <first>-<last>` and `uplink: vtnet0` on the network |
| `--ipv4 10.50.0.1/24` | `ipv4: {subnet: 10.50.0.0/24, gateway: 10.50.0.1, range: 10.50.0.2-10.50.0.254}` |

Proxy mode also needs the host operator's `ndp-proxy` line in `priv.conf`; that is root's decision and not something an API
call can make. Complete files are in `examples/ipv6-switched-cloud` and `examples/ipv6-routed-hetzner`.

**Not declarable, and why.**

- `jailops project create --new-network`: a create-time convenience that is never stored; declare the network and grant it
  (`validate` refuses `new_network`).
- Credentials: users, API tokens, trusted certificates, trust tokens, sessions. Their secrets are shown once and cannot be
  reproduced from a file; sync's own access is the token or certificate you give it (`secrets set`, `remote bootstrap`).
- Backup keys and the backup node itself: `jailops backup key create/escrow` produce key material that must not be in git, and the
  node (`jailops-backupd`) is configured by its own `backupd.yaml`. Targets and policies are declarable; see `examples/web-stack`.
- `priv.conf` (`ndp-proxy`, `admin-host-mount`, ...) and `jailopsd.yaml`: they are the host operator's boundary, on purpose out of
  reach of the API. `examples/provisioning/bootstrap.sh` writes them when it installs a host.
- Runtime actions and data: snapshots (use the `snapshot` hook), backups and restores, exec, updates, generations after the first.
- Provisioning **files**: `provision.files` copies from `<state_dir>/project-files/<project>/` on the host. Sync does not upload
  them (see `examples/pkgops-packaged-service`); commands, packages and services are declarable.
- Profile objects of the daemon (below).

### Profiles

A `Profile` document in the repository is a fragment that sync **merges into the jail before sending it**: devices merge by name,
the jail's own keys win, and `render` shows where each key came from. The daemon's own profile objects (`jailops profile create`)
are a different mechanism with the same purpose; sync does not create them, because a profile that lives only on a host would
make the host, not git, the source of truth for every jail that lists it. The effect is the same: a changed repository profile
shows in `plan` as a change of each jail that lists it. `adopt` tells you when a jail lists host profiles the repository does not know.

## Commands

| Command | Does |
|---|---|
| `validate` (alias `check`) | checks layout, names, references and values; offline |
| `render` | prints the resolved documents sync would send; offline, never decrypts |
| `plan` | reads the hosts and shows what would change; writes nothing; needs no decryption key (a secret's change is detected from a digest of its stored ciphertext) |
| `sync` | plans; with `--apply` makes the hosts match |
| `drift` | exits non-zero when live hosts differ from the repository, for CI and cron |
| `watch` | pulls the repository on an interval, plans, optionally applies |
| `rollout` | runs the staged upgrades declared as `Rollout` objects |
| `adopt` | writes live objects into the repository as YAML (`--claim` also takes ownership) |
| `prune-check` | lists live objects the repository does not declare; deletes nothing |
| `doctor` | checks the tool, the repository, the identity and every host |
| `secrets`, `remote`, `vault` | manage age secrets, the client-certificate identity of hosts, and the SOPS recipients |

Only the keys a document **declares** are compared: a key you did not write is not drift. A changed declared key is reported as
drift and reverted by `sync --apply`.

## Secrets

Two mechanisms, which can be mixed:

- `secrets/<name>.age`: one age-encrypted secret per file, for the API tokens of hosts and the hub (`token_secret`) and for jail
  environment values `secret:<name>`.
- SOPS: `shared/secrets.sops.yaml` and `hosts/<host>/remote.sops.yaml`, governed by `.sops.yaml`. A jail value
  `secret:app.db_password` is a value of the SOPS file.

Either way, sync decrypts a value **in memory only when it applies it**, and sends it **sealed** (`sealed:age:...`) to the age key
of the host (`secrets_recipient` in the host's server info, extension `sealed_secrets`), so the host that holds the key opens it,
and only when it starts a process in the jail. The API never returns the plaintext.

The key for SOPS files comes only from SOPS's own variables: `SOPS_AGE_KEY` (the key text), `SOPS_AGE_KEY_FILE` (an identity
file, which may hold an age plugin identity so that a hardware token or an agent supplies the key) or `SOPS_AGE_KEY_CMD` (a command
that prints it). The implicit `~/.config/sops/age/keys.txt` is not counted. `jailops-sync vault status|list-recipients|add-recipient|
remove-recipient` edits the recipients in `.sops.yaml` and re-wraps the files; removing a recipient is revocation only after the
re-wrap, and a removed key may already have read the current values, so rotate them.

## A first fleet

    jailops-sync -C ./fleet validate
    jailops-sync -C ./fleet plan
    jailops-sync -C ./fleet sync --apply
    jailops-sync -C ./fleet drift            # in CI: non-zero when something changed behind your back
    jailops-sync -C ./fleet adopt --claim    # take over what is already running, as YAML

Rollouts stage an upgrade (a project, a label selector, a canary, a batch size, a pause, a health gate) so one bad generation
does not reach every jail at once.

## Naming rules

File name equals `metadata.name`. Jails, networks and firewall policies use the daemon's rule (`^[a-z][a-z0-9-]{0,62}$`), address
sets `^[a-z][a-z0-9_]{0,23}$` (they become PF tables, so `web_tier`, not `web-tier`); `validate` says so before the daemon would.

## Plain http

A host URL is `https://`, with a pinned `fingerprint` for a self-signed certificate (or none for a certificate a public CA
signed). `http://` is accepted for loopback only, as the SDK does. See [exposure.md](exposure.md) for how a host gets its listener
and certificate.

## Verified, and not

Run against a real FreeBSD host: adopt with claim, plan, apply (a dual-stack network, a project grant and a jail), a changed
declared key reported as drift and reverted, and a secret in a SOPS file sealed to the host's own age key, stored sealed, and
opened inside the jail. **UNVERIFIED:** age plugin identities, `remote bootstrap` against a real host, and rollouts on real jails
(the package and generation updates they drive were tested with fakes).

Tested with fakes only (in-memory hosts, `internal/fleet/e2e`): `build: true` for images, every object kind of the table above applying
and then planning to zero, and the eight example repositories. **UNVERIFIED on a real host:** `build: true` (the first image build
through sync), an IPv6 proxy or routed network created by sync (the same objects created by the CLI are verified), a host mount applied by
sync with a global-admin token, and `jailops-sync` over the network with a CA-signed server certificate (a pinned self-signed one is
verified).
