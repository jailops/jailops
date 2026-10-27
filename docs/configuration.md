# Configuration

Four files. Each rejects keys it does not know, so a typo is an error, not a silent default. The annotated samples under
`packaging/` list every key, and a test keeps `packaging/jailopsd.yaml` equal to what the daemon accepts.

| File | Read by | Owner and mode |
|---|---|---|
| `/usr/local/etc/jailops/jailopsd.yaml` | the daemon `jailopsd` | written by `jailops-priv init`, mode 0640, group `jailops` |
| `/usr/local/etc/jailops/priv.conf` | the root helper `jailops-priv` | root, 0600 or 0644; **the helper refuses a file others can write** |
| `/usr/local/etc/jailops/backupd.yaml` | the backup node `jailops-backupd` | see [backup-restore.md](backup-restore.md) |
| `~/.config/jailops/config.yaml`, `/usr/local/etc/jailops/client.yaml` | the `jailops` client | the user; keep it private, it can hold tokens |

## jailopsd.yaml

Only `pool` has no default. Sample: `packaging/jailopsd.yaml`.

| Key | Default | Meaning |
|---|---|---|
| `role` | `host` | the only role of this daemon; the backup node is a separate program |
| `hub` | `false` | keep a registry of remote hosts and forward `?host=` requests to them over pinned mTLS |
| `pool` | none | the ZFS pool |
| `dataset` | `<pool>/jailops` | the storage root dataset |
| `mount_base` | `/jailops` | where the root is mounted |
| `state_dir` | `<data_dir>/state` | the host's configuration objects. **Outside the storage root**, because the root is root-owned and the daemon cannot write there |
| `data_dir` | `/var/db/jailops` | SQLite database, seal key, `secrets.key` (the age identity for sealed secrets), TLS material, operation journal |
| `conf_dir` | `/usr/local/etc/jailops/jails` | rendered `jail.conf` files; must equal `conf-dir` of `priv.conf` |
| `work_dir` | `/var/tmp/jailops` | scratch |
| `listen.unix.path`, `.mode` | `/var/run/jailops/jailops.sock`, `0666` | the local socket. Only root (uid 0) is recognised there, as a global admin; every other local user is anonymous like an unauthenticated remote client, so the mode confers nothing. An empty path disables it. The former keys `admin_group`, `user_group` and `user_projects` are removed: a configuration that still sets one stops the daemon with a message |
| `listen.https.address` | empty | `":39110"` or `"127.0.0.1:39110"` enables the network listener: **TLS 1.3 only**, there is no plain-http TCP listener (mTLS pinning, API tokens, the web console). Checked at load |
| `listen.https.names` | none | extra DNS names and IPs of the generated certificate |
| `listen.https.cert`, `.key` | generated | your own certificate and key, absolute paths, **both or neither**; both must exist (the daemon never generates over a configured path); a renewed pair is picked up without a restart (checked at most every 15 s, a broken one is ignored and logged). Default `<data_dir>/tls/server.{crt,key}`, generated self-signed on first start |
| `web.enabled` | `false` | `true` serves the embedded web console on the listeners (the API is unchanged); it is always in the binary. The same switch without editing the file: `jailopsd_web="YES"` in `/etc/rc.conf`, or the `-web` flag, which also opens the TLS listener on 39110 when none is configured (`"NO"` and `-no-web` force it off). `go build -tags noweb` leaves the console out of the binary. See [exposure.md](exposure.md) |
| `priv.socket` | `/var/run/jailops/priv.sock` | where the helper listens |
| `log.level`, `log.format` | `info`, `text` | `debug`, `info`, `warn`, `error`; `text` (key=value lines for syslog) or `json` |
| `reconcile.interval`, `.autostart` | `60s`, `true` | the loop that makes the live host equal the desired state; start jails with `boot.autostart` on the first pass |
| `zfs.compat_file`, `.compat_dir` | empty | an OpenZFS compatibility file name (for example `openzfs-2.2-freebsd`) to compare pool features with an older replication target; empty skips the check |
| `limits.max_body_bytes`, `.request_timeout` | `4194304`, `60s` | request bounds |
| `limits.auth_failure_threshold` | `5` | failed authentications from one source before it is locked out |
| `limits.exec_sessions_per_jail`, `.exec_idle`, `.exec_max` | `8`, `30m`, `12h` | exec and console session bounds |
| `network.sysctl` | the three below | the bridge sysctls the daemon sets at run time through the helper and re-asserts: `net.link.bridge.pfil_member: 1` (must stay 1), `pfil_bridge: 0`, `pfil_onlyip: 1`. Never written to `/etc/sysctl.conf` |
| `projects.network_pool`, `.network_bits`, `.uplink`, `.nat` | none | the IPv4 pool `project create NAME --new-network` carves a `/24` from; without it that option answers `unsupported` |
| `updates.tick`, `.project_files_dir` | `1m`, `<state_dir>/project-files` | how often update schedules run; where provisioning files live, one directory per project |
| `lifecycle.persistent` | the built-in list | the paths of a thin jail that survive a generation switch, each in its own dataset (`dataset`, `path`, `class` system or data, `merge`, `no_seed`). The default is `/etc`, `/usr/local`, `/var/db`, `/var/log`, `/var/mail`, `/var/spool`, `/home`, `/root`. **Setting it replaces the list** |
| `backup.*` | absent | host-side backups; absent, every backup call answers `unsupported`. See [backup-restore.md](backup-restore.md) |

The proxy for switched IPv6 has no switch in this file: it runs for every network in `proxy` mode, and what it may do is
decided in `priv.conf` ([networking-ipv6.md](networking-ipv6.md)).

## priv.conf

One `key = value` per line; the keys are the helper's command-line flags without the dash, and lists may be repeated or
comma separated. Check it with `jailops-priv check-config` (or `service jailops_priv checkconfig`); reread the storage
file with `service jailops_priv reload`. Sample: `packaging/priv.conf.sample`.

This file is the **operator's authority**: everything the root helper will do for the daemon is bounded by it. See
[security-model.md](security-model.md).

| Key | Meaning |
|---|---|
| `storage-root` | a ZFS dataset that exists, is on an imported pool and carries `jailops:managed=on`. Everything the helper does with ZFS, mounts and files stays below one. Repeatable; more are registered at run time (admin only) in `storage-file` (`/usr/local/etc/jailops/priv.d/storage.conf`) and reread on SIGHUP |
| `allow-pool-root` | `false`; a pool's top dataset as a root is refused unless you say so |
| `conf-dir`, `state-dir`, `work-dir`, `mount-base`, `helper-path` | directories; `state-dir` has no default unless `mount-base` is set |
| `socket`, `group`, `mode`, `allow-user`, `allow-group` | who may talk to the helper: the unix socket, its group and mode (`0660`), and the users and groups it accepts by peer credentials |
| `if-prefix` | `jo`; the prefix of the interfaces JailOps creates (`jo7`, `jo7ga`, `jo7gb`, epairs) |
| `ndp-proxy` | `<uplink>=<prefix>[,<prefix>...]`, one line per uplink, prefixes between /48 and /120. The only thing that lets the IPv6 proxy through. Without it the helper answers `forbidden` with the line to add |
| `sysctl` | extra sysctls readable through the helper (the built-in list covers `kern.osrelease`, the bridge `pfil_*` switches, forwarding, `hw.ncpu` and a few more) |
| `admin-raw-param`, `admin-allow-flag`, `admin-devfs-ruleset`, `admin-host-mount`, `admin-zfs-prop`, `admin-routes`, `admin-raw-pf`, `admin-resume-send` | what a global admin may unlock; empty or off by default. **An admin who may set these is root on the host**: keep the lists short |
| `fetch-host` | the hosts the download rule may fetch from, https only (default `dl-cdn.alpinelinux.org`, `pkg.freebsd.org`) |
| `image-mirror` | directories `fs.read_file` may read besides the storage roots (a local mirror of image archives) |

## backupd.yaml

The backup node. It needs PostgreSQL (its control plane), a datastore (a directory or an S3 bucket) and a certificate.
The sample `packaging/backupd.yaml` is annotated: `hostname`, `listen` (default `:39111`), `admin_socket` (mode 0600, root only; the former `admin_group` key is removed and stops the node at start), `tls`,
`database.dsn_file`, `datastore` (`directory` or `s3`), `web`, `grace`, `deletion_delay`, `retention`, `schedule`.
`jailops-backupd -c FILE init` creates the repository and the certificate and prints the fingerprint that hosts pin.

## The client file

`~/.config/jailops/config.yaml` (override with `--config` or `JAILOPS_CONFIG`) and the host-wide
`/usr/local/etc/jailops/client.yaml` hold named remotes (`url` or `socket`, `token` or `token_file`, `fingerprint`,
`cert`, `key`, `ca`, `host`, `project`), the current remote and, per remote, the **current project** that
`jailops project switch NAME` sets. That project is client-side state: the server has no default project. Precedence,
strongest first: the flags `--remote`, `--host` and `--project`; the environment (`JAILOPS_REMOTE`, `JAILOPS_URL`,
`JAILOPS_TOKEN`, `JAILOPS_FINGERPRINT`, `JAILOPS_CERT`, `JAILOPS_KEY`, `JAILOPS_CA`, `JAILOPS_HOST`, `JAILOPS_PROJECT`,
`JAILOPS_SOCKET`, `JAILOPS_CONFIG`); the user file; the host file; the default socket
`/var/run/jailops/jailops.sock`. Prefer `token_file` (or `--token-stdin`) to a token on the command line: a command-line
value is visible to other users.
