# Changelog

## Unreleased

- **`jailops shell`, `jailops exec` and the console no longer end a session after 30 minutes without traffic or 12 hours.** `limits.exec_idle` and `limits.exec_max` are off unless set (unset or `0`: no limit, like an ssh login); a positive duration is enforced as before and a negative one is a configuration error. A quiet long command such as `pg_dump | gzip` or `zfs send` is no longer killed; revoking the credential still ends a session. (#203)

## 0.9.6 - 2026-11-25

- **`jailops jail apply` takes many jails.** `-l KEY=VAL` (`--label` is the same) selects the jails that carry the labels and `--all` every jail of the project, as `jailops jail base add` does; a name and a selection exclude each other, and a Linux jail is not part of a selection. The recipe is rendered once (a file of `--set-file` is read once) and each jail is done on its own, one after the other, with one block of result per jail; a failure on one jail is reported as `jail/NAME: failed: ...` and the others go on, and the exit code is non-zero if any failed. `--dry-run` and `--validate` work with a selection. A jail that sync manages is named in the warning.
- **Recipes in Sync profiles.** A `Profile` may carry `recipes:`, `needs:` and `vars:` as a jail does, and every jail that lists the profile gets them: the recipes of the profiles run before the jail's own, the needs are added, the vars are merged with the jail's own winning. They are checked offline by `validate` and hashed like the jail's own recipes, so a changed profile shows in `plan` and `drift`. A recipe named by both a profile and the jail is an error; a jail is never its own need.
- **Recipes take `requirements:`.** A list before `steps:`; each item is `check: [argv...]`, a command run inside the jail (exit status 0 means met), and `message:`, shown when it is not; `${name}` works in both and a secret variable does not. All requirements are checked before the first step, and when any is not met the apply stops with the new error `requirement_failed` (HTTP 422, exit status 1) that names each unmet one and the exit status of its check: no step ran and nothing was changed. `--dry-run` runs no check and lists them as `not checked`; `--validate` checks the syntax offline. Requirements are in the hash of a recipe in sync, a failed one leaves the record unwritten so the next run tries again, and `jail apply -l`/`--all` reports it per jail. Additive in the API and SDK (`Recipe.Requirements`, `ApplyResult.Requirements`, `RecipeValidateResult.Requirements`, `CodeRequirementFailed`). Recipes are for FreeBSD jails only: the engine refuses a Linux jail that is named, a selection leaves Linux jails out, and sync skips a Linux jail's recipes (also those of its profiles) without error, `validate` notes it once.

## 0.9.5 - 2026-11-24

- **Recipes take variables, have a `dir` step and can be checked without a jail.** A recipe declares `vars:` (`default`, `required`, `secret`, `split`) and uses `${name}` in the values of its steps and in files that say `template: true`; `$${` is a literal `${` and `$$` stays as it is. Values come from `--set NAME=VALUE`, `--set-file NAME=FILE` and `--vars FILE` (`--set` beats `--vars` beats the default); the client fills them in and the daemon receives the finished recipe. A `secret` is given only with `--set-file` and goes only into the content of a template file; the request is then `sensitive` (`ApplyRequest.Sensitive`, additive): the daemon records no file content and drops the command output of every step from results, errors, operation records and the audit log. `jailops jail apply [NAME] -f DIR --validate` renders and checks a recipe on the client, with no jail name and no connection; `POST /api/v1/recipes/validate` (operator, extension `recipe_validate`, `Client.ValidateRecipe`) does the same for a rendered recipe, and its messages name the step and the field, never a value. The new step `dir` (`path`, `mode`, `owner`, `group`) makes one directory, never recursively, and changes an existing one only in what it names; the directories a `file` step creates are `root:wheel` `0755`. A failed step is the error `step_failed` (exit status 1, HTTP 422) with the step, the exit status and the output (not for a sensitive request), and an unquoted `run: [false]` is a YAML error that says to quote it. A temporary file that a killed writer left in a directory is deleted by the next write there. `docs/provisioning.md` lists the files JailOps writes in a jail (managed files, managed sections, seeded files).
- **`jailops apply -f` creates a `Volume`.** A volume that is missing is made, one that exists with the same spec is unchanged, a comment, note or label change is updated, and a file that asks for another storage, size, owner, mode or encryption of a volume that exists is refused: a file never re-owns or resizes data. An encrypted volume is made with `jailops volume create --encrypted`, which asks for the passphrase.
- **Apps are gone: a jail runs what a recipe says.** Gone: the application catalog and everything that served it. Commands `jailops app` (`list`, `info`, `install`, `installed`, `allow`, `deny`, `access`, `export`, `new`, `validate`) and the flags `--app`, `--app-logs` of `jailops logs` and `--no-app-logs` of `jailops logs forward`; the daemon's `apps:` section of `jailopsd.yaml` (`catalog_dir`, `local_dir`; a file that still has it, or `no_app_logs` under `logging.forward[].sources`, starts as before: the key is ignored) and the `jailops app list` line of `jailops-setup`; the routes `GET /api/v1/apps`, `GET /api/v1/apps/{name}`, `POST /api/v1/apps/{name}/install`, `GET /api/v1/jails/{name}/app-draft`, `GET /api/v1/jails/{name}/access`, `POST .../access/allow` and `.../access/deny`; the kinds `App` and `AppAccess` of the fleet repository (`projects/<p>/apps/`, `app-access/`) and of the state; the extensions `apps`, `app_install`, `app_draft` and `app_access`; the operation `app.install` and the events `app.installed` and `app.install.failed`; the labels `jailops.app`, `jailops.app.version` and `jailops.app.hash`; the check `apps.access` and `apps.local_dir` of `jailops doctor`; the console's Apps and Access pages, the installation dialog and the last step of the first-boot wizard ("Install your first app"); the package `jailops-apps` of the repository. A firewall policy that app access wrote (`app-NAME`) is an ordinary policy now: it can be changed, attached and deleted like any other. **A policy `app-NAME` left by an earlier app installation is attached to nothing now:** attach it with `jailops firewall policy attach` (or delete it) to keep a jail closed. A rule that names a jail (`jail:NAME`) still keeps that jail from being deleted or renamed until the rule changes. The logs of a jail are `messages` or the files you name: the `app` field is gone from the merged view, from the syslog structured data and the webhook lines of log forwarding. **What replaces it is `jailops jail apply NAME -f RECIPE`** ([provisioning.md](docs/provisioning.md#2-applying-a-recipe-to-a-jail)): create the jail, a volume and a firewall policy with the usual commands and put the packages, files, services and commands of the software in a recipe; `examples/recipes` has nginx and PostgreSQL. **SDK, a break before 1.0:** removed `Apps()`, `Jails().AppDraft`, `api.App*` (`App`, `AppSummary`, `AppDetail`, `AppInstall`, `AppInstallResult`, `AppDraft`, `AppAccess`, `AppAllow`, `AppDeny`, their results and parts), `api.KindAppAccess`, `api.AppAccessManagedBy`, `api.LabelApp`, `api.OpAppInstall`, `LogsQuery.App`, `.AppLogs`, `LogEntry.App`, `LogSource.App`, `LogSinkSource.NoAppLogs` and `FirewallTestRule.Managed`; the test server (`sdk/fake`) has no app routes. The previous-client baseline of the compatibility test is taken again from this client.
- **The prompt in `sh` shows the tag, and an existing jail gets the fix.** In FreeBSD's `sh` the prompt showed the text `${_jailops_where...}` instead of the tag `[project/host]`. The prompt file now puts the values into the text when it is read, a value that is not a word of lower-case letters, digits and hyphens (one the shell inherited) is dropped, and the block in root's `.shrc` also sources the variables file, so a `sh` that is not a login shell (`jailops shell`, `jexec`) shows the tag. Running jails get the new files at their next start, or at once with `jailops jail shell-setup NAME`; the daemon does not change a running jail by itself.
- **Recipes: fixes.** A `file` owner or group that an earlier `run` or `package` step created is found (the jail's `passwd` and `group` are read again on a miss). A `source:` that is a link leading out of the recipe directory is refused; one that stays inside is read. A `file` step on a symbolic link says so and does not follow it. A mode with set-id or sticky bits (`4755`, `1777`) is refused; a mode is three octal digits, or four with a leading `0`. `sysrc` keeps the mode and owner of an existing `rc.conf` or `periodic.conf`. `/usr/lib32`, `/usr/libdata` and `/usr/tests` are base directories a `file` step does not write. A failed `chmod` or `chown` while files are copied into a jail is an error and no longer reported as written.
- **`jailops-sync` keeps recipes.** A Jail file lists `recipes: [{path, vars, secrets}]` (a directory of the repository below `recipes/`, a git submodule works the same), `needs: [jails]` and `vars:` shared by its recipes. Sync renders each recipe on the client with the same code as `jailops jail apply`, compares a hash (the recipe, its files and a fingerprint of each secret) with the label `sync.jailops.net/recipe.NAME` of the jail and looks inside a running jail with a dry run: when the hash changed or is missing, when a readable step would change, and otherwise at most once per `drift_interval` of `fleet.yaml` (default 1h; `plan` and `drift` always look). The label is removed before the apply and written after its last step, so a run that fails halfway (a reload) is applied again at the next run. A stopped jail waits and is never started; a recipe waits for the jails it `needs`; a failed recipe stops its jail and its dependents, the rest goes on, exit 1. `drift` exits 1 only for drift: `unknown` and `waiting` are printed (text and `--json`, new on `plan`, `drift` and `sync`) and exit 0. `validate` renders and checks every recipe offline. A secret variable is a reference into the age or SOPS store under `secrets:`, never a value. `jailops jail apply` warns when the jail carries a sync label ("the jail is managed by sync, the next sync may revert this"). **Gone: the `after:` hooks of Jail and JailTemplate** (`start`, `restart`, `snapshot`, `exec`); `start: true` stays, and what `exec` did is a recipe. `watch` runs `git submodule update --init` after its pull when the repository has submodules. `docs/fleet-sync.md`, `examples/recipe-fleet`.

## 0.9.4 - 2026-11-24

- **`jailops jail apply NAME -f RECIPE` runs a recipe in a running jail, and replaces `spec.provision` and `jailops provision`.** **If a jail's `spec.provision` ran at its first start, move its steps into a recipe and apply it after the jail runs: nothing provisions a jail by itself any more, and `jailops provision` and `--again` are gone.** A recipe is a YAML list of steps (`package`, `file`, `sysrc`, `service`, `run`) that run in order and change only what differs, so a second run changes nothing; each step is reported `changed`, `unchanged`, `skipped`, `executed` (a `run` without `creates` or `unless`) or `failed` with its output. `--dry-run` runs none of the recipe's commands: it reads the state (`pkg query` on the host, file contents, `rc.conf`, and `service NAME status` in the jail) and writes nothing. It never restarts a jail or a service, and a project operator may run it. A `file` step is refused under `/etc` and the other base directories (local configuration goes to `/usr/local/etc`), except four files FreeBSD expects an administrator to edit: `/etc/sysctl.conf`, `/etc/periodic.conf`, `/etc/ssh/sshd_config` and `/etc/newsyslog.conf`; `rc.conf` and `periodic.conf` variables are set from the host with a `sysrc` step, so the jail needs no `sysrc`. [provisioning.md](docs/provisioning.md)
- **Every file of `/usr/local/etc/profile.d/*.sh` is read by bash, and by a login sh.** The block JailOps keeps in `/usr/local/etc/profile` and `/usr/local/etc/bash.bashrc` sources the whole directory (it named only `jailops-env.sh` and `jailops-prompt.sh`), so an alias or a prompt line put there by a recipe applies to login and other interactive bash; `jailops jail shell-setup NAME` gives an existing jail the new block. [shell.md](docs/shell.md#files-in-profiled)
- **FreeBSD jails run at `securelevel` 2 by default.** A jail that sets none gets `securelevel = 2;` in its `jail.conf` at its next start: root in the jail can no longer lower its level, and the system flags, raw disks, kernel modules and the clock stay closed to it when a device or a parameter is widened. Upgrades, `jail base add`, ports packages, services and ZFS delegation work as before. The new jail key `securelevel` (-1 to 3; -1 is the host's level) and `--securelevel` of `create` set another level, `jail show` prints the one a jail runs at, and Linux jails keep the host's level ([security-model.md](docs/security-model.md#the-securelevel-of-a-jail)).
- **`jailops image create --repo` works on FreeBSD 16.0-CURRENT.** The image build looked for the base repository's keys in `/usr/share/keys/pkgbase-16`, which 16.0-CURRENT does not ship. It now uses the key directory of the host's own `FreeBSD-base` repository (`/etc/pkg/FreeBSD.conf`, `/usr/local/etc/pkg/repos/FreeBSD.conf`) when that is the same kind of repository, then `pkgbase-MAJOR` or `pkg`. When none of these directories exists, the build fails and names the directories it tried; `--keys` still sets the directory explicitly.
- **JailOps never installs a shell.** The background installation of bash at a jail's start is gone, with its `Installing` state, its waiting in `jailops shell` and the event `jail-shell-setup-cancelled`; an app installation no longer adds bash. The `shell` key is the preferred shell: when it is not installed in the jail, `jailops shell` and the console fail (`precondition_failed`, "the shell /usr/local/bin/bash is not installed in jail NAME") with the hint `jailops pkg install NAME -- bash`, and the next session after that install starts it. The prompt is written at every start as before, and root's sh of `jailops shell` now reads `/root/.shrc` (`ENV`), so it shows the prompt. `jail shell-setup` writes the keys and the prompt and installs nothing; the console's button opens sh instead. `ShellSetupInstalling`, `ShellInstalling` and `ShellSetupStatus.step` stay in the API and are never set.
- **`jailops pkg` and `jail upgrade` are for FreeBSD jails.** A Linux jail is not part of a selection (several names, `-l`, `--all`); one named alone is refused. `jailops pkg install` lets FreeBSD's bootstrapper fetch pkg in a jail that has none yet (`ASSUME_ALWAYS_YES=yes`).
- **JailOps no longer updates the packages of Linux jails.** `update check`, `update apply`, the update schedule and the console's Updates tab no longer run `apk` in an Alpine jail: Linux jails are left out of every selection, and one named alone (also `update history`) is refused (`unsupported`, "Linux jails look after their own packages: JailOps does not update them"), as is setting or changing an update policy on one (`update.healthcheck` stays). A policy stored on a Linux jail by an earlier release loads and is ignored; a copy, import or restore leaves it out. Update a Linux jail from inside it, for instance `apk upgrade` or `apt-get upgrade` from its cron ([linux-jails.md](docs/linux-jails.md#packages)).
- **A jail's backup is restored and tested again.** Since 0.9.3 a backup holds the jail's root. `jailops backup verify --depth extract` (and a drill at that depth) failed on the immutable files of its base (`schg`) with "file flags of …/lib/libc.so.7: operation not permitted": the test checks content and now sets no file flags. `backup restore` and `backup verify --depth full` failed with "fsagent: frame of N bytes": the file agent was sent each directory with its whole subtree. A restore sets the flags of every entry once the whole tree is written, so a hard link to an immutable file is no longer refused.
- **`jailops doctor` warns when the host's neighbor advertisements miss the router that asked (`network.nd_reply_path`).** On a switched segment with a VRRP pair of routers, a host that holds its address as a `/128` answers a router's unicast solicitation through its default route, to the gateway's MAC; the backup router never gets the answer and traffic through it stalls three or four seconds after idle. The capture of the NDP proxy now sees the uplink in both directions and records, per router that asks for an address of the host three or more times within five seconds, the router's MAC and where the answer went; `jailops network ndp-proxy status` prints it (`Host address`), `GET /api/v1/ndp` has `reply_path` and `uplinks[].host_solicits`, and the doctor names the router with the fix, an on-link route for the routers' prefix in `/etc/rc.conf` ([networking-ipv6.md](docs/networking-ipv6.md#answers-to-the-providers-routers)). It only observes.
- **The resolver address of a deleted network no longer blocks a new network with the same subnet.** The address on `lo0` came off only at the next sync of the names; the helper now also takes off the addresses no network accounts for when it starts and when the names are off, and creating a network no longer counts its own resolver address on the loopback as an overlap ("subnet fd91:1::/64 overlaps fd91:1::1/128").
- **A failed `app install` finishes its rollback.** An installation that failed after the jail started (a service that did not start, the health check) unmounted the app's volumes a second time, which the helper refuses: the hint said the rollback was incomplete and `jailops doctor` failed `operations.journal` until the record was removed by hand. The jail is kept stopped as before and the run leaves the journal.
- **`jailops list -v` shows UPTIME and STARTED for jails that ran before the daemon started.** The start time is taken from the oldest process of the jail when `jailopsd` starts.
- **`jailops jail upgrade` says "up to date" and what it checked.** A jail with nothing newer is `up to date` (it said "nothing to upgrade", with the release in the reason column); one jail also lists what was checked (`N base packages are current in the repository of RELEASE`, `the ports packages are current`), and several jails leave that to the summary table. A ports-only upgrade shows `ports packages (RELEASE)` in the change column. The warning that volumes are not rolled back with the jail is gone: a jail with volumes is no different for the upgrade. The check of a pkg configuration for plugins reads it beneath the open root, so no link in the path can lead it out of the root.
- **A new image has `sysrc`.** The minimal jail set on FreeBSD 15 has no `sysrc` (it is in `FreeBSD-bsdconfig`, 1 MiB, which needs only the runtime), so an administrator in a new jail found `sysrc postgresql_enable=YES` missing. A new 15 image adds `FreeBSD-bsdconfig`; on 16 `sysrc` is in `FreeBSD-rc`, which the set holds. An existing jail gets it with `jailops jail base add NAME FreeBSD-bsdconfig`, and every jail of the project with `jailops jail base add --all FreeBSD-bsdconfig`: `jail base add` selects jails like `jail upgrade` does (`-l KEY=VAL` or `--all`, one after the other, a failure is reported and the run goes on).
- **`jailops jail audit` reports what it can show, and `--suggest` adds the rest.** By default the checks are `PASS`, `WARN` or `CRITICAL` (`INFO` only for a check that could not be made) and follow the FreeBSD Handbook and the CIS FreeBSD Benchmark; new are `pkg.kmods_repository` (`FreeBSD-ports-kmods` enabled in a jail is useless: the kernel is the host's), `pkg.vulnerabilities` (pkg audit of a copy of the jail's package database with the host's `/var/db/pkg/vuln.xml`, never fetched), `base.updates` (from the last update check), `files.sticky_bit`, `files.world_writable`, `files.unowned` and `jail.enforce_statfs`. The notes (periodic, cron jitter, `rc.conf`, root locked, shell, hosts block, volumes `nosuid`, disk limit and size, securelevel, `allow.*`, devfs ruleset) come only with `--suggest` (`suggest=true`, `suggestion: true` in the answer) and never count. `ports.listening` is gone, and no fix uses `sysrc`. See [audit.md](docs/audit.md).
- **A live volume mount is harder to redirect.** FreeBSD mounts by path (no mount through a descriptor), so root in a running jail can still swap a directory of the mount point for a link between the helper's check and the mount. The mount is now `nocover` (never over the root of another file system; the helper refuses a volume mount without `nosuid,nocover`), the after-check also undoes a mount on the right path from another source, and a stray mount is undone by its file system ID (`unmount(2)` with `MNT_BYFSID`), which no second swap can redirect ([security-model.md](docs/security-model.md)).
- **Every attachment of a host directory is admitted on its own.** A project administrator kept only what the first attachment of the same name allowed: a second attachment of the same host directory (also through another volume of it) that moves it or makes it writable now needs a global administrator, and a reordered list is no longer refused.
- **`jail rollback` no longer destroys newer snapshots unasked.** A snapshot taken after the target (by a backup, a replication or by hand) makes the rollback refuse (`precondition_failed`) and name them (`later_snapshots`); `--destroy-newer` (`destroy_newer`, with `confirm_data_loss`) destroys them on the root with the rollback, and only there: a snapshot of the same name on another dataset of the jail stays. The used snapshot goes from every dataset of the jail, as before.
- **`jailops doctor` checks the stock devfs rulesets (`jail.devfs_ruleset`).** A rule that ruleset 4 (or 1 to 3) has beyond FreeBSD's, such as `bpf*`, `pf` or `dri`, is a warning that names the jails that get it; a ruleset 4 that is not loaded fails, since it hides nothing. The helper's `devfs` grammar adds the read-only `rule showsets` and `rule -s 1..4 show`.
- **A jail's ICMPv6 errors no longer pass ahead of its policies.** An error the jail sends goes with the state of the connection it is about, any other is decided by its policies, so a jail whose policy denies everything cannot send errors to any address. Errors into a jail still always pass. The pass rules of IPv6 jails change their labels, so their established flows end once when the new anchors load.
- **The helper matches the rules on an uplink whole.** A translation, a forward or a pass on the uplink that carries options of its own (`route-to`, `dup-to`, `tag`, a label with text) is refused, and a translation must name a stored network's subnet on its own uplink.
- **`jailops doctor` warns when an `rdr` or `binat` rule of `pf.conf` takes an exposed port (`pf.legacy_rdr`).** pf evaluates it before the forwards of `jailops expose`, so the exposure never took effect.
- **`jailops doctor` warns about uRPF on a multi-uplink host (`pf.urpf`).** A `from urpf-failed` block on an uplink of the jails while a default route leaves through another interface drops the clients of the exposures and `allow in` rules there.

## 0.9.3 - 2026-11-23

- **`jailops jail audit NAME`: a read-only audit of a jail.** One line per check, `PASS`, `INFO`, `WARN` or `CRITICAL`, with what was found and the fix; nothing in the jail changes and a running jail keeps running. The security checks: the detection an upgrade makes before it starts (a link out of the root where the host's pkg writes, a link among pkg's files, a `pkg.conf` with plugins) and an account with uid 0 besides root and toor are critical; root not locked or an empty password, set-id files no package owns, repositories that are not FreeBSD's, `FreeBSD-base` enabled inside the jail, programs and libraries of the base that differ from their packages' checksums, and sshd allowing root logins or passwords warn. The practice checks (periodic's world/kernel check and reports by mail, cron jitter, `rc.conf` knobs that do nothing in a jail, the shell and prompt, the hosts block, volumes without `nosuid`, the disk limit, the size of the root, ports listening without being exposed) never go beyond a warning. The exit status is 1 when a check is critical. The root helper reads the root from the host with the new read-only operation `jail.audit_root`, which returns no password hash. New: the route `GET /api/v1/jails/{name}/audit` (operator in the project, audited, extension `jail_audit`), `JailAudit`, `JailAuditCheck` and the enum `JailAuditResult` in the API, `Jails().Audit` in the SDK and the fake, the doctor check `jail.audit` (the jails whose last audit found something) and the event `jail-audit-critical`. See [audit.md](docs/audit.md).
- **`jailops list` shows the OS of each jail, and `-v` (`--verbose`) adds VERSION, UPTIME and STARTED**: the release installed in a FreeBSD jail (the image of a Linux jail), how long a running jail has run and since when. `-o wide` shows them too, `-c` and `--sort` take them, and a narrow terminal drops STARTED first. In the test server of the SDK a new jail takes the OS of its image, as on a host.
- **Established connections of the jails survive a change of the host's addresses.** The labels that end the flows of a changed rule hashed the members of `<host>`, `<foreign>` and `<uplink_nets>`, so a jail starting or stopping, a WireGuard tunnel going up or down or any project creating a network ended every established connection of every jail on the host. Those tables no longer enter the labels; a change to an address set or a tag that a rule names still does. A member added to `<host>` or `<foreign>` narrows the pass rules that name them negated (`to ! <host>`, `from ! <foreign>`): a connection such a rule would now exclude keeps its state until it ends. **The first pass after the upgrade to this release changes every label once, so the established flows through the jails end once, at that moment.**
- **Stale pf anchors are removed.** The anchors of a jail, project or network that no longer exists (they stayed after some failed or interrupted deletions) are flushed by the reconciler after two minutes, with their flows, and never the anchor of an object that exists, of a jail that runs, or when the state could not be computed. A freed address that is handed out again can no longer meet an old forward or pass rule. The warning that the anchor of a running jail is empty is logged once per watched anchor instead of every five seconds while an operation (a stop, an upgrade) has emptied it, and it names that cause next to a flush of pf ([troubleshooting.md](docs/troubleshooting.md#common-failures)).
- **A jail cannot send to the limited broadcast address** (255.255.255.255), which reached host services that listen on every address. **Its neighbour discovery** (NS and NA) goes only to link-local and solicited-node destinations, which are never forwarded off the link.
- **`jailops doctor` has four new checks:** `pf.host_default_deny` (the loaded ruleset has no inbound default block on an uplink of the jails, each address family on its own: `block in inet all` leaves IPv6 open, and the warning names the family; a jail with a routed address is then reachable from outside whatever `firewall test` says), `pf.states` (the per-rule state limits of the running jails against the host's table: a warning for a running jail without a limit or with one above half the table, information when the limits add up to more than the table holds), `zfs.block_cloning` (per storage: a jail is a full copy of its image, about 50 MB, on a pool that has not enabled `block_cloning`, with `vfs.zfs.bclone_enabled=0` and on an encrypted storage), and `jail.upgrade_state` (an upgrade whose undo did not finish; the jail also carries the condition `UpgradeIncomplete`, reason `RollbackFailed`). `firewall test`, `firewall show` and `firewall exposed` say that "closed" assumes the host blocks inbound.
- **Security review fixes.** Every `pkg -r` of the helper runs with `-C` naming an empty configuration file of its own and with plugins off, so the `pkg.conf` of the tree it works on is never read (a jail's root could otherwise load a plugin into pkg as root of the host). JailOps no longer installs bash into a jail (it did with `pkg -r` from the host, in a root the jail controls, with package scripts as root of the host). Making a host-directory volume writable or moving it, granting the `bpf` or `dri` device, and attaching a policy with a raw PF fragment need a global administrator, and a profile can no longer give a jail such a policy for anyone else. Every volume mount is `nosuid`, the live one made while a jail runs too. A live volume mount is checked after it lands by reading the mount table with `getfsstat(2)` and undoing a stray with `unmount(2)` (nothing from a path a tenant could have covered is executed), and the check fails closed.

- **Every jail is one root of its own.** A jail, FreeBSD or Linux and every app of the marketplace, is one ZFS dataset copied from an image generation (`copy_file_range`: with block cloning, OpenZFS 2.2 and later, about a second and a few MB; the generation is mounted for the copy and put back after); the image is only the seed, no jail depends on a generation, and a generation or an image can be deleted while jails made from it exist. A jail is upgraded in place (`jailops jail upgrade`, below) and rolled back to a snapshot; volumes hold what must outlive the root. Gone: the layout of a clone of a generation with `/etc`, `/usr/local`, `/var/db`, `/var/log`, `/var/mail`, `/var/spool`, `/home` and `/root` as datasets of their own), root swaps and `root.old`, the `/etc` merge, the pass that kept the base package records in step with a generation, and the commands, flags and fields that served them: `jailops jail convert`, `jailops jail diff`, `jailops jail repair-packages`, `--layout` of `create`, `jail create` and `jail set`, `--generation` and `--image` of `jail upgrade`, `--to` and `--no-jails` of `image upgrade`, the column USED BY of the generations; in the API `spec.layout` of a jail, `JailConvert`, `JailConvertResult`, `JailDiff`, `RepairPackagesRequest`/`Result`, `JailUpgrade.generation` and `.image`, `ImageUpgrade.no_jails`, `.project` and `.to`, `ImageUpgradeResult.jails` and `ImageUpgradeJail`, `ImageGeneration.refs`, `ImageUpgradeRun.jails_moved`, `JailStatus.rollback_expires_at`, the routes `POST /api/v1/jails/{name}/convert`, `GET /api/v1/jails/{name}/diff` and `POST /api/v1/jails/{name}/repair-packages`, the extensions `jail_convert`, `jail_diff` and `jail_repair_packages`, the operation actions `jail.convert`, `jail.repair_packages` and `image.move`, the events `jail-converted` and `jail-base-packages-repaired`, `layout` and `image` of an export manifest and its part kind `persistent`, the SDK methods `Convert`, `Diff` and `RepairPackages`, the condition `BasePackages`, the `generation` of a rollout of `jailops-sync` (a rollout upgrades every jail in place), and `lifecycle.persistent` of `jailopsd.yaml`; the helper no longer runs `etcupdate`, `zfs diff` or the base package record forms of pkg, and its operations `zfs.diff` and `fs.tree_diff` are gone. A stored spec, a fleet file or a `jailopsd.yaml` that still names one of these fields is read without it. `jailops image upgrade IMG` builds the next generation with newer packages for the jails made later and touches no jail. Upgrading a jail in place is a global administrator's operation (the route says so). A volume may not be mounted on `/bin`, `/etc`, `/lib`, `/libexec`, `/sbin`, `/usr`, `/usr/local`, `/var`, `/var/db` or `/var/db/pkg` (below them it may); a jail that has one from 0.9.2 starts as before, `jailops doctor` warns (`jail.volume_system_dir`), and its in-place upgrade is refused until the volume moves below that directory. A token that runs `jail upgrade` (a `jailops-sync` rollout, a script) needs a global administrator. Before the upgrade of the host, `jailops doctor` on 0.9.2 must be clean and no operation interrupted: a conversion, upgrade or rollback of 0.9.2 cut in the middle is repaired by 0.9.2 only.
- **Thin jails of 0.9.2 are no longer supported.** Convert each one before you upgrade a host: `jailops jail convert NAME --layout thick` with JailOps 0.9.2, or create the jail again and copy its data over ([upgrade.md](docs/upgrade.md#jails-with-an-unsupported-root-layout)). One left after the upgrade does not start; a running one keeps running until it is stopped, it can be deleted, its data is never touched, and everything else is refused with the fix. It carries the condition `UnsupportedLayout` (reason `EarlierRelease`), `jailops doctor` fails `jail.unsupported_layout` naming it, its backup fails as a recorded run (`backup-failed`), and its old backups and export files are not restored or imported. To move it after the upgrade: create a new jail and copy the data over from its datasets, which stay on the pool as they were.
- **A jail is upgraded in place while it runs; JailOps never stops it for that.** `jailops jail upgrade NAME` upgrades the FreeBSD base from the host with `pkg -r` on the running jail's root, as the FreeBSD Handbook upgrades a pkgbase jail, with the repository configuration outside the root, and the ports packages inside the jail (`pkg -j`). pkg decides what changes (its own dry run on a copy of the jail's package database); with nothing newer the answer is "up to date" and nothing happens. Otherwise the root is snapshotted while the jail runs (`jo-pre-upgrade-<time>`, kept 14 days, for a manual rollback), pkg upgrades the base (`-C` an empty configuration, plugins off, `RUN_SCRIPTS=false`), what the scripts of the base packages do runs inside the jail (`pwd_mkdb`, `services_mkdb`, `cap_mkdb`, `certctl rehash`), every planned package is checked at its planned version, the ports packages follow and one health check runs. Services keep their old files until they are restarted: the result and the update history list them ("restart pending"); JailOps restarts nothing. A failure is reported and left as it is (running the upgrade again completes it); nothing rolls back on its own. `jail rollback NAME` returns the root to the snapshot, and refuses a running jail (every scope: `--scope data` and `--scope all` no longer stop and restart it), because a root rolled back under running services kills them: the administrator stops the jail, rolls back and starts it. A stopped jail is not upgraded (it is not started for that). `jailops jail base add NAME FreeBSD-ssh` adds base packages the same way ("nothing to add" when they are installed), and `jail base list NAME` shows them. A new release is manual, `--release 15.2-RELEASE`, never newer than the host kernel (`OSVERSION` is the host's `kern.osreldate`, held to the jail's major); `--dry-run` prints pkg's own report and every step, and several jails are upgraded one after the other with a summary. Before pkg works on a root JailOps checks it for what an attack on host-side pkg plants (a link out of the root, a link among pkg's files, a pkg configuration with plugins): such a jail is refused as `suspected_compromised` with what was found, in the update history, `jailops doctor` (`jail.suspected_compromised`), the condition `SuspectedCompromised` and the event `jail-suspected-compromised`, the other jails go on, and the next run checks again. This is a detection, not a boundary: the administrator trusts the jails whose base he upgrades from the host ([security-model.md](docs/security-model.md#the-in-place-upgrade-of-a-jail)). On a schedule, a jail is upgraded the same way by its own update policy only (`update.mode=managed` with `update.os=true`); on demand, a jail's base is a global administrator's, and so is turning that policy on. **The nightly upgrade of an image builds its next generation and touches no jail**: it no longer updates the packages inside the jails made from the image, `--upgrade-packages` of `image create`/`image set`, `ImageAutoUpgrade.packages` and `ImageUpgradeRun.jails_upgraded`/`.jails_failed` are gone, and a stored image that still has `packages` is read without it. Inside a jail `FreeBSD-base` stays disabled through `/usr/local/etc/pkg/repos/FreeBSD-base.conf`, which new images have and a jail made from an older image gets at its creation. The release of a jail is the one JailOps recorded on its root (`jailops:freebsd_version`), which `jail show` reports. New API fields `release`, `no_packages`, `force` and `add_packages` of the upgrade request, the route `GET /api/v1/jails/{name}/base`, and the doctor checks `jail.upgrade_state` and `jail.suspected_compromised`.
- **A jail without ports packages fetches no ports catalogue.** `jail upgrade` (and its dry run), `update check` and `update apply` read the jail's package database first; with no package besides the `FreeBSD-*` base and pkg there is no ports step and the FreeBSD-ports catalogue (about 49 MB in the root, and in its upgrade snapshot) is not downloaded. The update record says "no ports packages: nothing to update". A jail with ports packages is upgraded as before.
- **`update.os=true` now upgrades the base of a managed jail.** On 0.9.2 the key did nothing for a jail with a root of its own; from this release a jail with `update.mode=managed` and `update.os=true` gets the in-place upgrade of its base, inside its release, at its `update.schedule`, while it runs. Look at the policies before the upgrade (`jailops config get JAIL update.os`) and set `update.os=false` where you want to upgrade by hand. Setting `update.os=true` on a jail with `update.mode=managed`, or `update.mode=managed` on one with `update.os=true` (at create, `PUT`, `PATCH`, `config set`, copy, import and restore), needs a global administrator: the upgrade runs pkg as root of the host against the jail's root. A project administrator gets `forbidden` with that reason; the other update keys stay theirs, turning the pair off is allowed, and a jail that has it already keeps it until that key is changed.
- **Jails made before new images locked root** have an empty root password: a member of `wheel` becomes root with `su` without one, and `jailops jail audit` warns (`accounts.passwords`). Lock it in each such jail: `jailops exec JAIL -- pw usermod root -w no`.
- **From a 0.9.2 host.** `jailops doctor` names the datasets an interrupted 0.9.2 run left (`jail.old_datasets`: `root.old`, `root.new`, `root.stale`) and says to reinstall 0.9.2 so it finishes or rolls back that run; a new run of the same operation on the jail is refused while the old record is in the journal (`precondition_failed`), so it never overwrites it. `GET /api/v1/jails/{name}/base` has the extension `jail_base`.
- **JailOps never stops or restarts a running jail on its own for an update.** A package update (schedule or `update apply`) never restarts the jail: its record names the processes that still run replaced files ("restart pending"). `update.reboot` is still accepted; `auto` now reports, as `manual` does, and the console no longer offers it.
- **A new image is the minimal jail set.** `FreeBSD-set-minimal-jail` plus two libraries that ports packages link, `FreeBSD-libexecinfo` (PostgreSQL, Valkey) and `FreeBSD-kerberos-lib` (curl, so git over https in Forgejo), about 52 packages and 55 MB on ZFS, instead of the minimal and optional jail sets (109 packages): no sshd, no `pf`, no `sysrc`. The apps of the catalogue install on it, also on a host whose base is not installed from packages (the base repository is then disabled and pkg could not supply the two libraries). A jail adds a base package from the host with `jailops jail base add NAME FreeBSD-ssh`, and `jailops jail upgrade` keeps them current. JailOps no longer needs `sysrc` inside a jail: a provision enables its services by editing `/etc/rc.conf` from the host. Existing images are not changed.
- **A new image has a `/home`.** The minimal jail set ships none, so a script or a program that writes to `/home` in a jail failed with `No such file or directory` (`adduser` makes it itself). Every new generation makes the directory, as a FreeBSD system has it. A jail made from an earlier image gets it with the next generation of its image, or by `jailops exec NAME -- mkdir /home`.
- **New jails start with sh and have the prompt in sh too.** `jails.shell` of `jailopsd.yaml` defaults to `sh` (bash was the default); `jailops project set NAME --shell bash` makes bash the preferred shell of a project's new jails (new field `shell` of a project). In sh the prompt of JailOps applies through root's `/root/.shrc`, which sh reads after `~/.profile`: a marked block at the end of that file sources it after FreeBSD's own `PS1`. In bash it is set as before. Existing jails are not changed.
- **No kernel modules repository in a jail.** The kernel modules repository `FreeBSD-ports-kmods` is disabled in new images by `/usr/local/etc/pkg/repos/FreeBSD.conf` (the kernel is the host's).
- **A host that reboots twice within ten minutes starts its jails at the second boot.** The daemon starts the jails with `boot.autostart` only on the first pass after a boot, and it took two readings of `kern.boottime` less than ten minutes apart for one boot (the clock being set moves the reading). A host that was restarted again nine minutes after a boot came up with every jail stopped. A reading that is close to the recorded one now counts as the same boot only when the host has been up at least as long as it is since the reading was recorded.
- **Housekeeping kept nothing.** `RootOldRetention` was 0 in the real daemon, so the snapshot of an upgrade was destroyed by the housekeeping run right after the upgrade instead of after 14 days. It defaults to 14 days.

- **Jails get better defaults.** New FreeBSD images start with: `cron_flags="-J 60"` (the jobs of root, periodic among them, start at a random second of the first minute, so the jails of a host do not all start theirs at once), the reports of periodic in `/var/log/daily.log`, `weekly.log`, `monthly.log` and `security` (newsyslog rotates them) instead of mail that nobody reads, `daily_status_world_kernel="NO"` (a jail runs the host's kernel, so the daily check reported a mismatch whenever the host was on another patch level), and the password of root locked. A FreeBSD jail's `/etc/hosts` has a block, owned by JailOps, that makes its own name resolve to its own addresses (what `sudo` and many daemons wait for), the lines around it stay yours, and a `/etc/hosts` that cannot be read is left alone. Volumes are mounted `nosuid`. A storage root gets `atime=off` when it is registered, unless you set it on the dataset yourself. An existing image keeps what it has; a jail gets the hosts block and the mount options at its next start.

- **Registering a storage says how to mark the dataset.** The root helper registers only a dataset that carries `jailops:managed=on`; the error now names the two `zfs set` commands (`jailops:managed=on`, `jailops:kind=root`), and the storage guide shows them instead of saying that an empty dataset is enough.

- **`address-set show --flatten` says where each address comes from.** The output is a table of `ADDRESS`, `FROM` (the set that lists it, `NAME (jail)` for a jail's address; an address in two sets names both) and `COMMENT`. `-o json` gives `addresses` as a list of `{address, from, comment}` instead of a list of strings.

## 0.9.2 - 2026-11-20

- **`jailops shell` and `jail show` answer at once.** Reading one jail ran a ZFS check of every dataset of the jail to look for a rollback, and listed the datasets of every jail of the project for the disk size: about a second each. It is one check now and the jail's own datasets; `shell --shell /bin/sh` does not read the status at all.
- **An address set with a hyphen in its name works in every rule.** The rule that lets a connection out on the link of a jail wrote the table of the set with the hyphens of its name (`<s_bastion-hosts>`) while the table and the rule on the uplink have underscores (`<s_bastion_hosts>`), so the rule matched nothing and the connection that the uplink had passed was dropped at the jail, silently: a policy that allows `from: set:bastion-hosts` admitted nothing. All rules use the same table name now, and a test checks that every table of a set that a rule names is defined in the anchor.
- **`jailops firewall address-set show SET --flatten`** lists every address the set stands for: the sets it names (`set:NAME`) and the jails it names (`jail:NAME`) are replaced by their addresses, once each, as the firewall of the project uses them (`-o json` gives `{"name", "addresses"}`).
- **Notes.** A jail has `spec.notes` next to its one-line comment: a long text (at most 64 KiB) for what it does, how it was set up and who to ask. `jailops jail notes web` prints them, `jailops jail notes edit web` opens them in your editor (under `If-Match`: a change made meanwhile is refused and your text kept), `notes set --file` and `notes clear` are for scripts; `network`, `volume` and `project` have the same commands, and storage, firewall policies, address sets, images and profiles take `notes` in `edit` and `apply`. The console shows them with a safe Markdown subset and edits them in place. They travel with export, import, backups and fleet documents, and a change needs no restart ([notes.md](docs/notes.md)).
- **Random IPv6 addresses for new jails.** `jailops network ipv6-assignment NETWORK random` makes a new jail take a free address anywhere in the prefix of the network (the bits below the prefix are random; in a /80 one of 2^48) instead of the lowest free one of the range, so a jail is not found by trying `::2`, `::3`. `sequential` stays the default, IPv4 is always sequential, a jail keeps its address for good and can always be given one of its own (`--network NET=IPV4,IPV6`), which is checked against the addresses taken. The setting is `spec.ipv6.assignment`, a new optional member of the network.
- **pkg runs as pkg itself.** The helper called `/usr/sbin/pkg`, the program that fetches pkg. The one of the FreeBSD 16-CURRENT snapshot of 19 November looks for pkg inside the root of `-r` and refuses ("the package management tool is not yet installed") when the root is a jail tree that has none, so `image create` failed on a host with that snapshot. The helper runs `/usr/local/sbin/pkg` when it is installed and the other program only when it is not. [lifecycle.md](docs/lifecycle.md) says what to run by hand when `pkg -j` gives that answer.
- **The nightly runs are set at 19:17 UTC.** GitHub starts a scheduled run seven to eight hours after its time, so the runs of CI, the upgrade test and the Linux test land in the night, and the weekly run of the FreeBSD snapshots on Saturday evening.
- **From the run on FreeBSD 16-CURRENT.** `jailops doctor` warns (it no longer fails) when `net.inet6.ip6.use_stableaddr` is not 0: FreeBSD 16 has 1 by default and the jails of a routed network still get the link-local address of their MAC address, so every stock 16 host failed the check. Deleting a jail waits for the setup of its shell before the datasets are destroyed, as a stop does, so a delete right after a start no longer finds the root busy. [networking.md](docs/networking.md) says which source address the host sends from when a rule names a host address.
- **`scripts/vm-run.sh` and `scripts/vm-pulse.sh`.** `vm-run.sh PORT PART [SHARD]` runs a part of the FreeBSD tests on a test VM from a clean state; `vm-pulse.sh` runs the end-to-end and packet filter shards on the VMs of the coming release and of FreeBSD 16-CURRENT and writes a summary, with `--update` to bring the VMs to the newest snapshot of their branch first (by hand: the weekly run is the workflow `freebsd-ahead.yml`) ([docs/testing-on-freebsd.md](docs/testing-on-freebsd.md)).
- **A weekly run on the FreeBSD that is not the gate.** `freebsd-ahead.yml` boots the snapshot of `stable/15` and of 16.0-CURRENT with qemu and KVM on the runner (`scripts/ci-boot-vm.sh`), brings it to the newest packages of its branch and runs the end-to-end and packet filter shards (`scripts/ci-ahead-run.sh`), every Sunday and on demand (`gh workflow run freebsd-ahead.yml`); a table of the parts is in the summary of the run. It blocks nothing.
- **The support policy is written down.** [supported-versions.md](docs/supported-versions.md#the-rule): a release is supported while FreeBSD supports it, never below 15.1, for the two newest majors; a new major arrives in a minor release and a major is dropped with notice in the release notes; the binaries are static; a kernel interface that replaces a command keeps the command as the fallback for at least two years. The roadmap lists what FreeBSD 16 may bring (netlink, more than one uplink, SBOM, CPU fairness).
- **A worked example of the lifecycle guide.** [docs/automating-updates.md](docs/automating-updates.md): a script in
  periodic(8) that updates the host's patch level, the image generation and the packages inside the jails every
  night, corrected and explained line by line (what each command does on a pkgbase host, the exit statuses and the
  mail, what a stopped, pinned or thick jail gets, the checklist before the first unattended run, the built-in
  alternatives and which to choose, what to look at in the morning); the move of that host to 15.2 with the script
  switched off and on again; what differs for 16.0; and a table of the command line by question. Linked from
  `docs/lifecycle.md`, which stays the entry point.

## 0.9.1 - 2026-11-19

- **Fixes from the review of 0.9.0.**
  - An import never destroys a volume it did not make: the name is checked before the undo is armed, and an import takes the lock of every volume it creates.
  - An import file is kept below the state directory only while a free-space reserve of 1 GiB stays free, and two imports are received at a time; another is refused with `busy` and a larger file with a message that says where the space is missing.
  - A managed update records a failed automatic restart as a failure (the packages stay updated and the note says how to go back), and stops with a failure when the image of the jail cannot be read, instead of updating packages on an old root.
  - The rules of a pf anchor are not loaded while the states they end cannot be written down; the load is tried again on the next pass.
  - `just release` asks for a full CI run (the release pull request, the nightly run or a manual one) that is green on the tree it tags.
- **Gates for 1.0.** The compatibility checks that were still done by a reviewer are tests now ([docs/compatibility.md](docs/compatibility.md)):
  the Go API of the client library only grows (`TestSDKGoAPIOnlyGrows`); from 1.0 on a golden file of the freeze that loses a
  line fails CI unless the major version is raised (`scripts/check-golden-removals.sh`, `just freeze-removals`); a claim that a
  hub of an earlier release signed is still accepted by a host; the names of pf anchors, tables, tags, epair interfaces and
  interface descriptions are a golden (`internal/render/testdata/names.golden`).
- **A helper that is older than the daemon is refused.** The hello of the helper carries its release; `jailopsd` refuses to start
  with `version_skew` and says to restart `jailops-priv`, instead of asking an old helper for commands it does not have.
- **An import is one request and one operation.** The file is kept in `<state dir>/imports` while it arrives, the request answers
  202 with the pending operation when it is complete, and the operation makes the jail: a client that goes away after the upload
  does not stop it, and a restart removes the file of an interrupted import. `jailops jail import` and the SDK wait for the
  operation. A file that does not fit in the state directory is refused with a message that says so.
- **`jail info` and the jail object say how long the previous root is kept.** After a release move, `rollback_expires_at` (and the
  `Rollback:` line of `jail info`) is the day `jail rollback` can no longer return to it.
- **`gofmt` is clean again** in three files of the previous change.
- **One guide for updating FreeBSD, the host and the jails.** [docs/lifecycle.md](docs/lifecycle.md) is the entry point:
  the model (release, patch level, kernel and userland, pkgbase and distribution sets, the package ABI, images and
  generations, thin, thick and Linux jails), what works with what and what JailOps refuses, a numbered runbook for every
  situation (a patch level, a minor and a major release of the host, JailOps itself, the patch levels of thin jails, a
  thin jail to a new release, a thick jail, a Linux jail, the packages inside a jail, the apps of the catalog, a pinned
  jail) with FreeBSD's own procedure as FreeBSD documents it and a link to the source, the checks to run after every
  step, what to watch for, what the results of a failed update mean, and a cheat sheet. `docs/upgrade.md` is the
  reference behind it; `docs/supported-versions.md`, `docs/thick-jail-upgrades.md`, `docs/release-engineering.md` and the
  help of `jailops jail upgrade` and `jailops image upgrade` point to it.

## 0.9.0 - 2026-11-19

- **The API and the command line are named once, before the freeze.** This is the last release that renames anything; the
  names below are the ones 1.0 keeps. Stored jails, profiles, update histories and `jailopsd.yaml` are converted when they are
  read, the first time the new daemon starts; nothing has to be recreated. A client that was written against 0.8 needs the
  names of this table.

  | Before | After |
  |---|---|
  | config keys `limits.*`, `nic.NAME.*`, `disk.NAME.*` | `resources.*`, `network.NAME.*`, `volumes.NAME.*` |
  | config keys `updates.mode`, `.os`, `.packages`, `.reboot`, `.schedule`; `zfs.delegation` | `update.*`; `zfs_delegation` |
  | `boot.stop_timeout`, `stop_timeout` | `boot.stop_timeout_seconds` |
  | `update.strategy`, `update.window`, `--update-strategy`, `--update-window` | removed (`strategy: manual` is `mode: manual`) |
  | `JailAction.timeout`, `confirm_within` | `timeout_seconds`, `confirm_within_seconds` |
  | byte counts `size`, `allocated`, `free`, `used`, `available`, `written`, `disk_used`, `memory_used`, `memory_total`, `pool_size`, `pool_allocated`, `pool_free` | the same with `_bytes` |
  | `started`, `finished`, `next` | `started_at`, `finished_at`, `next_at` |
  | update result `ok`, `failed` | `success`, `failure`, with `preflight_failed`, `rolled_back` and `recovery_needed` |
  | `status.generation` | `status.image_generation` (always `IMAGE#gN`; a request takes `gN` or `IMAGE#gN`) |
  | `spec.expose[].by`, `.at` | removed (the audit log has who and when) |
  | `role: host` in `jailopsd.yaml` | removed |
  | error code `conflict` | `precondition_failed` |
  | `?all=true`, `sdk.All()` | `?all_projects=true`, `sdk.AllProjects()` |
  | `POST /api/v1/jails/{name}` (copy) | `POST /api/v1/jails/{name}/copy` |
  | `POST /api/v1/jails/{name}/access` | `POST /api/v1/jails/{name}/access/allow` |
  | `/api/v1/certificates/{fingerprint}` | `/api/v1/certificates/{name}` |
  | `POST /api/v1/jail-imports/preflight` | `POST /api/v1/jail-imports?dry_run=true` |
  | `POST /api/v1/jail-imports` answers 201 | answers 202 with an operation that is already finished |
  | audit `?before=`; log `?tail=` | `?cursor=` with a `Link` header; `?limit=` |
  | `--tail`, `--count` | `--limit` |
  | `--label`, `-l` to select objects; `--exclude-label` | `--selector`, `-l`; `--exclude-selector` |
  | `--all` for every project | `--all-projects` (`--all` is every jail) |
  | `jail rollback --root`, `--data`, `--all` | `jail rollback --scope root`, `data`, `all` |
  | `--timeout` to wait for an operation; `--grace` to stop jails | `--wait-timeout`; `--timeout` |
  | `--ro`; `--force` (export); `--map` (restore); `update apply --images`; `firewall show --all` | `--read-only`; `--overwrite`; `--network-map`; `update apply --os`; `firewall show --lines` |
  | `jailops set` | `jailops config set` |
  | `logs forward add`, `remove` | `logs forward create`, `delete` |
  | `jailops doctor -o json` as `{check, status, detail}` | `{name, status, message}` with `ok`, `info`, `warn`, `fail` |
  | `jailops jail info` member `state` | `power` |

- **Removed.** The routes of features that are not built (`/api/v1/cluster`, `/groups`, `/rollouts`, `/jail-templates`,
  `PUT /api/v1/server`, `GET /api/v1/server/config`, moves, bundle download, snapshot rename, `POST /api/v1/backups` and the
  backup transport routes of the host) are gone and answer 404 `not_found`; a restore preflight is `POST /api/v1/restores`
  with `dry_run`. The error codes `not_leader`, `no_quorum` and `stop_forced`, the `clustered` member of the server
  information, `sdk.Recursion`, the extensions `backup_pull` and `backup_push`, and the command `jailops set` do not exist.
- **An ETag names the incarnation and the revision.** The `ETag` of an object is an opaque strong tag that carries its UUID
  and its generation. A change or delete with `If-Match` fails with `precondition_failed` when the object was changed since and
  when it was deleted and created again under the same name, so a plan or a script never touches a replacement. Clients send
  back the tag they read. `LogsClient.SetForward` takes the ETag that `LogsClient.ForwardETag` returns, and
  `VolumeClient.DeleteETag` is new. `jailops-sync` deletes and updates with the tag it read after it checked the object
  against its plan, and refuses a host that answers with another host identity than the plan was made for.
- **Creating a jail and restoring a backup are safe to repeat.** `POST /api/v1/jails` and `POST /api/v1/restores` accept
  `Idempotency-Key`; the SDK sends one and repeats the call after a transport failure. A start that cannot read the host UUID
  stops instead of creating a new identity.
- **A stopped jail is `Ready=False` with the reason `Stopped`**, and `api.Condition.Problem()` is the one test for "something
  is wrong": a stopped jail is not a problem in `jailops info`, `jailops show` or the console. `status.shell_setup {state, step}`
  says what the background setup of bash is doing. An operation cut off by a daemon restart has the code `interrupted`;
  `error.detail.recovery` is `rolled_back`, `completed`, `dropped` or `needs_attention`. An operation has an `action`
  (`jail.create`, `volume.snapshot`, `image.upgrade`, ...).
- **The wire is frozen by golden files.** Routes with their `sync`, `async`, `either` or stream answer, error codes, envelopes,
  enum members, the conditions of each kind of object with their reasons, operation and event actions, `GET /healthz`, the
  command and flag tree with its exit codes, and the extension lists. `just freeze` accepts a meant change. A client ignores a
  member it does not know and treats an enum value it does not know as "other" ([docs/api.md](docs/api.md#what-a-client-does)).
- **Machine output is the API's.** With `-o json` or `-o yaml` an error is written to standard error as
  `{"error": {"code", "message", "hint", "detail"}}`. `jailops exec` exits 125 when JailOps itself failed and otherwise returns
  the status of the command in the jail.
- **A thin jail's base package records follow its image.** At every start the records of the base set (`FreeBSD-*`) are brought
  in step with the generation of the root, so `pkg which`, `pkg info FreeBSD-runtime` and `pkg check -s` are right after an
  upgrade, `jail upgrade --image` or a rollback. Packages the jail installed are never touched, no file is written and no script
  runs. A failed pass never blocks the start: the jail has the condition `BasePackages`, `jailops doctor` warns
  (`jails.base-packages`) and `jailops jail repair-packages NAME` (the same button in the console) repairs it.
- **Release moves keep the previous root for 14 days.** The previous root of an upgraded jail was destroyed at the next
  housekeeping pass; it is kept for 14 days, so `jail rollback --scope root` works for that long. An image's pkg ABI comes from
  its release and no longer from the host's `pkg config abi`, and a build of a RELEASE stops (`verify-release`) when the base
  packages are of another release. `jail upgrade --dry-run` never builds or receives an image. An OS update that fails is
  recorded as `preflight_failed` (nothing changed), `rolled_back` or `recovery_needed` (an undo step failed too).
- **The end of pf states of removed rules survives a restart of the daemon, and `FirewallApplied` says `Unknown` when pf cannot be
  asked.** The labels of removed rules are written to `pending-pf-kills.json` before the new anchor is loaded and removed once pf
  has ended the connections. When `pfctl` gives no answer or status, the condition is `Unknown` with the reason `Unverified`
  instead of `True`.
- **The architecture is closed for 1.0 in the documents.** Every object is authoritative on the host that stores it; the scope
  `fleet` means that the hub and the fleet repository may manage the object with one definition and does not mean shared
  state. [docs/design.md](docs/design.md) has a table "Resource authority" generated from `api.Authorities`, with tests that keep
  it equal to the code. [docs/networking.md](docs/networking.md) says that networks belong to a host.
  [docs/1.0.md](docs/1.0.md) says what 1.0.0 promises, what it does not, and the gates of a release;
  [docs/compatibility.md](docs/compatibility.md) has the rules for 1.x and says what the tests prove and which gates are not
  built yet. Cluster mode, moving a jail between hosts and failover come after 1.0.
- **An import that was cut off says so.** `jail import` of a truncated or damaged file fails with `invalid: the export file is
  truncated` and nothing is imported, instead of an internal error.
- **A jail moves to a new release with its data.** `jailops jail upgrade NAME --image freebsd-15-2` moves a thin FreeBSD
  jail to the newest generation of another image of the same OS and architecture, of the same or a newer release and not
  newer than the host's kernel, with the steps of an upgrade to a new generation: one atomic snapshot, a clone of the new
  root, the three-way `/etc` merge, the swap, the start, the health check and the swap back on failure. Persistent
  datasets, volumes, addresses and configuration stay, the jail follows the new image from then on (`spec.image`, the
  nightly auto upgrade of the new image), the move is written to the update history, and `jailops jail rollback` returns
  the jail to the old root and the old image together. `--dry-run` shows the releases, what is kept and the steps; a move
  to a new major release warns that the packages were built for the old ABI. Several jails move one at a time (named,
  selected with `-l` or `--all`; the first failure stops the rest), and `jailops image upgrade freebsd-15-1 --to
  freebsd-15-2` moves every thin jail of an image. `jailops image create IMAGE --release R --build` builds the first
  generation right after the definition is stored. The Upgrade dialog of the console and the jail list offer the same
  move, with the release change in the preview. A thick jail, a Linux jail and a jail on another OS are not moved this
  way, and an older release is refused.
- **A jail leaves as one file and comes back as another jail.** `jailops jail export NAME --to FILE` writes the jail's
  specification, the objects it refers to and its data (a thin jail's persistent datasets, a thick jail's whole root) as a
  versioned stream with checksums; `jailops jail import FILE` makes a new jail of it under another name (`--as`), in
  another project, with `--dry-run` showing the plan. A thin jail records its image and needs it on the target; volumes
  come only with `--with-volumes`; encrypted datasets are sent raw and stay locked; secrets are never plaintext in the
  file; a failed import leaves no half-made jail. The jail page and the jail list of the console export and import.
- **One command on many jails.** `jailops exec` takes several jails, `-l KEY=VALUE` or `--all` and runs the command in
  every running one, `--parallel N` at a time (4 by default), each in its own audited session, with `[jail]` in front of
  each line, standard output and standard error kept apart and a summary. The exit status is 0 only when every command is,
  otherwise the highest status or 1 for a jail that could not run it. `jailops pkg install|upgrade|remove|info` is exec of
  the jail's own `pkg`. The console runs a command on the selected jails.
- **Copy files with `jailops cp`.** `jailops cp SRC DST` copies files and trees between this machine and a jail or between
  jails (`NAME:/path`), with `-r`, `-p` and `--mkdir`; `jailops cp FILE -l role=web :/etc/FILE` sends to every matching running
  jail. The data goes through the daemon (extension `jail_files`, audited) as a tar stream that the helper reads and writes
  below the jail root without following a symlink, up to `limits.file_copy_bytes` (2 GiB). The jail page of the console
  uploads and downloads files.
- **`jailops jail copy SOURCE NAME --data` copies a jail with its data.** The root and the persistent directories are
  copied by a full send and receive, so the copy is independent of the source: the source may run (one atomic snapshot) and
  `--snapshot S` copies an earlier state. The copy has addresses of its own, no volume and no exposure; `--start` starts it.
- **`jailops jail convert NAME --layout thin|thick` changes how a FreeBSD jail's root is stored.** Thin to thick makes an
  independent root with the persistent datasets folded in; thick to thin gives the jail a clone of an image generation
  (`--image` names one) and keeps the data of the persistent paths. `--dry-run` lists what is kept and what is replaced, a
  snapshot is taken first and a failure puts the old layout back.
- **`--cpuset 2,3` and `limits.cpuset` pin a jail to CPUs of the host** with cpuset(1), whatever `kern.racct.enable` says.
  The set is checked against the CPUs of the host, and `jailops doctor` warns about a jail whose set names a CPU the host
  does not have.
- **`jailops top` shows what the host and its jails use right now.** A live table of the running jails (project, CPU, memory,
  processes, disk, network in and out, uptime) under a header with the load, CPU, memory, ZFS cache, pool and the number of
  jails; `c`, `m`, `d`, `n` sort and `q` quits, `--once -o json` prints one frame for a script. It asks the daemon
  (`GET /api/v1/top`, extension `top`, same visibility as `jail list`), which shares one measurement between all watchers.
  Without racct CPU and memory come from the process list and a line says so. The console has the same on the Live usage page.
- **Restore on a new host works from the CLI and the console.** `jailops backup key import` installs the backup key of a lost
  host on a new one, from the recovery key, a copy of the old `backup.key` or the escrow on the backup node. The key is checked
  against the key domain the node recorded before anything is written; a different key needs `--replace`; the audit log
  records the domain, never the key. `docs/backup-restore.md` has the walkthrough "Restore on a new host".
- **Fill a volume from a directory of the host.** `jailops volume create NAME --from-dir /host/dir` copies a plain directory
  once into a new ZFS volume (a global administrator, below an `admin-host-mount` directory): owners, modes, times and links
  are kept, no setuid bit or device comes across, nothing is read through a link out of the directory, and a failed copy
  removes the volume. A volume that is a plain directory (`--path`) stays refused, and the error now says why.
- **An upgrade keeps what a host holds, and that is tested.** A host of any release since 0.8.0 upgrades in place:
  `go test ./internal/upgradecheck` opens the stored state of every release (`internal/store/testdata/upgrade`) with the
  current code, a database or object newer than the binary is refused, `just release-pr` stops while the newest tag has no
  fixture (`scripts/make-upgrade-fixture.sh`), and a nightly run upgrades a VM from the previous release
  (`scripts/ci-freebsd-upgrade.sh`). See `docs/upgrade-guarantee.md`. The database is copied to `jailops.db.before-schema-N`
  before a migration; image aliases in an old `spec.json` and in fleet and CLI files, a tunnel's `down`, an IPv6 `mode`, a
  `proxy` device and `root-dataset` in `priv.conf` are read as their current shapes instead of being dropped or refused.
- **Which FreeBSD releases are supported, how to update a host and how to release are written down in one place each.**
  `docs/supported-versions.md` has the matrix (15.1 is supported and is the gate of the continuous integration, 15.2 is planned
  for December 2026, 16.0 for December 2027) and the rule; `docs/release-engineering.md` has the checklists to release
  JailOps and to update a host; `docs/upgrade.md` is the one page for updating a host. `jailops doctor` warns for a release
  newer than the newest in the test matrix (`host.release`) and says whether the base is installed from packages
  (`host.pkgbase`). `just publish-packages X` is the step after `just release X`.
- **A workflow walkthrough before every release.** `test/workflows/daily-tasks.md` lists what a jail administrator does every
  day and the command for each; `just e2e-workflows` runs the tasks on a FreeBSD test host, `scripts/check-workflows-table.sh`
  keeps the table, its steps and the roadmap in step, and `just release-pr` stops while a task is missing without a plan.
- **The API contract is checked by more tests.** The bodies of the main routes, the life of an asynchronous operation and the
  machine output of the main commands are recorded in golden files, the serialised state command is frozen
  (`internal/state`), and a program built with the client library of an earlier release makes the calls of a script against the
  current server. `just freeze` accepts a change that is meant. `docs/api.md` says which extensions carry the 1.x promise.
  Cancelling a finished operation answers "already finished" whether the record is still in memory or stored.
- **Security: a failed operation no longer shows host details in its error.** An error that is not one of the API's own codes
  (dataset names, paths of the host, command lines, addresses) was kept in the operation as it came, and every reader of the
  operation saw it. The operation now says `internal error` with its id in the hint, as a failed request does, and the text is
  in the log of `jailopsd` under that id; a failed app installation and the shell setup in a jail's status name the step or
  cause class instead of the output.
- **A jail that exposes a port below 1024 over IPv4 starts again after a restart and at boot.** The start hook loads the jail's
  firewall anchor as root with no API caller behind it, and the helper refused the forward of a privileged host port without
  the admin assertion. The hook now loads the forwards of the jail's own stored exposure, which an administrator already
  authorised; a project administrator still cannot store such an exposure.
- **A restart or stop of a jail whose mount went missing succeeds.** A volume, the twin of a volume or of `/proc` below
  `compat/linux` of a Linux jail, or a host directory that was unmounted by hand made jail(8) refuse with `not a mount point`
  and left the jail stopped; the stop now gives every such target a placeholder, and the next start mounts everything again.
  Converting a thick jail back to thin no longer asks ZFS to inherit `canmount`.
- **`jailops doctor` can count the neighbor solicitations the host rejected.** The helper had no rule for `netstat`, so the
  check of `net.inet6.icmp6.nd6_onlink_ns_rfc4861` could never warn; the allow-list now accepts exactly `netstat -s -p icmp6`,
  and a test requires every command the daemon issues to be on it. `scripts/vm-lock.sh` serializes runs on the FreeBSD test VMs.
## 0.8.6 - 2026-11-18

- **A firewall policy states a default only when you tell it to.** `default_in` and `default_out` are `deny`, `allow`
  or unset, and `firewall policy create` writes none unless you give `--default-in` or `--default-out`. A default is
  deny when any policy of the jail states deny (the project's mandatory policies, the managed policy of app access, the
  jail's own and those of its profiles), whatever the order, and allow when none does: a small policy that only allows
  tcp 2282 from an address set no longer closes the jail as a side effect, and no policy can turn another's deny into
  allow. Nothing is closed by default inside a project: close a project or a jail with a policy that says
  `default_in: deny`, in the project's `firewall_policies` or on the jail, and open what is needed with `allow` rules.
  `jailops firewall show JAIL`, `jailops jail info JAIL` and the Firewall tab of the console say which policies set each
  default (`Default in: deny (default [project])`), `firewall test` names them when a default decides, and a jail with an
  `allow in` rule from named sources and no policy that says deny gets a warning. The plan of a file sync flags a policy
  that stops saying deny. The doctor check `firewall.defaults` is gone: a policy without defaults is valid. Defaults never
  open the uplink; that is an explicit `allow in` rule with an external source.
- **A firewall policy says which jails use it.** A jail without the policy renders nothing from it, and nothing said so:
  a policy that was made but never named by a jail left the jail open. `firewall policy show` has a `Used by` line (the
  jails by name, `every jail (project)` for a mandatory policy of the project, `none`), `firewall policy list` has the
  column USED BY, the API carries it as `status.used_by` and `status.mandatory`, and the policy page of the console
  shows it, and says so when no jail uses the policy. A jail that has the policy through a profile or as the managed
  policy of app access counts, and the delete of a policy refuses for those jails too.
- **Attach and detach one firewall policy at a time.** `jailops firewall policy attach POLICY JAIL...` appends the
  policy to the policies of each jail and `detach` takes it out; the others stay, so nobody has to type the whole list
  of `firewall.policies` again. A jail that has the policy already (or has not) is named and left alone. With
  `--mandatory` the change is made to the mandatory list of the project (global admins). The change is made by the
  server against the stored jail (`add` and `remove` in the configuration change of the API, `ChangeConfig` in the
  SDK), so a policy attached at the same time is not lost. The Firewall tab of a jail in the console does the same, one
  policy at a time. `jailops firewall show JAIL` now starts with a Policies section: the policies in the order they are
  rendered (the project's marked `(project)`, the managed one of app access `(managed)`, then the jail's own) with the
  rules each has and the default it states, and `firewall show` and `jail info` end with the command that attaches one
  when the jail has none.
- **Other projects reach a jail like the Internet does, and what a jail sends is never closed on its side.** A jail reaches the
  jails of other projects like any other address, and an `allow out` rule that names them is no longer needed. What comes
  in from another project is closed until an `allow in` rule of the jail that is reached has a source that covers it: `any`,
  an address, a prefix (a wide one such as `0.0.0.0/0` or `::/0` too) or an address set, for every address of the jail, the
  private IPv4 one included. A rule without a source is for the project's own jails and the host, a default never opens it,
  and a deny rule wins. A rule that says `from any` or names a wide prefix, which other projects did not reach before, now
  admits them: name the sources or add a deny rule to keep them out. `any` as the destination of an allow rule is every
  address but the host's. `firewall show` and `reach` list the other projects like the Internet, with the rule and what it
  is limited to, and `firewall test` evaluates the anchor of another project's jail too when a global administrator names
  its address. The anchors of the jails change text, so the labels of their pass rules change and open flows reconnect once.
- **`expose` and policy rules agree.** A port is exposed once per protocol: `expose web 443 --from set:office` after
  `expose web 80,443` takes 443 out of the first entry (before, 443 stayed open to anyone), `unexpose web 443` takes only
  443 out of an entry that names 80,443, and an exposure of the same ports twice, also after a partial unexpose, leaves one
  entry and one forward. `--proto` repeats (`--proto tcp --proto udp`) as well as `tcp,udp`, and each port is said once in
  the answer. The answer reads `jail/web exposed: 80,443/tcp`, `IPv6 ADDR   open: no NAT, the jail's own address answers on
  80, 443` (also for IPv4 without NAT) and `IPv4 ADDR   forwarded: host 80->80, 443->443 (NAT)`. It names the policy rule
  that already lets the port in (`note: policy web rule 2 already lets 80/tcp in over IPv6; this exposure adds the IPv4
  forward`), `unexpose` names the rule that still does, and a single port inside an exposed range says which range holds it.
  `--host-port` with several ports says what to type. `firewall rule add`, `rule edit`, `policy create -f`, `policy edit`
  and `firewall validate` say once that a rule with the source `any`, `0.0.0.0/0` or `::/0` lets the Internet in and that
  `expose` does the same and adds the IPv4 forward. A policy cannot be called `expose` (the firewall views give that name
  to the rules of an exposure), and the view names an exposure as `expose tcp 80,443`. The console shows the same lines
  and notes, with the right family for a routed IPv4 address, and `docs/networking.md` has the table "Expose or a policy
  rule".
- **The lists of networks and storage show what the project holds, and say who holds the rest.** `jailops network list` and
  `jailops storage list` list what the selected project is granted, also for a global administrator (a project without
  grants lists none). `--all` lists every one with GRANTED (does the selected project hold it) and PROJECTS, and
  `network show` and `storage show` print `Granted to`. The console pages have the same switch, "All networks" and "All
  storage", and the API answers `?all=true` and carries `status.granted_to`.
- **A running jail that lacks a mount of its fstab is reported.** A Linux jail that started before its `fstab` gained the
  mounts below the `compat/linux` mirror (`/proc`, `/sys`, the volumes) showed a Linux process the empty directories of
  the mirror, and nothing said so. The loop compares the mount points of every running jail's `fstab` with the mounts of
  the host: `jailops info` (the InSync condition) and `jailops doctor` name each mount that is missing and say to
  restart the jail, which mounts it. A mount taken off by hand is reported the same way.
- **A volume is refused where it would hide something, when it is attached.** A path above a persistent directory of
  the jail (`/var` hides `/var/db`, `/usr` hides `/usr/local`) and a path at or below `/dev`, `/proc`, `/sys` or
  `/compat` are answered with `invalid` by `volume attach`, `create` and `config set`. They were accepted, and only the
  live mount of a running jail refused the second kind.
- **A host directory of a Linux jail is mounted again below the mirror too.** When a directory that was lost comes
  back, the mount in the jail's root was made again and the one below `compat/linux` was left over the old mount, so a
  Linux process kept seeing the lost directory. Both are made again, the one below the mirror last.
- A Linux jail is always thick. `--layout thin` for a Linux image is refused at once, before anything is made: the
  persistent datasets of a thin jail are ZFS mounts that the nullfs mirror at `compat/linux` does not show, so a Linux
  process would have seen empty directories. The console shows the layout of a Linux jail as thick and offers the
  Changes tab (which compares a thin jail with its image) for thin jails only.
- **`jailops doctor` no longer warns for a second project: `dns.unbound` asks each network for a jail that runs on it.**
  The resolver answers by the network a question comes from, with the names of that network's project only, so the
  gateway of one project's network answered `no such host` for a jail of another. The doctor now picks a running jail
  per network and asks that network's gateway and resolver address for it; a warning names the network and the address
  that was asked (it no longer names the nameserver of `resolv.conf`). The text of `jails.inbound-without-source` says
  that a rule without a source is for the project's jails and the host.
- **A jail of the boot and the first operation of a new daemon wait for the daemon's own short work.** The start of a
  jail at boot answered "busy" when the housekeeping pass or the clean-up of the mounts of the same jail held its lock
  in that moment, and the jail stayed down until a later pass tried it again; an operation that came right after the
  start of the daemon was refused the same way. Both wait for that work now, as they already waited for a reconcile
  pass; two operations of people at the same time are still refused.
- **A stopping daemon starts no more work.** The reconcile loop could still have a start of a jail in flight while the
  daemon closed its service, and that start began a background setup of the shell after the wait for the service's work
  had begun: the setup wrote into the state after the daemon had closed it (and a restart on the same state could meet
  it). Once the service closes, an operation that is asked for answers "busy" ("the daemon is stopping") and no setup
  of a shell starts; the jail stays due and the next start tries again.
- **`jailops doctor` warns when the host has rejected neighbor solicitations.** The setting
  `net.inet6.icmp6.nd6_onlink_ns_rfc4861` stays information while the host has rejected none. With the default, `0`, and
  a network with the NDP proxy, the check now reads the host's own count of rejected solicitations (`netstat -s -p icmp6`,
  "bad neighbor solicitation messages") and warns with it once it is above zero: a router of a shared segment that probes
  the host from a global address gets no answer and may mark the host and its jails unreachable. The hint is the
  `sysctl` line and the line for `/etc/sysctl.conf`; JailOps still sets nothing outside appliance mode.
- **The firewall says whether the kernel has it: `FirewallApplied`, `jailops firewall wait`, and the holes an external
  audit found in how pf follows the policies.** The status of a running jail with a firewall has a condition
  `FirewallApplied`: true while pf is enabled and evaluates the anchors, the anchor and its tables hold what the policies
  render, and the connections of removed rules are ended; false with the reason `NotLoaded`, `PFNotApplying` (pf disabled,
  the line `anchor "jailops/*"` gone from the main ruleset, or the ruleset unreadable) or `StatesPending`. `jailops info`
  has an `Applied` line, the console Firewall tab shows it, `GET /jails/{name}/reach` has `rules_id` (extension
  `firewall_applied`), and `jailops firewall wait JAIL...` returns when pf has the rules the policies render now (exit 1
  on timeout, naming the reason). Fixed on the way: an address set that lost its last member kept admitting its former
  members (pf keeps the members of a table whose definition has no braces; the empty table is now rendered `{ }` and the
  loop compares empty tables with the kernel's); the connections of a removed rule stayed open when pf refused to end them
  (the labels are kept and tried again at every pass); a jail with a network link started when the main ruleset of pf
  could not be read (it is refused now), and pf disabled or without the anchor line was not noticed while jails ran.
- **API: strong `If-Match`, conditional delete, checked merge patches, a freeze of the surface.** `If-Match` is parsed
  once: quoted strong tags, a weak tag matches nothing (412), `*` alone is any, a list matches when one of its tags is the
  generation, several different generations are refused; the SDK has `DeleteETag`. A merge patch is checked against the
  type of the object like a typed body (a misspelled member is `invalid`, not a 200 that changes nothing) and keeps
  numbers beyond 2^53. `jailops firewall test` exits 9 for a blocked connection (8 unknown; 1 to 7 are errors). Tests
  freeze the routes, the error codes with their HTTP status, the members of the envelopes and the extension list (append
  only): `just freeze` accepts a meant change, `docs/api.md` says what is frozen.
- **CLI and release hardening.** A remote name is a name (lower case letters, digits, hyphens): it was a file name in the
  certs directory, and `../x` was accepted; key files are created without overwriting, also through a symbolic link, and
  `remote remove` takes the pair it made with it. `remote add --dry-run` answers before it connects. The release workflow
  names every action by its commit and GoReleaser by an exact version, and only the publishing job can write.
## 0.8.5 - 2026-11-18

- **The NDP proxy starts on a host that holds the resolver address.** The proxy refused to open its capture when the
  prefix of the network held an address of the host, and the helper's own resolver address of the network (the second
  address of the subnet, held on the loopback for the names of the jails) counted as one: the proxy stayed off and the
  jails of the network got no IPv6. That address is the helper's, the proxy never answers for it, and it no longer
  refuses the prefix; any other address of the host inside the prefix still does, in the helper and when the network is
  made.
- **The e2e run no longer depends on a package mirror to get bash.** The host fills the package cache of the jail
  roots with bash before the shell steps, a jail whose first setup lost against a slow mirror is given the bash from
  that cache at its next start, and a failed step prints what the daemon and the helper logged about packages, shells
  and setups.
## 0.8.4 - 2026-11-18

- **No IPv6 mode: the NDP proxy is a switch of the network.** A network has an IPv6 prefix, and the proxy that answers
  neighbor discovery on a shared segment is switched with `jailops network ndp-proxy enable|disable NETWORK` (field
  `ipv6.ndp_proxy`; the web console has the same switch). **Breaking:** `--ipv6-mode` and `ipv6.mode` are gone; scripts
  and files that pass them must drop them and run `ndp-proxy enable` for a network on a shared segment. A stored network
  is converted when it is loaded, so a network that had the proxy keeps it: nothing to do for existing networks. A new
  IPv6 network gets the whole range of its prefix; the proxy answers for the first 1024 addresses of it.
- **Breaking: image aliases are gone.** `jailops image alias`, `image create --alias`, `IMAGE@alias`, the alias column of `image list` and the console's alias chips and routes are removed (API extension `image_aliases`, SDK `SetAlias` and `DeleteAlias`). An image name follows the newest generation; `IMAGE#gN` and `--generation gN` pin one. The first start converts what was stored: a jail that named an alias gets the bare image name and an event `image-alias-removed` says which alias it had and which generation it pointed at, the alias lists of images are dropped, and the alias table is cleared from each image dataset. The root of an existing jail does not change.
- **Removed: `jailops-priv --root-dataset`.** Use `storage-root` in `priv.conf` (or `-storage-root` on the command line).
- **`jailops jail diff NAME`: what a thin jail holds on top of its image.** It lists the files the jail added (`+`), changed
  (`M`), removed (`-`) or renamed (`R old -> new`), grouped by dataset, with a summary line. The root is compared with the
  `@ro` snapshot of the generation it was cloned from (`zfs diff`, through a read-only helper operation with a time and a
  size bound); each persistent dataset is compared with its new snapshot `jailops-base`, taken right after the image's
  directory was copied into it (never listed, rolled back or pruned with the snapshots of the jail). A jail made before
  that snapshot existed is compared file by file with the image and the output says so. `--summary` prints the counts,
  `--limit N` the lines per dataset (200 by default, the counts stay complete), `--dataset` one dataset, `--packages` the
  packages installed on purpose (a running jail), `-o json|yaml` the reply. A thick or Linux jail answers that diff needs a
  thin jail; a jail with a locked volume says so. API: `GET /jails/{name}/diff` (extension `jail_diff`, readonly, operator
  of the project), SDK `Jails().Diff`. Docs: [storage.md](docs/storage.md).
- **The web console shows what a jail holds on top of its image.** A new Changes tab on the jail page lists, per dataset, the
  files the jail added, changed, removed or renamed since its image or since the jail was made, with the counts, "Show all"
  when the list is cut, and the packages installed on purpose. It reads `GET /jails/{name}/diff` (the same answer as
  `jailops jail diff`), so a reader of the project sees it too.
- **Breaking: an `allow in` rule without `--from` is for the jail's link only** (the project's jails and the host) and no longer
  opens the uplink. `--from any` or `jailops expose` does. `firewall rule add` says so. `app allow` refuses `0.0.0.0/0`, `::/0`
  and `any` and points at `jailops expose`. `jailops doctor` (`jails.inbound-without-source`) lists the rules this changes on the first start after the upgrade: give such a rule `--from any`, or use `jailops expose`. `app allow` also refuses every prefix shorter than /8 (IPv4) or /16 (IPv6) and address sets that hold one.
- **`jailops expose`: let the Internet reach the ports of a jail, over IPv4 and IPv6, in one command.** `jailops expose web 80,443`
  opens the ports on every family the jail has: an IPv6 address, and an IPv4 address of a network without NAT, are let in on
  the uplink to the jail's own address; an IPv4 address behind NAT gets a forward of the same host ports (`--host-port`,
  `--listen`). `--from` limits the sources (addresses, prefixes, `set:NAME`), `--family 4|6`, `--proto tcp|udp|tcp,udp`,
  `--comment`, `--dry-run`. `jailops unexpose` takes ports out. The exposure is stored with the jail (`spec.expose`) and rendered as
  the section "exposed ports" of its anchor; the jail's policies are not touched. An administrator of a project exposes IPv6 and
  IPv4 without NAT; the IPv4 forward opens a port of the host and stays a global administrator's, and a request that needs one is
  refused as a whole with the hint `--family 6`. Two jails cannot forward the same host port (the error names the other jail).
  API: `POST /jails/{name}/expose` and `/unexpose` (extension `jail_expose`), SDK `Jails().Expose` and `Unexpose`.
- **Breaking: the old forms of a port forward are gone.** `--redirect` and `redirect:` rules of firewall policies, `proxy:` devices
  of jails and profiles and the `proxy.<name>.*` keys are removed. The first start converts what was stored (a record on disk keeps the forwards until every jail has its entry, a write that fails stops the start, and what is not converted - a switched-off rule, a host port another jail already has, sources that are jails or the host - is logged with an event): each becomes an IPv4
  entry of the exposure of the jails that used it, so the same host ports stay forwarded, and the policy or profile is written
  back without it (the rule numbers after a removed redirect shift). Fleet files: `proxy:` becomes `expose:` on the jail.
- **What reaches a jail is visible: `EXPOSED` in `jailops list`, `jailops firewall show`, `jailops firewall exposed`.**
  Every jail has one answer for each kind of source (the Internet over IPv4 and IPv6, each WireGuard tunnel, the other jails
  of the project, the host, other projects), computed from the anchor JailOps loads, not from the policies as typed. Each
  row names the family, protocol and ports, who it is limited to, and the policy rule that gives it ("policy web rule 2") or
  what stops it. `list` has an `EXPOSED` column (`tcp 80,443 v4,v6`; a forward shows the port of the host; a star marks a
  rule limited to some sources), `jail info` has a "Reachable from" section, `firewall show JAIL` adds what the jail reaches
  and, with `--all` and `--pf`, the rules in the order pf decides, and `firewall exposed` lists everything the Internet
  reaches across the jails (`--tunnels` adds WireGuard peers). `firewall test --from internet` asks the same of a jail's
  anchor, and for an IPv4 jail behind NAT it walks the forward of the host. `jailops doctor` (`jails.exposed`) names the
  family, protocol and source. The API has `GET /api/v1/jails/{name}/reach`, `GET /api/v1/exposed` and
  `JailStatus.exposed` (extension `jail_reach`); the console has a Firewall tab on every jail and an Exposed tab on the
  firewall page.
- **The console exposes ports too.** The Firewall tab of a jail has an Expose button (ports, protocol, families, limited sources, host port, comment, with a preview of what would be reached) and lists the exposures with a button to take each one out.
- `jailops doctor` `jails.exposed` and the setup guide point at `jailops expose`.
- **Safer exposure.** A forward is a global administrator's to give, change (sources, jail port) or take away, in `jailops expose`, `jail set`, a fleet file and a profile edit alike; a forward of a host port the host listens on is refused everywhere; an IPv4 range of ports on a jail behind NAT is refused (`--family 6` or single ports); `--family` holds for the pass rule on the jail's link too; two requests at once no longer overwrite each other. The views leave out a port an uplink deny rule closes.
- Fix: `--memory` in `jailops jail create --help` showed `jailops info JAIL` as the value name; no flag help has a backquote now (a test walks the command tree).
- **Security: the firewall of JailOps never opens the host or another project.** JailOps' firewall governs the jails and
  `/etc/pf.conf` governs the host. Before, four things went around that:
  - An allow rule that named `host`, a prefix holding a host address (all of IPv4 or IPv6, the network's own subnet, the LAN
    of the uplink) or a set holding one was rendered before the closing of the host, so anyone who could edit a policy of a
    project could open the host's sshd, API port or NFS to their jail, and the rules of `pf.conf` after the anchor line never
    saw the packet. The host is closed before the allow rules now, whatever they name (DNS to the resolver and ping stay
    open); an allow rule to `host` is refused when it is written, and a service of the host that a jail needs is opened in
    `/etc/pf.conf`.
  - A raw PF fragment of a policy needed only the admin role of a project. It needs a global administrator now, it is
    rendered after the closing of the host, and the helper refuses a rule in the anchor of one jail that stands on the link
    of another jail, with or without `admin-raw-pf`. Raw fragments that project administrators stored earlier stay until the
    policy is edited; they stand after the closing of the host and of the other projects, so they cannot open either.
  - An allow rule with a wide prefix (`allow out to 0.0.0.0/0`, a `/8`) or `allow in --from any` passed on the link of its
    own jail into the networks of other projects, and the isolation of a project depended on the policies of another.
    The other projects are closed before the allow rules now, in both directions, except for an address or prefix that a
    rule names itself ("name them to open them"); the block after the allow rules stays as the backstop.
  - When the addresses of the host, or the networks of the other projects, could not be read, the tables that close them
    were empty and the anchor was rendered open. The render is an error now: the jail keeps the anchor it has and a start is
    refused (the WireGuard anchor likewise). A new host address is in the tables after the next reconcile pass, a minute at
  most.
- **Security: a jail with a network link does not start when pf does not answer or its ruleset lacks `anchor "jailops/*"`**
  (`allow-start-without-pf` in `priv.conf` says otherwise). ICMP to the host is ping and the errors a path needs, not every
  type.
- **The jail's own firewall decides what the Internet reaches, not `pf.conf`.** An explicit `allow in` rule with an
  external source (`any`, an address, a prefix or an address set) renders `pass in quick on <uplink> ... to <jail address>
  port N` in the jail's anchor, for IPv6 routed and proxy networks and for IPv4 networks without NAT. A host whose
  `pf.conf` blocks inbound traffic by default needs no rule for its jails. A jail with only defaults stays closed from
  the Internet whatever its `default_in` says; a rule whose source is `host`, a jail or a tag never opens the uplink; a
  `deny in` rule is applied on the uplink first; a packet with a source in a network of JailOps, or one that pf's reverse
  path check refuses, is dropped as forged; every pass limits its states (`limits.states` of the jail, else `jails.max_states`,
  else 5000). **Behaviour change:** `allow in ... from any` means the whole Internet. Delete a hand-written `pass in quick on
  <uplink> ... to <jail prefix>` rule: it is not needed, and `jailops doctor` names it (`pf.inbound`).
- **`jailops firewall test` walks the uplink.** A connection from an address outside every network of JailOps meets the anchor
  of the jail on the uplink first and is blocked there when no rule admits it. `--to host` tests every address of the host.
- **`jailops doctor`:** `pf.inbound` fails when a `block ... quick` rule for every source stands before the anchor line and drops
  inbound traffic to the jails; `jails.exposed` (info) lists the jails the Internet can reach, with their ports.
- **WireGuard: the peers of a tunnel are an external source.** The anchor `jailops/wg.<name>` closes the host and every network
  the tunnel does not serve to its peers and passes nothing; a jail admits peers with its own `allow in --from <tunnel prefix>`
  rule. The `networks` of a server give routes and forwarding, not access. **Behaviour change:** peers no longer reach jails implicitly; add `jailops firewall rule add POLICY in
  --action allow --proto tcp --port N --from <tunnel prefix>` to the jails they must reach.
- **WireGuard: `up` and `down` are `enable` and `disable`.** The field `spec.down` is `spec.disabled`, the routes are
  `POST /api/v1/wireguard/{name}/enable` and `/disable`. **Breaking:** no aliases; change scripts. A stored tunnel that was
  down stays down: the field is renamed when the file or the record is loaded.
- **The NDP proxy answers for the range of the network, and the network switches it.** On a shared segment the host answers
  the router's "who has this address" for the allocation range of each network in `proxy` mode (the first 1024 addresses of the
  range and the address of every running jail), whether a jail runs or not. Nothing is registered when a jail starts, so a
  second or third jail is answered like the first. The capture is promiscuous and the solicited-node group of every address of
  the range is joined, so a multicast filter of the network card or the hypervisor cannot hide a question. The host's own
  addresses and the resolver address of the network are never answered. `jailops network ndp-proxy enable|disable NETWORK` sets
  `ipv6.mode` to `proxy` or `routed` for good; `status NETWORK` shows the range and what the proxy answered; `watch NETWORK`
  prints each question and answer live; REST `GET /api/v1/ndp` and `/ndp/events`. The web console has the switch and a status
  panel. `jailops doctor` fails `network.ndproxy` for an address the router asked for and the proxy never answered.
  **Breaking:** `network set --ipv6-mode` is gone (the mode is chosen at create or switched with the commands above);
  the flag is not on `network set` at all; `--ipv6-range a-b` sets the IPv6 range; a new proxy network gets 240 addresses (`::10` to `::ff` of a `/80`). A network in
  proxy mode keeps working after the upgrade.
- **Volumes are attached and detached while the jail runs.** `volume attach` and `detach`, `config set disk.NAME=PATH`, `edit`
  and a file mount the volume into a running jail at once (nullfs, read-only with `--ro`) and take it off again; the record and
  the jail's `fstab` follow. A detach of a mount that a process of the jail uses is refused with `busy` and changes nothing
  (`jailops exec JAIL -- fstat PATH` shows the process). The mount is made with the checks of a `fstab` line (a path inside the
  jail's root, no symlink on the way, a source that is a volume), and a failed update puts the record back. `--copy-existing`
  needs a stopped jail. A volume shared by several jails
  is not a warning: `volume show` lists `Attached to: mail (rw), desktop (ro)`.
- **Fixed: a volume of a Linux jail, `/proc` and `/sys` were empty for a Linux process.** The Linuxulator resolves every path
  through a nullfs mirror of the jail's root on `compat/linux`, and a mirror shows the plain directory under a mount. Volumes,
  `linprocfs` and `linsysfs` are mounted below the mirror too, at the start of the jail and by `volume attach` and `detach` on a
  running jail. `docs/linux-jails.md` lists what a Linux process sees and the limits of the Linuxulator.
- **`jailops shell` shows what it waits for while the bash of a new jail is installed:** what is going on, how to leave (Ctrl-C
  stops the waiting, not the setup; `jailops exec JAIL -- sh` works meanwhile), the step and the seconds. A failed setup names
  the step and the reason, and when pkg's output shows that the package server did not answer, the commands that tell which
  address family does not work. A command that needs no bash, and a shell that is not bash, enter the jail at once.
- **A stop, a restart, an update or an app installation waits three seconds for the setup of the shell**, then ends it gently
  (SIGTERM to pkg, SIGKILL three seconds later); the setup stays due and the next start tries again (event
  `jail-shell-setup-cancelled`). Its downloads give up after ten seconds for an address (`FETCH_TIMEOUT=10`, `FETCH_RETRY=1`; the
  helper accepts these two `pkg -o` options), so a dead IPv6 or IPv4 path fails fast and pkg goes on to the other family.
- **A jail whose start fails at boot is tried again** on the next passes of the reconcile loop (a few tries, ten seconds
  apart) instead of waiting for the next daemon start. A thin jail stored with `update_strategy: inplace` (an earlier version
  accepted it) stays updatable; `jailops doctor` warns (`jails.update`) and names `jailops jail set JAIL --update-strategy rebase`.
- **`jailops op show` is readable:** status with how long the operation ran and when it last changed, what it affects, the steps with
  their times, and the error with its hint; a running operation with no step says so.
- **The memory limit and the memory shown are named.** `jailops info JAIL` shows resident and reserved memory (`swapuse`, the
  counter `--memory` limits) next to the limit; the help and `docs/operations.md` say which counter is limited.
- **`jailops create` takes the host's only image** when `--image` is not given, and says so; `--update-strategy inplace` is refused
  for a thin jail at create and set; `jailops jail list` has a `USED` column (`disk_used` in the API).
- **Docs:** `design.md` names the unbound file among what JailOps writes on the host, lists volumes of an existing jail as built
  and puts the remotes in the client's configuration file.
- **CI:** Go modules are fetched with three attempts and `GOPROXY=https://proxy.golang.org|direct`, so a failed download from the
  module proxy does not fail a run (the composite action `.github/actions/go-modules`; the VM job retries the same way).
- **The host's own resolver is checked after every change of the names, and given back when the names are off.** After the
  helper writes the file of the names and has `local_unbound` read it, it asks the loopback nameservers of the host's
  `resolv.conf` (and the gateways it just made the resolver listen on) for `localhost`; an address that answered before
  and does not now puts the old file back, restarts the resolver once more, and says so. The text that failed is not tried
  again for ten minutes (it wrote the file and restarted the resolver at every pass), and a failed restart after the
  rollback is tried a second time and reported first. `jailops doctor` fails `dns.unbound` when a loopback nameserver of
  the host does not answer and warns (`dns.upstream`) when it answers but cannot resolve names outside. With `unbound-conf`
  empty the helper takes away the file it wrote earlier, and the package removes it (and the resolver addresses on `lo0`)
  when it is deinstalled. A host on 0.8.0 to 0.8.2 whose resolver lost its loopback needs `rm
  /var/unbound/conf.d/jailops.conf && service local_unbound restart` before `pkg upgrade` can download (docs/upgrade.md).
- **Fixed: the host's own resolver stopped answering on the loopback.** The unbound of the host listens on `127.0.0.1` and
  `::1` only while its configuration has no `interface:` line. The file JailOps writes for the jails' networks has such
  lines, so a host whose own configuration names none lost its resolver (`pkg update` failed with "Transient resolver
  failure"). The file names the loopback as well in that case.
- **Fixed: `jailops firewall test` from the host to a jail on an IPv6 network.** The resolver address of the network is on
  the host's loopback and answers DNS queries only; the test no longer tries it as a source of host traffic, which showed
  a block that no packet of the host meets.
## 0.8.3 - 2026-11-17

- **Fixed: a network that was just created was missing from `jailops network list`, `show`, `edit` and `get`.** The CLI sends
  the selected project with every command, and for the administrator of the host that narrowed the networks (and the images)
  to those the project is granted, so a network that is not granted yet looked as if it did not exist, while `network create`
  still said it did. A global administrator sees every network and image whatever project the request names; a project
  identity still sees only what its project is granted. `network create` and `storage create` end with the command that grants
  it to a project when no project has it, the refusal of a project that is not granted names the reason, and a jail or a
  volume that names no network or storage says that the project has none (and how to grant one) or lists the ones it has.
- **Fixed: the jails of an IPv6 only network had no resolver, and a dual stack network gave them an IPv4 one only.** A jail asks
  the host in the address families of its network: the IPv4 gateway, the resolver address of the IPv6 prefix (its second
  address, for example `2001:db8:1:2:100::1` in `2001:db8:1:2:100::/80`), or both, IPv4 first. While the names of the jails are
  on, the helper makes the host hold that address on its loopback as a `/128`, unbound listens on it, `host.PROJECT.internal`
  has an AAAA record for it, and `jailops doctor` asks every address the jails ask. The allocator keeps the address back from
  the jails; a network that is deleted loses it again.
- **Fixed: the first start of a jail does not wait for a package server, and the shell waits for bash.** A new jail starts at
  once. Bash is installed into the running jail in the background: `jailops start` says so, the jail shows a `ShellSetup`
  condition with the reason `Installing`, and `jailops shell` (or any command in the jail) waits up to a minute for it instead
  of answering that the shell does not exist. A later start, while bash is still missing, lets the host fetch it into the
  stopped root (three tries at most), and `jailops shell` says why when the installation failed. A stop waits for that setup
  to end instead of cutting `pkg` off in the middle, and `jailops update check` right after a start no longer panics.
- **The neighbor discovery setting `net.inet6.icmp6.nd6_onlink_ns_rfc4861` is information, not a warning.** It matters on some
  shared IPv6 segments only (those whose routers probe the host from their global addresses). `jailops network create` prints
  a `note:` that points at the explanation, `jailops doctor` shows the value as `info` while a proxy-mode network exists, and
  `sysctl.persistent` looks at forwarding only. The answer of a create or an update has a `notes` list next to `warnings`
  (`Status.Notes` in the SDK; the web console shows it as a note). The IPv6 docs say what the setting does, how to recognise
  the case and what to set; JailOps still never sets it.
- **Fixed: a release without its type was accepted.** `jailops image create NAME --release 15.1` (or a typo such as
  `15.1-RELESE`) was built from the `base_latest` packages without a word. A FreeBSD release has its type now
  (`15.1-RELEASE`, `15.2-PRERELEASE`, `16.0-CURRENT`, `15.1-RC2`), and the refusal names the release of the host. The
  console's image form says the same.
- **A proxy-mode subnet that holds the host's own address is refused when the network is made**, with a hint to take a
  part of the segment's prefix elsewhere, not when the first jail starts.
- **Onboarding texts.** The hints name `jailops server info` (`jailops info` takes a jail); the text after `pkg install`
  and the end of `jailops-setup` say that the CLI works as root and that `--web` adds the console, and the first-image hint
  names the release of the host. The install guide no longer asks for `pkg install jailops-apps` (it comes with
  `jailops`), names the plain jail `web`, and describes `remote add NAME ADDRESS` as the command is. The IPv6 guide says
  why the `nd6_onlink_ns_rfc4861` setting is recommended, that JailOps only flags it, and that a host outside appliance
  mode sets a static default route. The examples of the DNS pages use neutral names.
- **Fixed: a proxy-mode network on a host whose address is a `/128`.** A cloud server usually holds its address on the shared
  segment as a `/128`, and `jailops network create ... --ipv6-mode proxy` refused every subnet because no prefix of the uplink
  held it. The segment of an address longer than `/64` is the `/64` around it now, so a subnet inside it is accepted; one
  outside it is still refused, and the message names the segment.
## 0.8.2 - 2026-11-17

- **`jailops-setup` is for the CLI first: the web console is opt-in.** The setup no longer turns the web console on by
  itself. It asks "Serve the web console (port 39110)?" (default no; `-y` never answers yes), or take `--web`. Only with
  the console on is the first administrator offered, because the CLI as root over the local socket needs no user. A step
  that is optional (the host resolver, the console, the administrator) that is skipped or fails is a warning and does not
  make the exit status 1; a skipped pf change says so and what to do.
- **The install guide starts with `jailops-setup`.** Appliance mode (the setup token and the web wizard) is its own
  optional section.
- **`jailops user create --password-prompt`** says whether the password is empty or the two do not match, and asks again
  up to three times.
- **`service jailopsd start` before `jailops-setup`** says "no jailopsd.yaml: run jailops-setup first".

## 0.8.1 - 2026-11-16

- **Security: fixes from an external audit of 0.8.0.**
  - `etcupdate` through the helper no longer takes `-s` or `build`, and the `-t` archive is copied into the helper's own directory and listed before use, like the `tar` extraction.
  - A package name given to `pkg` is a name or a port origin: no `..`, no second slash, and nothing that ends as a package or archive file.
  - The `delegated` dataset of a jail is opened like the rest of the jail's tree (no link followed), and `fs.copy_tree` makes setuid and setgid files only in a jail tree.
  - A half-built copy with immutable flags is cleaned up, and the helper closes and refuses descriptors a client sends over `priv.sock`.
  - A directory made by the helper is changed through its descriptor, and a log is opened without following a link.
  - The backup node counts sign-in attempts before the hash (attempts at once cannot pass the limit), refuses a source that is locked before it asks the database about a token, waits in the shared queue for a hash instead of answering at once, and no longer writes the typed user name of a failed sign-in to the audit table; the audit filter by action prefix takes `%` and `_` literally.
  - A trust token is checked against the certificate the host serves now.
  - `jailops-priv init` makes `/var/tmp/jailops` and the directory of the rendered `jail.conf` files mode 0700.
  - `release.yml` does not keep the checkout's credentials.
- **Fixed: `jailops-setup`** handles `match ... scrub(` on the awk of the BSDs and writes `pf.conf` through a temporary file with a unique name; `bootstrap.sh` adds only `anchor "jailops/*"`.

- **Fixed: the jails of an older version get the search domain of their names.** A jail whose `resolv.conf` JailOps wrote before 0.8.0 (the line `# Managed by JailOps. Do not edit; changes are overwritten.`) was taken for a jail with a file of its own, so it never got `search PROJECT.internal` and the short names did not work. Such a file is JailOps's own and is written again at the next start of the jail.

- **Fixed: console output could come up blank.** The console of the web viewer sent the last event number of the event stream when it opened the stream of a jail's console, so the server skipped the first lines of output. Each stream keeps its own number now.
- **Security: a port forward that was switched off stays the global administrator's.** An administrator of a project could switch on a forward of a policy that a global administrator had disabled; enabling a forward needs the global role like adding one.
- **A failed scheduled backup is not only a line of the log.** `jailops doctor` has `backup.recent` (a warning for each policy and jail whose last scheduled backup failed, with the first line of the error), and the event stream gets `backup-failed`.
- **Backup node: one host cannot take every session.** A host holds at most eight open sessions (`MaxSessionsPerHost` of the node), so that one host cannot keep the others from backing up.
- **Security: a host cannot take another host's escrow.** The escrow blob lives in a slot of its own for each host (`escrow/DOMAIN/HOST.age`, named by the host identity the node authenticated), so a host that escrows first can neither pre-empt nor overwrite another's, and `backup key status` reports only the host's own blob. **Changed:** a blob in the old place (`escrow/DOMAIN.age`) is not found; run `jailops backup key escrow` on each host again. `escrow delete` removes the blobs of a domain for every host.
- **Backup node: a limit for each host.** `host_quota` in `backupd.yaml` (for example `500G`) caps what one host has stored on the node; a chunk over it is refused with `quota_exceeded`. Unset, there is no limit.
- **Fixed: help and docs that said otherwise.** A jail name has at most 29 characters and the stop timeout defaults to thirty seconds; the hint of `jailops explain forbidden` names `jailops storage grant`; `jail set --firewall-policy` replaces the whole list; the docs no longer tell you to write the unbound file by hand (the helper writes it: see docs/dns.md); the Referrer-Policy of the backup node is `no-referrer` like the host's.

## 0.8.0 - 2026-11-16

- **The names of the jails cannot take the host's resolver down.** The resolver checks the file of names before it reads it (`local-unbound-checkconf` with the new file in place); when it refuses, the old file is put back, nothing is reloaded and `jailops doctor` says what the resolver said. The resolver listens on the host's address of every network of a project, running jail or not, so it is restarted when the networks change and never when a jail starts or stops. The doctor line no longer names a jail or an address of another project.

- **Names for the jails, through the base system's local-unbound, with nothing to configure.** `jailops-setup` enables the
  host resolver (`local_unbound`) as part of the normal setup and leaves a resolver that already listens on port 53
  alone. The helper then writes `/var/unbound/conf.d/jailops.conf` with the names of the running jails and with what
  the resolver needs to answer them (`interface:` for the host's address on each network of a running jail,
  `ip-transparent: yes`, `access-control:` for the subnets). A jail called `postgres` in the project `shop` is
  `postgres.shop.internal` (A and AAAA), `postgres.v4.shop.internal` (IPv4 only) and `postgres.v6.shop.internal`
  (IPv6 only), and `postgres` through the `search` line of its `resolv.conf`; the host is `host.shop.internal`. A jail
  sees the names of its own project and of no other: the file holds a view for each project. The helper makes the text
  from the stored objects, not the daemon. It is written after a jail starts or stops and read with
  `local-unbound-control reload`; a new network with its first running jail adds an interface and restarts
  local-unbound once; `jailopsd` starts after `local_unbound`. `jailops jail info` has a `DNS` block (nameserver,
  search, the jail's names, where every other name goes) and the console shows it; `jailops doctor` has `dns.unbound`
  (`N names for M jails in P projects; suffix internal; unbound forwards everything else to ...`, a warning with the
  fix when the resolver is not enabled). Customising is three things: `dns-suffix` in `priv.conf` (`internal` by
  default), a network with `dns:` of its own (it keeps them and gets no names), and a jail's own `resolv.conf`.
  **Changed:** JailOps writes a jail's `/etc/resolv.conf` only while it is absent, empty or has the first line
  `# written by JailOps`; delete that line to keep your own file (the line `# jailops:keep` is gone).
  [docs/dns.md](docs/dns.md).

- **Breaking: one rule for every name a user chooses, and an address set takes hyphens.** A jail, project, network, storage, image, image alias, volume, snapshot, firewall policy, address set, profile, user, certificate, token, WireGuard tunnel, host of the backup node and every other name is lower case letters, digits and hyphens, starts with a letter and does not end in a hyphen (`^[a-z]([a-z0-9-]*[a-z0-9])?$`, `api.NamePattern`), at most 63 characters unless a derived name needs less (a project 13, a jail and an address set 29, a host firewall set 24). An underscore, a dot, an upper case letter and a digit at the start are refused everywhere; an address set called `git_allow` is `git-allow` now, and its pf table is `s_git_allow` (the hyphens of the name written as underscores, which is why no name has an underscore). Delete and make again what has an underscore in its name. The names of rules (`--name`) stay free labels, and the keys of labels keep their `domain/key` form.

- **`jailops firewall test` says it in a few lines.** The answer is the verdict and, for each end, the rule that decided (`web sends: allowed by the default of the anchor`, `db takes: allowed by policy postgres rule 1 "who may reach PostgreSQL"`). `-v` adds the rendered pf rules, the rules that were skipped and what the test does not cover.
- **Fixed: the time of a snapshot.** `jailops info` and `jailops jail snapshot list` showed `- ago` for every snapshot: the API never filled in when it was taken. It is read from ZFS now.
- **`jailops firewall rule add` allows by default.** Without `--action` the rule allows (`--action deny` blocks), and a rule with `--port` and no `--proto` is tcp.

- **An address set is made empty and filled one member at a time, like a policy and its rules.** `jailops firewall address-set create NAME` needs no address any more (`--comment` says what the set is; `--address` and `-f FILE` stay as shortcuts), `jailops firewall address-set add NAME MEMBER... [--comment TEXT]` adds members (one that is already there is left as it is and named) and `jailops firewall address-set remove NAME MEMBER...` takes them out; a member that is not in the set is an error. A rule that names an empty set matches nothing. **Breaking:** `--comment` of `create` was the comment of a single `--address`; it is the comment of the set now, and the comment of a member comes with `add`.

## 0.7.3 - 2026-11-16

- **Fixed: setting the clock does not make a restart of the daemon a boot.** FreeBSD moves `kern.boottime` when the clock is stepped, so a daemon that started again after a step took it for a new boot and started the jails an operator had stopped. Two readings within ten minutes are the same boot.

- **Fixed: a restart of the daemon starts no jail an operator stopped, on a real host.** The boot pass tells a boot of the host from a restart of the daemon by `kern.boottime`, which the helper did not let the daemon read, so every start of the daemon counted as a boot and started the jails with `boot.autostart` that had been stopped on purpose. `kern.boottime` is on the read list of the helper now, and a test keeps it there.
- **Fixed: a mirror that does not answer for a moment no longer fails an image build.** A `pkg` step of the build is tried three more times (after 5, 10 and 20 seconds) when the mirror cannot be reached or answers with a server error; what pkg already fetched is kept, so a retry asks only for what is missing. A checksum mismatch or a missing package is never retried.

## 0.7.2 - 2026-11-16

- **Fixed: a panic in a background loop no longer ends the daemon, and the daemon comes back on its own.** Every long-running loop of `jailopsd`
  (reconcile, housekeeping, the devd and host-directory watchers, the backup scheduler, the hub loops, the log forwarder, the certificate renewal)
  runs under a supervisor: a panic is logged with its stack, counted and the loop starts again after a delay that doubles from one second to a
  minute. `jailops doctor` warns (`daemon.loops`) while a loop was restarted in the last day, and a new check `operations.long` names operations that
  have run for more than 15 minutes. `jailopsd` and `jailops-backupd` start again after 10 seconds when they exit without being stopped (`-R 10` in
  their rc scripts; `service ... stop` stops the supervisor first, so a stop is not undone).
- **Fixed: exec sessions, the database copy and a few tables.** A command whose stdin pipe is full no longer hides a Ctrl-C or the client leaving
  (the input has its own queue of at most 1 MiB, then the session ends). The daily copy of the database runs on a connection of its own and no longer
  holds up every write of the daemon for the length of the copy (a 23 MiB database: the longest wait fell from the whole copy to 31 ms).
  A client that closes an exec session without reading its output leaks no goroutine, the log forwarder forgets deleted jails, the router
  solicitation proxy makes one timer of each kind and not three for every frame, and commands without a deadline (`netstat`, the key command, the
  commands of `jailops-priv init`) have one.
- **Faster with thousands of jails.** One write and one event for each step of an operation instead of two, and the reconcile loop waits three times
  as long as its last pass took (at most ten seconds) when kicks follow each other, so a bulk create no longer competes with passes that each
  read every jail: 1200 jails are made in 42 s instead of 101 s on the fakes.

- **Fixed: a jail that a failed stop or start left behind is cleaned up by JailOps itself.** A stop whose poststop hook had failed (the old bridge) left
  a `devfs` under `root/dev`, stacked once more by each further failed stop, and the root dataset stayed busy for good: every later stop took a minute.
  jail(8) does not run `exec.poststop` after a failed start and leaves its devfs mounted when it is killed, so nothing that the hooks made can be
  counted on to be removed by them. The helper has new operations: `mounts.stale` (read-only: the mounts of a kind JailOps makes below the root of
  a jail that does not run), `jail.heal` (one jail that does not run: those mounts, deepest first and one layer at a time, and what the poststop
  hook removes: rctl rules, the pf anchor, the links) and `net.heal` (the bridge and the gateway pair of the earlier network layout that no jail which
  runs uses). The daemon calls them, never from a hook (jail(8) mounts its own devfs before `exec.prestart`): as a step before `jail -c`, as the
  teardown of a stop and of a delete (also when the stop failed after the jail was gone), when it starts (every jail that does not run, and the old
  interfaces), and every ten minutes; and `jailops doctor --fix` does the same on request (`--dry-run` lists what it would do; the checks are
  `mounts.leftovers` and `network.leftovers`). A stop waits five seconds in all for the datasets to be released and says so (a `jail-unmount-busy`
  event naming the mounts below the root). The rendered configuration of a deleted jail is removed before its datasets, so a delete that dies in
  between leaves nothing. A jail's `stop.timeout` is always written into its configuration (its `boot.stop_timeout`, or 30 seconds): the stop commands
  get that long, the jail the same time again to exit after SIGTERM, and five seconds go on top; after that the jail is removed without its stop
  commands (`jail -R`), which kills what is left, and a `jail-stop-forced` event and the console log of the jail name the processes that still ran
  (65 seconds in all by default). A new app definition key `stop_timeout` (seconds) sets `boot.stop_timeout`: PostgreSQL says 60. `jailops op show`
  lists the steps of an operation with the time each took.
- **Fixed: a start and a stop no longer wait for what they do not need.** A stop waits at most five seconds in all for the
  datasets of the jail to be released after its processes died (a root that something on the host holds open used to cost ten
  seconds per dataset, up to a minute); what is still busy stays mounted until the next start or stop. A jail whose stop
  commands never return is removed after their budget (at least 50 seconds, longer with `boot.stop_timeout`, plus the
  jail's stop timeout to exit after SIGTERM): the jail is removed without them, which kills what is left, and the console log
  of the jail says so. A jail that has already started once never fetches bash from the host again, and a failed setup of
  bash in the background is tried again after half an hour, not at every start. `jailops op show` lists the steps of an
  operation with the time each took (`steps`: `jail-stop`, `teardown`, `unmount`, `mount`, `jail-start`, `shell-setup`, ...), so a slow
  stop or start explains itself.

- **Fixed: a jail that was started before the upgrade from a version with a bridge can be stopped and restarted.** The stop
  (the `poststop` hook) failed with `ifconfig: ioctl (SIOCDIFADDR): Can't assign requested address` and left the jail's link
  behind: the start had not given the link the gateway address, because the old gateway pair of the network still held it
  (the kernel answers `File exists` to a second `/32` of an address in the subnet of the old `/24`), and the stop then
  removed an address that was not there. Now the start takes the gateway address from that old interface of JailOps (never
  from an interface that is not JailOps's or from the link of another jail) and replaces the leftover link of the jail; the stop
  removes what is there, treats what is not as done, runs every step, destroys the link last and always, and only warns about
  what did not work. `jailops doctor` has a check `network.leftovers` for the bridge, the gateway pair and a loaded `if_bridge`
  of the earlier layout, with the commands that remove them ([troubleshooting](docs/troubleshooting.md)).

- **Security: port forwards are a global administrator's.** A redirect rule of a firewall policy, a `proxy.<name>` device of a
  jail and a profile that carries one need a global administrator (an administrator of a project was enough, and could forward
  the host's own ssh or console port into a jail). A forward is also refused on a port that a service of the host listens on
  (ssh, the console, the other JailOps listeners, the ACME challenge port while ACME is the source) and on a listen address
  that is not an address of the host. An administrator of a project still edits the other rules of a policy that has a forward.
  The console disables the forward controls for anyone else. `jailops config keys` marks these keys `global`.
- **Security: limits on the size of firewall objects.** A policy has at most 500 rules, an address set 5000 members, a project
  200 policies, 200 address sets and 20000 set members together, so one project cannot fill the table space pf has for every
  project. `jailops doctor` warns (`firewall.size`) at 80% of a limit.
- **Security: login and secret checks are counted before they are checked.** The current password before a change and a
  second-factor code, asked of a user who is signed in, are counted like a login, so a burst of wrong guesses sent at once
  is not checked one by one past the threshold. **The limit of one source is looser:** five failures lock one user at one
  source as before, but a source alone is locked after fifty failures over all users (it was five), so people behind one
  address are not locked out by a stranger's few wrong passwords; a spray over many users from one address is still stopped
  after fifty attempts. A valid token or hub claim is never refused because of bad tokens from its address (a wrong token from
  a locked source still answers with the lock). The backup node keys its limits by the /64 of an IPv6 source and has the same
  two limits.
- **Security: recovery codes are 128 bits and salted.** A recovery code of the second factor had 50 bits and an unsalted
  SHA-256, which a copy of the database could search. A new code has 128 random bits (`xxxxx-xxxxx-xxxxx-xxxxx-xxxxxx`), a
  salt of its own and is compared in constant time. **The recovery codes made before stop working: make new ones** in the
  console (it asks for the password again).
- **Security: errors do not carry host details.** An error the daemon did not expect answers `internal error` and the id of
  the request; the cause (a store, helper or command error that can carry paths and command output) is in the daemon log
  under that id.
- **Fixed: `jailops jail set --tag ""` clears the tags** (it stored an empty tag the server then refused), `set --help` says
  that `--env` and `--label` add to the stored ones, and `network.<device>.<field>` is no longer a spelling of
  `nic.<device>.<field>`.

- **Fixed: `project delete` no longer destroys volumes.** A project that still has volumes is not deleted ("1 volumes"):
  delete them first with `jailops volume delete`, which asks before it destroys data. A dataset below the project that no
  object accounts for (a jail container a replace-restore parked as `<name>.replaced-<id>`, a volume dataset an interrupted
  creation left) is named in the refusal and kept until the operator destroys it; `jailops doctor` lists these in
  `storage.leftovers`, and a replace-restore gives the parked container's `refreservation` back to the pool. Revoking the
  storage grant of a project that has volumes on it is refused.
- **Fixed: a profile is not a way around what a jail may attach or use.** Editing a profile now passes what the same change
  on the jail passes: a host directory is given by a global administrator only, an encrypted volume belongs to one jail (a
  profile cannot give it to two, and neither can a restore or a copy), the volume must exist, and the project's quotas hold
  with the limits a profile gives (a jail's quota usage counts its profiles; it counted only its own spec).
- **Fixed: a daemon killed while it creates a volume.** The recovery at the next start removes the dataset the run made, so
  the name can be used again, and `jailops doctor` fails on a run the recovery could not resolve instead of saying
  "operations are in flight". A test fails when a kind of run that is journalled has no recovery. A journal record that
  outlives a completed plan is dropped by the recovery instead of undoing the plan.
- **Fixed: a restart of the daemon starts no jail.** Jails with `boot.autostart` start when the host boots, not when the
  daemon starts again on a host that stayed up (a restart, a package upgrade): a jail an operator stopped for maintenance
  stays stopped.
- **Fixed: a failed upgrade does not roll back what the jail wrote to `/var/db`.** After the jail started on the new
  generation, a failed health check swaps the root back and puts `/etc` back, and keeps the data the jail wrote to
  `/var/db` (the databases of the packages). Before the start both go back to the moment before the merge.
- **Fixed: a host directory that does not answer costs one helper request.** The daemon sends one probe per directory at a
  time and joins every look that comes meanwhile (the watch, a list, a show, `doctor`); reads use the answer of the watch
  or wait a few seconds, and a list probes the directories side by side. A remount that fails leaves a read-only tmpfs on
  the jail's mount point (not the empty directory of its root) and is tried again every round; a box that came back while
  the daemon was down is found at the next start; a second look after `jail -c` stops a jail whose mount went away in
  between.
- **Fixed: a volume backup is the volume's own.** A jail backup records the UUID of each volume the jail attaches (its
  profiles' included), and a restore brings a missing volume back only from a backup of that volume, not from a newer one
  of another volume that had the name.
- **Fixed: smaller things.** `limits.disk` does not cover volumes (a volume is a dataset of the project: the docs said it
  did) and `volume list` says `unlimited` for a volume without `--size`; deleting a jail or a volume while a backup of it
  runs is refused; `jailops stop-all` stops up to eight jails at a time, each after the jails that depend on it.
- **Security: the helper checks what it runs, not what the daemon wrote.** The package repository directory is read in
  full by the helper (all entries, whole files, no key twice, one block), copied into a directory of its own and given to
  `pkg` there, so a repository the daemon chose can no longer be slipped past the check (a 65th file, bytes after the first
  8 KiB, a key given twice, a rewrite between the check and the run). An archive that is extracted is copied and listed
  in the same way. A key path with a variable twice is refused.
- **Security: a jail's root is its own root.** The `path` of a jail must be `<storage mount>/projects/<project>/jails/<name>/root`
  of that jail and must reach it without a link, at every start too: a link in `path` made the root of the jail `/`.
- **Security: nothing is mounted below a volume.** A volume mounted below another volume (or two on one mount point) is
  refused in the spec and in the fstab; a mount that lands elsewhere than at its path is unmounted at once; `umount` takes
  only a mount point of the host, and `chmod` is not done in the tree of a running jail.
- **Security: addresses, routes and forwards belong to a network.** The helper adds an address to a jail's link or a route
  through it only inside the subnet of a stored network; the IPv6 proxy's route only for an address inside the prefix of a
  network in proxy mode (not the anycast address, the default router or a neighbor of the uplink); a port forward leads to
  an address of a stored network and takes over a privileged port, or one a host process listens on, only for an
  administrator.
- **Security: smaller things.** A FIFO in a jail's tree is refused, not waited on. A host directory whose mount does not
  answer gives up after ten seconds and holds one of four probe slots instead of the helper's whole budget. The audit log
  records the program of an exec session and the number of its arguments, not the arguments.

- **Security: a jail with a link and no address is closed, not open.** A jail created with `--network lan=none` got an
  epair and no anchor at all: no anti-spoofing, none of the project's mandatory policies, no isolation. It could take the
  address of another jail and send in its name. Every jail with a link now has an anchor; when it has no address the anchor
  drops everything in both directions. A jail with a link and no rendered anchor does not start.
- **Security: `any` is not the host.** As the destination of an allow rule, `any` (named or left out) no longer includes the
  host's own addresses or the subnets of other projects: `allow out tcp port 22` lets a jail ssh to the internet, not to the
  sshd of the host or to a jail of another project, as the allow rule decided before the host and isolation blocks. Name the host
  (`--to host`) or a prefix of the other project to open them; a deny rule to `any` still denies them. Every anchor defines a
  new table, `not_any`. The pass rules of the anchors change text, so their labels change and open flows reconnect once.
- **Security: pf off or flushed.** A jail with a network link does not start while pf is disabled (`allow-start-without-pf`
  in `priv.conf` says otherwise), `jailops doctor` fails `pf.enabled`, and when `service pf restart` has emptied the anchors the
  daemon loads them again within seconds instead of at the next pass a minute later.
- **Neighbor solicitations: a flood from one sender no longer starves the router.** The proxy charges a solicitation to the
  Ethernet source of the frame first and to the global budget only when it passes, keeps a share of the global budget for the
  requesters it has answered over time (the router), and no longer trusts the source link-layer option of a solicitation,
  which a sender chooses itself, as a key.
- **Fixed: a comment in a raw fragment, a forwarded port taken twice, dummynet, WireGuard, allocation.** A comment of the
  administrator's own in a raw PF fragment no longer turns labelling on for the rules after it (a rule that already said `keep
  state` got a second one and the anchor did not load): the fragment is enclosed by two marker lines. Two jails cannot forward the
  same host port (a proxy device or a redirect rule): the second is refused, naming the first. A dummynet pipe a loaded anchor still
  names is not removed, and pipe numbers that cannot be saved stop the limit from being applied instead of being renumbered at the
  next start. The WireGuard anchor labels its pass rules and binds their states to the interface. A `/80` at the end of its `/64`
  does not hand out the reserved subnet anycast identifiers. A network must be an ordinary unicast range (IPv4 not in 0/8,
  loopback, link-local, multicast or 240/4; IPv6 global or unique-local). Removed with it: the unused project sub-range code of
  the address allocator.

## 0.7.1 - 2026-11-15

- **`rename` for the rest.** `jailops volume|storage|cert|user|wireguard|backup target|backup policy rename OLD NEW`
  give the object a new name and change everything that names it in one operation, or none of it: a volume (its dataset and
  mount point, the attachments of its jails, which must be stopped, and the `disk.NAME` keys of the project's profiles), a
  storage (the grants of the projects, the volumes and jails on it and the registry of the daemon; the default storage keeps its
  name), a backup target (the targets lists of the policies), a user (the password, the second factor and the sessions stay),
  a certificate, a backup policy and a WireGuard tunnel (the directory of its keys, which a new helper operation `wg.rename`
  moves inside the helper's own WireGuard directory, and its firewall anchor). The backups that were made keep the old
  names of their policy and target in their records, and the audit log keeps the names a user or certificate had.
- **Breaking: the host firewall rows and sets have commands of their own.** `jailops host firewall set` no longer takes
  `--allow`, `--clear-allow` or `--set`. Rows of the custom-rules template are `host firewall allow add --proto tcp --port
  80,443 --comment "the web"`, `allow list` and `allow remove NUMBER`; address sets are `host firewall address-set
  create|set|list|delete NAME --member ...`. Each change still reverts itself unless it is confirmed.
- **Breaking: `host_mounts` is gone from the jail spec.** A directory of the host that a jail should see is a volume with
  `--path` (`jailops volume create movies --path /mnt/archive/movies`, then `volume attach`), which a global administrator makes,
  which the daemon can watch and which has its place in the console. The `admin-host-mount` directories of `priv.conf` stay the
  allow-list for it.
- **Breaking: one shape for every command.** Every group that makes objects from flags now has `set NAME --flag ...`
  (only the flags you give change anything; a list flag replaces the list and an empty value clears it) next to
  `edit`, which stays the editor and `-f FILE` path. A human sentence about an object is `--comment` everywhere
  (`comment` in the API and in files; `description` is gone from projects, storage, profiles, templates and
  rollouts) and exists on jails, networks, images, certificates, tokens, users, hosts, backup policies and targets and
  WireGuard tunnels.
- **Breaking: one verb for one idea.** The `trust` group is gone: `jailops cert enroll NAME` makes the single-use
  token of a new client (`--name` is the positional) and `jailops cert list|show|edit|revoke` manage who is trusted;
  `cert` and `token` end with `revoke`, not `delete`. `remote add NAME ADDRESS` takes the name first; a trust token
  joins, an API token is saved. `logs forward add NAME --url URL` (the type follows the URL) and `logs forward set`.
  `app install APP [JAIL]`, `app allow APP --from SOURCE [--port N --proto udp]` and `app deny APP --from SOURCE`.
  `wireguard join NAME --token-file FILE`. Networks and storage are granted with `network grant|revoke` and
  `storage grant|revoke [--quota 500G]`: `project create|set` no longer take `--network`, `--storage` or `--revoke-*`.
  Volumes are attached with `volume attach`: `jail create` has no `--volume` or `--host-mount`.
  Images: `image set NAME --auto-upgrade --upgrade-at HH:MM --upgrade-packages=false` replaces `auto-upgrade`,
  `image alias add|remove NAME ALIAS [--generation gN]`, `image generation list|delete`.
- **Breaking: one meaning for one flag.** `--stop` (delete a running jail after stopping it) instead of `--force`;
  `--force` only means "do not wait for a graceful shutdown". `--overwrite` and `-f/--file` for `app new` and
  `app export`; `--from-generation` for `image build`; `--package-set` for image package sets; `--url` for the address
  of a backup target and of a host; `--no-volumes` for backup policies too; `--confirm-lockout` and
  `--confirm-replace` instead of `--i-know` (`confirm_lockout` in the API); `--images` for `update apply --os`;
  secrets only from `--password-file FILE|-` and `--token-file FILE|-`. No old spelling is accepted.

- **Breaking: the rules of a policy are changed with `jailops firewall rule`, and `--rule` is gone.** A rule is no longer a
  sentence inside a flag, with its comment as the tail of the sentence: it has a flag for each thing it says.
  `jailops firewall policy create web --comment "the public web server"` makes the policy; `jailops firewall rule add web
  in --action allow --proto tcp --from set:office --port 22 --comment "ssh from the office"` adds a rule (the direction is
  the word after the policy; `--at N` puts it at a position; `--name` gives it a name; `--log`, `--redirect
  HOSTPORT:JAILPORT` and `--disabled` do what their words say). `rule list` numbers the rules the way `policy show` and
  the rendered anchor do, `rule edit` changes the fields you give (an empty value clears one), `rule move` takes `--to N`,
  `--before` or `--after`, `rule enable` and `rule disable` switch rules on and off, and `rule remove` takes a number or a
  name. A rule is addressed by its number or its name; each command writes the policy back with the version it read, prints
  one line of what changed, and takes `--dry-run`, `-o json` and `-o yaml`. Tab completes every word: the policy, the
  direction, the references of `--from` and `--to` (also in a comma list), the ports, and the rules of a policy with their
  comments. `-f FILE` and `policy edit` still take a whole policy. `jailops firewall test` says that `--port` is needed
  when the protocol is the default, tcp.

- **Tab completes everywhere.** Every flag and argument of `jailops` either completes something or says it is typed, and a
  test fails for a new one that does neither. What names an object completes from the server in the right project (a
  firewall policy in `project set NAME --firewall-policy`, volumes, images, users, sessions, label keys and values, tags,
  hosts, projects); `jailops config set JAIL KEY` completes the value of the key from the key registry (`true` and `false`,
  the values of a choice, networks, storages, policies, profiles, jails); the ends of `jailops firewall test` offer
  `jail:NAME`, `host` and the addresses of the jails (no address sets); files complete where a flag names a file (`-f`
  only YAML and JSON) and directories where it names a directory. Where you type a new name, a number or free text the
  shell no longer offers the files of the current directory (a bare `.` after `jailops firewall policy create`): nothing
  completes and a second Tab says what is expected (cobra's ActiveHelp, off with `COBRA_ACTIVE_HELP=0`), and `exec NAME --`
  completes nothing of this host. The bash script now works without the `bash-completion` package (`source <(jailops
  completion bash)`), and a test drives it the way bash does, with and without the package, colons in `jail:web` included.

- **Security: `pkg` no longer runs as root of the host in the root of a running jail.** The scripts of a package run in a
  chroot of the root `pkg -r` is given, with the shell and the libraries found there, so a process of a running jail that
  replaced them would have had root of the host at the next package operation. The helper now refuses `pkg -r ROOT`
  for the root of a running jail (all spellings of the path) and allows exactly one of `-r ROOT` and `-j JAIL`. An app
  installation makes the jail, mounts its datasets and installs the packages while the jail has never run (new steps
  `mount-jail`, and `seed-jailops-repo` for packages of the JailOps repository, which copies the host's configuration
  of that repository and its key into the jail), and starts it after `record-version`; a failure from the mount on leaves the
  stopped jail to look at, also when it is the start that fails. Package updates, the bash of the shell setup and the
  app draft of a running jail run `pkg -j JAIL`: `pkg` works inside the jail, with the jail's own repository
  configuration, so a jail has to be able to reach the package servers.
- **The end-to-end run no longer races.** The stop timeout block waits until the service has set its signal handler, and the
  soak waits until its listeners are up.

## 0.7.0 - 2026-11-14

- **A jail can ask for more time to shut down.** `boot.stop_timeout` (`jailops config set db boot.stop_timeout 60`, or
  `--stop-timeout 60` when the jail is made) is the number of seconds a jail has after its stop command before it is
  killed, from 1 to 3600; unset is the ten seconds of jail(8). It is written as `stop.timeout` into the jail's
  configuration at its next start. `jailops stop`, `restart`, `stop-all` and the stop at shutdown wait as long as the
  jail asks: the helper's own limit for `jail -r` is two minutes plus the jail's timeout. `jailops_shutdown_timeout`
  has to cover the jails together.

- **Volumes can be directories of the host.** `jailops volume create NAME --path /mnt/storagebox/backups` makes a volume of
  a mount the host operator allows with `admin-host-mount` (the mount point has to lie below that directory: list `/mnt`, not
  the box). Only a global administrator makes or attaches one. There is no dataset: no snapshots, size, encryption or
  backup by JailOps, and `--copy-existing` is refused. The helper looks at the path (new operation `fs.source_probe`: no
  link on the way, a mount point on the way, optionally a file made and removed) when the volume is made and at every
  jail start, and a jail whose directory is not on its mount does not start, so it never writes to the empty directory a lost
  mount leaves behind. `--watch` (`--probe mounted|write`, `--on-loss none|stop`) has the daemon look every few seconds,
  show `state: ok|lost` in the volume, the console and `jailops doctor` (`volume.hostdirs`), send `volume-lost`,
  `volume-restored` and `volume-remounted` events, stop the jails that attach it with `stop`, and give the running jails their
  nullfs mount again when the directory is back (a mount made under a nullfs mount afterwards is not seen through it).
- **Breaking: the host is the router, and there is no bridge.** A network is an address range with an uplink policy and
  has no interface of its own. Every jail interface is a routed point-to-point link to the host: the host end of its
  epair carries the network's gateway as a `/32` (and the link-local `fe80::1`), the host holds a `/32` and a `/128` route
  per jail address through it, and the host answers a packet for an address of the subnet that no jail has with an
  error instead of sending it out of the uplink (a reject route for the subnet, or for a routed IPv6 prefix). Jails share
  no segment: none sees another's frames or can poison its neighbour cache, and there is no layer 2 between jails
  (no multicast, VRRP or mDNS between them). Ingress limits work now (`nic.<device>.ingress`). Removed: `--bridge` and
  `spec.bridge`, `ipv6.gateway` (the IPv6 gateway of a jail is always `fe80::1`), `status.bridge_up` and
  `status.members` (the running links are `status.links`), the interface of `firewall test` hops, the check
  `network.bridges`, the kernel module `if_bridge`, the `network.sysctl` configuration and the `net.link.bridge.*`
  sysctls, and the rule that an attachment's MTU must be the network's. `--ipv6 PREFIX` takes a prefix (`fd50::/64`).
  A project name now only prefixes the OS name of its jails. Rules of a jail's anchor stand on the host end of its own
  link only, with `keep state (if-bound)` on every pass rule, and the anchor of a jail is loaded before the jail's first
  process runs. See [docs/networking.md](docs/networking.md).

- **The IPv6 proxy limits the solicitations it looks at.** One hardware address may send 50 neighbor solicitations a second
  (a burst of 100) and all together 2000 a second (a burst of 4000); the rest are dropped without an answer, so a neighbor on the
  uplink that floods it cannot use the host's time or the budget the upstream router needs. The numbers are fixed. The drops are
  counted and logged at most once a minute, `jailops doctor` warns (`network.ndproxy`) with the count of the last hour, and the
  network has an `NDPFlood` condition while there are some.

- **IPv6 proxy mode needs no `priv.conf` line.** When an administrator creates a network in proxy mode, the helper accepts
  its prefix itself: between /48 and /120, global or unique-local, no address of the host inside it, no overlap with another
  network, and the uplink a plain interface. The line `ndp-proxy` and the helper flag of that name are gone, as is the
  `JAILOPS_NDP_PROXY` variable of the provisioning script.

- **Long commands and parallel starts.** A command run with `jailops exec` without a terminal is no longer killed after ten
  seconds: it runs until it ends. Starting several jails of one network at once no longer fails with `busy`, and the helper
  accepts as many requests at once as it has exec sessions to serve.

- **Backups include the volumes, once each.** A volume is a backup unit of its own, taken from its own snapshot: a policy
  backs up the volumes of the jails it selects and the volumes whose labels match its selector, each volume once per run
  however many jails attach it, and a jail's backup holds the attachment and no volume files. `exclude_volumes` on a
  policy (`jailops backup policy create --exclude-volumes`) and `jailops backup run POLICY --no-volumes` leave them out.
  `jailops backup list` has a KIND column and `--volume NAME`. A jail restore brings back the volumes the project lacks
  from their newest backup before the jail (`jailops backup restore --no-volumes` restores the jail with no volume attachments); a
  volume backup is restored on its own as a new volume (`jailops backup restore ID --as NAME`, never replacing one), and
  an encrypted volume needs its passphrase for that (`--passphrase-file`) and is not restored with the jail. A locked
  volume is skipped with the code `locked` until it is unlocked. API: `exclude_volumes` on `BackupPolicySpec`, `kind` and
  `volume` on `BackupSpec`, `no_volumes` and `passphrase` on `RestoreRequest`, `no_volumes` on `BackupRunRequest`. A
  restore test (`verify --depth full`) restores the jail without any volume and never touches the live data; a volume
  backup is tested at `extract` depth at most.

- **Breaking: volumes are objects of a project, attached to jails.** A volume is made on its own and a jail attaches it
  by name: `jailops volume create maildir --uid 5000 --gid 5000 --mode 0770`, then `jailops create mail ...
  --volume maildir:/var/mail/vhosts` or `jailops volume attach mail maildir /var/mail/vhosts`. It lives at
  `<storage>/projects/<project>/volumes/<name>`, is mounted on the host and reaches the jail through a `nullfs` line of
  its fstab (the helper allows only the root of a volume as such a source), and outlives the jails: deleting a jail never
  deletes a volume, a jail's snapshot, rollback, upgrade and copy leave volumes alone (a copy attaches none), and one
  volume can be attached to several jails (a volume writable in more than one says so). The owner and the mode of its
  root are set once, at creation, by a new narrow helper operation (`fs.volume_owner`: the empty root of a volume,
  opened one plain directory at a time, no setuid, setgid or sticky bit), so a user with the same number in a mail
  server and a desktop jail reads the same files. Attaching and detaching need the jail stopped; the attachment is the
  jail's `volumes: [{volume, mount, read_only}]` (and `disk.<volume>=<path>`), so a file, `edit` and `config` do the
  same. New: `jailops volume create | list | show | edit | delete | attach | detach | snapshot create|list|delete|rollback
  | unlock | lock | passphrase`, `kind: Volume` in `apply` and in a fleet repository (`projects/<p>/volumes/`, applied before
  the jails), API `/api/v1/volumes` and `/api/v1/jails/<n>/volumes` (extension `volumes`, which replaces `volume_unlock`),
  a Volumes page in the console, `jailops volume attach --copy-existing`, doctor check `jail.volumes`. **Changed:** an
  encrypted volume is created with `jailops volume create --encrypted` and unlocked with `jailops volume unlock`
  (`jail unlock`, `lock` and `passphrase` and `volume_passphrase` in the jail's create are gone); an encrypted volume is
  attached to one jail, and that jail is locked while the volume is. An app's `data` paths become volumes named
  `<jail>-<name>`, made by steps of the install plan. **Removed:** the
  per-jail volume datasets (`<jail>/data/<volume>`), the volume quota in `--volume` and in `disk.<volume>.quota` (use
  `volume create --size`), and the data/all rollback of volumes.

- **Security: the helper no longer lets the daemon choose whom it trusts when it runs `pkg` or `debootstrap` as root.** A
  repository configuration that the daemon wrote for an image build could name any https host and a key directory in the
  daemon's own work area, so a compromised daemon could have signed a package itself and had its install script run as
  root. Now a package repository or a `debootstrap` mirror must be on a host listed in `fetch-host` (defaults:
  the Alpine CDN, `pkg.freebsd.org`, `pkg.jailops.net`, `deb.debian.org`, `archive.ubuntu.com`, `ports.ubuntu.com`;
  `http://<host>` allows plain http for a repository there), the key directory must be below `/usr/share/keys` or the new
  `pkg-keys-dir` (default `/usr/local/etc/jailops/keys`), and the `debootstrap` keyring must be a file in the new
  `keyring-dir` (default `/usr/local/share/keyrings`). Both directories belong to root, are not writable by others, hold
  no symbolic links and lie outside the daemon's directories; the helper checks them every time. An image built from a
  repository of your own needs its host in `fetch-host` and its key directory below `pkg-keys-dir`, placed by root.

- **Breaking: NAT and port forwards are options on rules, and pf.conf needs one line.** The anchors of JailOps hold the
  translation as `match out on <uplink> … nat-to (<uplink>)` for the NAT of a network and `match in on <uplink> … rdr-to
  <jail> port N` for a port forward, which is how pf describes translation in FreeBSD 15; the older translation ruleset
  (`nat-anchor`, `rdr-anchor`) is no longer used. The only line `/etc/pf.conf` needs is `anchor "jailops/*"`, among the
  filter rules and before any `quick` rule that should not decide first (such a rule also keeps the NAT and the port
  forwards of the networks from applying to the traffic it matches): delete the two lines `nat-anchor "jailops/*"` and
  `rdr-anchor "jailops/*"`. `jailops-setup`, `jailops doctor` (`pf.anchors`), the host firewall module and the lines the
  console and the CLI print for the ACME redirect (`pass in quick … rdr-to`) follow.
- **Bandwidth limits are enforced in both directions: `nic.<device>.egress` and `nic.<device>.ingress`.** The host
  enforces the limit of what a jail sends and of what it receives with dummynet pipes that the jail's anchor sends the
  traffic into (`dnpipe` on `match` rules, on the jail's own link), for every connection whoever started it. A rate is
  in bits per second with decimal units: `10M` is 10 Mbit/s. The pipes (numbers 40000 to 59999, JailOps's own) get their
  number once, when a limit is first set, and keep it as long as the limit exists (`<state_dir>/rendered/pipes.json`), so
  no jail's pipe is renumbered by a change of another's. They are configured through the helper when the jail starts and
  kept right by the reconcile loop, and go away with the limit or the jail, after the anchors that named them are
  loaded. `dummynet` is loaded when a limit exists and `jailops doctor` fails `kernel.module.dummynet` when a limit is
  set and the module is missing.
- **A port forward ends its connections when it is removed.** The packet that comes out of a forward's translation is
  passed by a rule of its own on the uplink (`pass in quick on <uplink> … to <jail> port N`), which carries the label that
  every stateful rule gets, so the state of a connection that came through the forward is ended with the forward
  (`pfctl -k label`); a state that pf's default rule made has no label to end it by. The forward is the allow rule of its
  policy: it also admits what it names where the host's own rules would block inbound traffic.
- **ZFS delegation works.** A jail with `zfs_delegation` (`jailops create --zfs-delegation`, `jailops config set JAIL
  zfs.delegation true`, global administrators only) manages a dataset of its own, `<container>/delegated`: it has
  `jailed=on`, the host does not mount it, `jail.conf` names it (`zfs.dataset`) and `jail(8)` attaches it at every start.
  The host operator must allow it with `admin-zfs-delegation` in `priv.conf`; the helper sets `jailed` on that dataset
  only. Snapshots, data rollback and `jail copy` include it, a backup leaves it out and says so, turning it off keeps the
  data, and `jailops doctor` checks `jail.delegation`.

- **The limits of a jail are enforced.** `limits.memory`, `limits.pids` and `limits.cpu` were only counted against the
  project's quotas. The helper now adds rctl rules when the jail has started: `swapuse` (the anonymous memory the jail
  reserves; an allocation beyond it fails and nothing is killed), `maxproc` (fork fails) and `pcpu` (the jail is slowed down to its cores). They need
  `kern.racct.enable=1` in `/boot/loader.conf`; without it the jail still starts, `jailops doctor` fails and the jail
  shows the condition `Limits` (`NotEnforced`). The helper removes the rules when the jail stops and when it is deleted, and again before it adds them at a start. They
  take effect at the next start.
- **`limits.disk` is the quota of the whole jail.** It is set on the jail's container dataset, so it covers the root, the
  persistent datasets, the volumes and the snapshots, not only the root clone; `limits.disk_reservation` moves with it.
  A change of either applies at once (ZFS refuses a quota below what the jail uses). **Breaking:** the root clone no
  longer carries the quota.
- **`limits.states`** limits the pf states that each pass rule of a jail's anchor may create (`keep state (max N)`), so
  one jail cannot fill the host's state table. A jail that sets none gets `jails.max_states` of `jailopsd.yaml`, 10000
  by default (`0`: no limit).

- **Security: the devfs ruleset of a jail is loaded before the jail starts.** A Linux jail, and a FreeBSD jail given devices,
  mounts `/dev` with a private ruleset, but nothing loaded it: the kernel treats a ruleset number nobody loaded as an empty
  ruleset, which hides nothing, so the jail saw the host's whole `/dev`, raw disks included. The helper now loads the rules in
  the jail's pre-start hook (`devfs rule -s N add ...`, each rule checked against its own grammar), and a jail whose ruleset
  cannot be loaded does not start. The number of a ruleset is the bits of its devices (5000 and up, 64 more for a Linux jail),
  so two different sets of devices never share one. The `devfs.rules` file that was written beside the configuration, and
  its check, are gone.
- **Jails are stopped cleanly when the host shuts down.** The rc script `jailops_shutdown` (enabled with `jailopsd_enable`)
  runs `jailops stop-all` before the daemon and the helper stop: every running jail of every project is stopped one after
  the other, a jail before the jails in its `boot.depends` (the reverse of `jailops boot`), so databases get their shutdown
  and the poststop hooks run. The knobs are `jailops_shutdown_enable`, `_timeout` and `_grace`;
  `service jailopsd restart` still leaves the jails alone.
- Removed: `freebsd-update` from the helper's command allow-list (JailOps builds images from pkgbase and never ran it) and
  the unused `jail_get(2)` reader.

- **The kernel's notifications become events.** `jailopsd` reads devd's socket and publishes a ZFS error
  (`host-zfs-error`), a pool or device changing state or a scrub finishing (`host-zfs-pool`), the link of the uplink going up or down (`host-link-up`, `host-link-down`) and a limit that a jail hit (`jail-limit-hit`, with the
  rctl action `devctl`) in the event stream, with a one-line `detail`. Identical events are held back for 30 seconds and at
  most 30 are published a minute. `jailops doctor` has the check `host.devd`; `devd.enable` and `devd.pipe` in
  `jailopsd.yaml`.

- **Security: package updates and the bash setup of a running FreeBSD jail hold the jail.** While the host runs `pkg`
  against the jail's root for `jailops update`, the scheduled package update or `jailops jail shell-setup`, exec, shell
  and console sessions into that jail are refused (`busy`), as they already were during an app installation. The `busy`
  message says "packages are being installed or updated".

- **The prompt of a jail says where you are.** Every interactive bash of a FreeBSD jail has `JAILOPS_JAIL`,
  `JAILOPS_PROJECT` and `JAILOPS_HOST` (the host's short name). They come from the jail's own parameters: every start
  renders `env.jailops_jail`, `env.jailops_project` and `env.jailops_host` into its `jail.conf`, FreeBSD gives them to the
  jail as the read-only sysctl `security.jail.env` (`jls -j JAIL env` on the host, `sysctl -n security.jail.env` in the
  jail), and the same `jailops-env.sh` in every jail exports them, so they are right after a rename and after the jail
  has moved to another host. The default prompt reads them each time it is shown: `root@web [shop/babel]:/#`, with
  `user@jail` in the colour of `jails.prompt_color` and the tag in grey. `short` is the same without the tag
  (`jailops config set JAIL prompt short`, `--prompt short`, `jails.prompt`); `none` removes the prompt and keeps the
  variables. A prompt text may hold `{jail}`, `{project}` and `{host}`, which become references to the variables; any
  other brace is refused. Host names of jails do not change.

- **Breaking: a reference is `kind:name`, and `@` is gone.** `jail:NAME` (a jail of the project, its addresses read at every
  render), `set:NAME` (an address set) and `tag:NAME`, in rules, address-set members, `app allow` and `firewall test`;
  `@web` is refused with the words to write instead. A jail and an address set may have the same name, so the rule that
  forbade it, and every path that had to check it, is removed; the pf tables are `j_`, `s_` and `t_`. A jail name has at
  most 29 characters. A `jail:NAME` that does not exist renders as an empty table and `jailops doctor` warns
  (`firewall.refs`), so a policy may name a jail made later and a fresh host takes a whole fleet repository in one apply.
  Tab completes the kinds, then the names, also inside `--rule "..."`; `@` and Tab lists every reference.
- **`jailops firewall validate --review`** adds warnings a reviewer would raise (missing comments, disabled or very broad
  rules, unused sets and policies, jails that do not exist); they never change the exit status.

- **A firewall simulator in the console.** The Firewall page has a Test tab (`/firewall/test`): choose where a connection
  starts, where it goes, a protocol and a port, and see it flow through the firewall, each step with its verdict, what
  decided it (the policy and rule with its comment, the project's policy, an app's managed policy, the isolation of
  projects, the default), the rule as loaded in pf and the rules passed over. The question is in the address of the page,
  and the equivalent `jailops firewall test` line has a Copy button.

## 0.6.6 - 2026-11-11

- **Breaking: deny wins over allow, whatever the order of the policies.** The anchor of a jail with an enabled deny rule holds
  the deny rules of all its policies first (`# deny rules of all policies: they win over every allow rule`), then the allow
  rules of the policies, each group in the order of the policies and the rules, with the headings and the rule numbers of the
  policies; the same two groups stand on the gateway interface. An anchor without a deny rule is written as it was. The
  order of a jail's policies and of their rules no longer decides between an allow and a deny; `jailops firewall test` says
  which allow rules a deny rule won over.
- **A firewall rule can be switched off.** `disabled` on a rule (`--rule "allow in tcp port 22 disabled comment ..."`,
  `disabled: true` in a file, a checkbox in the console) keeps it in the policy with its number and shows it as a comment line
  of the anchor and as `disabled` in `policy show`, without loading it; `firewall test` names a disabled rule the packet
  would have met.

- **Breaking: a rule names only the other end.** An inbound rule is always to the jail itself and an outbound rule always
  from it, so `to` on an inbound rule and `from` on an outbound rule are refused (`allow in tcp from @office port 22`,
  `allow out tcp to 192.0.2.7 port 5432`). A policy that several jails share cannot name the address of one of them, and
  an inbound rule with its own `to` was rendered on the network's shared gateway interface, where it could decide for
  other jails.

- **`jailops firewall test`: would this connection pass?** `jailops firewall test --from @web --to @db --port 5432` says
  pass or block and which rule decides at each anchor it meets: the policy and the number of its rule, its name and comment, a
  project policy, the managed policy of an app, the isolation of projects, or the default of the anchor, with the rendered pf
  line. An end is a jail of the project, one address or `host`; both hops of a connection between two jails must pass (what the
  source sends is judged on its own port, what reaches the destination leaves the host on the gateway interface of its
  network). It walks the very anchors the renderer loads (`render.BuildAnchor`), not a second copy of the policies, and says
  `unknown`, or a line under "Not covered", where it cannot know (a raw PF fragment, the host's own `pf.conf`, an open
  connection, NAT). Exit status 0 pass, 1 block, 8 unknown; `-o json` for a later console. `POST /api/v1/firewall/test`
  (extension `firewall_test`, operator).

- **Breaking: a jail and a project have several firewall policies.** The jail's `firewall.policy` is `firewall.policies`, a
  list (`jailops config set DB firewall.policies postgres,ssh-office`; `--firewall-policy` is repeatable at `jailops create`),
  and the project's mandatory `firewall` is `firewall_policies`, a list (`jailops project set PROJECT --firewall-policy
  default --firewall-policy monitoring`; an empty value clears it). The anchor of a jail holds the project's policies in
  the order of their list, then the managed policy of an app, then the jail's own in theirs, each under its own heading
  (`# project policy NAME`, `# policy NAME`) with its own rule numbers; the strictest default of them all applies, so a
  jail's policy still cannot turn a project's default deny into allow. A policy that a list names is not deleted and is
  renamed in every list; a jail's list may not name a mandatory policy of the project, a managed policy or one policy
  twice. `jailops jail info` lists the policies in order, and `config set JAIL firewall.policies <Tab>` completes their
  names.

- **`jailops firewall policy create` defaults to deny inbound and allow outbound.** `--default-in` and `--default-out`
  are only given to change that; the policy still has both written in it.

- **Tab completion for `@jail` in an address set.** `jailops firewall address-set create NAME --address @<Tab>` offers the
  jails and the other address sets of the project (not the set being made).

## 0.6.5 - 2026-11-10

- **An installed app starts when the host boots.** `jailops app install` sets `boot.autostart` in its last step
  (`enable-autostart`), so a service comes back after a reboot without a second command, and a failed or interrupted
  installation, which is kept stopped for inspection, never starts again by itself; `jailops config set JAIL boot.autostart false` turns it off for one jail.

- **Boot order: which jails start with the host, in which order, and why one does not.** `jailops boot` shows what the daemon starts after a boot, from the jails as they are now: the jails in the order they start with the reason (`autostart`, or `dependency of NAME`), and the jails with autostart it does not start with why (a dependency cycle, a dependency that does not exist, an encrypted volume that waits for its passphrase, a jail that depends on such a jail). The order is that of the whole host (a jail after its `boot.depends`, then the lower `boot.priority`, then the name); you see the jails of every project you may use, or one with `--project`. `GET /api/v1/boot` (extension `boot_plan`, readonly), `api.BootPlan`, `c.BootPlan(ctx, allProjects)`; the order is computed by the one function the boot pass uses. `jailops list` has an `AUTOSTART` column (`yes` or `-`), and the create examples, `jailops create --help` and the install guide say that a jail without `--autostart` does not start when the host boots, also when it was running before.
- **Breaking: a project is a boundary, and across projects there are only addresses.** `jailops app allow` no longer takes `@PROJECT/JAIL`, and no command writes a rule into a project it was not asked about (the outbound rule in the source jail's own `app-JAIL` policy is gone, with the registry fields and the rename checks that carried it). Another project is treated like an external host: the app's project allows an address or an address set in, and the other project's operators allow the app's addresses out in a policy of their own. Things that talk to each other as one system belong in the same project. The documents, the help, the completion and the console say so.
- **`@jail` means the same everywhere.** A grant `app allow APP from @web` keeps `@web` in its firewall rule (read at every render, as in any rule) instead of a copy of the addresses. `pg_hba.conf` cannot follow, so its lines are still a copy: `jailops app access` and `jailops doctor` report `the addresses of @web are now ... but the pg_hba lines name ...`, and repeating the allow writes them again. A jail that a grant names is not deleted or renamed until the grant is denied, and the refusal says `jailops app deny APP from @web`. A jail whose name is too long for a rule is refused as a source with the address-set way out. The dead "both a jail and an address set" refusal and its comment are gone (names cannot clash).
- **Breaking: a policy always says its defaults.** `default_in` and `default_out` are required on every firewall policy (API, files, `policy create` flags, the console form, the fleet check; the managed `app-JAIL` policy says `deny` and `allow`). A policy that was stored without one is not rendered with a default of ours: the jail keeps its anchor, the error names the policy and the field and says `jailops firewall policy edit NAME`, and `jailops doctor` fails `firewall.defaults` naming each such policy. `jailops jail info` shows `Firewall:` with the policy, or `open: no firewall policy`.
- **Breaking: `includes` of an address set is gone.** A set names another set with a member `@name`, as it names a jail (`--include`, the `includes` field, the console field, the fleet check and the backup bundle follow `@name` members). Deleting or renaming a set protects the sets that name it as a member.
- **A jail's own policy is its own.** `firewall.policy` of a jail may not name a policy that something else manages (`app-JAIL`) or the project's mandatory policy, which is rendered for every jail already; the API, `jailops fleet` and the console refuse it with the reason.
- **One table of endpoints** in `docs/networking-ipv6.md`: an address or prefix, `@name`, `tag:name` (rules only), `host` (`app allow` only). Two rebuilds of an anchor (a jail's DNS change and a rename) left jails out of the sets and failed on a rule that names one; they read the sets with the jails now.
- **Interface ownership is the description, only.** The helper changes a bridge or epair when it carries the JailOps
  description; a name, a prefix or an undescribed interface never makes it JailOps's. **Removed:** `if-prefix` of
  `priv.conf` and `-if-prefix` of `jailops-priv`, the acceptance of undescribed `jo` interfaces, the `jailops:<id>`
  marker, the old socket name of the helper, and the special messages for the removed `listen.unix.*_group` keys and
  the backup node's `admin_group` (an unknown key is an unknown key) and the `private` option of a network, which had no effect (every jail port is a private bridge member).

## 0.6.4 - 2026-11-09

- **A firewall rule can name a jail: `from @web`, and so can an address set.** `@name` is an address set of the project or a
  jail of the project and stands for its addresses; they are read at every render, so a rule follows the jail. One name
  never means both: a jail and an address set of a project may not have the same name, with `-` read as `_` (`web-1` and
  `web_1` are one pf table), and the second one is refused when it is made or renamed. A member of an address set may be
  `@name` too, so a list such as `git_allow` holds jails and is added to later. A name with a hyphen works (`@web-2`); a jail
  whose name is longer than 29 characters needs an address set. A name that is neither is refused when the policy or the set
  is made. A jail that a rule or a set names is not renamed or deleted until the reference is changed (the refusal names the
  policy or the set): a rule that names a jail that is gone would match nobody, and a jail made later under the name would take
  its place. A restored or declared policy that names a missing jail still renders, as an empty set. Renaming an address set
  rewrites the `@name` members of other sets. `jailops firewall policy create db --rule "allow in tcp from @web port 5432"`,
  as `app allow` has `@JAIL`. `jailops fleet` checks the references of a declared repository (`@name` is a set or a jail
  declared for the host).
- **Every part of the firewall has a `comment`, for the review of it.** An address set, each of its entries (`addresses:` is
  a list of `{address, comment}` pairs), a policy and each rule: what it is, why it is there. They are what a reviewer of the
  firewall reads next to `@office` or a port: `address-set show` and `policy show` list them in a table, the rendered anchor of
  a jail carries them as comment lines (`# set NAME: MEMBER COMMENT`, `# rule 1: NAME` and `#   WHY`, `# policy: ...`), and the
  console has a comment field on a set, a policy and every rule, and takes the comment of a member at the end of its line
  (`198.51.100.0/24  # the office`). A comment is one line of printable text of at most 200 characters, without double quotes or backslashes (it becomes a comment line of the ruleset, and a backslash would continue it onto the next line), and is not part of any rule.
  `jailops firewall address-set create office --address 192.0.2.7 --comment "the office"` (`--comment` goes with a single
  `--address`; one entry is one address, so an IPv4 and an IPv6 network are two entries, each with its own comment; several
  commented entries come from a file or `address-set edit`), `jailops firewall policy create web
  --comment "the public web server" --rule "allow in tcp port 80,443 comment the site"` (`comment` ends a rule and takes
  the rest of it). **Breaking:** `addresses` was a list of strings; it is a list of pairs now, in the API, in files and in
  `address-set` YAML (the examples are rewritten).
- **Interfaces of a network that is gone are removed.** A host wiped by hand (the datasets destroyed, the configuration
  deleted) keeps the bridge and the gateway pair of its networks, because interfaces live in the kernel; they stayed until
  a reboot or an `ifconfig destroy`. The reconcile loop now destroys the bridge and the gateway pair that carry JailOps's
  description (`jailops net=NAME ...`) for a network it does not know; an interface with any other description is left
  alone. "Start over" in the install guide also destroys them (it matched only `wg-` and `epair` names, and the gateway
  pair is named after the network).

## 0.6.3 - 2026-11-05

- **The text the package prints is a file of the source** (`packaging/pkg/post-install.sh`): a first install says to run
  `jailops-setup`, an upgrade says `service jailopsd restart`. The recipe of the package reads the file, so a change of the
  text ships with a release and no longer needs an edit in the packages repository.
- **One service: `sysrc jailopsd_enable=YES` and `service jailopsd start`.** The helper has no line of its own any more:
  `jailops_priv_enable` (and the boot-time revert of a pending host change, which follows it) defaults to the value of
  `jailopsd_enable`, and `jailopsd` starts the helper first. `service jailopsd setuptoken` prints the one-time token of
  the first-run setup: the helper behind it is in `/usr/local/libexec`, not in `PATH`, and nobody runs
  it by hand. `jailops-setup` enables and starts only `jailopsd`. The install guide, the README, the man pages and the
  site say so everywhere (no `jailops-priv init` or `service jailops_priv start` as a step).
- **The two services, after a crash and an upgrade**: daemon(8) now supervises `jailops_priv`, so a helper that
  crashes (or is killed) is started again after 5 seconds instead of leaving the host without it; `stop` ends the
  supervisor, `reload` signals the helper itself, and `status` prints the helper's pid or that it is being restarted.
  `service jailopsd start` while the helper is not running starts the helper first, as rc.subr's `force_depend` does for
  the scripts of `/etc/rc.d` (the daemon used to start, wait 30 seconds for the socket and exit), a start while the helper still runs a binary a
  package upgrade replaced warns, and `service jailopsd status` also says whether the helper runs. The helper's rc
  script now requires `syslogd`, so its first audit lines are not lost at boot. `service jailopsd stop` stops the
  helper after the daemon, so `service jailopsd restart` replaces both after a `pkg upgrade` (`service jailops_priv`
  alone still works on the helper only).

## 0.6.2 - 2026-11-05

- **`jailops doctor` checks a `pf.conf` only root can read** (`pf.state-policy`): a `pf.conf` of mode 0600 is common,
  and the daemon (user `jailops`) cannot open it, so the check warned that it could not. The helper (root) now reads
  `/etc/pf.conf` and reports whether it has `set state-policy if-bound`. A `pf.conf` named by `pf_rules` in another
  place the daemon cannot read is shown as INFO, with the command that checks it.
- **`jailops doctor` says when the client's current project does not exist on the server** (`project`): the current
  project is kept in the client configuration (`~/.config/jailops/config.yaml`), so after a host was wiped it named a
  project the server no longer had, and the line read as a permissions problem. It names the project, where the choice
  comes from and how to choose another (`jailops project switch NAME`). "Start over" in the install guide says so.
- **`jailops-setup`: one command after `pkg install jailops`** (`/usr/local/sbin/jailops-setup`, `man jailops-setup`):
  as root it loads the kernel modules, runs `jailops-priv init` (the pool is found when there is one; `-pool` otherwise),
  turns forwarding on now and at boot, puts the three anchor lines into `/etc/pf.conf`, enables and starts
  `jailops_priv` and `jailopsd` with the web console, runs `jailops doctor`, offers to create the first administrator
  and prints the console's address. Each action is printed before it runs, `-n` changes nothing, and a second run
  changes nothing either. In `pf.conf` no existing line is changed or moved: missing lines are inserted where pf wants
  them (`set state-policy if-bound` and a scrub rule only when there are none), the old file is kept with a timestamp,
  the new text is checked with `pfctl -nf` before it is written and loaded with `pfctl -f` (open ssh sessions stay);
  pf is never restarted, and starting it is announced and asked for (`-y` answers yes). The package message named
  `jailops-priv init`, which is not in `PATH` (`/usr/local/libexec`); it now names `jailops-setup`.

## 0.6.1 - 2026-11-05

- **Image builds share one package cache**: the dataset `<pool>/jailops/cache` (mounted at `<mount-base>/cache`, made by
  the first build) holds the downloaded packages (`PKG_CACHEDIR`), so a package of the base is fetched once, not into
  every generation. `pkg clean` removes only the outdated ones.
- **The logs of the jails have a dataset**: `<pool>/jailops/logs` mounted at `/var/log/jailops` (lz4, made by
  `jailops-priv init`; the rc script makes the directory on a host set up by an older release). The files
  `jail_NAME_console.log` moved there from `/var/log` (the old ones stay), and newsyslog rotates them
  (`/usr/local/etc/newsyslog.conf.d/jailops.conf`: 7 files of 1 MB, compressed).
- **A new image has an `/etc/rc.conf` for jails**: `sendmail_enable="NONE"` (no MTA started in every jail) and
  `syslogd_flags="-ss"` (no network socket). The base ships no rc.conf; an image built before has none, change it in
  the jail with `sysrc`.
- **An image build and upgrade no longer leave the downloaded packages in the image**: they stayed in `/var/cache/pkg`
  of every generation (see the shared package cache above).
- **Jails get a nameserver without `--dns`.** A network without `dns` used to give its jails an empty `resolv.conf`
  (`pkg` failed with "Transient resolver failure"). Now the order is: the network's `dns`; else the host's own
  nameservers from `/etc/resolv.conf` (not loopback, not link-local); else, on a host that resolves only through
  loopback (a local unbound), the network's gateway address, with the unbound lines the administrator adds (JailOps
  changes nothing on the host and opens no port). `network show` and the API (`status.dns`) say which applies;
  `network create` prints the lines once; `doctor` (`network.dns`) probes the gateway with one query once a jail on the
  network runs ([networking-ipv6.md](docs/networking-ipv6.md#name-resolution-of-jails)).
- **A change of a network's `dns` reaches the running jails at once**, without a restart: their `resolv.conf` is written
  again and `network edit` names them (`status.dns.updated`, and `not_updated` with the reason). A stopped jail gets it
  at its next start.
- **A jail can keep its own `resolv.conf`**: with the line `# jailops:keep` in it JailOps never writes the file (a jail
  that runs its own resolver).
- **`jailops doctor` warns about a missing `set state-policy if-bound`** (`pf.state-policy`): a loaded ruleset that
  blocks by default, NAT or jails, and a `pf.conf` without the line lose all but the first packet of a jail's flow
  (reproduced on a FreeBSD 15.2 VM: one ping of four, four of four with the line).
- **The FreeBSD patch level of a generation is shown.** A new generation records the FreeBSD version of its packages
  (`15.1-RELEASE-p6`, from `FreeBSD-runtime`); `image show` and `image generations` have a column `FREEBSD`, `jailops
  list` writes `freebsd-15-1 (g1, p6)` (it wrote `freebsd-15-1 (freebsd-15-1#g1)`), the console shows it on the Images
  page and the jail's page. g1 is JailOps's generation counter; a generation built before shows `-`
  ([upgrade.md](docs/upgrade.md)).
- **Values the host would refuse later are refused when they are given.** A network, a jail or a project was accepted
  and then failed at the first jail start or inside the create operation; each of these is now refused at create or
  update with the reason and a hint:
  - Sizes are stored as bytes. `4GiB`, `512m` or `1k` as a project's storage quota was written verbatim to ZFS and
    refused by the helper at the first jail create, and `--disk 4GiB` failed inside the create ("bad disk quota");
    the quota was also set again on every pass because bytes were compared with the text. Sizes above 16T (the
    helper's limit) are refused.
  - An attachment MTU (`nic.<device>.mtu`) other than its network's: a bridge gives every port its own MTU (on
    FreeBSD 15.2 the jail's end kept the other MTU and large frames were lost).
  - A network whose subnet overlaps an address of the host (its uplink's LAN, for example), naming the interface; a
    routed IPv6 prefix likewise; a proxy-mode subnet outside the prefix of the uplink's segment.
  - An uplink that does not exist (`--uplink em9` rendered a NAT rule on nothing and the network looked ready), or is
    a loopback, a bridge, a jail interface or a bridge member; the hint lists the host's candidates.
  - A snapshot name of more than 64 characters, with upper case letters or starting with `jo-` (the API took up to
    128 characters, the operation 101). One rule: `^[a-z0-9][a-z0-9._-]{0,63}$`.
  - A WireGuard name of more than 12 characters without `--interface` now says so, instead of naming the interface
    field the caller never set.
  - Names and datasets too long for what is derived from them (ZFS names have at most 255 characters, a PF anchor
    path that `pfctl -a` loads at most 63): `project create --new-network` derives the network and bridge from the
    project name.
- **Name limits for new objects** ([design.md](docs/design.md#31-names-and-their-limits)): project 13 characters, jail,
  volume and image 32, snapshot 64, a storage dataset 88. Existing objects with longer names keep working;
  `jailops doctor` lists them (`names.length`). The console shows the limits on the name fields.
- `network create` warns when IPv4 or IPv6 forwarding is off: the network stays not ready (Precondition) until
  `sysctl net.inet.ip.forwarding=1` (`sysrc gateway_enable=YES`).
- `project create --new-network` skips subnets of the pool the host already has on an interface.
- `project show` and `jail info` show sizes stored as bytes in their unit (`quota 4G`); `config set` takes the sizes
  the flags take (`512m`, `4GiB`).

- **The default prompt has a space after it** (`root@test:/# ` and not `root@test:/#`): what is typed no longer touches
  the prompt. A jail that already has the old prompt file gets the new one with `jailops jail shell-setup NAME`.
- **`jailops doctor` does not call a feature missing that the server was not given.** A host without a backup node, a
  sealed-secrets identity or the host modules lacks their API extensions on purpose; the check said "the server lacks:
  backup_pull, backup_push, ..." on every such host. Only extensions every server of the release has are reported.
- **The default base of an image is the minimal jail set plus the optional jail set.** The minimal set alone (35 packages)
  left out what software from the FreeBSD ports expects of a base system: PostgreSQL stopped with `libexecinfo.so.1 not
  found`, and `sysrc` was "command not found" in a new jail. `FreeBSD-set-optional-jail` (about 105 packages, 160 MiB:
  libexecinfo, sysrc, ssh, ee, ntp, ftp, telnet, bmake and the rest of what a FreeBSD jail normally has) is now part of
  the default; the development set (compiler and headers, 570 MiB) is not, and an image that builds software names
  `FreeBSD-set-devel` in `source.sets`. An image with its own `source.sets` is unchanged; an image with none gets the
  optional set in its next generation (`jailops image upgrade IMAGE`).
- **The default storage mount is `/usr/local/jailops`** (it was `/jailops`, a new directory directly under `/`). The FreeBSD
  Handbook keeps jails under `/usr/local/jails` and `hier(7)` advises against new top-level directories; the dataset stays
  `<pool>/jailops`. Only the default of `jailops-priv init -mount`, `mount_base` of `jailopsd.yaml` and `mount-base` of
  `priv.conf` changed: an installed host keeps the value in its configuration and works as before. There is no
  in-place move yet (the datasets of images and jails carry their own absolute mountpoints); a host that has nothing
  to lose is best set up again with `jailops-priv init` on the new default.
- `web/*/node_modules` is ignored (a few cache files of it had been committed).

- **A bridge may have any free name, and a network's bridge is named like the network.** `jailops network create
  office --ipv4 10.66.0.1/24 --uplink vmx0 --nat` makes the bridge `office` (and its gateway pair `officega`,
  `officegb`). Without `--bridge` the bridge takes the network's name when it can be one (lowercase letters and
  digits, at most 13 characters, not a driver's form, free on the host and among the networks), else `jo<N>`, and
  `network create` says why in a warning. A `--bridge` that is invalid, too long, a driver's form (`vmx0`, `bridge0`,
  `lo0`) or taken (an interface of the host, or another network's bridge or gateway pair) is refused when the network is
  created, no longer when its first jail starts (that was `forbidden: interface "office" is not managed by JailOps
  (prefix jo)`). The bridge is stored in the spec and keeps its name until the network is deleted, also across a rename
  of the network; `network edit` still refuses to change it. New doctor check `network.bridges` explains an interface
  with a network's name that is not JailOps's.
- **The helper's ownership rule for interfaces changed** ([security-model.md](docs/security-model.md#interfaces)). It
  used to change any interface whose name started with `jo`; it now changes only a bridge or an epair whose description,
  read by the helper from the kernel in the same operation, carries JailOps's marker (`jailops ...`). The helper writes
  the marker when it creates an interface, before the interface has its name, and the rename that gives it the name is
  the kernel's atomic check that the name is free. What stays refused, and is now refused regardless of the name: any
  change to the uplink, `lo0`, a tunnel, a vlan or any interface that is not a bridge or an epair; any change to a bridge
  or epair without the marker (made by hand); writing the marker on an interface that exists, or a description that
  removes it (clearing writes the bare marker `jailops managed`); taking over an interface by its name; creating one
  under a driver's form, loaded or not. Newly refused: the command forms `ifconfig bridge create`, `ifconfig epair
  create` and `ifconfig bridgeN name X` (nothing used them; with them the daemon could rename a hand-made `bridge0` to a
  `jo` name), and an `ifconfig -j`/`route -j` form on an interface of a jail's vnet without the marker. Reading stays
  open.
- **Upgrade:** nothing to do. Interfaces of earlier releases carry their description already. A bridge or epair whose
  name starts with the legacy prefix and that has no description at all (an older build, or a crash between creation and
  description) is accepted once and marked. `if-prefix` in `priv.conf` is deprecated and now only names that legacy
  prefix (default `jo`); `jailops-priv check-config` says so. Existing networks keep their `jo<N>` bridges.
- **Renames.** Networks, address sets, firewall policies, profiles, jails, images and projects can be renamed:
  `jailops network rename lan office`, `jailops firewall address-set|policy rename`, `jailops profile rename`, `jailops
  image rename`, `jailops project rename`, and `jailops jail rename OLD NEW` or `jailops rename OLD NEW` for a stopped jail
  (`POST /api/v1/<kind>/{name}/rename`, extension `rename`, design 8.8). Everything that refers to the object changes in
  the same operation or nothing does: project grants and image lists, jail specs and records, policy rules and set
  includes, profile keys, tunnels, token and certificate scopes, datasets with their mountpoints and properties, PF
  anchors, interface descriptions (a network's interfaces keep their names, its running jails their traffic), update
  history and nightly upgrade runs. The command shows what changes and asks (`-y`, `--dry-run`); `jailops <kind> edit`
  with a new `metadata.name` renames the same way. A running jail, a managed object and app access grants are refused
  with the way out. The console has Rename on the page of each of these objects. An update that changes a name answers
  `invalid` and names the rename.
- **Address sets are created and deleted in the console.** The Address sets tab of the Firewall page has **Add set**
  (name, members, includes, checked as the server checks them), **Delete set** (lists the policies and sets that use it
  and shows the server's refusal) and an empty state; Save is labelled **Save set**.
- Fixed: the Address sets tab could render nothing, or show its list and lose it again, and saving a firewall policy
  failed: the editors copied a reactive object with `structuredClone`, which throws.
- Deleting an address set in use names every policy (or set) that uses it, with a hint and `detail.policies` /
  `detail.sets`.
- Demo: address sets can be created and deleted; a policy in use or mandatory is no longer deleted (the server refuses
  both); a rule's `to` is checked for unknown sets as `from` is.

## 0.6.0 - 2026-11-03

- **New jails come with bash and a coloured prompt** ([shell.md](docs/shell.md)). A new FreeBSD jail gets bash, installed
  from the host's package repository at its first start (`pkg -r ROOT install -y bash`, as an app installation does), as
  the shell of `jailops shell` and the console, and a light blue `user@host:dir#` prompt for every user of the jail, so a
  shell in a jail is recognisable at a glance. The prompt is one file JailOps owns,
  `/usr/local/etc/profile.d/jailops-prompt.sh`, sourced from a marked block at the top of `/usr/local/etc/profile` and
  `/usr/local/etc/bash.bashrc` (the files the FreeBSD bash package reads); all of it lives in the persistent
  `/usr/local` and survives OS updates, and a user's own `~/.bashrc` wins. The keys: `shell` (now also the names `bash`
  and `sh`) and the new `prompt` (`default`, `none` or a PS1 text; written at once into a running jail), `jailops create
  --shell sh` and `--prompt`, the Shell field of the console's jail form, and `jails.shell`, `jails.prompt`,
  `jails.prompt_color` in `jailopsd.yaml` for the defaults. A prompt is checked by an allow-list: nothing in it can run a
  command (no quotes, no backquotes, `$` only as `\$`, only bash's prompt escapes, no `\\` and no octal escapes but
  `\033` and `\007`). Existing jails are not changed: `jailops jail shell-setup NAME` (and **Install bash and the
  prompt** on the console's Console tab) gives one what a new jail gets. A setup that fails (no package repository) does
  not fail the start: it is the jail's `ShellSetup` condition. Linux jails are left alone. The host's green prompt is a
  paste in the same document; JailOps does not edit the host's shell files.
- **A shell session finds what packages installed.** `jailops shell` and the console start the shell with
  `PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin` and `HOME=/root` (before, `/usr/local/bin` was
  missing and HOME unset), and from a terminal `jailops shell` and `jailops exec -t` pass `TERM`, so bash edits lines
  instead of assuming a dumb terminal. The console's terminal draws the 256 colours.
- **Get to a jail and into its shell fast (host console).**
  - **Quick switcher**: `Ctrl+K` (`Cmd+K`), `/` where no text is typed, or the Search button in the side bar (top bar on
    a phone). It finds jails by name, image, address, label (`env=prod`, `team:`, `-tier=edge`) and app, the app catalog,
    images, networks, projects, storage and the console's pages, ranked whole name, prefix, part, anywhere, then letters
    in order; empty, it shows the recent items. **Every project** searches all the projects the identity may use. A jail
    row has its quick actions: Shell (`Shift+Enter`), Logs (`Alt+L`), Start or Stop (`Alt+S`, stop asks first). Keyboard
    and screen reader friendly (dialog, combobox, grouped listbox, results announced), capped per group with "refine your
    search", in memory so thousands of jails stay fast.
  - **Labels you can see and change**: chips in the jail list, on the jail page and in the installed apps; a click
    filters the list. The list's filter reads `key=value`, `key:`, `-key=value` and words, suggests the keys and values the
    jails carry, lives in the address (`/jails?q=env%3Dprod`, shareable, the back button undoes a chip) and is remembered
    per user and project. **Edit labels** on the jail page, **Labels** for the selected jails (set or remove one) and a
    labels field in **Create jail**; each change is a merge patch of `metadata.labels` with the existing `PATCH
    /api/v1/jails/{name}` (admin in the project). The console checks the server's label rules, on the same test cases
    (`sdk/api/testdata/labels.json`, new `TestLabelRulesFixture`). Keys starting with `jailops.` are shown, not edited.
    New page [docs/labels.md](docs/labels.md).
  - **Shell in one click**: a Shell button on every running jail of the list and on the jail page; `/jails/NAME/shell`
    opens the Console tab connected (shareable). A stopped jail offers **Start and open shell** (start, then connect when
    it runs). The Console tab says which shell runs as whom and shows the same shell's command line (`jailops shell NAME
    --project P`) with a copy button. The tab and the buttons are offered only with the `jail_console` extension to an
    operator or admin of the project, the right `jailops exec` needs; nothing changed on the server. A jail page opens on
    the tab used last for that jail.
  - Fixes on the way: a jail page reached from another jail's page (the switcher) shows the new jail, not the old one; on a
    phone the select box of a jail card takes the tap; the terminal's 16 colours are readable on its dark background.

- **An image is one release.** Images are named after their FreeBSD release, `freebsd-15-1` for 15.1-RELEASE; their
  generations are the release's patch levels, made with `jailops image upgrade freebsd-15-1`. A new release is a new
  image (`jailops image create freebsd-15-2 --release 15.2-RELEASE`), created after the host's upgrade; jails stay on
  the image of their release, and moving a jail's data to a jail of a new release is not automated
  ([upgrade.md](docs/upgrade.md#an-image-is-one-release)). The console, the wizard, the hints and the documentation no
  longer propose an alias: jails and apps name the image (`--image freebsd-15-1`, its newest generation). Aliases still
  work for whoever sets them.
- **Nightly auto upgrade per image.** `jailops image auto-upgrade freebsd-15-1 on [--at 03:00] [--no-packages]` (`off`
  stops it), the checkbox **Upgrade automatically every night** on the Images page and in Create image (off until it is ticked:
  nothing is upgraded unless the owner asked), and `spec.auto_upgrade` of `PATCH /api/v1/images/{name}`: every night at the chosen time (the
  host's time zone) the image gets its next generation, its thin jails move to it one at a time with the health check and
  the automatic rollback (the run stops at the first jail that fails), and with packages on the packages inside the jails
  are upgraded. Jails whose update strategy is manual or in place are left alone; a host that was off at that time runs it
  within four hours of coming back. `status.auto_upgrade` holds the next run and the latest runs (ok, unchanged or
  failed, with the jails moved, upgraded and failed), shown by `jailops image show` and on the image's card in the
  console, where a failed run stays as a notice.
- **Package updates of a FreeBSD jail run from the host.** A thin jail is a pkgbase userland without the `pkg` tool, so
  `pkg update` inside it answered "The package management tool is not yet installed": `jailops update check` and
  `update apply` (and the schedules of managed jails) could not upgrade the packages of such a jail. The host now runs
  `pkg -r <jail root>` for the catalog, the dry run and the upgrade, as the app installer does, and refreshes the
  library cache of the jail afterwards. Linux jails still run `apk` inside themselves.
- **Images of a -STABLE, PRERELEASE or -CURRENT release** (`base_latest`) are verified with the key of the ports
  repositories (`/usr/share/keys/pkg`); the release keys (`pkgbase-<major>`) still verify a -RELEASE. Before, building an
  image for 15.2-PRERELEASE failed with "No trusted public keys found". Checked with the whole end-to-end flow on
  FreeBSD 15.2-PRERELEASE.
- The event `image-upgrade-failed` is raised when a nightly run fails.
- **A short command run with a terminal keeps its output.** `jailops exec -t JAIL -- echo hi` printed nothing in 3 to 30%
  of the runs on FreeBSD: the kernel hangs a terminal up when its session leader exits and drops the output not yet read.
  A terminal session now runs under a small leader process of the helper that stays until the output was read, with the
  command in its own foreground process group (signals, resizing and job control work as before). Checked on FreeBSD
  15.2-PRERELEASE: none of 1,500 runs lost output.
- The snapshot ids of the backup repository carry 12 random bits after the microsecond instead of 8 (two jobs of one
  jail that started in the same microsecond collided in a test with a frozen clock; the ZFS snapshot names of a backup
  carry eight random hex digits instead of four).
- **Nothing is upgraded unless it was asked for, and a Debian jail is never touched.** The nightly upgrade is off until
  it is turned on (also in Create image and in the first-run wizard: the box is unticked). `jailops update check` and
  `apply`, the managed schedules and the nightly run drive `pkg` (FreeBSD jails) and `apk` (Alpine jails) only; a Debian
  jail, or any other Linux jail, keeps its own packages (for instance with unattended-upgrades) and JailOps never runs
  `apt` in it.
- **An image upgrade moves the jails that are behind even when it built nothing.** A run that stopped at a failing jail
  left the jails after it on the old generation, and the next run, finding no new packages, skipped them as "the
  package set did not change". They now move to the newest generation (`jailops image upgrade` says so).
- **Package updates upgrade the packages the host names, never the base system.** `pkg -r <root> upgrade` with no names
  would also pull `FreeBSD-*` base packages from the host's FreeBSD-base repository into a jail's root, behind the
  image generation; the base only moves with the generation. Verified on FreeBSD 15.2-PRERELEASE: the nightly run
  moved two jails that were behind and upgraded an outdated package.
- **The console points at a new release.** When the host runs a newer release than its newest FreeBSD image (the host is
  on 15.2, the image is `freebsd-15-1`), the Images page and the Overview say so and open Create image filled in for it.
- **The default image of an app install** is the FreeBSD image of the highest release the host's kernel can run (of two of
  the same release, the first by name), instead of the only image with the alias `stable`. The install dialog and the
  jail form list the images with their release (`freebsd-15-1 (15.1-RELEASE)`).
- **The console creates the base image.** The Images page has **Create image** (FreeBSD, and the Alpine and Debian
  images the API creates): the release defaults to the host's own and nothing older than 15.1 is accepted, the
  architecture is the host's, the name is proposed from the release (`freebsd-15-1` for 15.1-RELEASE, and it follows the
  release until you edit it), and "Build the first generation now" (on by default)
  builds it right after the definition, followed in the dialog as an operation with its progress, a Cancel button and,
  when the build fails, what to check and Try again (the definition is kept and not created twice). An empty Images page
  explains what an image is and offers it first. The first-run wizard has a new step **Base image** after Storage and
  network: it creates and builds `freebsd-15-1` (the host's release, the host's architecture) (or says that
  a FreeBSD image exists), shows the build in the step, and can be skipped; the Done step leads to **Install your first
  app** when an image exists and to **Create the base image** otherwise. The app install dialog and the jail form say in
  place that there is no image and offer the same dialog, instead of a refusal from the server. The existing routes do
  the work (`POST /api/v1/images`, `POST /api/v1/images/{name}/generations`); the build itself is unchanged.
- **The server refuses a FreeBSD image older than 15.1** (the console already did): `image create` and `POST /api/v1/images`
  answer `invalid` for `15.0-RELEASE`, `14.x` or a release the server cannot read, the same floor `jailops doctor` checks for the host.
- `GET /api/v1` reports the host's release as `environment.kernel_version` (`kern.osrelease`; the field existed and was
  always empty). `jailops server info` shows it.
- **Hints for a missing image.** `jailops app install` without any FreeBSD image, and `jailops create` (or the API) with
  an image that does not exist, answer with a hint that names the two commands that make it:
  `jailops image create freebsd-15-1 --release 15.1-RELEASE`, then `jailops image build freebsd-15-1` (the host's
  release; 15.1-RELEASE when the server cannot read it). The empty `jailops image list` names the host's release too
  (it always said 15.1-RELEASE). Before, the hints said "create or import the image first" and
  "name an image with --image".
- `jailops delete NAME --dry-run` on a running jail prints what would be destroyed and that the delete needs `--force` to
  stop it first; before, the dry run failed with `conflict: jail is running` unless `--force` was given too.
- The help of the configuration keys `packages`, `nic.<device>.ingress` and `nic.<device>.egress` says in plain words
  what is not done yet ("Not enforced yet: the limit is stored and reported only, ..."; "Cannot be set when a jail is
  created yet: ..."), in `jailops config keys` and in the console.

## 0.5.1 - 2026-11-02

- **The privileged helper no longer crashes on frames out of order.** A data frame after the end of a request's stream
  (while the request still ran) made `jailops-priv` panic with "send on closed channel" in its connection loop, outside
  the recovery around operations: one bad peer of the helper's socket stopped all jail management on the host. The
  helper now keeps the stream's state with the request and closes only that connection, with a
  `priv.protocol.violation` line in its log (also for an unknown frame kind, data beyond the window, too many acks or
  control frames, and descriptors from a client). Tests send `req`, `end`, `data` over a real socket and random frame
  sequences over a socketpair, and check that a new connection still works.
- **`GET /readyz`**, readiness for load balancers and monitors: `ok` (200) once start-up finished, the helper answers a
  read-only call and the database answers a query; `not ready: CLASS` (503) otherwise, with a fixed class and no
  detail. Public, cached for 2 seconds, not forwarded with `?host=`, left out of the access log. `GET /healthz` is
  unchanged and documented as liveness ([exposure.md](docs/exposure.md#load-balancers-and-monitors)).
- [docs/operations.md](docs/operations.md): runbooks (lost admin access, a stolen token or certificate, a compromised
  daemon and what to rotate, a lost backup key, restoring onto a new host, a firewall lock-out, going back one version),
  recovery objectives measured with a restore drill, and what to monitor, with what is not exposed yet (no metrics
  endpoint).

- **Security fixes from an external audit (November 2026)**, reported by [@samarason](https://github.com/samarason) (twelve findings, each checked against the code; the ones that held are fixed here) ([docs/security-model.md](docs/security-model.md)). **Upgrade:** stop `jailopsd` and `jailops_priv` before `pkg upgrade`, then start the helper first ([upgrade.md](docs/upgrade.md#jailops)).
  - Storage roots: the daemon could mount a dataset of its own on a storage root's directory (`mountpoint` equal to the root's), which replaced its mode 0700 with 0755 and put the jails' setuid programs in reach of its user; a root registered at run time kept the mode it was made with. The helper now refuses a `mountpoint` equal to a root's directory (create, set, clone, receive, encrypted volumes), never unmounts, remounts or moves a root nor changes its `canmount` or `devices`, and gives every root's directory to root with mode 0700 at start, reload and registration (existing roots are closed at the first start). `devices=off` is set on every root and cannot be turned on; a received stream can no longer set `devices`, `setuid` or `exec` (they are inherited; `setuid` stays on in jail roots). A file agent of a restore makes device nodes and setuid or setgid files only in a jail's root or a volume mounted in it; elsewhere it makes no device node and drops the bits, listed as skipped. The rules are one table test, `internal/priv/invariants_test.go`.
  - A storage root's mountpoint may not lie inside a system directory (`/etc`, `/boot`, `/root`, `/usr/local/etc`, `/var/db`, `/var/run`, ...) nor overlap a directory of the helper (`conf-dir`, `host-dir`, `wireguard-dir`, the storage file's directory) in either direction; before, only a root on or above them was refused. `/usr/jails` and the like stay fine.
  - The helper's socket moved to `/var/run/jailops-priv/priv.sock`, a directory of root (mode 0710, group `jailops`): in `/var/run/jailops`, which the daemon writes, its user could put a file or a socket at `priv.sock` and keep the helper from starting. `priv.conf` and `jailopsd.yaml` that name the old path keep working (read as the new one); the rc script makes the directory.
  - Backup node: the first backup session binds an unbound repository to the host's key domain, as the first escrow did. An escrow upload must be an age file. New `jailops-backupd escrow list | escrow delete DOMAIN` and `quarantine add CHUNK` (management API `GET /api/v1/escrow`, `DELETE /api/v1/escrow/{domain}`, `POST /api/v1/quarantine/{id}`, backup-admin, audited) remove a wrong escrow blob or a chunk a host found undecryptable, so it is uploaded again. Documented: hosts that share a node share its key and trust each other ([backup-restore.md](docs/backup-restore.md#who-trusts-whom-hosts-that-share-a-node)).
  - Log forwarding: a sink list set through the API may name its `ca_file` and `token_file` only as plain files directly in the new `logging.secrets_dir` (default `/usr/local/etc/jailops/sinks`, root-owned 0750 `root:jailops`, made by `jailops-priv init`), checked when the list is stored and at every read; before, any root-owned file the daemon's group could read could have its first line sent as a bearer token to a receiver of the caller's choosing. Such a list may not send to loopback, link-local (`169.254.0.0/16`, `fe80::/10`), unspecified, multicast or broadcast addresses or to the daemon's own ports on an address of the host, checked on the resolved address when the socket connects and for an address or `localhost` when the list is stored. Every CA and token file is opened from `/` without following a symbolic link at any component and checked on the open descriptor. The forward test answers a short class of the failure (`connection refused`, `timeout`, `HTTP 503`, `target refused: loopback`, ...), never the receiver's answer. Sinks of `jailopsd.yaml` keep free paths and may name the loopback address; a stored list from before naming files elsewhere stops delivering until it is changed or reset.
  - `Strict-Transport-Security` only while the served certificate comes from ACME or was uploaded or configured and verifies against the host's roots; before, it was sent with the self-signed certificate too, which a browser that kept it would then refuse without a way to click through. Both consoles send a `Permissions-Policy` that turns off camera, microphone, location and the other device features; the backup node sends no `Strict-Transport-Security` (its certificate is self-signed and pinned).
  - `jailops-priv setup-token` prints only a token of the expected shape (52 base32 characters), only console addresses of the form `https://HOST:PORT/` in printable ASCII, and the hint with control, bidirectional and zero-width characters replaced: what the daemon answers reaches root's terminal and the system console.
  - Tests for what the audit found already closed: a token in the query string authenticates no exec upgrade or console route, and at most 16 file agents run at once (one more answers `busy`).
  - CI: every apt mirror of the web end-to-end job is an `https` URL. [ci-supply-chain.md](docs/ci-supply-chain.md) lists the third-party actions and how to pin them to commits.

- **Security fixes from a review of the API, the console and the CLI** ([docs/security-model.md](docs/security-model.md)).
  - Logins: every attempt is counted before its password is checked (a burst from one source ran as many 64 MiB argon2id checks at once as it sent requests), at most four checks run at once for the daemon (32 waiting, then `rate_limited` at once), a locked-out source costs no check, and an IPv6 source is its /64 for every limiter. Guesses for one account from many sources wait 1 s, 2 s ... up to 30 s each after 10 failures (slowed, never locked).
  - Audit log: one function escapes (control, bidirectional and zero-width characters, invalid UTF-8) and cuts every entry (object 256 bytes, detail 4096, 256 for an anonymous caller); a failed login no longer records the typed user name; refusals of a locked-out source are one entry per minute with a count. **New configuration** `audit.retention` (default `90d`) and `audit.max_entries` (default 1000000), pruned daily; the newest 1000 entries are never deleted (a database trigger replaces the one that refused every delete; migration 0003). Durations in `jailopsd.yaml` accept whole days (`90d`).
  - Every API answer is `Cache-Control: no-store` (the check compared four bytes of the path with a seven-byte string and never matched).
  - Sessions: only a global admin sees and ends other users' sessions (a global reader could). Event streams, console output and exec sessions end at the next keepalive when their token, certificate, user or session is revoked, disabled or changed; at most 16 event streams per identity (`rate_limited`). Console sessions belong to the kind and name of the credential and to their project; their ids are no longer in the request log or audit objects.
  - **API strictness**: request bodies refuse unknown members, members in other cases, duplicates and data after the value (`invalid`, naming the member); a misspelled `dry_run` ran the change for real. The `status` of an object sent back is ignored; maps and merge patches are unchanged. A client that sends a field this version does not know (a newer client through a hub to an older host) now gets `invalid`.
  - Labels: keys of letters, digits, `-`, `_`, `.` (up to 63, with an optional prefix and `/`), values up to 253 bytes of printable text, at most 64 per object; descriptions and comments refuse control and invisible characters and are at most 4096 bytes (`invalid`). Stored labels are checked when they change.
  - **Anonymous `GET /api/v1`** answers only `api_version`, `api_extensions`, `role` and `auth`; the host name, host UUID, software version, environment and secrets recipient need an identity. `doctor` shows the text of failing checks to global admins only. `?access_token=` on `/api/v1/events` is no longer accepted (nothing used it). `POST /api/v1/auth/totp` (start) and `POST /api/v1/auth/recovery-codes` need `{"password": ...}`.
  - `jailops`: every string a server sends is cleaned before the terminal sees it (tables, show, errors and hints, audit, doctor, events, labels); `remote add` prints the fingerprint first and quotes the certificate's subject and issuer; `exec` and `shell` end and restore the terminal on SIGINT, SIGTERM and SIGHUP. `wireguard join` takes `--token-file` and warns about a token given as an argument.
  - Web console: the console embedded in `jailopsd` ignores `?demo` (demo mode is a build-time flag, `JAILOPS_DEMO_BUILD=1`, which the end-to-end tests use) and shows a demo banner on the login page of a demo build; an API token is forgotten on a 401.
  - Regression tests adopted from the review: a cross-project leak detector over every GET route, the event stream and every mutating route, and a check that token, session and client certificate decide every route alike.

- **Security fixes from a review of the appliance release.**
  - The helper's file operations (`fs.read_file`, `fs.write_file`, `fs.mkdir`, `fs.remove`, `fs.remove_all`, `fs.copy_tree`, `fs.seed_keys`) open a path below a jail's tree (its `root`, `root.new`, `root.old`, `root.stale`, a volume `data/<v>`, or an image generation) at that tree, reached from the storage mount one plain directory at a time with a check of device and inode, instead of at the whole storage mount. A relative symlink made inside a jail no longer leads into another jail or project of the same storage root (it did: `app allow` could read and write another jail's `pg_hba.conf`, an app draft could read another jail's `rc.conf`); one that stays inside is followed as before, and an absolute symlink is refused, as before. The path arguments of allow-listed commands below a jail's tree must resolve inside it too.
  - Pid files: daemon(8) writes them as root, so they moved out of `/var/run/jailops` (writable by the group `jailops`) into `/var/run`: `jailops-priv.pid`, `jailops-priv-daemon.pid`, `jailopsd.pid`, `jailopsd-daemon.pid`, and for the backup node `jailops-backupd.pid` and `jailops-backupd-daemon.pid` (out of the node user's directory). The sockets stay where they are. The helper's own `-pidfile` no longer writes through a symlink. **Upgrading**: stop `jailopsd` and `jailops_priv` (and `jailops_backupd`) before installing this version, since the new scripts look for the pid files in the new place.
  - WireGuard routes are checked by the helper: inside the allowed IPs of a peer of the tunnel, at least a /8 (IPv4) or /16 (IPv6), clear of loopback, link-local, multicast, the host's own addresses, its default gateways and its resolvers. Before, only `/0` was refused, so a tunnel could take the host's traffic with `0.0.0.0/1` and `128.0.0.0/1` although `admin-routes` was off. New `priv.conf` key `wireguard-routes` lists networks allowed anyway; `admin-routes` lifts the checks except the one of the allowed IPs. A full-tunnel client now needs one of the two.
  - A host restore (and a revert of a pending change, also at boot) puts back only what the module owns: whole `resolv.conf` and `pf.conf`, the jailops block of `ntp.conf`, and the module's own `rc.conf` variables, set or removed with `sysrc -x`. Snapshots no longer copy `rc.conf`; they record those variables. Before, the whole `rc.conf` and `ntp.conf` went back and undid what root had changed since; the restore diff showed lines of `rc.conf` the module does not own. A snapshot of 0.5.0 gives only the module's variables from its copy.
  - First-run setup: a global admin API token or client certificate that has not expired closes the setup like a user (before, only users counted, so every start of `jailopsd` printed a usable setup token on a host administered by tokens). A setup token left over is removed as soon as an administrator exists. Deleting or demoting the last global administrator (user, token or certificate) is refused with `conflict` while no other one exists; root on the socket stays the recovery access.
  - Log reads open `var/log` of a jail one directory at a time and compare it with what was checked, so swapping it for a symlink between the check and the open cannot point a read at the rest of the jail's root.
  - The helper limits wrong passphrases of encrypted volumes itself (five per volume, twenty for all, then a wait from 30 seconds doubling to an hour, `rate_limited`), not only the daemon.
  - Audit details quote and cut (200 bytes) the values a client sent (the source, ports and database of `app allow` and `deny`, log sink names, ACME domains), also for refused requests.
  - A failed `app allow` after psql made the role keeps the role in the registry (marked unfinished), so the next allow sets a new password and finishes instead of refusing a role "not made by JailOps".
  - The generated self-signed key is written atomically with mode 0600 (no write through a symlink); `jailops_pool` and `-pool` refuse a name that starts with `-`; the reference catalog's mail app starts Dovecot with `disable_plaintext_auth = yes` (no password in clear before TLS) and its notes say so.
  - Documented: what a global admin can point log sinks and the ACME directory at ([security-model.md](docs/security-model.md#log-forwarding)), and that volume ownership rests on the `jailops:managed` and `jailops:kind` user properties ([storage.md](docs/storage.md#encrypted-volumes)).

- **The appliance path: one rc.conf switch, a first start without a shell** ([docs/install.md](docs/install.md#appliance-mode-host-managed-by-jailops-set-up-from-the-web-console), [docs/appliance.md](docs/appliance.md)).
  - `jailops_appliance="YES"` in `/etc/rc.conf` (with `jailops_appliance_modules`, default all modules, and `jailops_pool`) turns host management on in both programs: the rc script of `jailops_priv` passes `-appliance-modules` to the helper, which manages `host-manage` of `priv.conf` and these together (read at start only; not a key of `priv.conf`; `check-config` prints both lists), and the rc script of `jailopsd` passes the same list to the daemon (`host.manage.modules` and these). The helper's list still decides: the daemon cannot switch itself on. Doctor check `appliance` (ok when both agree, a warning with the line to fix otherwise).
  - First start: without `priv.conf`, the rc script runs the new `jailops-priv appliance-init`, the setup of `init` on `jailops_pool` or the pool of the root file system, refusing a pool that holds another JailOps dataset and logging every command to syslog. `jailops-priv revert-pending` does nothing before the helper is set up.
  - **Setup token** (extension `setup`): `POST /api/v1/setup/token` (root on the socket) issues a one-time token (32 random bytes, base32, 24 hours, stored as its SHA-256 in `<state_dir>/setup/token.json`), `GET /api/v1/setup` tells anyone that an administrator is missing, `POST /api/v1/setup` with the token creates the first global administrator and uses the token up; wrong tokens are locked out per source like failed logins; every setup route answers `not_found` once an administrator exists. `jailops-priv setup-token [-wait D] [-syslog]` asks the daemon for one and prints it with the console's addresses (exit 3 once set up); the rc script of `jailopsd` runs it after every start in appliance mode and writes it to `/dev/console` and syslog `authpriv` (`/var/log/auth.log`), never to the daemon's own log. `jailopsd` now requires `syslogd` in its rc order. SDK `SetupStatus`, `Setup`, `SetupToken`.
  - The console's **first-run wizard** (`/setup`, shown instead of the login while no administrator exists): the account with the token, the host name, resolvers and NTP, the certificate, the host firewall (ssh only from the browser's address, commit confirmed), storage, a managed network and a project when none exist, and a link to the app catalog; every step but the account can be skipped. Demo `?demo=setup`.
- **The console's certificate: upload, and ACME** ([docs/exposure.md](docs/exposure.md)) (extension `host_certificate`, kind `HostCertificate`).
  - `GET /api/v1/host/certificate` (the served certificate: subject, names, issuer, validity, fingerprint, whether a public CA signed it, the ACME state, the redirect, the `jailops remote add` command, without a fingerprint for a public certificate), `PUT` uploads a PEM chain and key (checked: the chain parses and is in order, the key matches and is not encrypted, valid now, for server authentication and for the host name given; written to `<data_dir>/tls/uploaded.{crt,key}`, 0600, atomically), `POST .../acme`, `POST .../self-signed`. The choice is kept in `<data_dir>/tls/choice.json`; `listen.https.cert`/`key` of the file still win and cannot be changed from the API. Trust tokens pin the certificate served when they are issued. SDK `c.HostSystem().Certificate()`.
  - **ACME** with `golang.org/x/crypto/acme` (already a dependency): `listen.https.acme` (`enabled`, `email`, `domains`, `directory_url` with the production directory by default, `challenge` `http-01` or `tls-alpn-01`, `accept_tos`, which must be true, `challenge_address`, default `:39080`), or the console. The daemon answers http-01 on its challenge listener (plain http, challenge tokens only) and tls-alpn-01 on its TLS listener; the host redirects port 80 or 443 to it: the host firewall module renders the `rdr` and `pass` rules from two validated values (`acme` of the firewall spec, filled in by the daemon), otherwise the console prints the two `rdr pass` lines. Daily check with jitter, renewal 30 days before expiry, backoff of 1 h doubling to a day after failures, the new certificate served from the next handshake on; keys and certificate in `<data_dir>/tls/acme` (0700/0600). Doctor `tls.certificate` and `tls.acme` (days left, the last error, the redirect, a self-check of the challenge listener). No dns-01, so no wildcard certificates. Tested against an ACME CA written in the tests; UNVERIFIED with a real CA (the staging check is in docs/exposure.md).
  - Console: a Certificates panel on the Host page (the current certificate, Upload, Request with ACME with the CA's terms, Use self-signed again with a warning) and the right `jailops remote add` command in Access for a public certificate.
- `scripts/ci-freebsd-e2e.sh`: a block behind `JAILOPS_E2E_APPLIANCE=1` (off by default): the rc switch on a test configuration, `check-config` and doctor `appliance`, the setup token flow with curl, a certificate upload served to `openssl s_client`, and the refusals of ACME settings, restoring `rc.conf` at the end. UNVERIFIED on a FreeBSD host.

- **The command line after a usability review** (changes that scripts see are marked **scripts**).
  - **scripts:** `jailops delete` (and `jail delete`, `delete --all`, `delete -l`) is a data-loss action: before anything is deleted it shows, read from the server, the size of each jail's root and data, its volumes and its number of snapshots, and asks for the jail's name typed (the project name for several). `-y` no longer answers it; scripts pass `--confirm-data-loss`. A running jail without `--force` is refused before the question; `--force` still stops it first and confirms nothing. `--dry-run` shows the same summary, and a missing jail is `not_found` in a dry run too (every kind's `delete --dry-run` checks that the object exists).
  - **scripts:** `user set-role` keeps what is not given: `--projects` alone keeps the role, `--role` alone keeps the scope (it used to fall back to `readonly`); it prints the role and scope before and after.
  - **scripts:** `--role` is the flag of `token create`, `cert create`, `user create`, `user set-role` and `trust add`; `--permission` stays accepted as a hidden alias. Tables say `ROLE` where they said `PERMISSION`; the API fields keep their names (`permission`).
  - **scripts:** `app export` writes its file with `--to FILE`; `-o` is the output format there too (`-o json` or `-o yaml` prints the draft as data). `-o NAME.yaml` (or `.yml`), the former spelling, still writes the file and prints a deprecation warning.
  - **scripts:** `logs forward remove` and `logs forward reset` ask for confirmation (`-y` answers it). Their confirmations read `log-sink/NAME added|updated|removed` and `log-forward reset: ...` like the other commands.
  - **scripts:** `not_managed` exits 6 (like `unsupported`) instead of 3.
  - **scripts:** a daemon that cannot be reached prints `unavailable: cannot reach jailopsd at SOCKET (reason)` (or the URL and remote name) with a hint (`service jailopsd status` for the local socket) instead of the raw transport error; a missing input file no longer gets the socket hint. The exit status stays 1.
  - **scripts:** `exec` reads its flags anywhere before `--` (`jailops exec web --project shop -- ls`); only the jail name may stand before `--` with them. Without `--` the first word after the name starts the command as before, and a first word that starts with `-` is refused with the right form.
  - **scripts:** `network grant` and `network revoke` take the project from the global `--project`, `JAILOPS_PROJECT` or the current project (their own required `--project` hid the global one).
  - `jailops host certificate show|acme|upload|self-signed` (extension `host_certificate`): the served certificate with its subject, names, issuer, expiry, fingerprint and source, the ACME state and the exact `jailops remote add` command; ACME with `--domain`, `--email`, `--challenge`, `--staging`, `--accept-tos`; upload with `--cert`, `--key`, `--hostname`; back to self-signed after the name of the replaced certificate is typed (`--i-know` for a script). docs/exposure.md uses these commands; the curl form stays as one line. The fake serves the four routes.
  - `jailops audit --limit` works (it was in the help but not a flag), with `--since DURATION` and `--user NAME` (sent to the server) and `--action PATTERN` (matched by the client over the server's 1000 newest entries). SDK `AuditFiltered` and `AuditFilter`.
  - `jail create` without `--network` or `--storage` (and without a profile) takes the project's only network or storage, as `app install` does, and says which on standard error; several grants are never guessed from. `api.ProjectSpec.SoleNetwork` and `SoleStorage` are the one rule for both.
  - Error hints: an `invalid` error no longer gets a generic hint; `not_found` names the list command of the kind (`jailops jail list`, `jailops op list`, ...); `conflict` gets the read-again hint only for a change made meanwhile; `forbidden` for a network or storage that is not granted names `jailops network grant NET --project P` or `jailops project set P --storage NAME` (from the server). Server messages name the CLI flag: quotas ask for `--cpu`, `--memory`, `--pids`; sizes, CPU and image references name `--memory`, `--disk`, `--cpu`, `--image`.
  - The vocabulary: `-l` and `--selector` (and `--label`) are accepted wherever a selector is (lists, start/stop/restart/delete, logs, log sinks, `backup policy create`, which also gets `-l`); `trust add --expires` (`--ttl` stays a hidden alias); `--password-file FILE|-` for `user create` and `user set-password`, `--passphrase-file -` and `remote add --token-file -` read standard input. A trust token given to the two-argument `remote add NAME URL` is refused with the one-argument command that joins.
  - `show` of storage, networks, images and projects prints a short human form with sizes in units (no raw bytes, no generation counter); `-o yaml`/`json` are unchanged. An empty list says `No jails in project P. Create one with: ...` with the kind's create command; a filtered one says `No jails in project P match.`.
  - Lists: `-c` and `--sort` accept every column the kind has (the short terminal form's too, such as `ipv4`), the error and the help list them, and the generic example uses columns each kind has.
  - Every example of the help runs: a test parses each example line against the command tree and runs it with `--dry-run` against the fake (a capitalised word or `...` is a placeholder). The failing examples (`-c name,age` on kinds without an age, `token create`, `cert create` and `user create` without a scope) are fixed.
  - Help and man pages: the root help and every man page have ENVIRONMENT (with `JAILOPS_PROJECT`, `JAILOPS_OUTPUT`, `JAILOPS_EDITOR`) and EXIT STATUS sections; the "a project is required" hint names `JAILOPS_PROJECT`; `explain` tells the confirmation flags apart (`-y`, `--confirm-data-loss`, `--i-know`, `--force`) and completes error codes; `--image` completes `NAME@alias`; confirmations that get no answer say that standard input gave none; the `(admin)` markers left the one-line descriptions (the long text says what role a command needs). jailops-priv(8) documents its one-dash flags.
  - docs/install.md: the kernel modules are the ones doctor checks (`if_bridge`, `if_epair`, `pf`, `zfs`), the token is created after the project it is scoped to, the storage `default` is explained before it is used, `vtnet0` is the uplink of the example, root is assumed instead of `sudo` (not in the base system), the appliance path has four commands and a browser, and remote access uses the one-argument `remote add HOST --token-stdin --name NAME` that redeems a trust token (also in docs/exposure.md; a test runs this sequence). `scripts/ci-freebsd-e2e.sh` and `ci-freebsd-pf.sh` delete jails with `--confirm-data-loss`, export with `--to`, answer the sink removal with `-y`, and check the delete summary, the exec flag order, the create defaults and the certificate commands.

## 0.5.0 - 2026-11-02

- **The logs of all jails, and forwarding them** ([docs/logging.md](docs/logging.md)).
  - `jailops logs` without a jail merges the end of `messages` (or `--file`) of every jail of the project into one stream ordered by the timestamp each line starts with (RFC 3339, RFC 5424, `YYYY-MM-DD HH:MM:SS`, `Mon DD HH:MM:SS` in the current year or the one before, the bracketed web form); a line without one keeps its place after the line before it. Each line starts with `jail/file`, coloured per jail on a terminal; `--jail` and `-l KEY=VALUE` (repeatable), `--app`, `--app-logs` (the files installed apps declare below `/var/log`), `--all-projects`, `--tail`, `--since`, `--grep` per file, `-f` follows all of them, `-o json` prints one `{time, project, jail, app, file, line}` per line. A file that cannot be read (a jail that is not mounted) is named on standard error. `jailops logs NAME [FILE]` is still the one jail's logs; a jail named `forward` is read with `jailops jail logs forward`.
  - New route `GET /api/v1/logs` (extension `logs_unified`, operator in every project read, audited once per identity and selection every five minutes): the per-jail reads of up to 50 files, merged, at most 2000 lines, with a `next` map of offsets per file sent back as `cursor` to follow. Types `LogsQuery`, `LogsRead`, `LogEntry`, `LogSource`; `c.Logs().Read` in the SDK and the fake.
  - **Forwarding**: `jailopsd` sends the same files (default `messages` of every jail plus the logs installed apps declare; narrowed by project, jail and label, with exclusions) and, on request, its audit log to sinks: RFC 5424 syslog over udp, tcp or tls (octet-counted frames, facility `local0` to `local7`, APP-NAME `jailops`, structured data `[jailops project="" jail="" app="" file=""]`, the line escaped and cut at 8 KiB) and https webhooks (JSON lines, a bearer token read from a root-owned file at each delivery). TLS is always verified (the host's roots or `ca_file`); a plain sink to a remote address carries a warning. One loop per sink reads through the helper's `fs.tail_file` only (no new helper operation), keeps a bounded queue (default 10,000 lines, the oldest dropped and counted), retries with exponential backoff and jitter, and saves per-file offsets in the database: at least once across restarts, starting over after a rotation; a new sink starts at the end of the files. No new dependency.
  - The sinks are `logging.forward` of `jailopsd.yaml` unless a global admin stores a list (state kind `LogForward`, extension `log_forward`): `GET`/`PUT`/`DELETE /api/v1/logs/forward` (`PUT` with `If-Match`; `DELETE` returns to the file) and `POST /api/v1/logs/forward/test`. `jailops logs forward list|add|edit|remove|test|status|reset` (`add syslog --to tls://logs.example.com:6514 --facility local3 [--ca FILE] [--jail NAME]... [-l K=V]...`, `--dry-run`, completion), `c.Logs().Forward`, `SetForward`, `ResetForward`, `TestForward` in the SDK and the fake (`SeedLogForward`, `SeedLogSinkStatus`, `SeedLogSinkTestFailure`). `jailops doctor` reports `logs.forward` (warn when a sink has failed for more than five minutes).
  - The host console has a **Logs** page (`/logs`): filters for project (or all), jails, app, text and time window, the file and app logs, a live follow, a colour chip per jail and the files that could not be read; a **Forwarding** tab for a global admin lists the sinks with their state and adds, edits, tests and removes them, or returns to the file's list. The Logs tab of a jail links to it.
  - `scripts/ci-freebsd-e2e.sh` starts a tcp listener on the host, adds a syslog sink with the CLI, logs a line with `logger` in a jail and checks that it arrives with the jail in the structured data, then removes the sink. UNVERIFIED on a FreeBSD host.
- **Host firewall (appliance module 2) and commit confirmed** ([docs/appliance.md](docs/appliance.md)). Off by default: with `firewall` in both `host.manage.modules` of `jailopsd.yaml` and `host-manage` of `priv.conf`, and only then, JailOps owns `/etc/pf.conf` as a whole file (and `pf_enable`, `pf_rules`); a host without the module keeps today's behaviour exactly.
  - The privileged helper renders the file itself from a validated `HostFirewallSpec` (template `default`, `open` or `custom-rules`; ssh ports and sources; console sources, the port being the daemon's TLS port; up to 64 `allow` rows of proto, ports, sources and a comment; address sets as pf tables; `icmp`; `logging`; `nat: none`): the three anchor lines of design 5.2 (`anchor "jailops/*"` first among the filter rules), loopback, neighbour discovery, router advertisements, path MTU discovery and ICMP errors always, `block in all` for the rest. It checks the text with `pfctl -n -f -`, refuses a ruleset that would shut out the caller's address (the daemon passes the peer address; unknown on the unix socket and through a hub, then `i_know` is needed), keeps a snapshot (the first apply keeps the administrator's own file, reason `adopt`), writes and loads the file and turns pf on when it was off. Golden files for the renderer, refusal tests over the wire for every field.
  - **Commit confirmed**, generic in the helper: a risky change writes and syncs `<host-dir>/pending.json` (id, module, snapshot, deadline) before it touches the host; the helper's own timer reverts it at the deadline (`--confirm-within`, default 120 s, 10 s to 30 min) unless it is confirmed. One change waits at a time; any other host change answers `busy` naming it. The helper re-arms the timer at start, or reverts at once when the deadline passed; the new rc.d script `jailops_revert` (`BEFORE: netif pf jailops_priv`) runs `jailops-priv revert-pending` at boot, so a reboot never keeps an unconfirmed change. How the last change ended is kept (`confirmed`, `reverted`, `timed_out`, `reverted_at_boot`, `revert_failed`).
  - Helper: five new operations, `host.firewall.read`, `host.firewall.apply` (admin, `host-manage = firewall`), `host.pending.read`, `host.pending.confirm` (admin, the module), `host.pending.revert` (admin); its own six `pfctl` forms (`-n -f -`, `-f /etc/pf.conf`, `-e`, `-d`, `-s info`, `-s rules`), none reachable through `run.exec`; `sysrc` accepts `pf_enable=YES` and `pf_rules=/etc/pf.conf` only. `host.snapshot.restore` now needs the module of the snapshot.
  - API (extensions `host_firewall`, `host_pending`): `GET`/`PUT /api/v1/host/firewall` (kind `HostFirewall`), `GET /api/v1/host/pending`, `POST /api/v1/host/pending/confirm`, `POST /api/v1/host/pending/revert`; `HostChange` gains `pending`, `rules` and `warnings` (additive). SDK `c.HostSystem().Firewall()` (`Get`, `Apply`), `Pending`, `Confirm`, `Revert`; the fake serves them (`fake.WithHostFirewall()`).
  - CLI: `jailops host firewall show|set|apply -f FILE|diff`, `jailops host pending|confirm [--id]|revert [--id]`, with `--dry-run`, `--confirm-within` and `--i-know`.
  - Doctor: `host.firewall` (not managed; enabled on one side; a change waiting for its confirmation; drift of `/etc/pf.conf`, `pf_enable` or `pf_rules`).
  - Console: a Firewall section on the Host page (rules, an editor with inline validation, Preview with the diff, Apply with the confirmation window explained, a warning while ssh is open to the world) and a banner on every page while a change waits, with a countdown and Confirm and Revert, also after a reload.
  - `scripts/ci-freebsd-e2e.sh`: with `JAILOPS_E2E_HOSTFW=1` (off by default, CI does not set it) it keeps the ssh port open to the `SSH_CLIENT` address, applies with `--confirm-within 20` and lets it revert, applies and confirms, then restores the original `pf.conf` through the snapshot and checks it byte for byte, with an EXIT trap that puts the original back. UNVERIFIED on FreeBSD: the rendered file with the real `pfctl`, a host whose pf was disabled, IPv6 sources, and the boot order of `jailops_revert`.
- **Encrypted volumes** ([docs/storage.md](docs/storage.md#encrypted-volumes)): a volume can be encrypted at rest with native ZFS encryption, chosen when the jail is created (`--volume NAME:MOUNT:encrypted[:QUOTA]`, `encrypted: true` on a volume of a spec). The volume is its own encryption root (`aes-256-gcm`, `keyformat=passphrase`, `keylocation=prompt`); one passphrase per jail, 12 to 512 bytes, asked twice without echo or read from `--passphrase-file` (never an argument). After every host boot the jail is **locked** (state `locked`, `status.locked`), a start is refused with the new error code `locked` (409, hint `jailops jail unlock NAME`) and the boot skips it; `jailops jail unlock NAME [--start]` loads the keys and starts a jail that was due for autostart. `jailops jail lock NAME` stops the jail and unloads the keys; `jailops jail passphrase NAME` changes the passphrase (`zfs change-key`, the old one checked first). A wrong passphrase answers `bad_passphrase` (403); five in a row lock a jail's unlocks out for 30 seconds. Copies of an encrypted volume are raw sends (still encrypted, same passphrase, locked). A backup skips a locked volume (`skipped: locked: volume NAME` among the run's warnings); a restore makes a plain volume and its preflight says so. The encryption of a volume cannot change later; encrypted storages and whole jails stay unsupported. API extension `volume_unlock`: `volume_passphrase` in `POST /api/v1/jails` (`JailCreate`), `POST /api/v1/jails/{name}/unlock` (operator), `.../lock` (operator), `.../passphrase` (admin); `Jails().CreateWithPassphrase`, `Unlock`, `Lock`, `ChangePassphrase` in the SDK and the fake (which records request bodies with passphrases replaced, and has `RebootHost`). The passphrase reaches the helper on the request stream of five new typed operations (`zfs.create_encrypted`, `zfs.load_key`, `zfs.unload_key`, `zfs.change_key`, and the read-only `zfs.feature_state`; `zfs.replicate` takes `raw`), which accept only an encrypted volume of a JailOps jail, and `zfs` on standard input: it is in no argument vector, reply, operation, journal, audit entry or log line, and tests search all of them for it. Doctor: `volumes.locked` (info, the jails waiting for a passphrase) and `volumes.encryption` (`feature@encryption` of each storage pool). The host console has an Unlock dialog (no echo, a show/hide toggle, nothing kept), a Lock button, a Change passphrase dialog (admin), a locked chip in the jail list and on the jail page, a dashboard banner counting locked jails, and a volume (optionally encrypted) in the create form; the demo has a locked jail `vault` (passphrase `demo-vault-passphrase`). `scripts/ci-freebsd-e2e.sh` has an `encrypted volume` block (skipped when the pool lacks the feature). UNVERIFIED on a FreeBSD host.

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
