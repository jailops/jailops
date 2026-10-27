# Changelog

## Unreleased

- `jailops-backupd version`, its `/1.0` answer and its log now report the release version. The release stamp (`-X main.version`) never reached it, so the 0.1.0 binaries say `0.1.0-dev`. CI now checks that every command declares `var version`.
- README and docs/install.md say what is true today: the JailOps packages are in the repository, and the status block is replaced by a link to the roadmap.

## 0.1.0 - 2026-10-26

This is the first release, so the list below is ordered by area, not by date: what changes behaviour first, then
networking, security, fleets, packaging, and the groundwork.

### Advertised features that did not work
Everything below used to be reachable from the CLI, the SDK or the docs and answered `unsupported` at run time.
- `jailops jail console` and `Jails().Console()` are real: they use the console session protocol the web console uses
  (HTTP requests and server-sent events), with a raw local terminal and window resize forwarding. The fake server
  implements the same routes. Verified against the fake and the in-process daemon; UNVERIFIED on a real host.
- Removed: `jail create --package` and `jail create --encrypted`, `storage create --encrypted`, and
  `Jails().Freeze()` with the `freeze` action (FreeBSD has no jail freeze). A spec that still sets `packages` or
  `encrypted` is refused with `unsupported` and a hint (provisioning `packages` step; the manual ZFS encryption-root
  recipe in docs/storage.md). Native ZFS encryption and packages at creation remain unbuilt.
- Still `unsupported` (HTTP 501, listed in `sdk/PROTOCOL.md` and design section 16): `PUT /server`, cluster routes,
  listing console sessions, jail logs history, file access, cold moves, snapshot rename, volume management of an
  existing jail, bundle download, jail templates, groups, rollouts, and the backup transport routes.
- Documented as not enforced or not driven: the rate-limit shaper (stored, not enforced), `zfs_delegation` (accepted,
  never handed to the jail), replication between hosts (a library, no API or CLI), the `inplace` strategy for thin
  jails (refused). The "OpenAPI is planned" note now says there is no OpenAPI document today.
- Tests: the service layer is tested directly on the in-memory fakes (scope checks, error mapping, create rollback at
  every injected failure), `fleet/apply` and `fleet/rollout` have unit tests, and the helper session tests wait for
  observable output instead of sleeping.

### Image aliases and deletion
- Deleting an image removes it with all its generations and aliases; it is refused only while a jail references a generation, and the error names the jails.
- Deleting a generation that only aliases reference moves each alias to the newest remaining generation, or removes it when none remains (a generation a jail uses is still refused).
- Aliases are managed explicitly: `jailops image alias set IMAGE ALIAS [GENERATION]` and `jailops image alias delete IMAGE ALIAS` (replacing `image alias ... [--remove]`), `PUT|DELETE /1.0/images/{name}/aliases/{alias}` (admin, extension `image_aliases`), `Images().SetAlias/DeleteAlias` in the SDK and the fake, and buttons in the web console.

### One identity system, root as the break-glass
- The unix socket no longer maps Unix users or groups to roles. Peer uid 0 (root) is a global admin, the host's recovery access;
  every other local user is anonymous, exactly like an unauthenticated remote client (`GET /1.0`, `GET /healthz`, login, trust
  redemption, the static console) and authenticates with a token or a login over the socket. Removed: `listen.unix.admin_group`,
  `user_group`, `user_projects` (a configuration that sets one stops the daemon with a message), the `jailops-admin` group
  created by `jailops-priv init` (and its `-admin-group` option), and `JAILOPS_ADMINS` in `examples/provisioning`. The socket stays mode 0666: connecting confers nothing.
- Identities are managed from the host with `sudo jailops user ...`: `user create NAME --admin` (first administrator),
  `user set-password` (prompt, `--password-stdin` or `--generate`; no password ever on the command line), `user set-role`,
  `token revoke`. New API route `POST /1.0/users/{name}/password` (global admin), `Users().SetPassword` in the SDK and the fake, and user
  creation, role changes, password reset and deletion in the web console.
- Tests: root is a global admin; a non-root peer is anonymous on every route (the authorization matrix has an "anonymous
  local" column equal to "anonymous remote"); a token over the socket works for any uid. `temp/verify-identity.sh` is the check for a real FreeBSD host (not yet run).
- The backup node's admin socket follows the same rule: only root (uid 0) is an administrator there, the socket is mode 0600, and the `admin_group` key is removed (a configuration that sets it stops the node with a message). The management API/UI (127.0.0.1:8445) and the host listener are unchanged.

### Fleets, examples and exposure
- `jailops-sync`: `build: true` on an Image builds its first generation when the host has none, so a fresh host reaches a running jail
  with one `sync --apply`; `check` is an alias of `validate`; `validate` refuses what the daemon would refuse at apply time
  (name rules, overlapping subnets and datasets, IPv6 proxy and routed limits, reserved fields, `host_mounts` and
  `zfs_delegation` in a profile, `http://` to a non-loopback host); `adopt` notes host profiles the repository does not know.
- Examples (`examples/`), each with a README and an `EXPECTED-PLAN.md`, tested against in-memory hosts and the daemon's validators:
  `ipv6-switched-cloud`, `ipv6-routed-hetzner`, `web-stack`, `big-pool-tank`, `multi-host-fleet`, `provisioning`,
  `pkgops-packaged-service`. New pages: `docs/exposure.md`, `docs/provisioning.md`, `docs/api.md`.
- `jailopsd`: `web.enabled: false` switches the embedded console off and `-tags noweb` builds without it; the TLS listener serves a
  reloaded certificate (a renewed `listen.https.cert`/`key` needs no restart); a configured certificate is never generated or
  overwritten (a missing file or half a pair stops the start); `listen.https.address`, and `cert` without `key`, are validated at load.

### No default project, everything explicit
- A fresh host has no project. Every request about something a project owns names the project (`?project=`, or the
  current project set with `jailops project switch`); without one the server answers `400 a project is required`.
- A jail is created with a network and a storage named explicitly, in the jail or in a profile; nothing is filled in
  from the project. The project fields `default` on network and storage grants and `--default-network`/`--default-storage`
  are gone.
- A network belongs to one project: granting a network another project holds is refused, and grants are whole networks
  (no shared bridges, no per-project sub-ranges). Storage can still be shared; each project gets its own dataset tree
  and quota.
- Only `<mount>/projects/<project>/jails/<name>` exists on disk; the legacy layout of the default project is removed.
- `jailops-sync`: jails and project-level objects live under `projects/<project>/` only, rollouts name their project,
  two projects naming one network is a validation error, and `adopt` writes by project.

### Supported FreeBSD releases
- JailOps targets FreeBSD 15.1 and later; CI, the release workflow and the package build use 15.1 and the 15 ABI only. `jailops doctor` fails (`host.release`) on an older host. Examples and help
  texts use 15.1.

### Configuration
- `state_dir` defaults to `<data_dir>/state` (`/var/db/jailops/state`, where `jailops-priv init` mounts the state
  dataset) instead of a directory inside the root-owned storage mount, which the daemon cannot write. A test keeps
  `packaging/jailopsd.yaml` listing every key the daemon accepts.

### Audit fixes (proxy, host mounts, PF)
- The helper rechecks a jail's fstab when the jail starts and refuses a mount point or source that passes through a
  symbolic link; an unchanged host mount survives an operator's start or restart, and setting `host_mounts` or
  `zfs_delegation` needs a global admin, not a global operator.
- The NDP proxy announces new addresses only (own budget, up to three times), answers a repeated solicitation once,
  bounds answers per target, reports a closed capture in the network condition and the doctor, and backs off failed
  opens. The helper only speaks for addresses that have a `/128` host route through one of its interfaces, and the
  multicast sockets of the proxy stay on loopback and are closed with the proxy.
- A pass rule's label includes everything above it in the anchor, so a deny inserted above an unchanged rule ends the
  flows that rule admitted; an anchor whose loaded rules cannot be read is not replaced.

### Existing data in a jail
- A jail can mount existing host directories, for example datasets of a large pool JailOps does not manage:
  `host_mounts` in the spec, `--host-mount SOURCE:TARGET[:ro]` on create. Only a global administrator may set them, the
  source must lie below an `admin-host-mount` directory in the helper's `priv.conf`, profiles cannot carry them, and the
  mount point is created in the jail's root on every render. Everything else (jails on a big pool as storage, volumes
  with quotas) already worked and is explained on the website.

### Networking and IPv6
- IPv6 proxy mode has no Experimental switch any more (`experimental.ndproxy` is gone): a network in proxy mode always
  has its proxy, which answers for every address of a running jail (any prefix length inside the uplink's /64, for
  example a /80) and announces each address when it appears and whenever the capture is reopened. The operator's
  `ndp-proxy` line in `priv.conf` stays the authority; `jailops doctor` fails (`network.ndproxy`) with that line when a
  proxy network is not answering.
- A jail address on a switched IPv6 /64 can be given to a new jail with another hardware address; the kernel's cached
  neighbor entry of the old one no longer lingers (it is dropped when the host route is removed and before the next is
  made), so the host reaches the new jail at once.
- Deleting a network flushes its anchor whether or not it had NAT (the anchor also holds the pass of the gateway port).
- The helper offers `ndp.open` (a read-only, locked, filtered capture of an approved uplink) and `ndp.answer`
  (one advertisement, built by the helper) instead of the unconfigured `sys.open_bpf`. What may be answered is set by
  the operator in `priv.conf` with `ndp-proxy = <uplink>=<prefix>,...`; nothing works without it.
- The helper's audit lines now reach syslog under rc (`-log syslog`).
- Doctor accepts `gateway_enable` and `ipv6_gateway_enable` in rc.conf as persistent forwarding.
- A jail attached to a network without naming addresses gets one of every family the network has (dual-stack networks
  work with plain `--network NAME`); verified on a managed bridge with a /24 and a /80 of the host's routed /64.
- Debian images (`--source-type debootstrap`, glibc) build and run; the release signature is always checked.
- Copying an image into a jail happens inside the helper (`zfs.replicate`): 140 MB in 2 s instead of minutes.

### Packet filter
- The first Linux image build works from a clean host: the Linux modules and the fallback brand are asserted (through the
  reconciler's host pass, so with the helper's existing `kldload` and `sysctl` rules) before the build, and the host pass
  keeps them while a Linux image is defined. `doctor` warns (`network.dns`) about a network that jails use and that has
  no nameserver.
- The helper accepts `jexec <build jail> /sbin/apk` again: the image build jail (`jobuild-<image>-g<N>`) was refused by
  the jail-key check of the `jexec` rule (Alpine and Debian images could not be built). Linux jails (Alpine, Debian) were
  run with the new addressing on FreeBSD arm64 and amd64 (docs/linux-jails.md).
- **A jail has exactly the addresses JailOps gave it.** A jail holds its IPv4 address as `/32` and its IPv6 address as
  `/128` (also in routed mode), with default routes through the host (an on-link host route to the IPv4 gateway,
  `fe80::1` for IPv6), so jails reach each other only through the host and its PF; every jail port is a private bridge
  member (the `private` network option no longer has an effect). At attach the host installs permanent ARP and neighbor
  entries for the jail (removed at detach and by the stale-entry collection), sets `staticarp` on the gateway interface
  and pins the jail's MAC address to its port with a static forwarding entry; no jail may send a neighbour
  advertisement, and router advertisements and redirects are dropped (also from the host towards a jail). Verified with
  forged ARP, neighbour advertisements and router advertisements sent from inside the jails, an extra IPv4 or IPv6
  address, another jail's MAC address and a DHCP discover (darius, proxy mode; jailbe, routed mode). The anti-spoofing
  table holds the jail's own link-local address instead of `fe80::/10`, `::` passes only as DAD, and ICMPv6 errors
  (packet too big and the rest) always pass. Existing jails get the new addressing when they are restarted.
  `doctor` checks the proxy uplink (`network.uplink`), warns about a gateway interface that is not `staticarp` yet (`network.staticarp`) and warns without a `scrub` rule.
- Stale `/128` host routes of proxy networks are removed by the reconcile loop; a refused announcement is retried;
  other hosts' DAD for a proxied address is answered; proxy subnets are /48 to /126, overlapping network subnets are
  refused, and the reserved IPv6 addresses are never allocated.
- The gateway addresses of a network moved from the bridge to a gateway epair (the bridge is layer 2 only), so that a
  jail's traffic to the host and to routed networks meets the jail's own rules: `default_out: deny`, egress rules
  and anti-spoofing now work towards the host and the internet (they did not; see design 18.9).
- Projects are isolated from each other by default (every jail has an anchor); a project policy's defaults are a floor.
- Fixed pf grammar errors in the generated rules (`icmp6-type`, `tagged`), and the `::` source address (DAD) that pf
  silently dropped from the antispoof table. The unverified `network.pf_placement: bridge` option is gone.
- `scripts/ci-freebsd-pf.sh`: the packet filter tested with real traffic, run by the e2e script; with `FORGE_BIN` set it also sends forged ARP, ICMPv6 and router-advertisement frames from inside a jail (`scripts/forge`) and checks real effects, not rule text (phase 0e).
- Stopping a jail retries a busy unmount for a few seconds; the daemon waits for the helper's socket at start.

### Security
- The reconciler's host pass can be asserted on demand (`Reconciler.AssertHost`), so building a Linux image loads the
  Linux kernel modules and sets the fallback brand before it runs, instead of needing a Linux jail to already exist;
  the host pass also keeps them loaded while a Linux image is defined. `doctor` warns (`network.dns`) about a network
  in use with no nameserver, and (`network.staticarp`) about a gateway interface a jail started before this version
  left resolving addresses with ARP.
- `jailopsd`'s `REQUIRE` line now names `pf` (`packaging/rc.d/jailopsd`): without it rcorder had no reason to start pf
  before jailopsd, and a host where it lost that race would have no filtering at all, not even the administrator's
  default-deny, while jailopsd created bridges and started jails (design 18.14). The helper's `fs.read_file` can now
  read `/etc/rc.conf` (read-only, like `/etc/sysctl.conf` and `/etc/resolv.conf` already were): `doctor`'s persistent-
  forwarding check reads it for `gateway_enable`/`ipv6_gateway_enable` and, without this, always warned even when
  they were set (found live on darius).
- Boot-order audit (design 18.14): a jail's own PF anchor loads only after its `exec.start` (the guest's whole boot)
  finishes, which an anchor call that resolves to nothing falls through safely on a default-deny host with the anchor
  line placed correctly (both checked by `doctor`), but depends on pf actually being up, which the `REQUIRE: pf` fix
  above now guarantees. Measured at the daemon level with a jail whose `exec.start` sent spoofed-source pings (refused
  by the kernel before leaving it: a jail cannot bind a raw socket to an address it does not own) and forged ARP from
  the first instant it ran: zero packets reached either an external observer or a local capture, and a production
  jail's reachability had no downtime. A literal power-cycle reboot could not be measured on the test host: its
  disposable, loopback-file ZFS pool does not survive a reboot (`/tmp` is cleared at boot, and `zpool import -a` does
  not scan arbitrary file paths even from a location that is cleared) — a property of the test harness, not of
  JailOps or of this fix.
- The NDP proxy's per-target answer budget is now keyed by `(target, requester MAC)`, not by target alone: a flood of
  solicitations for one address that only forges its claimed source IP (free, no state) can no longer drain what a
  different requester's own address — notably the real router's — needs for the very same target (design 18.10).
- `scripts/forge` (built by `just build-forge`) sends forged ARP, ICMPv6 and router-advertisement frames from inside a
  jail for the PF test matrix's forgery phase (0e, `FORGE_BIN`): real effects are asserted (host ARP/ND caches
  unchanged, the victim's traffic keeps flowing, the attacker gains no address, nothing reaches the uplink), not rule
  presence.
- TOTP codes are single-use under concurrency; password and TOTP-confirmation checks are rate-limited; hubs refuse
  authentication endpoints and unaddressed claims; client certificate dates and key usage are enforced; JSON bodies
  need `application/json`; session cookies are tied to the transport; stricter CSP, HSTS; backup-node login limits,
  login Origin check, bad-token limit and session invalidation on password change.
- The helper's authority over file agents comes from records it holds itself (datasets it created incomplete, snapshots
  it made for a backup), never from ZFS user properties, which the daemon can set.
- The daemon's state directory is its own (a separate dataset outside the storage root); the helper no longer reads or
  writes there. The storage root and the scratch directory belong to root and are checked before every use.
- The helper validates the pkg repository configuration it is pointed at (signed, http(s) URL), accepts `devfs unhide`
  only for known device paths, restricts `mount.fstab` to its own directory, leaves existing directories alone in
  `MkdirAll`, and bounds the life of a file agent.
- A hub cannot reach the login, credential or session endpoints of a host; a requestor claim must name its audience;
  client certificates are checked for validity period and key usage.
- Browser consoles reach jail terminals through a session over POST and server-sent events (`jail_console`).

### Fleet sync and secrets
- Fleet repositories can hold SOPS-encrypted files: `shared/secrets.sops.yaml` (jail environment values
  `secret:<dotted.path>`, sealed to the hosts when applied, hashed from the stored ciphertext so plans need no key)
  and `hosts/<host>/remote.sops.yaml` (url, pinned server fingerprint and an encrypted client certificate for
  pinned mTLS), governed by a `.sops.yaml` policy. The existing `secrets/<name>.age` mechanism is unchanged.
- The key comes from `SOPS_AGE_KEY`, `SOPS_AGE_KEY_FILE` (age plugin identities work) or `SOPS_AGE_KEY_CMD`.
- New commands: `vault status|list-recipients|add-recipient|remove-recipient` and
  `remote list|bootstrap`; `validate` and `doctor` check the SOPS files; `render` never decrypts.
- `jailops-sync` has the SOPS/age workflow: `shared/secrets.sops.yaml`, per-host `remote.sops.yaml` (pinned mTLS with a
  generated client certificate), `vault` commands for recipients, `remote bootstrap`, `secrets add-hosts`.
- Jail `env` is now used (every process started in the jail), and a host opens `sealed:age:` values with its own age
  key (`sealed_secrets`, `secrets_recipient`).
- Run against a real FreeBSD host: adopt with claim, plan, apply (a dual-stack network, a project grant, a jail), drift
  on a declared key, revert on apply.

### Packaging and installation
- FreeBSD packages (`jailops-client`, `jailops`, `jailops-backupd`) and a signed pkg repository (`scripts/pkg-build.sh`,
  `scripts/pkg-repo.sh`), GoReleaser archives and Linux packages (`.goreleaser.yaml`), a release workflow and CI
  (`.github/workflows`), including a FreeBSD VM job that runs `scripts/ci-freebsd-e2e.sh`.
- `jailops-priv init -pool POOL` sets a host up (user, datasets, directories, configuration); rc.d scripts for
  `jailopsd`, `jailops_priv` and `jailops_backupd`; a sample `backupd.yaml` and a systemd unit for Linux backup nodes.
- Backups work with the unprivileged daemon: file agents (`internal/fsagent`) read snapshots and write restored jails
  for it; `jailops label` sets the labels policies select by; a policy is looked up by project and name.

- Full restore tests boot the restored backup as a hidden jail without a network and check it from the inside.

### Fixes found by running on FreeBSD
- Pseudo-terminals use `posix_openpt` and `FIODGNAME` (FreeBSD has no `/dev/ptmx`); `exec -t` and `shell` work.
- Fleet reconcile passes are serialised (the timer and a manual pass could conflict on a project generation).
- Doctor works through the unprivileged daemon and the helper: `kldstat -q -n`, `pfctl -s rules|nat`, `kern.racct.enable`.

### Foundations, module path and rename
- Repository, design document, Justfile, API types (`sdk/api`).
- The Go module path is `jailops.net/jailops` (SDK: `jailops.net/jailops/sdk`), served by a `go-import` page on jailops.net.
- New logo: an isometric cube with bars and a prompt, as SVG (`docs/brand/`), used by the website, the favicons and
  the web consoles. The files are generated by `scripts/make_logo.py` in the website repository.
- The project is now JailOps (`jailops`, `jailopsd`, `jailops-priv`, `jailops-sync`, `jailops-backupd`).
- Repository on-disk format v1 identifiers (magic bytes `JOCH`/`JOIX`/`JOMF`/`JOCT`, recovery key prefix `JOR1`,
  key-derivation namespaces, gear-table seed) were renamed before the format was ever released, and the golden
  vectors were regenerated once; they are pinned again from here on.
