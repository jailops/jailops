# The audit of a jail

`jailops jail audit NAME` looks at a FreeBSD jail and prints one line per check: `PASS`, `INFO`, `WARN` or `CRITICAL`,
what it found and, below the table, the fix of each finding. It changes nothing and works on a running jail: the root
helper reads the jail's root from the host, and in a running jail `sockstat` lists the listening sockets. A stopped
jail's root is mounted for the read and unmounted after it; the jail is never started or stopped.

    jailops jail audit web
    jailops jail audit web -o json

The exit status is 0 when no check is `CRITICAL` (`WARN` and `INFO` included) and 1 when one is, so a script can act
on it. The route is `GET /api/v1/jails/{name}/audit` (extension `jail_audit`), `c.Jails().Audit` in the SDK; it needs
the operator role in the project, never readonly, because the helper reads the tenant's files as root of the host
(`/etc/master.passwd` among them) and the jail runs `sockstat`, as `exec` would. Each audit is written to the audit log.

## What it checks

The checks come in two groups. Only a security check is ever `CRITICAL`, and only for what crosses the boundary of the
jail or opens a second way to root in it; the tenant's own configuration is at most a `WARN`.

| Check | Group | Finding | Result |
|---|---|---|---|
| `root.links` | security | a link out of the root on a path the base's packages own, a link among pkg's own files (`/var/db/pkg`, `/var/cache/pkg`, the repository directories), a `pkg.conf` that turns plugins on: what an attack on the host's pkg plants, the same detection an upgrade makes before it starts ([thick-jail-upgrades.md](thick-jail-upgrades.md#a-jail-jailops-does-not-trust)). A link the jail makes for itself (`/etc/localtime` from tzsetup, `/var/www`) is fine | CRITICAL |
| `accounts.uid0` | security | an account with uid 0 besides `root` and `toor` | CRITICAL |
| `accounts.passwords` | security | `root` or `toor` not locked, an account with an empty password. An empty password of root lets a member of `wheel` become root with `su` without one (`pam_group` limits `su` to wheel; sshd refuses empty passwords). A jail made before new images locked root has it: `pw usermod root -w no` in the jail | WARN |
| `files.setid` | security | a set-uid or set-gid file that no installed package owns (a package of the ports such as `sudo` is a package's) | WARN |
| `pkg.repositories` | security | an enabled repository that is not FreeBSD's (`pkg.FreeBSD.org`) or the host's JailOps repository | WARN |
| `pkg.base_repository` | security | `FreeBSD-base` enabled inside the jail: pkg there takes base packages it cannot install ([FreeBSD bug 293817](https://bugs.freebsd.org/bugzilla/show_bug.cgi?id=293817)), and the host upgrades the base | WARN |
| `base.checksums` | security | a program or library of the base (`/bin`, `/sbin`, `/lib`, `/libexec`, `/usr/bin`, `/usr/sbin`, `/usr/lib`, `/usr/libexec`) whose content differs from what its package recorded, as `pkg check -s` compares; configuration is not compared. A file larger than 256 MiB (a sparse one too) is listed without being read, and one audit reads at most 4 GiB (the rest is counted as not compared) | WARN |
| `sshd.config` | security | sshd installed with `PermitRootLogin yes`, `PasswordAuthentication yes`, or keyboard-interactive through PAM, which takes a password and which FreeBSD's sshd has on unless `/etc/ssh/sshd_config` says `KbdInteractiveAuthentication no` or `UsePAM no` | WARN |
| `periodic.world_kernel` | practice | `daily_status_world_kernel` is not `NO`: the daily report compares the userland with the kernel, which is the host's | INFO |
| `periodic.output` | practice | periodic mails its reports to root instead of writing them to `/var/log` | INFO |
| `cron.jitter` | practice | `cron_flags` without `-J`: the jobs of root, periodic among them, start at the same second in every jail of the host | INFO |
| `rc.conf` | practice | knobs of `rc.conf` that do nothing in a jail (their `rc.d` script carries the `nojail` keyword, or the kernel is the host's: `ntpd_enable`, `devd_enable`, `kld_list`, `dumpdev`, `powerd_enable`, ...), and `sendmail_enable="YES"` | INFO |
| `shell.prompt` | practice | root's shell is not the one of the jail's spec, or `/root/.shrc` does not read the JailOps prompt ([shell.md](shell.md)) | INFO |
| `hosts.block` | practice | the JailOps block of `/etc/hosts` does not resolve the jail's name to its addresses | INFO |
| `volumes.nosuid` | practice | a nullfs mount in the running jail without `nosuid` (JailOps mounts its volumes with it) | WARN |
| `disk.limit` | practice | the jail has no `resources.disk` | INFO |
| `disk.size` | practice | the space the root takes and the number of packages, next to the minimal set | INFO |
| `ports.listening` | practice | a running jail listens beyond loopback on a port it does not expose | INFO |

`INFO` is a note: worth knowing, nothing is wrong. A jail of a current image starts with what the practice checks look
for: `cron_flags="-J 60"`, the reports of periodic in `/var/log`, `daily_status_world_kernel="NO"`, root locked,
`FreeBSD-base` disabled and the hosts block.

## How it reads the jail

The daemon copies the jail's package database to a scratch directory of the jail's container and asks pkg there for
the files of every installed package and their checksums (`pkg -r SCRATCH query -a`); pkg never reads the jail's own
files as root of the host. The root helper then reads the root (`jail.audit_root`, a read-only operation): it opens
every file through the jail's root, never follows a link as the last component, and returns no password hash, only
which accounts have uid 0, which are locked and root's shell. The walk for set-id files stays on the root's file
system, so a volume is not walked, and it stops after two million entries (the check says so). A running jail can
change its root while it is read; the audit is a look at one moment, the same as the check before an upgrade.

## In the doctor, the event stream and the daily report

- `jailops doctor` has the check `jail.audit`: for each jail whose last audit since the daemon started found
  something, `fail` with a critical finding and `warn` with a warning, naming the checks. A jail that was not audited
  is not named; the audit runs when someone asks for it.
- An audit with a critical finding sends the lifecycle event `jail-audit-critical` (`jailops events`).
- For the daily report of periodic(8), a script in `/usr/local/etc/periodic/daily/` audits every jail of a project
  and prints the ones with a critical finding (periodic mails what a daily script prints, or writes it to
  `daily_output`, see [automating-updates.md](automating-updates.md#14-mail-logs-and-the-records)):

      #!/bin/sh
      # /usr/local/etc/periodic/daily/460.jailops-audit
      rc=0
      for j in $(jailops list --project shop -o name); do
          jailops jail audit "$j" --project shop >/dev/null 2>&1
          case $? in
          0) ;;
          1) echo "CRITICAL: jail shop/$j: jailops jail audit $j --project shop"; rc=1 ;;
          *) echo "jail shop/$j was not audited (a Linux jail, busy, or an error): jailops jail audit $j --project shop" ;;
          esac
      done
      exit $rc

  Exit status 1 is a completed audit with a critical finding; any other non-zero status is an audit that did not run
  (a Linux jail is refused, a jail another operation holds is `busy`), which the script prints as a plain line.

## What it does not do

It changes nothing: every fix is a command for you to run. It does not compare a jail with what it was when it was made
(there is no baseline). `files.setid` and `base.checksums` compare with the jail's own package database, which root in
the jail can change: a compromised jail can hide from these two checks, so they are hygiene, not evidence. It reads `sshd_config` up to its first `Match` block and not the files it
includes, and a Linux jail is refused (`invalid`).
