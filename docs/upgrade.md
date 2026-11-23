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

Read [CHANGELOG.md](../CHANGELOG.md) before upgrading: it names what a release changes and what you do about it. From
0.9.2: convert or replace the jails of the old root layout first ([Jails with an unsupported root
layout](#jails-with-an-unsupported-root-layout)); `jail upgrade` is a global administrator's, so a token that runs it (a jailops-sync rollout, a script) needs that role; and a
jail with a volume at a directory of its system (`/usr/local`, `/var`, `/var/db`) starts as before, but `jailops doctor`
warns (`jail.volume_system_dir`) and its in-place upgrade is refused until the volume moves below that directory. A jail with
`update.mode=managed` and `update.os=true`, which on 0.9.2 changed nothing for a jail with a root of its own, now has
its base upgraded in place at its schedule: set `update.os=false` where you want to upgrade by hand (turning that pair
on is a global administrator's from this release: [Managed updates](#managed-updates)). A
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

A jail is an independent system with a root of its own, copied from a generation of an image; the image is only the seed.
`jailops jail upgrade NAME` upgrades a FreeBSD jail in place while it runs, the base from the host and the ports
packages through the running jail, and `--release 15.2-RELEASE` moves it to a new release.
[lifecycle.md](lifecycle.md#35-a-freebsd-jail) is the procedure; this section is the reference behind it.

### An image is one release

An image is one FreeBSD release and is named after it: `freebsd-15-1` is 15.1-RELEASE, `freebsd-15-2` is 15.2-RELEASE.
Its **generations** (g1, g2, ...) are the patch levels of that release: each `jailops image upgrade` builds the next one
with the release's current patches and packages. A new jail is copied from the newest generation (`--image freebsd-15-1`)
or from the one it names (`freebsd-15-1#g2`) and has no tie to it afterwards. A new release (15.2, 16.0) is **a new
image**, never a new generation of an old one.

**g1 is JailOps's generation counter; p6 is the FreeBSD patch level of its packages.** The number of a generation counts
the builds of the image on this host (g1, g2, ...) and says nothing about FreeBSD. When a generation is built, JailOps
records the FreeBSD version its base packages come from (the version of the `FreeBSD-runtime` package, `15.1p6`, shown
as `15.1-RELEASE-p6`; a snapshot of a branch keeps the package version as it is, `15.snap20261031190411`). `jailops image
show` and `jailops image generation list` have a column `FREEBSD`, and the console shows it on the Images page. A
generation whose version was not recorded shows `-`: JailOps does not guess it afterwards.

The image's pkg ABI comes from its release (`FreeBSD:15:amd64` for a 15.x release), and a build of a RELEASE stops
(`verify-release`) when the base packages it fetched are of another release. A build downloads the base packages from
`pkg.FreeBSD.org/${ABI}/base_release_<minor>` (a snapshot of a branch from `base_latest`) and takes minutes.

| You want | You run |
|---|---|
| the patches of the release your jails run | `jailops jail upgrade web` (or `--all`), or the jail's own update policy ([automating-updates.md](automating-updates.md#16-the-built-in-ways-and-which-to-choose)) |
| a new release for a jail | upgrade the host, then `jailops jail upgrade web --release 15.2-RELEASE` |
| new jails on the newest patch level | `jailops image upgrade freebsd-15-1` (or the nightly auto upgrade, below) |
| new jails on a new release | upgrade the host, then `jailops image create freebsd-15-2 --release 15.2-RELEASE --build` |

### Patch levels of an image: image upgrade

    jailops image upgrade freebsd-15-1 --dry-run         # the build plan
    jailops image upgrade freebsd-15-1                   # build the next generation

`image upgrade` builds the next generation from the newest one (its packages upgraded; minutes) for the jails made from
then on, and touches no jail. When the package set did not change, nothing is kept; the command says so. `jailops image
build freebsd-15-1` does the same build, and `--from-generation g2` builds from an earlier one.

A generation is written `gN` wherever a command takes one (`image build --from-generation g2`, `image generation delete
freebsd-15-1 g1`); a bare number is accepted too. No jail depends on a generation: `image generation delete` and the
retention of the image (`--keep-last`, `--keep-days`) remove generations whatever jails were copied from them.

### Nightly auto upgrade

Instead of running `image upgrade` by hand, let JailOps do it every night, per image:

    jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00
    jailops image set freebsd-15-1 --auto-upgrade=false
    jailops image show freebsd-15-1                           # the next run and the latest runs with their results

In the console it is the checkbox **Upgrade automatically every night** on the image's card of the Images page (with the
time), and the same checkbox in **Create image**. It is off until you tick it: JailOps upgrades nothing on its own
unless you asked. The card shows the next run and the latest runs; a failed run is shown as a notice on the card until a later
run succeeds. Only a global administrator changes it; everyone else sees it.

A run builds the image's next generation every night at the chosen time (the host's time zone; 03:00 unless you
choose another): the patch updates of the release (minutes), for new jails. It touches no jail. A jail takes its
patches by its own update policy: `update.mode=managed` with `update.os=true` upgrades its base in place on its
schedule, and `update.packages=true` its packages, while it runs
([automating-updates.md](automating-updates.md#16-the-built-in-ways-and-which-to-choose)). A host that was off at the
chosen time runs the image upgrade when it is back, if that is within four hours; later than that it waits for the
next night. Each run is recorded with its time and its result (ok, unchanged or failed, with what failed); the image
keeps the latest 30.

**From a cron script to auto upgrade.** If you ran `jailops image upgrade` from cron, remove that entry and turn auto
upgrade on for the image at the same hour. The daemon runs it itself, so it needs no token or client on the host; a
host that was off catches up within four hours; and each run's result is kept with the image (`jailops image show`,
the console) instead of in a mail from cron.

### A new release: move the jails

A jail moves to a new FreeBSD release in place, with its data, its volumes, its addresses and its configuration. The jails
you have stay on their release, and keep getting its patch levels, until you move them. The order:

1. **Upgrade the host first.** A jail runs on the host's kernel, so the host must run a release at least as new as the
   jail's; a jail is not moved to a release newer than the host's kernel (`version_skew`, "upgrade the host first"). The
   host's upgrade and the checks after it: [lifecycle.md](lifecycle.md#32-a-minor-release-of-the-host-151-to-152).
2. **Move one jail and look at it:**

       jailops jail upgrade web --release 15.2-RELEASE --dry-run     # pkg's report and every step
       jailops jail upgrade web --release 15.2-RELEASE

   The move is the in-place upgrade ([below](#what-an-upgrade-of-a-jail-does)) against the repository of the new
   release. Inside one major (15.1 to 15.2) the ports packages keep working and are upgraded in the same run. Across
   majors (15 to 16) they were built for the old ABI: move with `--no-packages` and run `pkg upgrade -f` inside the jail.
   Afterwards `jailops exec web -- find /etc /usr/local/etc -name '*.pkgnew'` lists the files pkg could not merge.
3. **Move the rest**, one at a time; a failure is reported and the run goes on (`--stop-on-error` stops it):

       jailops jail upgrade web db mail --release 15.2-RELEASE
       jailops jail upgrade -l env=prod --release 15.2-RELEASE

4. **The image of the new release**, for the jails you create from then on:

       jailops image create freebsd-15-2 --release 15.2-RELEASE --build
       jailops image set freebsd-15-2 --auto-upgrade --upgrade-at 03:00

   `--build` builds the first generation right after the image is made (without it, `jailops image build freebsd-15-2`
   does). The console proposes this itself: when the host runs a newer release than the newest FreeBSD image, the Images
   page and the Overview say so and offer **Create image** for it, filled in, and it builds the first generation. Delete
   the old image (`jailops image delete freebsd-15-1`) when you make no more jails from it; no jail depends on it.

**What the move requires.** A release that is not older than the one installed in the jail, and not newer than the host's
kernel. A Linux jail is replaced by a new one instead: create the new jail next to the old one on the new image, copy or
export the data from the old jail and import it in the new one with the application's own tools, check it, move the
addresses or ports over, and delete the old jail when you do not need it any more. Take a backup of the old jail first
([backup-restore.md](backup-restore.md)).

**Going back.** Stop the jail and run `jailops jail rollback web`: the root returns to the snapshot of the move, with
everything written to the root since. The snapshot is kept for 14 days.

### What an upgrade of a jail does

`jailops jail upgrade NAME` works on the running jail and never stops, starts or restarts it:

1. pkg's own dry run on a copy of the jail's package database, against the repository of the installed release (or of
   `--release`). Nothing newer: "Nothing to upgrade.", and nothing else happens.
2. The checks of the root for what an attack on host-side pkg plants; a finding refuses the jail as
   `suspected_compromised` ([thick-jail-upgrades.md](thick-jail-upgrades.md#a-jail-jailops-does-not-trust)).
3. The packages are fetched, and the root is snapshotted (`jo-pre-upgrade-<time>`, kept 14 days, for a manual rollback).
4. The base is upgraded from the host with `pkg -r` on the running root (`-C` an empty configuration, plugins off,
   `RUN_SCRIPTS=false`), every package is checked at its planned version, and what the scripts of the base packages do
   runs inside the jail (`pwd_mkdb`, `services_mkdb`, `cap_mkdb`, `certctl rehash`).
5. The ports packages are upgraded inside the jail (`pkg -j`), unless `--no-packages`. A jail without ports packages
   has no ports step: the ports catalogue is not fetched into its root.
6. One health check (`update.healthcheck`). The processes that still run replaced files are listed ("restart pending").

A failure is reported and left as it is: the jail runs on, and running the upgrade again completes it. Nothing rolls back
on its own, because a root rolled back under running services kills them. Each jail has its own lock: while one
operation changes its packages, no other one does. A stopped jail is not upgraded.

Rollback has scopes, and every one needs the jail stopped:

| Scope | Does | Data |
|---|---|---|
| `--scope root` (default) | the root back to the snapshot of the last upgrade (or `jail base add`) | everything written to the root since is discarded |
| `--scope all --snapshot S` | the root and the delegated dataset back to a snapshot of the jail | **everything written since is discarded for good** |
| `--scope data --snapshot S` | only the delegated dataset (`zfs_delegation`) back to a snapshot | as `--scope all`, for that dataset |

The command shows how many bytes were written since the snapshot and asks you to type the jail name; in a script pass
`--confirm-data-loss` (`-y` does not apply to data loss). A volume is never rolled back with the jail. A Linux jail is
updated by its package manager (`jailops update apply`); a debootstrap (Debian) generation cannot be upgraded: build a
new one. The checkpoint around an upgrade and a Linux jail: [thick-jail-upgrades.md](thick-jail-upgrades.md).

### Jails with an unsupported root layout

JailOps 0.9.2 could make a jail whose root was a clone of an image generation with separate datasets for `/etc`,
`/usr/local`, `/var` and `/home`. This release does not support that root layout. **Before you upgrade a host to this
release**, convert each such jail with JailOps 0.9.2:

    jailops jail info web --project shop                   # on 0.9.2: the kind line names the layout
    jailops jail convert web --project shop --layout thick --dry-run
    jailops jail convert web --project shop --layout thick

The conversion stops a running jail and starts it again. Or create the jail again and copy its data over. Then, still
on 0.9.2, run `jailops doctor` and `jailops op list`: upgrade only when doctor is clean and no operation is running or
was interrupted (a conversion, upgrade or rollback of 0.9.2 cut in the middle is repaired by 0.9.2 only; this release
leaves it as it is). After the upgrade a jail of that layout that is left:

- keeps its data: the daemon starts normally and never touches or destroys its datasets;
- does not start; one that still runs keeps running until you stop it, and it can be stopped and deleted;
- refuses every other operation (start, restart, upgrade, rollback, snapshot, copy, export, `exec`, `console`, `jail
  base`) with an error that names the fix; its backup fails, as a failed run with the reason (`backup-failed`, `jailops
  doctor` for a scheduled one): its data gets no new backup on this release; its older backups and export files are
  refused by restore and import;
- carries the condition `UnsupportedLayout` (reason `EarlierRelease`), and `jailops doctor` fails the check
  `jail.unsupported_layout` with its name.

After the upgrade the fix is a new jail on this release: create it, move the data over, move the addresses or ports,
and delete the old jail. Its datasets stay on the pool as they were: `zfs list -r -o name,mountpoint,jailops:path
POOL/jailops/projects/shop/jails/web` lists them (`jailops:path` is the directory of the jail each one held); while it
still runs they are mounted, otherwise `zfs mount` one to read it on the host, and copy what you need into the new
jail or a volume. Or install the earlier release again, convert the jail there and upgrade again.

## Managed updates

Updates are **off for every jail** until you set its update mode:

    jailops config set web update.mode=notify update.schedule="0 3 * * 0" update.packages=true update.os=true --project shop
    jailops update check --project shop         # every jail of the project: lists what could change, changes nothing
    jailops update apply --project shop web     # snapshot first, update, health check, record
    jailops update history web --project shop

Modes: `manual` (default), `notify` (check on the schedule and report), `managed` (check and apply on the schedule).
`update.packages` updates the packages inside the jail from the host. `update.os` upgrades the base of a FreeBSD jail in
place inside its release: on the jail's schedule (mode `managed`), or on demand when a global administrator runs
`update apply`. Only a global administrator turns on `update.os=true` with mode `managed` (either key, at create, update,
patch or `config set`): a project administrator gets `forbidden`, may change the other update keys and may turn the
pair off ([security-model.md](security-model.md#the-in-place-upgrade-of-a-jail)); `update check` reports the newer base packages of a running jail. Nothing else upgrades a jail on a
schedule: the nightly upgrade of an image only builds its next generation. `update.reboot` takes `manual` or `auto`, and both only report:
JailOps never stops or restarts a running jail for an update, and the record names what waits for a restart.
`update.healthcheck` is the command every update runs in the jail afterwards. The daemon evaluates schedules every
`updates.tick`.

- An apply does the base first (the in-place upgrade, [above](#what-an-upgrade-of-a-jail-does)), then the packages, takes
  a snapshot before each part, runs the jail's health check and records the result (`success`, `failure`,
  `preflight_failed`; what each one means: [lifecycle.md](lifecycle.md#6-when-something-fails)).
- A **failed update is not rolled back by itself**. The way back is to stop the jail and run `jail rollback --scope all
  --snapshot <the recorded one>`, which discards what the jail wrote to its root since. The record says so.
- A **successful package update never restarts the jail**: the record names the processes that still run replaced files
  ("restart pending: nginx (pid 812)"); restart them, or the jail, when it suits you.
- The packages of a FreeBSD jail are checked and upgraded by the host's `pkg -j PROJECT_JAIL`, which enters the jail and
  reads the repositories configured in it; only the packages that are not part of the base (`FreeBSD-*`) are named: the
  base is upgraded from the host. After the upgrade `ldconfig -m /usr/local/lib` runs in the jail. Linux jails look after
  their own packages: JailOps does not update them ([linux-jails.md](linux-jails.md#packages)).
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
