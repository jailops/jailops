# Host management (appliance mode)

JailOps manages jails. By default it leaves the host alone: the administrator sets the host name, the network, the
host's own firewall, the time service and the system updates with the tools of FreeBSD, and JailOps never edits
`pf.conf`, `rc.conf`, `resolv.conf` or any other file of the host's own configuration.

Appliance mode is the **optional** exception. An administrator who wants to run a host from the console alone (set its
name, its addresses, its firewall, its updates) can hand JailOps one area of the host at a time. Each area is a
**module**, and each module is off until it is listed by name in two configuration files. Nothing changes for a host that
does not opt in: the light, command-line use stays the default.

This page is the design of all modules and the description of the one that is built. What is built and what is not is
in the table below; everything that has not run on a FreeBSD host is marked UNVERIFIED.

## Status

| Part | State |
|---|---|
| Opt-in switches (`host.manage.modules` in `jailopsd.yaml`, `host-manage` in `priv.conf`) | Built, for `identity` |
| Module 1, identity: host name, resolvers, NTP | Built: API, helper operations, CLI, doctor checks, console page. UNVERIFIED on FreeBSD: tested against real files in temporary directories and fake commands on the development host; the end-to-end block of `scripts/ci-freebsd-e2e.sh` (off unless `JAILOPS_E2E_HOST=1`) has not run on a host yet |
| Snapshots before a change, restore | Built for module 1 |
| Commit confirmed (automatic revert) | Design only |
| Boot environment before a risky change | Design only |
| Module 2, host firewall | Design only |
| Module 3, network | Design only |
| Module 4, updates | Design only |
| Module 5, system | Design only |
| Module 6, storage overview | Design only |
| First-boot wizard | Design only |
| Installer image | Design only |

## Goals and non-goals

Goals:

- A host can be run from the console without a shell, one area at a time, when the administrator chooses so.
- Each module owns an exact, documented set of files and settings, and nothing else.
- Every change can be previewed (a dry run with a diff), is audited, is preceded by a copy of exactly the files it
  touches, and can be undone with one command.
- A change that can cut the administrator off (network, firewall) reverts itself unless it is confirmed, and the revert
  does not depend on the daemon or the console being reachable.
- A hand edit is never silently overwritten: it is reported as drift.
- The root helper stays narrow: a module adds a few typed operations that take validated fields, never a file body or a
  command line from the daemon.

Non-goals:

- A general host configuration tool. Modules cover what a host that runs jails needs; anything else stays with the
  administrator.
- Owning the host by default. Without the switches JailOps behaves exactly as before appliance mode existed.
- Managing other operating systems, or FreeBSD releases before 15.1.
- Replacing the administrator's shell. Everything a module does is plain FreeBSD configuration that can be read and
  changed by hand; the module then reports the change.
- Cluster-wide host management. A module manages the host its daemon runs on; a hub forwards requests like any other.

## The opt-in model

### Two switches, both needed

A module is managed only when **both** files list it:

```yaml
# /usr/local/etc/jailops/jailopsd.yaml
host:
  manage:
    modules: [identity]
```

```
# /usr/local/etc/jailops/priv.conf
host-manage = identity
```

Restart both services after a change (`service jailops_priv restart`, `service jailopsd restart`).

The default of both is an empty list. Then:

- The daemon refuses every change of that module with the code `not_managed` (HTTP 403). The error's hint names the
  lines to add. `jailops explain not_managed` says the same.
- The helper refuses the module's changing operations with `forbidden`, whatever the daemon asks.
- Reading always works: `GET /api/v1/host/identity`, `jailops host show` and the console page show the host's current
  values read-only, and say that the module is not managed and how to enable it.

Why two switches: `priv.conf` is root's file and the operator's authority over the root helper (see
[security-model.md](security-model.md)). `jailopsd.yaml` is the daemon's file. If the daemon's switch alone were
enough, a compromised daemon could opt itself in by ignoring its own configuration. The helper's switch is the
boundary; the daemon's switch makes the choice visible where people configure the daemon and lets the API answer with
a clear error instead of a refusal from below. The status of the object reports both (`config_enabled`,
`helper_enabled`), and `managed` is true only when both are on.

### What a module owns, and how its files are marked

A module owns an exact list of files, blocks inside files, and `rc.conf` variables, written in this page and in the
helper's code. It never touches anything else. Two ways of marking:

- **A whole file** the module renders starts with the header line `# managed by jailops`, followed by one line of
  explanation. Module 1 owns `/etc/resolv.conf` this way.
- **A managed block** inside a file the administrator also edits sits between `# jailops begin` and `# jailops end`.
  Everything outside the markers is left byte for byte as it was. Module 1 owns such a block in `/etc/ntp.conf`. A file
  whose markers are broken (a begin without an end, two blocks, an end before the begin) is refused: the helper never
  guesses where a block ends.
- **`rc.conf` variables** are named one by one in an allow-list and set with `sysrc(8)`; other variables are never
  written.

### Drift: a hand edit is reported, never silently overwritten

The helper keeps a record of what a module last applied (for module 1, `<host-dir>/identity.json`). When the host's
current values differ from that record, the module reports **drift**: `jailops doctor` warns (check `host.identity`),
`jailops host show` lists the drifting parts, and the console shows a drift badge. Nothing is changed back on its own.
The administrator either applies again (the file is rewritten, after a snapshot) or keeps the hand edit, in which case
the next apply will show it in the diff.

A module may later offer an explicit `reconcile: true` setting that makes the daemon re-apply on drift, the way it
asserts the bridge sysctls today. That is opt-in per module and documented with the module; module 1 does not offer it.

## Common mechanics of every module

Each module follows the same pattern, so that what is learned on one applies to all.

- **A declarative singleton object.** Each module is one object of a `Host*` kind under `/api/v1/host/...`
  (`HostIdentity` at `/api/v1/host/identity`). `GET` returns the values read from the host as `spec` and the management
  state as `status` (managed, drift, what was last applied). `PUT` with a spec asks for that state; a part left out of
  the spec is not changed.
- **Dry run.** Every change accepts `dry_run`. The reply is the same as for a real change: a unified diff of the files
  the module renders, and the list of actions in order (commands that would run, services that would restart). Nothing
  is written. The console shows this as Preview before Apply.
- **Snapshot of exactly the touched files.** Before a change, the helper copies each file that will change into a new
  snapshot directory with a manifest (module, time, reason, each file's path, mode, owner, and whether it existed). The
  apply's reply names the snapshot. `jailops host restore [SNAPSHOT]` (default: the latest) puts the files back byte for
  byte, removes a file that did not exist before, and re-runs what makes the files take effect. A restore first takes a
  snapshot of its own (reason `restore`), so a restore can be undone too. The last 20 snapshots are kept.
- **A boot environment first, for risky modules.** Modules that change what the host boots (updates) or that can make it
  unreachable after a reboot (network, firewall) create a boot environment with `bectl create jailops-<module>-<time>`
  before the change, and name it in the reply. Falling back to it is the last resort when everything else fails; the
  loader menu offers it. UNVERIFIED: `bectl` on the root pool layouts `bsdinstall` creates on 15.1.
- **Audit.** Every `PUT` and `POST` under `/api/v1/host` is in the audit log like any other change (dry runs too, with
  `dry_run` in the detail), and the helper's own audit log records each of its operations.
- **Roles.** Changing the host needs the `admin` role and a global identity. `operator` is not enough: these are not
  jail operations, and a host change affects every project.
- **Idempotent.** Applying the same spec twice changes nothing the second time (`changed: false`, no snapshot, no
  restart).
- **The helper renders.** The daemon sends validated, structured fields. The helper validates them again and renders the
  files itself. No module accepts a file body or a command line from the daemon.
- **Atomic writes.** A file is written to a temporary file in the same directory, synced, and renamed over the old one;
  mode and owner are kept.
- **A failed change rolls back.** When a step fails after the snapshot was taken, the helper puts the snapshot back (best
  effort) and the error says whether that worked and which snapshot to restore by hand otherwise.

### Commit confirmed (network and firewall; design)

A change to the host's addresses, routes or firewall can cut off the administrator who made it, and with them the
console that would undo it. Such modules use **commit confirmed**: the change is applied, and it reverts itself after a
time unless it is confirmed.

1. The administrator applies with a confirmation window, for example `jailops host network set ... --confirm-within 120s`
   (default 120 seconds, at most 30 minutes). The reply says the change is pending.
2. The helper writes a **pending change record** in its own directory (`<host-dir>/pending.json`: module, snapshot
   name, deadline, boot environment if any) and syncs it before it touches anything. Then it applies the change.
3. The helper arms a **timer in its own process**. When the deadline passes without a confirmation, the helper restores
   the snapshot (and re-runs what makes it take effect: `service netif restart`, `pfctl -f`), removes the record and
   audits the revert.
4. The administrator confirms with `jailops host confirm` or the Confirm button the console shows with a countdown. The
   daemon calls the helper's `host.confirm` operation, which removes the record and disarms the timer. A confirmation
   that arrives through the new configuration proves the administrator can still reach the host; that is the point.

Why the timer lives in the helper and not in the daemon: the daemon may be the thing that is unreachable, may be
restarted, or may be the compromised party. The helper is root, starts before the daemon and is the only process that
can revert the files anyway. A console that the change locked out cannot prevent the revert, because nothing has to
reach the host for it to happen.

What survives what:

- **The daemon restarts**: nothing changes; the record and the timer are in the helper.
- **The helper restarts**: at start the helper reads `pending.json`. If the deadline has passed it reverts at once;
  otherwise it re-arms the timer for the time left.
- **The host reboots before the deadline**: an rc.d script of the helper package, ordered before `netif` and `pf`,
  checks for `pending.json` at boot. An unconfirmed record is reverted before the network comes up (the snapshot is put
  back, so the network starts with the old configuration), the record is moved aside and the revert is logged. A reboot
  therefore never makes an unconfirmed change permanent.
- **Two pending changes**: refused. One change waits for its confirmation at a time.

Commit confirmed is design only. Module 1 does not need it: a wrong host name or resolver does not cut off the console.

## The modules, in build order

### 1. Identity: host name, resolvers, NTP (built)

Scope: the host name (one label or a fully qualified name; the domain is part of it), the resolvers and search domains,
and the NTP servers and whether `ntpd` runs. Kind `HostIdentity`, extension `host_identity`.

**What it owns, exactly:**

| What | How |
|---|---|
| `/etc/resolv.conf` | the whole file, rendered by the helper: `# managed by jailops`, one comment line, a `search` line when there are search domains, then one `nameserver` line per server |
| `/etc/ntp.conf` | only the block between `# jailops begin` and `# jailops end`: one `server <name> iburst` line per server. Every other line stays, including the default `pool` lines. The block is appended at the end when the file has none, and removed when the server list is empty. A file with broken markers is refused |
| `/etc/rc.conf` variables | `hostname`, `ntpd_enable`, `ntpd_sync_on_start`, nothing else (an allow-list in the helper), set with `/usr/sbin/sysrc -f /etc/rc.conf name=value` |
| the running host name | `/bin/hostname <name>` |
| `ntpd` | `/usr/sbin/service ntpd restart` when NTP is enabled and the block or the switch changed; `/usr/sbin/service ntpd onestop` when it is disabled |

Reading uses `/bin/hostname`, a parse of `/etc/resolv.conf`, `/etc/ntp.conf` and `/etc/rc.conf`, and `/usr/bin/ntpq -pn`
for the time sync state (a line starting with `*` is the selected system peer: synchronised). Only the values the module
manages leave the helper, never the contents of the files.

In Go: `c.HostSystem().Identity().Get(ctx)`, `.Apply(ctx, spec, dryRun)`, `c.HostSystem().Snapshots(ctx)` and
`c.HostSystem().Restore(ctx, name, dryRun)` (`c.Host(name)` was taken: it scopes a hub's client to one of its hosts).

**Validated values** (by the client, the daemon and again by the helper):

- host name: RFC 1123 labels (letters, digits, hyphens, at most 63 characters each, not starting or ending with a
  hyphen), at most 253 characters, not an IP address;
- nameservers: 1 to 3 IPv4 or IPv6 literals, no zone, not the unspecified address, no duplicates;
- search domains: at most 6, valid domain names, at most 256 characters together, no duplicates;
- NTP servers: at most 4 host names or IP literals, no duplicates.

No value can carry a newline, a space, a quote or a `#`, so none can add a line to a file.

**API, CLI, console:**

```
GET  /api/v1/host/identity                       values and management state (readonly role, global identity)
PUT  /api/v1/host/identity                       {"spec": {...}, "dry_run": true}  (admin)
GET  /api/v1/host/snapshots                      the snapshots
POST /api/v1/host/snapshots/{name}/restore       {"dry_run": true}  (admin; name "latest" for the newest)
```

```
jailops host show
jailops host identity set --hostname web1.example.org --nameserver 192.0.2.53 --nameserver 2001:db8::53 \
    --search example.org --ntp-server ntp1.example.org --ntp enable --dry-run
jailops host snapshots
jailops host restore [SNAPSHOT] [--dry-run]
```

The replies are synchronous (no operation): the change takes a few seconds. The console's Host page has the identity
form, shows whether the module is managed (with the exact lines to enable it), shows the dry-run diff before Apply,
lists the snapshots with a Restore button, and shows a drift badge.

**Doctor:** `host.identity` is ok with the message "not managed" when the module is off (the doctor has no
informational level), warns on drift, and is ok when managed without drift. `host.ntp` warns when `ntpd_enable` is not
`YES` or `ntpq` shows no system peer, and is unknown with the reason when `ntpq` cannot be asked.

**Risks and limits:**

- On a host that gets its address by DHCP, `dhclient(8)` and `resolvconf(8)` rewrite `/etc/resolv.conf` on every lease.
  The module then reports drift. Stop that by setting `resolv_conf="/dev/null"` in `/etc/resolvconf.conf`
  (UNVERIFIED on 15.1), or do not manage the resolvers on such a host.
- `/etc/rc.conf.local` and files under `/etc/rc.conf.d` are not read. A `hostname` set there wins at boot over the one
  the module writes; the module does not detect it.
- The running host name and the one in `rc.conf` can differ; the status shows both (`spec.hostname` and
  `status.rc_hostname`) and drift covers both.
- The daemon's generated TLS certificate and anything else that was made with the old name keep it.
- A restore puts whole files back. A change made to `/etc/rc.conf` (or `/etc/ntp.conf`) after the snapshot, to a line the
  module does not own, is undone too; `jailops host restore --dry-run` shows it, and the restore's own snapshot keeps it.
- Everything above is UNVERIFIED on FreeBSD until the end-to-end block has run on a host: `sysrc -f` on a stock
  `rc.conf`, `service ntpd onestop` when ntpd is not running, the `ntpq -pn` output on 15.1, and the restore putting the
  originals back byte for byte.

### 2. Host firewall (design)

Scope: the host's own packet filter: what may reach the host itself (ssh, the console, the API), not the jails.

Today JailOps never owns `pf.conf`: the administrator adds three anchor lines and JailOps loads only its anchors
(`jailops/...`). With the firewall module enabled, and only then, **JailOps owns `/etc/pf.conf`** (whole-file, with the
header) and the `rc.conf` variables `pf_enable` and `pf_rules`. The rendered file contains, in pf's required order:

1. options and normalisation;
2. the translation anchors JailOps already needs (`nat-anchor "jailops/*"`, `rdr-anchor "jailops/*"`);
3. the host policy from a template;
4. the filter anchor `anchor "jailops/*"` for the jails, unchanged.

The jail anchors keep working exactly as now; the module only writes the part around them that the administrator used
to write. Templates:

- **default**: block inbound to the host; pass ssh (22) and the console/API port (`listen.https.address`, 39110 by
  default); pass ICMP and ICMPv6 needed for path MTU and neighbor discovery; pass all outbound with state.
- **open**: pass everything to the host (for a host behind another firewall).

An allow list from the console narrows ssh and the console to address sets (`HostFirewall.spec.admin_sources`). The
rendered ruleset is checked with `pfctl -n` before it is loaded, loaded with `pfctl -f`, and read back, the same way the
jail anchors are. Every change uses commit confirmed. Before the first enable, the module refuses unless the address
the request came from is admitted by the new ruleset (a guard against the obvious lock-out), and still uses the
confirmation window.

Risks: a lock-out (mitigated by commit confirmed and the guard); an existing hand-written `pf.conf` with rules the
template lacks (the first apply shows it in the diff, and the snapshot keeps it); pf syntax changes between releases
(the PF traffic matrix is the acceptance test).

### 3. Network (design)

Scope, deliberately minimal: the host's interfaces and their IPv4 and IPv6 addresses (static or DHCP/SLAAC), the
default routes, and VLAN and LAGG interfaces. Owned: the `rc.conf` variables `ifconfig_<if>`, `ifconfig_<if>_ipv6`,
`ifconfig_<if>_alias<N>`, `defaultrouter`, `ipv6_defaultrouter`, `vlans_<if>`, `cloned_interfaces` entries the
module added (`vlan*`, `lagg*`), and `create_args_<if>`; only for interfaces the request names, never the `jo*`
interfaces JailOps creates for jails. Values are validated as interface names, prefixes and addresses; an `ifconfig_`
value is assembled by the helper from fields (address, prefix length, `up`, `DHCP`, `inet6 accept_rtadv`, `vlan`
tag and parent, `laggproto` and ports), never taken as a string.

Applying runs `service netif restart <if>` and `service routing restart` for the interfaces involved, always with
commit confirmed and a boot environment first. The console warns when the change touches the interface its own
connection uses.

Risks: the biggest of all modules. A lost default route or a wrong address cuts the host off; DHCP leases change the
address after the change; uplinks the jail networks use (`projects.uplink`, NDP proxy uplinks) must not be renamed under
them, so the module refuses to remove or rename an interface a JailOps network uses.

### 4. Updates (design)

Scope: updating the host's base system and its packages, with a way back.

JailOps targets 15.1 and later, where `bsdinstall` installs the base system as packages (pkgbase), `freebsd-update` is
planned for removal in 16, and JailOps itself builds images from pkgbase only. The module therefore updates the base
system with `pkg upgrade` of the base repository (`FreeBSD-*` packages) and the third-party packages with `pkg upgrade`.
A host installed from distribution sets is reported by the doctor and refused by the module (`unsupported`, with the
hint to convert the host to pkgbase); `freebsd-update` is not used.

Every update: check (`pkg upgrade -n`, shown as the dry run), `bectl create` of a boot environment, the upgrade, and a
reboot only when asked (`reboot: manual | auto` like jail updates). After the reboot the daemon checks that it came up
on the new release and that the helper and the daemon run; `jailops host rollback` activates the previous boot
environment (`bectl activate`) and reboots. Owned: nothing in `/etc`; the module acts through `pkg` and `bectl` only.

Risks: `/etc` merges after a base upgrade (pkgbase leaves `.pkgsave` files; the module reports them and does not merge
them by hand), a kernel that does not boot (the boot environment is the answer), and jails whose image generation is
older than the host (reported; images are upgraded separately, design section 4.3).

### 5. System (design)

Scope:

- reboot and shutdown (`shutdown -r now`, `shutdown -p now`) with a confirmation and an audit entry;
- a **sysctl allow-list**: a fixed list of tunables (for example `kern.ipc.somaxconn`, `vfs.zfs.arc.max`,
  `net.inet6.icmp6.nd6_onlink_ns_rfc4861`) with a value pattern each, written to a managed block of `/etc/sysctl.conf`
  and set at run time; nothing outside the list;
- **ssh keys and users**: the `authorized_keys` of named users (a managed block per user) and creating or locking local
  users with `pw(8)`, never uid 0, never changing `root`'s shell or password, and never removing the last key of the
  user the request came from.

Risks: a reboot interrupts every jail (the console says how many run); a wrong `authorized_keys` locks out ssh (the
module refuses to leave the requesting administrator with no key, and keeps a snapshot).

### 6. Storage overview (design)

Scope: read-only first. Pools (`zpool list`, `zpool status`: health, errors, last scrub), disks (`camcontrol devlist`,
SMART health where `smartctl` is installed) and a scrub schedule (`periodic.conf` variables
`daily_scrub_zfs_enable`, `daily_scrub_zfs_pools`, `daily_scrub_zfs_default_threshold` in a managed block of
`/etc/periodic.conf`). Creating pools stays with the first-boot wizard and the administrator; destroying pools is not
offered.

Risks: low; the only write is the scrub schedule.

## The first-boot wizard (design)

A host installed with the installer image (below) starts the daemon with the console on and appliance mode enabled for
`identity`, `network` and `firewall`, and without any user. The console then shows a wizard instead of the login:

1. **Setup token.** The installer prints a one-time setup token on the system console (and writes it to a root-only
   file). The wizard asks for it; without it nothing can be done, so a host reachable on the network before setup
   cannot be taken over. The token expires after 24 hours and after first use; `jailops-priv setup-token` prints a new
   one on the console.
2. **Host name** (module 1).
3. **Network**: confirm or change what DHCP gave, or set a static address (module 3, with commit confirmed).
4. **Pool**: choose the pool and the dataset for JailOps (`jailops-priv init -pool`), or create a pool on empty disks
   (a mirror or a single disk; the wizard lists the disks and says what will be erased).
5. **Admin account**: the first global administrator with a password and, optionally, TOTP.
6. **First app**: optionally install one app from the catalog (or skip).

At the end the setup token is gone, the firewall module's default template is loaded (ssh and the console open), and
the console asks the administrator to sign in.

## The installer image (design)

A FreeBSD installation image with a `bsdinstall` script (`installerconfig`) and the JailOps packages on it.

It asks only: the target disk or disks (with a clear warning), the keyboard layout and time zone, and the root password
(or an ssh key). It installs:

- the FreeBSD base system as packages (pkgbase) on ZFS on root, with boot environments;
- `jailops`, `jailops-priv`, `jailopsd` and `jailops-apps`;
- an `rc.conf` with DHCP on the first interface that has a link, `sshd_enable`, `ntpd_enable`, `jailops_priv_enable`,
  `jailopsd_enable` and `jailopsd_web="YES"`;
- `priv.conf` and `jailopsd.yaml` with appliance mode on for `identity`, `network` and `firewall`;
- the setup token, printed on the console at first boot with the address to open.

Nothing else: no extra packages, no users besides root. Everything the wizard does can be changed later from the console
or by hand.

## Security

### What the helper may touch

The helper's operations for appliance mode are typed, take structured fields, and work only when `priv.conf` lists the
module (`host-manage`). Changing operations also need the admin assertion. Built (module 1):

| Operation | Admin | Needs `host-manage` | What it may touch |
|---|---|---|---|
| `host.identity.read` | no | no | reads `/etc/resolv.conf`, `/etc/ntp.conf`, `/etc/rc.conf`, runs `/bin/hostname` and `/usr/bin/ntpq -pn`; writes nothing |
| `host.identity.apply` | yes | `identity` | writes `/etc/resolv.conf` (whole) and the jailops block of `/etc/ntp.conf`; `sysrc -f /etc/rc.conf` for `hostname`, `ntpd_enable`, `ntpd_sync_on_start` only; `/bin/hostname <name>`; `service ntpd restart` or `onestop`; the snapshot directory and `identity.json` under `host-dir`. With `dry_run` it writes nothing |
| `host.snapshot.list` | no | no | reads the snapshot manifests under `host-dir` |
| `host.snapshot.restore` | yes | `identity` | the same files as apply, from a snapshot under `host-dir`; only the paths the module owns are ever restored, whatever a manifest says |

Planned, per module: firewall: `/etc/pf.conf` and `pf_enable`, `pf_rules`; network: the `rc.conf` variables listed
above for named interfaces; updates: `pkg` and `bectl` with fixed arguments; system: `shutdown` with two fixed forms, a
managed block of `/etc/sysctl.conf` with an allow-list, managed blocks of `authorized_keys`, `pw` with fixed forms;
storage: a managed block of `/etc/periodic.conf`. Each is added as its own operations with refusal tests, like module 1.

### Where snapshots live, and why

The helper keeps snapshots and its records in its **own** directory, `host-dir` in `priv.conf` (default
`/var/db/jailops-priv/host`, owned by root, mode 0700): `snapshots/<name>/` with the copies and a manifest, and
`identity.json`, the last applied identity. A snapshot also carries the record that was current when it was taken, so a
restore puts the record back with the files.

They are not in the daemon's state directory, although that would be the obvious place. The helper never writes in a
directory the daemon can write (the daemon could swap a path component for a symlink between a check and the write).
More importantly, a restore copies files back into `/etc` as root. If the copies lived where the daemon can write, a
compromised daemon could plant an `rc.conf` of its own in a "snapshot" and ask for a restore: root code execution at
the next boot. In the helper's directory the copies are what the helper itself took from the host, and nothing else.

### Threat model

Assume the daemon is compromised. With appliance mode off, it gains nothing new: the changing operations are refused
by the helper. With a module on, it gains exactly that module's operations, with validated values:

- module 1: it can set a wrong host name, point the resolvers at a server it controls, change the NTP servers and
  stop ntpd. That is a real power (resolvers decide where names resolve, and time matters for certificates), and it is
  why the module is opt-in in `priv.conf`. It cannot write any other line into those files, any other `rc.conf`
  variable, or any file outside the list; it cannot run a command of its choosing; it cannot restore content it wrote
  itself.
- firewall and network (planned): it can open or close the host's firewall and change addresses, within the template
  and the validated fields. Commit confirmed does not protect against a compromised daemon (it can confirm); it
  protects the administrator against mistakes.

An API admin with a module enabled can change what that module owns; that is the purpose. As with the other `priv.conf`
allow-lists, enable only the modules you would let your API admins change.

### Why commit confirmed lives in the helper

The revert must happen when the daemon is down, unreachable, or cut off by the very change it applied, and after a
reboot. The helper is root, independent of the daemon, already holds the snapshot, and is started early by rc.d; the
daemon is none of these. Keeping the timer and the pending record in the helper's directory also means the daemon
cannot extend or fake a deadline: it can only confirm or let the change revert.
