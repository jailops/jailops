# Troubleshooting

Start with `jailops doctor`. It prints one line per check, `ok`, `warn`, `fail` or `unknown` (a check that cannot be made, for
example without FreeBSD), with a hint on the next line when there is one. `jailops explain CODE` explains an error code and
`jailops explain` lists them. Errors print as `code: message` with a hint; the exit codes are 0 ok, 1 failure, 2 usage or invalid,
3 auth, 4 not found, 5 conflict, 6 unsupported, 7 internal. `jailops firewall test` adds two: 0 the connection would pass, 9 it would be
blocked, 8 the test cannot say. `jailops exec` returns the status of the command that ran in the jail, and 125 when
JailOps itself failed before or while running it (a wrong invocation stays 2). With `-o json` or `-o yaml` an error is written to standard error as
`{"error": {"code", "message", "hint", "detail"}}`: the code is an API error code, or `usage`, `aborted`, `cancelled`, `unavailable` or `error` for a failure of the CLI itself.

## `jailops doctor` checks

| Id | Looks at | If it is not ok |
|---|---|---|
| `host.release` | the running FreeBSD release | `fail` below 15.1: JailOps targets 15.1 and later; upgrade the host. `warn` for a release newer than the newest in the test matrix and for another major: it is supported once the PF traffic matrix and the e2e suites pass on it ([supported-versions.md](supported-versions.md)) |
| `host.pkgbase` | whether the base system is installed from packages | `info` on pkgbase; `warn` on a host installed from distribution sets: nothing to do on 15.x, but FreeBSD 16 removes `freebsd-update`, so convert the host to pkgbase before a major upgrade |
| `kernel.module.<name>` | `if_epair`, `pf`, `zfs` (and `linux64`, `linprocfs`, `linsysfs`, `fdescfs` while a Linux jail exists) | `kldload <name>` and add it to `/boot/loader.conf` |
| `sysctl.<name>` | `kern.elf64.fallback_brand` for Linux jails | the daemon sets it at run time through the helper; check that the helper allows it |
| `sysctl.net.inet.ip.forwarding`, `sysctl.net.inet6.ip6.forwarding` | forwarding, while a network needs it | `fail`: `sysctl <name>=1` and the `rc.conf` lines of [install.md](install.md) (JailOps never sets forwarding) |
| `sysctl.net.inet6.icmp6.nd6_onlink_ns_rfc4861` | the host answers the off-link probes of a shared IPv6 segment's routers, while a network with the NDP proxy exists | `info` while it is 0 and the host has rejected no neighbor solicitation; `warn` with the count of rejected solicitations once it has: set `sysctl net.inet6.icmp6.nd6_onlink_ns_rfc4861=1` with the line in `/etc/sysctl.conf` ([networking-ipv6.md](networking-ipv6.md#one-host-setting-that-matters-on-some-segments)) |
| `host.identity` | host management, module identity ([appliance.md](appliance.md)) | `ok` "not managed" while the module is off (the doctor has no info level); `warn` when only one of `jailopsd.yaml` and `priv.conf` enables it, or on drift: the host name, `/etc/resolv.conf` or the NTP block was changed outside JailOps since it applied them (`jailops host show` names the part; apply again or `jailops host restore`) |
| `host.devd` | the daemon reads the kernel's notifications from devd ([operations.md](operations.md#events-from-the-kernel)) | `ok` while connected; `warn` with the reason when devd's socket cannot be read, and the check is absent with `devd.enable: false` |
| `host.firewall` | host management, module firewall ([appliance.md](appliance.md)) | `ok` "not managed" while the module is off; `warn` when only one of `jailopsd.yaml` and `priv.conf` enables it, while a change waits for its confirmation (`jailops host confirm` or `jailops host revert`), or on drift: `/etc/pf.conf`, `pf_enable` or `pf_rules` changed outside JailOps (`jailops host firewall diff` shows it; apply again or `jailops host restore`) |
| `appliance` | host management modules of the daemon and the helper ([appliance.md](appliance.md)) | `ok` when both manage the same modules, or none (`off`); `warn` naming a module one side manages and the other does not, with the line to add (`jailops_appliance` in `/etc/rc.conf`, `host-manage` in `priv.conf`, `host.manage.modules` in `jailopsd.yaml`) or the restart that reads `rc.conf` again |
| `tls.certificate` | the certificate the TLS listener serves ([exposure.md](exposure.md)) | `ok` with its source and days left; `warn` when one nothing renews (uploaded or the files of `jailopsd.yaml`) expires within 14 days, `fail` once expired |
| `tls.acme` | ACME ([exposure.md](exposure.md#acme-a-certificate-from-a-public-ca)) | `ok` "not used" without ACME; `warn` when the last request or renewal failed (with the error and the next attempt), when there is no certificate yet, when the firewall module does not redirect the challenge port yet (apply the host firewall again), or when the daemon cannot reach its own challenge listener |
| `host.ntp` | `ntpd_enable` and what `ntpq -pn` reports | `warn` when ntpd is not enabled or has no system peer yet; `unknown` when ntpq does not answer |
| `sysctl.persistent` | forwarding on and persistent | `warn`: set `gateway_enable=YES` and `ipv6_gateway_enable=YES` in `rc.conf`, or the forwarding lines in `sysctl.conf` (JailOps never edits them) |
| `zfs.pool`, `zfs.pool.<storage>` | the pools are `ONLINE` | `zpool status <pool>` |
| `zfs.compat` | pool features against `zfs.compat_file` | `fail`: replication to an older host would fail; JailOps never runs `zpool upgrade` |
| `zfs.block_cloning`, `zfs.block_cloning.<storage>` | per storage: whether a new jail shares the blocks of its image (about 3 MB) or is a full copy (about 50 MB) | `warn` when the pool has not enabled the feature `block_cloning` (`zpool upgrade POOL` enables it; systems older than OpenZFS 2.2 can then no longer import the pool, and JailOps never changes pool features) or when `vfs.zfs.bclone_enabled=0` (`sysctl vfs.zfs.bclone_enabled=1` and the line in `/etc/sysctl.conf`); `info` for an encrypted storage, where every jail is a full copy. No line when the state cannot be read |
| `jail.volumes` | the volume datasets of the jails have `canmount=noauto` | `warn`: a volume made before that has `canmount=on` and is mounted at boot before the jail root it lies in; run the `zfs set canmount=noauto <dataset>` the hint lists |
| `jail.devfs_ruleset` | the stock devfs rulesets 1 to 4 as the kernel has them loaded (`devfs rule -s N show`), against FreeBSD's `/etc/defaults/devfs.rules`: a FreeBSD jail without devices mounts `/dev` with ruleset 4, and a jail with devices builds on 1 to 3 | `warn` names the extra rules (an `unhide` or an `include` FreeBSD's sets do not have, such as `bpf*`, `pf` or `dri`) and the jails that get them; grant a device to the jail that needs it (`jailops config set JAIL devfs.NAME.device bpf`) instead, take the rule out of `/etc/devfs.rules` and `service devfs restart`. `fail` when ruleset 4 is not loaded: it hides nothing, so a jail on it sees the host's whole `/dev` |
| `jail.upgrade_state` | jails whose in-place upgrade did not finish | `fail` per jail: its root may hold part of the upgrade. Run the upgrade again to complete it, or stop the jail and run the rollback the hint names (`jailops jail rollback NAME --project P --confirm-data-loss`); a successful upgrade or rollback clears it |
| `jail.volume_system_dir` | volumes of a jail at a directory of its system (`/usr/local`, `/var`, `/var/db`, `/var/db/pkg`, ...), which JailOps 0.9.2 allowed | `warn` per volume: the jail starts, but its in-place upgrade is refused (what pkg writes there would be outside the root's snapshot) and no other volume can be attached or detached. The fix: move the data into a volume mounted below that directory and detach this one |
| `jail.unsupported_layout` | jails with a root layout of JailOps 0.9.2 and before that this release does not support (a clone of an image with separate datasets for `/etc`, `/usr/local` and `/var`) | `fail` per jail: it does not start; a running one can be stopped, and it can be deleted, nothing else; the jail carries the condition `UnsupportedLayout` (reason `EarlierRelease`). The fix: [upgrade.md](upgrade.md#jails-with-an-unsupported-root-layout) |
| `jail.audit` | jails whose last `jailops jail audit` since the daemon started found something ([audit.md](audit.md)) | `fail` per jail with a critical finding, `warn` with a warning, naming the checks (a suggestion of `--suggest` never counts); `jailops jail audit NAME` shows each finding and its fix. No line for a jail that was not audited |
| `jail.suspected_compromised` | jails whose root held what only an attack on host-side pkg plants (a link out of the root, a link among pkg's files, a `pkg.conf` with plugins) at the last upgrade check | `fail` per jail, with what was found: the jail is not upgraded. Look at it and remove what is listed; the next upgrade checks again and clears it |
| `pf.anchors` | the anchor line is in the loaded ruleset | add it to `pf.conf` among the filter rules and `pfctl -f /etc/pf.conf` ([install.md](install.md)) |
| `pf.order` | a `quick` pass before the `jailops` anchor | `warn`: such a rule passes its traffic for good so policies cannot filter it; move the anchor line before it |
| `pf.inbound` | while a jail on an IPv6 or a non-NAT IPv4 network exists: a `block ... quick` rule for every source before the anchor line, or a hand-written `pass ... quick` rule for a jail prefix | `fail` for the block (it drops inbound traffic to the jails before their policies are asked: decide whether that is what the rule is for; to let the policies decide, move the anchor line above it; rules for single sources, such as a blocklist, are not flagged), `warn` for the pass (it keeps a jail without an `allow in` rule reachable: delete it; the jail's own `allow in` rules open the uplink, [install.md](install.md) section 3) |
| `jails.cpuset` | the CPUs that jails are pinned to (`resources.cpuset`) exist on this host | no line when no jail has a cpuset; `ok` when every CPU exists; `warn` for a jail whose cpuset names a CPU the host does not have (it does not start; a profile written for a larger host does that); `jailops config set JAIL resources.cpuset 0-3` for the CPUs the host has |
| `jails.exposed` | the jails whose anchor has a pass rule on an uplink | `info`: lists the jails the Internet can reach and their ports (a port forward, or an `allow in` rule with an external source), so the rules a project's administrator writes for his own jail are visible to the others |
| `jails.inbound-without-source` | jails on a routed address with an `allow in` rule that names no source | `info`: such a rule is for the project's jails and the host and does not open the uplink; give it `--from any` or use `jailops expose JAIL PORTS` |
| `pf.state-policy` | a loaded ruleset that blocks by default (a `block ... all` without `quick`) while a network has NAT or jails exist, against the `pf.conf` the system loads (`pf_rules` of `rc.conf`, else `/etc/pf.conf`; includes are not followed) | `warn` when it has no `set state-policy if-bound`: jails behind NAT lose packets (only the first of a flow passes). Add the line at the top of `pf.conf` and `pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf`; never `service pf restart` over ssh. `unknown` when the daemon cannot read the file |
| `pf.host_default_deny` | the loaded ruleset blocks inbound traffic by default on each uplink of the jails' networks, each address family on its own (`block in inet all` leaves IPv6 open) | `warn` naming the uplink, and the family when only one is open (`vtnet0 (IPv6)`): pf passes what no rule decides, so the routed addresses of the jails (IPv6, IPv4 without NAT) are reachable from the Internet on every port their policies do not deny, and "closed" in `firewall test`, `firewall show` and `firewall exposed` assumes the block. Add `block in all` (both families) without `quick` before the anchor line, with a pass for ssh ([install.md](install.md#3-the-firewall-one-line-in-pfconf)). No line without jails on an uplink |
| `pf.legacy_rdr` | while a jail is exposed: the `rdr` and `binat` rules of the loaded main ruleset (`pfctl -s nat`) on the uplinks of the exposures | `warn` names each rule and the exposures whose ports it takes: pf evaluates such a rule before the forwards of `jailops expose`, so it wins and the exposure listed by `jailops expose` never takes effect. Remove the rule from `pf.conf` (the exposure makes the forward) or expose the jail on another port |
| `pf.urpf` | a `from urpf-failed` block of the loaded ruleset on an uplink of the jails, against the interfaces of the host's default routes (`netstat -rn`) | `warn` when a default route leaves through another interface: a client on the Internet comes in on the uplink and its route back leaves elsewhere, so uRPF drops it and the exposures and `allow in` rules there silently do nothing. Limit the rule to the interface of the default route (`block in quick on IF from urpf-failed`). `info` when the routes cannot be read. No line without such a rule |
| `pf.states` | the per-rule state limits of the running jails with a link (`jails.max_states`, or `resources.states`) against the hard limit of pf's state table (`pfctl -s memory`); stopped jails create no state and are not counted | `warn` when a running jail has no limit, or a limit above half the table (one busy jail could fill it, and new connections then fail host-wide, ssh included); `info` when the limits of the running jails add up to more than the table holds. Raise it with `set limit states N` in `pf.conf` (the hint gives a value), or lower `jails.max_states`. No line while no jail with a link runs |
| `network.dns` | for every network a jail uses: where its jails get their nameservers (the network's `dns`, the host's `/etc/resolv.conf`, or the gateway when the host resolves only through loopback); a gateway is asked one query (`. NS`, one second) once a jail on the network runs | `warn` when the gateway does not answer, refuses (access control) or no nameserver could be worked out; the hint has the resolver and `pf.conf` lines ([networking-ipv6.md](networking-ipv6.md#name-resolution-of-jails)) |
| `network.epairs` | epairs without a jail | `warn`: the reconcile loop removes them on its next pass |
| `net.descriptions` | every interface JailOps owns carries its description (both ends of running jails' epairs, WireGuard interfaces) | `warn` lists them with what they carry: the reconcile loop sets them on its next pass |
| `wireguard.tools` | only with a WireGuard tunnel: the wg program (`/usr/bin/wg` or `/usr/local/bin/wg`) and the module `if_wg` | `warn`: `pkg install wireguard-tools`; the loop loads `if_wg` (`if_wg_load="YES"` in `/boot/loader.conf` for boot) |
| `wireguard.forwarding` | a tunnel serves JailOps networks and forwarding is off | `warn`: `gateway_enable=YES` and `sysctl net.inet.ip.forwarding=1` (JailOps never sets forwarding) |
| `wireguard.firewall` | a loaded pf rule of the host mentions each server's listen port | `warn`: add the rule the hint prints to `pf.conf` and reload it |
| `logs.forward` | only with log sinks ([logging.md](logging.md)): whether each delivers | `warn`: a sink has failed for more than five minutes (`jailops logs forward status` shows the error, `jailops logs forward test NAME` tries again); `info`: a plain udp or tcp sink goes to a remote address |
| `network.ndproxy` | every `proxy`-mode network has an answering proxy | `fail`: see the network's `NDPProxy` condition, it says what the helper refused or what failed. `warn` in the first minutes after `jailopsd` starts: the capture of the uplink is being opened; run the doctor again |
| `network.nd_reply_path` | only while the NDP proxy captures an uplink: a router that asked for an address of the host itself three or more times within five seconds in the last day, and where the host sent its answers | `warn` names the router when the answers went to another MAC than the router's (the default gateway's): traffic through it stalls a few seconds after idle; the hint has the on-link route for `/etc/rc.conf` ([networking-ipv6.md](networking-ipv6.md#answers-to-the-providers-routers)). `info` while no solicitation for the host's addresses was seen since `jailopsd` started |
| `jails.health` | datasets and records of every jail | `warn` names the damaged jails |
| `reconcile` | the last reconcile pass | `fail` shows the error; `warn` lists drift (rendered and live state differ) |
| `render` | every configuration renders | `warn` says which one does not |
| `db.integrity` | the SQLite database | restore from the latest `VACUUM INTO` copy in `<data_dir>/backups/` |
| `state.files` | repaired state files at start | `warn`: look at the `.previous` copies under the state directory |
| `operations.journal` | rollbacks that did not complete | `fail`: inspect the datasets of the named jails, then remove the journal record |
| `startup.recovery` | operations handled at start | informational |
| `conf.dir` | the configuration directory | check it equals `conf-dir` of `priv.conf` |

## Common failures

**A web or mail jail cannot be reached from the Internet.** Ask the firewall what reaches it: `jailops firewall show web`
has a row for the Internet, and `jailops firewall test --from internet --to jail:web --port 443` says which rule decides.
An IPv6 address needs an `allow in` rule with an external source; an IPv4 address of a NAT network needs a forward of a
port of the host (`allow in` with `redirect`). When the row is open and the connection still fails, the cause is outside
the jail's anchor: the host's `pf.conf` (a `quick` rule before the `anchor "jailops/*"` line wins; `jailops doctor`,
`pf.inbound`), the upstream router, or a service that does not listen ([networking.md](networking.md#what-reaches-a-jail)).

**`pkg` says "Transient resolver failure", or every lookup on the host fails (`jailops doctor`: `dns.unbound` fails).** The host
asks its own `local_unbound` on `127.0.0.1`, and it does not answer. Look at `sockstat -46l | grep :53` (what listens),
`service local_unbound status` and `local-unbound-checkconf`. If the file `/var/unbound/conf.d/jailops.conf` is the cause, `rm
/var/unbound/conf.d/jailops.conf && service local_unbound restart` gives the host its resolver back at once, and JailOps writes
the file again with the loopback ([dns.md](dns.md#the-hosts-own-resolver-keeps-its-loopback)). When the
resolver answers but cannot resolve names outside (`dns.upstream` warns), its upstream is the thing to look at: the forward
zone of `local_unbound`, or `local-unbound-setup` without one so that it resolves from the root servers.

**A stop or a restart takes a minute, or `jailops doctor` says `mounts.leftovers`.** A stop that did not finish (the poststop
hook failed, a daemon was killed) can leave mounts below the root of the jail: a `devfs` under `root/dev`, stacked once more by each further
failed stop. They keep the root dataset busy. `jailops op show ID` lists the steps of the operation with the time each took (`unmount`
says where the minute went). `jailops doctor --fix` takes the mounts off the roots of jails that do not run (`--dry-run` lists them first;
a jail that runs is never touched, and the command can be run again). The daemon does the same when it starts and every ten minutes, and
a stop and a start of the jail do it for that jail; `--fix` also destroys the bridge and the gateway pair of the earlier network layout that no
jail which runs uses. By hand: `mount -p | grep <jail>/root/` and `umount` the lines below the root, deepest
first, once for every layer.

**A stop of a jail whose shutdown never returns.** The stop commands of a jail (`/etc/rc.shutdown`) get its stop timeout (`boot.stop_timeout_seconds`, 30 seconds unless set), and the jail the same time
again to exit after SIGTERM, with five seconds on top. After that the jail is removed without them, which kills what is left, and a
`jail-stop-forced` event and the console log of the jail (`/var/log/jailops/jail_<project>_<name>_console.log`) say so and name the processes
that still ran. A database that needs a minute to close
says `jailops config set db boot.stop_timeout_seconds 60`.

**A jail cannot resolve names** ("Transient resolver failure" from `pkg`). `jailops network show NET` says where the jails of
the network get their nameservers and `jailops doctor` (`network.dns`) checks the gateway case. On a host whose resolver
is a local cache (`nameserver 127.0.0.1` only), either let the cache answer on the gateway (the lines `network show`
prints) or give the network nameservers: `jailops network edit NET` with `dns: [192.0.2.53]`; the running jails get the
new `resolv.conf` at once.

**Jails behind NAT lose most packets** (one ping of four answered) on a host whose `pf.conf` blocks by default: add
`set state-policy if-bound` at the top of `pf.conf` and reload it (`jailops doctor`: `pf.state-policy`).

**`the socket does not exist: is jailopsd running?`** The daemon is not up. `service jailopsd status`, then
`service jailopsd start` and read the output: the rc script validates the configuration first and prints the problem
(`field X not found` is an unknown key; remove it). `service jailops_priv checkconfig` does the same for the helper.

**`GET /readyz` answers 503** ([exposure.md](exposure.md#load-balancers-and-monitors)). `not ready: starting`: the daemon
has not finished starting; if it stays, read its log. `helper`: `jailops-priv` does not answer;
`service jailops_priv status`, then `service jailopsd restart` (a helper that crashed is started again by itself after 5 seconds). `storage`: the helper
answers and the JailOps dataset is missing; `zfs list` and `service jailops_priv checkconfig`. `store`: the database does not
answer a query; `jailops doctor` (`db.integrity`) and the daemon's log. `stopping`: the daemon is shutting down.
`GET /healthz` stays `ok` in all of these: it only says the process runs.

**`unauthorized: authentication required` on the local socket.** Only root is recognised on the socket; any other local user is
anonymous, like an unauthenticated remote client. Run the command with `sudo`, or give it an identity: a token
(`JAILOPS_TOKEN=jot_...`, created with `sudo jailops token create`) or a client certificate (`jailops remote add`). A lost user
password is replaced by root with `sudo jailops user set-password NAME`.

**The daemon starts and every ZFS or jail operation fails with `forbidden`.** The helper refuses what `priv.conf` does not allow.
The message names the line to add (`admin-host-mount`, ...). Edit `priv.conf` as root, `service jailopsd restart`.
`service jailops_priv checkconfig` prints the effective roots and allow-lists.

**`forbidden: interface "NAME" is not managed by JailOps: ...`.** The helper changes only epairs that carry
JailOps's description, which it gives an interface when it creates it ([security-model.md](security-model.md#interfaces)).
The rest of the message says what the kernel showed: not an epair (the uplink, `lo0`, a bridge, a tunnel), no JailOps
description (made by hand), or a description of someone else. `forbidden: interface "NAME" exists and is not JailOps's;
the helper never takes over an interface by its name` comes from a creation under a name that is taken; remove the
leftover with `ifconfig NAME destroy`.

**`a project is required`.** There is no default project. Pass `--project NAME`, set `JAILOPS_PROJECT`, or
`jailops project switch NAME`. **`a jail needs a network` / `a jail needs a storage`:** name them with `--network` and `--storage`,
or use a profile that carries them. Nothing is inferred.

**`network N is already granted to project P`.** A network belongs to exactly one project. Use another network, or revoke the grant
(`jailops network revoke`).

**A jail cannot reach another project's jail.** By design: projects are closed to each other, like an external host.
The jail that is reached opens the way with an `allow in` rule that has a source (an address set, an address, a prefix, `any`):
`allow in tcp from set:name port ...` in the project that serves; the jail that connects needs nothing
([networking-ipv6.md](networking-ipv6.md)).

**A jail starts and has no network.** `jailops network show NAME` (conditions), `jailops doctor` (`sysctl.*`, `kernel.module.*`,
`pf.anchors`). Check that forwarding is on and that the host's `pf.conf` lets the jails' traffic out of the uplink.

**A switched-IPv6 jail is not reachable from outside.** `jailops doctor` (`network.ndproxy`); then on the host
`tcpdump -n -e -i <uplink> 'icmp6 and (ip6[40] == 135 or ip6[40] == 136)'` while the router asks: you should see a neighbor
solicitation for the jail's address and an advertisement from the host's MAC within a millisecond. The first packets to a *new*
jail can wait a few seconds for the router's own timers (the proxy announces the address, which usually avoids it). If the
solicitation never arrives, the provider may be filtering by MAC or not delegating the prefix to you: this is the case the proxy
cannot fix (design section 16).

**Host is unreachable from a monitor every few hours on a shared IPv6 segment.** The host's own IPv6 address (not only
the jails') is unreachable for a few minutes at a time, a few times a day, and the counter of bad neighbor solicitations
grows (`netstat -s -p icmp6 | grep -i 'bad neighbor solicitation'`). On some segments the routers probe the host with
unicast neighbor solicitations from their global addresses, which FreeBSD drops by default. What it is, how to be sure
and what to set (`net.inet6.icmp6.nd6_onlink_ns_rfc4861=1`, which JailOps never sets): see
[networking-ipv6.md](networking-ipv6.md#one-host-setting-that-matters-on-some-segments).

**Jail deletion or unmount reports `busy`.** Stopping a jail retries a busy unmount for a few seconds; if it persists, something
inside still holds a file open (`fstat -f /usr/local/jailops/projects/...`).

**`jailops jail upgrade` or `jail base add` is refused.** The message says why, and none of these
leaves a package changed:

- `forbidden: suspected_compromised: jail X: etc/Y is a symbolic link out of the root (to /Z); ...`: the root holds what
  only an attack on host-side pkg plants (a link out of the root, a link among pkg's files, a `pkg.conf` with plugins).
  Nothing in the jail changed, and `jailops doctor` names it (`jail.suspected_compromised`). Look at the jail, remove what
  is listed (or make a link of your own point inside the jail with a relative target) and run the command again: it
  checks again. [thick-jail-upgrades.md](thick-jail-upgrades.md#a-jail-jailops-does-not-trust).
- `precondition_failed: jail X does not run`: a jail is upgraded while it runs; JailOps does not start it.
- `precondition_failed: jail X runs: a root rolled back under running services kills them`: stop the jail, roll back,
  start it.
- `precondition_failed: the root of jail X says it is A and JailOps installed B`: the files of the root disagree with
  the release JailOps recorded. Name the release with `--release`, or roll the jail back
  ([lifecycle.md](lifecycle.md#35-a-freebsd-jail)).
- `precondition_failed: pkg's dry run said nothing JailOps understands`: pkg answered in a form JailOps does not read;
  the message carries pkg's last line.
- `nothing to add: pkg finds P installed`: not an error; the package is there.

**`jailops cp` or the Files tab answers `busy`.** An operation has the jail (an upgrade, a stop, a start) or a package
update holds it; the message names it. Copy again when it has ended (`jailops op list`).

**The daemon logs "the firewall anchor of a running jail is empty".** The daemon watches the anchor of one running
jail (the field `anchor` of the line names it) to notice a flush; that anchor was found empty, and the daemon loads the
anchors again at once. Either pf was flushed (`service pf
restart`, `pfctl -F all`), or an operation was stopping that jail at that moment (a stop, an upgrade), which empties its
anchor on purpose: then the line names the jail being stopped and needs nothing. It is logged once for that anchor,
not every few seconds; the next pass picks the anchor it watches anew. When it repeats for jails nobody stopped, look for what flushes pf on the host
([networking.md](networking.md#when-pf-is-off-or-was-flushed)).

**`unverified_platform`.** The feature needs a facility this host does not have, or that was never proven on FreeBSD 15.1 or later.
The hint says which.

### A jail has no IPv6 outside (switched network)

DNS and IPv4 work but `ping` to an IPv6 address from the jail gets no answer, and a jail that started earlier on the same
network works: run `jailops network ndp-proxy status NETWORK` and look at the jail's address. `seen` above zero with
`answered` zero means the router asked and the proxy did not answer (the last question says why); `seen` zero means the
proxy's capture never saw the question. `jailops network ndp-proxy watch NETWORK` shows the router's questions and the
proxy's answers live while you ping. If the network says `NDP proxy: off`, the provider must route the prefix to this host,
or switch the proxy on with `jailops network ndp-proxy enable NETWORK`. `jailops doctor` names addresses the router asked
for and the proxy never answered (`network.ndproxy`).

### IPv6 stalls for a few seconds after idle (switched network)

IPv6 to the host and its jails hangs for three or four seconds after a quiet minute, then works; IPv4 does not. On a
segment where the provider's routers are a VRRP pair, the host answers a router's unicast neighbor solicitation through its
default route, to the gateway's MAC, and the backup router that asked never gets the answer. `jailops doctor` warns
(`network.nd_reply_path`) with the router and the fix, an on-link route for the routers' prefix in `/etc/rc.conf`:

    ipv6_static_routes="infra"
    ipv6_route_infra="-net 2001:db8:40::/64 -interface vtnet0"

[networking-ipv6.md](networking-ipv6.md#answers-to-the-providers-routers) has the cause and how FreeBSD differs from
Linux here.

## Error codes

| Code | Meaning | What to do |
|---|---|---|
| `invalid` | a field is missing, malformed or out of range | read the message, correct the field |
| `not_found` | the object does not exist, or you may not see it | list the kind; check `--host`, `--remote`, `--project` and the spelling |
| `already_exists` | that kind and name exist | pick another name or edit the existing one |
| `precondition_failed` | the object changed after you read it (HTTP 412), or another project holds the network | read it again and repeat |
| `unauthorized` | no token, an expired or revoked one, an unknown certificate | `jailops remote list`, `jailops doctor`, a new token |
| `forbidden` | the role or scope does not allow it, or the helper refused | ask for a higher role, or see `priv.conf` |
| `unsupported` | the server lacks the feature or an API extension | `jailops server info` lists the extensions |
| `busy` | another operation holds a lock, or the server is overloaded | `jailops op list`, retry |
| `internal` | the server failed | retry once, then report the request id and read the server log |
| `interrupted` | the daemon stopped while the operation ran; `error.detail.recovery` is `rolled_back`, `completed`, `dropped` or `needs_attention` | check the object, repeat the command; for `needs_attention` read `jailops doctor` |
| `quota_exceeded` | a project quota (jails, CPU, memory, PIDs, addresses, storage) | `jailops project show NAME`; a global admin raises it |
| `ip_conflict`, `pool_exhausted` | the address is taken or outside the range; the range is full | use `auto`; `jailops network leases NAME`; widen the range |
| `data_loss_unconfirmed` | the operation would discard data | type the jail name or pass `--confirm-data-loss` |
| `zfs_incompatible` | the pool lacks a feature the operation needs | check the pool features on both ends |
| `chain_broken` | a backup chain has a missing or corrupt link | `jailops backup verify`, take a new full backup |
| `key_not_escrowed` | a raw backup was refused because the key has no escrow | `jailops backup key escrow` |
| `rate_limited` | too many failed authentications from this source | wait and use the right credential |
| `not_managed` | JailOps does not manage that part of the host: `host.manage.modules` of `jailopsd.yaml` or `host-manage` of `priv.conf` does not list the module | add the lines the hint names and restart both services, or change the host by hand ([appliance.md](appliance.md)) |
| `locked` | an encrypted volume of the jail waits for its passphrase (after a boot or a lock); the jail does not start | `jailops volume unlock NAME [--start]` ([storage.md](storage.md#encrypted-volumes)) |
| `bad_passphrase` | the passphrase of the encrypted volumes is wrong (five in a row: `rate_limited` for 30 seconds) | type it again; a lost passphrase cannot be recovered |
| `maintenance`, `missing_chunks`, `lease_expired`, `plaintext_denied` | backup node states | `jailops explain CODE` |
| `version_skew` | the client, hub and host run API versions too far apart | `jailops explain CODE` |

## Logs

`jailopsd` and `jailops-priv` log to syslog (`/var/log/messages`) under rc; the helper writes an audit line per request
(`priv.request`, with the operation, the outcome and the user id; the daemon's pid is not recorded). `jailops audit` shows the
daemon's own audit log, and `jailops op list` / `jailops events` show operations and events. Set `log.level: debug` in
`jailopsd.yaml` for more.

The logs of a jail itself are read with `jailops jail logs NAME` (the files below its `/var/log`, rotated copies included)
and `jailops jail logs NAME FILE` (the last 200 lines; `--limit`, `--since 15m`, `--grep TEXT`, `-f` to follow, `-o json`
for the server's reply), running or stopped, through the server: no shell in the jail is needed, the operator role is, and
every read is audited. A stopped jail's root is mounted only while it runs, so its logs answer `busy: jail "x" is not mounted`
with the dataset to `zfs mount` on the host. A compressed rotated copy (`messages.1.gz`) is listed but answers
`not readable (compressed)`: read it from inside the jail. The log events the server publishes (not a file) are
`jailops jail logs NAME --events`.

`jailops-backupd` logs to syslog under the tag `jailops-backupd` when it runs under rc. `jailops-backupd status` shows
the repository (mode, snapshots, chunks, open sessions, quarantined chunks, last GC and verify), `jailops-backupd jobs`
the recent jobs and `jailops-backupd jobs ID` one job with its result or error. Both talk to the running daemon through
its admin socket, so they need root; `jailops-backupd check-config` validates the configuration without the daemon.
