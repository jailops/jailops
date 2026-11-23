# Every night with a periodic script, and the next release when it comes

One administrator's way of keeping a JailOps host current, written out with the commands. The host is `host1`, a
pkgbase host on FreeBSD 15.1 with the image `freebsd-15-1`, the projects `shop` and `blog`, and in each of them the
jails `web` and `db`. A script in FreeBSD's [periodic(8)](https://man.freebsd.org/cgi/man.cgi?query=periodic&sektion=8)
does the nightly work; the move to 15.2, and later to 16.0, is done by hand with the same commands. The model, the
runbooks and the checks this page relies on are in [lifecycle.md](lifecycle.md); the mechanics of each JailOps
command in [upgrade.md](upgrade.md).

## 1. Updating every night with a periodic script

### 1.1 The script as it is often written

    #!/usr/local/bin/bash
    set -euo pipefail
    release="15.1-RELEASE"
    pkg update -f
    pkg upgrade -y
    jailops jail upgrade --all --project shop
    jailops jail upgrade --all --project blog
    jailops update apply --all --packages -y --project shop
    jailops update apply --all --packages -y --project blog

It does the work, and four things about it are wrong on a JailOps host:

- **`pkg upgrade -y` takes everything the enabled repositories offer**: the base system (`FreeBSD-*` from
  `FreeBSD-base`), the third-party packages, and the JailOps packages from the `jailops` repository, in one run.
  The second rule of [lifecycle.md](lifecycle.md) is one thing at a time, each with its way back, and JailOps itself
  is a step of its own with its changelog read first ([3.4](lifecycle.md#34-jailops-itself)). `-r REPO` names the
  repository a `pkg upgrade` takes packages from
  ([pkg-upgrade(8)](https://man.freebsd.org/cgi/man.cgi?query=pkg-upgrade&sektion=8)): one line per repository, and
  no line for `jailops`.
- **`set -e` stops the script at the first command that fails.** `jailops update apply` exits 1 when one jail's
  update failed, and a jail that is stopped counts as failed (its packages are updated inside it, so there is nothing
  to run); `jailops jail upgrade` exits 1 when one jail failed. One stopped jail in `shop` means `blog` gets nothing
  that night, and nothing says so.
- **The exit status says nothing to periodic.** A script under periodic says with its exit status whether its output
  is notable, and the output of a script that exits above 2 is never masked
  ([periodic(8)](https://man.freebsd.org/cgi/man.cgi?query=periodic&sektion=8)).
- **`release` is set and never read**, and `bash` is a package, not part of the base system the script updates. The
  script below is `/bin/sh`, and `release` is the guard that stops it after the host moved to another release
  ([section 2](#2-moving-host1-from-151-to-152-when-it-is-released)).

### 1.2 The script, corrected

`/usr/local/etc/periodic/daily/490-jail-upgrade`, executable. periodic runs the executable files of `/etc/periodic/daily`
and of the directories in `local_periodic`, whose default is `/usr/local/etc/periodic`
([periodic.conf(5)](https://man.freebsd.org/cgi/man.cgi?query=periodic.conf&sektion=5)); the names sort, so the number
places the script among the base system's own (`ls /etc/periodic/daily`). The default `/etc/crontab` runs `periodic
daily` at 03:01 ([crontab of releng/15.1](https://cgit.freebsd.org/src/tree/usr.sbin/cron/cron/crontab?h=releng/15.1)).

    #!/bin/sh
    # Nightly update of host1: the patch level of the base system, the third-party packages of the host, the next
    # generation of the image for new jails, and every jail of each project upgraded in place, base and packages.
    # JailOps itself is not in it: it is upgraded by hand, with its changelog read first (docs/lifecycle.md, 3.4).
    #
    # On:  sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=YES
    # Off: sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO
    #
    # Exit status for periodic(8): 0 nothing to report, 1 a reboot is due (notable), 3 a step failed (never masked).

    if [ -r /etc/defaults/periodic.conf ]; then
        . /etc/defaults/periodic.conf
        source_periodic_confs
    fi

    release="15.1-RELEASE"      # the release this script was written for; it stops when the host runs another
    image="freebsd-15-1"        # the image of that release
    projects="shop blog"        # the projects whose jails are upgraded

    case "${daily_jail_upgrade_enable:-NO}" in
    [Yy][Ee][Ss]) ;;
    *) exit 0 ;;
    esac

    echo ""
    echo "Nightly update of the host and the jails:"
    case "$(freebsd-version -u)" in
    "$release"*) ;;
    *) echo "the host runs $(freebsd-version -u), not $release: update release and image in this script first"; exit 3 ;;
    esac

    rc=0
    step() {                    # one step; a failure is printed and remembered, and the next step still runs
        echo "+ $*"
        "$@" || { rc=3; echo "! failed: $*"; }
    }

    step pkg update -q
    step pkg upgrade -y -r FreeBSD-base             # the patch level of the base: the kernel and userland packages
    step pkg upgrade -y -r FreeBSD-ports-kmods      # kernel modules from ports, which must match the kernel
    step pkg upgrade -y -r FreeBSD-ports            # the third-party packages of the host
    step jailops image upgrade "$image" --wait-timeout 1h          # the next generation, for the jails made from now on
    for p in $projects; do
        step jailops jail upgrade --all --project "$p" --wait-timeout 1h          # the base of each jail, in place
        step jailops update apply --all --packages -y --project "$p" --wait-timeout 1h   # the ports packages
    done

    if [ "$(freebsd-version -k)" != "$(freebsd-version -r)" ]; then
        echo "kernel $(freebsd-version -k) is installed and $(freebsd-version -r) runs: reboot the host"
        [ "$rc" -eq 0 ] && rc=1
    fi
    exit "$rc"

The settings, in `/etc/periodic.conf` (an override file; the defaults are in `/etc/defaults/periodic.conf`, which the
script reads the way the base system's scripts do, with `source_periodic_confs`,
[periodic.conf(5)](https://man.freebsd.org/cgi/man.cgi?query=periodic.conf&sektion=5)):

    daily_jail_upgrade_enable="YES"     # the switch of the script above
    daily_output="root"                 # the default: the output of every daily script is mailed to root
    daily_show_success="YES"            # the default: the output of a script that exits 0 is shown

### 1.3 Line by line

**`pkg update -q`** downloads the catalogues of the repositories when the remote copy is newer; `-f` forces the full
download every time and is not needed ([pkg-update(8)](https://man.freebsd.org/cgi/man.cgi?query=pkg-update&sektion=8)).
`pkg upgrade` updates the catalogues itself when it runs as root
([pkg-upgrade(8)](https://man.freebsd.org/cgi/man.cgi?query=pkg-upgrade&sektion=8)); the line is kept so that a
repository that cannot be reached fails on its own, before anything is changed.

**`pkg upgrade -y -r FreeBSD-base`** is the patch level of the host ([lifecycle.md 3.1](lifecycle.md#31-a-patch-level-of-the-host-151-p6-to-151-p7)),
as the Handbook writes it ([Updating FreeBSD with packages](https://docs.freebsd.org/en/books/handbook/cutting-edge/)).
On a pkgbase host the base system is packages, and this moves the `FreeBSD-*` packages, the kernel package among them,
to the current patch level of the release the `FreeBSD-base` repository points at: its URL ends in
`base_release_${VERSION_MINOR}`, so a 15.1 host gets 15.1's patch levels and never 15.2's
([lifecycle.md, the repositories](lifecycle.md#1-the-model)). The repository has to be enabled once
(`FreeBSD-base: { enabled: yes }` in `/usr/local/etc/pkg/repos/FreeBSD.conf`). `-y` answers pkg's question. After a
patch that touches the kernel, the installed kernel and the running one differ until a reboot:
`freebsd-version -k` prints the installed kernel, `-r` the running one, `-u` the userland
([freebsd-version(1)](https://man.freebsd.org/cgi/man.cgi?query=freebsd-version&sektion=1)); the last lines of the
script compare the first two and make the exit status 1 when they differ, so the mail says that a reboot is due. The
script never reboots: you do, at a time of your choosing (`shutdown -r now`), and a reboot starts the jails with
autostart ([`jailops boot`](lifecycle.md#3-the-runbooks)). A patch level of the host's base needs that reboot; a
jail update never does, because a jail has no kernel of its own. Between a userland update and the reboot, the
daemon runs with the libraries it loaded; `service jailopsd restart` makes it run on the new ones when the night
brought no kernel. `find /etc -name '*.pkgnew' -ls` after a night that updated the base: a file pkg could not merge
([lifecycle.md 5](lifecycle.md#5-what-to-watch-for)).

**`pkg upgrade -y -r FreeBSD-ports-kmods`** and **`-r FreeBSD-ports`** are the third-party kernel modules, which must
match the kernel, and the third-party packages of the host, each from its repository and nothing else. The JailOps
packages come from the repository `jailops` and are not named, so they stay where they are until you upgrade them
([lifecycle.md 3.4](lifecycle.md#34-jailops-itself)).

**`jailops image upgrade freebsd-15-1`** builds the next generation of the image from the release's current base
packages (minutes). It touches no jail: a new jail is a copy of the newest generation, so the jails made from tomorrow
on start at tonight's patch level. When the repository had nothing new, no generation is kept. `--wait-timeout 1h`:
the client waits 10 minutes by default and the operation keeps running when the client stops waiting, so the script
would start the next step while the build still runs.

**`jailops jail upgrade --all --project shop`** upgrades every FreeBSD jail of the project in place, one after the
other, while it runs ([lifecycle.md 3.5](lifecycle.md#35-a-freebsd-jail)). For each jail it asks pkg first, read-only:
with no newer base package of the jail's installed release the answer is `nothing to upgrade` and nothing happens.
Otherwise the root is snapshotted while the jail runs (`jo-pre-upgrade-<time>`, kept 14 days), the base is upgraded
from the host with `pkg -r`, the ports packages inside the jail with `pkg -j`, and one health check runs. The jail is
never stopped, started or restarted: the result lists the services that still run replaced files ("restart pending"),
and you restart them in a window of your choosing. A failure is reported, the jail runs on as pkg left it and the next
night completes it; nothing rolls back on its own. A stopped jail is not upgraded. A Linux jail is not part of a selection: it looks after its own packages. A
summary table ends the run (`upgraded`, `nothing to upgrade`, `failed`, `skipped`), and the exit status is 1 when a
jail failed. Each jail is an operation of its own (`jailops op list`) and a record in its update history (`jailops
update history web --project shop`). The token of the script is a global administrator's: only a global administrator
upgrades the base of a jail ([security-model.md](security-model.md#the-in-place-upgrade-of-a-jail)).

**`jailops update apply --all --packages -y --project shop`**, once per project, because the command acts in one
project and there is no default project. It takes every jail of the project and updates the packages inside it,
after the base step: the base first and the packages on top of it, the order the daemon itself uses in one update. A
jail whose base had nothing newer still gets its ports packages here. For a FreeBSD jail the host runs `pkg -j shop_web` with the
jail's own repositories and names the jail's own packages, so a `FreeBSD-*` package never comes in this way (a jail with
no ports packages fetches no catalogue: "no ports packages: nothing to update"). Linux jails look after their own
packages and are left out ([linux-jails.md](linux-jails.md#packages)).
What one jail gets:

| The jail | What happens |
|---|---|
| running, with updates | `pkg update`, a dry run that lists the packages, a snapshot of the jail (`update-...`), the upgrade, `ldconfig -m /usr/local/lib`, the health check (`update.healthcheck`), a record |
| running, nothing to update | a record `success` with the note `nothing to update`; no snapshot |
| stopped | a record `failure`: "the jail is not running; packages are updated inside it". It makes the command exit 1, which is why the script remembers the failure and goes on |
| Debian | a record `success` with the note that the jail keeps its own packages |
| the health check fails, or `pkg upgrade` fails | a record `failure` that names the snapshot and the command that restores it (`jailops jail rollback web --scope all --snapshot S`), and nothing is rolled back for you |
| any `update.reboot` | the jail is never stopped or restarted: the record names the processes that still run replaced files ("restart pending: nginx (pid 812)"), and you restart them in a window of your choosing. `update.reboot=auto` is accepted and does the same as `manual` |

`-y` answers the batch question of `--all` (a typed project name, otherwise); periodic has no terminal, so a script
without it fails at the question. The command exits 1 when any record is not `success`, and the hint names the
history command.

**`step` and the exit status.** Every step runs whatever happened before it: a stopped jail in `shop` does not cost
`blog` its night, and a night the repository could not be reached is a mail that says which lines failed. The exit
status is the one periodic reads ([periodic(8)](https://man.freebsd.org/cgi/man.cgi?query=periodic&sektion=8)): 0,
nothing notable, and the output is shown while `daily_show_success` is `YES` (the default); 1, notable, a reboot is
due; 3, a step failed, and the output of a script that exits above 2 is never masked, whatever the `show` settings
say.

### 1.4 Mail, logs and the records

periodic collects the output of every daily script and sends it to `daily_output`: `root` by default, so the mail
goes where the base system's daily report goes (the alias of `root` in `/etc/mail/aliases` says to whom); a value
that starts with `/` is a file instead (`daily_output="/var/log/daily.log"`, which `newsyslog` rotates), and
`daily_show_success`, `daily_show_info` and `daily_show_badconfig` mask the output of scripts that exit 0, 1 and 2
([periodic.conf(5)](https://man.freebsd.org/cgi/man.cgi?query=periodic.conf&sektion=5)). The script's `echo` lines
and the tables the commands print are that mail.

What JailOps records on its own, independent of the mail: every in-place upgrade and every package update as a record in the
jail's history (`jailops update history web --project shop`: result, change, the snapshot, and for a failure the
error and the way back), and the operations (`jailops op list`). The runs listed under **Automatic upgrades** in
`jailops image show freebsd-15-1` are the daemon's own nightly image builds
([1.6](#16-the-built-in-ways-and-which-to-choose)); an `image upgrade` run from a script is in the operations, not
there.

### 1.5 Before the first night: the checklist for an unattended run

1. **A health check on every jail**, because an upgrade without one passes when the jail starts, not when the service
   answers. The command runs inside the jail after every upgrade and every package update, and a non-zero exit writes
   the failure with the snapshot:

       jailops config set web --project shop update.healthcheck="/usr/local/bin/check"
       jailops config set db --project shop update.healthcheck="/usr/local/bin/check"

   `/usr/local/bin/check` is yours: a request to the service's port, `service nginx status`, a `psql` that answers.
   JailOps does not restart a jail after an update: the morning's records name what waits for a restart.
2. **A backup before the night**, not during it: a backup policy whose schedule runs before 03:00 keeps the night's
   backup ahead of the update ([backup-restore.md](backup-restore.md)):

       jailops backup policy create nightly --schedule "0 1 * * *" -l env=prod --target node1 --keep-daily 14
       jailops backup list --project shop                        # in the morning: last night's backups are there

   A host without a backup node has the snapshots the updates take; they are a way back for the jail, not a backup.
3. **Run the JailOps lines by hand with `--dry-run` first**, and then without:

       jailops image upgrade freebsd-15-1 --dry-run
       jailops jail upgrade --all --project shop --dry-run
       jailops jail upgrade --all --project blog --dry-run
       jailops update apply --all --packages --project shop --dry-run
       jailops update apply --all --packages --project blog --dry-run

4. **Run the script once by hand** (`sh /usr/local/etc/periodic/daily/490-jail-upgrade`), read what it prints, and
   switch it on: `sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=YES`.
5. **A jail that must not change at night:** name the others instead of `--all`, in both steps
   (`jailops jail upgrade web --project shop`, `jailops update apply web --packages -y --project shop`), or select
   them by a label (`-l nightly=yes`); upgrade that jail by hand when it suits it
   ([lifecycle.md 3.10](lifecycle.md#310-a-jail-that-stays-behind)).

**To skip a night:** `sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO`, and `YES` the day after. A file
without the executable bit is ignored by periodic too (`chmod -x` the script), but the variable is the switch the
mail shows.

### 1.6 The built-in ways, and which to choose

JailOps has two built-in ways, without a script of yours: one for the jails, one for the images.

- **The update policy of each jail** (`jailops config set web --project shop update.mode=managed
  update.schedule="0 3 * * *" update.os=true update.packages=true`, [lifecycle.md 3.8](lifecycle.md#38-packages-inside-a-jail)):
  the daemon checks and applies, per jail, at the jail's own cron expression, what the policy allows: with `update.os`
  the base is upgraded in place inside the jail's release ([lifecycle.md 3.5](lifecycle.md#35-a-freebsd-jail)), with
  `update.packages` its packages; `notify` checks and reports only, `manual` (the default) does nothing. It never stops,
  starts or restarts a jail: the record names the processes that still run replaced files ("restart pending"); restart
  them in a window of your choosing. A jail whose root holds what an attack on host-side pkg plants
  (`suspected_compromised`) is refused and reported. Each run that changes or fails something has a record in
  `jailops update history JAIL`. Whoever may change the jail sets its policy, but `update.os=true` with `update.mode=managed` is turned on by a
  global administrator only (a project administrator gets `forbidden`); nothing else upgrades a jail at night.
- **The nightly upgrade of an image** (`jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00`,
  [upgrade.md](upgrade.md#nightly-auto-upgrade)): the daemon builds the next generation itself at that time, in the
  host's time zone, for the jails made from then on; it touches no jail. A host that was off at the time runs it when
  it is back within four hours. Every run is recorded on the image (`jailops image show freebsd-15-1`: the next run,
  the latest runs with their results) with the event `image-upgrade-failed` for a failed one. It needs no token, no
  client and no mail.

| You want | Use |
|---|---|
| the host's base and packages updated at night too, and one mail in the morning | the periodic script: nothing else touches the host |
| the jails only, no script, no restart, a record per jail, each on its own schedule | the update policy of each jail |
| new jails on the newest patch level without a script | the nightly upgrade of the image |
| both the script and a jail's policy | not at the same hour: each jail is locked while it is upgraded, and the second upgrade is refused with `busy` |

The host part has no built-in way: JailOps never upgrades the host ([the third rule](lifecycle.md)).

### 1.7 In the morning

    freebsd-version -kru                                 # -k and -r differ: reboot the host
    jailops doctor                                       # every line ok or info
    jailops list                                         # every jail running that ran before, on the release you expect
    jailops update check --project shop                  # what is still newer; a jail left behind stands out
    jailops update history web --project shop            # newest first: success, or the error and the snapshot
    jailops op list                                      # the night's operations and their results

The mail itself: a `! failed:` line names the step. For a jail, the record says what happened and what to do
([lifecycle.md 6](lifecycle.md#6-when-something-fails)): `failure` (os), the jail runs as the upgrade left it, read the
error and the health check's output, fix, and the next night completes it; `failure` (packages), the packages stay
updated and the record names the snapshot that undoes them; a stopped jail, start it or leave it out. `jailops info web
--project shop` prints the conditions (`Ready`, `Healthy`) and the last update.

## 2. Moving host1 from 15.1 to 15.2 when it is released

The order is [lifecycle.md 3.2](lifecycle.md#32-a-minor-release-of-the-host-151-to-152) (the host) and
[3.5](lifecycle.md#35-a-freebsd-jail) (the jails); this is the same with the script around it. FreeBSD
publishes the **Upgrading Instructions** of 15.2 at `https://www.freebsd.org/releases/15.2R/upgrading/` when 15.2 is
announced; until then the page of 15.1, [releases/15.1R/upgrading](https://www.freebsd.org/releases/15.1R/upgrading/),
is the shape of it, and the values that change (the `OSVERSION`, the boot environment's name) are the new page's.

1. **Switch the script off** and let a running night finish:

       sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO
       jailops op list --active                          # nothing of the script's runs any more

2. **BEFORE** of [lifecycle.md](lifecycle.md#3-the-runbooks), with a fresh backup and the boot environment the
   Upgrading Instructions ask for:

       jailops doctor && jailops version && freebsd-version -kru
       jailops list                                      # written down
       jailops backup run nightly --project shop && jailops backup run nightly --project blog
       bectl create -r pre-15.2

3. **The host**, with the commands of the 15.2 Upgrading Instructions. For a pkgbase host, the 15.1 page writes
   them as: the current release fully patched first, then the base of the new release through `-oABI` and the page's
   `-oOSVERSION` (which selects `base_release_2`), then the kernel modules, then the `.pkgnew` files, the boot loader
   as the page describes for the host's firmware, and the reboot:

       pkg upgrade -r FreeBSD-base
       pkg -oABI=FreeBSD:15:$(uname -p) -oOSVERSION=<the value of the 15.2 page> upgrade -r FreeBSD-base
       pkg upgrade -r FreeBSD-ports-kmods
       find /etc /usr/local/etc -name '*.pkgnew' -ls
       shutdown -r now

   After the reboot: `freebsd-version -kru` prints 15.2 three times; `/etc/pf.conf` still has `anchor "jailops/*"`
   and `pfctl -nf /etc/pf.conf` accepts it; `jailops doctor`; `jailops list` shows every jail that ran before
   running, on 15.1, where they keep getting 15.1's patch levels. JailOps itself is not touched by this
   step: the packages are the same `FreeBSD:15` packages on 15.2 ([lifecycle.md 2](lifecycle.md#2-what-works-with-what)).
4. **The image of the new release**, built once, for the jails made from now on:

       jailops image create freebsd-15-2 --release 15.2-RELEASE --build

5. **One jail first, and the rest after it**, in place, while they run:

       jailops jail upgrade web --project shop --release 15.2-RELEASE --dry-run
       jailops jail upgrade web --project shop --release 15.2-RELEASE
       jailops jail upgrade --all --project shop --release 15.2-RELEASE --stop-on-error
       jailops jail upgrade --all --project blog --release 15.2-RELEASE --stop-on-error

   `--release` is never newer than the host's kernel, so this comes after the host. A jail you leave out stays on
   15.1 and keeps getting its patch levels. A Linux jail is not concerned by a FreeBSD release.
6. **The checks** of [lifecycle.md 4](lifecycle.md#4-always-test-after), per jail: the jail runs, `freebsd-version -j
   shop_web` prints 15.2, the health check passed (`jailops update history web --project shop`), the service answers.
7. **The script afterwards.** Two lines change, and nothing else: the `pkg` lines are the same, because
   `FreeBSD-base` now points at `base_release_2` through the host's own `OSVERSION`; the image line names the image
   through the variable, and `jail upgrade` keeps each jail on the release installed in it.

       release="15.2-RELEASE"
       image="freebsd-15-2"

   Run it by hand once (`sh /usr/local/etc/periodic/daily/490-jail-upgrade`), then switch it on:
   `sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=YES`. Until `release` is changed the script exits 3 with
   the line "the host runs 15.2-RELEASE, not 15.1-RELEASE", in the mail: that is the guard.
8. **The old image.** No jail holds it: each jail has its own root. Delete it when you make no more 15.1 jails:

       jailops image delete freebsd-15-1

9. **Going back**, strongest first: `jailops jail rollback web --project shop`, with the jail stopped, returns its
   root to the snapshot of the upgrade, within the 14 days, and loses what was written to the root since (its volumes
   stay); the host goes back with `bectl activate pre-15.2 && shutdown -r now` only after every jail that moved to
   15.2 was rolled back, because a 15.2 userland does not run on the 15.1 kernel. Put `release` and `image` back
   before you switch the script on again. Do not run `zpool upgrade` while the boot environment is the way back.

## 3. FreeBSD 16.0

16.0 is a major release, and the move is [lifecycle.md 3.3](lifecycle.md#33-a-major-release-of-the-host-15-to-16)
with the script around it. What differs from the move to 15.2, in the order it matters:

- **The host must be on pkgbase before 16.** Distribution sets and `freebsd-update` are planned for removal in
  FreeBSD 16 and continue for the lifetime of the 15 branch ([Handbook, Jail Upgrading](https://docs.freebsd.org/en/books/handbook/jails/));
  the Handbook converts a host with `pkgbasify` ([Updating FreeBSD with packages](https://docs.freebsd.org/en/books/handbook/cutting-edge/)).
  `host1` is a pkgbase host already; `jailops doctor` (`host.pkgbase`) says so.
- **A new package ABI, `FreeBSD:16`.** Every package comes from a `FreeBSD:16` build: the third-party packages of the
  host and of every jail, and the JailOps packages from `pkg.jailops.net`. The host moves when both repositories
  serve it; the release of JailOps that runs on 16 names it in its changelog.
- **The host's upgrade is the Handbook's major upgrade in a boot environment**, as [3.3](lifecycle.md#33-a-major-release-of-the-host-15-to-16)
  writes it with the Handbook's commands (`bectl create`, `bectl mount`, `env ABI=FreeBSD:16:amd64 pkg-static -c
  /mnt/upgrade upgrade -r FreeBSD-base`, `bectl activate -t`, the reboot, `pkg upgrade` after it; `pkg prime-origins`
  before, `pkg lock pkg` during), and the 16.0 Upgrading Instructions at `https://www.freebsd.org/releases/16.0R/upgrading/`
  are the procedure once 16.0 is announced.
- **The image is `freebsd-16-0`**, for the jails made from then on.
- **A jail moves in place** with `jailops jail upgrade NAME --release 16.0-RELEASE --no-packages`, then
  `pkg upgrade -f` inside the jail reinstalls its ports packages for `FreeBSD:16` ([lifecycle.md 3.5](lifecycle.md#35-a-freebsd-jail)).

The commands, in order, once 16.0 is announced and the `FreeBSD:16` packages are published:

    sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO
    # BEFORE (lifecycle.md), the backups, then the host as the Handbook's major upgrade (3.3) and the reboot
    pkg upgrade -r FreeBSD-ports-kmods && pkg upgrade -r FreeBSD-ports        # the third-party packages, for FreeBSD:16
    pkg upgrade -r jailops && service jailopsd restart && jailops doctor      # JailOps for FreeBSD:16, as 3.4 does it
    jailops image create freebsd-16-0 --release 16.0-RELEASE --build
    jailops jail upgrade web --project shop --release 16.0-RELEASE --no-packages --dry-run
    jailops jail upgrade web --project shop --release 16.0-RELEASE --no-packages
    jailops exec web --project shop -- pkg upgrade -f                 # the ports packages, reinstalled for FreeBSD:16
    jailops jail upgrade --all --project shop --release 16.0-RELEASE --no-packages --stop-on-error   # the rest, then pkg upgrade -f in each

Then the script: `release="16.0-RELEASE"`, `image="freebsd-16-0"`, and on. Going back is the boot environment of 15
while no jail has moved to 16, and `jailops jail rollback` per stopped jail within 14 days after that: the root goes
back to the snapshot of its upgrade, base and packages on `FreeBSD:15` together, and what the jail wrote to its root
since is discarded.

## 4. The command line, by question

Every line is from the built `jailops` of this repository; `shop` is the project and `web` the jail.

| The question | The command |
|---|---|
| Is there anything to update? | `jailops update check --project shop` (every jail of the project: newer base and ports packages; a stopped jail answers with an error for its packages); `jailops jail upgrade --all --project shop --dry-run` |
| What would an apply do? | `jailops jail upgrade web --project shop --dry-run`; `jailops update apply --all --packages --project shop --dry-run` |
| Apply it | `jailops jail upgrade --all --project shop`; `jailops update apply --all --packages -y --project shop` |
| One jail only | `jailops jail upgrade web --project shop`; `jailops update apply web --packages --project shop` |
| What happened last night? | `jailops update history web --project shop`; `jailops op list`; `jailops image show freebsd-15-1` for the daemon's own nightly image builds |
| Go back | `jailops jail rollback web --project shop` (a stopped jail: the root to the snapshot of its last upgrade, 14 days); `jailops jail rollback web --project shop --scope all --snapshot S --confirm-data-loss` (a package update, discarding what was written since) |
| Keep a jail where it is | leave it out of the script's `--all` (name the others, or select by label) and upgrade it by hand later; leave its update policy manual |
| Which release does a jail run? | `jailops list`; `jailops jail show web --project shop -o json` (`status.freebsd_version`) |
| Move everyone to a new release | the host first, then `jailops jail upgrade --all --project shop --release 15.2-RELEASE`; `jailops image create freebsd-15-2 --release 15.2-RELEASE --build` for new jails |
| Delete the old image | `jailops image delete freebsd-15-1`: no jail holds it |
| Let the daemon do the nights | per jail: `jailops config set web --project shop update.mode=managed update.schedule="0 3 * * *" update.os=true update.packages=true`; for new jails: `jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00` |
