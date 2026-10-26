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

**Not yet: native ZFS encryption.** There is no `--encrypted` flag; a storage or jail spec with `encrypted: true` is
refused with `unsupported` ("encrypted storage is not implemented" / "native ZFS encryption of jails is not
implemented"). The manual alternative is to make the dataset itself an encryption root before you register it:

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

Syntax `NAME:MOUNT[:ro][:QUOTA]`. Volumes are snapshotted, rolled back, copied and backed up with the jail; a `data` or
`all` rollback restores them. They cannot be added to or removed from an existing jail yet: the volume routes answer `unsupported` ("volume
management is not part of this version") and changing a `disk.<name>` key of an existing jail answers `unsupported`
("changing volumes of an existing jail is not implemented"); declare them at creation. The same thing as configuration keys: `disk.<name>`, `disk.<name>.mount`,
`.quota`, `.read_only` (`jailops config --help`).

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
snapshot. The root of a thin jail is **not** copied: it is rebuilt from the image generation on restore. **Not** in the
backup: host mounts, images (they are rebuilt from pkgbase), and the host's state unless the policy sets `--include-state`.
Replicate or snapshot an archive dataset with ZFS itself; JailOps backups cover what JailOps owns.
See [backup-restore.md](backup-restore.md).
