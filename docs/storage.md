# Storage: big pools, volumes and existing data

ZFS is the only storage layer. A host often has more than one pool: a small fast one for the system and a large one for
bulk data (say a `hdd` pool of spinning disks). JailOps gives you three ways to put data in front of a jail. They differ
in **who owns the data**, and that decides what JailOps will do with it.

| | Owner | Snapshots, rollback, copy | Backed up by JailOps | Who may set it up |
|---|---|---|---|---|
| A jail on a storage | JailOps | yes | yes | a global admin registers the storage; a project's grant limits it |
| A volume | JailOps | yes, with the jail | yes, with the jail | whoever may create the jail |
| A host mount | you | no | no | a **global admin**, and only below a directory the host operator listed |

## Storages

A **storage** is a named ZFS dataset root on an imported pool. The daemon creates `default` (`<pool>/jailops`) at its first
start. Register another, for example on the big pool:

    zfs create hdd/jailops                                   # or use an existing empty dataset
    jailops storage create bulk --dataset hdd/jailops --compression zstd
    jailops storage list                                     # POOL, HEALTH, USED, AVAILABLE come from ZFS

The dataset must exist; it must be empty or already owned by JailOps (`jailops:managed=on`). Registering a storage also
registers it with the root helper, which then allows its operations below that root and nowhere else. `--compression` applies to
new datasets.

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

    jailops project set media --storage default --storage bulk=4T
    jailops create plex --project media --image freebsd-15@stable --network web --storage bulk

- **Storage may be shared between projects**; each project gets its own `projects/<project>/` subtree under the root and its
  own quota. (Networks are different: a network belongs to exactly one project.)
- Layout below a root: `images/<image>/g<N>` (immutable image generations), `projects/<project>/jails/<name>` (the
  jail's container dataset: `root`, the persistent datasets, `data/<volume>`), `deleted/`. Image generations are built or
  received **per pool**: the first jail of an image on a pool builds the generation there, so a jail on `bulk` keeps its own
  copy of the generations it uses.
- A jail's disk quota (`--disk 20G`, `resources.disk`) is checked against the project quota before anything is created.

Putting jails on the big pool and keeping the system on the fast one is the usual split: the jail's root and `/usr/local`
on spinning disks are fine for most services, and a database jail can sit on `default`.

## Volumes

A volume is a dataset JailOps creates under the jail and mounts at a path:

    jailops create db --project web --image freebsd-15@stable --network lan --storage bulk \
        --volume data:/var/db/postgres:4T
    jailops create web2 ... --volume www:/usr/local/www:ro

Syntax `NAME:MOUNT[:ro][:encrypted][:QUOTA]` (`encrypted`: see [Encrypted volumes](#encrypted-volumes)). Volumes are snapshotted, rolled back, copied and backed up with the jail; a `data` or
`all` rollback restores them. They cannot be added to or removed from an existing jail yet: the volume routes answer `unsupported` ("volume
management is not part of this version") and changing a `disk.<name>` key of an existing jail answers `unsupported`
("changing volumes of an existing jail is not implemented"); declare them at creation. The same thing as configuration keys: `disk.<name>`, `disk.<name>.mount`,
`.quota`, `.read_only` (`jailops config --help`).

## Encrypted volumes

A volume can be encrypted at rest with native ZFS encryption. You choose it when you create the jail, and you type its
passphrase after every boot of the host:

    jailops jail create vault --project web --image freebsd-15@stable --network lan --storage default \
        --volume secrets:/var/db/secrets:encrypted:20G
    The encrypted volumes need a passphrase. It is asked for after every host boot and cannot be recovered when lost.
    Passphrase for the encrypted volumes:
    Repeat the passphrase:
    jail/vault created

`encrypted: true` on a volume does the same in a file (`jailops jail create -f`). The volume becomes its own ZFS
encryption root: `encryption=aes-256-gcm`, `keyformat=passphrase`, `keylocation=prompt`, the key derived from the
passphrase with PBKDF2 (ZFS's default). Nothing else of the jail is encrypted: its root, `/etc`, `/usr/local`, the `/var`
datasets, `/home` and its other volumes stay as they are. Put what must be protected (a database directory, a key store)
on the encrypted volume.

**The passphrase.** One passphrase per jail, for all its encrypted volumes: 12 to 512 bytes, one line. `jailops` asks for it
twice without echo and refuses a pipe; a script gives a file with `--passphrase-file FILE` (its first line; keep it at
mode 0600, `jailops` warns otherwise). There is no `--passphrase VALUE`: a value on the command line ends up in the shell
history and in `ps`. Over the API the passphrase is `volume_passphrase` in the body of the creation (`POST /api/v1/jails`)
and `passphrase` in the body of an unlock, over TLS or the local socket. The daemon hands it to the privileged helper on
the request stream of the operation and drops it; the helper gives it to `zfs` on standard input. It is never stored, and
never in an answer, an operation, the journal, the audit log, a log line or a command line.

**After the creation** the volume's key is loaded: the jail starts at once, like any other.

**After every boot of the host** the keys are not loaded. The jail is **locked**: its state is `locked`, `status.locked`
names the volumes, and a start or restart is refused with `locked` (HTTP 409, hint `jailops jail unlock NAME`). The boot
skips a locked jail that has `boot.autostart` (its `Autostart` condition says `Locked`, the audit log says `skipped`) and
remembers that it was due. Unlock it:

    jailops jail unlock vault            # asks for the passphrase; --passphrase-file FILE for scripts
    jailops jail unlock vault --start    # and start it

The unlock loads the keys (`zfs load-key`) and nothing else; the start mounts the root first and the volumes over it. A jail
the boot wanted to start starts right after its unlock, without `--start`. That wish lives in the daemon's memory only: a
restart of the daemon forgets it, and the restarted daemon's own boot pass derives it again from `boot.autostart`. A jail
that depends on a locked jail (`boot.depends`) is not started at boot; start it after the unlock.

A wrong passphrase answers `bad_passphrase` (HTTP 403) and says nothing else. After five wrong passphrases in a row a jail
refuses unlocks (and passphrase changes) for 30 seconds (`rate_limited`); a right one resets the count. The count is in the
daemon's memory. `jailops doctor` lists the locked jails (`volumes.locked`, info) and checks that each pool of a storage can
hold encrypted volumes (`volumes.encryption`: the pool feature `feature@encryption` must be `enabled` or `active`; JailOps
never changes pool features).

**Lock** a jail when its data should be unreadable again without a reboot:

    jailops jail lock vault

It stops the jail, unmounts its datasets and unloads the keys (`zfs unload-key`).

**Change the passphrase** of an unlocked jail:

    jailops jail passphrase vault        # the current passphrase once, the new one twice

The helper checks the current passphrase (`zfs load-key -n`) and then runs `zfs change-key` on every encrypted volume of the
jail; if one fails, the volumes already changed are changed back. Only the key that wraps the data key changes: the data is
not encrypted again, so a copy of the dataset taken before the change (a raw replica, an old disk) still opens with the old
passphrase. Changing the passphrase needs the admin role; unlock and lock need operator.

**What is protected, and what is not.** Protected at rest: the volume's blocks on the disks, in a pool image, in a raw send
or replica, on a disk that leaves the building or a host that is stolen while powered off or locked. Not protected:

- the data while the jail runs or the volume is unlocked: the jail, and root on the host, read the files as plain files;
- the key itself while it is loaded: it is in the kernel's memory until the lock or the next shutdown (and pages of the jail's
  processes may reach swap: use encrypted swap, `.eli` in `/etc/fstab`);
- what ZFS leaves readable on an encrypted dataset: dataset and snapshot names, sizes, properties, the dedup table;
- everything of the jail that is not on the encrypted volume.

**A forgotten passphrase means the data is lost.** There is no recovery key, by design: JailOps keeps no copy and no escrow
of a volume passphrase. Keep it in a password manager, and keep backups (below).

**Copies, snapshots, backups.**

- `jailops jail copy` sends an encrypted volume raw (`zfs send -w`): the copy stays encrypted, has the same passphrase and
  starts locked.
- A backup reads the files of an atomic snapshot of the mounted datasets, so it needs the volume unlocked. A locked volume is
  left out and the run says `skipped: locked: volume NAME` among its warnings; the rest of the jail is backed up. The backup
  of an unlocked volume holds its files, encrypted again on the host with the repository key before they leave it (design
  9.3); the backup node never sees them. A restore makes a plain volume: the preflight says so ("restored as a plain
  volume"), and the restored jail's spec no longer claims `encrypted`. To have the data encrypted again, create a new jail
  with an encrypted volume and copy the data in. A restore into an encrypted volume is not built.
- Replication between hosts (design 9.8) uses raw sends, which keep encrypted datasets encrypted; it has no command yet.

**Not in this version:** encrypting a volume of an existing jail or a volume in place (`encrypted` cannot change later: the
update is refused with a hint), adding a volume to an existing jail, key files or any unattended unlock at boot, encrypted
storages or whole jails, a restore into an encrypted volume.

**Verified on FreeBSD 15.1** by the `encrypted-volume` block of `scripts/ci-freebsd-e2e.sh` (amd64 and arm64): `zfs create`
and `zfs load-key` read the passphrase as one line from a standard input that is not a terminal, a locked jail refuses to
start, a wrong passphrase is refused (`Incorrect key` mapped to `bad_passphrase`) and the right one opens the volume, the data
is still there, and neither passphrase appears in `/var/log/messages` or the state directory. **UNVERIFIED:** `zfs change-key`
with `zfs load-key -n` while the key is loaded, a raw receive of an encryption root with `-o keylocation=prompt`, and snapshots
and rollbacks of a locked volume.

## Existing data: host mounts

You have a dataset of ten years of archive on `hdd/archive`, mounted at `/hdd/archive`, and you want a jail to see part of
it. That data is not JailOps's: it is not snapshotted, backed up or rolled back by JailOps, and a read-write mount lets the
jail change or delete it. Use `:ro` where you can.

Two keys, two people:

1. **The host operator** (root on the host) names the directories in `priv.conf` and restarts the helper:

       admin-host-mount = /hdd/archive

2. **A global administrator** mounts below one of them:

       jailops create plex --project media --image freebsd-15@stable --network web --storage bulk \
           --host-mount /hdd/archive/movies:/mnt/movies:ro

   `--host-mount SOURCE:TARGET[:ro]`, repeatable (`host_mounts` in the jail spec).

Properties worth knowing:

- A project's operator cannot add a host mount to a jail, and a profile cannot carry one. An operator can still manage a jail
  that already has them (start, stop, change other settings).
- The API accepts the spec; the **helper refuses at jail start** a source that is not below an `admin-host-mount` directory
  (the API does not know the operator's list). The mount point inside the jail is created for you on every render.
- The source is checked as a path string against the operator's list. Do not make a directory below an approved root a
  symlink to somewhere else, and do not approve a root that unprivileged users can write into.
- Verified on FreeBSD 15.1 arm64 (design 18.11): a read-only mount of a dataset of a second pool showed its files in the jail,
  refused writes, and was gone after the jail stopped; a directory the operator had not listed (`/etc`) was refused.

**Handing a whole dataset to the jail to manage** (`zfs_delegation`: `jailed=on`, `zfs jail`) is accepted by the API (global
admin only) and rendered into `jail.conf`, but the helper does not hand the dataset over yet. Treat it as planned.

## What is backed up

A JailOps backup of a jail contains its spec and restore bundle, its persistent datasets and its volumes, read from an atomic
snapshot (an encrypted volume only while it is unlocked, see above). The root of a thin jail is **not** copied: it is rebuilt from the image generation on restore. **Not** in the
backup: host mounts, images (they are rebuilt from pkgbase), and the host's state unless the policy sets `--include-state`.
Replicate or snapshot an archive dataset with ZFS itself; JailOps backups cover what JailOps owns.
See [backup-restore.md](backup-restore.md).
