# The in-place upgrade of a jail

A FreeBSD jail is an independent system on one dataset, and `jailops jail upgrade NAME` upgrades it in place while
it runs: the FreeBSD base packages from the host with `pkg -r` on the jail's root, as the FreeBSD Handbook upgrades a
pkgbase jail, the ports packages inside the jail, as one operation with one snapshot and one health check. JailOps never
stops, starts or restarts a jail for an upgrade; with nothing newer the answer is "Nothing to upgrade." and nothing
happens. A stopped jail is not upgraded. A new release is `--release 15.2-RELEASE`, and only a global
administrator runs it. The whole procedure, the flags and the order on a host are in
[lifecycle.md](lifecycle.md#35-a-freebsd-jail); this page is the checkpoint around it, the checks after it, the
way back, and what a Linux jail needs.

Inside a jail `FreeBSD-base` stays disabled, as FreeBSD ships it: `pkg install` and `pkg upgrade` there handle the
ports packages and never touch the base. Leave it disabled: with it enabled `pkg` resolves a dependency of a port, such
as `llvm19`'s on `FreeBSD-clibs-lib32`, from the base
([FreeBSD bug 293817](https://bugs.freebsd.org/bugzilla/show_bug.cgi?id=293817)), and the base packages carry `schg`
files, which a jail may not change. A base package the jail needs is added from the host with `jailops jail base add
NAME FreeBSD-ssh`; `jailops jail base list NAME` shows what is installed. A package that is already installed answers
"nothing to add". The file that keeps the repository disabled is
`/usr/local/etc/pkg/repos/FreeBSD-base.conf`: every new image has it (and `FreeBSD.conf` there, which disables the kernel
modules repository `FreeBSD-ports-kmods`), and a jail made from an image without it gets it at its
creation.

The release a jail is at is the one JailOps recorded on its root (the property `jailops:freebsd_version`, set when
the jail is made, upgraded, given a base package or rolled back), not what the files of the jail say: `/bin/freebsd-version`
and the package database are the tenant's to change. When the root says another release than the record, the upgrade
is refused as `precondition_failed` and names both; name the release with `--release`, or roll the jail back.

## What the upgrade runs

`jailops jail upgrade web --project shop --dry-run` prints the plan and changes nothing. pkg decides what changes: its own
dry run (`pkg upgrade -n`) runs on a copy of the jail's package database, and the command prints that report, the
packages with their old and new versions, how much pkg downloads (only when pkg says), how much the root grows or
shrinks, the health check, the snapshot that will be the way back, and each step with the command it runs: the prefetch
into the package cache, the snapshot (taken while the jail runs), the `pkg -r` update and upgrade of the base with the
repository configuration outside the jail's root, the checks that every package is at the version pkg planned and that
the release is the one asked for, the settle step, the checksums, the ports packages through `pkg -j`, the list of the
processes that still run replaced files, and the health check. pkg merges `/etc` itself, as the [Handbook](https://docs.freebsd.org/en/books/handbook/jails/) says of a pkgbase jail.

No package script runs on the host (`RUN_SCRIPTS=false`). What the scripts of the base packages do runs inside the jail
after every base change: `pwd_mkdb`, `services_mkdb`, `cap_mkdb` and `certctl rehash`. The repository of the release must
be built for the host's kernel or an older one: a catalogue built for a newer kernel is refused as `version_skew` before
anything changes, with the hint to upgrade the host first.

The services of the jail keep running their old programs and libraries until they are restarted: the result and the
update history list them as "restart pending" (from `procstat -v` inside the jail). A security fix of a library or a
daemon takes effect when you restart that service, or the jail, in a window of your choosing; JailOps restarts nothing.

A failure is reported and left as it is: the jail runs on as pkg left it, and running the upgrade again completes it.
Nothing is rolled back on its own.

## A jail JailOps does not trust

Before pkg works on a jail's root JailOps checks it for what only an attack on host-side pkg puts there: a symbolic
link out of the root (absolute, or climbing above it with `..`; `etc/termcap` and `etc/motd` are made by the base and
kept; below `usr/local`, `home`, `usr/home` and `srv`, where no base package installs, only a link to a host directory
counts), a link among pkg's own files, and a `pkg.conf` that loads plugins. Such a jail is not upgraded: the command
answers `forbidden` with `suspected_compromised` and everything it found, the jail gets the condition
`SuspectedCompromised`, `jailops doctor` fails `jail.suspected_compromised`, the update history has the record, and the
event `jail-suspected-compromised` goes out. The other jails of a bulk run go on, and a jail's scheduled update tries again at its next run. Look at the jail;
remove what was found (or make a link of your own point inside the jail with a relative target); the next run checks
again and goes on. This is a detection, not a boundary: the trust it assumes is in
[security-model.md](security-model.md#the-in-place-upgrade-of-a-jail).

While an upgrade or any other operation has the jail, `jailops cp` and the files API answer `busy` for that jail.

## Before the upgrade

The snapshot of the upgrade is the way back, and a rollback puts the whole root back: the base, the ports packages, the
package database and everything the services wrote to the root since the snapshot. For a service whose writes matter:

1. For a new release, upgrade and reboot the host first: a jail's userland is never newer than the host's kernel
   ([FreeBSD Handbook, Jails and Containers](https://docs.freebsd.org/en/books/handbook/jails/)). `freebsd-version -kru`
   on the host, then `jailops doctor`.
2. Take an application-consistent backup. Data that must survive a rollback of the root belongs in a volume
   ([storage.md](storage.md#volumes)); a volume is outside the root and is not rolled back, so snapshot it yourself while its
   writers are stopped, and back up host-directory volumes at the source. For shared volumes, coordinate with the other
   jails.
3. Drain traffic from the jail if the service must not lose writes made between the upgrade and a rollback.

Example for jail `web` in project `shop`, with a ZFS volume named `web-data`:

```sh
jailops jail upgrade web --project shop --dry-run
jailops volume snapshot create web-data before-upgrade --project shop
jailops jail upgrade web --project shop
```

## After the upgrade

Check the console and application logs, test a request and, where applicable, that the application can read and write
its data before routing traffic back; the full list is
[lifecycle.md, always test after](lifecycle.md#4-always-test-after).

```sh
jailops exec web --project shop -- freebsd-version -u
jailops exec web --project shop -- find /etc /usr/local/etc -name '*.pkgnew'
jailops update history web --project shop
jailops logs --project shop --jail web --since 15m --app-logs
```

A `FILE.pkgnew` is a configuration file pkg could not merge; compare it with the file and merge it by hand. The upgrade
itself lists the `*.pkgsave` files it finds in `/etc` and `/usr/local/etc`: the copies pkg kept of files it replaced.

## Going back

`jailops jail rollback web --project shop` restores the snapshot of the last upgrade (`jo-pre-upgrade-<time>`) within its
14 days and uses it up. It refuses a running jail: a root rolled back under running services kills them (a program that
maps a file the rollback changes dies of SIGBUS), and JailOps does not stop jails. Stop the jail yourself, roll back,
start it:

```sh
jailops stop web --project shop
jailops jail rollback web --project shop --confirm-data-loss
jailops start web --project shop
```

The release recorded for the jail goes back with it. The command names the snapshot and how long ago it was taken, says
that everything written to the root since and the snapshots taken after it are discarded, and asks you to type the jail
name; in a script pass `--confirm-data-loss`. If the new version has written data you want to keep, export it first. The
volumes have their own snapshots and their own recovery decisions.

A snapshot of your own is restored with `--scope all`:

```sh
jailops jail snapshot create web before-change --project shop
jailops jail rollback web --scope all --snapshot before-change --confirm-data-loss --project shop
```

Repeat the checks after the rollback.

## Alpine and Debian

Linux jails look after their own packages: JailOps does not update them ([linux-jails.md](linux-jails.md#packages)).
