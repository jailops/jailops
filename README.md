<p align="center"><img src="docs/brand/logo-horizontal-light.svg" alt="JailOps" width="380"></p>

# JailOps

**The open-source, ZFS-native management platform for FreeBSD jails** ([jailops.net](https://jailops.net)). A REST API, a CLI,
web consoles and a Go SDK. No virtual machines, no hypervisor, no OCI containers: jails only.
JailOps targets **FreeBSD 15.1 and later**; older releases are not looked at, and `jailops doctor` says so.

What is available today and what comes next: [the roadmap](https://jailops.net/status/).

- Every jail an independent system on its own ZFS root, block-cloned from a pkgbase image and upgraded in place while it runs
- Routed VNET networks with deterministic IPv4 and IPv6 allocation, one link per jail. No DHCP, no radvd, no SLAAC
- PF anchors per jail
- Encrypted, deduplicated backups to a keyless backup node, with restore and restore tests
- Local-first: every host has its own API and console. A hub manages many hosts; cluster mode is on the roadmap
- `jailops top`: the live CPU, memory, disk and network of the host and its jails, in the terminal and the console
- Declarative fleets with `jailops-sync`
- One command on many jails (`jailops exec -l role=web -- uptime`) and files in and out of a jail (`jailops cp`), through the daemon, audited
- Alpine and Debian jails through the Linuxulator
- Real IPv6 for every jail, routed or on a shared switched /64 with a built-in neighbor discovery proxy
- An unprivileged daemon with a small root helper that the operator's `priv.conf` allow-lists bound

```
just build          # native binaries in bin/
just build-freebsd  # cross-build for FreeBSD
just test
```

License: BSD-2-Clause.

## Install (FreeBSD)

    pkg install jailops                    # from pkg.jailops.net: add the repository first, see "Packages" below
    jailops-setup                          # as root: init, forwarding, host resolver, pf.conf, service, doctor

`jailops-setup -n` shows what it would do; it asks before it changes pf. Afterwards the CLI works as root over the local
socket: no user, token or browser is needed. The web console is optional (`jailops-setup --web`, port 39110). A host run
from the web console alone is the optional [appliance mode](docs/install.md#appliance-mode-host-managed-by-jailops-set-up-from-the-web-console).
What the setup does step by step, and how to do it by hand, is in [docs/install.md](docs/install.md).

### Packages

`pkg.jailops.net` is a signed FreeBSD 15.1+ package repository (amd64 and aarch64) built with
[PkgOps](https://github.com/jailops/jailops-pkgops) by [jailops-packages](https://github.com/jailops/jailops-packages), which also
explains how to trust it (`/usr/local/etc/pkg/repos/jailops.conf` and the public key, checked against the fingerprint in
that README). The JailOps packages (`jailops-client`, `jailops`, `jailops-backupd`) are published there after each
release, signed, for amd64 and arm64. You can also build from source (`just build-freebsd`), or take the static
binaries from the GitHub releases page. The client package ships bash, zsh and fish completion for `jailops` and
`jailops-sync` under `/usr/local/share`; a hand-installed binary prints it with `jailops completion SHELL`.

## Read next

| For | Read |
|---|---|
| What is installed and how to start | [docs/install.md](docs/install.md) |
| Every key of the configuration files | [docs/configuration.md](docs/configuration.md) |
| What the daemon and the helper may and may not do | [docs/security-model.md](docs/security-model.md) |
| IPv6, routed and switched | [docs/networking-ipv6.md](docs/networking-ipv6.md) |
| Big pools, volumes and existing data | [docs/storage.md](docs/storage.md) |
| Moving a jail to another host or project as a file | [docs/export-import.md](docs/export-import.md) |
| Backups and restores | [docs/backup-restore.md](docs/backup-restore.md) |
| Updating FreeBSD, the host and the jails: the runbooks, the checks, the way back | [docs/lifecycle.md](docs/lifecycle.md) |
| Every night with a periodic script, and the move to the next release | [docs/automating-updates.md](docs/automating-updates.md) |
| Upgrading JailOps and its jails: the mechanisms | [docs/upgrade.md](docs/upgrade.md) |
| Which FreeBSD releases are supported | [docs/supported-versions.md](docs/supported-versions.md) |
| What 1.0 promises and what it does not | [docs/1.0.md](docs/1.0.md) |
| Which releases work together, and the rules for 1.x | [docs/compatibility.md](docs/compatibility.md) |
| When something is wrong | [docs/troubleshooting.md](docs/troubleshooting.md) |
| Alpine and Debian jails | [docs/linux-jails.md](docs/linux-jails.md) |
| Fleets from git | [docs/fleet-sync.md](docs/fleet-sync.md) |
| Running commands on many jails and copying files | [docs/operations.md](docs/operations.md#one-command-on-many-jails) |
| The application catalog and installing an app with one command | [docs/apps.md](docs/apps.md) |
| How a new FreeBSD release is handled (for maintainers) | [docs/release-engineering.md](docs/release-engineering.md) |
| The design contract | [docs/design.md](docs/design.md) |
| The wire protocol | [sdk/PROTOCOL.md](sdk/PROTOCOL.md) |

Questions, bugs and contributions: the issue tracker of the repository; the website, [jailops.net](https://jailops.net),
has the same material as a guided tour.

## Acknowledgements

JailOps builds on ideas and code from `incus-backup` and `incus-sync`, two earlier projects by the same
author: the encrypted, deduplicated chunk repository behind JailOps backups, and the declarative fleet
workflow behind `jailops-sync`. They are adapted here for FreeBSD jails and ZFS.

## Go modules

The import paths are served by jailops.net (a `go-import` page that points to the repository):
`jailops.net/jailops` (daemon, CLI and internals) and `jailops.net/jailops/sdk` (the stdlib-only client SDK).
