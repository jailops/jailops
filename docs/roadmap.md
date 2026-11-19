# Roadmap

What is planned, in the order it will be built. This is a plan, not a promise: an item moves when something we learn
says it should, and a release only claims what has run on a host ([CHANGELOG.md](../CHANGELOG.md) says what shipped).

## Long-term roadmap: more than one host, and knowing what happens

The goal of this part is that a small fleet of hosts can be run with the same ease as one host, without a cluster to
operate and without pretending that jails can be live-migrated.

1. **Move a jail to another host.** `jailops jail move NAME --to HOST`: stop, send the ZFS snapshots (incrementally when
   an earlier copy is there), start on the other host, run the health check, and keep the old copy until the new one is
   confirmed. It is not live migration (a jail shares the kernel of its host, so there is none): the jail is down for
   the last increment. Encrypted volumes stay encrypted (raw sends). The console shows it as one operation with progress.
2. **Replication between hosts.** A schedule that keeps a second copy of chosen jails (or whole projects) on another host:
   `jailops replicate`, with the age of the last send, the size and errors shown in the console, and a one-command
   restore or promote of the copy by hand after a failure. This is the "enough availability" for most users: a copy that is
   minutes old and a person who decides.
3. **Notifications.** A webhook and mail sink for events that need a person: a failed nightly upgrade, a replication that
   lags, a certificate that expires, a pool that fills, a jail that does not come back after an update. The log
   forwarding sinks already exist; this adds the rules for what is sent and when.
4. **Keep a service running.** `jailops jail monitor enable NAME [--service nginx]` is a persistent switch: a service of the jail that
   died is started again, and every restart is an event, so that a notification (item 3) can name it. The health checks that run
   inside an update stay as they are.
5. **`jailops monitor`.** One line per jail that keeps being printed while something changes (state, health, restarts,
   limits reached), for a terminal or a script; `jailops top` shows the usage of the moment, this shows what happens.
6. **Graphs.** CPU, memory, network and disk of a jail over time in the console, from the accounting JailOps already
   reads (rctl, ZFS), kept in the operational database with a bounded retention.
7. **One console for every host.** From the console of one host (the hub) see the jails, projects, images and
   updates of all the hosts it knows, manage them from there and move a jail between them (item 1). The hub and its
   Hosts page exist; this makes them the place where a small fleet is run, with the same search and shell access as on a
   single host.
8. **Automatic failover, last and only with fencing.** Promoting a replica without a person needs a rule for who may
   promote, a way to be sure the old host is down (fencing), and a way back. It is built after 1 to 7, and no stateful
   jail is ever promoted automatically without all three ([design.md](design.md), section 8.6).

## Later, if asked for

- Sign-in with an identity provider (OpenID Connect). Local users with a second factor (TOTP) exist today.
- A permission for opening a shell in a jail that is separate from the one for starting and stopping it.
- Restart a service or a jail when one of its packages was upgraded (the update record already lists what changed).
- A ready-made installation image of FreeBSD with JailOps, if people ask for one.

## Gaps found by the workflow walkthrough

The table [test/workflows/daily-tasks.md](../test/workflows/daily-tasks.md) lists what a jail administrator does every day. Each
task that is not one plain command has an entry here, named `W-...`, with the command it should have. An entry leaves this
list in the change that adds the command; the table and the script then say OK. Every command has a page or a button in
the console as well.
- **`W-upgrade-major`**: `jailops jail upgrade NAME --image IMG` moves the root across a major release and warns about the packages, which are
  reinstalled by hand today; the command reinstalls them for the new ABI inside the same snapshot and rollback once a major
  release exists to test it on.
- **`W-backup-nodeless`**: a backup of a jail to a directory or dataset of the host, without a separate node, key escrow and
  policy for the cases that need only a copy.
