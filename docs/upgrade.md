# Upgrading

Two different things: upgrading **JailOps itself**, and upgrading the **jails** it runs.

## JailOps

    pkg upgrade jailops jailops-client            # and jailops-backupd on a node
    service jailops_priv restart
    service jailopsd restart
    jailops doctor

Running jails are not stopped by restarting the daemon: they are kernel jails. Restarting `jailopsd` briefly stops the
reconcile loop and the API; restarting `jailops-priv` makes the daemon wait for the helper's socket (it retries for up to 30 seconds
at start). For a switched-IPv6 network the proxy stops answering while `jailopsd` is down, and announces its addresses
again when it is back.

What protects your data across an upgrade:

- The daemon copies its SQLite database (users, token hashes, sessions, audit log) with `VACUUM INTO` once a day into
  `<data_dir>/backups/`, keeping the newest seven, and a ZFS snapshot of the state dataset is taken before destructive operations.
- Everything else is derived: the jail files and PF anchors are rendered again from the stored objects on the next pass.
- Back up `<data_dir>/secrets.key` (the age identity that opens sealed secrets), `backup.key` and your TLS material with your
  own tools; they are not in a JailOps backup.

Read [CHANGELOG.md](../CHANGELOG.md) before upgrading. A configuration key that was removed is an error, because the files
reject unknown keys: the daemon names the key and does not start until you remove it (for example `experimental:` from an
early build). **UNVERIFIED:** upgrades between released versions of JailOps; there has been no release yet, and nothing
migrates from the pre-release layouts.

After a FreeBSD minor release upgrade of the host (15.1 to 15.2 and so on), run `jailops doctor` and, if you can, the PF test
(`scripts/ci-freebsd-pf.sh`) before relying on the firewall: pf changes between releases, and a rule that loads without an
error but does nothing is exactly what the read-back and that test exist to catch.

## Jails

A thin jail's root is a clone of an **image generation**: an immutable pkgbase userland. Upgrading moves the jail to a newer
generation and keeps its data. One command does it for the whole image:

    jailops image upgrade freebsd-15 --dry-run           # the build plan, and which jails would move
    jailops image upgrade freebsd-15                     # build the next generation, then move every thin jail to it

`image upgrade` builds the next generation from the newest one (its packages upgraded; minutes), then moves every thin jail
that uses the image, in every project, one at a time, exactly as `jail upgrade` does (below), writing the same update history
and events as `update apply`. It **stops at the first jail that fails or needs a human**: the jails after it are listed as not
attempted, so a bad generation is not rolled across the host; fix the jail (its history says what happened) and run the
command again. When the package set did not change, nothing is kept and no jail is touched; the command says so. A stopped
jail is moved too: it is started for the health check and stopped again. Thick jails, Linux jails (always thick), jails with an
update strategy other than `rebase` and jails pinned to a generation (`freebsd-15#g3`) are skipped and listed. `--project P`
moves one project's jails, `--no-jails` builds the generation only, and `jailops image generations freebsd-15` shows who is
where afterwards.

The same in two steps, one jail at a time:

    jailops image build freebsd-15                       # build the next generation (minutes)
    jailops jail upgrade web --dry-run --project shop    # what would change
    jailops jail upgrade web --project shop              # snapshot, new root, /etc merge, swap, health check
    jailops jail rollback web --project shop             # root only: back to the previous generation, data untouched

A generation is written `gN` wherever a command takes one (`image build --from g2`, `jail upgrade --generation g4`,
`image alias set freebsd-15 stable g2`, `image delete-generation freebsd-15 g1`); a bare number is accepted too.

The upgrade takes one atomic snapshot of all the jail's datasets (the rollback point), clones the new generation, merges
`/etc` three ways (the account databases `passwd`, `master.passwd` and `group` are merged by JailOps itself by user and group
name), swaps the roots with `zfs rename`, starts the jail and runs its health check. On failure it swaps back and starts the old
root. An `/etc` conflict a human must resolve leaves the jail on the old generation (`merge_conflict`).

Rollback has scopes, and the difference matters:

| Scope | Does | Data |
|---|---|---|
| `--root` (default) | the OS root back to the previous generation | untouched |
| `--data --snapshot S` | the data datasets back to a snapshot | **everything written since is discarded for good** |
| `--all` | both | as `--data` |

Before `--data` or `--all` the command shows how many bytes were written since the snapshot and asks you to type the jail name;
in a script pass `--confirm-data-loss` (`-y` does not apply to data loss).

Open: how `/var/db/pkg` behaves after a root swap (design section 16). A thick jail (a full copy) and a Linux jail are not
rebased; they are updated in place, and a debootstrap (Debian) generation cannot be upgraded: build a new one.

## Managed updates

Updates are **off for every jail** until you set its update mode:

    jailops set web updates.mode=notify updates.schedule="0 3 * * 0" updates.packages=true updates.os=true --project shop
    jailops update check --project shop         # every jail of the project: lists what could change, changes nothing
    jailops update apply --project shop web     # snapshot first, update, health check, record
    jailops update history web --project shop

Modes: `manual` (default), `notify` (check on the schedule and report), `managed` (check and apply on the schedule). `updates.packages`
updates packages inside the jail from the host, `updates.os` moves it to a newer image generation, `updates.reboot` (`manual` or `auto`)
says whether a restart an update needs is applied automatically. The daemon evaluates schedules every `updates.tick`.

- An apply takes a snapshot, updates, runs the jail's health check and records the result. A **failed OS update is swapped back**
  by the root-swap plan.
- A **failed package update is not rolled back by itself**: package files live in the jail's datasets, so a root rollback does not
  undo them. The way back is `jail rollback --all --snapshot <the recorded one>`, which discards what the jail wrote since. The record
  says so.
- Package checks and updates inside a jail run `pkg` (FreeBSD jails) or `apk` (Alpine). **Debian jails have no managed package
  updates or provisioning yet** (design section 16).
- UNVERIFIED: the package and generation updates have been tested with fakes, and the mechanism ran on a real jail only
  for the generation swap. `jailops-sync` rollouts stage upgrades across hosts (canary, batches, a health gate); see
  [fleet-sync.md](fleet-sync.md).
