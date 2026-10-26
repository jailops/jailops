# big-pool-tank

A NAS: a small fast pool (`zroot`, the system and JailOps's `default` storage) and a **16 TB pool of spinning disks**
(`tank`). Three ways to put the big pool in front of jails, and this example uses all three. They differ in **who owns the data**
([storage.md](../../docs/storage.md)):

| What | In the example | Owner | Snapshots / backup by JailOps |
|---|---|---|---|
| A **storage** on `tank` | `hosts/nas1/storage/bulk.yaml` (`dataset: tank/bulk`), granted to the project with a 12 TB quota | JailOps | yes |
| A **volume** | `scratch` has `work` (4T), `plex` has `metadata` (200G): datasets under the jail on `bulk` | JailOps | yes, with the jail |
| A **host mount** | `plex` sees `/tank/archive/movies` and `/music` read-only | you | no |

## Prepare the host (once)

    zfs create tank/bulk                                  # JailOps data; must exist, must not overlap another storage
    # The archive is yours: it is only mounted. Say where mounts may come from, as root, in priv.conf:
    echo 'admin-host-mount = /tank/archive' >> /usr/local/etc/jailops/priv.conf
    service jailops_priv restart

(`bootstrap.sh` of [provisioning](../provisioning) does the second step with `JAILOPS_HOST_MOUNTS=/tank/archive`.)

Registering the storage (`sync` creates it) also registers it with the root helper, which then allows its operations below
`tank/bulk` and nowhere else.

## What to know

- **Host mounts need a global-admin identity**, so the token or certificate that `jailops-sync` uses must be a global
  admin's. A project's operator cannot add one, and a **profile cannot carry one** (`validate` refuses it). The helper refuses
  the jail's start when the source is not below an `admin-host-mount` directory; `validate` prints a RISK for every host mount and
  for every read-write one. Use `read_only: true` where you can: a read-write mount lets the jail change or delete the
  host's data.
- The archive is **not backed up** by JailOps and not rolled back with the jail. Use ZFS itself (`zfs snapshot`, replication)
  for it.
- Storage may be shared between projects, each with its own quota; a network belongs to one project. The jail's root can stay on
  the fast pool (`storage: default`) while its data is on `bulk`: here `plex` is wholly on `bulk`, because spinning disks are fine for a media
  server.
- Image generations are built **per pool**: the first jail on `bulk` builds its copy there, so the first start takes longer.
- Quotas are checked before anything is created: a jail's `resources.disk` and volumes against the project quota.

## Run it

    jailops-sync -C examples/big-pool-tank validate
    jailops-sync -C examples/big-pool-tank plan          # compare with EXPECTED-PLAN.md
    jailops-sync -C examples/big-pool-tank sync --apply

## Verified, and not

A read-only host mount of a dataset of a second pool, refused writes, and a refused directory outside the approved list are
verified on FreeBSD 15.1 arm64 (design 18.11). **UNVERIFIED:** this repository on a real 16 TB pool, a storage of
that size under load.
