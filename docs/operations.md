# Operations: runbooks and monitoring

What to do when something goes wrong, written from what the product does today. Every command here exists in
`jailops --help`, `jailops-priv` or `jailops-backupd`; what is not built is said plainly. Run the `jailops` commands as
root on the host (root on the unix socket is a global administrator) or with an identity that has the role named.

Two questions are kept apart in every runbook: **taking access away** (a credential stops working at once) and
**getting data back** (from snapshots, the database copies, the backup node). The first never needs the second.

## Lost administrator access

Root on the host is the recovery access, always; it is not an identity of the database and cannot be locked out by it.

    sudo jailops user list                                # who exists, who is disabled
    sudo jailops user set-password admin                  # a lost password; the user's sessions end
    sudo jailops user set-role admin --role admin --global
    sudo jailops user enable admin
    sudo jailops user create admin2 --admin --password-prompt   # a new global administrator

A lost second factor: sign in with a recovery code. Without one, root creates a new administrator as above and deletes
the old user (`jailops user delete NAME`); there is no command that removes another user's second factor.

`jailops-priv setup-token` (as root) prints a one-time token for the console's first-run setup, and only while **no**
global administrator exists (a user, or a token or certificate that has not expired). Once one exists the setup routes
answer `not_found`: use `jailops user create` as root instead. No root on the host means no recovery from JailOps;
that is the boundary ([security-model.md](security-model.md#the-first-administrator-the-setup-token)).

## A stolen API token, client certificate or session

Take the credential away first; it stops working on its next request, and long requests (event streams, console and exec
sessions) end at their next keepalive.

    jailops token list && jailops token delete NAME            # an API token
    jailops trust revoke NAME|FINGERPRINT                      # a client certificate (also: jailops cert delete)
    jailops sessions list && jailops sessions revoke ID        # a console session
    jailops user disable NAME                                  # every session and login of a user, until enable

Then find out what it did. The audit log names the requestor (a user, a token's name or a certificate's fingerprint):

    jailops audit --user NAME --since 72h --limit 1000

Entries older than the newest 1000 can be pruned by retention (`audit.retention`, default 90 days); forward the audit log
off the host ([logging.md](logging.md)) so that it outlives the host. Make the replacement narrower than the one that
leaked: `jailops token create NAME --role ROLE --projects P --expires 720h --allow-from 192.0.2.0/24`.

A stolen client certificate of the **backup node** (`backup-client.key`): on the node, `jailops-backupd hosts revoke NAME`,
then enrol a new certificate ([backup-restore.md](backup-restore.md#2-enrol-the-host)).

## A daemon you suspect is compromised

**What still holds** while `jailopsd` (the user `jailops`) is in someone else's hands: the helper checks every request
against `priv.conf`, so no path outside the storage roots, no program outside the allow-list, no host mount the operator
did not name, no raw `pf.conf` text, no private WireGuard key and no deadline of a pending firewall change
([security-model.md](security-model.md#two-programs-one-boundary)). **What does not:** the attacker can do anything an API
administrator can do within those bounds, and holds everything the daemon holds.

1. Stop it and keep the evidence: `service jailopsd stop`, then copy `/var/db/jailops` and `/var/log/messages` (the
   helper's audit lines, `priv.request`, record every operation the daemon asked for). Running jails keep running.
2. Rotate what the daemon held. All of it is in `data_dir` (default `/var/db/jailops`) or was seen while it ran:

| Secret | Where | What to do |
|---|---|---|
| API tokens, passwords, sessions | the database, hashed | delete and reissue the tokens; `jailops user set-password` for every user; sessions end with it |
| Second factors | sealed with `seal.key` | each user removes the second factor and sets it up again in the console |
| The server certificate's key (self-signed) | `tls/server.{crt,key}` | remove both and restart: a new one is generated; every client pins the new fingerprint, and a hub's hosts trust its new client certificate |
| ACME account and certificate keys | `tls/acme` | remove `tls/acme/account.key` with the daemon stopped and request again (`jailops host certificate acme`); the certificate key is new at every order. JailOps cannot revoke the old certificate at the CA |
| Sealed secrets of jails (`sealed:age:`) | `secrets.key` opens them | treat every sealed value as read: change it where it is used. There is no command to replace `secrets.key` and seal again |
| Passphrases of encrypted volumes | never stored, but typed through the daemon | change those typed while it was compromised: `jailops jail passphrase NAME` |
| WireGuard keys | in the helper's `wireguard-dir`, never in the daemon | not exposed; a join token shown while compromised held a peer's key: remove that peer (`jailops wireguard peer remove SERVER PEER`) and enrol it again. `jailops wireguard rotate-keys NAME` gives a tunnel a new key pair |
| The backup key | `backup.key`, readable by the daemon | assume the backups can be read by whoever reaches the node. Rotation is a new repository; see below |
| The backup node client key | `backup-client.key` | revoke and enrol a new one on the node |
| Log forwarding tokens | `logging.secrets_dir` | the daemon reads them: issue new ones at the receivers |

3. Look for what it changed: `jailops audit --since 168h`, the helper's log lines, `jailops doctor` (drift of the host
   modules), `jailops host snapshots`. Reinstalling the package does not clean a compromised database; the jails' own
   data is outside the daemon's reach only as far as the helper's rules go.

## A lost backup key

The host key is `backup.key` (default under `/var/db/jailops`); the recovery key `JOR1-...` was printed once by
`jailops backup key create`; the escrow is the key wrapped to your age recipients and stored on the node
(`jailops backup key escrow`). `jailops backup key status` tells which of them this host has.

- **The key file is gone, the recovery key or an escrow identity is kept:** the backups are not lost, but **loading a
  recovery key or an escrowed key into a host is not built**: the library has it, no API route or command calls it
  ([backup-restore.md](backup-restore.md#7-restore)). Until it is, the only working path is a copy of `backup.key` (and
  its `backup.passphrase_file`, if the key is wrapped) kept with your own tools. Keep one.
- **The key file, the recovery key and every escrow identity are gone:** the backups cannot be read by anyone, JailOps
  included. Start again: a new key needs a new repository, because `jailops backup key create` refuses to replace a key
  and a node binds itself to one key domain.
- **Rotating the repository key** (after a compromise): not built in place. The way is a new repository (a node set up
  again, or another node) with a new key; the old repository stays readable with the old key until its backups expire or
  are deleted on the node.

## Restoring onto a new host

UNVERIFIED end to end: no restore onto a second machine has been run by the test suite. What the code does:

1. Install the packages and run `jailops-priv init` ([install.md](install.md)); start both services.
2. Put back what is not in a JailOps backup, from your own copies: `backup.key` (and its passphrase file),
   `secrets.key` (sealed secrets), the backup client certificate or a new one enrolled with `jailops-backupd hosts enroll`.
3. Point `backup:` of `jailopsd.yaml` at the same node and restart `jailopsd`. With an empty catalog and the key present,
   the daemon rebuilds its catalog from the repository at start.
4. Create the projects, networks and storages again (`jailops-sync` from your repository, if you keep one); a restore is
   admitted like a new jail and needs them granted.
5. Restore each jail; the preflight lists what is missing (an image generation is rebuilt from pkgbase):

       jailops backup list --project shop
       jailops backup restore --project shop BACKUP --as web --dry-run
       jailops backup restore --project shop BACKUP --as web -y

## A firewall change that locks you out

Only with the host firewall module ([appliance.md](appliance.md#commit-confirmed-built-firewall-now-network-later)).
Every change waits for a confirmation, and the **helper**, not the daemon, puts the old `pf.conf` back when the deadline
passes. A change that would shut out the address the request came from is refused before it is made.

- **Locked out, change unconfirmed:** wait for the deadline (default 120 seconds). The helper reverts and reloads pf
  without anything reaching the host. `jailops host pending` shows the outcome afterwards (`last`: `timed_out`).
- **Still connected and it looks wrong:** `jailops host revert` puts it back now; `jailops host confirm` keeps it.
- **The host reboots before the deadline:** the boot script `jailops_revert` puts the old files back before the network
  and pf start (`reverted_at_boot`).
- **The helper is down while a change waits:** its timer went with it. Starting it (`service jailops_priv start`) reads
  the pending record first and reverts at once when the deadline has passed. If it does not start, as root at the
  console: `jailops-priv revert-pending` (it puts the files back and does not reload) and then `service pf reload`.
- **The revert itself failed** (`last.outcome` is `revert_failed`, with the snapshot in `detail`): from the console as
  root, `jailops host snapshots` and `jailops host restore SNAPSHOT`.
- **A confirmed change locks you out later** (your address changed): nothing reverts it. From the console as root,
  `jailops host restore` (the latest snapshot) or edit `/etc/pf.conf` and `service pf reload`.

## Upgrading, and going back one version

    bectl create pre-jailops-upgrade                 # a boot environment of the root file system
    pkg upgrade jailops jailops-client
    service jailops_priv restart && service jailopsd restart
    jailops doctor

The database (`/var/db/jailops/jailops.db`) has **forward-only** schema migrations (`internal/store/migrations`,
three so far): none has a way back. A daemon older than the database refuses to start with `version_skew` and leaves the
database as it is. Going back is therefore a matter of putting the old database back with the old binaries:

- **With the boot environment** (`bectl activate pre-jailops-upgrade`, then reboot): binaries, configuration and the
  database go back together, as long as `/var/db` is part of the root file system (the installer's default layout;
  check with `zfs list -o name,mountpoint`). The **state dataset** (`<pool>/jailops/state`, mounted at
  `/var/db/jailops/state`) and the jails are datasets of their own and are not rolled back: objects created after the
  upgrade stay. Its documents have no migrations today, so an older daemon reads them.
- **Without one:** stop `jailopsd` before the upgrade and copy `jailops.db` (the daemon checkpoints it when it stops).
  To go back, install the older package file (`pkg add -f FILE`; pkg keeps what it downloaded in `/var/cache/pkg`
  until `pkg clean`), put the copy back, start both services. The daily copies in `/var/db/jailops/backups/` are a last
  resort: what changed since the copy (users, tokens, audit entries) is lost.

UNVERIFIED: neither path, nor an upgrade between two released versions, has been run by the test suite ([upgrade.md](upgrade.md)).

## Recovery objectives (RPO and RTO)

How often snapshots are taken is **not** a recovery objective. A snapshot lives on the same pool as the jail: it protects
against a bad change, not against losing the pool or the host. Write down, per service:

- **RPO**, the data you accept to lose: at worst the interval of its backup policy (`--schedule`) plus the time a run
  takes, and more when runs fail. Measure it as the age of the newest backup of the jail (`metadata.created_at` in
  `jailops backup list --project P -o json`), not as the schedule.
- **RTO**, how long until it serves again: measure it with a drill, not an estimate. `jailops backup verify --project P
  BACKUP --depth full` restores the backup as a hidden jail, boots it without network and checks it from the inside;
  time it (`time jailops backup verify ...`) and compare with the target. A real restore onto a second machine
  (`jailops backup restore --as NAME`) measures the whole path, including what is not in the backup (step 2 above).
  Policies run the drill on a schedule with `--verify-schedule` and `--verify-mode full`.

A target the drill does not meet is a finding: shorten the schedule, keep the key copies where the restoring host gets
them, or change the target.

## What to monitor

Only what the product exposes today. Use a dedicated read-only token for the checks that need one:

    jailops token create monitor --role readonly --global --expires 2160h

A non-administrator sees each doctor check's status but not the text of a failing one (it can hold paths of the host);
read the details as an administrator when an alert fires.

| What | How | Alert when |
|---|---|---|
| The daemon serves | `GET /readyz`, no credentials ([exposure.md](exposure.md#load-balancers-and-monitors)) | not 200 for two probes in a row; the body names the class |
| The daemon runs | `GET /healthz`, no credentials | no answer: restart `jailopsd` |
| The helper is down | `/readyz` answers `not ready: helper`; `service jailops_priv status` | at once: nothing on the host can change |
| The host checks | `GET /api/v1/doctor` with the token (or `jailops doctor`, exit status 1 on a failed check) | `metadata.status` is `fail`; `warn` for a day |
| Reconcile failures | doctor check `reconcile` (`fail` with the error, `warn` for drift); `status.conditions` of each object | `fail` |
| Pool health | doctor checks `zfs.pool` and `zfs.pool.<storage>` | not `ok` |
| Pool capacity | `GET /api/v1/resources`: `size`, `allocated`, `free` of the pool (global identity; no doctor check) | `allocated / size` above 80% |
| The certificate | doctor check `tls.certificate` (warns 14 days before an expiry nothing renews, fails once expired); `tls.acme`; `GET /api/v1/host/certificate`: `current.days_left`, `acme.failures`, `acme.last_error` | `days_left` below 14, or ACME failures |
| The last successful backup | `GET /api/v1/backups?project=P` (readonly in the project): the newest `metadata.created_at` per `spec.jail` with `status.failed` false; `status.verified_at` for the last verification | older than the RPO |
| Failed scheduled backups | the daemon's log only: `backup: job failed` | any line |
| A firewall revert that failed | `GET /api/v1/host/pending`: `last.outcome` is `revert_failed` | at once |
| Who did what | the audit log forwarded to syslog ([logging.md](logging.md)), or `GET /api/v1/audit` (global admin) | `denied` results and `authentication failed` from unknown sources |
| Protocol violations at the helper | the helper's log: `priv.protocol.violation` (a peer sent frames out of order; the helper closed that connection) | any line: the daemon or something else on the socket misbehaves |

**Not exposed yet**, so it cannot be monitored from the API: there is **no metrics endpoint** (counters, latencies,
queue lengths); the history of backup and drill runs (the scheduler keeps it, no route returns it); reconcile pass
durations; the helper's own state beyond whether it answers. JailOps pushes no alerts itself: a monitor polls.
