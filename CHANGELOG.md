# Changelog

## Unreleased

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
