# Provisioning: a host, and a jail

Two things are called provisioning. This page keeps them apart.

## 1. Provisioning a host: a fresh VM to a running fleet

[`examples/provisioning`](../examples/provisioning) is a path in one repeatable script, `bootstrap.sh`: it checks
FreeBSD 15.1 and the pool, loads the kernel modules, installs the packages, adds the three `pf.conf` anchor lines, turns on
forwarding, runs the setup, writes what the host operator approves into `priv.conf` (`admin-host-mount`),
starts the services, requires a clean `jailops doctor`, clones a fleet repository and runs `jailops-sync validate`, `plan` and
`sync --apply`. It is the sequence of [install.md](install.md) as a script; the README of the example lists every variable.

Order of responsibility, and why it is split:

| Who | Decides | Where |
|---|---|---|
| the host operator (root) | what the unprivileged daemon may ask the helper for | `priv.conf`, `jailopsd.yaml`, `pf.conf`: written by `bootstrap.sh`, never by the API |
| a global admin | networks, storage, images, projects | the fleet repository, applied by `jailops-sync` |
| a project operator | jails inside a project's grants | the fleet repository or the CLI |

A fleet applied on the host itself reaches `jailopsd` through the local socket (root is a global admin there), so a bootstrap needs
no token. Anything remote uses a token or a pinned client certificate; see [exposure.md](exposure.md).

Keep it converged with `jailops-sync watch --once --apply` from cron, or only report with `jailops-sync drift`.

**UNVERIFIED:** the script on a fresh cloud VM, and `nuageinit` as the way to run it (the individual commands are those of install.md and
`scripts/ci-freebsd-e2e.sh`, which run on FreeBSD 15.1).

## 2. Applying a recipe to a jail

`jailops jail apply NAME -f RECIPE` runs a **recipe** in a running FreeBSD jail: packages, files, rc variables, services and
commands. You run it, when you want, as often as you want. Nothing runs when a jail is created or started, and the jail is never
stopped, restarted or changed in its specification. There is no agent in the jail and no convergence.

    # web/recipe.yaml
    steps:
      - package: [nginx, rsync]
      - file:
          path: /usr/local/etc/nginx/nginx.conf
          source: nginx.conf                  # a file beside the recipe; or  content: |  with the text itself
          mode: "0644"
      - file:
          path: /usr/local/etc/profile.d/alias.sh
          content: |
            alias w='w -n'
      - sysrc: {nginx_enable: "YES"}
      - service: {name: nginx, enabled: true, running: true}
      - run: [/usr/local/sbin/nginx, -t]
      - run: [pw, useradd, deploy, -m, -s, /bin/sh]
        unless: [pw, usershow, deploy]        # skipped when this exits 0

    $ jailops jail apply web -f web/ --dry-run
    $ jailops jail apply web -f web/
      package  nginx rsync                  changed  (installed nginx rsync)
      file     /usr/local/etc/nginx/nginx.conf   changed  (new file)
      ...
      6 steps: 5 changed, 1 executed; a run without creates or unless is executed every time

`-f DIR` means `DIR/recipe.yaml`; `source:` files are read by the client from the directory of the recipe (clean relative
paths below it) and sent with the request, 1 MiB each and 8 MiB in all, as text. The recipe is a list of steps that run **in the
order of the list**; an unknown key is an error.

| Step | Fields | Unchanged when |
|---|---|---|
| `package` | a list of package names | every package is installed (`pkg query` through the host's `pkg -j`, so the jail needs no package tool of its own); only the missing ones are installed |
| `file` | `path` (absolute, inside the jail), `source` or `content`, `mode` (default `0644`), `owner`, `group` (names of the jail's own `/etc/passwd` and `/etc/group`, or numbers) | the bytes, the mode and the owner are those of the recipe; otherwise the file is written whole to a temporary name with its final mode and owner and renamed, so there is no moment in which a `0600` file is readable by others |
| `sysrc` | a map of variables, and `in:` `rc.conf` (default) or `periodic.conf` | every variable has the value; the file of the jail is edited from the host, only the variables that differ |
| `service` | `name`, `enabled`, `running` | `enabled` sets `NAME_enable` in `rc.conf` like `sysrc` does; `running: true` starts the service only when `service NAME status` says it does not run, `running: false` stops it only when it runs. **A service is never restarted.** A service that is not enabled cannot be started: set `enabled: true` too |
| `run` | `run` (argv, never a shell), `creates` (a path), `unless` (argv) | never "unchanged": see below |

The step is called `sysrc` because that is the FreeBSD name for editing rc variables; it does **not** call the tool in the jail
(new images have none). `rc.conf.local` is not allowed: JailOps writes it at every start, and it wins over `rc.conf`.

**Commands.** `run` takes an argv (a shell is `[sh, -c, "..."]` and yours to write) and runs with the login PATH of the jail
(`/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin`), so `nginx -t` works. `creates: PATH` skips the step when the path
exists in the jail; `unless: [argv]` skips it when that command exits 0. A guard that fired is a `skipped` step, a guard that did
not fire runs the command and reports `changed`. A `run` **without a guard** is reported `executed`, never `changed`, because
JailOps cannot know whether it changed anything; the summary says that a run had no guard.

**Result and failure.** Each step is `changed`, `unchanged`, `skipped`, `executed` or `failed` (with the last lines of its output).
The first failed step stops the run; the steps before it stay and there is no undo. Fix the recipe and run it again: every step
looks at the state first, so the steps that worked are `unchanged`.

**Dry run.** `--dry-run` reads the state (the package list, the files, `rc.conf`, `service NAME status`, the `creates` paths) and
reports `would change`, `unchanged` or `skipped`. It runs none of your commands, installs nothing and writes nothing: a `run` with
`unless` is `would check` and one without a guard `would execute`. The only things it asks the jail are read-only (`service NAME
status`).

**Which files.** `file` refuses paths below `/etc`, `/boot`, `/bin`, `/sbin`, `/lib`, `/libexec`, `/rescue`, `/usr/bin`, `/usr/sbin`,
`/usr/lib`, `/usr/libexec`, `/usr/share` and `/usr/include`: the base system (pkgbase) owns them, and local configuration goes to
`/usr/local/etc`. The exceptions are the four files FreeBSD expects an administrator to edit and that only exist as defaults in
the base: `/etc/sysctl.conf`, `/etc/periodic.conf`, `/etc/ssh/sshd_config` and `/etc/newsyslog.conf` (a `file` step replaces the whole file; the image starts
`periodic.conf` with a few lines of its own, so set variables there with `sysrc` and `in: periodic.conf`). This is a rule against
overwriting what a package or an upgrade owns, **not a security boundary**: a `run` step can do anything its operator can do in the jail.

**Shell files.** A file in `/usr/local/etc/profile.d/` ending in `.sh` is read by the shells listed in [shell.md](shell.md#files-in-profiled).
The jail needs the shell hook of JailOps for that: `jailops jail shell-setup NAME` writes it (new jails have it).

**Who.** A project operator, as for `exec`, `cp` and `jailops pkg`: a recipe can do nothing they could not do by hand, and the
operation is audited. The jail must run and be a FreeBSD jail (a Linux jail is refused: use `jailops exec` and `jailops cp`).

Examples: [`examples/recipes`](../examples/recipes) (nginx and PostgreSQL), [`examples/pkgops-packaged-service`](../examples/pkgops-packaged-service)
(a package from your own PkgOps repository).
