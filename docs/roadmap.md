# Roadmap

What is planned, in the order it will be built. This is a plan, not a promise: an item moves when something we learn
says it should, and a release only claims what has run on a host ([CHANGELOG.md](../CHANGELOG.md) says what shipped).

## What 1.0 is

1.0 is standalone hosts, the hub and the Git fleet, with the API and the stored state promised in [1.0.md](1.0.md): a frozen
API surface, in-place upgrades that keep what a host holds, the FreeBSD releases of
[supported-versions.md](supported-versions.md), and the release gates. It does not contain a cluster, a move of a jail to
another host or automatic failover.

## Long-term roadmap

After 1.0, in this order. The order matters more than the dates, and there are none. An item moves when something we learn says it should.

1. **A cold move of a jail between two independent hosts that one operator controls.** `jailops jail move NAME --to HOST`:
   a preflight (the other host's release, image, storage, network and quotas), stop, send the ZFS snapshots (incrementally when
   an earlier copy is there), validate on the other host, an explicit traffic switch, and the old copy kept until the new one is
   confirmed, with the option to cancel before the switch. It is not live migration: a jail shares the kernel of its host, so it
   is down for the last increment. Encrypted volumes stay encrypted (raw sends). The console shows one operation with progress.
   It builds on `jail export` and `jail import`.
2. **A copy kept on another host, promoted by a person.** `jailops replicate`: a schedule that keeps a second copy of chosen
   jails (or whole projects) on another host, with the age of the last send, the size and the errors in the console, and a
   promote of the copy by hand after a failure, with the keys and the ownership confirmed.
3. **Notifications, graphs and monitoring.** A webhook and mail sink for events that need a person; CPU, memory, network and
   disk of a jail over time from the accounting JailOps already reads; `jailops jail monitor` to restart a service that died
   and `jailops monitor` to follow what happens.
4. **One console for every host.** From the console of one host (the hub) see and manage the jails, projects, images and updates
   of all the hosts it knows, and run the move of item 1 from there.
5. **A cluster for a bounded set of logical metadata, when the need is confirmed.** Cluster mode is an additive mode beside
   standalone (design section 11): projects, policies, address sets, users and tokens replicated by a log, three nodes, no
   promise of failover. It needs the separations of design section 11.2 first, a routing domain with one writer
   ([networking.md](networking.md#networks-belong-to-a-host)) where hosts share address space, and join, leave, schema
   versions, mixed-version upgrades and quorum-loss recovery as a new contract.
6. **Automatic failover, last and only with fencing.** Promoting a replica without a person needs a rule for who may promote,
   a way to be sure the old host is down (fencing of execution, routing and storage), and a way back. No stateful jail is ever
   promoted automatically without all three ([design.md](design.md), section 8.6).

## With FreeBSD 16

Planned beside the list above, none of it a promise for 1.x releases before 16.0 exists:

- **Netlink for pf and the network, where the kernel offers it.** The helper runs `pfctl`, `ifconfig` and `route` today. A direct
  netlink interface needs no text parsing and is easier to test; the commands stay as the fallback for at least two years
  ([supported-versions.md](supported-versions.md#the-rule)).
- **More than one uplink for a network**, with the failover of multipath routes, and a source address on a route
  (`RTA_PREFSRC`) for jails with a public address.
- **FreeBSD's own key for the base packages of 16.0-RELEASE.** `image create` verifies the base packages it fetches from FreeBSD with FreeBSD's key (`pkgbase-15` for `base_release_N` of 15). A RELEASE of 16 has `base_release_0` and the key `pkgbase-16`; `internal/imagegen` has to pick it. The key of the JailOps package repository is one key for every ABI and does not change.
- **A software bill of materials** with every package release of `pkgops`.
- **CPU fairness between jails**, read from the accounting in `jailops top`, once the scheduler work of FreeBSD 16 is in a release.

## Later, if asked for

- Sign-in with an identity provider (OpenID Connect). Local users with a second factor (TOTP) exist today.
- A permission for opening a shell in a jail that is separate from the one for starting and stopping it.
- Restart a service or a jail when one of its packages was upgraded (the update record already lists what changed).
- A ready-made installation image of FreeBSD with JailOps, if people ask for one.
- `jailops jail upgrade` for an Alpine jail: the release move with apk inside one snapshot, as a FreeBSD jail has it.

## Gaps found by the workflow walkthrough

The table [test/workflows/daily-tasks.md](../test/workflows/daily-tasks.md) lists what a jail administrator does every day. Each
task that is not one plain command has an entry here, named `W-...`, with the command it should have. An entry leaves this
list in the change that adds the command; the table and the script then say OK. Every command has a page or a button in
the console as well.
- **`W-upgrade-major`**: `jailops jail upgrade NAME --release 16.0-RELEASE` upgrades the base in place across a major
  release; the ports packages are reinstalled for the new ABI by hand today (`pkg upgrade -f` inside the jail); the command
  does it inside the same snapshot once a major release exists to test it on.
- **`W-backup-nodeless`**: a backup of a jail to a directory or dataset of the host, without a separate node, key escrow and
  policy for the cases that need only a copy.
