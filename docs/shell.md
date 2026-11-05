# The shell of a jail, and its prompt

A new FreeBSD jail comes with **bash** and a coloured prompt, so a shell inside a jail is recognisable at a glance:

    root@web:/#            (light blue: 256-colour number 39)

`jailops shell web` and the console's Console tab start bash; every interactive bash in the jail (root, users you add,
`su`, ssh logins) shows the prompt. Nothing has to be edited by hand for the common cases, and everything can be
changed or removed.

## What a new jail gets

When the request (and the jail's profiles) do not say otherwise, the daemon fills two keys of a new FreeBSD jail from
`jailopsd.yaml`, so `jailops config show NAME` shows what it got:

| Key | Default | Meaning |
|---|---|---|
| `shell` | `/usr/local/bin/bash` | the shell of `jailops shell` and the console: `bash`, `sh` or a path inside the jail |
| `prompt` | `default` | the bash prompt of every user: `default`, `none`, or a PS1 text |

At the jail's **first start** JailOps installs the `bash` package into the jail from the host (`pkg -r ROOT install -y
bash`, the same command an app installation uses) and writes the prompt. Both live below `/usr/local`, a persistent
dataset of a thin jail, so they survive an OS update (the root swap of `jailops jail upgrade` and of the nightly image
upgrade); a thick jail is upgraded in place. App installations do the same right after the app's packages.

If the host cannot reach its package repository, the start still succeeds: the jail shows a `ShellSetup` condition
(`jailops jail show NAME`, and a notice on the jail's page in the console), `jailops shell` says how to repair it, and
`jailops jail shell-setup NAME` tries again. It is not retried at every start.

Linux jails are left alone: they keep the shell and prompt of their distribution, and the prompt key is refused for them.

## The files

| File | Owner | What it does |
|---|---|---|
| `/usr/local/etc/profile.d/jailops-prompt.sh` | JailOps, rewritten from the `prompt` key | sets `PS1` in an interactive bash, nothing else |
| `/usr/local/etc/bash.bashrc` | you; JailOps keeps a marked block at its top | read by every interactive bash that is not a login shell |
| `/usr/local/etc/profile` | you; JailOps keeps a marked block at its top | read by every login bash (and by `sh -l` through `/etc/profile`) |

The FreeBSD `bash` package installs neither start-up file; it is built with its `SYSBASHRC` option, so bash reads
`/usr/local/etc/bash.bashrc` and `/usr/local/etc/profile` (not `/etc/profile`). The marked block is three lines:

    # BEGIN jailops prompt: managed by JailOps (jailops config set JAIL prompt none removes it)
    if [ -r /usr/local/etc/profile.d/jailops-prompt.sh ]; then . /usr/local/etc/profile.d/jailops-prompt.sh; fi
    # END jailops prompt

Everything else in those two files is yours and stays as it is. The block is at the top, so your own lines come after it
and win. A block you cut in half by hand is refused (the operation names the file), never guessed at.

The prompt file sets `PS1` only when bash runs interactively (`PS1` is set): scripts, `bash -c` and `bash -lc` are not
affected, and `sh` ignores it (it keeps FreeBSD's own prompt from `/root/.shrc`). Verified on FreeBSD 15.2 with bash
5.3: an interactive bash and an interactive login bash have the prompt, `bash -c` and `bash -lc` have no `PS1`, a login
`sh` keeps `\u@\h:\w \$ `.

## Overriding it

**For one user:** set `PS1` in your own start-up file. Bash reads the system files first, so yours wins:

- `~/.bashrc` for the shells `jailops shell` and the console open (interactive, not login shells);
- `~/.bash_profile` (or `~/.profile`) for login shells such as ssh logins. The usual way to have one place for both is a
  `~/.bash_profile` with `. ~/.profile` and `. ~/.bashrc` in it.

**For one jail:**

    jailops config set web prompt '\[\e[38;5;196m\]\u@\h:\w\$\[\e[0m\] '   # red, with a space after # or $
    jailops config set web prompt none       # remove the prompt file and the two blocks
    jailops config set web prompt default    # back to the default
    jailops set web shell sh                 # jailops shell and the console start /bin/sh again

A running jail gets a new prompt at once, a stopped one at its next start. The console's Configuration tab has both keys.
To drop bash itself, set the shell to `sh` and remove the package by hand (`pkg -r` from the host, or `pkg delete bash`
in the jail if it has `pkg`); JailOps does not install it again on its own.

**For all new jails of a host** (`/usr/local/etc/jailops/jailopsd.yaml`, read at the daemon's start):

```yaml
jails:
  shell: bash          # or sh: nothing is installed, and the prompt key stays empty
  prompt: default      # default, none, or a PS1 text
  prompt_color: 39     # 256-colour number of the default prompt (39 light blue, 40 green, 196 red)
```

A jail whose prompt is `default` takes a changed `prompt_color` at its next start; a jail with a PS1 text of its own
keeps it.

`jailops create web --shell sh` or `--prompt none` (and `shell`/`prompt` in a file for `jailops apply` or `jailops-sync`)
choose per jail at creation; a profile may set both keys too.

## Existing jails

A jail created before this version has neither key set, and empty means "leave the files alone": nothing changes on its
own, not at a start and not at an upgrade. Ask for it:

    jailops jail shell-setup web

It fills the keys the jail does not set from the host's defaults, installs bash when it is the shell and missing, and
writes the prompt; it prints the `PS1` it wrote and what it did. Running it again repairs the files and installs
nothing. The jail must run. In the console, a jail whose shell is bash but has no bash offers the same on its Console
tab (**Install bash and the prompt**).

## What a prompt may contain

The prompt is written into a file that bash reads as shell code, and bash expands variables and commands in `PS1` each
time it shows the prompt. JailOps therefore accepts an allow-list only (`api.CheckPrompt`, used by the API, the CLI and
`jailopsd.yaml`):

- at most 256 bytes of printable UTF-8 on one line;
- no `'` and no `` ` ``; a `$` only as the escape `\$` (`#` for root, `$` for everyone else);
- a backslash only before one of bash's prompt escapes: `\u \h \H \w \W \$ \# \! \j \l \s \t \T \@ \A \d \D{...} \n \r
  \a \e \[ \]` and the octal escapes `\033` (escape) and `\007` (bell); colours are `\[\e[...m\]`;
- refused, because bash would turn them into a character it then expands: `\\` (a backslash), and every other octal
  escape (`\044` is a `$`, `\140` a backquote). The tests run the refused forms through a real bash to show that they would
  run a command, and the accepted ones to show that they do not.

The accepted text is then written as one single-quoted assignment, `PS1='...'`. A prompt that runs a command (a git
branch, the exit status) belongs in your own `~/.bashrc`.

## The environment of jailops shell

`jailops shell` and the console run the shell through `jexec` without a login: bash starts as an interactive shell that
is not a login shell (it reads `bash.bashrc` and `~/.bashrc`), in `/`, as root. The session gets
`PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin` (the path of a login, so what packages install is
found) and `HOME=/root`; the jail's own `env.*` keys and what the caller sends come after and win. From a terminal,
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
[appliance.md](appliance.md#the-shell-prompt-of-the-host) says why this is not a host management module.
