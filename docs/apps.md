# Applications: the catalog

An **app** is a definition of a jail that runs one or more packages from the FreeBSD repository or from the JailOps
package repository, with what an operator needs to run it: the services, the ports, where the data, the
configuration and the logs live, how to check that it runs. The catalog is the set of definitions a host has;
`jailops app list` shows it and `jailops app info NAME` describes one entry.

This page is the contract of the catalog and of the installation: `jailops app install NAME` creates a jail that runs
an app as one operation, `jailops app installed` lists the jails that came from the catalog.

## Trust model

The catalog is delivered as an ordinary signed package, `jailops-apps`, from `pkg.jailops.net`. It installs one
YAML file per app under `/usr/local/share/jailops/apps/` (`apps.catalog_dir` of `jailopsd.yaml`, see
[configuration.md](configuration.md)). The signature of the package is the root of trust: the daemon **reads local
files only and never fetches anything**, so a catalog cannot change without a package operation on the host.
An operator may drop a definition of their own into the directory; it is read like any other.

Every definition is validated when it is read (on each request; the directory is small). A definition that fails is
**listed as invalid with the reason** (`jailops app list` shows it, `GET /api/v1/apps` carries `valid: false` and
`error`) and `GET /api/v1/apps/NAME` answers `invalid`; it can never be installed. The other definitions are not
affected. A missing directory is an empty catalog, not an error. A file that is not a regular file (a symbolic link,
a directory) or larger than 256 KB is invalid. The list reads at most 512 definitions, in name order; the first one
beyond is listed once as invalid ("catalog has too many definitions"), the others are not read. The list does not read
icons; `GET /api/v1/apps/NAME` reads the one definition and its icon, so a bad icon shows there.

Each definition carries the hash of its file (`sha256:<hex>`); an installed jail will record it, so a later
`app info` can say whether the definition changed since the installation.

## The definition format, version 1

One YAML document per file, strict: an unknown key is an error, like every other configuration file of JailOps.
The file name is the app name with `.yaml` (`postgresql.yaml` holds `name: postgresql`; a mismatch is invalid, so two
files cannot claim one name). The starter catalog in `packaging/apps/` is the reference.

```yaml
version: 1                       # required, 1
name: postgresql                 # a jail name: ^[a-z][a-z0-9-]{0,62}$, equal to the file name
title: PostgreSQL                # one line, at most 80 characters
summary: Relational database server, PostgreSQL 18 from the FreeBSD packages.   # one line, at most 140
description: |                   # plain text paragraphs, no markup, at most 2000 characters
  ...
category: database               # database, web, tools, devops, storage, monitoring, communication, network, security, other
homepage: https://www.postgresql.org/    # an https URL
license: PostgreSQL              # free text, at most 64 characters
icon: postgresql.svg             # optional: a .png or .svg next to the definition, at most 64 KB
image: {os: freebsd, release_min: "15.1"}   # freebsd only; 15.1 or later
packages:                        # at least one; repo: ports (default) or jailops
  - {name: postgresql18-server, repo: ports, version: "18.6"}   # version: what the definition was written against
services:                        # rc.d services, in start order
  - name: postgresql             # the rc.d script name
    rc: {postgresql_initdb_flags: "--encoding=utf-8"}   # rc.conf knobs; <name>_enable is written by the installer
    start: true                  # default true; false: enabled, not started
ports:
  - {name: postgres, port: 5432, proto: tcp, description: The SQL port., expose: false}
data:                            # the state; each path becomes a persistent dataset of the jail
  - {path: /var/db/postgres, description: The cluster.}
config:                          # files a person edits; restart names the service to restart afterwards
  - {path: /var/db/postgres/data18/postgresql.conf, description: Server settings., restart: postgresql}
logs:                            # files or directories; reading them is a separate feature
  - {name: server, path: /var/log/messages, description: The port logs to syslog.}
setup:                           # run once in the jail after the packages, before the services: argv, no shell
  - [/usr/sbin/chown, "postgres:postgres", /var/db/postgres]
  - [/usr/sbin/service, postgresql, initdb]
sysvipc: true                    # optional, default false: System V IPC of the jail's own (see below)
health:                          # optional; exactly one of tcp, http, exec; wait_seconds at most 300 (default 60)
  exec: [/usr/local/bin/pg_isready, -U, postgres]
  wait_seconds: 60
resources: {memory: 1G, disk: 10G, cpu: "1"}   # hints for the jail's limits, not limits
notes: |                         # shown after the installation, at most 2000 characters
  ...
```

Rules the validator enforces, besides the sizes above:

- `data`, `config` and `logs` paths are absolute and clean (no `.`, `..`, empty segments, no characters outside
  `A-Za-z0-9/._+@:=,-`). `data` and `config` paths lie **below** `/var`, `/usr/local`, `/usr/local/etc` or `/home`
  (never one of those themselves); `logs` paths below `/var/log`, `/var/db` or `/usr/local`. A `data` or `config` path
  is never one of the persistent datasets of every jail (`/etc`, `/usr/local`, `/var/db`, `/var/log`, `/var/mail`,
  `/var/spool`, `/home`, `/root`); the installer also refuses, before the operation starts and with the same message,
  a path that equals one this host adds in `lifecycle.persistent`. Nothing lies under the base
  system (`/etc`, `/bin`, `/sbin`, `/lib`, `/usr/bin`, `/usr/lib`, `/usr/share`, ...) nor under the packages' own
  directories (`/usr/local/bin`, `/usr/local/lib`, `/usr/local/share`, `/usr/local/etc/rc.d`, ...) nor under `/var/run`,
  `/var/tmp` or `/tmp`.
- Two `data` paths never overlap: each is its own dataset.
- `rc` keys are rc.conf variable names (`^[a-z][a-z0-9_]{0,63}$`); values are one printable line. The installer writes
  `<name>_enable=YES` itself, with `-` and `.` of the service name turned into `_` as rc.conf spells it, and a
  definition that sets any service's `_enable` is invalid. Two services whose names are the same once converted
  (`foo-bar` and `foo_bar`) are invalid.
- `setup` and `health.exec` commands are argv with an absolute program path; there is no shell, no search path, no
  pipe. At most 32 setup commands.
- Names of ports and logs are short slugs (`^[a-z][a-z0-9-]{0,31}$`); a port is unique per `port/proto`.
- `health.http.path` is an absolute request path; `status` defaults to 200. A definition may leave `health` out; the
  starter catalog does so only for an app that starts none of its services (`isms`: nothing runs to check until the
  operator has configured it).
- `resources.memory` and `.disk` are sizes (`512M`, `10G`); `cpu` is a number.
- `sysvipc` is a boolean (`true` or `false`, nothing else). A jail has no System V shared memory, semaphores or message
  queues unless its parameters allow them, and a server that needs them fails: PostgreSQL's `initdb` stops at
  `shmget(...)` "Failed system call". `sysvipc: true` sets the jail spec's `sysvipc`, which renders
  `sysvmsg = new; sysvsem = new; sysvshm = new;` into the jail's `jail.conf`: namespaces of the jail's own, separate
  from the host's and from every other jail's (never `inherit`, which would share the host's). The objects still count
  against the host's `kern.ipc` limits. The key is an ordinary key of the jail too (`jailops config set NAME
  sysvipc=true`, a restart applies it; a profile may carry it). The FreeBSD end-to-end test (below) installs PostgreSQL
  with it and runs a query.

Defaults are filled in when the definition is read, so the API always answers a complete object: `repo: ports`,
`proto: tcp`, `start: true`, `health.wait_seconds: 60`, `health.http.status: 200`.

## The API and the clients

| Route | Reply |
|---|---|
| `GET /api/v1/apps` | `[]AppSummary`: `name`, `title`, `summary`, `category`, `version` (of the first package, when the definition states one), `valid`, `error` |
| `GET /api/v1/apps/<name>` | `AppDetail`: `definition` (the `App`), `hash`, `file`, `icon` (a data URI, `image/png` or `image/svg+xml`, when the definition has one); `invalid` for an invalid definition, `not_found` for an unknown name |

| `POST /api/v1/apps/<name>/install` | body `AppInstall`: `jail` (default: the app name), `network`, `storage` (each defaults to the project's only grant of its kind), `image` (default: the only FreeBSD image with the alias `stable`, as `NAME@stable`; none or several is `invalid`), `limits` (replaces the resource hints), `dry_run`; async, result `AppInstallResult`: `jail`, `version`, `addresses`, `ports` (with `endpoint`), `data`, `config`, `logs`, `notes`, `snapshot`, `steps` |

Extensions `apps` (the catalog) and `app_install` (the installation). The Go SDK has `Apps().List`, `Apps().Get`,
`Apps().Install` and `Apps().Installed` (the jails of the project that carry the label `jailops.app`); the in-memory
server of `sdk/fake` starts with six definitions (nginx, postgresql, valkey, isms, nsd, mail, shortened) and installs them as running jails. The CLI:
`jailops app list [PATTERN...] [--category C] [-c COLS] [--sort COL] [-o wide|json|yaml|name|csv]`,
`jailops app info NAME [-o json|yaml]`, `jailops app install NAME [--name JAIL] [--network N] [--storage S]
[--image I] [--memory M] [--disk D] [--cpu N] [--project P] [--dry-run] [--no-wait]` and
`jailops app installed [PATTERN...] [-o json|yaml|name|csv]` (columns `JAIL`, `APP`, `VERSION`, `STATE`,
`ADDRESSES`, `PORTS`; the list is the project's jail list narrowed on the client to the label, no route of its own).

**Permissions.** The catalog is readable by **any authenticated identity**, whatever its role and project scope
(route class `self`, like the configuration key registry): it is documentation of what the host can run, not an object
of a project. Installing is a project operation: `POST /api/v1/apps/<name>/install` is its own entry of the permission
table, **operator** in the project of the request (route class `project`: a scope-less identity is forbidden, a project
the identity may not use is `not_found`). The create of a jail stays `admin`; the install is `operator` because it creates
nothing the definition does not say. The jail it makes is an ordinary jail of the project afterwards, with the ordinary
roles (`delete` is admin, `start` and `stop` are operator).

## Console

The host console has an **Apps** page (`/apps`): the catalog as cards with a search over name, title and summary and category filters; an invalid definition is shown with its reason and cannot be installed. `/apps/NAME` shows a definition in full (packages, services and their `rc.conf` knobs, ports and whether they are exposed, the data paths with the volume each becomes, the configuration files and the service each one restarts, the log files, the health check, the resource hints, the notes and the definition's hash). **Install** opens a dialog with the jail name (the app name by default), the network and storage among the project's grants (preset when there is one of a kind) and the limits prefilled from the hints; *Preview steps* runs the dry run and lists the steps, and the installation then follows the operation: the step list is ticked off from the operation's progress (the server sets it after each step) and ends in a report with the jail, its addresses, the port endpoints, where the data, configuration and logs are, the notes and the `app-installed` snapshot with the rollback command, or with the error, its hint and a link to the jail when it was kept. The button is disabled, with the reason, for an identity below operator in the project and for a project without a network or storage grant. The **Installed** tab lists the project's jails that carry `jailops.app`, with their version, state, addresses and ports, and an *Open* link for the exposed `http` and `https` ports. The log files of a definition link to the **Logs** tab of the jail they were installed as, which reads the jail logs API (tail, since, text filter, follow).

## How the installation works

`jailops app install NAME [--name JAIL] [--network N] [--storage S] [--project P]` is one operation with the usual
journal: `POST /api/v1/apps/NAME/install` answers `202` with the operation, and the client waits for it. The steps, in
this order, each a step of the operation's plan (`--dry-run` returns them and does nothing):

1. **Create a thin jail** from the image `--image` names, or else from the only FreeBSD image with the alias `stable`
   (the quick start's `freebsd-15@stable`; with none or several the request is refused before the operation and the
   hint lists the images: name one with `--image`). The image must be a FreeBSD image whose release is at least
   `image.release_min`, compared major.minor. The jail is created in the project, on the network and storage named
   (the project's only grant of a kind is the default), with the `data` paths as **volumes** of the jail: one dataset each, named after the
   path (`/var/db/postgres` becomes the volume `var-db-postgres`), class data, mounted at the path, snapshotted,
   rolled back and backed up with the jail, and untouched by a generation switch like `/usr/local` and `/var/db`. The
   datasets start empty and root-owned; the definition's `setup` chowns them. The labels `jailops.app=<name>` and
   `jailops.app.hash=<hash>` are set at creation. The `resources` hints become the jail's limits unless `--memory`,
   `--disk` or `--cpu` replace them. The jail is then **started**.
2. **Install the packages from the host** with `pkg -r <root> install -y <names>` through the privileged helper, from
   the FreeBSD repository (`repo: ports`, the host's own repository configuration) or from `pkg.jailops.net`
   (`repo: jailops`: `pkg -r <root> install -y -r jailops <names>`, the repository named `jailops` in the host's
   `pkg` configuration, the one [install.md](install.md) sets up). `-r jailops` limits the whole installation to that
   repository, dependencies included: a JailOps package that needs packages from the FreeBSD repository installs only if
   the definition lists them as `repo: ports` packages too, which go in first (the `ports` group is installed before
   the `jailops` group). The installer assumes the repository exists on the host and does not check it beforehand; a
   host without it fails at this step with pkg's own message, and the jail is kept stopped. `pkg -r` does not run the jail's library trigger, so the
   library cache of the running jail is refreshed with `/sbin/ldconfig -m /usr/local/lib` in the jail. The version that was installed is read with
   `pkg -r <root> query %v <first package>` and becomes the label `jailops.app.version`.

   **Ownership.** `pkg -r` extracts the files from the host. A package that creates a user in its install script and
   lists directories owned by that user (`/var/log/valkey`, `/var/run/valkey` belong to `valkey`) gets them
   **root-owned** in the jail, or owned by a number that is the host's idea of that user, depending on the package,
   because the user exists only in the jail's password file. Restoring the owners generically is not possible from what
   the packages leave behind: pkg's local database records the paths and checksums of files and the paths of
   directories, not their owners or modes, so `pkg query` cannot report them and `pkg check` has nothing to re-apply.
   A definition therefore gives the directories its services write to their user in `setup`, with `mkdir -p` first for
   a directory that may not exist and `chown` (the starter catalog: `valkey:valkey` on `/var/db/valkey`, `/var/log/valkey`
   and `/var/run/valkey`; `git:git` on `/usr/local/etc/forgejo`, `/var/db/forgejo` and `/var/log/forgejo`;
   `postgres:postgres` on `/var/db/postgres`; `prometheus:prometheus` on `/var/db/prometheus`; `isms:isms` on
   `/var/db/isms`, `/var/db/isms/private` and `/usr/local/etc/isms/server.env`; `nsd:nsd` on `/var/db/nsd` and
   `/var/run/nsd`). The `data` volumes start root-owned anyway (step 1). What a package's install message leaves to the
   person is the definition's to do as well: the `mail` definition writes `/usr/local/etc/mail/mailer.conf` itself, and
   runs `postfix set-permissions` to give Postfix's programs and queue the owners, groups and setgid bits its own
   file list names.
3. **Enable the services**: `<service>_enable="YES"` and the `rc` knobs are written into the jail's `/etc/rc.conf`
   (read and written through the helper's file operations, below the jail root; a line that already sets a variable is
   replaced, the others are kept). Every value is written double-quoted with `\`, `"`, `$` and the backquote escaped,
   so nothing is read as shell syntax; no `sysrc` and no shell is involved. The jail's own `rc.conf.local` (the network
   JailOps renders) is untouched.
4. **Run the `setup` commands** in the jail, in order, with the in-jail exec of the helper (`jexec`, argv only, no
   shell, 30 minutes each at most); the first failure ends the installation.
5. **Start the services** with `/usr/sbin/service <name> start` in the jail, in the order of the definition, those
   with `start: true`. The command runs through a fixed wrapper, `/bin/sh -c '<script>' jailops-start <name>`, whose
   script is a constant and takes the service name as its positional parameter, never as text:

   ```sh
   out=$(/usr/bin/mktemp -t jailops) || exit 1; /usr/sbin/service "$1" start </dev/null >"$out" 2>&1; rc=$?; [ $rc -eq 0 ] || /bin/cat "$out"; /bin/rm -f "$out"; exit $rc
   ```

   An rc script that starts its daemon through daemon(8) without `-f` (forgejo's) leaves a supervisor that keeps the
   descriptors it was given; given the pipes of the in-jail exec, it would keep them open and the step would wait for
   the end of an output that never comes. With the wrapper the rc script and everything it starts write to a temporary
   file in the jail; when the start fails, the file is printed and the error names `/usr/sbin/service <name> start` with
   that output. The service name is checked against `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` before the command is built.
   The provisioning `services.start` step of a FreeBSD jail uses the same wrapper. `setup` and `health.exec` commands
   stay plain argv: they are the definition's own commands, without a shell. The jail's own start (`jail -c` running
   `/etc/rc`) is detached the same way by `exec.consolelog` in its `jail.conf`: the output of `/etc/rc` goes to
   `/var/log/jail_<project>_<jail>_console.log` on the host (design section 6).
6. **Run the health check**: `tcp` connects from the host to the port on the jail's first address, `http` requests
   the path there (plain HTTP) and expects the status, `exec` runs the command in the jail; the check is retried once a
   second until it passes or `wait_seconds` have passed. Each attempt is given at most 5 seconds, and never more than
   the time left, so the step ends within `wait_seconds` and one attempt.
7. **Take the snapshot `app-installed`** of all the jail's datasets: the point to roll back to before the first
   change (`jailops jail rollback JAIL --all --snapshot app-installed`).
8. **Say how to reach it**: the jail's addresses with the `ports` (`endpoint` is `<first address>:<port>`), the
   `data`, `config` and `logs` paths, and the `notes`. Ports marked `expose: true` are recorded as such and nothing
   more: forwarding them through the jail's PF anchor (`proxy` devices) is a later feature, so every port is reachable
   from the jail's network only.

The events `app.installed` and `app.install.failed` (source `/api/v1/jails/<jail>`) end it, after the ordinary
`jail-created`, `jail-started` and `jail-snapshot-created` of the steps.

**While the installation runs**, the jail refuses every exec, shell and console session (`busy`, "the app is being
installed"): `pkg -r` works on the jail root from the host, as root, and nobody else may change that tree meanwhile.
The packages' install scripts run as root chrooted into the jail root; the app's services are not running during the
installation (they are enabled and started only afterwards). See [security-model.md](security-model.md).

**What a failure leaves behind.** A failure before the jail's first start (the creation itself, also after the jail was
made, and the start) undoes the creation: the jail and its datasets are deleted again, nothing is left. A failure after the start (a package that does
not install, a setup command or a service that fails, a health check that does not pass within `wait_seconds`) ends the
operation with the jail **stopped and kept** for inspection, with its labels (`jailops.app.version` once the packages
went in): look inside it with `jailops start JAIL` and `jailops shell JAIL`, and `jailops delete JAIL` before trying
again. The error of the operation names the step (`detail.step`), the jail (`detail.jail`) and whether it was kept
(`detail.jail_kept`), and its hint says what to do. A daemon restart in the middle of an installation does the same from
the journal: a jail whose start never completed is deleted (a start that was cut short is rolled back first), one that
was started is stopped and kept.

| Refused before the operation starts | Code |
|---|---|
| the app is not in the catalog | `not_found` |
| the definition is invalid (listed with the reason) | `invalid` |
| the jail name is not a name, or the body names another project | `invalid` |
| a jail of that name exists in the project | `already_exists` |
| no network or storage named and the project is granted none or several | `invalid` |
| the network or storage is not granted to the project | `forbidden` |
| the image does not exist, or the project does not allow it | `not_found` |
| no image named, and no FreeBSD image or more than one has the alias `stable` | `invalid` |
| the image is not FreeBSD, or its release is below `release_min` | `invalid` |
| a project quota would be exceeded | `quota_exceeded` |
| the role is below operator, or the identity has no project scope | `forbidden` |

Generated secrets (a database password written into a configuration file) are **not in this version**: a
definition has no parameters, and what needs a value is a `config` file the person edits, as `notes` says.

## Logs, updates, upgrades

- **Logs.** A definition declares its log files and directories; reading them is a separate feature (the jail logs
  API) and the catalog only says where they are.
- **Updates.** The packages of an app follow the jail's ordinary update policy ([upgrade.md](upgrade.md)):
  `updates.packages` upgrades them with `pkg upgrade` inside the jail, `updates.os` moves the jail to a newer image
  generation with the data datasets untouched. There is no separate app update; the catalog's `version` is what the
  definition was written against, not a pin.
- **Rollback.** `jailops jail rollback JAIL --all --snapshot app-installed` is the way back to the fresh installation.

## Deliberately out of scope

- **Parameters** beyond generated secrets: a definition has no variables and no templates. What needs a choice (a
  domain, a port, a password policy) is a `config` file the person edits, and `notes` says so.
- **Dependencies between apps** (a forge that needs a database): each app is one jail; connect them by hand.
- **Multi-jail apps**: one definition, one jail.
- **Fetching catalogs**: the catalog comes with a package, signed, or from the operator's own files. The daemon makes
  no request for it.

## The starter catalog

`packaging/apps/` holds eight definitions. The first five were written against the FreeBSD 15 quarterly packages of
October 2026: package names, rc.d names, rc.conf knobs, ports and paths were taken from the packages themselves. The
three later ones were not: their sources are named in the table and below, and what could not be checked is marked.

| App | Category | Packages (repository) | Ports | Health check | Source of the facts |
|---|---|---|---|---|---|
| `postgresql` | database | `postgresql18-server` (ports) | 5432/tcp | `pg_isready` | the package |
| `nginx` | web | `nginx` (ports) | 80/tcp, 443/tcp | HTTP `/` | the package; ran on a host |
| `valkey` | database | `valkey` (ports) | 6379/tcp | `valkey-cli ping` | the package |
| `forgejo` | devops | `forgejo16` (ports) | 3000/tcp | tcp 3000 | the package |
| `prometheus` | monitoring | `prometheus3` (ports) | 9090/tcp | HTTP `/-/healthy` | the package; ran on a host |
| `isms` | security | `isms` (jailops), `openssh-portable` (ports) | 8080/tcp (UNVERIFIED) | none: not started | the package recipe of the JailOps repository, its `server.env.sample` and install message |
| `nsd` | network | `nsd` (ports) | 53/udp, 53/tcp | tcp 53 | NSD's documented configuration; the FreeBSD package not inspected |
| `mail` | communication | `postfix`, `dovecot` (ports) | 25, 587, 143, 993 (tcp) | tcp 25 | Postfix's and Dovecot 2.3's documented configuration and the Postfix package's install message; the packages not inspected |

**isms** (featured: the platform's own information security management system) installs the package `isms` from the
JailOps package repository (and `openssh-portable` from the FreeBSD packages, because the thin image has no `ssh-keygen`, which the signing key needs): `pkg -r ROOT install -y -r jailops isms`, which assumes the host's pkg configuration has the
repository named `jailops` (the one [install.md](install.md) sets up for JailOps itself). The package has no
dependencies. The paths (`/usr/local/bin/isms`, data `/var/db/isms`, `/var/db/isms/private` for the signing key,
`/usr/local/etc/isms/server.env` mode 0600 owner `isms`, log `/var/log/isms.log`), the user `isms`, the rc.d service
`isms` (daemon(8), restarted 5 seconds after it exits) and the first-run steps come from the package recipe and its
install message. The definition enables the service but does not start it, and generates nothing: isms needs a
PostgreSQL database, and its master secret `ISMS_SECRET` is empty until the operator sets it (isms refuses to start
without it). The setup only gives the data directory, the key directory and `server.env` back to `isms` with the modes
the package intends. Its `notes` are the manual steps: a role and a database in a `postgresql` jail with a `pg_hba.conf`
rule for the isms jail's address, `DATABASE_URL`, `ISMS_BASE_URL`, `ISMS_SIGNING_EMAIL` and the secret in `server.env`,
the signing key, `isms server migrate` as `isms`, then `service isms start`. Connecting two apps by hand is the current
state; the coming `jailops app allow` flow is meant to make the role, the password and `DATABASE_URL` itself. The end-to-end test installs it, runs these steps
against a `postgresql` jail and sees port 8080 answer with a 2xx or 3xx status. **UNVERIFIED**: what exactly the program
answers there, and therefore the health path, which the definition leaves out (8080 is the default of `isms server
serve`; the FreeBSD service passes no `--addr`). Until `server.env` is complete, a restart of the jail starts the
service, which exits and is restarted by daemon(8) every 5 seconds with the reason in `/var/log/isms.log`.

**nsd** is the authoritative DNS server NSD (`dns/nsd`), chosen because one program is both a primary (it sends NOTIFY
and answers zone transfers) and a secondary (it requests them) with a short configuration. The setup writes a starter
`nsd.conf` and one zone, `example.com`, under `/var/db/nsd/zones` (the package's sample configuration is replaced, a zone file that exists stays), with the state files
on the volume, no database file and no remote control; it gives `/var/db/nsd` and `/var/run/nsd` to `nsd`, then runs
`nsd-checkconf` and `nsd-checkzone`, so a configuration the packaged release rejects ends the installation at that
step. The ports are 53/udp and 53/tcp; the definition format has had `proto: udp` since version 1, and ports are recorded
only (forwarding is a later feature), so nothing else changes for UDP. The `notes` give the lines for a primary
(`notify`, `provide-xfr`) and a secondary (`allow-notify`, `request-xfr`) and recommend a TSIG key. The end-to-end test
installs it, runs `nsd-checkconf` and asks the jail for the SOA of `example.com` over UDP and over TCP. **UNVERIFIED**:
that NSD logs to syslog facility daemon (`/var/log/daemon.log`) and a zone transfer between two jails.

**mail** is a starting point, said so in its summary: Postfix (SMTP 25, submission 587) and Dovecot 2.3 (IMAP 143; 993
disabled until a certificate is configured) for one domain, `example.com` until changed, with system users whose homes
lie on the volume `/home/mail` (mail goes to `~/Maildir`), the queue on the volume `/var/spool/postfix`, and no TLS,
spam filter, DKIM signing or webmail. `sendmail_enable="NONE"` keeps the base system's MTA off; the setup copies
`main.cf` and `master.cf` from their samples when missing, sets the domain and Dovecot authentication for submission
with `postconf -e`, `-M` and `-P`, runs `postfix set-permissions` and then `postfix check`, builds the aliases with
`postalias`, writes `/usr/local/etc/mail/mailer.conf` so that `/usr/sbin/sendmail` is Postfix's, writes `dovecot.conf`
when absent and checks it with `doveconf -n`. The `notes` add a user, set the domain, add TLS to both servers, and list
the DNS records (A/AAAA and PTR, MX, SPF, DMARC; DKIM needs a signing service this app does not include). The end-to-end test installs it
(the packaged Dovecot is 2.3, so the starter configuration uses its syntax; `doveconf -n` fails the installation if it
is not), starts both services and reads the Postfix `220` and Dovecot `* OK` greetings. **UNVERIFIED**: PAM
authentication of system users through `/etc/pam.d/other`, delivering a message to a local user and reading it over
IMAP, and TLS.

The catalog itself is validated by a test on every build, and the installation is tested
end to end against in-memory fakes of ZFS, the jail runtime and `pkg`. What is checked against the privileged helper's
real rules: the argument vectors the builders of `internal/apps` make (`pkg -r ... install`, `pkg -r ... query`), the
rc.conf path and the in-jail commands, in `internal/priv`; the install tests assert that the service sends exactly those
vectors. What is not: the service's own call sites are not run through the helper, and the whole flow on FreeBSD is left
to the end-to-end test below.

**Testing limits.** The fake `pkg` accepts any argument vector with `-r`; the tests compare what it received with the
builders' output instead. The fake log reader keeps the helper's path rules (a jail's `/var/log`, names two segments deep at
most) but not its symlink and file-type checks, which `internal/priv` tests on a real file system. The ZFS fake does not
model a dataset mounted under an unmounted parent; the tests assert `canmount=noauto` on the volume datasets instead.

**What ran on a FreeBSD 15.1 host:** `nginx` and `prometheus` installed and ran. That run found the three problems
this version addresses: PostgreSQL's `initdb` failed without System V shared memory (`sysvipc`), Valkey could not open
its log file in a root-owned `/var/log/valkey` (the `setup` lines above), and forgejo failed because the daemon(8)
supervisor of its rc script held the pipes of the in-jail `service forgejo start` and, at the next start, of `jail -c`
(the start wrapper and the console log).

Verified on a FreeBSD host with the end-to-end test: PostgreSQL with `sysvipc`, Valkey with its setup lines, forgejo with
the start wrapper and the console log, and `isms`, `nsd` and `mail` as far as stated above. The end-to-end test
(`scripts/ci-freebsd-e2e.sh`) installs nginx, fetches its default page from the host and stops it; with
`JAILOPS_E2E_APPS="postgresql valkey prometheus forgejo nsd mail isms"` (or any of them) it then installs each named
app in its own block and checks it: `select 1` through `psql` as `postgres`, `valkey-cli ping` answering `PONG`,
Prometheus' `/-/ready`, forgejo's port 3000 answering 200 or a redirect to its installer; for `nsd`, `drill` from the
host asks the jail for the SOA of `example.com` over UDP and over TCP and `nsd-checkconf` passes in the jail; for `mail`,
Postfix's `220` greeting on port 25 and Dovecot's `* OK` on port 143 (`nc` that sends `QUIT` or `LOGOUT` one second after connecting, because Postfix refuses commands that arrive before its greeting) and the
`sendmail` line of `mailer.conf`; for `isms`, the steps of its notes with a `postgresql` jail of its own (`isms-db`):
role, database, `listen_addresses`, a `pg_hba.conf` rule, `server.env`, the secret, the signing key, `isms server
migrate`, `service isms start`, and port 8080 answering a 2xx or 3xx status (curl retries while the program starts).
None of these checks reaches the internet. `isms` needs the `jailops` repository in the host's pkg configuration; the
script refuses to start (exit 2) with the reason when it is missing. Then it lists the jail's log files and stops the
jail. CI does not set the variable.
