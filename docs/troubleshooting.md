# Troubleshooting

Start with `jailops doctor`. It prints one line per check, `ok`, `warn`, `fail` or `unknown` (a check that cannot be made, for
example without FreeBSD), with a hint on the next line when there is one. `jailops explain CODE` explains an error code and
`jailops explain` lists them. Errors print as `code: message` with a hint; the exit codes are 0 ok, 1 failure, 2 usage or invalid,
3 auth, 4 not found, 5 conflict, 6 unsupported, 7 internal.

## `jailops doctor` checks

| Id | Looks at | If it is not ok |
|---|---|---|
| `host.release` | the running FreeBSD release | `fail` below 15.1: JailOps targets 15.1 and later; upgrade the host |
| `kernel.module.<name>` | `if_bridge`, `if_epair`, `pf`, `zfs` (and `linux64`, `linprocfs`, `linsysfs`, `fdescfs` while a Linux jail exists) | `kldload <name>` and add it to `/boot/loader.conf` |
| `sysctl.<name>` | the bridge `pfil_*` switches against `network.sysctl`; `kern.elf64.fallback_brand` for Linux jails | the daemon sets them at run time through the helper; check that the helper allows them |
| `sysctl.net.inet.ip.forwarding`, `sysctl.net.inet6.ip6.forwarding` | forwarding, while a network needs it | `fail`: `sysctl <name>=1` and the `rc.conf` lines of [install.md](install.md) (JailOps never sets forwarding) |
| `sysctl.net.inet6.icmp6.nd6_onlink_ns_rfc4861` | the host answers the off-link probes of a shared IPv6 segment's routers, while a `proxy`-mode network exists | `warn` (recommended): `sysctl net.inet6.icmp6.nd6_onlink_ns_rfc4861=1` and the line in `/etc/sysctl.conf`; see below |
| `sysctl.persistent` | forwarding (and the sysctl above) on and persistent | `warn`: set `gateway_enable=YES` and `ipv6_gateway_enable=YES` in `rc.conf`, `net.inet6.icmp6.nd6_onlink_ns_rfc4861=1` in `sysctl.conf` (JailOps never edits them) |
| `zfs.pool`, `zfs.pool.<storage>` | the pools are `ONLINE` | `zpool status <pool>` |
| `zfs.compat` | pool features against `zfs.compat_file` | `fail`: replication to an older host would fail; JailOps never runs `zpool upgrade` |
| `jail.volumes` | the volume datasets of the jails have `canmount=noauto` | `warn`: a volume made before that has `canmount=on` and is mounted at boot before the jail root it lies in; run the `zfs set canmount=noauto <dataset>` the hint lists |
| `pf.anchors` | the three anchor lines are in the loaded ruleset | add them to `pf.conf` in the right order and `pfctl -f /etc/pf.conf` ([install.md](install.md)) |
| `pf.order` | a `quick` pass before the `jailops` anchor | `warn`: such a rule passes its traffic for good so policies cannot filter it; move the anchor line before it |
| `network.epairs` | epairs without a jail | `warn`: the reconcile loop removes them on its next pass |
| `network.ndproxy` | every `proxy`-mode network has an answering proxy | `fail`: add `ndp-proxy = <uplink>=<prefix>` to `priv.conf` and restart `jailops_priv`; see the network's `NDPProxy` condition. `warn` in the first minutes after `jailopsd` starts: the capture of the uplink is being opened; run the doctor again |
| `jails.health` | datasets and records of every jail | `warn` names the damaged jails |
| `reconcile` | the last reconcile pass | `fail` shows the error; `warn` lists drift (rendered and live state differ) |
| `render` | every configuration renders | `warn` says which one does not |
| `db.integrity` | the SQLite database | restore from the latest `VACUUM INTO` copy in `<data_dir>/backups/` |
| `state.files` | repaired state files at start | `warn`: look at the `.previous` copies under the state directory |
| `operations.journal` | rollbacks that did not complete | `fail`: inspect the datasets of the named jails, then remove the journal record |
| `startup.recovery` | operations handled at start | informational |
| `conf.dir` | the configuration directory | check it equals `conf-dir` of `priv.conf` |

## Common failures

**`the socket does not exist: is jailopsd running?`** The daemon is not up. `service jailopsd status`, then
`service jailopsd start` and read the output: the rc script validates the configuration first and prints the problem
(`field X not found` is an unknown key; remove it). `service jailops_priv checkconfig` does the same for the helper.

**`unauthorized: authentication required` on the local socket.** Only root is recognised on the socket; any other local user is
anonymous, like an unauthenticated remote client. Run the command with `sudo`, or give it an identity: a token
(`JAILOPS_TOKEN=jot_...`, created with `sudo jailops token create`) or a client certificate (`jailops remote add`). A lost user
password is replaced by root with `sudo jailops user set-password NAME`.

**`listen.unix.admin_group is no longer supported` (or `user_group`, `user_projects`) when the daemon starts.** Remove the key from
`jailopsd.yaml`. Unix groups no longer grant a role; create the administrators as JailOps users (`sudo jailops user create NAME --admin`).

**The daemon starts and every ZFS or jail operation fails with `forbidden`.** The helper refuses what `priv.conf` does not allow.
The message names the line to add (`ndp-proxy`, `admin-host-mount`, ...). Edit `priv.conf` as root, `service jailops_priv restart`.
`jailops-priv check-config` prints the effective roots and allow-lists.

**`a project is required`.** There is no default project. Pass `--project NAME`, set `JAILOPS_PROJECT`, or
`jailops project switch NAME`. **`a jail needs a network` / `a jail needs a storage`:** name them with `--network` and `--storage`,
or use a profile that carries them. Nothing is inferred.

**`network N is already granted to project P`.** A network belongs to exactly one project. Use another network, or revoke the grant
(`jailops network revoke`).

**A jail cannot reach another project's jail.** By design: projects are closed to each other. Open it with a rule, for example
an address set and a policy rule `allow in tcp from @name port ...`.

**A jail starts and has no network.** `jailops network show NAME` (conditions), `jailops doctor` (`sysctl.*`, `kernel.module.*`,
`pf.anchors`). Check that forwarding is on for routed networks and that `pfil_member` is 1.

**A switched-IPv6 jail is not reachable from outside.** `jailops doctor` (`network.ndproxy`); then on the host
`tcpdump -n -e -i <uplink> 'icmp6 and (ip6[40] == 135 or ip6[40] == 136)'` while the router asks: you should see a neighbor
solicitation for the jail's address and an advertisement from the host's MAC within a millisecond. The first packets to a *new*
jail can wait a few seconds for the router's own timers (the proxy announces the address, which usually avoids it). If the
solicitation never arrives, the provider may be filtering by MAC or not delegating the prefix to you: this is the case the proxy
cannot fix (design section 16).

**Host is unreachable from a monitor every few hours on a shared IPv6 segment.** The host's own IPv6 address (not only
the jails') is unreachable for a few minutes at a time, a few times a day; `jailops doctor` fails
`sysctl.net.inet6.icmp6.nd6_onlink_ns_rfc4861`. The provider's routers probe the host's address with unicast neighbor
solicitations sent from their global addresses; the host holds a `/128` and no router advertisement describes the
`/64`, so FreeBSD's default drops them as off-link and the router marks the host unreachable. Check:
`netstat -s -p icmp6 | grep bad` (the counter of bad neighbor solicitations grows) and
`tcpdump -n -e -i <uplink> 'icmp6 and ip6[40] == 135 and ip6 dst host <host address>'` (probes arrive, no
advertisement follows). Fix: `sysctl net.inet6.icmp6.nd6_onlink_ns_rfc4861=1` now and the line
`net.inet6.icmp6.nd6_onlink_ns_rfc4861=1` in `/etc/sysctl.conf`; JailOps never sets it. See
[networking-ipv6.md](networking-ipv6.md).

**Jail deletion or unmount reports `busy`.** Stopping a jail retries a busy unmount for a few seconds; if it persists, something
inside still holds a file open (`fstat -f /jailops/projects/...`).

**An `/etc` merge conflict (`merge_conflict`) on upgrade.** The jail stays on the old generation. Resolve the listed files in the
jail, then `jailops jail upgrade` again.

**`unverified_platform`.** The feature needs a facility this host does not have, or that was never proven on FreeBSD 15.1 or later.
The hint says which.

## Error codes

| Code | Meaning | What to do |
|---|---|---|
| `invalid` | a field is missing, malformed or out of range | read the message, correct the field |
| `not_found` | the object does not exist, or you may not see it | list the kind; check `--host`, `--remote`, `--project` and the spelling |
| `already_exists` | that kind and name exist | pick another name or edit the existing one |
| `conflict` | the object changed after you read it (HTTP 412), or another project holds the network | read it again and repeat |
| `unauthorized` | no token, an expired or revoked one, an unknown certificate | `jailops remote list`, `jailops doctor`, a new token |
| `forbidden` | the role or scope does not allow it, or the helper refused | ask for a higher role, or see `priv.conf` |
| `unsupported` | the server lacks the feature or an API extension | `jailops server info` lists the extensions |
| `busy` | another operation holds a lock, or the server is overloaded | `jailops op list`, retry |
| `internal` | the server failed | retry once, then report the request id and read the server log |
| `quota_exceeded` | a project quota (jails, CPU, memory, PIDs, addresses, storage) | `jailops project show NAME`; a global admin raises it |
| `ip_conflict`, `pool_exhausted` | the address is taken or outside the range; the range is full | use `auto`; `jailops network leases NAME`; widen the range |
| `image_in_use` | a jail still uses the image or generation (the error names the jails; an alias alone never blocks) | upgrade or delete those jails |
| `data_loss_unconfirmed` | the operation would discard data | type the jail name or pass `--confirm-data-loss` |
| `merge_conflict` | an `/etc` merge needs a human | see above |
| `zfs_incompatible` | the pool lacks a feature the operation needs | check the pool features on both ends |
| `chain_broken` | a backup chain has a missing or corrupt link | `jailops backup verify`, take a new full backup |
| `key_not_escrowed` | a raw backup was refused because the key has no escrow | `jailops backup key escrow` |
| `rate_limited` | too many failed authentications from this source | wait and use the right credential |
| `maintenance`, `missing_chunks`, `lease_expired`, `plaintext_denied` | backup node states | `jailops explain CODE` |
| `not_leader`, `no_quorum`, `version_skew` | cluster and version states (cluster mode is not built) | `jailops explain CODE` |

## Logs

`jailopsd` and `jailops-priv` log to syslog (`/var/log/messages`) under rc; the helper writes an audit line per request
(`priv.request`, with the operation, the outcome and the user id; the daemon's pid is not recorded). `jailops audit` shows the
daemon's own audit log, and `jailops op list` / `jailops events` show operations and events. Set `log.level: debug` in
`jailopsd.yaml` for more.

The logs of a jail itself are read with `jailops jail logs NAME` (the files below its `/var/log`, rotated copies included)
and `jailops jail logs NAME FILE` (the last 200 lines; `--tail`, `--since 15m`, `--grep TEXT`, `-f` to follow, `-o json`
for the server's reply), running or stopped, through the server: no shell in the jail is needed, the operator role is, and
every read is audited. A stopped jail's root is mounted only while it runs, so its logs answer `busy: jail "x" is not mounted`
with the dataset to `zfs mount` on the host. A compressed rotated copy (`messages.1.gz`) is listed but answers
`not readable (compressed)`: read it from inside the jail. The log events the server publishes (not a file) are
`jailops jail logs NAME --events`.

`jailops-backupd` logs to syslog under the tag `jailops-backupd` when it runs under rc. `jailops-backupd status` shows
the repository (mode, snapshots, chunks, open sessions, quarantined chunks, last GC and verify), `jailops-backupd jobs`
the recent jobs and `jailops-backupd jobs ID` one job with its result or error. Both talk to the running daemon through
its admin socket, so they need root; `jailops-backupd check-config` validates the configuration without the daemon.
