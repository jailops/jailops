# Moving a jail as a file: export and import

`jailops jail export` writes a jail to one file. `jailops jail import` makes a new jail of such a file, on this host or another, in this project or another, under the same name or a new one. No backup node, key or policy is involved: the file is the whole transfer, and you carry it as you like (`scp`, a USB disk, a pipe).

```sh
jailops jail export web --to web.jailexport
jailops --host host-b jail import web.jailexport --project staging --as web
# straight from one host to the other
jailops --host host-a jail export web --to - | jailops --host host-b jail import -
```

## What the file holds

- **The jail's specification**, and frozen copies of the networks, firewall policies and address sets it refers to. Sealed secrets are never in the file, only their names; the importing host needs the same secrets sealed to its own key.
- **A thin jail's data**: its persistent datasets (`etc`, `local`, `var-*`, `home`). The root is the image's and is not in the file; the manifest records which image, and the importing host needs that image (`jailops image list`; the preflight names it when it is missing). The jail then runs on the newest generation of the image there.
- **A thick jail, and a Linux jail, carry their whole root.**
- **Volumes only with `--with-volumes`.** A volume that other jails attach too is copied by value, with a warning; the copy is a new volume. An encrypted volume is sent as it is stored and stays encrypted: the file has no key and the volume is locked after the import (unlock it with its passphrase). A volume that is a directory of the host is never in the file.

The datasets are read as they are at one moment, as if the host had lost power then; a database recovers as it does after a crash. Export while the jail runs; it is busy (no stop, no upgrade) until the file is written. `--snapshot NAME` exports the state of a snapshot made earlier (`jail snapshot create`). `--zstd` compresses the data in the file.

## Import

The file is read once. Its manifest is checked first against the host and the project, with the checks a new jail goes through:

| Check | What happens |
|---|---|
| name | a jail of that name in the project is refused; use `--as NAME` |
| image | a thin jail needs the image of the same name |
| networks | a network of the file that this host lacks is refused; map it with `--network-map OLD=NEW` (an import never makes a network) |
| addresses | new ones are allocated; with `--keep-ip` the file's addresses are kept when they are free |
| policies, address sets | made from the file when the project lacks them |
| volumes | made as volumes of the project; a name that is taken is refused, rename with `--volume-map OLD=NEW` |
| release, quotas | the host's FreeBSD must not be older than the image's; the project's quotas apply as for a new jail |
| grants | the project must hold the grants the jail needs |

`--dry-run` prints this plan, reads the whole file to its end and changes nothing. The new jail has a new uuid and does not start at boot; `--start` starts it once it is made.

An import is atomic. The datasets are received into new names, verified against the checksums of the file, and the jail appears only when all of them are there. A file that is cut off or damaged is named as such (`the export file is truncated`, `the export file is corrupt`) and leaves no jail and no dataset behind; a daemon that stops in the middle removes the leftovers when it starts.

## Roles and audit

Exporting needs the operator role in the jail's project; importing needs the admin role, like creating a jail. Both are in the audit log.

## The file format

A file is the line `JAILOPS-EXPORT 1`, then records: a type byte, the length of the payload as eight bytes (big endian) and the payload.

| Record | Holds |
|---|---|
| `M` | the manifest (JSON): jail, project, layout, image, bundle, the list of parts, warnings |
| `D` | the start of a part: dataset, kind, snapshot, the snapshot it is incremental from, whether it is raw, whether it is compressed |
| `B` | a piece of the `zfs send` stream |
| `E` | the end of a part: SHA-256 and length of the stream |
| `F` | the footer: the number of parts and a SHA-256 over the manifest and the ends of the parts |

A persistent dataset that has a baseline snapshot (what `jail diff` compares with) is two parts: the baseline, then the export snapshot incremental from it, so `jail diff` still works on the new jail.

## The API

`GET /api/v1/jails/{name}/export` (extension `jail_export`, operator) streams the file; `?snapshot=NAME`, `?with_volumes=true` and `?zstd=true` choose what it holds. The reply ends with the trailer `X-Jailops-Export: ok`; a client that does not see it has an incomplete file (the SDK returns an error). `POST /api/v1/jail-imports/preflight` (admin) takes the manifest and the options and answers what the import would do. `POST /api/v1/jail-imports` (admin) takes the file as the body, with `?as=`, `?keep_ip=true`, `?start=true`, `?network_map=OLD=NEW` and `?volume_map=OLD=NEW`, and answers with the new jail. The privileged helper is only asked for `zfs send` and `zfs receive` of the jail's own datasets.
