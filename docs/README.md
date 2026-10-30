# JailOps documentation

JailOps targets **FreeBSD 15.1 and later**. These pages are for the person who installs and runs it. Each one says what
it is sure of and marks what has not been run on a real host as UNVERIFIED; [design.md](design.md) section 16 is the
register of what is settled and what is open.

| Read | When |
|---|---|
| [install.md](install.md) | A new host: packages, `jailops-priv init`, the three `pf.conf` lines, forwarding, the first jail |
| [configuration.md](configuration.md) | Every key of `jailopsd.yaml`, `priv.conf`, `backupd.yaml` and the client file |
| [exposure.md](exposure.md) | Exposing the API: local only, behind a proxy or Cloudflare, direct with TLS and mTLS; certificates; the web console, off by default |
| [ports.md](ports.md) | Every port and socket of the family (39110 to 39112): which process, what for, what to open |
| [security-model.md](security-model.md) | What the unprivileged daemon and the root helper may and may not do, and what you decide |
| [networking-ipv6.md](networking-ipv6.md) | Networks, routed and switched IPv6, the NDP proxy, the PF isolation |
| [wireguard.md](wireguard.md) | WireGuard tunnels: a server, peers enrolled with one command and a join token, clients, where the keys live |
| [storage.md](storage.md) | Storages on big pools, volumes, host mounts, quotas, what is and is not backed up |
| [backup-restore.md](backup-restore.md) | The backup node, the key and its escrow, policies, verification, restoring |
| [upgrade.md](upgrade.md) | Upgrading JailOps, upgrading and rolling back jails, managed updates |
| [troubleshooting.md](troubleshooting.md) | `jailops doctor` checks by id, `jailops explain` codes, common failures |
| [linux-jails.md](linux-jails.md) | Alpine and Debian jails under the Linuxulator: limits and prerequisites |
| [fleet-sync.md](fleet-sync.md) | A git repository of YAML across hosts, what it can declare, secrets with age and SOPS, rollouts |
| [provisioning.md](provisioning.md) | A fresh VM to a running fleet; the `provision:` step of a jail |
| [apps.md](apps.md) | The application catalog: signed definitions of jails that run packages, the format, and how an installation will work |
| [appliance.md](appliance.md) | Optional host management from the console (appliance mode), module by module: off by default; host name, resolvers and NTP are built, the rest is design |
| [api.md](api.md) | The API for programs: the SDK, authentication, and where the protocol is written down |
| [../examples/README.md](../examples/README.md) | Runnable fleets: IPv6, a web stack with backups, a big pool, two hosts, provisioning |
| [design.md](design.md) | The contract: principles, layout, networking, API, backup, fleets, and the verification log |
| [../sdk/PROTOCOL.md](../sdk/PROTOCOL.md) | The wire protocol the SDK, the fake server and the daemon implement |

The command line documents itself: `jailops --help`, `jailops <command> --help` and `jailops explain CODE`.
