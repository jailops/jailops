# Upgrading JailOps and the jails: the mechanisms

The runbooks are in [lifecycle.md](lifecycle.md): the order of the steps for FreeBSD on the host, for JailOps and for
the jails, the checks after every step, and the way back. This page is the reference behind them: what each JailOps
command does and what it keeps.

## The host's FreeBSD

The host is upgraded with FreeBSD's own tools, before JailOps and before any jail, each step in a boot environment of its
own: [lifecycle.md, runbooks 3.1 to 3.3](lifecycle.md#3-the-runbooks). The line `anchor "jailops/*"` of `/etc/pf.conf`,
the `.pkgnew` files of `/etc` and `zpool upgrade` are there too. What an upgrade of JailOps keeps, and how that is
tested: [upgrade-guarantee.md](upgrade-guarantee.md).

## JailOps

Upgrade JailOps in a step of its own, after the host's FreeBSD has been upgraded and has rebooted.

    bectl create pre-jailops-upgrade                 # a boot environment of the root file system
    service jailopsd stop
    cp /var/db/jailops/jailops.db /var/db/jailops/jailops.db.pre-upgrade    # the daemon checkpoints it when it stops
    pkg upgrade -r jailops
    service jailopsd restart
    jailops doctor
    jailops list

`pkg upgrade -r jailops` takes every package of the JailOps repository: `jailops`, `jailops-client`, `jailops-apps` and, on a
backup node, `jailops-backupd`. A bare `pkg upgrade` on a pkgbase host mixes base and JailOps updates, and naming only
`jailops` and `jailops-client` leaves `jailops-apps` and `jailops-backupd` behind.

Both binaries are new and each service runs the old one until it restarts. The two are one service: the helper is the
part that runs as root. `service jailopsd stop` stops the helper after the daemon and `service jailopsd start` starts it
first, so `service jailopsd restart` replaces both. `service jailops_priv restart` alone restarts only the helper: the
daemon, which keeps running, reconnects once it is back, and the requests it makes meanwhile fail. `service jailopsd start`
also warns while the helper still runs a replaced binary, and a daemon that finds the helper to be an older release than
itself refuses to start with `version_skew` until the helper restarts.
Running jails are not stopped by restarting either service: they are kernel jails. Restarting `jailopsd` briefly stops the
reconcile loop and the API. For a switched-IPv6 network the proxy stops answering while `jailopsd` is down, and announces its addresses
again when it is back.

What protects your data across an upgrade:

- The daemon copies its SQLite database (users, token hashes, sessions, audit log) with `VACUUM INTO` once a day into
  `<data_dir>/backups/`, keeping the newest seven, writes `jailops.db.before-schema-N` before a schema migration, and a
  ZFS snapshot of the state dataset is taken before destructive operations.
- Everything else is derived: the jail files and PF anchors are rendered again from the stored objects on the next pass.
- Back up `<data_dir>/secrets.key` (the age identity that opens sealed secrets), `backup.key` and your TLS material with your
  own tools; they are not in a JailOps backup.

Read [CHANGELOG.md](../CHANGELOG.md) before upgrading: it names what a release changes and what you do about it. A
configuration key that was removed is an error, because the files reject unknown keys: the daemon names the key and does not
start until you remove it.

### Going back one version of JailOps

The database (`/var/db/jailops/jailops.db`) has **forward-only** schema migrations: none has a way back. A daemon older than
the database refuses to start with `version_skew` and leaves the database as it is. Going back is therefore a matter of
putting the old database back with the old binaries.

- **With the boot environment:** `bectl activate pre-jailops-upgrade`, then reboot. Binaries, configuration and the database
  go back together, as long as `/var/db` is part of the root file system (the installer's default layout; check with
  `zfs list -o name,mountpoint`). The jails with autostart start. The **state dataset** (`<pool>/jailops/state`, mounted at
  `/var/db/jailops/state`) and the jails are datasets of their own and are not rolled back: objects created after the
  upgrade stay, and an older daemon reads them as long as none carries a newer `apiVersion` (`version_skew` otherwise).
- **Without it:** install the previous package files with `pkg add -f` from `/var/cache/pkg` (pkg keeps what it downloaded
  until `pkg clean`), `jailops-client` first and then `jailops`, put the copy of `jailops.db` back, `pkg lock jailops
  jailops-client` so the next `pkg upgrade` leaves them, and `service jailopsd restart`. A downgrade without the database
  copy leaves a daemon that refuses to start (`version_skew`). The daily copies in `/var/db/jailops/backups/` are a last
  resort: what changed since the copy (users, tokens, audit entries) is lost.

## Jails

### An image is one release

An image is one FreeBSD release and is named after it: `freebsd-15-1` is 15.1-RELEASE, `freebsd-15-2` is 15.2-RELEASE.
Its **generations** (g1, g2, ...) are the patch levels of that release: each `jailops image upgrade` builds the next one
with the release's current patches and packages. A thin jail's root is a clone of one generation, an immutable pkgbase
userland; a jail made with `--image freebsd-15-1` follows the newest generation of the image. A new release (15.2, 16.0)
is **a new image**, never a new generation of an old one, and a jail moves to it with `jailops jail upgrade --image`
([a new release: move the jails](#a-new-release-move-the-jails)).

**g1 is JailOps's generation counter; p6 is the FreeBSD patch level of its packages.** The number of a generation counts
the builds of the image on this host (g1, g2, ...) and says nothing about FreeBSD. When a generation is built, JailOps
records the FreeBSD version its base packages come from (the version of the `FreeBSD-runtime` package, `15.1p6`, shown
as `15.1-RELEASE-p6`; a snapshot of a branch keeps the package version as it is, `15.snap20261031190411`). `jailops image
show` and `jailops image generation list` have a column `FREEBSD`, `jailops list` writes `freebsd-15-1 (g1, p6)`, and the
console shows it on the Images page and the jail's page. A generation whose version was not recorded shows `-`: JailOps
does not guess it afterwards.

The image's pkg ABI comes from its release (`FreeBSD:15:amd64` for a 15.x release), and a build of a RELEASE stops
(`verify-release`) when the base packages it fetched are of another release. A build downloads the base packages from
`pkg.FreeBSD.org/${ABI}/base_release_<minor>` (a snapshot of a branch from `base_latest`) and takes minutes.

| You want | You run |
|---|---|
| the patches of the release your jails run | `jailops image upgrade freebsd-15-1` (or the nightly auto upgrade, below) |
| a new release | upgrade the host, then `jailops image create freebsd-15-2 --release 15.2-RELEASE --build`, then `jailops jail upgrade web --image freebsd-15-2` (or `jailops image upgrade freebsd-15-1 --to freebsd-15-2` for every jail of the image), or create new jails on it |

### Patch levels: image upgrade

Upgrading moves a jail to a newer generation of its image and keeps its data. One command does it for the whole image:

    jailops image upgrade freebsd-15-1 --dry-run         # the build plan, and which jails would move
    jailops image upgrade freebsd-15-1                   # build the next generation, then move every thin jail to it

`image upgrade` builds the next generation from the newest one (its packages upgraded; minutes), then moves every thin jail
that uses the image, in every project, one at a time, exactly as `jail upgrade` does (below), writing the same update history
and events as `update apply`. It **stops at the first jail that fails or needs a human**: the jails after it are listed as not
attempted, so a bad generation is not rolled across the host; fix the jail (its history says what happened) and run the
command again. When the package set did not change, nothing is kept and no jail is touched; the command says so. A stopped
jail is moved too: it is started for the health check and stopped again. Thick jails, Linux jails (always thick), jails
pinned to a generation (`freebsd-15-1#g3`) and jails whose encrypted volumes are locked are skipped and listed. `--project P`
moves one project's jails, `--no-jails` builds the generation only, and `jailops image generation list freebsd-15-1` shows who is
where afterwards.

The same in two steps, one jail at a time:

    jailops image build freebsd-15-1                     # build the next generation (minutes)
    jailops jail upgrade web --dry-run --project shop    # what would change
    jailops jail upgrade web --project shop              # snapshot, new root, /etc merge, swap, health check
    jailops jail rollback web --project shop             # root only: back to the previous generation, data untouched

A generation is written `gN` wherever a command takes one (`image build --from-generation g2`, `jail upgrade --generation g4`,
`image generation delete freebsd-15-1 g1`); a bare number is accepted too. The plain image name follows the newest
generation; `freebsd-15-1#g2` pins one (`jailops config set web image=freebsd-15-1#g2`).

### Nightly auto upgrade

Instead of running `image upgrade` by hand, let JailOps do it every night, per image:

    jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00            # --upgrade-packages=false leaves the packages inside the jails alone
    jailops image set freebsd-15-1 --auto-upgrade=false
    jailops image show freebsd-15-1                           # the next run and the latest runs with their results

In the console it is the checkbox **Upgrade automatically every night** on the image's card of the Images page (with the
time and **Also upgrade the packages inside the jails**), and the same checkbox in **Create image**. It is off until you tick it: JailOps upgrades nothing on its own
unless you asked. The card shows the next run and the latest runs; a failed run is shown as a notice on the card until a later
run succeeds. Only a global administrator changes it; everyone else sees it.

What a run does, every night at the chosen time (the host's time zone; 03:00 unless you choose another):

1. It builds the image's next generation: the patch updates of the release (minutes). When nothing changed, the run is
   recorded as unchanged and nothing else happens.
2. It moves the thin jails of the image to the new generation one at a time, exactly as `image upgrade` does: snapshot,
   new root, `/etc` merge, health check, and the automatic rollback of a jail whose health check fails. **It stops at the
   first jail that fails**; the jails after it stay where they are until the next run or until you run `image upgrade`.
3. With packages on (the default), it upgrades the packages inside the running jails of the image.

It leaves alone thick jails, Linux jails and jails pinned to a
generation, as `image upgrade` does. A host that was off at the chosen time runs the upgrade when it is back, if that is
within four hours; later than that it waits for the next night. Each run is recorded with its time, its result (ok,
unchanged or failed, with what failed) and the number of jails moved, upgraded and failed; the image keeps the latest 30.

**From a cron script to auto upgrade.** If you ran `jailops image upgrade` (and a package upgrade per jail) from cron, remove
that entry and turn auto upgrade on for the image at the same hour. What changes: the daemon runs it itself, so it needs no
token or client on the host; a host that was off catches up within four hours; the packages are upgraded in the same run;
and each run's result is kept with the image (`jailops image show`, the console) instead of in a mail from cron.

### A new release: move the jails

A new FreeBSD release is a new image, and a jail moves to it the way it moves to a new generation: with its data, its
volumes, its addresses and its configuration, in one operation with a snapshot to go back to. The jails you have stay on
the image of their release, and keep getting its patch levels, until you move them. The order:

1. **Upgrade the host first.** A jail runs on the host's kernel, so the host must run a release at least as new as the
   image's; a jail is not moved to a generation of a newer release than the host's kernel (`version_skew`, "upgrade the
   host first"). The host's upgrade and the checks after it: [lifecycle.md](lifecycle.md#32-a-minor-release-of-the-host-151-to-152).
2. **Create and build the image of the new release:**

       jailops image create freebsd-15-2 --release 15.2-RELEASE --build
       jailops image set freebsd-15-2 --auto-upgrade --upgrade-at 03:00

   `--build` builds the first generation right after the image is made (without it, `jailops image build freebsd-15-2`
   does). The console proposes this itself: when the host runs a newer release than the newest FreeBSD image, the Images
   page and the Overview say so and offer **Create image** for it, filled in, and it builds the first generation.
3. **Move one jail and look at it:**

       jailops jail upgrade web --image freebsd-15-2 --dry-run     # releases, what is kept, the steps
       jailops jail upgrade web --image freebsd-15-2

   The move is the steps of an upgrade to a new generation (below): one atomic snapshot, a clone of the newest generation
   of the new image, the three-way `/etc` merge, the swap, the start and the health check, and the swap back when any of it
   fails. The jail follows the new image from then on: `spec.image` names it, `jailops list` shows it, the nightly auto
   upgrade of the new image moves the jail through its patch levels, and the old image does not list the jail any more. The move is
   written to the update history (`jailops update history web`) as an OS update from `freebsd-15-1#g2` to `freebsd-15-2#g1`.
4. **Move the rest:**

       jailops jail upgrade web db mail --image freebsd-15-2
       jailops jail upgrade -l env=prod --image freebsd-15-2
       jailops image upgrade freebsd-15-1 --to freebsd-15-2 --dry-run
       jailops image upgrade freebsd-15-1 --to freebsd-15-2

   The jails move one at a time and the first failure stops the rest. `image upgrade --to` moves the thin jails of the
   old image of every project (`--project` narrows it) and lists the ones it skips: jails pinned to a generation, thick
   jails and Linux jails.

**What the move requires.** The new image is of the same OS and architecture and of a release that is not older: a jail
does not go back to an older release (make a new jail from the old image for that). It is a thin FreeBSD jail: a thick
jail is updated through managed updates and a Linux jail is replaced by a new one. The image must be one the project may
use.

**What may need a manual look.** The `/etc` merge is the one of any upgrade: the files you changed stay, the files the new
release changed are taken, and a file both changed that cannot be merged stops the move with the files named
(`merge_conflict`); the jail stays on the old image, you resolve the file in the jail, and run the move again. The packages
the jail has installed stay as they are. Inside one major release (15.1 to 15.2) they keep working; a move to a new
*major* release (15 to 16) is answered with a warning, because they were built for the old ABI: reinstall them with
`jailops update apply web --packages` and `pkg upgrade -f` inside the jail. A service that reads a file the new release
does not ship is yours to look at; `jailops jail diff web` lists what differs from the image since the move.

**Going back.** `jailops jail rollback web` returns the jail to the previous root, **and to the previous image**: the root,
`spec.image` and the history go back together. It never touches data, so what the jail wrote after the move stays. The old
root is kept for 14 days after an upgrade (`jailops jail info web` shows the day), so go back soon: after that housekeeping removes it and the root cannot come back. Restoring a snapshot (`--scope all`) that was taken on the
old image is refused while the jail is on the new one: roll the root back first.

**A jail that does not move.** A thick jail is upgraded inside the jail ([thick-jail-upgrades.md](thick-jail-upgrades.md)). A thick jail, a Linux jail and a jail on another OS can be replaced instead: create the new jail next
to the old one on the new image, copy or export the data from the old jail and import it in the new one with the
application's own tools, check it, move the addresses or ports over, and delete the old jail when you do not need it any more.
Take a backup of the old jail first ([backup-restore.md](backup-restore.md)). Delete the old image
(`jailops image delete freebsd-15-1`) once no jail uses it.

### What an upgrade of a jail does

The upgrade (to a new generation of the image, or to another image) takes one atomic snapshot of all the jail's datasets (the rollback point), clones the new generation, merges
`/etc` three ways with `etcupdate` (the account databases `passwd`, `master.passwd` and `group` are merged by JailOps itself by user and group
name), swaps the roots with `zfs rename`, starts the jail and runs its health check (`update.healthcheck`). On failure it swaps back and starts the old
root. An `/etc` conflict a human must resolve leaves the jail on the old generation (`merge_conflict`). The preflight refuses
a move while a `root.new` or `root.stale` of an interrupted operation exists (`busy`), while a snapshot of the jail's root has
clones, while a replication of the jail runs, and when the generation's release is newer than the host's kernel (`version_skew`).

What a failed upgrade puts back depends on whether the jail had run on the new generation. Before the start nothing the
jail wrote exists, so `/etc` and `/var/db` go back to the moment before the merge. After the start `/etc` still goes back
(it was merged for the new generation and the old root needs its own), but `/var/db` keeps what the jail wrote while it ran:
the databases of the packages are not rolled back. Changes made to files in `/etc` by the jail in that window are lost.

Every step is journalled. A daemon that starts while a plan was running marks the operation `interrupted` and runs the undo
steps of the plan, so the jail can start on its old generation; the outcome (`rolled_back`, `completed`, `dropped`,
`needs_attention`) is in the log and in `jailops doctor` (`startup.recovery`, `operations.journal`).

Rollback has scopes, and the difference matters:

| Scope | Does | Data |
|---|---|---|
| `--scope root` (default) | the OS root back to the previous generation | untouched |
| `--scope data --snapshot S` | the data datasets back to a snapshot | **everything written since is discarded for good** |
| `--scope all` | both | as `--scope data` |

Before `--scope data` or `--scope all` the command shows how many bytes were written since the snapshot and asks you to type the jail name;
in a script pass `--confirm-data-loss` (`-y` does not apply to data loss).

A thick jail (a full copy) and a Linux jail are not rebased; they are updated in place, and a debootstrap (Debian)
generation cannot be upgraded: build a new one. How to do it by hand, with a checkpoint to go back to, is in
[Upgrading a thick jail by hand](thick-jail-upgrades.md).

### The package records of the base

The root of a thin jail is a clone of a generation and `/var/db` is the jail's own dataset, mounted over the root's. After
a swap (an upgrade, `jail upgrade --image`, a rollback) the files of the base come from the new generation, while the jail's
package database still holds the base records of the old one: `pkg which /usr/bin/ftp` finds nothing, `pkg info
FreeBSD-runtime` shows the old patch level and `pkg check -s` checks the old files.

Every start of a thin jail brings the records of the base set (the `FreeBSD-*` packages) in step with the generation of its
root. The generation's database is the truth for that set: a record whose version differs is replaced and a missing one is
added. The records of every other package (what `pkg install` or an app put there) are never touched, and no file is written
and no script runs: only the database changes. A start that finds the records in step reads two lists and does nothing else.

If the pass fails (the disk is full, a manifest cannot be read), the jail still starts. It then has the condition
`BasePackages`, `jailops doctor` warns (`jails.base-packages`), and this repairs it once the jail is stopped (a restart makes
the same pass):

    jailops jail repair-packages web

The console has the same button on the jail's page.

## Managed updates

Updates are **off for every jail** until you set its update mode:

    jailops config set web update.mode=notify update.schedule="0 3 * * 0" update.packages=true update.os=true --project shop
    jailops update check --project shop         # every jail of the project: lists what could change, changes nothing
    jailops update apply --project shop web     # snapshot first, update, health check, record
    jailops update history web --project shop

Modes: `manual` (default), `notify` (check on the schedule and report), `managed` (check and apply on the schedule). `update.packages`
updates packages inside the jail from the host, `update.os` moves it to a newer image generation, `update.reboot` (`manual` or `auto`)
says whether a restart an update needs is applied automatically, and `update.healthcheck` is the command every update runs in
the jail afterwards. The daemon evaluates schedules every `updates.tick`.

- An apply does the OS update first (a new root, then the packages on top of it), takes a snapshot before each part, runs
  the jail's health check and records the result (`success`, `failure`, `preflight_failed`, `rolled_back`,
  `recovery_needed`; what each one means: [lifecycle.md](lifecycle.md#6-when-something-fails)). A **failed OS update is swapped back**
  by the root-swap plan.
- A **failed package update is not rolled back by itself**: package files live in the jail's datasets, so a root rollback does not
  undo them. The way back is `jail rollback --scope all --snapshot <the recorded one>`, which discards what the jail wrote since. The record
  says so. A failed automatic restart (`update.reboot=auto`) is recorded as a failure too: the packages are updated and the
  jail is stopped.
- The packages of a FreeBSD jail are checked and upgraded by the host's `pkg -j PROJECT_JAIL`, which enters the jail and
  reads the repositories configured in it; only the packages that are not part of the base (`FreeBSD-*`) are named, so the
  base moves with the generation only. After the upgrade `ldconfig -m /usr/local/lib` runs in the jail. An Alpine jail runs
  `apk` itself. **A Debian jail keeps its own packages**: JailOps never runs `apt` in it.
- `jailops-sync` rollouts stage upgrades across hosts (canary, batches, a health gate); see [fleet-sync.md](fleet-sync.md).

## Leftovers of the bridge layout

A host that still has a bridge and a gateway pair (`NETNAMEga`, `NETNAMEgb`) for a network carries the layout of an earlier
release; a network is routed and has no interface of its own. `jailops doctor` says `network.leftovers` and names them, and
`jailops doctor --fix` lists the commands.
The jails that were started before the upgrade are still members of the bridge until they are restarted, so restart them first
(`jailops restart JAIL ...`: they are down for a few seconds and come back with the routed link; the first start takes the
gateway address away from the old gateway pair, which is why the jails still on the bridge lose it). Then remove what is left:

    ifconfig -g bridge                # the bridges of the host
    ifconfig NETNAME destroy          # the bridge; ifconfig NETNAME | grep member must show no jail link any more
    ifconfig NETNAMEga destroy        # destroying one end of the pair removes the other
    kldunload if_bridge               # and remove if_bridge_load from /boot/loader.conf

A stop of such a jail works too: what is not there is not an error, and what could not be
removed is written as a warning to the console log of the jail (`/var/log/jailops/jail_PROJECT_NAME_console.log`) and to the
audit log, and never fails the stop.

## Host identity

The host UUID is made once at the first start and kept in the database. A start that cannot read it stops with a message
instead of making a new one, because backups and the hub know the host by it. To adopt a machine as a new host on purpose,
remove the `host_uuid` row from the `meta` table; to keep the identity, restore the database from a copy.
