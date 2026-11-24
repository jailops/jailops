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

    fleet.yaml                      id: <fleet id>, the ownership marker; drift_interval (see "Recipes")
    recipes/<name>/recipe.yaml      a recipe and the files beside it (recipes/book/... may be a git submodule)
    .sops.yaml                      who can decrypt the SOPS files (age public keys) and which fields each rule encrypts
    hosts/<host>/host.yaml          kind Host: url, fingerprint, token_secret; labels select groups
    hosts/<host>/remote.sops.yaml   a client certificate for pinned mTLS (url and fingerprint readable, key encrypted)
    hosts/<host>/storage/*.yaml     storage that exists on that host (host-specific)
    hosts/<host>/wireguard/*.yaml   kind WireGuard: a tunnel server of that host and the public parts of its peers
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
| `jailops project create/set` | `projects/<name>.yaml` (Project) | networks granted, storage granted with quotas (`fast=500G` is `{storage: fast, quota: 500G}`), `quotas`, the mandatory policies (`firewall_policies`, a list), the `images` allow-list, and `hosts` (a project spans hosts and has the same UUID on each). Removing a grant revokes it |
| `jailops network grant/revoke` | the project's `networks:` list | a network belongs to **one** project: `validate` refuses a second grant |
| `jailops network create` | `shared/networks/*.yaml` (or `hosts/<h>/networks/`) | every field of the API: IPv4, IPv6 `routed`, `proxy` and `none`, uplink, `private`, DNS, MTU, exclusions. See below for the CLI flags |
| `jailops wireguard create --mode server`, `peer add` | `hosts/<host>/wireguard/*.yaml` (WireGuard) | servers only, public parts only; see "WireGuard" below |
| `jailops storage create` | `hosts/<host>/storage/*.yaml` (Storage) | a dataset on any pool, a big HDD pool included; `compression`. Datasets may not be equal or nested |
| `jailops image create`, `image build` | `shared/images/*.yaml` (Image) | `build: true` builds the **first** generation when the host has none (a fresh host has none and a jail cannot be created from an image without one). Later generations are an operation, not a declaration: `jailops image build` or `jailops image upgrade` |
| `jailops create` | `projects/<p>/jails/*.yaml` (Jail) | the whole spec: network and storage (explicit), `volumes`, `expose`, `devfs`, `resources`, `boot`, `update` schedule, `firewall` (`policies`, a list, and `tags`), `env` with `secret:` references, `labels`. `start: true` starts the jail once, when sync creates it. `recipes:`, `needs:` and `vars:` configure what runs inside it, see "Recipes" below |
| `jailops firewall policy/address-set create` | `projects/<p>/firewall-policies/`, `address-sets/` | the project's mandatory policies are set in a second write once the policies exist |
| `jailops backup target create` | `shared/backup-targets/*.yaml` (BackupTarget) | `kind: node` needs `address` and a pinned `fingerprint` |
| `jailops backup policy create` | `projects/<p>/backup-policies/*.yaml` (BackupPolicy) | selector, targets, `schedule`, retention, `include_state`, `exclude_volumes`, `verify_schedule`, `verify_mode` |
| `jailops profile create` | `profiles/` or `projects/<p>/profiles/` (Profile) | see "Profiles" below |
| secrets in a jail's environment | `secret:<name>` and `secret:<dotted.path>` | age and SOPS, see "Secrets" |

Because the specs are the wire types of the API (decoded strictly: a misspelled key is an error), a field the daemon gains is
declarable the day it exists: `notes` ([notes.md](notes.md)), the long text next to the comment, is a key of the spec of a
jail, network, volume, project, storage, firewall policy, address set, image and profile. `validate` also checks, offline, what the daemon would refuse at apply time: the name rules of
jails, networks, policies, address sets and WireGuard tunnels, overlapping subnets and storage datasets, the IPv6 proxy and routed prefix limits,
the proxy gateway and uplink, reserved fields (`encrypted`, `packages`), that a jail's `volumes` name a volume (`kind: Volume`, `projects/<p>/volumes/`), and that a **profile cannot carry `host_mounts` or
`zfs_delegation`**. It refuses a `Volume` with `encrypted: true`: its passphrase is typed by a person, so create that volume
with `jailops volume create --encrypted` ([storage.md](storage.md#encrypted-volumes)); a jail of the repository can then
attach it, and `validate` warns that a volume a jail names is not declared.

**IPv6, CLI flags and YAML.** `--ipv6` and `network ndp-proxy enable` fill in what a file spells out:

| CLI | YAML (`spec.ipv6`) |
|---|---|
| `--ipv6 2001:db8:1:2:1::1/80` (routed) | `subnet: 2001:db8:1:2:1::/80`, `gateway: 2001:db8:1:2:1::1`, `range: <first>-<last>` |
| `--ipv6 2001:db8:1:2:100::/80`, then `network ndp-proxy enable` | `ndp_proxy: true`, `subnet: 2001:db8:1:2:100::/80`, `gateway: fe80::1`, `range: <first>-<last>` and `uplink: vtnet0` on the network |
| `--ipv4 10.50.0.1/24` | `ipv4: {subnet: 10.50.0.0/24, gateway: 10.50.0.1, range: 10.50.0.2-10.50.0.254}` |

A network that exists switches the NDP proxy with `jailops network ndp-proxy enable|disable NETWORK`, which changes
`ndp_proxy` in the stored network; in a repository the same change is the `ndp_proxy` line of the file.

Complete files are in `examples/ipv6-switched-cloud` and `examples/ipv6-routed-hetzner`.

**WireGuard.** A tunnel belongs to one host (its endpoint and its keys are that host's), so it is declared under
`hosts/<host>/wireguard/` and never under `shared/`. A file carries the spec of a **server**: `mode: server`, `interface`,
`address`, `listen_port`, `endpoint`, `allowed_ips` (the pool), `dns`, `keepalive`, `networks`, `disabled`, and per peer `name`,
`public_key`, `allowed_ips` (its tunnel address) and `keepalive`. There is no field for a private key or a preshared key, and the
strict decoding refuses one:

    apiVersion: jailops/v1
    kind: WireGuard
    metadata:
      name: office
    spec:
      mode: server
      address: [10.99.0.1/24]
      endpoint: vpn.example.com:51820
      networks: [lan]
      peers:
        - {name: laptop, public_key: AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=, allowed_ips: [10.99.0.2/32]}

- The server's own key pair is made on the host by the daemon when sync creates the object; it never leaves the host. Its
  public key is in the object's status (`jailops wireguard show office`), which sync never writes.
- A peer listed in a file is a peer whose key pair was made elsewhere (a phone or laptop that made its own keys): it gets **no
  preshared key**, unless the host already holds one for exactly that public key (the helper keeps preshared keys by public
  key, so a peer enrolled with `jailops wireguard peer add` and then written into the file keeps its preshared key).
- A peer enrolled on the host with `jailops wireguard peer add` (the way to get a join token and a preshared key) is not in the
  repository: the next `sync --apply` would remove it, and because removing a peer cuts it off, sync calls that a dangerous
  change and refuses it without `--force`. Copy the peer's `name`, `public_key` and `allowed_ips` from
  `jailops wireguard show office -o yaml` into the file to keep it.
- What the daemon fills in is not drift: `interface` (`wg-<name>`), `listen_port` (51820), `allowed_ips` (the subnets of
  `address`), `endpoint` (the host name and the port) and each peer's `created` time. A re-plan after an apply is empty.
- **Clients are not declarable.** A client's private key comes from a join token on its own host (`jailops wireguard join
  TOKEN`) and cannot be reproduced from a file; `validate` refuses `mode: client`, and `adopt` skips client tunnels.
- `validate` checks offline what the daemon would refuse: the interface name (at most 15 characters starting with `wg`; a name
  longer than 12 characters needs `interface`), the addresses, the peers' keys (base64 of 32 bytes), addresses that overlap
  another peer or hold the server's own address, the networks named, and two tunnels of one host sharing an interface, a
  listen port or overlapping addresses. Deleting a tunnel, changing its address or its listen port, and removing a peer need
  `--force`.

**Not declarable, and why.**

- `jailops project create --new-network`: a create-time convenience that is never stored; declare the network and grant it
  (`validate` refuses `new_network`).
- Credentials: users, API tokens, trusted certificates, trust tokens, sessions. Their secrets are shown once and cannot be
  reproduced from a file; sync's own access is the token or certificate you give it (`secrets set`, `remote bootstrap`).
- Backup keys and the backup node itself: `jailops backup key create/escrow` produce key material that must not be in git, and the
  node (`jailops-backupd`) is configured by its own `backupd.yaml`. Targets and policies are declarable; see `examples/web-stack`.
- `priv.conf` (`admin-host-mount`, ...) and `jailopsd.yaml`: they are the host operator's boundary, on purpose out of
  reach of the API. `examples/provisioning/bootstrap.sh` writes them when it installs a host.
- Runtime actions and data: snapshots, backups and restores, exec, updates, generations after the first. Operations such as a backup, a restore or a major upgrade are never listed in a fleet: run them by hand.
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
| `validate` (alias `check`) | checks layout, names, references and values, and renders and checks every recipe (the variables, the files beside it, the daemon's own check); offline, no daemon |
| `render` | prints the resolved documents sync would send; offline, never decrypts |
| `plan` | reads the hosts and shows what would change, and for every recipe of a running jail what a dry run found; writes nothing to a host; needs no decryption key to compare (a secret's change is detected from a digest of its stored ciphertext); `--json` |
| `sync` | plans; with `--apply` makes the hosts match |
| `drift` | exits 1 when live hosts differ from the repository, for CI and cron; `--json`. See "Recipes" for what exit 0 means |
| `watch` | pulls the repository on an interval, plans, optionally applies |
| `rollout` | runs the staged upgrades declared as `Rollout` objects |
| `adopt` | writes live objects into the repository as YAML (`--claim` also takes ownership) |
| `prune-check` | lists live objects the repository does not declare; deletes nothing |
| `doctor` | checks the tool, the repository, the identity and every host |
| `secrets`, `remote`, `vault` | manage age secrets, the client-certificate identity of hosts, and the SOPS recipients |

Only the keys a document **declares** are compared: a key you did not write is not drift. A changed declared key is reported as
drift and reverted by `sync --apply`.

## Recipes

A recipe is configuration **inside** a jail: packages, files, rc variables, services, commands. The recipe itself is the one of
`jailops jail apply` ([provisioning.md](provisioning.md)); sync only decides when to apply it. Sync reads the recipe's directory and
the files beside it, fills in the variables, and calls the same API as the command line and the console: there is no engine in sync
and nothing to run in the daemon.

    # projects/shop/jails/web.yaml
    apiVersion: jailops/v1
    kind: Jail
    hosts: [h1]
    metadata: {name: web}
    start: true
    needs: [db]                          # jails of the same project and host whose recipes settle first
    vars: {site: shop.example.test}      # for every recipe of this jail that declares the name
    recipes:
      - path: recipes/nginx-site         # a directory of the repository, below recipes/
        vars: {upstream: 10.70.0.10}     # per recipe; wins over the jail's vars
      - path: recipes/book/nginx-tls-site    # a git submodule works the same: only files are read
        secrets: {tls_key: tls.key}      # a secret variable: a reference into the secret store, never the value
    spec: {...}

`path` is relative to the repository root and stays below `recipes/`; the last element is the recipe's name in the jail. Files beside the recipe
are read by sync and sent in the request. A link that leads out of the recipe's directory is refused. Only recipes listed under
`recipes:` are kept: backups, restores and upgrades are never listed and are run by hand. A name that no recipe of the jail declares in
`vars:`, a secret variable written under `vars:`, a missing file and everything `jailops jail apply --validate` refuses are `validate`
errors, offline. A Recipe Book kept as a submodule is pinned by the commit of the gitlink; `watch` runs `git submodule update --init`
after its pull.

**What decides.** On each jail sync records, per recipe, the **hash** of what it applied: the rendered recipe, the files beside it
and a fingerprint of each secret, never a value. It is the label `sync.jailops.net/recipe.<name>` of the jail. A recipe is
**converged**, **drift** or **unknown**:

- converged: every step a dry run *can read* is as the recipe says (package, file, dir, sysrc, service, a run with `creates`). A run
  without guard and a run with `unless` are actions, not state: a dry run never executes them, so they are not part of the judgment.
- drift: a readable step would change (a file edited by hand, a package removed, a directory with another mode).
- unknown: the jail could not be read now (a stopped jail, a failed look, a secret that needs a key `plan` does not have). Never read as converged.

| Hash on the jail | Dry run | Sync |
|---|---|---|
| differs from the recipe | any | applies |
| missing (never applied, or the last apply did not finish) | any | applies |
| same | drift | applies |
| same | converged | nothing |
| same | unknown | nothing, shown as `unknown` |

An unguarded `run` (a reload at the end of a recipe) never starts an apply by itself; it runs when an apply happens. Sync **removes the
label before it applies and writes it only after the last step succeeded**: a run that fails halfway, a reload that failed after the file was
written, leaves no record, so the next run applies again, also when the file is by then as it should be. Nothing a recipe did is
undone, and what a recipe no longer mentions stays in the jail. A label of a recipe you removed from the file is cleaned up (`forgotten`);
the jail is not touched.

**When sync looks inside a jail.** Comparing hashes costs nothing and happens on every run. A dry run reads the jail and happens when the hash
changed, when you ask (`plan` and `drift` always do), and otherwise at most once per `drift_interval` of `fleet.yaml` (default `1h`) for each recipe
of a running jail, so a change made by hand is found and put back within that time. Every dry run is an operation of the daemon (`jail.apply` with `dry_run`), pruned by age like all
operations, which is why a run that is not asked for does not look more than once per interval. The time of the last converged look is a memory in the state directory of sync, not on the jail: if it is lost, the next run looks.

**Order and `needs`.** Sync first makes every object of a host (images, networks, policies, volumes, jails), then the recipes, jails in
the order of `needs` and recipes in list order. A policy rule that names `jail:db` is fine before the jail exists: the API only warns, and it matches
nobody until then. A jail that is not running **waits** (`waiting: not running`): sync never starts or restarts a jail that exists, `start: true` is
only the first start at creation, in the same run. A recipe of a jail that `needs` another goes on when that jail runs and each of its recipes has the
current hash and was found converged, or its look is not due yet (the earlier result is trusted). It **waits** (`waiting: needs db`) when the other jail does not run, when a recipe
of it has no record or a changed hash and is not applied, when drift is found and not repaired, or when its last look **failed**; `plan` says which: a
look that is skipped until `drift_interval` passes is a trusted result, a look that failed now is not. A dependency applied earlier in the same run counts as met.

**Failures stay small.** A recipe that fails stops the later recipes of its jail and the recipes of the jails that need it (`skipped: needs db`); every other
jail goes on, the jails themselves are never stopped, and the run exits 1.

**Exit codes.** `sync --apply` exits 1 when anything failed. `drift` exits 0 when no drift was *confirmed* and 1 when a recipe would be applied (a changed
hash, a missing record or drift) or an object differs. **Exit 0 does not say everything was verified:** a recipe that is `unknown` or `waiting` is printed, with its reason,
in the text and in the JSON (`--json`: `recipes[].action` is `apply`, `match`, `wait`, `forget`; `status` is `converged`, `drift` or `unknown`), and exits 0. Read the
output when that matters.

**Secrets.** A secret variable (`secret: true` in the recipe) is given under `secrets:` as a reference into the store described below
(`secrets/<name>.age`, or a dotted path of the SOPS file), never as a value in a fleet file. Sync decrypts it in memory when it applies or looks, puts it only into the
file the recipe names, and marks the request sensitive: the daemon then keeps no file content and shows no command output of that request. The hash holds a fingerprint of the
**stored ciphertext**, so a label cannot reveal a weak secret. Two consequences: re-encrypting a secret with `secrets set` (the same text, a new ciphertext) or after
`vault` re-wraps a SOPS file changes the fingerprint and runs the recipes that use it once more; and `plan` without a key compares by hash and says `unknown` for the look inside the jail.

**Shared values.** `vars:` at the top of a jail file is the shared `--vars` file of its recipes: every recipe that declares the name takes it, the entry's own `vars:` wins,
and a name that no recipe of the jail declares is an error.

**Hand-made jails.** A jail that existed before sync (taken over with `adopt --claim`) has no record for a recipe. The first sync applies the whole recipe, so a file the recipe names is
**overwritten as a whole** with what the repository says. Read `plan` first. If you change a jail with `jailops jail apply` that sync keeps, the command warns
(`the jail is managed by sync, the next sync may revert this`).

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

Rollouts stage the in-place upgrade of the jails (`jailops jail upgrade` on each, with a project, a label selector, a
canary, a batch size, a pause and a health gate) so one bad set of packages does not reach every jail at once. `jail
upgrade` is a global administrator's: the token or certificate of the host needs that role for a rollout, and a
project-scoped one gets `forbidden` on every jail.

## Naming rules

File name equals `metadata.name`. Every kind uses the daemon's one rule (`^[a-z]([a-z0-9-]*[a-z0-9])?$`, hyphens only, at most 63 characters; an address
set 29, because its name becomes a PF table with the hyphens written as underscores); `validate` says so before the daemon would.

## Plain http

A host URL is `https://`, with a pinned `fingerprint` for a self-signed certificate (or none for a certificate a public CA
signed). `http://` is accepted for loopback only, as the SDK does. See [exposure.md](exposure.md) for how a host gets its listener
and certificate.

