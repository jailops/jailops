# Daily tasks of a jail administrator

The workflow walkthrough: every task a jail administrator expects to do, the command that does it today, and whether that
is as easy as it should be. `scripts/e2e-workflows.sh` runs the rows marked OK on a FreeBSD machine
(`just e2e-workflows`), and `scripts/check-workflows-table.sh` keeps this table and that script in step. `just release-pr`
runs the check, so a release does not go out with a row that is missing and has no plan.

A row has six cells:

- **ID**: a short name, also the name of its step in the script.
- **A jail admin expects to**: the task, in the words of the person who does it.
- **Command**: what does it today, or `none`.
- **Rating**: `OK` (one command, or the commands that belong together), `MORE STEPS` (it can be done, with steps the admin
  should not need), `MISSING` (it cannot be done, or only by hand).
- **Ref**: for every row that is not OK, an entry `W-name` of the section "Gaps found by the workflow walkthrough" in
  [docs/roadmap.md](../../docs/roadmap.md): what is planned and in what shape; or `docs/roadmap.md#long-term-roadmap` for what
  works between hosts and is a long-term item.
- **Step**: the step of `scripts/e2e-workflows.sh` that covers the row. An OK row has a `row` (it runs) or a
  `row_elsewhere` (another script covers it, and says which); a MISSING row has a `row_pending` that starts to run when
  the command exists; a MORE STEPS row has `-` or a step.

Add a row whenever someone names a task that is not here. When a command lands, the row becomes OK, its pending step
becomes a real one and its entry leaves the roadmap section, all in the change that adds the command.

| ID | A jail admin expects to | Command | Rating | Ref | Step |
|---|---|---|---|---|---|
| bootstrap-release | get the image of a new FreeBSD release ready | `jailops image create freebsd-15-2 --release 15.2-RELEASE --build` | OK | - | bootstrap-release |
| create | create a jail from an image | `jailops create NAME --image IMG --start` | OK | - | create |
| patch-one | patch the operating system of one jail | `jailops jail upgrade NAME` | OK | - | patch-one |
| patch-all | patch every jail | `jailops jail upgrade --all` (also `-l KEY=VALUE`; the nightly run with `image set IMG --auto-upgrade` does it unattended) | OK | - | patch-all |
| upgrade-minor | move a jail to the next minor release (15.1 to 15.2) | `jailops jail upgrade NAME --release 15.2-RELEASE` (also `-l KEY=VALUE`, `--all`) | OK | - | upgrade-minor |
| upgrade-major | move a jail to the next major release (15 to 16) | `jailops jail upgrade NAME --release 16.0-RELEASE` upgrades the base in place; the ports packages are reinstalled by hand (`pkg upgrade -f` inside the jail) | MORE STEPS | W-upgrade-major | - |
| copy-spec | create a jail with the configuration of another | `jailops jail copy SOURCE NAME` | OK | - | copy-spec |
| clone-data | clone a jail with its data | `jailops jail copy SOURCE NAME --data [--start]` | OK | - | clone-data |
| clone-snapshot | create a jail from a snapshot of another | `jailops jail copy SOURCE NAME --data --snapshot S` | OK | - | clone-snapshot |
| move-host | clone or move a jail to another host | none (between hosts: later; a backup restore with `--to-host` needs a backup node) | MISSING | docs/roadmap.md#long-term-roadmap | move-host |
| export-import | export a jail to a file and import it on another host | `jailops jail export NAME --to FILE`, `jailops jail import FILE [--as NAME]` | OK | - | export-import |
| move-project | move a jail to another project | `jailops jail export NAME --to FILE`, then `jailops jail import FILE --project P` | OK | - | move-project |
| rename | rename a jail | `jailops rename OLD NEW` | OK | - | rename |
| shell-and-command | open a shell in a jail and run one command | `jailops shell NAME`, `jailops exec NAME -- CMD` | OK | - | shell-and-command |
| exec-many | run a command, or install a package, in many jails at once | `jailops exec (NAME... \| -l KEY=VALUE \| --all) -- CMD`, `jailops pkg install -l KEY=VALUE -- PKG` | OK | - | exec-many |
| profiles | apply the same settings to several jails | `jailops profile create`, `jailops create --profile` | OK | - | profiles |
| apps | install an application from the catalog as a jail | `jailops app install APP` | OK | - | apps |
| copy-files | copy files into and out of a jail | `jailops cp SRC DST` with `NAME:/path` on either side | OK | - | copy-files |
| volume | give a jail a volume, move it to another jail | `jailops volume create`, `volume attach`, `volume detach` | OK | - | volume |
| host-dir | mount a directory of the host in a jail | `jailops volume create NAME --from-dir /host/dir`, then `jailops volume attach` | OK | - | host-dir |
| limits | limit memory, processes and CPU share of a jail | `jailops create --memory --pids --cpu`, `jailops config set limits.*` | OK | - | limits |
| cpu-pinning | pin a jail to CPU cores | `jailops create --cpuset 2,3`, `jailops config set NAME resources.cpuset=2,3` | OK | - | cpu-pinning |
| expose | let the Internet reach a port of a jail | `jailops expose NAME PORTS`, `jailops unexpose NAME` | OK | - | expose |
| snapshot-rollback | snapshot a jail and roll it back | `jailops jail snapshot create`, `jailops jail rollback --scope root\|data\|all` | OK | - | snapshot-rollback |
| boot-order | decide which jails start at boot and in which order | `jailops create --autostart --boot-priority N --depends JAIL`, `jailops boot` | OK | - | boot-order |
| destroy | destroy a jail | `jailops delete NAME` | OK | - | destroy |
| edit-config | read and change the configuration of a jail | `jailops config get\|set\|edit NAME` | OK | - | edit-config |
| list-monitor | see which jails run and what one of them uses | `jailops list`, `jailops info NAME` | OK | - | list-monitor |
| live-usage | watch the live CPU, memory and disk use of the jails of this host | `jailops top [-l KEY=VALUE]` | OK | - | live-usage |
| service-monitor | have a dead service in a jail started again | none (later) | MISSING | docs/roadmap.md#long-term-roadmap | service-monitor |
| backup-restore | back up a jail and restore it on the same host | `jailops backup target\|policy\|run\|restore` (needs a backup node, a target and a policy) | MORE STEPS | W-backup-nodeless | - |
| restore-after-loss | restore a jail on a new host after the old host is gone | `jailops backup key import`, then `jailops backup restore` | OK | - | restore-after-loss |
| previous-release | keep jails of the previous release running after the host moved to the next | the jail keeps its image | OK | - | previous-release |
