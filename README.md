<p align="center"><img src="docs/brand/logo-horizontal-light.svg" alt="JailOps" width="380"></p>

# JailOps

**The open-source, ZFS-native management platform for FreeBSD jails** ([jailops.net](https://jailops.net)). A REST API, a CLI,
web consoles and a Go SDK. No virtual machines, no hypervisor, no OCI containers: jails only.
JailOps targets **FreeBSD 15.1 and later**; older releases are not looked at, and `jailops doctor` says so.

What is available today and what comes next: [the roadmap](https://jailops.net/status/).

- Thin jails as ZFS clones of immutable, upgradable pkgbase image generations
- Managed VNET bridges with deterministic IPv4 and IPv6 allocation. No DHCP, no radvd, no SLAAC
- PF anchors per jail
- Encrypted, deduplicated backups to a keyless backup node, with restore and restore tests
- Local-first: every host has its own API and console. A hub manages many hosts; cluster mode is on the roadmap
- Declarative fleets with `jailops-sync`
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
    jailops-priv init -pool zroot          # the user, the datasets and the configuration
    sysrc jailops_priv_enable=YES jailopsd_enable=YES jailopsd_web=YES    # jailopsd_web: the web console on :39110
    service jailops_priv start && service jailopsd start
    jailops doctor
    jailops user create admin --admin --password-prompt    # then sign in at https://HOST:39110/

### Packages

`pkg.jailops.net` is a signed FreeBSD 15.1+ package repository (amd64 and aarch64) built with
[PkgOps](https://github.com/jailops/jailops-pkgops) by [jailops-packages](https://github.com/jailops/jailops-packages), which also
explains how to trust it (`/usr/local/etc/pkg/repos/jailops.conf` and the public key, checked against the fingerprint in
that README). The JailOps packages (`jailops-client`, `jailops`, `jailops-backupd`) are published there after each
release, signed, for amd64 and arm64. You can also build from source (`just build-freebsd`), or take the static
binaries from the GitHub releases page.

## Read next

| For | Read |
|---|---|
| What is installed and how to start | [docs/install.md](docs/install.md) |
| Every key of the configuration files | [docs/configuration.md](docs/configuration.md) |
| What the daemon and the helper may and may not do | [docs/security-model.md](docs/security-model.md) |
| IPv6, routed and switched | [docs/networking-ipv6.md](docs/networking-ipv6.md) |
| Big pools, volumes and existing data | [docs/storage.md](docs/storage.md) |
| Backups and restores | [docs/backup-restore.md](docs/backup-restore.md) |
| Upgrading JailOps and its jails | [docs/upgrade.md](docs/upgrade.md) |
| When something is wrong | [docs/troubleshooting.md](docs/troubleshooting.md) |
| Alpine and Debian jails | [docs/linux-jails.md](docs/linux-jails.md) |
| Fleets from git | [docs/fleet-sync.md](docs/fleet-sync.md) |
| The contract and what is verified | [docs/design.md](docs/design.md) |
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
