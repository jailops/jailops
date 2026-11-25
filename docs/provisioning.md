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
commands. Recipes are for FreeBSD jails only: the daemon refuses a Linux jail that is named (no requirement and no step runs), a
selection with `-l` or `--all` leaves Linux jails out, and sync skips them ([fleet-sync.md](fleet-sync.md#recipes)). You run it, when you want, as often as you want. Nothing runs when a jail is created or started, and the jail is never
stopped, restarted or changed in its specification. There is no agent in the jail and no convergence.

    # web/recipe.yaml
    requirements:                             # optional: what the jail must already have
      - check: [test, -x, /usr/sbin/sshd]     # a command run in the jail; exit status 0 means met
        message: sshd is not installed in the jail
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
paths below it; a symbolic link that leads out of the directory is refused, one that stays inside is fine) and sent with the
request, 1 MiB each and 8 MiB in all, as text. The daemon reads no path of the host and fetches nothing from the network. The
recipe is a list of steps that run **in the order of the list**; an unknown key is an error, at most 200 steps.

| Step | Fields | Unchanged when |
|---|---|---|
| `package` | a list of package names | every package is installed (`pkg query` through the host's `pkg -j`, so the jail needs no package tool of its own); only the missing ones are installed |
| `file` | `path` (absolute, inside the jail), `source` or `content`, `mode` (three octal digits, optionally with a leading `0`; default `0644`; no set-id or sticky bit), `owner`, `group` (names of the jail's own `/etc/passwd` and `/etc/group`, or numbers) | the bytes, the mode and the owner are those of the recipe; otherwise the file is written whole to a temporary name with its final mode and owner and renamed, so there is no moment in which a `0600` file is readable by others |
| `dir` | `path`, `mode`, `owner`, `group` | the directory exists and has the mode and owner the step names. It is never recursive and touches nothing inside. A new directory is `root:wheel` `0755` unless the step says otherwise; one that exists is changed only in the fields the step names (a step with no `mode` leaves the mode). The mode gives the owner all rights (`0755`, `0750`, `0700`) |
| `sysrc` | a map of variables, and `in:` `rc.conf` (default) or `periodic.conf` | every variable has the value; the file of the jail is edited from the host, only the variables that differ |
| `service` | `name`, `enabled`, `running` | `enabled` sets `NAME_enable` in `rc.conf` like `sysrc` does; `running: true` starts the service only when `service NAME status` says it does not run, `running: false` stops it only when it runs. **A service is never restarted.** A service that is not enabled cannot be started: set `enabled: true` too |
| `run` | `run` (argv, never a shell), `creates` (a path), `unless` (argv) | never "unchanged": see below |

The step is called `sysrc` because that is the FreeBSD name for editing rc variables; it does **not** call the tool in the jail
(new images have none). `rc.conf.local` is not allowed: JailOps writes it at every start, and it wins over `rc.conf`.

Directories that a `file` step needs and that do not exist are made `root:wheel` `0755`, never owned by the owner of the file
(a directory owned by the user of the file would let that user replace what the file's parent holds). Use a `dir` step when the
directory itself needs another mode or owner, for example `/home/admin/.ssh` with `0700` and `owner: admin`.

**Commands.** `run` takes an argv (a shell is `[sh, -c, "..."]` and yours to write) and runs with the login PATH of the jail
(`/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin`), so `nginx -t` works. `creates: PATH` skips the step when the path
exists in the jail; `unless: [argv]` skips it when that command exits 0. A guard that fired is a `skipped` step, a guard that did
not fire runs the command and reports `changed`. A `run` **without a guard** is reported `executed`, never `changed`, because
JailOps cannot know whether it changed anything; the summary says that a run had no guard.

**Result and failure.** Each step is `changed`, `unchanged`, `skipped`, `executed` or `failed`. The first failed step stops the run;
the steps before it stay and there is no undo. Fix the recipe and run it again: every step looks at the state first, so the steps
that worked are `unchanged`. A failed step is the error `step_failed` (exit status 1, not `internal`): it names the step, the exit
status of the command and its last lines of output, for example `step 4 of 6 failed (run /usr/sbin/sshd -t): exit status 1:
...`. A recipe that needs a reload ends with an unguarded `run` such as `[service, nginx, reload]`: it runs in every apply
and is reported `executed`, so a reload that failed once is done by the next run even when the file is `unchanged` by then.
YAML reads `[false]`, `[yes]`, `[5]` and `mode: 0644` as booleans and numbers, not text: the command line says which step and field and
asks you to put quotes around the word (`["false"]`).

**Requirements.** A recipe may start with `requirements:`, a list placed before `steps:`. Each item has `check`, a command as argv
(never a shell) that runs inside the jail with the login PATH, where exit status 0 means the requirement is met, and `message`, one
line that says what is missing. `${name}` is replaced in `check` (a `split` variable as in `run`) and in `message`; a `secret`
variable may not be used in a requirement. **All requirements are checked before any step runs.** When one or more are not met the
apply stops with the error `requirement_failed` (exit status 1, HTTP 422), which names each unmet requirement by its message and
the exit status of its check, for example `requirement not met: sshd is not installed in the jail (the check exited with status 1)`:
no step ran and nothing in the jail was changed. The output of a check is never shown. JailOps does not know what a check asks, it
only runs the command: anything that can be fixed inside the jail is fixed by an ordinary step of the recipe, and what needs a change of the
jail's parameters or of the host (a `jail.conf` key, a kernel setting) is for the administrator, who changes it and applies the recipe
again. A requirement is part of the recipe: changing one changes the recipe's hash for sync.

**Dry run.** `--dry-run` reads the state (the package list, the files, `rc.conf`, `service NAME status`, the `creates` paths) and
reports `would change`, `unchanged` or `skipped`. It runs none of your commands, installs nothing and writes nothing: a `run` with
`unless` is `would check` and one without a guard `would execute`. It runs no check of `requirements:` either: they are listed as
`not checked`, and the dry run does not say whether the jail meets them. The only things it asks the jail are read-only (`service NAME
status`).

**Which files.** `file` refuses paths below `/etc`, `/boot`, `/bin`, `/sbin`, `/lib`, `/libexec`, `/rescue`, `/usr/bin`, `/usr/sbin`,
`/usr/lib`, `/usr/libexec`, `/usr/lib32`, `/usr/libdata`, `/usr/share`, `/usr/include` and `/usr/tests`: the base system (pkgbase) owns them, and local configuration goes to
`/usr/local/etc`. The exceptions are the four files FreeBSD expects an administrator to edit and that only exist as defaults in
the base: `/etc/sysctl.conf`, `/etc/periodic.conf`, `/etc/ssh/sshd_config` and `/etc/newsyslog.conf` (a `file` step replaces the whole file; the image starts
`periodic.conf` with a few lines of its own, so set variables there with `sysrc` and `in: periodic.conf`). This is a rule against
overwriting what a package or an upgrade owns, **not a security boundary**: a `run` step can do anything its operator can do in the jail.

**Shell files.** A file in `/usr/local/etc/profile.d/` ending in `.sh` is read by the shells listed in [shell.md](shell.md#files-in-profiled).
The jail needs the shell hook of JailOps for that: `jailops jail shell-setup NAME` writes it (new jails have it).

### Variables

One recipe serves many jails when its values are variables. They are filled in by the client (the command line, `jailops-sync` and
the console) before the request is sent: the daemon receives the finished recipe and knows nothing about variables.

    # web/recipe.yaml
    vars:
      site:     {required: true}                       # no default: the apply stops until it is given
      port:     {default: 8080}
      packages: {default: "nginx curl", split: true}   # a list: cut at spaces where the recipe takes a list
      pg_pass:  {secret: true, required: true}         # only in the content of a template file
    steps:
      - package: ["${packages}"]
      - dir: {path: "/var/db/${site}", mode: "0750"}
      - file:
          path: /usr/local/etc/nginx/sites/${site}.conf
          source: files/site.conf.in
          template: true                               # ${site} and ${port} inside the file are filled in
      - file:
          path: /usr/local/etc/app.conf
          template: true
          mode: "0600"
          content: "password=${pg_pass}\n"
      - run: [sh, -c, 'echo "$1" >/var/db/note', sh, "${site}"]   # a value is an argument, never part of the shell text

    $ jailops jail apply web -f web/ --set site=example.test --set-file pg_pass=app.password
    $ jailops jail apply web -f web/ --vars web.vars          # lines NAME=VALUE; # starts a comment; quotes are optional
    $ jailops jail apply -f web/ --set site=example.test --set-file pg_pass=app.password --validate

Many jails take the same recipe with `-l KEY=VAL` (jails with the label) or `--all` (every jail of the project), instead of a name; a Linux jail is not part of
a selection. The recipe is rendered once and a file given with `--set-file` is read once. Each jail is done on its own, one after the other, with a block of result
per jail; a failure on one jail is reported (`jail/NAME: failed: ...`) and the others go on, and the exit code is non-zero if any jail failed.
`--dry-run` and `--validate` work the same way.

    $ jailops jail apply --all -f hardening/ --dry-run
    $ jailops jail apply -l tier=web -f web/ --set-file pg_pass=app.password

- A name is letters, digits and `_`, not starting with a digit, in either case. A declaration has `default`, `required`, `secret` and `split`.
  A variable with neither a default nor `required` is the empty string. A default may use the variables declared before it.
- `${name}` is replaced in every string value of a step: package names, words of a command, paths, `owner`, `mode`, `sysrc` values and
  the file name of a `source`. The body of a file (`content` or the text of its `source`) is replaced only when the step says
  `template: true`; every other file goes byte for byte, so shell scripts and web server files that are full of `$` and `${` need no care.
- `$${` is a literal `${`. `$$` that is not followed by `{` stays as it is, so `$$` quoting in SQL and `echo $$` in a command are
  unchanged. A `${` that is not a declared variable is an error that names the step, the field and (in a template file) the line.
  A value is put in once: it is not searched for `${` again.
- The value comes from the default, then `--vars FILE`, then `--set NAME=VALUE` or `--set-file NAME=FILE`; the last one wins. A name in a
  `--vars` file that the recipe does not declare is ignored, so one file can serve many recipes; in `--set` it is an error, so is a
  required variable without a value, a variable the recipe never uses, and a value of several lines given with `--set`:
  `--set-file` is the way for a value of several lines (one trailing line break of the file is removed).
- `split: true` cuts the value at white space where the recipe takes a list: a word of `package`, `run` or `unless` that is exactly
  `${name}` becomes one word per piece, and so does a `package:` value that is exactly `${name}`. Anywhere else the variable is one string.
- **A secret** (`secret: true`) has no default, is given only with `--set-file` (never on the command line: `--set` and `--vars` refuse it) and
  may be used **only in the content of a `file` step with `template: true`**: not in a command, a path, a name or a `source`. JailOps puts
  it only where it is meant to go, the file inside the jail: not into a file on the host, a log, an operation record, the audit log, a
  diff or a message. What the service itself writes into its own log is for the recipe to prevent (give a database password to
  `psql` as a variable, not as SQL text).
- `--validate` renders the recipe and checks it with the validator of the daemon, on this machine: it needs no jail name, makes
  no connection and reads and writes nothing on a host. The console does the same with `POST /api/v1/recipes/validate`, which takes a
  rendered recipe and needs no jail. An error names the step and the field (`steps[2].run[1]`), never a value or the content of a file.

**Sensitive requests.** When a secret went into the recipe, the client marks the whole request `sensitive`. The daemon then never
records the content of its files and drops the raw output of the commands of **all** its steps (a `psql -f role.sql` can print the
password): a failed step shows the step and the exit status only, and nothing of the output reaches the result, the error, the
operation record or the audit log. The file in the jail is written to a temporary name in its own directory (exclusive, `0600`,
owned by root), given its final mode and owner and renamed; a failed write removes the temporary file at once, and one left by a
killed process is removed by the next write in that directory.

**Who.** A project operator, as for `exec`, `cp` and `jailops pkg`: a recipe can do nothing they could not do by hand, and the
operation is audited. The jail must run and be a FreeBSD jail (a Linux jail is refused: use `jailops exec` and `jailops cp`).

Examples: [`examples/recipes`](../examples/recipes) (nginx and PostgreSQL), [`examples/pkgops-packaged-service`](../examples/pkgops-packaged-service)
(a package from your own PkgOps repository).

## 3. JailOps-managed files

JailOps writes a few files inside a jail. They fall into three groups; a recipe that changes one of them meets the rule of its group.

**Managed files** (JailOps owns the whole file and writes it at every start of the jail; do not edit them, they are rewritten):

| File | Holds |
|---|---|
| `/etc/rc.conf.local` | the network, host name and settings of the jail; it is read after `rc.conf` and wins over it |
| `/usr/local/etc/profile.d/jailops-env.sh` | `JAILOPS_JAIL`, `JAILOPS_PROJECT`, `JAILOPS_HOST` |
| `/usr/local/etc/profile.d/jailops-prompt.sh` | the prompt (when the host sets one) |

`jailops jail shell-setup NAME` writes the two `profile.d` files ([shell.md](shell.md)).

**Managed sections** (a marked block is JailOps', everything outside it is yours and stays):

| File | Block |
|---|---|
| `/etc/hosts` | `# BEGIN JailOps` to `# END JailOps`: the name of the jail and its addresses |
| `/usr/local/etc/profile` and `/usr/local/etc/bash.bashrc` | a block at the top that sources the files of `profile.d` |
| `/root/.shrc` | at the end, `# BEGIN jailops shell` to `# END jailops shell`: sources the env file, then the prompt file |

**Seeded files** (JailOps writes a starting point, then they are yours):

| File | Seeded |
|---|---|
| `/etc/rc.conf`, `/etc/periodic.conf` | from the image, when the jail is made; change them with `sysrc` steps |
| `/usr/local/etc/pkg/repos/FreeBSD.conf`, `/usr/local/etc/pkg/repos/FreeBSD-base.conf` | from the image: the kernel modules and base repositories are disabled in a jail (the host upgrades the base) |
| `/etc/resolv.conf` | written only while it is missing, empty or still carries the line `# written by JailOps`; remove that line to keep your own |

A recipe sets variables with `sysrc` (which keeps the mode and owner of the file), writes its own files below `/usr/local/etc` and adds
to `profile.d` with a `file` step; it does not rewrite a managed file or a managed section. There is no extra protection for
this: the rules above, the examples of the Recipe Book and the checks of the engine (`file` refuses `/etc` apart from four files and
`sysrc` refuses `rc.conf.local`) are what there is.
