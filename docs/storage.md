# Storage: big pools, volumes and existing data

ZFS is the only storage layer. A host often has more than one pool: a small fast one for the system and a large one for
bulk data (say a `hdd` pool of spinning disks). JailOps gives you three ways to put data in front of a jail. They differ
in **who owns the data**, and that decides what JailOps will do with it.

| | Owner | Snapshots, rollback, copy | Backed up by JailOps | Who may set it up |
|---|---|---|---|---|
| A jail on a storage | JailOps | yes | yes | a global admin registers the storage; a project's grant limits it |
| A volume | JailOps | its own snapshots; not part of a jail's | not yet (see [What is backed up](#what-is-backed-up)) | a project admin |
| A host directory (a volume with `--path`) | you | no | no | a **global admin**, and only below a directory the host operator listed |

## Storages

A **storage** is a named ZFS dataset root on an imported pool. The daemon creates `default` (`<pool>/jailops`) at its first
start. Register another, for example on the big pool:

    zfs create hdd/jailops                                   # or use an existing empty dataset
    jailops storage create bulk --dataset hdd/jailops --compression zstd
    jailops storage list                                     # POOL, HEALTH, USED, AVAILABLE come from ZFS

`jailops storage list` shows the storages granted to the selected project; `--all` (a global administrator) shows every
storage of the host with the projects that hold it, and `storage show` names them under `Granted to`.

The dataset must exist; it must be empty or already owned by JailOps (`jailops:managed=on`). Registering a storage also
registers it with the root helper, which then allows its operations below that root and nowhere else. `--compression` applies to
new datasets. The helper gives the root's directory (its mountpoint) to root with mode 0700 and sets `devices=off` on the
dataset, at the registration and at every start: jails keep setuid programs below it, and only that mode keeps every
other user of the host away from them. A root may not be mounted on, above or inside a system directory (`/etc`,
`/boot`, `/root`, `/usr/local/etc`, `/var/db`, ...); `/usr/jails` or `/hdd/jailops` are fine
([security-model.md](security-model.md#two-programs-one-boundary)).

**Not yet: encrypted storages and whole encrypted jails.** A storage or jail spec with `encrypted: true` is refused with
`unsupported` ("encrypted storage is not implemented" / "native ZFS encryption of jails is not implemented"). What is
built is an **encrypted volume** inside a jail ([below](#encrypted-volumes)). The manual alternative for a whole
storage is to make the dataset itself an encryption root before you register it:

    zfs create -o encryption=on -o keyformat=passphrase hdd/jailops
    jailops storage create secure --dataset hdd/jailops
    zfs load-key hdd/jailops        # after every boot, before the daemon starts jails on it

ZFS encrypts everything created below an encryption root. JailOps does not load the key, does not store it and does not
know whether it is loaded: a jail on an unloaded storage fails to start with the ZFS error. This arrangement is
UNVERIFIED on a real host.

A storage cannot be deleted while a project grants it or a jail uses it, and deleting it never destroys data.

Grant it to a project, with a quota for that project's datasets, and put jails on it:

    jailops storage grant default --project media
    jailops storage grant bulk --project media --quota 4T
    jailops create plex --project media --image freebsd-15-1 --network web --storage bulk

- **Storage may be shared between projects**; each project gets its own `projects/<project>/` subtree under the root and its
  own quota. (Networks are different: a network belongs to exactly one project.)
- Layout below a root: `images/<image>/g<N>` (immutable image generations), `projects/<project>/jails/<name>` (the
  jail's container dataset: `root` and the persistent datasets), `projects/<project>/volumes/<name>` (the volumes of the
  project), `deleted/`. Image generations are built or
  received **per pool**: the first jail of an image on a pool builds the generation there, so a jail on `bulk` keeps its own
  copy of the generations it uses.
- A jail's disk quota (`--disk 20G`, `resources.disk`) is checked against the project quota before anything is created.

Putting jails on the big pool and keeping the system on the fast one is the usual split: the jail's root and `/usr/local`
on spinning disks are fine for most services, and a database jail can sit on `default`.

## Volumes

A **volume** is a dataset of a project that jails attach by name. It is an object of its own: it outlives the jails, one
volume can be attached to several of them, and deleting a jail never deletes a volume. Where a jail's own datasets (its
root and persistent paths) belong to that jail, a volume belongs to the project.

The example is a mail server and a desktop that share the mail of one user. The mail lives in a volume that both jails
see; the user and the group that own it have the same numbers in both jails (FreeBSD has no uid mapping, so a number is a
person on the whole host):

    jailops volume create maildir --uid 5000 --gid 5000 --mode 0770 --comment "the mail of every user"
    jailops create mail    --image freebsd-15-1 --network lan --storage default
    jailops create desktop --image freebsd-15-1 --network lan --storage default
    jailops volume attach mail    maildir /var/mail/vhosts
    jailops volume attach desktop maildir /home/ann/Mail

`volume attach JAIL VOLUME PATH` (add `--ro` for a read-only mount) attaches a volume that exists already; it never creates one. The jail
may run: the volume is mounted into it at once. A volume is let go again with

    jailops volume detach desktop maildir

The attachment is part of the jail's specification (`volumes: [{volume, mount, read_only}]`, or `disk.<volume>=<path>` with
`jailops config`), so `jailops edit` and a file do the same. A **running** jail gets the mount at once and gives it back at once; the
attachment is in its record and its `fstab` from then on, so the next start makes it again. A detach is refused with `busy`
while a process of the jail uses the mount (`jailops exec JAIL -- fstat PATH` shows which one); nothing is changed, and
the detach works once the process is done. The volume is mounted on the host at its own place,
`<storage mount>/projects/<project>/volumes/<name>`, and the jail's `fstab` binds it into the jail with `nullfs`; the jail
stops the mount again when it stops, and the volume stays where it is for the other jails. `--copy-existing` (the files of
the path go into the volume first) needs a stopped jail. Volumes that a profile of the project attaches (`disk.<volume>`)
reach a running jail at its next start.

**Owner and mode.** `--uid`, `--gid` and `--mode` (default `0` `0` `0755`) are the owner and the mode of the root of the
volume. They are set once, when the volume is made, by the privileged helper (an operation that works on the empty root of
a volume and refuses anything else), and they cannot change later: change the owner of the files from inside a jail with
`chown`. The mode has no setuid, setgid or sticky bit.

**Several jails.** A volume can be attached to more than one jail, and the jails share the same files; `jailops volume show`
lists them (`Attached to: mail (rw), desktop (ro)`). An application that needs a single writer, such as a database, is
attached read-write to one jail and `--ro` to the others. An encrypted volume is attached to one jail only.

**Linux jails.** A volume of a Linux jail is mounted twice, at its path in the jail's root and at the same path below
`compat/linux`, because a Linux process reaches every path through that mirror first (see
[linux-jails.md](linux-jails.md#what-a-linux-process-sees-and-what-it-does-not)). Attach and detach do both mounts.

**Where a volume can go.** Not over a persistent directory of the jail or above one (`/etc`, `/usr/local`, `/var/db`,
`/var/log`, `/var/mail`, `/var/spool`, `/home`, `/root`: a mount on `/var` would hide the datasets below it), not at or
below `/dev`, `/proc`, `/sys` or `/compat`, where the jail's own file systems are, and not inside another volume. The
request says so at once.

**Attached over existing files.** What the jail has at the path is hidden while the volume is mounted over it.
`jailops volume attach JAIL VOLUME PATH --copy-existing` copies the files of the path into the volume first (the volume
should be new); nothing is removed from the jail's own root.

**What the jail's operations do with volumes.**

| | |
|---|---|
| `jail delete` | detaches; the volume and its files stay (`volume list` shows it). |
| `jail snapshot`, `jail rollback`, `jail upgrade` | never touch a volume; a volume has snapshots of its own (below). |
| `jail copy` | the copy attaches no volume, with the data or without it (see [Copying a jail](#copying-a-jail)). |
| `jail backup` | a jail's backup holds the attachments; the volume is a backup of its own, taken once however many jails attach it (see [What is backed up](#what-is-backed-up)). |
| `app install` | the app's `data` paths become volumes named `<jail>-<name>`, made before the jail by visible steps of the plan; `app` jails keep them when deleted. |

**Snapshots of a volume.**

    jailops volume snapshot create maildir before-move -m "before the move"
    jailops volume snapshot list maildir
    jailops volume snapshot rollback maildir before-move      # asks you to type the name; --confirm-data-loss in scripts
    jailops volume snapshot delete maildir before-move

A rollback discards what was written since and the snapshots after it, and is refused while a jail that has the volume
attached runs. `--size` (a ZFS quota) can be changed later with `jailops volume edit`; the labels and the comment too.

**Deleting a volume** destroys its files and its snapshots, and is refused while a jail attaches it (`busy`, with the
command that detaches it). The command shows the size and the number of snapshots and asks you to type the name.

A volume is declared in a file as `kind: Volume` (`jailops volume create -f`, `jailops apply`, and `projects/<p>/volumes/`
in a fleet repository, applied before the jails that attach it). `jailops doctor` checks that every volume has its dataset
and is mounted, and that every attachment names a volume that exists (`jail.volumes`).

## Encrypted volumes

A volume can be encrypted at rest with native ZFS encryption. You choose it when you create the volume, and you type its
passphrase after every boot of the host:

    jailops volume create vault --encrypted --size 20G
    The encrypted volume needs a passphrase. It is asked for after every host boot and cannot be recovered when lost.
    Passphrase for the encrypted volume:
    Repeat the passphrase:
    volume/vault created
    jailops create secrets --image freebsd-15-1 --network lan --storage default
    jailops volume attach secrets vault /var/db/secrets

`encrypted: true` in a file does the same for `jailops volume create -f` (the passphrase is asked for; a fleet repository
cannot declare an encrypted volume, there is no place for a passphrase in a file). The volume becomes its own ZFS
encryption root: `encryption=aes-256-gcm`, `keyformat=passphrase`, `keylocation=prompt`, the key derived from the
passphrase with PBKDF2 (ZFS's default). Nothing else of a jail that attaches it is encrypted: its root, `/etc`,
`/usr/local`, the `/var` datasets and `/home` stay as they are. Put what must be protected (a database directory, a key
store) on the encrypted volume.

**The passphrase.** One passphrase per volume: 12 to 512 bytes, one line. `jailops` asks for it twice without echo and
refuses a pipe; a script gives a file with `--passphrase-file FILE` (its first line; keep it at mode 0600, `jailops` warns
otherwise). There is no `--passphrase VALUE`: a value on the command line ends up in the shell history and in `ps`. Over the
API the passphrase is `passphrase` in the body of the creation (`POST /api/v1/volumes`, `VolumeCreate`) and of an unlock,
over TLS or the local socket. The daemon hands it to the privileged helper on the request stream of the operation and drops
it; the helper gives it to `zfs` on standard input. It is never stored, and never in an answer, an operation, the journal,
the audit log, a log line or a command line.

**After the creation** the volume's key is loaded: the jail starts at once, like any other.

**After every boot of the host** the keys are not loaded. The volume is **locked**, and so is the jail that attaches it: its
state is `locked`, `status.locked` names the volumes, and a start or restart is refused with `locked` (HTTP 409, hint
`jailops volume unlock NAME`). The boot skips a locked jail that has `boot.autostart` (its `Autostart` condition says
`Locked`, the audit log says `skipped`) and remembers that it was due. Unlock the volume:

    jailops volume unlock vault            # asks for the passphrase; --passphrase-file FILE for scripts
    jailops volume unlock vault --start    # and start the jail that attaches it

The unlock loads the key (`zfs load-key`) and nothing else; the start mounts the volume on the host. A jail the boot
wanted to start starts right after the unlock of its volume, without `--start`. That wish lives in the daemon's memory
only: a restart of the daemon forgets it, and the restarted daemon's own boot pass derives it again from `boot.autostart`.
A jail that depends on a locked jail (`boot.depends`) is not started at boot; start it after the unlock.

A wrong passphrase answers `bad_passphrase` (HTTP 403) and says nothing else. After five wrong passphrases in a row a
volume refuses unlocks (and passphrase changes) for 30 seconds (`rate_limited`); a right one resets the count. The count is
in the daemon's memory. The privileged helper keeps a count of its own, which a daemon cannot skip: after five wrong
passphrases for a volume (twenty for all volumes together) every try waits, 30 seconds at first and twice as long after
each further wrong one, at most an hour, without running `zfs`; a right one resets it, and so does a restart of the helper.
`jailops doctor` lists the jails that wait for a passphrase (`volumes.locked`, info) and checks that each pool of a storage
can hold encrypted volumes (`volumes.encryption`: the pool feature `feature@encryption` must be `enabled` or `active`;
JailOps never changes pool features).

**Lock** a volume when its data should be unreadable again without a reboot:

    jailops volume lock vault

It stops the jail that attaches the volume, unmounts the volume and unloads its key (`zfs unload-key`).

**Change the passphrase** of an unlocked volume:

    jailops volume passphrase vault        # the current passphrase once, the new one twice

The helper checks the current passphrase (`zfs load-key -n`) and then runs `zfs change-key`. Only the key that wraps the
data key changes: the data is not encrypted again, so a copy of the dataset taken before the change (a raw replica, an old
disk) still opens with the old passphrase. Changing the passphrase needs the admin role; unlock and lock need operator.

**What is protected, and what is not.** Protected at rest: the volume's blocks on the disks, in a pool image, in a raw send
or replica, on a disk that leaves the building or a host that is stolen while powered off or locked. Not protected:

- the data while the jail runs or the volume is unlocked: the jail, and root on the host, read the files as plain files;
- the key itself while it is loaded: it is in the kernel's memory until the lock or the next shutdown (and pages of the jail's
  processes may reach swap: use encrypted swap, `.eli` in `/etc/fstab`);
- what ZFS leaves readable on an encrypted dataset: dataset and snapshot names, sizes, properties, the dedup table;
- everything of the jail that is not on the encrypted volume.

**A forgotten passphrase means the data is lost.** There is no recovery key, by design: JailOps keeps no copy and no escrow
of a volume passphrase. Keep it in a password manager, and keep backups.

**How the helper knows a volume.** The key operations accept a dataset only when it is named
`<storage root>/projects/<project>/volumes/<volume>`, its own encryption root with `keyformat=passphrase`, and carries the
user properties `jailops:managed=on` (set locally) and `jailops:kind=volume` below a parent of kind `volumes`. These
properties are the mark of ownership, and anyone who may set user properties on the pool (root, or a user with `zfs allow`
there) can set them on a dataset of their own under that name: the helper then treats it as a volume and loads, unloads or
changes its key on request. It never changes the encryption settings it did not set, and a change needs the current
passphrase. Do not delegate `zfs allow` of a storage root to anybody you would not let manage its projects. The helper's
operation that sets the owner of a new volume works on the root of a volume only, opened from the storage mount one plain
directory at a time (a link anywhere on the way is refused), and only while that root is empty.

**Not here:** encrypting a volume in place (`encrypted` cannot change later: the update is refused with a hint), key files or
any unattended unlock at boot, encrypted storages or whole jails.

## Handing a dataset to the jail to manage

A jail can manage a ZFS dataset of its own: root in the jail creates datasets, snapshots and mounts below it with `zfs`
(a database that keeps its own snapshots, a nested set of volumes). The dataset is `<container>/delegated`, a child of the
jail's container next to `root` and `data`; it has `jailed=on` and `mountpoint=none`, so the host never mounts it, and
`jail.conf` names it (`zfs.dataset`) so `jail(8)` attaches it at every start and detaches it at the stop.

    jailops create db --image freebsd-15-1 --zfs-delegation --disk 50GiB ...
    jailops config set db zfs.delegation true          # an existing jail (it needs limits.disk); it takes effect at the next start

- **A disk limit.** A jail with delegation needs `limits.disk`: the quota on its container is what keeps a jail that manages its own datasets from filling the pool.
- **Two consents.** Only a **global administrator** may turn it on (a project user cannot), and the host's `priv.conf`
  must allow it with `admin-zfs-delegation = true`; without that line the helper refuses the jail's `jail.conf`. The helper
  sets `jailed` on that one dataset and on nothing else.
- **What the jail can do.** Everything `zfs` allows inside a jail on that dataset and its children, including setting
  mountpoints below the jail's root and mounting them. It is **not** under JailOps' mountpoint rules, and the jail also
  gets `allow.mount`, `allow.mount.zfs` and `enforce_statfs=1`. Do not delegate to a jail you do not trust with that.
- **Quota.** The jail's disk limit is a quota on the container, so it covers the delegated dataset. A quota set on the
  dataset itself can only be changed from the host.
- **Snapshots and rollback** of the jail take the delegated dataset in the same atomic set, and a data rollback restores it
  (the jail is stopped for it). `jailops jail copy` copies it and hands the copy its own.
- **Backups leave it out.** A backup reads files from mounted datasets, and the host does not mount this one: the run
  says `skipped: delegated: ...`. Use `zfs send` of the dataset, or a backup inside the jail.
- **Turning it off** (`zfs.delegation false`, the jail stopped) clears `jailed` and keeps the data; turning it on again
  hands the same dataset over. `jailops doctor` (`jail.delegation`) fails when a jail asks for the dataset and it is not
  jailed, or a dataset is jailed and no jail asks for it.

## Host directories as volumes

A storage box mounted at `/mnt/storagebox`, a share of the office, a disk of its own: a mount that is not a dataset of
JailOps. A **volume with a path** gives a directory of it to jails the way any volume is given, and adds what a mount
needs: JailOps knows when the mount is gone.

The example is a backup server in a jail that writes to a storage box the host has mounted:

    # the host operator allows the directory above the mount, in priv.conf, and restarts the helper
    admin-host-mount = /mnt

    # a global administrator makes the volume and attaches it
    jailops volume create backups --path /mnt/storagebox/backups --watch --probe write --on-loss stop
    jailops volume attach pbs backups /srv/backups

- **Who.** Only a global administrator makes a volume with a path or attaches one, as for the directory above; a project
  operator can start, stop and detach it, and read its state. The directory has to lie on a mount **below** an
  `admin-host-mount` directory: list `/mnt`, not `/mnt/storagebox` (the mount point of the box is the thing that goes
  away, so it must be inside the approved directory, not be it).
- **A real mount.** The helper looks at the path when the volume is made and again at every jail start: no symbolic link
  on the way, and a mount point (a change of device) on the way from the approved directory down to the path. A path on the
  host's own disk is refused, because a lost mount and an empty directory could not be told apart; to use the files of
  a plain directory, copy them into a volume ([below](#using-data-that-lives-on-the-host)). The volume is made only while the
  directory is on its mount.
- **A jail never gets an empty directory.** When a mount goes away it leaves an empty directory behind, and a jail that
  started on it would write its backups to the host's own disk. A jail whose volume is lost **does not start** (`busy`, with
  the path); a running one keeps the mount it has, which answers with errors and never shows a local directory.
- **`--watch`.** The daemon looks at the mount every few seconds (`--probe mounted`, the default) or also makes and
  removes a file in it (`--probe write`, for a share that went read-only). The volume shows `state: ok` or `lost` with the
  reason in `volume list` and `volume show`, in the console, in `jailops doctor` (`volume.hostdirs`) and as events
  (`volume-lost`, `volume-restored`).
- **`--on-loss`.** `none` (the default) leaves the running jails alone; `stop` stops the jails that attach the volume when
  the mount is lost. They stay stopped: start them when `volume show` says `ok`.
- **When the mount is back.** A running jail gets its mount again by itself (the daemon removes the old mount in the jail
  and makes it again; a mount made under a nullfs mount afterwards is not seen through it, which is why). The event
  `volume-remounted` says so. If the new mount cannot be made, a small read-only tmpfs stands on the jail's mount point
  (never the empty directory of the jail's own root) and the daemon tries again every round. A box that came back while
  the daemon was down is found at the daemon's next start.
- **A share that does not answer.** A hung NFS or SMB mount answers no probe: the volume is `lost` ("the directory does
  not answer") and the daemon keeps one probe on its way per directory, however many looks come meanwhile, so a dead share
  cannot use up the helper's requests. A list or a show uses the watch's last answer or waits a few seconds.
- **Not JailOps's files.** No snapshots, rollback, size limit, encryption or copy of the jail's files into it
  (`--copy-existing`), and backups skip it: back it up where it is, with the tool of the box. Deleting the volume forgets it
  and leaves the directory as it is. A volume that is a directory is attached read-write to as many jails as you like, with the
  same warning as any volume.

`jailops volume edit` changes the watch (a global administrator), the comment and the labels; the path is fixed, so make a
new volume for another directory.

## Changing the layout of a jail

`jailops jail convert NAME --layout thick|thin` changes how the root of a FreeBSD jail is stored. A Linux jail is thick and
stays so.

    jailops jail convert web --layout thick --dry-run
    jailops jail convert web --layout thick
    jailops jail convert web --layout thin

- **Thin to thick** copies the root (a full send and receive) into `root.new`, folds the persistent datasets (`/etc`,
  `/usr/local`, `/var`, `/home`) into the copy, swaps it in and puts the datasets away below `root.old`. The jail no longer
  depends on its image generation.
- **Thick to thin** clones the generation the root was copied from (`--image gN` or `--image NAME#gN` names another generation
  of the same image) as the new root and makes a persistent dataset of each persistent directory of the thick root. Only those
  directories keep the jail's data: everything else comes from the image, so a package installed outside `/usr/local` or a
  changed system file is replaced. `--dry-run` prints what is kept and what is replaced.
- The jail is stopped for the conversion and started again when it ran, followed by the health check. A snapshot of every
  dataset (`jo-pre-convert-...`) is taken first; a failure at any step, and a crash of the daemon, puts the old layout back. The
  old root stays as `root.old` for the retention period of an upgrade. The disk limit of the jail counts both roots until the
  conversion ends. The console's jail page has **Convert layout** with the same preview.

## Copying a jail

`jailops jail copy SOURCE NAME` makes a jail from the specification of another (the image, the limits, the policies, the labels),
empty from its image. **`--data` copies the data too**: the root and the persistent directories (`/etc`, `/usr/local`, `/var`,
`/home`) of the source, in the same project and on the same storage.

    jailops jail copy web web2 --data --start
    jailops jail copy db db-test --data --snapshot before-upgrade

- **The copy is independent.** It is a full `zfs send | zfs receive` of each dataset, never a clone, so it pins no snapshot of the
  source: the source can be deleted, rolled back and snapshotted afterwards, and the copy can be deleted without a word to the source.
  The price is the time and the space of a full copy.
- **The source may run.** The datasets of the source are snapshotted in one atomic step and the copy is made from that snapshot, so
  it holds what a power loss at that moment would have left: a database recovers from it as it does after a crash. Stop the source
  first for a copy that needs no recovery.
- **`--snapshot S` copies an earlier state.** `S` is a snapshot of the source (`jailops jail snapshot list SOURCE`); every dataset of the
  source must have it. The snapshot stays on the source and the copy does not carry it. Without `--snapshot` the plan takes a
  temporary snapshot and drops it when the copy is made.
- **What the copy does not take over.** It has addresses and a MAC address of its own (the source's fixed addresses become `auto`), no
  volume attached (a volume belongs to the jails it is attached to; `jailops volume attach` gives the copy what it needs, and the
  data of a volume is not copied), and no exposure of the source's ports (the host port of a forward belongs to the source's
  exposure: `jailops expose` makes the copy's own). Its hostname and the files inside it are the source's: change them before both
  run.
- **A copy can start.** `--start` starts it when it is made.

The console's jail page has the same: **Copy** asks for the name, whether to copy the data, which snapshot of the source to copy
and whether to start the copy. A copy of another project, or of another host, is a backup of the source restored there
(`jailops backup restore --as NAME --project PROJECT`, [backup-restore.md](backup-restore.md)).

## Using data that lives on the host

The files are in a plain directory of the host's own disk, say `/srv/data/site`, and a jail should have them. A volume that
**is** that directory is refused (it is not a mount, section above). What works is a **copy, once**, into a new ZFS volume:

    # the host operator allows the directory, in priv.conf, and restarts the helper
    admin-host-mount = /srv/data

    # a global administrator copies it into a new volume
    jailops volume create site --from-dir /srv/data/site --uid 80 --gid 80
    jailops volume attach web site /usr/local/www/site

The copy ends with a line that says what came across, for example `copied 1204 files and 87 directories (212 MiB)`. The
console's volume dialog has the same field, "Copy files from a directory of the host".

- **A copy, not a link.** The new volume is an ordinary dataset with snapshots, a size limit and backups. It keeps nothing of
  the directory afterwards: a later change in `/srv/data/site` does not reach the volume, and the other way round. Run it
  again into a new volume to take a fresh copy.
- **Who and where.** Only a global administrator. The directory has to lie below an `admin-host-mount` directory, or be
  one; it need not be a mount. That is the same narrow rule as for a volume that is a mount, so the host operator decides
  which parts of the host's disk a jail's volume can ever be filled from.
- **What is kept.** Owners, modes, times, hard links and symbolic links. Nothing is followed out of the directory: a link
  stays a link and is never read through, and a path on the way that is a link, not a plain directory, is refused.
  `--uid` and `--gid` set the owner of the volume's top directory only; the files below keep their own owners.
- **What is not.** The setuid and setgid bits are dropped and devices, fifos and sockets are skipped (the note counts them).
  A volume is data, not a place for programs that run with another user's rights.
- **Plain volumes only.** Not with `--path` (the directory itself) and not with `--encrypted`; to fill an encrypted volume,
  attach it and the data to one jail and copy inside.
- **It ends whole or not at all.** The directory is checked first, before the volume exists. When the copy fails (disk
  full, the daemon stops), the volume is removed again, so a half-filled one is never left. If the daemon itself dies in the
  middle, the volume may stay; delete it and run the copy again.

## What is unique in a jail

A thin jail is a clone of an image generation plus its persistent datasets (`etc`, `local`, `var-db`, `var-log`,
`var-mail`, `var-spool`, `home`, `rootdir`). `jailops jail diff NAME` lists what the jail holds that the image does not:

    $ jailops jail diff web
    root /: since freebsd-15-1#g3
      M /boot/loader.conf
    etc /etc: since the jail was made
      M /etc/rc.conf
    local /usr/local: since the jail was made
      + /usr/local/etc/web.conf
    total: 3 changes (1 added, 2 modified)

Each line is `+` added, `M` changed, `-` removed or `R old -> new`, under the dataset it belongs to. The root is compared
with the `@ro` snapshot of the generation it was cloned from, a persistent dataset with the snapshot `jailops-base` taken
right after the image's directory was copied into it. That snapshot is on the persistent dataset only: it does not show in
`jailops jail snapshot list`, and a rollback or the retention of snapshots never touches it. A jail made before the snapshot
existed is compared file by file with the same directory of the image instead (kind, mode, size and time; renames show as
`-` and `+`), and the output says "compared with the image, not with a snapshot". `--summary` prints the counts, `--limit N`
lists N lines per dataset (200 by default; the counts always cover all of them), `--dataset etc` keeps one dataset and
`--packages` adds the packages installed on purpose, which needs a running jail. `-o json` prints the reply. The web console shows the same lists in the Changes tab of a jail.

A jail that was just made holds nothing: the diff is empty until it runs. A running jail lists what the system writes
(`/var/run`, `/tmp` in the root, the logs in `var-log`, the package database in `var-db`) and the files JailOps writes at
a start (`/etc/resolv.conf`, the shell files in `/usr/local/etc`). A new generation changes the root and, through the `/etc`
and `/var/db` merge of an upgrade, those two datasets: what the merge brought in shows as a change. A jail with a full copy of its
image (a thick or Linux jail) has no snapshot of the image to compare with, and a jail with a locked encrypted volume is
refused until it is unlocked. Reading needs the operator role for the project.

## What is backed up

A JailOps backup of a jail contains its spec and restore bundle and its persistent datasets, read from an atomic snapshot.
The root of a thin jail is **not** copied: it is rebuilt from the image generation on restore. **Not** in the backup: host
mounts, images (they are rebuilt from pkgbase) and the host's state unless the policy sets `--include-state`.

A **volume** is a backup of its own: a policy backs up the volumes of the jails it selects and the volumes whose labels match
its selector, each once per run however many jails attach it, and a restore of a jail brings back the volumes the project
lacks from the newest backup of that very volume (a volume that was deleted and made again under the same name is another
volume). `--no-volumes` on the policy and `--no-volumes` on a run or a restore leave volumes
out. An encrypted volume is backed up while it is unlocked and restored on its own, with its passphrase. A host directory (a
volume with `--path`) and an archive dataset are not JailOps's to back up: snapshot or replicate them with ZFS itself or
with the tool of the box.
See [backup-restore.md](backup-restore.md).
