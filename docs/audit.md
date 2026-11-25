# The audit of a jail

`jailops jail audit NAME` looks at a FreeBSD jail and prints one line per check: `PASS`, `WARN` or `CRITICAL`, what
it found and, below the table, the fix of each finding. It changes nothing and nothing runs in the jail: the root
helper reads the jail's root from the host, and pkg reads a copy of its package database. A stopped jail's root is
mounted for the read and unmounted after it; the jail is never started or stopped.

    jailops jail audit web
    jailops jail audit web --suggest
    jailops jail audit web -o json

The exit status is 0 when no check is `CRITICAL` (`WARN` included) and 1 when one is, so a script can act on it. The
route is `GET /api/v1/jails/{name}/audit` (extension `jail_audit`; `?suggest=true` for the suggestions),
`c.Jails().Audit` and `c.Jails().AuditWith` in the SDK; it needs the operator role in the project, never readonly,
because the helper reads the tenant's files as root of the host (`/etc/master.passwd` among them). Each audit is
written to the audit log.

## What it checks

By default the audit reports what it reads from the jail and what FreeBSD's own documentation names as practice:
the [FreeBSD Handbook](https://docs.freebsd.org/en/books/handbook/) (the chapters Jails and Security) and the
[CIS FreeBSD Benchmark](https://www.cisecurity.org/benchmark/freebsd). Each check is `PASS`, `WARN` or `CRITICAL`;
`INFO` in this list says a check could not be made, and why. Only what crosses the boundary of the jail or opens a
second way to root in it is ever `CRITICAL`.

| Check | Finding | Source | Result |
|---|---|---|---|
| `root.links` | a link out of the root on a path the base's packages own, a link among pkg's own files (`/var/db/pkg`, `/var/cache/pkg`, the repository directories), a `pkg.conf` that turns plugins on: what an attack on the host's pkg plants, the same detection an upgrade makes before it starts ([thick-jail-upgrades.md](thick-jail-upgrades.md#a-jail-jailops-does-not-trust)). A link the jail makes for itself (`/etc/localtime` from tzsetup, `/var/www`) is fine | the jail's root | CRITICAL |
| `accounts.uid0` | an account with uid 0 besides `root` and `toor` | Handbook, Security | CRITICAL |
| `accounts.passwords` | an account with an empty password, `root` and `toor` included. An empty password of root lets a member of `wheel` become root with `su` without one | CIS: no empty passwords | WARN |
| `files.setid` | a set-uid or set-gid file that no installed package owns (a package of the ports such as `sudo` is a package's) | Handbook, Security | WARN |
| `files.sticky_bit` | a directory anyone may write to without the sticky bit | CIS 6.3: world-writable directories have the sticky bit | WARN |
| `files.world_writable` | a regular file anyone may write to, outside a directory with the sticky bit that anyone may write to (`/tmp`, `/var/tmp`) | CIS 6.4: no world-writable files | WARN |
| `files.unowned` | a file or directory whose owner is no account of the jail's `/etc/master.passwd`, or whose group is not in its `/etc/group`: an account made later with that id owns it | CIS 6.7: no unowned files or directories | WARN |
| `pkg.repositories` | an enabled package repository that is neither signed (`signature_type: pubkey` or `fingerprints`) nor fetched over https: a repository you added is fine when pkg can verify what it downloads | the jail's pkg configuration | WARN |
| `pkg.base_repository` | `FreeBSD-base` enabled inside the jail: pkg there takes base packages it cannot install ([FreeBSD bug 293817](https://bugs.freebsd.org/bugzilla/show_bug.cgi?id=293817)), and the host upgrades the base | the jail's pkg configuration | WARN |
| `pkg.kmods_repository` | `FreeBSD-ports-kmods` enabled inside the jail: kernel modules belong to the host's kernel, so the repository is of no use there and costs a catalogue fetch at every `pkg update` | the jail's pkg configuration | WARN |
| `pkg.vulnerabilities` | a package with a known vulnerability: the name, the version and the first five advisory ids (CVE) of each | pkg-audit(8), Handbook, Security | WARN |
| `base.updates` | newer base packages the last update check of the jail found (`jailops update check`, the same dry run as `jailops jail upgrade --dry-run`); `INFO` when no check ran or it did not look at the base | Handbook, Updating | WARN |
| `base.checksums` | a program or library of the base (`/bin`, `/sbin`, `/lib`, `/libexec`, `/usr/bin`, `/usr/sbin`, `/usr/lib`, `/usr/libexec`) whose content differs from what its package recorded, as `pkg check -s` compares; configuration is not compared. A file larger than 256 MiB (a sparse one too) is listed without being read, and one audit reads at most 4 GiB (the rest is counted as not compared) | pkg-check(8) | WARN |
| `sshd.config` | sshd installed with `PermitRootLogin yes`, `PasswordAuthentication yes`, or keyboard-interactive through PAM, which takes a password and which FreeBSD's sshd has on unless `/etc/ssh/sshd_config` says `KbdInteractiveAuthentication no` or `UsePAM no` | CIS: sshd root login | WARN |
| `jail.enforce_statfs` | `enforce_statfs` below 2: the jail sees mount points beyond its root. `zfs_delegation` sets 1, which the jail's own dataset needs | Handbook, Jails; jail(8) | WARN |

The jail parameters come from the `jail.conf` JailOps renders for the jail, which is what jail(8) started it with.

### pkg audit with the host's vulnerability database

`pkg.vulnerabilities` runs `pkg -r SCRATCH audit -f /var/db/pkg/vuln.xml` on the copy of the jail's package
database: the packages of the jail against the vulnerability database of the host. The audit never fetches the
database. The host's daily periodic run refreshes it when `security_status_pkgaudit_enable` is on (pkg's default);
`pkg audit -F` on the host does it at once. Without the file the check is `INFO` (not checked), and with a
file older than seven days it says the database is not current.

## What `--suggest` adds

`--suggest` (`suggest=true` in the API) adds the suggestions: how a jail runs better on JailOps, or a setting worth a
second look. Each is `PASS` or `INFO`, marked `suggestion` (in the table its detail starts with `suggestion:`), and
never counts as a finding: not in the numbers of the summary, not in the exit status, not in the doctor.

| Check | Note |
|---|---|
| `accounts.root_locked` | `root` or `toor` takes a password; `jexec` and `jailops shell` need none, and the images of JailOps lock root |
| `periodic.world_kernel` | `daily_status_world_kernel` is not `NO`: the daily report compares the userland with the kernel, which is the host's |
| `periodic.output` | periodic mails its reports to root instead of writing them to `/var/log` |
| `cron.jitter` | `cron_flags` without `-J`: the jobs of root, periodic among them, start at the same second in every jail of the host |
| `rc.conf` | knobs of `rc.conf` that do nothing in a jail (their `rc.d` script carries the `nojail` keyword, or the kernel is the host's: `ntpd_enable`, `devd_enable`, `kld_list`, `dumpdev`, `powerd_enable`, ...), and `sendmail_enable="YES"` |
| `shell.prompt` | root's shell is not the one of the jail's spec, or `/root/.shrc` does not read the JailOps prompt ([shell.md](shell.md)) |
| `hosts.block` | the JailOps block of `/etc/hosts` does not resolve the jail's name to its addresses |
| `volumes.nosuid` | a nullfs mount in the running jail without `nosuid` (JailOps mounts its volumes with it) |
| `disk.limit` | the jail has no `resources.disk` |
| `disk.size` | the space the root takes and the number of packages, next to the minimal set |
| `jail.securelevel` | the jail's `securelevel` is below 2, the hardened setting of security(7) |
| `jail.allow` | the `allow.*` parameters the jail has |
| `jail.devfs_ruleset` | a devfs ruleset other than the stock ruleset of jails (4) and the one JailOps makes for the jail's devices |

A jail of a current image starts with what the suggestions look for: `cron_flags="-J 60"`, the reports of periodic in
`/var/log`, `daily_status_world_kernel="NO"`, root locked and the hosts block. A fix names the file and the line
(`/etc/rc.conf`: `cron_flags="-J 60"`) or the command, such as `jailops exec web -- service cron restart`.

## How it reads the jail

The daemon copies the jail's package database to a scratch directory of the jail's container and asks pkg there for
the files of every installed package and their checksums (`pkg -r SCRATCH query -a`) and for the vulnerable packages
(`pkg -r SCRATCH audit`); pkg never reads the jail's own files as root of the host. The root helper then reads the
root (`jail.audit_root`, a read-only operation): it opens every file through the jail's root, never follows a link as
the last component, and returns no password hash, only which accounts have uid 0, which are locked and root's shell.
One walk of the root finds the set-id, world-writable and unowned files; it stays on the root's file system, so a
volume is not walked, and it stops after two million entries (the checks say so). A running jail can change its root
while it is read; the audit is a look at one moment, the same as the check before an upgrade.

## In the doctor, the event stream and the daily report

- `jailops doctor` has the check `jail.audit`: for each jail whose last audit since the daemon started found
  something, `fail` with a critical finding and `warn` with a warning, naming the checks. A jail that was not audited
  is not named; the audit runs when someone asks for it. A suggestion never counts.
- An audit with a critical finding sends the lifecycle event `jail-audit-critical` (`jailops events`).
- For the daily report of periodic(8), a script in `/usr/local/etc/periodic/daily/` audits every jail of a project
  and prints the ones with a critical finding (periodic mails what a daily script prints, or writes it to
  `daily_output`, see [automating-updates.md](automating-updates.md#14-mail-logs-and-the-records)). It runs after the
  host's own security run (`450.status-security`), which refreshes the vulnerability database the audit uses:

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
  (a Linux jail is refused, a jail another operation holds is `busy`), which the script prints as a plain line. A
  warning, a vulnerable package among them, does not change the exit status: `jailops doctor` names the jail.

## What it does not do

It changes nothing: every fix is a command for you to run. It does not compare a jail with what it was when it was made
(there is no baseline), and it says nothing of the firewall: which ports reach the jail is the host's pf
([networking.md](networking.md)). `files.setid`, `base.checksums` and `pkg.vulnerabilities` read the jail's own package
database, which root in the jail can change: a compromised jail can hide from these checks, so they are hygiene, not
evidence. It reads `sshd_config` up to its first `Match` block and not the files it includes, and a Linux jail is
refused (`invalid`).
