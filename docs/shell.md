# The shell of a jail, and its prompt

A new FreeBSD jail starts with **sh**, FreeBSD's own shell, and a coloured prompt that says where you are: the jail, its
project and the host it runs on, so a shell inside a jail is recognisable at a glance:

    root@web [shop/babel]:/#           root@web light blue (256-colour number 39), "[shop/babel]" grey (245)

`jailops shell web` and the console's Console tab start the jail's shell. Root's interactive sh shows the prompt, and so
does every interactive bash in a jail that has bash (root, users you add, `su`, ssh logins). Nothing has to be edited by
hand for the common cases, and everything can be changed or removed.

## What a new jail gets

When the request (and the jail's profiles) do not say otherwise, the daemon fills the `prompt` key of a new FreeBSD jail
from `jailopsd.yaml`, and the `shell` key when the host's default is bash, so `jailops config show NAME` shows what it
got:

| Key | Default | Meaning |
|---|---|---|
| `shell` | unset (`/bin/sh`) | the preferred shell of `jailops shell` and the console: `sh`, `bash` or a path inside the jail (`jails.shell` of `jailopsd.yaml`, or `project set --shell` for a project's new jails) |
| `prompt` | `default` | the prompt: `default`, `short`, `none`, or a PS1 text |

The prompt is written at every start where it changed, and at once into a running jail when the key changes.

**JailOps never installs a shell.** sh is always there. A jail whose preferred shell is bash gets bash when you install
it in the jail:

    jailops pkg install web -- bash

The next `jailops shell web` starts it, with no restart and no change of the jail; the prompt files are already in
place. Until then `jailops shell` and the console refuse with the reason and both ways out:

    the shell /usr/local/bin/bash is not installed in jail web
    hint: install it with: jailops pkg install web -- bash, or use sh: jailops shell web --shell /bin/sh

No other shell is started in its place. `jailops exec` runs a command and needs no shell. Bash and the prompt files live
below `/usr/local` on the jail's root, and the in-place upgrade keeps them.

When the prompt cannot be written (a start-up file whose JailOps block was cut by hand), the start still succeeds: the
jail shows a `ShellSetup` condition (`jailops jail show NAME`, and a notice on the jail's page in the console), and
`jailops jail shell-setup NAME` writes it again once the file is fixed.

Linux jails are left alone: they keep the shell and prompt of their distribution, and the prompt key is refused for them.

## The files

| File | Owner | What it does |
|---|---|---|
| `/usr/local/etc/profile.d/jailops-env.sh` | JailOps, the same text in every jail | exports `JAILOPS_JAIL`, `JAILOPS_PROJECT` and `JAILOPS_HOST` from the jail's `env` parameters |
| `/usr/local/etc/profile.d/jailops-prompt.sh` | JailOps, rewritten from the `prompt` key | sets `PS1` in an interactive sh or bash, nothing else |
| `/root/.shrc` | you; JailOps keeps a marked block at its end | read by root's interactive sh; the block sources the prompt file |
| `/usr/local/etc/bash.bashrc` | you; JailOps keeps a marked block at its top | read by every interactive bash that is not a login shell; the block sources `profile.d/*.sh` |
| `/usr/local/etc/profile` | you; JailOps keeps a marked block at its top | read by every login bash (and by `sh -l` through `/etc/profile`); the block sources `profile.d/*.sh` where `/etc/profile` does not |

The FreeBSD `bash` package installs neither start-up file; it is built with its `SYSBASHRC` option, so bash reads
`/usr/local/etc/bash.bashrc` and `/usr/local/etc/profile` (not `/etc/profile`). The marked block sources every file of
`/usr/local/etc/profile.d` that ends in `.sh`, in the order of the glob: `jailops-env.sh` and `jailops-prompt.sh`, and what
you or a recipe put there:

    # BEGIN jailops shell: managed by JailOps (jailops jail shell-setup writes it)
    if [ -z "${_loaded:-}" ]; then
    	for _jailops_f in /usr/local/etc/profile.d/*.sh; do
    		if [ -r "$_jailops_f" ]; then . "$_jailops_f"; fi
    	done
    	unset _jailops_f
    fi
    # END jailops shell

FreeBSD's `/etc/profile` reads `/usr/local/etc/profile` and then `/usr/local/etc/profile.d/*.sh` for a login sh and sets
`_loaded` while it does; the `if` makes the block read the directory only where nobody else does, so no file is sourced twice.
With `prompt: none` the prompt file is gone and the variables script stays: the variables are useful without a prompt.

Everything else in those two files is yours and stays as it is. The block is at the top, so your own lines come after it
and win. A block you cut in half by hand is refused (the operation names the file), never guessed at.

The prompt file sets `PS1` only in an interactive shell (`case "$-" in *i*`): scripts, `sh -c`, `bash -c` and `bash -lc`
are not affected. In sh the prompt applies through root's `/root/.shrc`: a login sh reads `~/.profile` first and then
`~/.shrc` (through `ENV`), and FreeBSD's `/root/.shrc` sets `PS1` itself, so JailOps keeps its marked block at the
**end** of that file, where it sources the prompt file last. A `PS1` of your own goes below that block. The other users of
sh keep FreeBSD's prompt.

## Files in profile.d

Any file `/usr/local/etc/profile.d/NAME.sh` is read by the shells below. A recipe writes one with a `file` step
([provisioning.md](provisioning.md#2-applying-a-recipe-to-a-jail)), for example an alias for every user:

    - file: {path: /usr/local/etc/profile.d/50-alias.sh, content: "alias ll='ls -lAh'\n"}

| Shell | Reads `profile.d/*.sh` |
|---|---|
| login `sh` (`sh -l`, a console login, `su -`) | yes, through FreeBSD's `/etc/profile` |
| login `bash` | yes, through the block in `/usr/local/etc/profile` |
| interactive `bash` that is not a login shell (a terminal inside a session, `su` without `-`, ssh without a login shell) | yes, through the block in `/usr/local/etc/bash.bashrc` |
| interactive `sh` that is not a login shell | no: it reads only the file `ENV` names (`~/.shrc`), where JailOps sources the prompt file and nothing else |
| scripts, `sh -c`, `bash -c` | no |

The files are read in the order of the glob, so `jailops-prompt.sh` comes after `10-...` and before `z-...`; name a file that
must change `PS1` after the prompt with a name that sorts later. Aliases and functions only matter in an interactive shell.
A jail made before this release has the old block: `jailops jail shell-setup NAME` writes the new one.

## Where the jail is

The default prompt is `user@jail`, then, in a quiet grey, the **project** and the **host** the jail runs on in square
brackets, then the directory and `#` (root) or `$`:

    root@web [shop/babel]:/usr/local/etc#      a root shell, an ssh login or `jailops shell`
    ann@web [shop/babel]:~$                    a user

`user@jail` is in the colour of `jails.prompt_color` (39, light blue, unless you change it); the tag is grey (245) and
the directory has no colour. The jail's host name is not changed: `\h` is the jail's host name, which is the jail's
name. If the project or the host is not known the tag is `[shop]` or `[babel]`, and without both there is no tag.
The `short` prompt has no tag: `root@web:/usr/local/etc#`.

The tag is not written into the prompt file. Every interactive bash of the jail has three variables, and the prompt
reads them each time it is shown:

| Variable | Is |
|---|---|
| `JAILOPS_JAIL` | the name of the jail |
| `JAILOPS_PROJECT` | the project of the jail |
| `JAILOPS_HOST` | the short name of the host: the first label of its host name, in lower case (`Babel.example.org` is `babel`) |

**Where they come from.** Every start renders three parameters into the jail's `jail.conf`, on the host the jail is
started on: `env.jailops_jail`, `env.jailops_project` and `env.jailops_host` (a value that is not known has no
parameter; the values hold only lower-case letters, digits and hyphens). FreeBSD gives a jail its `env` parameters as the
read-only sysctl `security.jail.env`, lines of `key=value`, which the jail cannot change:

    jls -j web env                       # on the host
    sysctl -n security.jail.env          # in any shell inside the jail
    jailops_jail=web
    jailops_project=shop
    jailops_host=babel

`jailops-env.sh` reads that sysctl once when a bash starts and exports the three variables. It uses only these three
keys and only values of the kind above, so another tool that writes the same buffer cannot set anything else. The
variables are right after a rename (a jail is renamed while it is stopped, and the next start renders the new name) and
after a jail has moved to another host, and nothing in the jail's files changes for either. A shell that is already open
keeps its values until the next login. `sh`, scripts, `bash -c` and `ssh host command` do not read the script and do not
have the variables; they can read the sysctl.

A prompt text of your own may use three words in braces, which become references to the variables:

| Word | Reads |
|---|---|
| `{jail}` | `JAILOPS_JAIL` |
| `{project}` | `JAILOPS_PROJECT` |
| `{host}` | `JAILOPS_HOST` |

A variable that is not known is empty, and the text around it stays as you wrote it. Any other `{` or `}` is refused, and
the message names the three words.

    jailops config set web prompt '\u@{jail} [{project}/{host}]:\w\$ '      one jail:  root@web [shop/babel]:/#
    jailops config set web prompt short                                       no tag:    root@web:/#
    jailops config set web prompt none                                        no prompt: the variables stay

In your own `~/.bashrc` the variables are ordinary variables, so a prompt of your own can use them in any way:

    PS1='\[\e[38;5;39m\]\u@\h\[\e[0m\] ${JAILOPS_HOST-}/${JAILOPS_PROJECT-}:\w\$ '

If you write a prompt yourself: wrap every colour sequence, including the final reset, in `\[` and `\]` so bash measures
the line correctly, and end it with a space so what you type does not touch it.

The colour says one thing: the kind of host. Set `prompt_color` once per host, in `jailopsd.yaml`, red for production and
green for test, and the same text tells hosts apart at a glance. For **every new jail of a host**:

```yaml
jails:
  prompt: default      # default, short, none, or a PS1 text
  prompt_color: 196    # user@jail in red on a production host
```

## Overriding it

**For one user:** set `PS1` in your own start-up file. Bash reads the system files first, so yours wins:

- `~/.bashrc` for the shells `jailops shell` and the console open (interactive, not login shells);
- `~/.bash_profile` (or `~/.profile`) for login shells such as ssh logins. The usual way to have one place for both is a
  `~/.bash_profile` with `. ~/.profile` and `. ~/.bashrc` in it.

**For one jail:**

    jailops config set web prompt '\[\e[38;5;196m\]\u@\h:\w\$\[\e[0m\] '   # red, with a space after # or $
    jailops config set web prompt none       # remove the prompt file; the variables stay
    jailops config set web prompt short      # user@jail:dir without the grey tag
    jailops config set web prompt default    # back to the default
    jailops config set web shell sh              # jailops shell and the console start /bin/sh again

A running jail gets a new prompt at once, a stopped one at its next start. The console's Configuration tab has both keys.
To drop bash itself, set the shell to `sh` and remove the package (`jailops pkg remove web -- bash`).

**For all new jails of a host** (`/usr/local/etc/jailops/jailopsd.yaml`, read at the daemon's start):

```yaml
jails:
  shell: bash          # or sh (the default); the preferred shell, never installed by JailOps
  prompt: default      # default, short, none, or a PS1 text
  prompt_color: 39     # 256-colour number of user@jail (39 light blue, 40 green, 196 red)
```

A jail whose prompt is `default` takes a changed `prompt_color` at its next start; a jail with a PS1 text of its own
keeps it.

`jailops create web --shell sh` or `--prompt none` (and `shell`/`prompt` in a file for `jailops apply` or `jailops-sync`)
choose per jail at creation; a profile may set both keys too.

## Existing jails

A jail whose `shell` and `prompt` keys are empty is left alone: nothing changes on its own, not at a start and not at an
upgrade. Ask for it (it also gives a jail made before the hook of `profile.d` the new block):

    jailops jail shell-setup web

It fills the keys the jail does not set from the host's defaults and writes the prompt; it prints the `PS1` it wrote and
what it did, and names a preferred shell that is not installed with the command that installs it. It installs nothing.
Running it again repairs the files. The jail must run. In the console, a jail whose preferred shell is not installed
says how to install it on its Console tab and can open sh for the session (**Open sh**).

## What a prompt may contain

The prompt is written into a file that bash reads as shell code, and bash expands variables and commands in `PS1` each
time it shows the prompt. JailOps therefore accepts an allow-list only (`api.CheckPrompt`, used by the API, the CLI and
`jailopsd.yaml`):

- at most 256 bytes of printable UTF-8 on one line;
- a `{` only as the start of `{jail}`, `{project}` or `{host}` and a `}` only as the end of one (see [Where the jail is](#where-the-jail-is)), or inside `\D{...}`;
- no `'` and no `` ` ``; a `$` only as the escape `\$` (`#` for root, `$` for everyone else);
- a backslash only before one of bash's prompt escapes: `\u \h \H \w \W \$ \# \! \j \l \s \t \T \@ \A \d \D{...} \n \r
  \a \e \[ \]` and the octal escapes `\033` (escape) and `\007` (bell); colours are `\[\e[...m\]`;
- refused, because bash would turn them into a character it then expands: `\\` (a backslash), and every other octal
  escape (`\044` is a `$`, `\140` a backquote).
The accepted text is then written as one single-quoted assignment, `PS1='...'`. A prompt that runs a command (a git
branch, the exit status) belongs in your own `~/.bashrc`.

## The environment of jailops shell

`jailops shell` and the console run the shell through `jexec` without a login: bash starts as an interactive shell that
is not a login shell (it reads `bash.bashrc` and `~/.bashrc`), in `/`, as root. The session gets
`PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin` (the path of a login, so what packages install is
found) and `HOME=/root`, and root's sh also `ENV=/root/.shrc`, which an interactive sh reads (so the prompt shows as in a
login); the jail's own `env.*` keys and what the caller sends come after and win. From a terminal,
`jailops shell` and `jailops exec -t` pass the terminal's `TERM`, so bash edits lines as it does in a login (without it,
bash assumes a dumb terminal). The console's terminal understands a small part of what a terminal does and sends no
`TERM`; bash then works line by line, and the coloured prompt shows (the console draws the 256 colours).

The login shell of root in `/etc/passwd` is not changed: `su -` and a console login keep `/bin/sh`. Change it in the jail
with `chsh -s /usr/local/bin/bash` if you want bash there too.

## The host

The green prompt on the host is the same mechanism, written once by hand (JailOps never edits the host's shell files):

    pkg install bash
    mkdir -p /usr/local/etc/profile.d
    printf '%s\n' 'if [ -n "${BASH_VERSION-}" ] && [ -n "${PS1-}" ]; then' \
      "    PS1='\\[\\e[38;5;40m\\]\\u@\\h:\\w\\\$\\[\\e[0m\\] '" 'fi' > /usr/local/etc/profile.d/host-prompt.sh
    for f in /usr/local/etc/profile /usr/local/etc/bash.bashrc; do
      printf '%s\n' '. /usr/local/etc/profile.d/host-prompt.sh' >> "$f"
    done

`chsh -s /usr/local/bin/bash` makes bash your login shell. Your own `~/.bashrc` wins here as in a jail.
[appliance.md](appliance.md#the-shell-prompt-of-the-host-not-a-module) says why this is not a host management module.

## bash instead of sh

`jailops project set shop --shell bash` makes bash the preferred shell of the new FreeBSD jails of the project (`--shell
sh` goes back); `jailops create --shell bash` and `jailops config set web shell bash` do it for one jail, and `jails.shell`
in `jailopsd.yaml` for the host. Install bash in each such jail (`jailops pkg install web -- bash`); JailOps never does.
