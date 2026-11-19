# Every night with a periodic script, and the next release when it comes

One administrator's way of keeping a JailOps host current, written out with the commands. The host is `host1`, a
pkgbase host on FreeBSD 15.1 with the image `freebsd-15-1`, the projects `shop` and `blog`, and in each of them the
thin jails `web` and `db`. A script in FreeBSD's [periodic(8)](https://man.freebsd.org/cgi/man.cgi?query=periodic&sektion=8)
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
    jailops image upgrade freebsd-15-1
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
  to run). One stopped jail in `shop` means `blog` gets nothing that night, and nothing says so.
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
    # generation of the image with every thin jail moved to it, and the packages inside the jails of each project.
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
    projects="shop blog"        # the projects whose jails get their packages updated

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
    step jailops image upgrade "$image" --wait-timeout 2h          # the next generation, then every thin jail of every project
    for p in $projects; do
        step jailops update apply --all --packages -y --project "$p" --wait-timeout 1h
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
packages (minutes) and moves every thin jail that uses the image, of every project, to it, one at a time, each move
with a snapshot, the `/etc` merge, the swap, the start and the jail's health check, and the swap back when the start
or the check fails. The first jail that fails stops the run: the jails after it are listed as `not_attempted`, the
jail that failed is back on its old root, and the exit status is 1. A later run moves the jails that were left
behind, on a night when nothing new was built too. When the repository had nothing new, no generation is kept and
no jail is touched. A stopped jail is moved like a running one: started for the health check, then stopped again.
Thick jails, Linux jails, jails pinned to a generation (`freebsd-15-1#g3`) and jails whose encrypted volumes are
locked are skipped and listed with the reason. The run is one operation (`jailops op list`), and each move is a
record in the jail's update history (`jailops update history web --project shop`). `--wait-timeout 2h`: the client
waits 10 minutes by default and the operation keeps running when the client stops waiting, so the script would start
the package step while the image step still holds the jails, and be refused with `busy`; a build plus the moves of a
host with many jails takes longer than 10 minutes.

**`jailops update apply --all --packages -y --project shop`**, once per project, because the command acts in one
project and there is no default project. It takes every jail of the project and updates the packages inside it, on
the root the jail has now, which is the one the image step just gave it: the base first and the packages on top of
it, the order the daemon itself uses in one update. For a FreeBSD jail the host runs `pkg -j shop_web` with the
jail's own repositories and names the jail's own packages, so a `FreeBSD-*` package never comes in this way; an
Alpine jail runs `apk upgrade`; a Debian jail keeps its own packages, and the record says that nothing was run.
What one jail gets:

| The jail | What happens |
|---|---|
| running, with updates | `pkg update`, a dry run that lists the packages, a snapshot of the jail (`update-...`), the upgrade, `ldconfig -m /usr/local/lib`, the health check (`update.healthcheck`), a record |
| running, nothing to update | a record `success` with the note `nothing to update`; no snapshot |
| stopped | a record `failure`: "the jail is not running; packages are updated inside it". It makes the command exit 1, which is why the script remembers the failure and goes on |
| pinned to a generation | its packages are updated like any other jail's: the pin holds the root, not the packages |
| thick | its own packages are updated; its base (`FreeBSD-*`) is not, because that is done inside the jail by hand ([lifecycle.md 3.7](lifecycle.md#37-a-thick-freebsd-jail)) |
| Debian | a record `success` with the note that the jail keeps its own packages |
| the health check fails, or `pkg upgrade` fails | a record `failure` that names the snapshot and the command that restores it (`jailops jail rollback web --scope all --snapshot S`), and nothing is rolled back for you: package files live in the jail's own datasets, and a root rollback does not undo them |
| `update.reboot=auto` | the jail is restarted after a successful update, so the updated programs run; with `manual` (the default) the record says "restart the jail to run the updated programs" |

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

What JailOps records on its own, independent of the mail: every move and every package update as a record in the
jail's history (`jailops update history web --project shop`: result, change, the snapshot, and for a failure the
error and the way back), and the operations (`jailops op list`). The runs listed under **Automatic upgrades** in
`jailops image show freebsd-15-1` are the daemon's own nightly runs ([1.6](#16-the-built-in-ways-and-which-to-choose));
an `image upgrade` run from a script is in the histories and the operations, not there.

### 1.5 Before the first night: the checklist for an unattended run

1. **A health check on every jail**, because an upgrade without one passes when the jail starts, not when the service
   answers. The command runs inside the jail after every move and every package update, and a non-zero exit swaps
   the root back (a move) or writes the failure with the snapshot (packages):

       jailops config set web --project shop update.healthcheck="/usr/local/bin/check" update.reboot=auto
       jailops config set db --project shop update.healthcheck="/usr/local/bin/check" update.reboot=auto

   `/usr/local/bin/check` is yours: a request to the service's port, `service nginx status`, a `psql` that answers.
   `update.reboot=auto` restarts the jail after a package update, so the night's packages run in the morning.
2. **A backup before the night**, not during it: a backup policy whose schedule runs before 03:00 keeps the night's
   backup ahead of the update ([backup-restore.md](backup-restore.md)):

       jailops backup policy create nightly --schedule "0 1 * * *" -l env=prod --target node1 --keep-daily 14
       jailops backup list --project shop                        # in the morning: last night's backups are there

   A host without a backup node has the snapshots the updates take; they are a way back for the jail, not a backup.
3. **Run the two JailOps lines by hand with `--dry-run` first**, and then without:

       jailops image upgrade freebsd-15-1 --dry-run
       jailops update apply --all --packages --project shop --dry-run
       jailops update apply --all --packages --project blog --dry-run

4. **Run the script once by hand** (`sh /usr/local/etc/periodic/daily/490-jail-upgrade`), read what it prints, and
   switch it on: `sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=YES`.
5. **A safe default for a jail that must not move at night:** pin it (`jailops config set db --project shop
   image=freebsd-15-1#g3`, [lifecycle.md 3.11](lifecycle.md#311-a-jail-that-stays-behind)); the image step skips it
   and lists it, and the package step still updates its packages. Leave such a jail out of the package step by
   naming the others instead of `--all`: `jailops update apply web --packages -y --project shop`.

**To skip a night:** `sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO`, and `YES` the day after. A file
without the executable bit is ignored by periodic too (`chmod -x` the script), but the variable is the switch the
mail shows.

### 1.6 The built-in ways, and which to choose

JailOps has two ways of doing the jail part without a script of yours.

- **The nightly upgrade of an image** (`jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00`,
  [upgrade.md](upgrade.md#nightly-auto-upgrade)): the daemon runs the same image upgrade itself at that time, in the
  host's time zone, and then, with `--upgrade-packages` (the default), updates the packages inside the **running**
  jails of the image, of every project; a stopped jail's packages are left alone, so a stopped jail is not a failure.
  A host that was off at the time runs it when it is back within four hours. The first jail that fails stops the run,
  and every run is recorded on the image (`jailops image show freebsd-15-1`: the next run, the latest runs with their
  results) with the event `image-upgrade-failed` for a failed one. It needs no token, no client and no mail.
- **Managed updates per jail** (`jailops config set web --project shop update.mode=managed update.schedule="0 3 * * *"
  update.packages=true update.os=true`, [lifecycle.md 3.9](lifecycle.md#39-packages-inside-a-jail)): the daemon
  checks and applies, per jail, at the jail's own cron expression, what the jail's policy allows; `notify` checks and
  reports only. The OS part moves the jail to the newest generation that exists; it builds none.

| You want | Use |
|---|---|
| the host's base and packages updated at night too, and one mail in the morning | the periodic script: nothing else touches the host |
| the jails only, no script, a record per run on the image, and a host that catches up after being off | the nightly upgrade of the image |
| one jail on its own schedule, or packages only for some jails and the base for others | managed updates per jail |
| both the script and the nightly upgrade | not at the same hour: an image is locked while it is upgraded, and the second upgrade of it is refused with `busy` |

The host part has no built-in way: JailOps never upgrades the host ([the third rule](lifecycle.md)).

### 1.7 In the morning

    freebsd-version -kru                                 # -k and -r differ: reboot the host
    jailops doctor                                       # every line ok or info
    jailops list -c name,state,image,gen                 # every jail running that ran before, on the generation you expect
    jailops image generation list freebsd-15-1           # who is on which generation; a jail left behind stands out
    jailops update history web --project shop            # newest first: success, or the error and the snapshot
    jailops op list                                      # the night's operations and their results

The mail itself: a `! failed:` line names the step. For a jail, the record says what happened and what to do
([lifecycle.md 6](lifecycle.md#6-when-something-fails)): `rolled_back`, the jail is on its old root, read the health
check's output, fix, and the next night moves it; `failure` (packages), the packages stay updated and the record names
the snapshot that undoes them; a stopped jail, start it or leave it out. `jailops info web --project shop` prints the
conditions (`Ready`, `Healthy`, `BasePackages`) and the last update.

## 2. Moving host1 from 15.1 to 15.2 when it is released

The order is [lifecycle.md 3.2](lifecycle.md#32-a-minor-release-of-the-host-151-to-152) (the host) and
[3.6](lifecycle.md#36-a-thin-jail-to-a-new-release) (the jails); this is the same with the script around it. FreeBSD
publishes the **Upgrading Instructions** of 15.2 at `https://www.freebsd.org/releases/15.2R/upgrading/` when 15.2 is
announced; until then the page of 15.1, [releases/15.1R/upgrading](https://www.freebsd.org/releases/15.1R/upgrading/),
is the shape of it, and the values that change (the `OSVERSION`, the boot environment's name) are the new page's.

1. **Switch the script off** and let a running night finish:

       sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO
       jailops op list --active                          # nothing of the script's runs any more

2. **BEFORE** of [lifecycle.md](lifecycle.md#3-the-runbooks), with a fresh backup and the boot environment the
   Upgrading Instructions ask for:

       jailops doctor && jailops version && freebsd-version -kru
       jailops list -c name,state,image,gen              # written down
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
   running, on `freebsd-15-1`, where they keep getting 15.1's patch levels. JailOps itself is not touched by this
   step: the packages are the same `FreeBSD:15` packages on 15.2 ([lifecycle.md 2](lifecycle.md#2-what-works-with-what)).
4. **The image of the new release**, built once:

       jailops image create freebsd-15-2 --release 15.2-RELEASE --build

5. **One jail first, and the rest after it**, the first failure stopping the rest:

       jailops jail upgrade web --project shop --image freebsd-15-2 --dry-run
       jailops jail upgrade web --project shop --image freebsd-15-2
       jailops image upgrade freebsd-15-1 --to freebsd-15-2 --dry-run        # every thin jail of the old image, every project
       jailops image upgrade freebsd-15-1 --to freebsd-15-2

   `image upgrade --to` skips and lists a jail pinned to a generation; naming it moves it:
   `jailops jail upgrade db --project shop --image freebsd-15-2`. A thick jail moves inside the jail with the same
   `-oABI` and `-oOSVERSION` as the host ([lifecycle.md 3.7](lifecycle.md#37-a-thick-freebsd-jail)); a Linux jail is
   not concerned by a FreeBSD release.
6. **The checks** of [lifecycle.md 4](lifecycle.md#4-always-test-after), per jail: the jail runs, `freebsd-version -j
   shop_web` prints 15.2, the health check passed (`jailops update history web --project shop`), the service answers.
7. **The script afterwards.** Two lines change, and nothing else: the `pkg` lines are the same, because
   `FreeBSD-base` now points at `base_release_2` through the host's own `OSVERSION`, and the `jailops` lines name
   the image through the variable.

       release="15.2-RELEASE"
       image="freebsd-15-2"

   Run it by hand once (`sh /usr/local/etc/periodic/daily/490-jail-upgrade`), then switch it on:
   `sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=YES`. Until `release` is changed the script exits 3 with
   the line "the host runs 15.2-RELEASE, not 15.1-RELEASE", in the mail: that is the guard.
8. **The old image.** `freebsd-15-1` stays as long as a jail uses one of its generations, and `jailops image delete
   freebsd-15-1` is refused with `image_in_use` and the names while one does. Each moved jail keeps its old root for
   14 days (`jailops jail info web --project shop` prints the day under `Rollback:`); delete the image after that:

       jailops image generation list freebsd-15-1        # USED BY empty for every generation
       jailops image delete freebsd-15-1

9. **Going back**, strongest first: `jailops jail rollback web --project shop` returns the root and the image
   together, data untouched, within the 14 days; a jail with a thick layout goes back to its snapshot of step 2 in
   3.7's way; the host goes back with `bectl activate pre-15.2 && shutdown -r now` only after every jail that moved
   to 15.2 was rolled back, because a 15.2 userland does not run on the 15.1 kernel. Put `release` and `image` back
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
- **The image is `freebsd-16-0`**, and the jails move with the same commands as to 15.2. A move across majors prints a
  warning: the packages inside the jail were built for `FreeBSD:15` and are reinstalled for `FreeBSD:16`.
- **A thick jail is a pkgbase system of its own** and takes the major upgrade inside the jail, with the `-oABI=FreeBSD:16:$(uname -p)`
  and the `-oOSVERSION` of the 16.0 Upgrading Instructions, then `pkg upgrade -f` of its packages.

The commands, in order, once 16.0 is announced and the `FreeBSD:16` packages are published:

    sysrc -f /etc/periodic.conf daily_jail_upgrade_enable=NO
    # BEFORE (lifecycle.md), the backups, then the host as the Handbook's major upgrade (3.3) and the reboot
    pkg upgrade -r FreeBSD-ports-kmods && pkg upgrade -r FreeBSD-ports        # the third-party packages, for FreeBSD:16
    pkg upgrade -r jailops && service jailopsd restart && jailops doctor      # JailOps for FreeBSD:16, as 3.4 does it
    jailops image create freebsd-16-0 --release 16.0-RELEASE --build
    jailops jail upgrade web --project shop --image freebsd-16-0 --dry-run
    jailops jail upgrade web --project shop --image freebsd-16-0
    jailops update apply web --project shop --packages               # the packages of the jail, rebuilt for FreeBSD:16
    jailops exec web --project shop -- pkg upgrade -f                 # what the first line left on the old ABI
    jailops image upgrade freebsd-15-2 --to freebsd-16-0              # the rest, one at a time, the first failure stopping it

Then the script: `release="16.0-RELEASE"`, `image="freebsd-16-0"`, and on. Going back is the boot environment of 15
while no jail has moved to 16, and `jailops jail rollback` per jail within 14 days after that; for a jail whose
packages were reinstalled for `FreeBSD:16`, `jailops jail rollback web --project shop --scope all --snapshot S
--confirm-data-loss` with the snapshot of its package update returns the root to the 15 image and the packages to
their `FreeBSD:15` builds in one step, and discards what the jail wrote since.

## 4. The command line, by question

Every line is from the built `jailops` of this repository; `shop` is the project and `web` the jail.

| The question | The command |
|---|---|
| Is there anything to update? | `jailops update check --project shop` (every jail of the project; a stopped jail answers with an error for its packages); `jailops image upgrade freebsd-15-1 --dry-run` (the build plan and the jails that would move) |
| What would an apply do? | `jailops update apply --all --packages --project shop --dry-run` |
| Apply it | `jailops image upgrade freebsd-15-1`; `jailops update apply --all --packages -y --project shop` |
| One jail only | `jailops jail upgrade web --project shop`; `jailops update apply web --packages --project shop` |
| What happened last night? | `jailops update history web --project shop`; `jailops op list`; `jailops image show freebsd-15-1` for the daemon's own nightly runs |
| Go back | `jailops jail rollback web --project shop` (the root, and the image after a release move; data untouched, 14 days); `jailops jail rollback web --project shop --scope all --snapshot S --confirm-data-loss` (a package update, discarding what was written since) |
| Keep a jail where it is | `jailops config set db --project shop image=freebsd-15-1#g3`; back to the newest generation with `jailops config set db --project shop image=freebsd-15-1` |
| Who is on which generation? | `jailops image generation list freebsd-15-1`; `jailops list -c name,state,image,gen` |
| Move everyone to a new release | `jailops image create freebsd-15-2 --release 15.2-RELEASE --build`; `jailops image upgrade freebsd-15-1 --to freebsd-15-2` |
| Delete the old image | `jailops image generation list freebsd-15-1` (no jail in `USED BY`), then `jailops image delete freebsd-15-1` |
| Let the daemon do the nights | `jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00`; off with `--auto-upgrade=false` |
