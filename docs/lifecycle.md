# Updating FreeBSD, the host and the jails

The one page for keeping a JailOps host current: FreeBSD on the host, JailOps itself, and the jails. It says what each
thing is, what works with what, the order of the steps for every situation, what to test afterwards, and the way back.
The mechanics of each JailOps command are in [upgrade.md](upgrade.md); this page is the runbook. One administrator's
way of running it, night after night with a script in periodic(8) and through the next releases with the command line,
is written out in [automating-updates.md](automating-updates.md).

**Three rules, before anything else.**

1. **The host first, the jails after.** A jail runs on the host's kernel. The FreeBSD Handbook: "The FreeBSD version
   running in the jail can not be newer than the version running in the host"
   ([Jails and Containers, Host Configuration](https://docs.freebsd.org/en/books/handbook/jails/)). JailOps refuses to
   move a jail to a release newer than the host's kernel (`version_skew`, "upgrade the host first").
2. **One thing at a time, each with its way back.** FreeBSD, then JailOps, then the jails: never the base system and
   JailOps in one `pkg upgrade`, and a boot environment or a snapshot before each step.
3. **Nothing moves on its own.** A host is never upgraded by JailOps. A jail moves to a new generation only when you run
   the upgrade or turned the nightly upgrade on; it moves to a new release only when you say so.

## 1. The model

| Word | What it is | Where you see it |
|---|---|---|
| Release | `15.1-RELEASE`, `15.2-RELEASE`: a minor release of the 15 major. FreeBSD publishes one every three months ([release schedule](https://www.freebsd.org/releng/)): 15.2 in December 2026, 15.3 in June 2027, 16.0 in December 2027 | `freebsd-version -u` |
| Patch level | `15.1-RELEASE-p6`: the security and errata patches of a release, numbered. A new patch level changes the userland, often the kernel, never the release | `freebsd-version -kru` |
| Kernel and userland | The kernel is the host's and only the host's; a jail has none. The userland (`/bin`, `/lib`, ...) is the host's own, and each jail's own. The host kernel runs userlands of its own release and of older ones: the GENERIC kernel of 15.1 carries `COMPAT_FREEBSD14` down to `COMPAT_FREEBSD4` ([GENERIC of releng/15.1](https://cgit.freebsd.org/src/tree/sys/amd64/conf/GENERIC?h=releng/15.1)). It never runs a newer one | `freebsd-version -k` (installed kernel), `-r` (running kernel), `-u` (userland), `-j JAIL` (a jail's userland), [freebsd-version(1)](https://man.freebsd.org/cgi/man.cgi?query=freebsd-version&sektion=1) |
| Base, two ways | **pkgbase**: the base system as packages (`FreeBSD-runtime`, `FreeBSD-kernel-generic`, ...) from the repository `FreeBSD-base`, updated with `pkg`. The 15.1 installer installs it by default ([15.1 release notes](https://www.freebsd.org/releases/15.1R/relnotes/)). **Distribution sets**: the base as tarballs, updated with `freebsd-update`. The Handbook: support for distribution sets is planned for removal in FreeBSD 16 and continues, with `freebsd-update`, for the lifetime of the 15 stable branch ([Jail Upgrading](https://docs.freebsd.org/en/books/handbook/jails/)). Never mix the two on one system (same section) | `pkg which /usr/bin/uname`: a package name means pkgbase, "not found in the database" means distribution sets ([15.1 Upgrading Instructions](https://www.freebsd.org/releases/15.1R/upgrading/)); `jailops doctor` says it too (`host.pkgbase`) |
| The repositories | `/etc/pkg/FreeBSD.conf` of a RELEASE defines `FreeBSD-ports` (`quarterly`), `FreeBSD-ports-kmods` and `FreeBSD-base` with the URL `base_release_${VERSION_MINOR}` and the keys of `/usr/share/keys/pkgbase-${VERSION_MAJOR}`; `FreeBSD-base` is **disabled by default** ([FreeBSD.conf of releng/15.1](https://cgit.freebsd.org/src/tree/usr.sbin/pkg/FreeBSD.conf.quarterly-release?h=releng/15.1)). Enable it with the line `FreeBSD-base: { enabled: yes }` in `/usr/local/etc/pkg/repos/FreeBSD.conf` ([15.0 release notes](https://www.freebsd.org/releases/15.0R/relnotes/)). `${VERSION_MINOR}` comes from pkg's `OSVERSION`, which is why the release upgrade below passes `-oOSVERSION` | `pkg -vv` shows the repositories and the ABI |
| Package ABI | `FreeBSD:15:amd64`, `FreeBSD:15:aarch64`: the major release and the architecture, nothing else ([pkg.conf(5)](https://man.freebsd.org/cgi/man.cgi?query=pkg.conf&sektion=5)). 15.1 and 15.2 install the same third-party packages and the same JailOps packages; FreeBSD 16 is a new ABI, and every package has to come from a `FreeBSD:16` build | `pkg config abi` |
| Image | One FreeBSD release, named after it: `freebsd-15-1` is 15.1-RELEASE. Built from pkgbase (`pkg -r ROOT install` of the base packages of `base_release_1`), never with `freebsd-update` | `jailops image list` |
| Generation | `freebsd-15-1#g3`: one immutable build of the image, a dataset with a `@ro` snapshot. Each `image upgrade` builds the next one with the release's current patch level; JailOps records the FreeBSD version of its `FreeBSD-runtime` (`15.1-RELEASE-p6`). The number counts builds on this host and says nothing about FreeBSD. A new release is a new image, never a generation of the old one | `jailops image generation list freebsd-15-1` (column `FREEBSD`); `jailops list` writes `freebsd-15-1 (g3, p6)` |
| Thin jail | A FreeBSD jail whose root is a ZFS clone of one generation. `/etc`, `/usr/local`, `/var/db`, `/var/log`, `/var/mail`, `/var/spool`, `/home`, `/root` and the volumes are datasets of its own. An upgrade swaps the root for a clone of a newer generation and keeps those | `jailops jail info web` (layout `thin`) |
| Thick jail | A FreeBSD jail with a full writable copy of its base. It gets no new generation: its base is updated inside it with `pkg` against `FreeBSD-base`, like a pkgbase host | layout `thick` |
| Linux jail | Alpine or Debian under the Linuxulator; always thick. Alpine's packages are updated with `apk` by JailOps; Debian keeps its own (`unattended-upgrades`); the base of either is replaced by a new jail, never upgraded | `os: linux` |
| JailOps | The packages `jailops`, `jailops-client`, `jailops-apps` and, on a backup node, `jailops-backupd` from the repository `jailops` (`pkg.jailops.net/${ABI}`), built per package ABI. The daemon `jailopsd` and the helper `jailops-priv` are one service | `jailops version` |

**Who does what.**

| JailOps does | You do |
|---|---|
| Builds image generations, moves thin jails between generations and images with a snapshot, an `/etc` merge, a health check and an automatic root rollback; runs the nightly upgrade you turned on; updates the packages inside FreeBSD and Alpine jails (`pkg -j`, `apk`) on a schedule or on request; keeps the base package records of a thin jail in step with its root at every start; converts its own stored state in place when you upgrade it ([upgrade-guarantee.md](upgrade-guarantee.md)) | Upgrade the host's FreeBSD with FreeBSD's tools; upgrade JailOps with `pkg`; decide when a jail moves to a new release; update thick and Linux jails inside the jail; keep backups and test them; run the checks of [section 4](#4-always-test-after) |

## 2. What works with what

The host's kernel decides. Image generations record the release of their base packages, and the preflight of every move
compares it with `kern.osrelease` of the host.

| Host kernel | Jail userland (the image's release) | Package ABI inside the jail | Result |
|---|---|---|---|
| 15.1 | 15.1 | `FreeBSD:15` | works, supported |
| 15.2 | 15.1 | `FreeBSD:15` | works: a jail stays on the release of its image and keeps getting its patch levels until you move it; FreeBSD supports 15.1 until March 31, 2027 ([security page](https://www.freebsd.org/security/)) |
| 15.2 | 15.2 | `FreeBSD:15` | works, supported from the release of 15.2 and after JailOps's test suites pass on it ([supported-versions.md](supported-versions.md)) |
| 15.1 | 15.2 | | **refused**: `jailops jail upgrade --image freebsd-15-2` and `jailops image upgrade --to` answer `version_skew`, "upgrade the host first"; `jailops app install` without `--image` never picks such an image; `jailops jail import` of a jail exported from a newer release names the move to make after the import |
| 16.0 | 15.x | `FreeBSD:15` | works for the jail's base (an older userland on a newer kernel); its packages were built for the old ABI and are reinstalled for `FreeBSD:16` when the jail moves to a 16 image |
| 16.0 | 16.0 | `FreeBSD:16` | after FreeBSD 16.0 is released and `pkg.jailops.net` serves `FreeBSD:16` packages ([release-engineering.md](release-engineering.md)) |
| 15.0 or older | any | | **not supported**: `jailops doctor` fails `host.release`; FreeBSD's support of 15.0 has ended (it is not in the supported table of [freebsd.org/security](https://www.freebsd.org/security/)) |

What JailOps refuses, by design, with the error you see:

| You ask | Answer |
|---|---|
| a move to an image of an older release (`freebsd-15-2` to `freebsd-15-1`) | `invalid`: "a jail does not move to an older release"; create a new jail from the older image instead |
| a move to an image of another OS, or of a Linux jail | `invalid` or `unsupported`: a jail moves between releases of one OS; a Linux jail is replaced by a new jail |
| `jail upgrade` or `jail upgrade --image` of a thick jail | `unsupported`: a thick jail is updated inside the jail ([3.7](#37-a-thick-freebsd-jail)) |
| `image build` of a RELEASE image whose repository serves another release | the build stops at `verify-release` |
| a daemon older than its database or than a stored object | `version_skew` at start, the data untouched: upgrade the binary, do not downgrade the data |
| a daemon newer than its helper | `version_skew` at start: `service jailops_priv restart` |
| `jail rollback --scope all --snapshot S` where `S` was taken on another image | refused: roll the root back first |

**The compatibility stance, once.** Stored JailOps state is converted in place by the next release that reads it, and a
host of any release from 0.8.0 on upgrades in place ([upgrade-guarantee.md](upgrade-guarantee.md)). From 1.0 the API
and the command line only grow within 1.x: an older client works against a newer daemon, a newer client fails fast
against an older one ([compatibility.md](compatibility.md)). FreeBSD's own rule is the first one on this page: a jail's
userland is never newer than the host's kernel, and older is what the kernel's `COMPAT_FREEBSD*` options are for.

## 3. The runbooks

Every runbook starts with the same **BEFORE** and ends with the same **AFTER**; they are written once here.

**BEFORE, every time.**

    jailops doctor                               # clean, or you know why not
    jailops version                              # client and server versions, written down
    freebsd-version -kru                         # installed kernel, running kernel, userland: written down
    jailops list -c name,state,image,gen         # which jail is where: written down
    jailops boot                                 # which jails start at boot: a jail without autostart stays down after a reboot
    jailops backup list --project shop           # the backups of the important jails are fresh (backup-restore.md)
    bectl create pre-STEP                        # a boot environment of the host's root before a step that changes the host

`bectl create NAME` clones the running boot environment; `bectl activate NAME` makes it the default for the next boot,
`bectl activate -t NAME` for one boot only, `bectl list` shows them ([bectl(8)](https://man.freebsd.org/cgi/man.cgi?query=bectl&sektion=8)).
A boot environment holds the root file system, so it holds the base system, the JailOps binaries, the configuration in
`/usr/local/etc/jailops` and, with the installer's default layout, `/var/db/jailops` (check with `zfs list -o name,mountpoint`).
It does not hold the jails or the JailOps state dataset: those are datasets of their own and are not rolled back by a boot
environment.

**AFTER, every time:** the table of [section 4](#4-always-test-after).

### 3.1 A patch level of the host (15.1-p6 to 15.1-p7)

The jails are not touched: a thin jail gets its patch levels through its image ([3.5](#35-patch-levels-of-thin-jails)), a
thick jail inside the jail ([3.7](#37-a-thick-freebsd-jail)).

1. BEFORE, with `bectl create pre-p7`.
2. **pkgbase host**, as the Handbook writes it
   ([Updating FreeBSD with packages](https://docs.freebsd.org/en/books/handbook/cutting-edge/)):

       pkg update -r FreeBSD-base
       pkg upgrade -r FreeBSD-base

   **Distribution sets**, as the Handbook writes it ([FreeBSD Update](https://docs.freebsd.org/en/books/handbook/cutting-edge/)):

       freebsd-update fetch
       freebsd-update install

3. **Reboot when the kernel changed:** `freebsd-version -k` (installed) differs from `freebsd-version -r` (running).
   Without a kernel change, `service jailopsd restart` is enough for the daemon to run on the new libraries. A reboot
   starts the jails with autostart and no other (`jailops boot`).
4. AFTER.
5. **Going back:** `bectl activate pre-p7 && shutdown -r now`; on distribution sets also `freebsd-update rollback`
   ([freebsd-update(8)](https://man.freebsd.org/cgi/man.cgi?query=freebsd-update&sektion=8)).

### 3.2 A minor release of the host (15.1 to 15.2)

FreeBSD publishes one **Upgrading Instructions** page per release, `https://www.freebsd.org/releases/X.YR/upgrading/`,
with the commands for that release. The page of 15.1 is
[releases/15.1R/upgrading](https://www.freebsd.org/releases/15.1R/upgrading/); the one for 15.2 appears with 15.2, under
the same path. The steps below are the 15.1 page's, with its release-specific values marked.

1. BEFORE, with `bectl create -r pre-15.2` (the page's own first step on ZFS). Read the release's errata and release
   notes (linked from the same page).
2. **Which way the host is updated:** `pkg which /usr/bin/uname`.
3. **pkgbase host.** The 15.1 page, for a host on 15.0:

       pkg upgrade -r FreeBSD-base                                              # the current release, fully patched first
       pkg -oABI=FreeBSD:15:$(uname -p) -oOSVERSION=1501000 upgrade -r FreeBSD-base   # 1501000 is 15.1's value; the 15.2 page has 15.2's
       pkg upgrade -r FreeBSD-ports-kmods                                       # third-party kernel modules, if any
       find /etc /usr/local/etc -name '*.pkgnew' -ls                            # configuration files pkg could not merge

   `-oOSVERSION` selects the branch `base_release_<minor>` of the new release through `${VERSION_MINOR}` of the
   repository URL. For every `.pkgnew`, compare and merge (`diff /etc/rc.conf /etc/rc.conf.pkgnew`), as the page says;
   a `.pkgsave` is the copy pkg kept of a file it took over.

   **Distribution sets.** The 15.1 page, for a host on 14.4 or 15.0:

       freebsd-update fetch
       freebsd-update install                     # all pending patches of the current release first
       freebsd-update upgrade -r 15.1-RELEASE     # the new release's name
       freebsd-update install                     # the kernel; freebsd-update may ask for help merging /etc files
       shutdown -r +10min "Rebooting for a kernel upgrade"
       freebsd-update install                     # after the reboot: the userland
       freebsd-update install                     # once more: removes stale files

4. **Both ways: upgrade the boot loader** as the page describes for the host's firmware (`sysctl machdep.bootmethod`,
   then the UEFI or BIOS steps), then reboot.
5. **`/etc/pf.conf` still has the line `anchor "jailops/*"`** among the filter rules, and
   `pfctl -nf /etc/pf.conf` accepts the file: pkg's merge of `/etc` leaves a `.pkgnew` next to a file it could not merge,
   and `freebsd-update` prints the files it did not merge. `jailops doctor` (`pf.anchors`, `pf.order`,
   `pf.state-policy`) says whether the loaded ruleset is right.
6. AFTER. `freebsd-version -kru` prints the new release three times.
7. **A new image for the new release, when you want it:** [3.6](#36-a-thin-jail-to-a-new-release). The jails you have
   stay on their image and keep getting its patch levels.
8. **Going back:** `bectl activate pre-15.2 && shutdown -r now`. The jails come back on the old kernel with the roots they
   have; a jail you moved to the new image in between is moved back with `jailops jail rollback` first, because its
   userland would be newer than the old kernel.
   **Do not run `zpool upgrade`** until the way back is not needed any more and every backup node runs the new release:
   the 15.1 Upgrading Instructions discourage it, JailOps never runs it, and `jailops doctor` (`zfs.compat`) fails when
   the pool has features a replication target cannot read.

### 3.3 A major release of the host (15 to 16)

What FreeBSD has published about 16, and what follows from it for a JailOps host:

- Distribution sets and `freebsd-update` continue for the lifetime of the 15 stable branch and are planned for removal
  in FreeBSD 16 ([Handbook, Jail Upgrading](https://docs.freebsd.org/en/books/handbook/jails/)). A host on
  distribution sets is converted to pkgbase while it runs 15: the Handbook recommends `pkgbasify`
  ([Updating FreeBSD with packages](https://docs.freebsd.org/en/books/handbook/cutting-edge/)); it needs up to 5 GiB
  free and makes a boot environment first. `jailops doctor` reminds you (`host.pkgbase`).
- FreeBSD 16 is a new package ABI, `FreeBSD:16:*`. Third-party packages are reinstalled for it (`pkg upgrade` after the
  base, as the Handbook's major upgrade ends; `pkg-static upgrade -f` on distribution sets), and the JailOps packages
  come from `pkg.jailops.net` once it serves `FreeBSD:16` ([release-engineering.md](release-engineering.md)).
  **Do not upgrade the host to 16 before that.**
- The 16.0 Upgrading Instructions page (`https://www.freebsd.org/releases/16.0R/upgrading/`) is the procedure; the
  Handbook's **major upgrade with ZFS** of today is the shape of it. The Handbook writes it for 15.0 and says to put
  the targeted version and the architecture in; with 16 that is:

      bectl create 16.0-RELEASE
      mkdir /mnt/upgrade
      bectl mount 16.0-RELEASE /mnt/upgrade
      env ABI=FreeBSD:16:amd64 pkg-static -c /mnt/upgrade upgrade -r FreeBSD-base
      chroot /mnt/upgrade
      freebsd-version -ku
      exit
      bectl activate -t 16.0-RELEASE
      shutdown -r now                      # one boot; bectl activate 16.0-RELEASE once it runs well

  The same section says to save the origins of the installed packages first (`pkg prime-origins | sort -u > /var/tmp/pkg-prime-origins.txt`),
  to lock `pkg` before the upgrade (`pkg -c /mnt/upgrade lock pkg`, `pkg unlock pkg` after), and to run `pkg upgrade`
  after the first boot.

The order on a JailOps host: BEFORE; convert to pkgbase if needed and reboot (a step of its own); wait for `FreeBSD:16`
packages of JailOps; upgrade the base in a new boot environment and reboot; `pkg upgrade` the third-party packages,
`pkg upgrade -r jailops` ([3.4](#34-jailops-itself)); AFTER; then the image `freebsd-16-0`
(`jailops image create freebsd-16-0 --release 16.0-RELEASE --build`) and the jails one at a time
([3.6](#36-a-thin-jail-to-a-new-release): a move across majors warns that the packages inside the jail were built for
the old ABI, and you reinstall them). Going back is the boot environment of 15 while no jail has moved to 16.

### 3.4 JailOps itself

After the host's FreeBSD, never in the same `pkg upgrade`. Read the `CHANGELOG.md` section of the release first: it
names what you do about a change, and a configuration key it removed stops the daemon with the key's name until you
remove it.

1. BEFORE, with `bectl create pre-jailops-X.Y.Z`.
2. The upgrade:

       service jailopsd stop                                                    # checkpoints the database
       cp /var/db/jailops/jailops.db /var/db/jailops/jailops.db.pre-upgrade
       pkg upgrade -r jailops                                                   # every package of the jailops repository
       service jailopsd restart                                                 # the helper first, then the daemon
       jailops doctor
       jailops list

   `pkg upgrade -r jailops` takes `jailops`, `jailops-client`, `jailops-apps` and, on a backup node, `jailops-backupd`
   together; naming only two of them leaves the others behind, and a bare `pkg upgrade` on a pkgbase host would mix the
   base system in. Running jails are kernel jails: restarting the daemon and the helper stops none of them. The API and
   the reconcile loop are down for the seconds of the restart; the NDP proxy of a switched IPv6 network announces its
   addresses again when the daemon is back.
3. AFTER. `jailops version` prints the new version for the client and the server.
4. **Going back:** [upgrade.md, going back one version of JailOps](upgrade.md#going-back-one-version-of-jailops). The
   database has forward-only migrations, so going back is the old binaries **with** the old database: `bectl activate
   pre-jailops-X.Y.Z` and a reboot do both at once; without a boot environment, `pkg add -f` of the previous packages
   from `/var/cache/pkg` and the database copy put back with the daemon stopped.

### 3.5 Patch levels of thin jails

A thin jail follows its image. Building the next generation downloads the release's current base packages from
`pkg.FreeBSD.org` (minutes); moving a jail takes a snapshot, clones the new root, merges `/etc` three ways, swaps,
starts the jail and runs its health check, and swaps back when the start or the check fails. The details are in
[upgrade.md](upgrade.md#what-an-upgrade-of-a-jail-does).

1. BEFORE (no boot environment: nothing on the host changes).
2. By hand, the whole image or one jail:

       jailops image upgrade freebsd-15-1 --dry-run          # the build plan, and the jails that would move
       jailops image upgrade freebsd-15-1                    # the next generation, then every thin jail, one at a time
       jailops image upgrade freebsd-15-1 --no-jails         # the generation only
       jailops jail upgrade web --project shop --dry-run     # one jail: what would change
       jailops jail upgrade web --project shop

   Or every night, per image, which does the same and upgrades the packages inside the running jails too:

       jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00
       jailops image set freebsd-15-1 --auto-upgrade --upgrade-packages=false
       jailops image show freebsd-15-1                       # the next run, the latest runs and their results

   A run stops at the first jail that fails; the jails after it wait for the next run or for you. Thick jails, Linux
   jails and jails pinned to a generation (`freebsd-15-1#g3`) are skipped and listed. A stopped jail is moved, started
   for the health check and stopped again. Nothing is kept when the repository had nothing new. The same from a
   script of your own in periodic(8), with the host's patch level in the same night: [automating-updates.md](automating-updates.md).
3. AFTER, for the jails that moved: `jailops image generation list freebsd-15-1` shows who is where,
   `jailops update history web` the record of each move.
4. **Going back:** `jailops jail rollback web --project shop` returns the root to the previous generation and leaves
   the data alone, for 14 days after the move (`jailops jail info web` shows the day). `jailops image generation delete
   freebsd-15-1 g4` removes a generation no jail uses.

### 3.6 A thin jail to a new release

A new release is a new image, and a jail moves to it with its data, volumes, addresses and configuration, through the
same steps as a generation upgrade. It needs the host on that release or newer ([3.2](#32-a-minor-release-of-the-host-151-to-152)).

1. BEFORE. `freebsd-version -k` is at least the release of the new image.
2. **The image of the new release**, once per host:

       jailops image create freebsd-15-2 --release 15.2-RELEASE --build
       jailops image set freebsd-15-2 --auto-upgrade --upgrade-at 03:00     # if the old image had it: the setting is per image

   The console offers the same (Images, **Create image**, filled in with the host's release) when the host runs a newer
   release than its newest image.
3. **One jail first, and look at it:**

       jailops jail upgrade web --project shop --image freebsd-15-2 --dry-run    # the releases, what is kept, the steps
       jailops jail upgrade web --project shop --image freebsd-15-2

   The jail follows the new image from then on: `spec.image`, `jailops list`, the nightly upgrade of the new image. The
   packages installed in the jail stay as they are and keep working inside one major (same ABI). A move across majors
   (15 to 16) prints a warning: reinstall them with `jailops update apply web --packages` and `pkg upgrade -f` inside
   the jail.
4. **The rest**, one at a time, the first failure stopping the rest:

       jailops jail upgrade web db mail --project shop --image freebsd-15-2
       jailops jail upgrade -l env=prod --project shop --image freebsd-15-2
       jailops image upgrade freebsd-15-1 --to freebsd-15-2 --dry-run        # every thin jail of the old image, every project
       jailops image upgrade freebsd-15-1 --to freebsd-15-2

5. AFTER, per jail. `jailops jail diff web --summary` lists what the jail holds on top of the new image.
6. **Going back:** `jailops jail rollback web --project shop` returns the root **and the image** together, data untouched,
   for 14 days. Delete the old image when no jail uses it: `jailops image delete freebsd-15-1`.

### 3.7 A thick FreeBSD jail

A thick jail is a pkgbase system of its own: its base is updated with `pkg` against `FreeBSD-base`, inside the jail, the
way the Handbook updates a pkgbase jail ([Jail Upgrading](https://docs.freebsd.org/en/books/handbook/jails/)), and a
release move follows the Upgrading Instructions of the release, as for the host. The full walk, with the checkpoint and the
checks, is [thick-jail-upgrades.md](thick-jail-upgrades.md).

1. BEFORE, plus a snapshot of the jail and of its volumes while their writers are stopped:

       jailops stop web --project shop
       jailops jail snapshot create web before-base-upgrade --project shop
       jailops volume snapshot create web-data before-base-upgrade --project shop
       jailops start web --project shop

2. Inside the jail (`jailops shell web --project shop`, or `jailops exec web --project shop -- CMD`), with
   `FreeBSD-base` enabled in the jail's `/usr/local/etc/pkg/repos/FreeBSD.conf` (`FreeBSD-base: { enabled: yes }`):

       pkg update -r FreeBSD-base
       pkg upgrade -n -r FreeBSD-base          # the plan: the packages of the right release
       pkg upgrade -r FreeBSD-base             # a patch level; for 15.1 to 15.2 add the -oABI and -oOSVERSION of the release's Upgrading Instructions
       find /etc /usr/local/etc -name '*.pkgnew' -ls

   The same from the host: `pkg -j shop_web update -r FreeBSD-base` and `pkg -j shop_web upgrade -r FreeBSD-base`
   (the OS-level jail name is `PROJECT_JAIL`). Do not run `etcupdate` in a pkgbase jail: pkg merges `/etc` itself
   (Handbook, same section).
3. `jailops restart web --project shop`, then AFTER.
4. **Going back:** `jailops jail rollback web --project shop --scope all --snapshot before-base-upgrade` (the jail
   stopped; it discards what the jail wrote since), and the volumes' own snapshots.

### 3.8 A Linux jail

The base of a Linux jail is its image generation, copied in full; a new base is a new jail. Alpine's packages are updated
by JailOps (`jailops update check`, `jailops update apply web --packages`, the nightly run of the image with packages on),
with a snapshot first; Debian's are the jail's own business (`unattended-upgrades`, or `apt-get` by hand). A release move
(Alpine 3.22 to 3.23, Debian 13 to 14) is a new image and a new jail: create it next to the old one, move the data with the
application's own tools, move the addresses or ports, delete the old jail. [linux-jails.md](linux-jails.md).

### 3.9 Packages inside a jail

Packages live on the jail's own datasets. A thin jail's base packages (`FreeBSD-*`) move only with the generation; its
other packages and a thick jail's are updated by the host's `pkg -j PROJECT_JAIL` with the jail's own repositories,
Alpine's by `apk` in the jail. Off for every jail until you set its mode.

    jailops config set web --project shop update.mode=managed update.schedule="0 3 * * 0" update.packages=true update.os=true update.healthcheck="/usr/local/bin/check" update.reboot=manual
    jailops update check --project shop                 # every jail of the project: what could change, nothing changes
    jailops update apply web --project shop --packages  # snapshot, pkg upgrade, ldconfig, health check, record
    jailops update history web --project shop           # newest first, with the snapshot of each update
    jailops pkg upgrade web --project shop              # plain pkg upgrade -y in the jail, no snapshot and no record

`update.mode` is `manual` (default), `notify` (check on the schedule, report) or `managed` (check and apply);
`update.reboot=auto` restarts the jail after a package update, `manual` leaves it to you and says so in the record. A
failed package update is **not** rolled back by JailOps: the record names the snapshot and the command that restores it
(`jail rollback --scope all --snapshot S`), which discards what the jail wrote since. By hand, the manual equivalent is
`pkg -j shop_web upgrade` on the host or `pkg upgrade` in the jail, after `jailops jail snapshot create`.

### 3.10 Apps of the catalog

An installed app is an ordinary thin jail with the labels `jailops.app` and `jailops.app.version`. There is no app
update: its packages follow the jail's update policy ([3.9](#39-packages-inside-a-jail)), its base follows the image
([3.5](#35-patch-levels-of-thin-jails), [3.6](#36-a-thin-jail-to-a-new-release)), and `jailops app installed` lists the
jails with the version that was installed. The catalog itself is the package `jailops-apps`, which `pkg upgrade -r jailops`
keeps current; a definition written for a newer JailOps is listed as invalid until JailOps is upgraded ([apps.md](apps.md)).
The `app-installed` snapshot of the installation is the way back to the fresh state (`jail rollback --scope all`).

### 3.11 A jail that stays behind

To keep a jail on a generation while the image moves on, pin it: `jailops config set web --project shop image=freebsd-15-1#g3`
(the plain image name follows the newest generation). `image upgrade` and the nightly run skip it and list it; `jailops jail
upgrade web --generation g5` moves it when you want. A jail on an old image after the host moved to a new release keeps
running and keeps getting the patch levels of its release until FreeBSD ends them ([supported-versions.md](supported-versions.md));
`jailops doctor` does not warn about it, so write the date down. Delete an image only when no jail uses it
(`image_in_use` names the jails).

## 4. Always test after

One table, used by every runbook. Run it after the host, after JailOps and after each jail; `shop` and `web` are the
project and the jail.

| Check | Command | What you want |
|---|---|---|
| Versions | `freebsd-version -kru` on the host; `freebsd-version -j shop_web` or `jailops exec web --project shop -- freebsd-version -u` for a jail | the three lines of the host agree (a different `-k` and `-r` means a reboot is due); the jail's userland is the release of its image and not newer than the host's `-r` |
| JailOps versions | `jailops version` | client and server print the version you installed; `jailops server info` lists the extensions |
| The host checks | `jailops doctor` | every line `ok` or `info`. `host.release` fail: below 15.1. `host.release` warn: newer than the test matrix. `host.pkgbase` warn: distribution sets. `pf.anchors` fail: the anchor line is gone from the loaded ruleset. `pf.order`, `pf.state-policy`, `pf.inbound`: a rule of yours before the anchor. `zfs.compat` fail: do not replicate until the target can read the pool. `jails.base-packages` warn: `jailops jail repair-packages`. `operations.journal` fail: an interrupted rollback. `network.leftovers`: the old bridge layout, `jailops doctor --fix` lists the commands. The full list: [troubleshooting.md](troubleshooting.md) |
| The jails run | `jailops list`; `jailops jail info web --project shop` | `running` for every jail that ran before; `image` and `gen` as expected. Conditions: `Ready=True` (`Stopped` is not a problem for a jail you stopped), `FirewallApplied=True` (`StatesPending` passes within seconds; `Unverified` means pf could not be asked), `BasePackages` absent, `InSync` with no `Drift`, `Healthy` without `Degraded` |
| The health check | `jailops update history web --project shop`, `jailops image show freebsd-15-1` | the newest record is `success`; `update.healthcheck` of the jail is a command that fails when the service is wrong, and every upgrade runs it |
| The services | `jailops exec web --project shop -- service -e`; `jailops exec web --project shop -- service nginx status` | the services the jail is for are enabled and running; `jailops app installed --project shop` shows the app's state and ports |
| The logs | `jailops logs --project shop --jail web --since 15m --app-logs`; the console log of the jail on the host, `/var/log/jailops/jail_shop_web_console.log` | nothing new that was not there before: a service that fails to start, a missing library, a file the new release does not ship |
| The firewall | `jailops firewall show web --project shop`; `jailops firewall test --from internet --to jail:web --port 443 --project shop`; `pfctl -sr \| grep jailops` on the host | the rules you expect decide; exit status 0 for a connection that must pass, 9 for one that must not |
| Network in and out | from the jail: `jailops exec web --project shop -- ping -c1 -4 pkg.freebsd.org` and `-6`, `jailops exec web --project shop -- drill pkg.freebsd.org`; from outside: the service's own address and port | the jail resolves names and reaches the Internet in every family it has; the port answers from where it should |
| Volumes and data | `jailops volume list --project shop`; `jailops exec web --project shop -- df -h`; `jailops jail diff web --project shop --summary` | every volume attached and mounted where the jail expects it; the application's own check that it reads and writes its data |
| Backups | `jailops backup run nightly --project shop`; `jailops backup verify --project shop ID --depth full` | the run succeeds for every jail and volume, and a full verification boots the backup in a hidden jail and passes |
| The console | `https://HOST:39110` | the overview loads, the jail's page shows the same as `jailops jail info`, a terminal opens (`jailops jail console web --project shop` does the same from the command line) |

## 5. What to watch for

- **`/etc` on the host:** pkg leaves `FILE.pkgnew` next to a file it could not merge and `FILE.pkgsave` for a file it
  took over ([Handbook, Jail Upgrading](https://docs.freebsd.org/en/books/handbook/jails/)); `freebsd-update` prints the
  files it did not merge and asks during `install`. `find /etc /usr/local/etc -name '*.pkgnew' -ls` after every base
  upgrade. The files that matter for JailOps: `/etc/pf.conf` (the anchor line, `set state-policy if-bound`, the scrub
  line), `/etc/rc.conf` (`jailopsd_enable`, `pf_enable`, `gateway_enable`, `ipv6_gateway_enable`), `/etc/sysctl.conf`,
  `/boot/loader.conf` (the modules `jailops doctor` lists under `kernel.module.*`), `/etc/resolv.conf`.
- **`/etc` in a thin jail** is merged by JailOps with `etcupdate` between the old and the new generation; a file both
  changed that cannot be merged stops the move (`merge_conflict`), the jail stays where it was, and the files are named.
  Resolve them in the jail and run the move again.
- **The pf anchor** `anchor "jailops/*"` is the whole firewall of the jails. After every host upgrade: `pfctl -sr | grep anchor`
  and `jailops doctor`. A `quick` pass rule of yours above the anchor wins over every policy (`pf.order`).
- **`zpool upgrade`**: never while a boot environment of the previous release is your way back, never while a backup
  node or a replication target runs an older release ([3.2](#32-a-minor-release-of-the-host-151-to-152)).
- **Boot environments** hold the root file system only. The jails, the volumes and the state dataset are outside them,
  so `bectl activate` of an older environment takes the base system and JailOps back and leaves the jails as they are.
- **The ABI** changes with the major release only. A package built for `FreeBSD:15` runs on 15.2; on 16 it is reinstalled.
  A package that has no build for the new ABI yet is a reason to keep that jail on the old image (3.11).
- **The jail's own package repository**: `pkg -j` reads the repositories configured **in the jail**. A jail with a
  repository file of its own (`/usr/local/etc/pkg/repos/*.conf`) keeps using it; the base packages of a thin jail never
  come through it.
- **rctl and cpuset**: a jail with `resources.cpuset` names CPUs; `jailops doctor` (`jails.cpuset`) warns when the host
  does not have them. Limits that need `kern.racct.enable=1` are `Limits=NotEnforced` on a host without it.
- **devfs rulesets**: JailOps renders the ruleset of each jail; a ruleset number of your own in `/etc/devfs.rules` must
  still exist after the upgrade.
- **The resolver**: a host whose `/etc/resolv.conf` is rewritten by `resolvconf` or DHCP at boot changes what the jails
  get; `jailops doctor` (`network.dns`) asks the gateway of every network.
- **A reboot starts only the jails with autostart** (`jailops boot`). A jail you stopped for maintenance stays stopped.
- **The nightly upgrade and a planned host reboot**: a host that is off at the chosen time runs the upgrade when it is
  back within four hours; a reboot in that window runs it right after boot.

## 6. When something fails

**The record says what happened.** `jailops update history web --project shop`, `jailops image show IMAGE` (the runs of the
nightly upgrade) and `jailops op list` carry one of these results:

| Result | Meaning | What you do |
|---|---|---|
| `success` | the update was applied and the health check passed | nothing; a package update with `update.reboot=manual` waits for your restart |
| `preflight_failed` | nothing changed: the move did not start (the host kernel is older than the image, a `root.new` of an interrupted operation exists, a snapshot of the jail has clones, replication runs), or the `/etc` merge needs a human (`merge_conflict`): the jail stays on its generation | read the error; resolve the conflict in the jail, or the precondition, and run the command again |
| `rolled_back` | the jail was started on the new root and failed its start or its health check; it was swapped back to its old root, data untouched | read the health check's output in the record; fix; run again |
| `recovery_needed` | the undo did not finish: the jail may be between the old and the new root | `service jailopsd restart` replays the interrupted plan from its journal at start; then `jailops info web`, `jailops doctor` (`operations.journal`, `startup.recovery`) |
| `failure` (packages) | `pkg upgrade` failed, or the health check after it, or the automatic restart (`update.reboot=auto`): the packages stay updated | the record names the snapshot and the command that restores it |
| `interrupted` (an operation) | the daemon stopped while the operation ran; `error.detail.recovery` is `rolled_back`, `completed`, `dropped` or `needs_attention` | check the object, repeat the command; `needs_attention` keeps its journal record and `jailops doctor` names it |

**The ways back, strongest first.**

1. `jailops jail rollback web --project shop` (`--scope root`): the previous root, and the previous image after a release
   move, for 14 days after the move; data untouched. `--dry-run` shows the plan.
2. `jailops jail rollback web --project shop --scope data --snapshot S --confirm-data-loss`: the data datasets back to a
   snapshot (`jailops jail snapshot list web` shows them with the bytes written since); everything written since is
   gone. `--scope all` does both.
3. `bectl activate pre-STEP && shutdown -r now`: the host's base system and JailOps back to before the step; the jails as
   they are.
4. A backup: `jailops backup restore --project shop ID --as web-restored --dry-run`, then without `--dry-run`
   ([backup-restore.md](backup-restore.md)). For a jail whose data the application's own upgrade rewrote in a way its
   old version cannot read, and for a host you cannot bring back.

## 7. Cheat sheet

    # before any step
    jailops doctor && jailops version && freebsd-version -kru && jailops list -c name,state,image,gen && jailops boot
    bectl create pre-STEP

    # host: a patch level (pkgbase / distribution sets), reboot when -k and -r differ
    pkg update -r FreeBSD-base && pkg upgrade -r FreeBSD-base
    freebsd-update fetch && freebsd-update install

    # host: a minor release, the commands of https://www.freebsd.org/releases/X.YR/upgrading/
    bectl create -r pre-15.2
    pkg -oABI=FreeBSD:15:$(uname -p) -oOSVERSION=1501000 upgrade -r FreeBSD-base     # the OSVERSION of the page
    find /etc /usr/local/etc -name '*.pkgnew' -ls
    freebsd-update upgrade -r 15.1-RELEASE && freebsd-update install                 # distribution sets, then reboot, install, install

    # JailOps itself
    service jailopsd stop && cp /var/db/jailops/jailops.db /var/db/jailops/jailops.db.pre-upgrade
    pkg upgrade -r jailops && service jailopsd restart && jailops doctor

    # thin jails: patch levels
    jailops image upgrade freebsd-15-1 --dry-run
    jailops image upgrade freebsd-15-1
    jailops image set freebsd-15-1 --auto-upgrade --upgrade-at 03:00

    # thin jails: a new release (host first)
    jailops image create freebsd-15-2 --release 15.2-RELEASE --build
    jailops jail upgrade web --project shop --image freebsd-15-2 --dry-run
    jailops jail upgrade web --project shop --image freebsd-15-2
    jailops image upgrade freebsd-15-1 --to freebsd-15-2

    # packages in a jail
    jailops update check --project shop
    jailops update apply web --project shop --packages
    jailops update history web --project shop

    # after every step
    jailops doctor && jailops list && jailops update history web --project shop
    jailops firewall test --from internet --to jail:web --port 443 --project shop
    jailops logs --project shop --jail web --since 15m --app-logs

    # back
    jailops jail rollback web --project shop                                   # root (and image), data untouched
    jailops jail rollback web --project shop --scope all --snapshot S --confirm-data-loss
    bectl activate pre-STEP && shutdown -r now
