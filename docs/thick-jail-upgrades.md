# Upgrading a thick jail by hand

A thick jail has its own writable root and package database: a pkgbase system of its own on FreeBSD, an Alpine or Debian
root on Linux. Its base is updated inside the jail, with `pkg` against the `FreeBSD-base` repository or with `apk`; JailOps
manages the jail around that work: it stops and starts it, snapshots its datasets, shows its logs and restores a snapshot.
Run a release upgrade on one jail at a time and check the service before returning traffic to it. Where this fits in the
whole, and the checks after it: [lifecycle.md](lifecycle.md#37-a-thick-freebsd-jail).

A thin FreeBSD jail follows the base of its image instead: a patch level is a new generation, with the `/etc` merge done by
`etcupdate`, and `jailops jail upgrade NAME --image IMAGE` moves it to an image of a newer release ([upgrade.md](upgrade.md#jails)).

## Before changing the base

1. For a FreeBSD release upgrade, upgrade and reboot the host first: a jail's userland is never newer than the host's
   kernel ([FreeBSD Handbook, Jails and Containers](https://docs.freebsd.org/en/books/handbook/jails/)). Check the running
   kernel and installed userland with `freebsd-version -kru` on the host, then run `jailops doctor`.
2. Drain traffic and stop the application cleanly. Take an application-consistent backup, then stop the jail and take a
   named snapshot. Snapshot any attached ZFS volumes separately while their writers are stopped. For shared volumes,
   coordinate with the other jails. Back up host-directory volumes at the source.
3. Start the jail for maintenance and keep application traffic off it. If normal startup launches the application,
   arrange for it to stay stopped during the update.

Example for jail `web` in project `shop`, with a ZFS volume named `web-data`:

```sh
jailops stop web --project shop
jailops jail snapshot create web before-base-upgrade --project shop
jailops volume snapshot create web-data before-base-upgrade --project shop
jailops start web --project shop
jailops shell web --project shop
```

## FreeBSD pkgbase

The Handbook updates a pkgbase jail with `pkg`, from the host with `pkg -r ROOT` or inside the jail with a plain `pkg
upgrade`, and says that `freebsd-update` does not apply to it and that `etcupdate` is not run in it: pkg merges `/etc`
itself ([Handbook, Jail Upgrading](https://docs.freebsd.org/en/books/handbook/jails/)). The repository `FreeBSD-base` is
defined in `/etc/pkg/FreeBSD.conf` and disabled by default; enable it in the jail with the line
`FreeBSD-base: { enabled: yes }` in `/usr/local/etc/pkg/repos/FreeBSD.conf`
([15.0 release notes](https://www.freebsd.org/releases/15.0R/relnotes/)). Its URL selects the release through
`base_release_${VERSION_MINOR}`.

**A patch level**, inside the jail:

```sh
freebsd-version -u
pkg update -r FreeBSD-base
pkg upgrade -n -r FreeBSD-base
pkg upgrade -r FreeBSD-base
```

The same from the host, with the OS-level jail name `PROJECT_JAIL`: `pkg -j shop_web update -r FreeBSD-base` and
`pkg -j shop_web upgrade -r FreeBSD-base`.

**A new release** (15.1 to 15.2) follows the pkgbase section of that release's Upgrading Instructions
(`https://www.freebsd.org/releases/X.YR/upgrading/`), as the host does. For 15.1 the page's command is
`pkg -oABI=FreeBSD:15:$(uname -p) -oOSVERSION=1501000 upgrade -r FreeBSD-base`, where `-oOSVERSION` selects the branch
of the new release ([15.1 Upgrading Instructions](https://www.freebsd.org/releases/15.1R/upgrading/)); the 15.2 page
carries 15.2's value. A new major release is a new package ABI: the page says to reinstall the third-party packages
afterwards (`pkg upgrade`).

Confirm that the plan names the intended release before the last command. Then review what pkg could not merge: a file
it left as `FILE.pkgnew` and a copy it kept as `FILE.pkgsave`:

```sh
find /etc /usr/local/etc -name '*.pkgnew' -ls
```

`freebsd-version -u` inside the jail prints the installed userland afterwards. The image the jail was created from says
where it started; it says nothing about the jail's base from then on.

## Alpine and Debian

For routine package updates of an Alpine jail, `apk update` and `apk upgrade` inside the jail, or `jailops update apply
web --packages`, which snapshots first and records the result. When moving between Alpine releases, follow Alpine's
release upgrade procedure for the target repositories inside the jail, with the same snapshot and acceptance process. A
Debian jail is updated with `apt-get` inside the jail; JailOps never runs `apt`. A new base of either is a new jail:
[linux-jails.md](linux-jails.md).

## Restart and verify

Exit the shell, restart the jail and check the console and application logs. Start the application if you held it back.
Test a request and, where applicable, check that the application can read and write its data before routing traffic
back; the full list is [lifecycle.md, always test after](lifecycle.md#4-always-test-after).

```sh
jailops restart web --project shop
jailops logs --project shop --jail web --since 15m --app-logs
```

Keep the checkpoint until the application has passed your checks. If the new version has written data, export or back up
those writes before deciding whether to restore the jail. For a thick jail, use `--scope all` with the named snapshot to recover
the old root and jail-owned datasets. The CLI asks you to confirm that writes since the snapshot can be discarded.
Attached volumes have their own recovery decisions. For example, an application may need its database restored to the
same point as its binaries and configuration.

```sh
jailops stop web --project shop
jailops jail rollback web --scope all --snapshot before-base-upgrade --project shop
```

Start the jail after any required volume recovery and repeat the same checks.
