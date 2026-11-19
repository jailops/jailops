# Upgrading a thick jail by hand

A thick jail has its own writable root and package database. Update it inside the jail with pkg on FreeBSD or apk on
Alpine. JailOps manages the jail around that work. It can stop and start it, snapshot its datasets, show logs and restore
a snapshot. Run a release upgrade on one jail at a time and check the service before returning traffic to it.

A thin FreeBSD jail follows the base of its image. Its patch upgrade switches generations and uses `etcupdate` to merge
persistent configuration, and `jailops jail upgrade NAME --image IMAGE` moves it to an image of a newer release. See
[Upgrading](upgrade.md#jails) for the image workflow.

## Before changing packages

1. For a FreeBSD release upgrade, update and reboot the host first. Check the running kernel and installed userland with
   `freebsd-version -kru` on the host, then run `jailops doctor`. Read the target release's pkgbase instructions in the
   [FreeBSD Handbook](https://docs.freebsd.org/en/books/handbook/jails/).
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
jailops exec web --project shop --user root -- /bin/sh
```

## FreeBSD pkgbase

Inside the jail, use pkg to inspect the installed userland and select the signed base repository for the target release.
The examples assume the active base repository is named `FreeBSD-base`. The repo URL must select the intended minor
release. For example, 15.1 and 15.2 both use the FreeBSD 15 pkg ABI. For a major release, follow the target release's
instructions for the target ABI, OSVERSION and any required signing keys or pkg bootstrap.

Preview the base upgrade before accepting it:

```sh
freebsd-version -u
pkg update -r FreeBSD-base
pkg upgrade -n -r FreeBSD-base
pkg upgrade -r FreeBSD-base
```

Confirm that the plan names the intended release before the last command. Pkg handles merges for tagged configuration
files. Review any `.pkgnew` and `.pkgsave` it reports. A separate `etcupdate` pass is unnecessary for pkgbase.

After a major release change, reconcile third-party packages against the target ABI and follow any application-specific
migration instructions. Check that target builds are available before starting the upgrade. The original JailOps image
name identifies where the thick jail was created. Use `freebsd-version -u` inside it to check the installed userland.
[FreeBSD Handbook on pkgbase jails](https://docs.freebsd.org/en/books/handbook/jails/)

## Alpine

For routine package updates, run `apk update` and `apk upgrade` inside the jail. When moving between Alpine releases,
follow the release upgrade procedure for the target repositories. Use the same snapshot and acceptance process.
[Linux jails in JailOps](linux-jails.md)

## Restart and verify

Exit the shell, restart the jail and check the console and application logs. Start the application if you held it back.
Test a request and, where applicable, verify the application can read and write its data before routing traffic back.

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
