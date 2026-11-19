# Supported FreeBSD versions

This page answers which FreeBSD releases JailOps runs on and supports, for the person who chooses a host and plans its
upgrades.

## The matrix

The dates are FreeBSD's, as listed on [freebsd.org/security](https://www.freebsd.org/security/) and
[freebsd.org/releng](https://www.freebsd.org/releng/) on 2026-11-19. Those pages are the source and can move them.

| FreeBSD | FreeBSD supports it until | JailOps |
|---|---|---|
| 16.0 (planned for December 2027) | not released | Not supported until it is released. Snapshots are tried before the release ([release-engineering.md](release-engineering.md)). |
| 15.2 (planned for December 2026) | three months after the next 15.x release | Supported from its release. |
| 15.1 (June 16, 2026) | March 31, 2027 | Supported. A 15.1 VM is the gate of the continuous integration. |
| 15.0 | September 30, 2026 | Not supported. `jailops doctor` fails below 15.1. |
| 14.x and older | | Not supported. `jailops doctor` fails below 15.1. |

| Part | Supported |
|---|---|
| Jail images of FreeBSD | one image per supported release (`freebsd-15-1`, `freebsd-15-2`); a jail runs on a host of the same or a newer release ([upgrade.md](upgrade.md#an-image-is-one-release)) |
| Linux jails (Alpine, Debian) | as in [linux-jails.md](linux-jails.md), on a supported host |
| amd64 | built, packaged and run in the end-to-end and packet filter tests of every change |
| arm64 | built and vetted for every change and packaged for every release (the package ABI is `FreeBSD:15:aarch64`); the VM test suites run on amd64 |

## The rule

**A release is supported while FreeBSD supports it, and never below 15.1.**

FreeBSD's policy, on its security page: "Each minor release is only supported for three months after the next minor
release within the same major version." The last minor release of a major is supported until the stable branch ends, and
from FreeBSD 15 on a stable branch is supported for four years from its dot-zero release (`stable/15` until
December 31, 2029). So at any time the supported releases of a major are its newest minor release and, for three months
after a newer one appears, the one before it.

## What the words mean

- **Supported:** a bug found on that release is fixed, the continuous integration covers it, and `jailops doctor` passes.
- **Works, not supported:** a release that FreeBSD has ended, or a snapshot. The packages install, because the package ABI
  is the major version (`FreeBSD:15`), but nothing tests it and nothing is fixed for it alone.
- **Not supported:** below 15.1. `jailops doctor` fails (`host.release`): JailOps relies on pkgbase and on the pf and VNET
  behaviour of 15.1.

`jailops doctor` checks the release it runs on: `host.release` fails below 15.1 and says OK for every newer release. It does
not yet warn about a release newer than the newest one tested.

## When FreeBSD ends a release

1. The continuous integration stops running it and its row moves to "works, not supported".
2. The packages keep installing on it.
3. Its image gets no further patch levels, because the patch levels of an image are what FreeBSD publishes for the
   release ([upgrade.md](upgrade.md#an-image-is-one-release)).
4. Hosts and jails move on as below.

## Moving between releases

| What moves | How |
|---|---|
| The host | With the tool the host uses: `freebsd-update` on a classic install, `pkg` on a pkgbase install. Upgrade the host before any jail: a jail runs on the host's kernel ([upgrade.md](upgrade.md#the-hosts-freebsd)). |
| The patch levels of a release | `jailops image upgrade freebsd-15-1`, or the nightly auto upgrade. It stays inside the release of the image ([upgrade.md](upgrade.md#nightly-auto-upgrade)). |
| A jail to a new release | `jailops jail upgrade web --image freebsd-15-2` (after `jailops image create freebsd-15-2 --release 15.2-RELEASE --build`), or `jailops image upgrade freebsd-15-1 --to freebsd-15-2` for every jail of an image: the data, volumes and addresses stay, one snapshot is the way back. A thick or Linux jail is updated by hand inside the jail ([thick-jail-upgrades.md](thick-jail-upgrades.md)), or replaced by a new jail on the new image ([upgrade.md](upgrade.md#a-new-release-move-the-jails)). |

Try the new release with one jail first, and keep the old image until no jail uses it.
