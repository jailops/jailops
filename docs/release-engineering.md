# Release engineering for new FreeBSD releases

This page answers what the maintainers do when FreeBSD publishes a snapshot, a beta or a release, and how the parts of
JailOps that depend on a FreeBSD version fit together. The maintainers read it. What is supported is in
[supported-versions.md](supported-versions.md).

## How the pieces fit

- **Packages are built per package ABI, and the ABI is the major version** (`FreeBSD:15:amd64`, `FreeBSD:15:aarch64`).
  15.1 and 15.2 install the same packages and the same static binaries. A new major (16) needs builds for
  `FreeBSD:16:*`. The size of the repository and what to do when it grows are in the
  [capacity page of jailops-packages](https://github.com/jailops/jailops-packages/blob/master/docs/capacity.md).
- **Images are one per release.** `freebsd-15-1` and `freebsd-15-2` live side by side, and the generations of an image are
  the patch levels of its release ([upgrade.md](upgrade.md#an-image-is-one-release)).
- **The nightly auto upgrade stays inside one release.** It builds the next generation of an image and moves its thin jails
  to it ([upgrade.md](upgrade.md#nightly-auto-upgrade)). A new release is explicit: upgrade the host, create the image, create
  jails on it ([upgrade.md](upgrade.md#a-new-release-move-the-jails)).
- **The gate of the continuous integration is a FreeBSD 15.1 VM** that runs the end-to-end and packet filter suites of
  every change ([ci-supply-chain.md](ci-supply-chain.md) lists the workflows). Other releases are brought in by the
  steps below.
- **Pages holds the newest two versions of every package per ABI; the archive in GitHub Releases holds every file ever
  published** ([retention in jailops-packages](https://github.com/jailops/jailops-packages/blob/master/docs/RETENTION.md)).
- **The host check is `jailops doctor`:** it fails below 15.1 ([supported-versions.md](supported-versions.md)).

## Release JailOps

The maintainers' procedure, from a filled changelog to a package a host can install. Each step ends when its check is true.

1. **A release candidate.** The `Unreleased` section of `CHANGELOG.md` is filled, CI on master is green
   (`gh run list -w CI -b master -L1`), and the [workflow walkthrough](#before-a-release-the-workflow-walkthrough) has run on a
   FreeBSD test host.
2. **The release pull request.** `just release-pr X`. The pull request touches only `version.txt` and `CHANGELOG.md`. A change runs the quick gate
   (lint, build, the race tests, the console tests); the pull request of a release (`release/*`), the nightly run and `gh workflow run CI`
   also run the browsers, the fuzzers and the FreeBSD VMs. When its CI is green, `gh pr merge N --squash --delete-branch`, then
   `gh run watch` until CI on master is green.
3. **The tag and the release.** `just release X` needs the quick gate green on the commit and a full run (the release pull request, the
   nightly run or a manual one) green on the same tree; it says which. Then `gh run watch` on the Release run, and `gh release view vX` lists the
   assets.
4. **jailops-packages is quiet.** `gh run list -w "Build and publish" -L3` shows nothing running and nothing failed, and no
   other bump pull request is open.
5. **The bump.** `gh workflow run "Check for updates"` and `gh run watch`. In the bump pull request the versions are X, the
   sha256 is not `PENDING`, and the branch build is green. Read the bump note, then merge.
6. **The publish.** `gh run watch` on the publish run: `publish` and `deploy` succeed, and the prune plan and the size line are
   as expected. Merge no other bump until it has finished and ten more minutes have passed: the Pages cache lives that
   long (`max-age=600`).
7. **A host sees it.** `pkg update -f -r jailops && pkg rquery -r jailops %v jailops` prints X. Announce the release only now.
8. **If the deploy failed** within one day: `gh workflow run build-and-publish.yml -f deploy_run_id=RUN`, then
   `gh workflow run build-and-publish.yml`
   ([recovery in jailops-packages](https://github.com/jailops/jailops-packages/blob/master/docs/supply-chain.md)).
   A bad release is answered with release X+1. A tag is never moved.

## Before a release: the workflow walkthrough

Security audits and tests ask whether the code does what it says. The walkthrough asks whether it does what a jail
administrator needs. [test/workflows/daily-tasks.md](../test/workflows/daily-tasks.md) lists the tasks of a jail
administrator, from getting the image of a new release to destroying a jail, with the command that does each one today and a
rating: `OK` (one command, or the commands that belong together), `MORE STEPS` (it can be done with steps the administrator
should not need) or `MISSING`.

- **The steps.** `just e2e-workflows` runs `scripts/e2e-workflows.sh` on a disposable FreeBSD machine, as root, with the machine
  set up as `scripts/ci-freebsd-e2e.sh` does it (`JAILOPS_E2E_PART=workflows`). Each OK row is a step that does the task and
  checks the result. A row that is `MISSING` has a pending step that prints `PENDING` and starts to run in the change that adds
  the command. Run it before the release is tagged: it takes a few minutes, so CI does not.
- **The gaps.** Every row that is not `OK` points at an entry `W-name` in the section "Gaps found by the workflow walkthrough" of the
  [roadmap](roadmap.md): what is planned and the shape of the command. An entry leaves the roadmap in the change that adds the
  command, together with the row's rating and its step.
- **The check.** `scripts/check-workflows-table.sh` keeps the table, the steps and the roadmap in step: every row has a unique
  ID, a rating and a step that fits it; a row that is not OK has an entry in the roadmap and every entry belongs to a row; every step
  belongs to a row. CI runs it, `just check` runs it, and `just release-pr` runs it on master before it opens the release
  pull request.
- **A new task.** When someone names a task that is not in the table, add a row in the same change that answers it: rated, with its entry
  in the roadmap when it is not OK.

## Update a host

The procedure for the people who run JailOps is one page, [lifecycle.md](lifecycle.md), with [upgrade.md](upgrade.md) as
the reference behind it. In order, FreeBSD first and JailOps second, each with a boot environment of its own and never both
in one `pkg upgrade`:

1. Before: `jailops doctor`, the versions written down, fresh backups, `bectl create` ([lifecycle.md, the runbooks](lifecycle.md#3-the-runbooks)).
2. FreeBSD 15.1 to 15.2: the commands of the release's Upgrading Instructions page, the `.pkgnew` files of `/etc`, the
   `anchor "jailops/*"` line, the boot loader, reboot, the checks, and the way back with `bectl activate`
   ([3.2](lifecycle.md#32-a-minor-release-of-the-host-151-to-152); no `zpool upgrade` while the way back is needed).
3. JailOps: `bectl create`, the database copy, `pkg upgrade -r jailops`, `service jailopsd restart`, `jailops doctor`, and the
   way back ([3.4](lifecycle.md#34-jailops-itself), [going back](upgrade.md#going-back-one-version-of-jailops)).
4. A new image and the jails on it when you want them: `jailops jail upgrade NAME --image freebsd-15-2` moves a jail with its data
   ([3.6](lifecycle.md#36-a-thin-jail-to-a-new-release)).
5. FreeBSD 15 to 16: after the repository serves `FreeBSD:16` packages, with the host converted to pkgbase first
   ([3.3](lifecycle.md#33-a-major-release-of-the-host-15-to-16)).

## What to do, by event

The dates come from FreeBSD's [release engineering page](https://www.freebsd.org/releng/) (15.2 in December 2026,
16.0 in December 2027, as read on 2026-11-19). The steps hang on events, so they hold when a date moves.

### A snapshot of the next minor release or of the next major appears

1. Add a scheduled job that boots the snapshot and runs the end-to-end and packet filter suites, in the shape of the other
   VM jobs. It blocks no pull request and is not part of `CI ok`.
2. Read what fails. The failures are changes in what JailOps calls directly: `jail(8)`, pf, ZFS, `ifconfig`, dummynet.
   Fix them in the ordinary way, on a branch, with a test.

### At BETA and RC

1. Run every shard of the end-to-end and packet filter suites on the build, and `jailops doctor` on the VM.
2. Fix what fails. A failure that only the new release shows goes into [supported-versions.md](supported-versions.md) as a
   known difference until it is fixed.

### At the release of a minor release (15.2)

1. Add the release to the VM jobs of `ci.yml` next to the gate. When the VM action has no image of it yet, keep the
   snapshot job and add the entry when the image exists.
2. Update the matrix in [supported-versions.md](supported-versions.md): the new row, and FreeBSD's end date for the minor
   release before it.
3. Leave the minimum of `jailops doctor` at 15.1 unless a feature needs a newer release.
4. Build nothing new for the packages: the ABI is the same.
5. Run the host flow once on a real host: upgrade the host, `jailops image create freebsd-15-2 --release 15.2-RELEASE`,
   `jailops image build freebsd-15-2`, and a jail on it ([upgrade.md](upgrade.md#a-new-release-move-the-jails)).

### At the release of a new major (16.0)

1. Add `FreeBSD:16:amd64` and `FreeBSD:16:aarch64` to the ABI list of `pkgops-repo.yaml` in jailops-packages.
2. Check the size of the site. Two majors together are about 490 MB with two versions kept
   ([capacity](https://github.com/jailops/jailops-packages/blob/master/docs/capacity.md)).
3. Publish, then run `pkg install` on a new-major VM and `jailops doctor` after it.
4. Build `freebsd-16-0` and create a jail on it. Keep the 15.x gate until the new major has run the suites for a full
   release cycle, then add it as a gate.
5. Add the rows to [supported-versions.md](supported-versions.md).

### When a minor release ends (15.1 on March 31, 2027)

1. Drop it from the VM jobs.
2. Move its row to "works, not supported".
3. Nothing changes in the packages: they keep installing.

### When the old major nears the end of FreeBSD's support (`stable/15`, December 31, 2029)

1. Announce the date, and take `FreeBSD:15:*` out of the ABI list of jailops-packages. Its files stay in the archive.
2. Drop the major from the VM jobs.
3. Move its rows to "works, not supported".
