# Changelog

## Unreleased

## 0.4.0 - 2026-10-30

- **Interfaces say what they are.** Every interface JailOps makes carries a short description that `ifconfig` shows: `jailops net=<network> bridge`, `jailops net=<network> gateway (bridge side)` and `(host side)`, `jailops jail=<project>/<jail> net=<network> (host side)` and `(jail side)` (the jail end is described from the host inside the jail's vnet), `jailops wireguard=<name>`. At most 64 characters of `A-Za-z0-9 ._:/=()-`, built from names only, cut with `..` when a name is long (golden-tested). The reconcile loop repairs a missing or different description of an interface JailOps owns (a jail's epairs by their name, the jail end once per start; a network's bridge and gateway pair when they carry its text or the `jailops:<uuid>` marker of earlier releases, which is replaced) and touches no other; a bridge without a description is still not taken. Orphaned epairs are now matched to jails by name. `jailops doctor` reports `net.descriptions`. The helper's `net.set_description` accepts only `jailops ...` texts of that character set, and the command rules accept two more forms, `ifconfig -j <jail> <if>` (read) and `ifconfig -j <jail> <if> description <JailOps text>`. API extension `interface_descriptions`. UNVERIFIED on a FreeBSD host.
- **WireGuard tunnels** ([docs/wireguard.md](docs/wireguard.md)): a new kind `WireGuard` (`/api/v1/wireguard`, extension `wireguard`; operator reads, admin changes) for a server and its peers or a client. `jailops wireguard create NAME --address CIDR [--network NET] [--endpoint HOST:PORT]`, `peer add SERVER PEER [--address A] [--keepalive N] [--format token|conf]` (keys made on the host, the next free pool address, applied at once, a join token `jwg1.<base64url(JSON)>` printed once), `join TOKEN|-` (a client in one command), `peer list|show|remove`, `list`, `show`, `status` (handshake, transfer, endpoint per peer), `up`, `down`, `rotate-keys`, `edit`, `delete`; `--dry-run` everywhere. Private and preshared keys are made and kept by the helper in `wireguard-dir` (`/var/db/jailops-priv/wireguard`, root, 0700, files 0600) and never reach a command line, the API, the audit log or a log; the configuration reaches the kernel with `wg syncconf` of a 0600 file the helper removes. Seven typed helper operations (`wg.keys`, `wg.peer_keys`, `wg.import`, `wg.apply`, `wg.remove`, `wg.show`, `wg.tools`), the admin assertion for every change, `if_wg` on the module list. The reconcile loop recreates the interfaces after a reboot. A server with `networks` gets the pf anchor `jailops/wg.<name>`; the rule for the listen port stays in your `pf.conf` (printed as `firewall_rule`). `jailops doctor`: `wireguard.tools` (the wg program at `/usr/bin/wg` or `/usr/local/bin/wg`, `pkg install wireguard-tools`), `wireguard.forwarding`, `wireguard.firewall`. The host console has a WireGuard tab under Networks (create a server, add a peer with the token shown once and a `.conf` download, join by token, peers with their last handshake and transfer). Fleet sync declares servers and their peers' public parts (`hosts/<host>/wireguard/`). `scripts/ci-freebsd-e2e.sh` runs a server, a peer and a client on one host when `if_wg` and `wg` are there and says SKIP otherwise. UNVERIFIED on a FreeBSD host.
- **Who may connect to an installed app** (`docs/apps.md`, "Who may connect"). An app is **closed** after its installation: a new last firewall step, `close-firewall`, gives the jail the managed policy `app-<jail>` (`managed_by: app-access`, default deny, after the health check). Apps installed before stay open until their first allow, which closes them.
  - `jailops app allow APP from SOURCE [--port N[/proto]]... [--db NAME] [--rotate] [--dry-run]`, `jailops app deny APP from SOURCE [--drop-role] [--dry-run]` and `jailops app access [APP]`. SOURCE is `@jail`, `@project/jail` (needs the other project too), `@address-set`, an address or prefix, or `host` (the gateway addresses the host connects from); the shell completes them. One rule per protocol in the managed policy, rendered after the project's mandatory policy and before the jail's own; the firewall routes refuse to change or delete a managed policy (`forbidden`).
  - A definition may carry an optional `access:` block (`type: postgresql`, `port`, `service`, `hba_file`; strict, new keys are invalid for older daemons). For such an app an allow makes a role and a database (`--db`, default the source jail's name; `^[a-z][a-z0-9_]{0,62}$`), with `psql` run as `postgres` in the jail and the statements on stdin; the server gets a SCRAM-SHA-256 verifier of a generated 32-character password, never the password; a managed block of `pg_hba.conf` (`# jailops access begin`/`end`, one `host DB ROLE PREFIX scram-sha-256` line per address) is written through the helper and reloaded; `listen_addresses` is set once (one restart). The connection string `postgres://ROLE:PASSWORD@HOST:5432/DB?sslmode=disable` is in the answer once and stored nowhere; `--rotate` makes a new one. Deny removes the rule and the lines and takes the login of a role JailOps made (or drops it with `--drop-role`, its objects given to postgres, the database kept); a role JailOps did not make is never touched. The reference copy of `postgresql` has the block; the catalog's definition gets it in the packages repository.
  - New routes `GET`/`POST /api/v1/jails/{name}/access` and `POST .../access/deny` (readonly, operator, operator; audited; answered at once, not as operations), types `AppAccess`, `AppAllow`, `AppAllowResult`, `AppDeny`, `AppDenyResult`, `AppAccessDef`, state kind `AppAccess` (the registry of grants and of the roles JailOps made, no password), extension `app_access`, `Apps().Access`, `Allow`, `Deny` in the SDK and the fake, `SeedAppGrant`. Doctor reports `apps.access` (warn) when a grant's rule or `pg_hba.conf` line is missing or its source jail is gone.
  - The host console has an **Access** tab on the page of an installed app: the grants with their rules, anchor lines and `pg_hba.conf` lines, an Allow dialog (a searchable list of the project's jails, address sets, an address field, the host, a database name) that shows the connection string once with a copy button, and Deny with a confirmation.
  - `jailops-sync` declares apps (`App`) and their grants (`AppAccess`); see `docs/fleet-sync.md` and `examples/app-access-fleet`.
  - `scripts/ci-freebsd-e2e.sh`: every check that connects from the host first allows the host; the nginx block checks that the page is refused before; `JAILOPS_E2E_APPS=postgresql-access` installs PostgreSQL, allows one jail, connects from it with the printed string, is refused from another, and checks the closure after a deny. UNVERIFIED on a FreeBSD host.
- **Rate limits say they are not enforced**: a jail whose `nic` sets `ingress` or `egress` carries a warning in `status.warnings` ("stored but not enforced yet"), `jailops config set` prints it, and doctor reports `jails.rate_limits` with the new status `info` (counted as ok; `jailops doctor` prints `INFO`). No shaper was built.
- `scripts/ci-freebsd-pf.sh` phase 11 checks a port forward (`redirect` of a policy rule) from a client on the uplink's network to the jail, and that a port that is not forwarded stays closed. UNVERIFIED until it has run on a host.

- **Write your own app** (`docs/apps.md`, a new section with the whole loop and a worked example, memcached): try an app on a host, turn it into a definition, send it to the packages repository.
  - `jailops app new NAME [--package PKG[,PKG...]] [--service NAME] [--port N[/proto]] [--category C] [--dir DIR] [--force]` writes `DIR/NAME.yaml` offline: every key of the format with a short comment on what it does and the usual traps (`pkg -r` and the owners of a package user's directories, `ldconfig`, `sysvipc`, the start wrapper), prefilled from the flags, with `TODO` markers where a person decides. The file parses and validates as it is. It also writes `NAME.svg`, a neutral placeholder icon, unless one is there, and refuses to write over `NAME.yaml` without `--force`. `-o json` reports the files and the marked lines.
  - `jailops app export JAIL [--project P] [-o FILE] [--force]` drafts a definition from a running FreeBSD jail and writes it, commented, to `FILE` or the standard output (`-o` names the file here). The draft holds the packages installed on purpose, the services enabled in `rc.conf` and their knobs, what listens now (`sockstat`), the jail's volumes as `data`, the names of the files below `/usr/local/etc` and `/var/log` changed after the last package operation, and `setup` lines with the owners the app's directories have in the jail; the base system's services, ports and logs are left out. It never holds a secret: configuration files are listed by name only, a secret-looking knob is written as `TODO`, a knob with a shell expansion is left out. The comment at the top lists what was found and left out.
  - `jailops app validate` reports each line that still holds a `TODO` marker as `FILE: line N: replace TODO` (exit 2). The daemon does not look for them, so a draft installs while it is being written.
  - New route `GET /api/v1/jails/{name}/app-draft` (`AppDraft`: `jail`, `definition`, `todo`, `findings`; operator in the project, audited with counts only, `busy` for a stopped jail, `invalid` for a Linux jail; extension `app_draft`), `Jails().AppDraft` in the SDK and the fake (`SeedAppDraft`). The helper accepts exactly one more form, `pkg -r <jail root> query -e "%a = 0" "%n %v %R %w"` (a new `RestExact` list of a command rule), and refuses every other `pkg query -e`; the in-jail commands (`sockstat -4 -6 -l -q`, `find DIR -type f -newer /var/db/pkg/local.sqlite`, `stat -f "%Su:%Sg %N" PATH...`) go through the jail guard like every exec. UNVERIFIED on a FreeBSD host: the output of these commands in a jail.
- **Local apps**: `apps.local_dir` in `jailopsd.yaml` (default `/usr/local/etc/jailops/apps`) is read with the packaged catalog on each request, so a `NAME.yaml` (and its icon) dropped there is listed and installable at once, without a package. `GET /api/v1/apps` and `GET /api/v1/apps/{name}` carry `source` (`catalog` or `local`; additive) and `overrides: catalog` for a local definition that hides a packaged one of the same name, which wins until it is removed; an invalid local definition is listed as invalid with the reason and its hint points at `apps.local_dir`. `jailops app list --source local|catalog`, a `SOURCE` column, `(local)` in the short table and a `Source:` line in `app info`; `SeedLocalApp` in the fake. `jailops doctor` reports `apps.local_dir` when the directory exists (definitions, invalid ones, or that the daemon cannot read it). The host console shows a small **local** chip on local apps (the Apps page, the app's page with "overrides the catalog's definition", the Installed tab), and the demo has a local memcached.


- **Appliance mode, designed** (`docs/appliance.md`): JailOps may manage the host itself, one module at a time and only where the administrator opts in; light CLI-only use stays the default and nothing changes for hosts that do not. The document covers the opt-in model, drift, dry runs with a diff, snapshots and restore, boot environments, "commit confirmed" for network and firewall changes (the revert timer lives in the privileged helper), six modules in build order, a first-boot wizard and an installer image, and the security model. Only module 1 is built. `docs/design.md` has a principle and status rows for it, `docs/security-model.md` a pointer.
- **Host identity (module 1)**: the host name, `/etc/resolv.conf` and NTP, off by default. Enabling it takes both `host.manage.modules: [identity]` in `jailopsd.yaml` and `host-manage = identity` in `priv.conf`; with either missing a change answers the new error code `not_managed` (HTTP 403) with the lines to add, and reading still works.
  - API (extension `host_identity`): `GET`/`PUT /api/v1/host/identity` (kind `HostIdentity`), `GET /api/v1/host/snapshots`, `POST /api/v1/host/snapshots/{name}/restore`. A change needs a global admin (operator is not enough), is audited and can be a dry run that returns a unified diff and the list of actions. SDK `c.HostSystem().Identity()` (`Get`, `Apply`) and `c.HostSystem()` (`Snapshots`, `Restore`); the fake serves the routes (`fake.WithHostManaged()`).
  - Helper: four new operations. `host.identity.read` is read-only; `host.identity.apply` and `host.snapshot.restore` need the admin assertion and `host-manage`; `host.snapshot.list` lists. The helper takes validated fields, never a file: it renders `/etc/resolv.conf` (first line `# managed by jailops`) and the block between `# jailops begin` and `# jailops end` of `/etc/ntp.conf` (other lines untouched), sets only `hostname`, `ntpd_enable` and `ntpd_sync_on_start` with `sysrc -f /etc/rc.conf`, runs `hostname` and `service ntpd restart|onestop`, writes atomically, keeps a copy of every file it changes first in `host-dir` (new `priv.conf` key, default `/var/db/jailops-priv/host`, root only, the last 20 snapshots) and puts it back when a step fails. A restore keeps a copy of what it replaces. Refusal tests cover every field.
  - CLI: `jailops host show` (without a name: this host), `jailops host identity set [--hostname] [--nameserver]... [--search]... [--ntp-server]... [--ntp enable|disable] [--dry-run]`, `jailops host snapshots`, `jailops host restore [SNAPSHOT]` (snapshot names complete).
  - Doctor: `host.identity` (not managed, or drift from what was applied) and `host.ntp` (ntpd enabled and synchronised).
  - Console: a Host page (`/host`, shown when the server has the extension): the identity form, the managed state with the exact lines to enable, Preview (the diff) before Apply, snapshots with Restore, a drift badge.
  - `scripts/ci-freebsd-e2e.sh`: with `JAILOPS_E2E_HOST=1` (off by default, CI does not set it) it enables the module, applies a host name and documentation-range resolvers, checks hostname(1), the files and `sysrc`, restores and checks the originals byte for byte. UNVERIFIED on FreeBSD until that block has run.

## 0.3.2 - 2026-10-30

- **The application catalog is released on its own.** The definitions and icons left this repository: they live in the packages repository (`main/jailops-apps/apps/` of `jailops/jailops-packages`), and the `jailops-apps` package has a version of its own, so a new or changed app is one pull request there and no JailOps release. The `jailops` package depends on `jailops-apps` by name, any version. `packaging/apps/` is gone; the tests, the console's demo notes and `scripts/ci-freebsd-e2e.sh` use a reference copy in `internal/apps/testdata/catalog/` (refreshed by hand when the format changes; `JAILOPS_E2E_CATALOG` names another directory for the end-to-end test). `docs/apps.md` describes the pull request flow.
- **`jailops app validate PATH...`** checks catalog definitions on the machine it runs on, without a server, a network or root: a catalog directory (every `*.yaml`, as the daemon lists them) or single files, parsed and validated with the daemon's own code, and the icon each one names must be next to it and readable. Every problem is printed as `FILE: reason`; the exit status is 2 when there is one, 0 with a one-line summary otherwise; `-o json` and `-o yaml` print the counts and the problems. The CI of the packages repository runs it before it builds `jailops-apps`. The client now links `internal/apps` for this; a test keeps that package and what it imports free of daemon code (the installer's rc.conf variables are its own type now, converted to the renderer's).
- A definition written for a newer daemon (an unknown key, format version or category) is listed as invalid with the reason, the others are unaffected: that was the behaviour, a test of the catalog listing now checks it and `docs/apps.md` states it, since a catalog update can reach a host before the JailOps update it needs.
- **Three new apps in the catalog** (now in the packages repository, each with an icon of its own; `docs/apps.md` has a table of the eight apps with their ports, health checks and where their facts come from). The three install and run on a FreeBSD host in the end-to-end test (`JAILOPS_E2E_APPS`); what is not yet checked is listed in `docs/apps.md`.
  - `isms` (category security): the information security management platform from the JailOps package repository (`pkg -r ROOT install -y -r jailops isms`; the host's pkg configuration must have the `jailops` repository). It is enabled but not started and nothing is generated: it needs a PostgreSQL database and a master secret first. The setup gives `/var/db/isms`, `/var/db/isms/private` and `server.env` back to `isms` with modes 0750, 0700 and 0600. The notes are the manual steps (role and database in a `postgresql` jail, `server.env`, the secret, the signing key, `isms server migrate`, `service isms start`) until `jailops app allow` connects apps. Port 8080 answers in the end-to-end test; what it answers is UNVERIFIED, so the definition has no health check, because nothing is started.
  - `nsd` (category network): the authoritative DNS server NSD on 53/udp and 53/tcp, with a starter configuration and one zone, `example.com`, on the volume `/var/db/nsd`, checked with `nsd-checkconf` and `nsd-checkzone` before the start; health tcp 53. The notes have the lines for a primary and for a secondary with zone transfers.
  - `mail` (category communication): a starting point for a single-domain mail server, Postfix (25, 587) and Dovecot 2.3 (143; 993 once TLS is set), system users with their homes on the volume `/home/mail`, the queue on `/var/spool/postfix`, `sendmail_enable="NONE"` and a `mailer.conf` that hands `/usr/sbin/sendmail` to Postfix, `postfix set-permissions` after `pkg -r`; health tcp 25. The notes cover users, the domain, TLS and the DNS records (MX, SPF, DKIM, DMARC).
- App categories `network` and `security` (the API's `AppCategory`, `jailops app list --category`, the console's category chips).
- `health` was always optional in a definition, and `docs/apps.md` now says so; the starter catalog leaves it out only for an app that starts none of its services. The catalog test (on the reference copy) checks that rule, the repositories, nsd's UDP and TCP ports, mail's services and `mailer.conf`, and the owners the setup lines give; a new daemon test dry-runs `isms`, `nsd` and `mail` and installs `isms` through the fakes (from the `jailops` repository, no service started).
- The fake server of `sdk/fake` starts with six definitions (with `isms`, `nsd` and `mail`), and its install steps, like the console's demo, name one `install-packages-<repo>` step per repository as the daemon does.
- `scripts/ci-freebsd-e2e.sh`: `JAILOPS_E2E_APPS` accepts `nsd` (`drill` for the SOA of `example.com` over UDP and TCP, `nsd-checkconf`), `mail` (the SMTP and IMAP greetings, `mailer.conf`) and `isms` (a `postgresql` jail `isms-db`, the steps of the notes, port 8080 answering); with `isms` and no `jailops` repository on the host the script stops at once with the reason.

## 0.3.1 - 2026-10-30

- The rollback command that the app install report, the console's install dialog and a failed package update show is now one the CLI accepts: `jailops jail rollback NAME --all --snapshot SNAPSHOT`. It used to say `--scope all`, which is not a flag. A test parses the printed command with the real command tree.
- The Valkey definition's summary is now "In-memory key-value store under the BSD license.", and its description no longer names another project.
- `jailops app install NAME` without `--image` uses the only FreeBSD image with the alias `stable`, as `NAME@stable`. It used to use `freebsd@stable`, which does not exist on a host that followed the quick start (`freebsd-15`). With no such image, or more than one, the request is refused before the operation, and the hint lists your images and says to name one with `--image`.
- **System V IPC for a jail**: the new jail key `sysvipc` (`spec.sysvipc`, `jailops config set NAME sysvipc=true`, allowed in a profile, applied at the next start) renders `sysvmsg = new; sysvsem = new; sysvshm = new;` into the jail's `jail.conf`: System V message queues, semaphores and shared memory of the jail's own, never the host's. An app definition asks for it with `sysvipc: true` (a strict boolean); the PostgreSQL definition does, because `initdb` failed with "Failed system call" on `shmget` in a jail without it. The helper accepts exactly `= new` for the three parameters. UNVERIFIED on a FreeBSD host.
- **A daemon started by a jail's rc no longer holds the caller's pipes**: every `jail.conf` sets `exec.consolelog = "/var/log/jail_${name}_console.log"`, so the output of `/etc/rc`, `/etc/rc.shutdown` and the hooks goes to that file on the host (FreeBSD and Linux jails). A forgejo jail did not start any more ("WaitDelay expired before I/O complete"): the daemon(8) supervisor of its rc script kept the pipes of `jail -c` open. A failing `jail -c` or `jail -r` names the file in its hint. The command runner also treats a command that exited 0 while a leftover process still held its output as a success. UNVERIFIED on a FreeBSD host.
- `jailops app install` and the provisioning `services.start` step start a service with a fixed wrapper, `/bin/sh -c '<constant script>' jailops-start NAME`, which sends the rc script's output to a temporary file in the jail and prints it only when the start fails; the service name is an argument, never shell text, and is checked before the command is built. The install step's error names `/usr/sbin/service NAME start` with that output. Forgejo's `service forgejo start` used to wait for ever. `setup` and `health.exec` commands stay plain argv.
- The Valkey definition creates `/var/log/valkey` and `/var/run/valkey` if needed and gives them, with `/var/db/valkey`, to `valkey:valkey`: `pkg -r` installs from the host, where the package's user does not exist, so its directories came out root-owned and the server could not open its log file. `docs/apps.md` explains the limitation (pkg's database records no owners, so they cannot be restored generically) and what a definition must do; a test checks the owners the starter catalog sets.
- `scripts/ci-freebsd-e2e.sh`: `JAILOPS_E2E_APPS="postgresql valkey prometheus forgejo"` (any of them) installs those apps after nginx, each in its own block, with a check of its own (`select 1`, `PONG`, `/-/ready`, port 3000 answering 200 or a redirect), lists the jail's log files and stops it. CI does not set it.

## 0.3.0 - 2026-10-29

- **The logs of a jail are read through the API**: `jailops jail logs NAME` lists the files below `/var/log` of the jail's root (rotated copies included; compressed ones are listed but not readable) and `jailops jail logs NAME FILE` prints the last lines of one as plain text, with `--tail N`, `--since 15m` (lines whose timestamp is not older; a line without one belongs to the preceding line), `--grep TEXT` (a plain substring) and `-f` to follow, which continues at the server's `next_offset` and starts over when the file is rotated; `-o json` prints the reply. The top-level `jailops logs NAME [FILE]` is the same command. It works for a running jail and for a stopped one whose root is mounted; otherwise it answers `busy: jail "x" is not mounted` with the dataset to mount. New routes `GET /api/v1/jails/{name}/logs` and `GET .../logs/{file}` (extension `jail_logs`, `Jails().Logs` and `Jails().Log` in the SDK and the fake), operator role (logs hold secrets; the role that may exec), audited once per identity, jail and file every five minutes. The helper gained two read-only operations, `fs.list_logs` and `fs.tail_file`, that accept only the log directory of a jail root and plain names below it, follow no symlink below the jail root, read regular files only and at most 1 MiB per call.

- `jailops jail logs NAME` used to stream the log events the server publishes; that is now `jailops jail logs NAME --events` (with `--since-id` and `--count` as before).

- **The OS upgrade of a jail works through the privileged helper**: the `/etc` merge of `jail upgrade`, `update apply` and `image upgrade` makes two archives with `tar -cf`, which the helper refused (only listing and extracting were allowed), so the upgrade stopped at the merge step on a real host and rolled the jail back. The helper now accepts exactly `tar -C <root> -cf <.merge-*>/old-etc.tar|new-etc.tar ./etc`. The FreeBSD end-to-end test now upgrades a jail to a new generation, checks that its data survived and rolls the root back.

- `jailops doctor` checks `net.inet6.icmp6.nd6_onlink_ns_rfc4861` (`sysctl.net.inet6.icmp6.nd6_onlink_ns_rfc4861`) while a network in IPv6 proxy mode exists: the routers of a shared segment probe the host's address from their global addresses, FreeBSD's default drops those solicitations as off-link, and the router marks the host unreachable for minutes every few hours. The check warns that the setting is recommended, with the command and the `/etc/sysctl.conf` line; `sysctl.persistent` warns until the line is there. JailOps never sets it. The helper's read allow-list has the new name.

- `network.ndproxy` warns instead of failing while the capture of the uplink is being opened (`Starting`, the first minutes after `jailopsd` starts); a capture that cannot be opened, a failed sync or a stopped proxy still fail. The reconcile loop looks at a proxy it just started once more after the debounce instead of a whole interval later.

- `jailops network create --ipv6-mode proxy` (and from a file, and `apply`) warns, in the answer and on the client's standard error, when the sysctl above is 0 on the host. The warning is in `status.warnings` of the answer only; it is never stored.

- **The application catalog** (`docs/apps.md`): a definition of a jail that runs packages (packages, rc.d services and knobs, ports, data paths, configuration files, logs, setup commands, health check, resource hints, notes), one strict YAML file per app, installed on the host by the signed `jailops-apps` package under `/usr/local/share/jailops/apps` (`apps.catalog_dir` in `jailopsd.yaml`). The daemon reads these local files only and never fetches anything; a definition that does not validate is listed as invalid with the reason and can never be installed. New routes `GET /api/v1/apps` and `GET /api/v1/apps/{name}` (any authenticated identity, extension `apps`), `Apps().List/Get` in the SDK and the fake, `jailops app list` (patterns, `--category`, `-c`, `--sort`, every output format) and `jailops app info NAME`. The starter catalog in `packaging/apps/` has `postgresql`, `nginx`, `valkey`, `forgejo` and `prometheus`, written against the FreeBSD 15 quarterly packages.

- **Installing an app from the catalog** (`docs/apps.md`): `jailops app install NAME [--name JAIL] [--network N] [--storage S] [--image I] [--memory M] [--disk D] [--cpu N] [--dry-run] [--no-wait]` creates a jail that runs an app as one operation: a thin jail from `freebsd@stable` with the definition's data paths as volumes and the labels `jailops.app` and `jailops.app.hash`, started, the packages installed from the host with `pkg -r` through the privileged helper (the installed version becomes `jailops.app.version`), the services enabled in the jail's `rc.conf` (written through the helper's file operations, every value escaped, no shell), the setup commands and the services run in the jail through the in-jail exec, the health check (`tcp` and `http` from the host, `exec` in the jail, retried for `wait_seconds`), and a snapshot `app-installed` of the fresh installation. The report says how to reach it, where the data, configuration and logs are, and the notes. A failure before the jail's first start deletes the jail again; after it the jail is left stopped for inspection with its labels, the error says so, and a daemon restart in the middle does the same from the journal. `--dry-run` returns the steps. `jailops app installed` lists the jails that came from the catalog (jail, app, version, state, addresses, ports). New route `POST /api/v1/apps/{name}/install` (operator in the project, extension `app_install`), events `app.installed` and `app.install.failed`, `Apps().Install` and `Apps().Installed` in the SDK and the fake. Ports marked `expose` are recorded only; forwarding them is a later feature. The FreeBSD end-to-end test installs `nginx` from the starter catalog and fetches its default page from the host. UNVERIFIED on a FreeBSD host until that run.

- `jailops app install`: while an installation holds a jail, `exec`, `shell` and console sessions into it are refused (`busy`, "the app is being installed"): `pkg -r` works on the jail root from the host as root and nobody else may change that tree meanwhile. `docs/security-model.md` describes what remains (the packages' scripts run as root chrooted into the jail root).

- The app catalog refuses two services whose names are the same in rc.conf (`foo-bar` and `foo_bar`), an `rc` key that sets the `_enable` of any service, and a `data` or `config` path that is one of the persistent datasets of every jail (`/var/db`, `/var/log`, ...; the installer also checks the host's `lifecycle.persistent`). `GET /api/v1/apps` reads no icons and at most 512 definitions; the first one beyond is listed as invalid.

- `jailops app install`: a dry run creates nothing (it made the project's datasets); a failure of the create step after the jail was made deletes the jail; a daemon restart during the start of the jail deletes it, as the documentation said; the health check ends within `wait_seconds` (and one attempt of at most 5 seconds) instead of counting attempts.

- The listing of a jail's logs reads at most 4097 entries of a directory and 64 subdirectories in the helper; a listing that was cut carries the header `X-Jailops-Truncated: true`. `jailops jail logs NAME FILE` replaces escape sequences and other control characters of the lines with U+FFFD (`-o json` is unchanged).

- `jailops doctor` warns (`jail.volumes`) about volume datasets with `canmount=on`, made before volumes were created with `canmount=noauto`, and lists `zfs set canmount=noauto <dataset>` for each.

- **The host console has Apps pages and a log viewer**: `/apps` is the catalog (search, category filters with counts, cards with icon or monogram, version and ports; invalid definitions with their reason), `/apps/NAME` the definition (packages, services, ports, data paths and their volumes, configuration files, logs, health check, resource hints, notes, hash) and the install dialog: jail name, network and storage from the project's grants, optional limits, a dry-run preview of the steps, then the operation with its steps ticked off from the operation's progress and a report (jail, addresses, endpoints, paths, notes, the `app-installed` snapshot and the rollback command; on a failure the step, the hint and the kept jail). The Installed tab lists the project's jails with the label `jailops.app`, with links to exposed web ports. A jail's new Logs tab lists its files below `/var/log` and shows the tail with line count, since and text filters, follow (once a second from `next_offset`, across rotations, paused while the browser tab is hidden), wrap, copy and download; control characters are stripped and lines are only ever shown as text. Install is offered to operators of the project; the reason is shown otherwise. All of it works in the demo mode (`?demo`).

## 0.2.1 - 2026-10-29

- **Images are built from the packages of their own release**: the base repository of a RELEASE image was written as `base_release_${VERSION_MINOR}` and pkg expanded the minor from the ABI it was given, which has none, so every image came from `base_release_0`: a 15.1-RELEASE image had a 15.0 userland (`uname -U` 1500068 against 1501000 on the host), and `pkg` in its jails refused newer packages. The minor of the image's release is now written into the URL, and the end-to-end test checks that a jail's userland matches the host's. Images built before this keep their 15.0 userland until the next generation is built from them (`jailops image build NAME --from gN`), which now reads the right repository.

- **An image can be upgraded as a whole**: `jailops image upgrade IMAGE` builds the next generation from the newest one and
  then moves every thin jail that uses the image to it, one jail at a time, through the same steps as `jail upgrade`
  (snapshot, new root, `/etc` merge, swap, health check, automatic root rollback), writing the same update history and
  events as `update apply`. It stops at the first jail that fails or needs a human, so a bad generation is not rolled across
  the host; the jails after it are listed as not attempted. When the package set did not change, no generation is kept and
  no jail is touched. Thick jails, Linux jails, other update strategies and pinned jails are skipped and listed; a stopped jail
  is moved and left stopped. `--project P` narrows it to one project, `--no-jails` builds only, `--dry-run` plans and lists.
  New route `POST /api/v1/images/{name}/upgrade` (global admin, extension `image_upgrade`), `Images().Upgrade` in the SDK
  and the fake, and an "Upgrade" button in the web console.

- **The `jailops` client acts on several jails at once**: `start`, `stop`, `restart` and `delete` take `-l key=value` (the labels of `list -l`) or `--all` instead of names; a bulk delete asks for the project name to be typed (or `-y`). `create --start` starts the new jail at once, `delete --force` stops a running jail first, and `--grace` takes a duration (`30s`, `1m30s`) as well as seconds.

- `jailops list` takes name patterns (`list 'web*'`), `-c name,state` chooses the columns, `--sort COLUMN` orders the rows, `-o csv` prints the table as CSV and `-o compact` the plain borderless table on a terminal too. The output of a pipe is unchanged. `list --state` refuses a state that does not exist and completes the real ones.

- `jailops apply -f FILE` creates or updates every document of a YAML or JSON file (jails, images, networks, projects, storage, firewall policies, address sets, backup targets and policies, profiles, hosts), leaves unchanged objects alone and supports `--dry-run`.

- `-q/--quiet` leaves out confirmations such as `jail/web started`; errors and requested output still print.

- `jailops exec` allocates a pseudo-terminal on its own when stdin and stdout are terminals; `-T` runs without one (`-t` names the default).

- `jailops image create` takes the architecture from the server when `--arch` is not given (an arm64 host no longer gets amd64 images) and fails with a hint when the server cannot tell.

- One generation notation: `gN` wherever a command takes a generation (`image build --from`, `jail upgrade --generation`, `image alias set`, `image delete-generation`); a bare number and `IMAGE#gN` are accepted too.

- `jailops jail snapshot show JAIL SNAPSHOT`; `jailops image|network|project show` print a short human form (header, specification next to status) instead of raw YAML, as `jail show` does; `-o yaml` and `-o json` are unchanged.

- `jailops update check` with no names checks every jail of the project (`--all` says the same).

- Shell completion continues after the first name (`jailops stop web <TAB>`, `delete a b <TAB>`), completes snapshot names, image aliases and generations, and the values of `--state`, `--layout`, `--update-strategy`, `--os`, `--arch`, `--source-type`, `--ipv6-mode`, `--compression`, `--kind`, `--verify-mode`, `--type`, `--host`, `--remote`, `--profile`, `--firewall-policy`, `--depends`, `--target` and `--to-host`.

- Flag names line up: `jailops trust add --permission` (as `token create` and `user create`; `--role` still works, unlisted), `remote create` and `remote delete` next to `add` and `remove`, `backup restore --to-host` by host name or UUID, `jailops jail label` next to `jailops label`; `--project` is documented on `events` and `op list`, which it narrows.

- Generated help says "Create an image" and "an address set" instead of "a image"; the fake server answers `exec` and `shell` on a stopped jail with the hint of the real daemon ("start it first").

- **Lists read better on a terminal.** `jailops jail list` (and every other list) is a short boxed table when standard output is a terminal: for jails NAME, STATE, IPV4, IPV6, IMAGE (with the generation) and AGE, every address of a jail on its own line with its network, long IPv6 addresses never cut; a narrow terminal loses the least important columns first. `-o wide` shows every column as before; a pipe, a file, `--no-headers` and `-o json|yaml|name` print exactly what they did, so scripts are unaffected. `network list` on a terminal shows both address families.

- `jailops jail info NAME` (also `jailops info NAME`): a short report on one jail for a person: state and JID, type, image and generation, storage, dates, limits and usage, every interface with all its addresses (family and scope), snapshots, the update policy with the last update, labels and problems. `-o json|yaml` gives the same data structured. A part the server cannot give is said to be missing, never guessed. `show` is unchanged.

- **A jail's resource usage is measured on request**: `GET /api/v1/jails/{name}/usage` (readonly, extension `jail_usage`, `Jails().Usage` in the SDK and the fake) answers what the jail uses now: processes, CPU time and resident memory from the kernel's resource accounting (`rctl -u jail:<key>`, a new read-only form on the helper's command allow-list, accepted for managed jails only) while it runs, and the space of its datasets (`used` and `referenced` of the root, `used` of the persistent datasets and volumes) always. Nothing is stored and the jail list stays as cheap as before. When racct is off (`kern.racct.enable`), when `rctl` fails or when a dataset cannot be read, the reply carries the reason instead of zeros, and `jail info` prints it (`Usage: - (racct is off: ...)`); otherwise it prints `processes 23, cpu 2h13m, memory 388.2 MiB of 2G` and `Disk: root 256.0 MiB used (1.5 GiB referenced), data 70.0 MiB`. Interface counters are not part of it. UNVERIFIED on a host: the rctl output format is implemented from rctl(8).

- **Shell completion is installed by the packages**: the FreeBSD client package and the Linux packages carry the bash, zsh and fish scripts of `jailops` and `jailops-sync` under `share/` (`/usr/local/share/bash-completion/completions/`, `.../zsh/site-functions/`, `.../fish/vendor_completions.d/`), generated from the built binaries at package build time (`scripts/gen-share.sh`); the release archives of the client carry them too. Completing names against a server that does not answer gives up after a moment and prints nothing.

- **Man pages**: the packages and release archives install them the usual FreeBSD way. `jailops` and `jailops-sync` have one mdoc page per command (`man jailops-jail-list`), generated from the command tree at package build time by the hidden `man DIR` command of each tool (deterministic, `SOURCE_DATE_EPOCH` honoured); `jailopsd(8)`, `jailops-priv(8)` and `jailops-backupd(8)` are written by hand in `packaging/man/`. `scripts/gen-share.sh` replaces `gen-completions.sh` and produces completion scripts and man pages together into `bin/share`.

- `jailops-backupd`: `<command> --help` prints the synopsis, a description and the flags on standard output and exits 0 (it used to print `flag: help requested` and exit 1); `-c FILE`, `--config FILE` and `--json` are accepted after the command too (`status --json`); `--version` is an alias of `version` in every tool (`jailopsd`, `jailops-priv`, `jailops-backupd`, `jailops-sync`, `jailops`); `check-config` validates the configuration and the files it names without the network, and the rc.d script runs it (`service jailops_backupd checkconfig`) and refuses to start with a bad configuration; a second `init` reports the existing repository and its owner and exits 0; `init --admin NAME --password-prompt` (and `users add --password-prompt`) ask for the password on the terminal; `completion bash|zsh|fish` prints a completion script, which the backup node package installs.

- `jailops-priv`: `-h` prints a synopsis before the flags, `init --help` works and exits 0, and `init` checks its arguments (`-pool`) before it asks for root.

- `jailops-sync`: an unknown command or flag, or the wrong arguments, exits 2 like the other tools (it was 1) and points at `--help`; `sync`, `plan`, `drift`, `doctor`, `secrets set` and `remote bootstrap` have examples in their help.

- The rc.d header of `jailopsd` says there is no `reload` (SIGHUP is ignored by design; a renewed certificate is picked up on its own), and docs/troubleshooting.md tells where the backup node logs and how to read its status and jobs.

## 0.2.0 - 2026-10-27

- **Getting started turns the web console on**: `sysrc jailopsd_web=YES` (or `jailopsd -web`) now also opens the TLS listener on port 39110 when `listen.https.address` is empty, so one line puts the console in a browser; `web.enabled: true` in the file alone still opens no listener. `jailops-priv init -web` writes the same two settings into the `jailopsd.yaml` it creates. The guides use the rc.conf knob and the provisioning example turns it on (`JAILOPS_WEB=no` leaves it off); leave it out for a host you reach with the CLI only.

- **The API is under `/api/v1/` on every service** (it was `/1.0/`), and the web console is on `/`: the same layout for `jailopsd`, `jailops-backupd` and `pkgops serve` (which already used `/api/v1/`). The old paths are gone, not aliased. The CLI, the SDK, `jailops-sync`, the consoles, the hub, the docs and the tests use the new paths; the version number in the answer (`api_version: "1.0"`) is unchanged. Nothing under `/api/` is ever answered with the console. The backup node's unauthenticated `GET /api/v1` no longer tells the host name, host id or software version.

- `just build` and `just build-freebsd` always build the web consoles first, so the binaries always have everything embedded (before, you had to run `just web-all` yourself, or the daemon had no console).

- **The web consoles are in every binary and off until you turn them on.** `web.enabled` defaults to `false` in `jailopsd` (it was `true`) and in the shipped `backupd.yaml`. Turn the host console on with `web.enabled: true`, `sysrc jailopsd_web=YES` or `jailopsd -web`; `jailopsd_web=NO` and `-no-web` force it off. Hosts that used the console must set one of these.
- **Backup node, upgrading:** the node now has one TLS listener on `listen` (default `:39111`, every interface unless you bind an address). It serves the host API (mutual TLS), the management API (users, tokens, sessions; it used to be on a loopback listener of its own, `web.listen`, which is gone: delete it) and, with `web.enabled: true`, the console. `web.enabled` switches the console only. The two ways of proving who you are are independent: the host API needs a valid certificate of an enrolled, non-revoked host (401 otherwise), the management API takes no identity from a certificate (an administrator is not turned away by the certificate the browser offers, and a host certificate is never an administrator). The listener is TLS 1.3 only, password hashing is bounded to one hash per CPU at once (429 beyond that), and a management request has 15 seconds to arrive.
- **One port per service, in a range of our own: 39110 to 39112**, documented in docs/ports.md. `jailopsd` TLS API and console 39110 (was 8443; still off until you set `listen.https.address`), `jailops-backupd` 39111 (was 8444) for the hosts **and** for its management API and console, which now share that port (the second listener, `web.listen` on 8445, is gone: setting it is an error), `pkgops serve` 39112. `jailops remote add` without a port uses 39110.
- `jailops-backupd version`, its `/1.0` answer and its log now report the release version. The release stamp (`-X main.version`) never reached it, so the 0.1.0 binaries say `0.1.0-dev`. CI now checks that every command declares `var version`.
- README and docs/install.md say what is true today: the JailOps packages are in the repository, and the status block is replaced by a link to the roadmap.

## 0.1.0 - 2026-10-26

This is the first release, so the list below is ordered by area, not by date: what changes behaviour first, then
networking, security, fleets, packaging, and the groundwork.

### Advertised features that did not work
Everything below used to be reachable from the CLI, the SDK or the docs and answered `unsupported` at run time.
- `jailops jail console` and `Jails().Console()` are real: they use the console session protocol the web console uses
  (HTTP requests and server-sent events), with a raw local terminal and window resize forwarding. The fake server
  implements the same routes. Verified against the fake and the in-process daemon; UNVERIFIED on a real host.
- Removed: `jail create --package` and `jail create --encrypted`, `storage create --encrypted`, and
  `Jails().Freeze()` with the `freeze` action (FreeBSD has no jail freeze). A spec that still sets `packages` or
  `encrypted` is refused with `unsupported` and a hint (provisioning `packages` step; the manual ZFS encryption-root
  recipe in docs/storage.md). Native ZFS encryption and packages at creation remain unbuilt.
- Still `unsupported` (HTTP 501, listed in `sdk/PROTOCOL.md` and design section 16): `PUT /server`, cluster routes,
  listing console sessions, jail logs history, file access, cold moves, snapshot rename, volume management of an
  existing jail, bundle download, jail templates, groups, rollouts, and the backup transport routes.
- Documented as not enforced or not driven: the rate-limit shaper (stored, not enforced), `zfs_delegation` (accepted,
  never handed to the jail), replication between hosts (a library, no API or CLI), the `inplace` strategy for thin
  jails (refused). The "OpenAPI is planned" note now says there is no OpenAPI document today.
- Tests: the service layer is tested directly on the in-memory fakes (scope checks, error mapping, create rollback at
  every injected failure), `fleet/apply` and `fleet/rollout` have unit tests, and the helper session tests wait for
  observable output instead of sleeping.

### Image aliases and deletion
- Deleting an image removes it with all its generations and aliases; it is refused only while a jail references a generation, and the error names the jails.
- Deleting a generation that only aliases reference moves each alias to the newest remaining generation, or removes it when none remains (a generation a jail uses is still refused).
- Aliases are managed explicitly: `jailops image alias set IMAGE ALIAS [GENERATION]` and `jailops image alias delete IMAGE ALIAS` (replacing `image alias ... [--remove]`), `PUT|DELETE /1.0/images/{name}/aliases/{alias}` (admin, extension `image_aliases`), `Images().SetAlias/DeleteAlias` in the SDK and the fake, and buttons in the web console.

### One identity system, root as the break-glass
- The unix socket no longer maps Unix users or groups to roles. Peer uid 0 (root) is a global admin, the host's recovery access;
  every other local user is anonymous, exactly like an unauthenticated remote client (`GET /1.0`, `GET /healthz`, login, trust
  redemption, the static console) and authenticates with a token or a login over the socket. Removed: `listen.unix.admin_group`,
  `user_group`, `user_projects` (a configuration that sets one stops the daemon with a message), the `jailops-admin` group
  created by `jailops-priv init` (and its `-admin-group` option), and `JAILOPS_ADMINS` in `examples/provisioning`. The socket stays mode 0666: connecting confers nothing.
- Identities are managed from the host with `sudo jailops user ...`: `user create NAME --admin` (first administrator),
  `user set-password` (prompt, `--password-stdin` or `--generate`; no password ever on the command line), `user set-role`,
  `token revoke`. New API route `POST /1.0/users/{name}/password` (global admin), `Users().SetPassword` in the SDK and the fake, and user
  creation, role changes, password reset and deletion in the web console.
- Tests: root is a global admin; a non-root peer is anonymous on every route (the authorization matrix has an "anonymous
  local" column equal to "anonymous remote"); a token over the socket works for any uid. `temp/verify-identity.sh` is the check for a real FreeBSD host (not yet run).
- The backup node's admin socket follows the same rule: only root (uid 0) is an administrator there, the socket is mode 0600, and the `admin_group` key is removed (a configuration that sets it stops the node with a message). The management API/UI (127.0.0.1:8445) and the host listener are unchanged.

### Fleets, examples and exposure
- `jailops-sync`: `build: true` on an Image builds its first generation when the host has none, so a fresh host reaches a running jail
  with one `sync --apply`; `check` is an alias of `validate`; `validate` refuses what the daemon would refuse at apply time
  (name rules, overlapping subnets and datasets, IPv6 proxy and routed limits, reserved fields, `host_mounts` and
  `zfs_delegation` in a profile, `http://` to a non-loopback host); `adopt` notes host profiles the repository does not know.
- Examples (`examples/`), each with a README and an `EXPECTED-PLAN.md`, tested against in-memory hosts and the daemon's validators:
  `ipv6-switched-cloud`, `ipv6-routed-hetzner`, `web-stack`, `big-pool-tank`, `multi-host-fleet`, `provisioning`,
  `pkgops-packaged-service`. New pages: `docs/exposure.md`, `docs/provisioning.md`, `docs/api.md`.
- `jailopsd`: `web.enabled: false` switches the embedded console off and `-tags noweb` builds without it; the TLS listener serves a
  reloaded certificate (a renewed `listen.https.cert`/`key` needs no restart); a configured certificate is never generated or
  overwritten (a missing file or half a pair stops the start); `listen.https.address`, and `cert` without `key`, are validated at load.

### No default project, everything explicit
- A fresh host has no project. Every request about something a project owns names the project (`?project=`, or the
  current project set with `jailops project switch`); without one the server answers `400 a project is required`.
- A jail is created with a network and a storage named explicitly, in the jail or in a profile; nothing is filled in
  from the project. The project fields `default` on network and storage grants and `--default-network`/`--default-storage`
  are gone.
- A network belongs to one project: granting a network another project holds is refused, and grants are whole networks
  (no shared bridges, no per-project sub-ranges). Storage can still be shared; each project gets its own dataset tree
  and quota.
- Only `<mount>/projects/<project>/jails/<name>` exists on disk; the legacy layout of the default project is removed.
- `jailops-sync`: jails and project-level objects live under `projects/<project>/` only, rollouts name their project,
  two projects naming one network is a validation error, and `adopt` writes by project.

### Supported FreeBSD releases
- JailOps targets FreeBSD 15.1 and later; CI, the release workflow and the package build use 15.1 and the 15 ABI only. `jailops doctor` fails (`host.release`) on an older host. Examples and help
  texts use 15.1.

### Configuration
- `state_dir` defaults to `<data_dir>/state` (`/var/db/jailops/state`, where `jailops-priv init` mounts the state
  dataset) instead of a directory inside the root-owned storage mount, which the daemon cannot write. A test keeps
  `packaging/jailopsd.yaml` listing every key the daemon accepts.

### Audit fixes (proxy, host mounts, PF)
- The helper rechecks a jail's fstab when the jail starts and refuses a mount point or source that passes through a
  symbolic link; an unchanged host mount survives an operator's start or restart, and setting `host_mounts` or
  `zfs_delegation` needs a global admin, not a global operator.
- The NDP proxy announces new addresses only (own budget, up to three times), answers a repeated solicitation once,
  bounds answers per target, reports a closed capture in the network condition and the doctor, and backs off failed
  opens. The helper only speaks for addresses that have a `/128` host route through one of its interfaces, and the
  multicast sockets of the proxy stay on loopback and are closed with the proxy.
- A pass rule's label includes everything above it in the anchor, so a deny inserted above an unchanged rule ends the
  flows that rule admitted; an anchor whose loaded rules cannot be read is not replaced.

### Existing data in a jail
- A jail can mount existing host directories, for example datasets of a large pool JailOps does not manage:
  `host_mounts` in the spec, `--host-mount SOURCE:TARGET[:ro]` on create. Only a global administrator may set them, the
  source must lie below an `admin-host-mount` directory in the helper's `priv.conf`, profiles cannot carry them, and the
  mount point is created in the jail's root on every render. Everything else (jails on a big pool as storage, volumes
  with quotas) already worked and is explained on the website.

### Networking and IPv6
- IPv6 proxy mode has no Experimental switch any more (`experimental.ndproxy` is gone): a network in proxy mode always
  has its proxy, which answers for every address of a running jail (any prefix length inside the uplink's /64, for
  example a /80) and announces each address when it appears and whenever the capture is reopened. The operator's
  `ndp-proxy` line in `priv.conf` stays the authority; `jailops doctor` fails (`network.ndproxy`) with that line when a
  proxy network is not answering.
- A jail address on a switched IPv6 /64 can be given to a new jail with another hardware address; the kernel's cached
  neighbor entry of the old one no longer lingers (it is dropped when the host route is removed and before the next is
  made), so the host reaches the new jail at once.
- Deleting a network flushes its anchor whether or not it had NAT (the anchor also holds the pass of the gateway port).
- The helper offers `ndp.open` (a read-only, locked, filtered capture of an approved uplink) and `ndp.answer`
  (one advertisement, built by the helper) instead of the unconfigured `sys.open_bpf`. What may be answered is set by
  the operator in `priv.conf` with `ndp-proxy = <uplink>=<prefix>,...`; nothing works without it.
- The helper's audit lines now reach syslog under rc (`-log syslog`).
- Doctor accepts `gateway_enable` and `ipv6_gateway_enable` in rc.conf as persistent forwarding.
- A jail attached to a network without naming addresses gets one of every family the network has (dual-stack networks
  work with plain `--network NAME`); verified on a managed bridge with a /24 and a /80 of the host's routed /64.
- Debian images (`--source-type debootstrap`, glibc) build and run; the release signature is always checked.
- Copying an image into a jail happens inside the helper (`zfs.replicate`): 140 MB in 2 s instead of minutes.

### Packet filter
- The first Linux image build works from a clean host: the Linux modules and the fallback brand are asserted (through the
  reconciler's host pass, so with the helper's existing `kldload` and `sysctl` rules) before the build, and the host pass
  keeps them while a Linux image is defined. `doctor` warns (`network.dns`) about a network that jails use and that has
  no nameserver.
- The helper accepts `jexec <build jail> /sbin/apk` again: the image build jail (`jobuild-<image>-g<N>`) was refused by
  the jail-key check of the `jexec` rule (Alpine and Debian images could not be built). Linux jails (Alpine, Debian) were
  run with the new addressing on FreeBSD arm64 and amd64 (docs/linux-jails.md).
- **A jail has exactly the addresses JailOps gave it.** A jail holds its IPv4 address as `/32` and its IPv6 address as
  `/128` (also in routed mode), with default routes through the host (an on-link host route to the IPv4 gateway,
  `fe80::1` for IPv6), so jails reach each other only through the host and its PF; every jail port is a private bridge
  member (the `private` network option no longer has an effect). At attach the host installs permanent ARP and neighbor
  entries for the jail (removed at detach and by the stale-entry collection), sets `staticarp` on the gateway interface
  and pins the jail's MAC address to its port with a static forwarding entry; no jail may send a neighbour
  advertisement, and router advertisements and redirects are dropped (also from the host towards a jail). Verified with
  forged ARP, neighbour advertisements and router advertisements sent from inside the jails, an extra IPv4 or IPv6
  address, another jail's MAC address and a DHCP discover (the amd64 host, proxy mode; the arm64 host, routed mode). The anti-spoofing
  table holds the jail's own link-local address instead of `fe80::/10`, `::` passes only as DAD, and ICMPv6 errors
  (packet too big and the rest) always pass. Existing jails get the new addressing when they are restarted.
  `doctor` checks the proxy uplink (`network.uplink`), warns about a gateway interface that is not `staticarp` yet (`network.staticarp`) and warns without a `scrub` rule.
- Stale `/128` host routes of proxy networks are removed by the reconcile loop; a refused announcement is retried;
  other hosts' DAD for a proxied address is answered; proxy subnets are /48 to /126, overlapping network subnets are
  refused, and the reserved IPv6 addresses are never allocated.
- The gateway addresses of a network moved from the bridge to a gateway epair (the bridge is layer 2 only), so that a
  jail's traffic to the host and to routed networks meets the jail's own rules: `default_out: deny`, egress rules
  and anti-spoofing now work towards the host and the internet (they did not; see design 18.9).
- Projects are isolated from each other by default (every jail has an anchor); a project policy's defaults are a floor.
- Fixed pf grammar errors in the generated rules (`icmp6-type`, `tagged`), and the `::` source address (DAD) that pf
  silently dropped from the antispoof table. The unverified `network.pf_placement: bridge` option is gone.
- `scripts/ci-freebsd-pf.sh`: the packet filter tested with real traffic, run by the e2e script; with `FORGE_BIN` set it also sends forged ARP, ICMPv6 and router-advertisement frames from inside a jail (`scripts/forge`) and checks real effects, not rule text (phase 0e).
- Stopping a jail retries a busy unmount for a few seconds; the daemon waits for the helper's socket at start.

### Security
- The reconciler's host pass can be asserted on demand (`Reconciler.AssertHost`), so building a Linux image loads the
  Linux kernel modules and sets the fallback brand before it runs, instead of needing a Linux jail to already exist;
  the host pass also keeps them loaded while a Linux image is defined. `doctor` warns (`network.dns`) about a network
  in use with no nameserver, and (`network.staticarp`) about a gateway interface a jail started before this version
  left resolving addresses with ARP.
- `jailopsd`'s `REQUIRE` line now names `pf` (`packaging/rc.d/jailopsd`): without it rcorder had no reason to start pf
  before jailopsd, and a host where it lost that race would have no filtering at all, not even the administrator's
  default-deny, while jailopsd created bridges and started jails (design 18.14). The helper's `fs.read_file` can now
  read `/etc/rc.conf` (read-only, like `/etc/sysctl.conf` and `/etc/resolv.conf` already were): `doctor`'s persistent-
  forwarding check reads it for `gateway_enable`/`ipv6_gateway_enable` and, without this, always warned even when
  they were set (found live on the amd64 host).
- Boot-order audit (design 18.14): a jail's own PF anchor loads only after its `exec.start` (the guest's whole boot)
  finishes, which an anchor call that resolves to nothing falls through safely on a default-deny host with the anchor
  line placed correctly (both checked by `doctor`), but depends on pf actually being up, which the `REQUIRE: pf` fix
  above now guarantees. Measured at the daemon level with a jail whose `exec.start` sent spoofed-source pings (refused
  by the kernel before leaving it: a jail cannot bind a raw socket to an address it does not own) and forged ARP from
  the first instant it ran: zero packets reached either an external observer or a local capture, and a production
  jail's reachability had no downtime. A literal power-cycle reboot could not be measured on the test host: its
  disposable, loopback-file ZFS pool does not survive a reboot (`/tmp` is cleared at boot, and `zpool import -a` does
  not scan arbitrary file paths even from a location that is cleared) — a property of the test harness, not of
  JailOps or of this fix.
- The NDP proxy's per-target answer budget is now keyed by `(target, requester MAC)`, not by target alone: a flood of
  solicitations for one address that only forges its claimed source IP (free, no state) can no longer drain what a
  different requester's own address — notably the real router's — needs for the very same target (design 18.10).
- `scripts/forge` (built by `just build-forge`) sends forged ARP, ICMPv6 and router-advertisement frames from inside a
  jail for the PF test matrix's forgery phase (0e, `FORGE_BIN`): real effects are asserted (host ARP/ND caches
  unchanged, the victim's traffic keeps flowing, the attacker gains no address, nothing reaches the uplink), not rule
  presence.
- TOTP codes are single-use under concurrency; password and TOTP-confirmation checks are rate-limited; hubs refuse
  authentication endpoints and unaddressed claims; client certificate dates and key usage are enforced; JSON bodies
  need `application/json`; session cookies are tied to the transport; stricter CSP, HSTS; backup-node login limits,
  login Origin check, bad-token limit and session invalidation on password change.
- The helper's authority over file agents comes from records it holds itself (datasets it created incomplete, snapshots
  it made for a backup), never from ZFS user properties, which the daemon can set.
- The daemon's state directory is its own (a separate dataset outside the storage root); the helper no longer reads or
  writes there. The storage root and the scratch directory belong to root and are checked before every use.
- The helper validates the pkg repository configuration it is pointed at (signed, http(s) URL), accepts `devfs unhide`
  only for known device paths, restricts `mount.fstab` to its own directory, leaves existing directories alone in
  `MkdirAll`, and bounds the life of a file agent.
- A hub cannot reach the login, credential or session endpoints of a host; a requestor claim must name its audience;
  client certificates are checked for validity period and key usage.
- Browser consoles reach jail terminals through a session over POST and server-sent events (`jail_console`).

### Fleet sync and secrets
- Fleet repositories can hold SOPS-encrypted files: `shared/secrets.sops.yaml` (jail environment values
  `secret:<dotted.path>`, sealed to the hosts when applied, hashed from the stored ciphertext so plans need no key)
  and `hosts/<host>/remote.sops.yaml` (url, pinned server fingerprint and an encrypted client certificate for
  pinned mTLS), governed by a `.sops.yaml` policy. The existing `secrets/<name>.age` mechanism is unchanged.
- The key comes from `SOPS_AGE_KEY`, `SOPS_AGE_KEY_FILE` (age plugin identities work) or `SOPS_AGE_KEY_CMD`.
- New commands: `vault status|list-recipients|add-recipient|remove-recipient` and
  `remote list|bootstrap`; `validate` and `doctor` check the SOPS files; `render` never decrypts.
- `jailops-sync` has the SOPS/age workflow: `shared/secrets.sops.yaml`, per-host `remote.sops.yaml` (pinned mTLS with a
  generated client certificate), `vault` commands for recipients, `remote bootstrap`, `secrets add-hosts`.
- Jail `env` is now used (every process started in the jail), and a host opens `sealed:age:` values with its own age
  key (`sealed_secrets`, `secrets_recipient`).
- Run against a real FreeBSD host: adopt with claim, plan, apply (a dual-stack network, a project grant, a jail), drift
  on a declared key, revert on apply.

### Packaging and installation
- FreeBSD packages (`jailops-client`, `jailops`, `jailops-backupd`) and a signed pkg repository (`scripts/pkg-build.sh`,
  `scripts/pkg-repo.sh`), GoReleaser archives and Linux packages (`.goreleaser.yaml`), a release workflow and CI
  (`.github/workflows`), including a FreeBSD VM job that runs `scripts/ci-freebsd-e2e.sh`.
- `jailops-priv init -pool POOL` sets a host up (user, datasets, directories, configuration); rc.d scripts for
  `jailopsd`, `jailops_priv` and `jailops_backupd`; a sample `backupd.yaml` and a systemd unit for Linux backup nodes.
- Backups work with the unprivileged daemon: file agents (`internal/fsagent`) read snapshots and write restored jails
  for it; `jailops label` sets the labels policies select by; a policy is looked up by project and name.

- Full restore tests boot the restored backup as a hidden jail without a network and check it from the inside.

### Fixes found by running on FreeBSD
- Pseudo-terminals use `posix_openpt` and `FIODGNAME` (FreeBSD has no `/dev/ptmx`); `exec -t` and `shell` work.
- Fleet reconcile passes are serialised (the timer and a manual pass could conflict on a project generation).
- Doctor works through the unprivileged daemon and the helper: `kldstat -q -n`, `pfctl -s rules|nat`, `kern.racct.enable`.

### Foundations, module path and rename
- Repository, design document, Justfile, API types (`sdk/api`).
- The Go module path is `jailops.net/jailops` (SDK: `jailops.net/jailops/sdk`), served by a `go-import` page on jailops.net.
- New logo: an isometric cube with bars and a prompt, as SVG (`docs/brand/`), used by the website, the favicons and
  the web consoles. The files are generated by `scripts/make_logo.py` in the website repository.
- The project is now JailOps (`jailops`, `jailopsd`, `jailops-priv`, `jailops-sync`, `jailops-backupd`).
- Repository on-disk format v1 identifiers (magic bytes `JOCH`/`JOIX`/`JOMF`/`JOCT`, recovery key prefix `JOR1`,
  key-derivation namespaces, gear-table seed) were renamed before the format was ever released, and the golden
  vectors were regenerated once; they are pinned again from here on.
