# Backup and restore

## What it is

Backups are **encrypted on the host before they leave it**, into a repository on a **backup node that holds no key**. The
host cuts data into content-defined chunks, compresses them (zstd) and encrypts them (XChaCha20-Poly1305). A chunk's id is a
keyed HMAC, so the node cannot even confirm that it holds known data. Every snapshot is self-contained, a list of references
to chunks: there are no incremental chains to break, and only new chunks are uploaded. The node sees object counts and sizes,
the deduplication graph, snapshot times, and host and jail identity. It never sees file names, contents or keys.

This is **not ZFS send and not ZFS native encryption**. ZFS gives the backup a consistent view (an atomic recursive snapshot).
Replication between hosts you trust is a separate feature that has no API or CLI yet. Design section 9 has the details.

What is in a backup, and what is not: see the last section of [storage.md](storage.md).

Verified on FreeBSD 15.1 arm64 (design 18.3, 18.4, and the clean-install run on both architectures): back up, verify at three
depths, restore as a new jail, the restored jail starts and has its data.

## The pieces

| | Where | Program |
|---|---|---|
| The jail host | every host | `jailopsd`, with a `backup:` section in `jailopsd.yaml` |
| The backup node | a machine you trust to be available, not to be secret | `jailops-backupd`, package `jailops-backupd`, needs PostgreSQL and a directory or an S3 bucket |

## 1. Set up the node

    # /usr/local/etc/jailops/backupd.yaml: see packaging/backupd.yaml
    jailops-backupd -c /usr/local/etc/jailops/backupd.yaml init --admin alice
    sysrc jailops_backupd_enable=YES
    service jailops_backupd start

`init` creates the repository, probes the datastore, migrates the database, creates the node's certificate and optionally the
first administrator, and **prints the certificate's fingerprint, which hosts pin**. Keep it.

## 2. Enrol the host

The host authenticates to the node with a client certificate; the node knows the certificate by fingerprint:

    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
        -keyout /var/db/jailops/backup-client.key -out /var/db/jailops/backup-client.crt -subj /CN=host1 -days 3650
    chown jailops /var/db/jailops/backup-client.*; chmod 600 /var/db/jailops/backup-client.key
    jailops-backupd -c /usr/local/etc/jailops/backupd.yaml hosts enroll host1 \
        --fingerprint <sha256 hex of the client certificate> --scopes backup-push,backup-pull,restore

`hosts list` and `hosts revoke NAME` manage them. A host cannot delete or overwrite its backups: writes are create-only, and
deletion is a request with a delay (`delete request`, default 72 hours) that the node's administrator can cancel.

## 3. Point the host at it

In `jailopsd.yaml` (see [configuration.md](configuration.md)):

    backup:
      node:
        name: vault
        address: backup.example.net:8444
        fingerprint: <the node's fingerprint>
        cert_file: /var/db/jailops/backup-client.crt
        key_file: /var/db/jailops/backup-client.key
      recovery_recipients: []        # age public keys; see the next section

Restart `jailopsd`. Without a `backup:` section every backup call answers `unsupported`.

## 4. The key, and the one thing you must not lose

    jailops backup key create        # prints the recovery key ONCE
    jailops backup key status

The **recovery key** (`JOR1-...`) is shown once, kept nowhere by JailOps and cannot be shown again. It is the only way to restore on
another machine if this host is lost. Store it offline. The host key itself is a file (`backup.key`, default under
`/var/db/jailops`), optionally wrapped with a passphrase (`backup.passphrase_file`; without one the file mode protects it, which
unattended schedules need).

**Escrow** wraps the key to one or more age recipients and uploads it to the node, so a holder of a matching identity can
recover it even when the host and the printed recovery key are gone:

    jailops backup key escrow --recipient age1...

Raw backups are **refused** (`key_not_escrowed`) while the key is not escrowed, unless `backup.allow_unescrowed` is true, which you
should not use outside a test.

## 5. Targets, policies and runs

A policy selects jails **by label**, so label the jails first:

    jailops label web --project shop backup=yes
    jailops backup target create vault --address https://backup.example.net:8444 --fingerprint <the node's fingerprint>
    jailops backup policy create nightly --project shop --selector backup=yes --target vault \
        --schedule "0 2 * * *" --keep-daily 14 --keep-weekly 8 --keep-monthly 12 \
        --verify-schedule "0 4 * * 0" --verify-mode extract
    jailops backup run --project shop nightly      # now, instead of at 02:00
    jailops backup list --project shop

An empty selector matches no jail. `--include-state` also backs up the host's state dataset. The node applies retention,
garbage collection (with a grace period, minimum 12 hours) and quarantine of corrupt chunks on its own schedule
(`jailops-backupd gc`, `verify`, `retention`, `quarantine list`).

## 6. Verify

    jailops backup verify --project shop <id>                  # deep: reads and checks every chunk
    jailops backup verify --project shop <id> --depth extract  # restores into a scratch directory
    jailops backup verify --project shop <id> --depth full     # restores and boots a hidden jail with no network, checks it from the inside

`extract` and `full` need the key on the host that runs the check and a scratch location (`backup.scratch_dir`,
`backup.scratch_dataset`). Run verification on a schedule; an untested backup is not a backup. The node verifies the structure of what it holds
without a key (`jailops-backupd verify`).

## 7. Restore

Restoring always starts with a **preflight** that lists what is missing (image generations, networks, policies), what would be
created and what conflicts. Nothing is touched until you confirm.

    jailops backup restore --project shop <id> --as web-restored --dry-run     # the preflight only
    jailops backup restore --project shop <id> --as web-restored -y

A restore names the project it goes into (`--project`; there is no default) and is admitted like a new jail: the project's
grants and quotas apply, and the network and storage must be granted to it. The jail gets a new UUID and fresh addresses
(`--keep-ip` keeps the fixed addresses, `--map` moves its networks). `--replace` destroys an existing jail and needs its name
typed or `--confirm-data-loss`.

**On a new host** (the old one is gone): the design is that a restore there needs only the recovery key (design 9.3). Install
JailOps, set up `backup:` pointing at the same node and enrol the new host's certificate on the node. **Not done yet:** the
`jailops backup key` commands are `create`, `escrow` and `status`; there is no command that loads a recovery key or an
escrowed key into a new host, so this path is **UNVERIFIED and not usable from the CLI today**. Keep the old host's
`backup.key` file (it is the key) in your own backup, and test a restore on a second machine with it before you rely on this.
The jail itself comes back from the repository: the preflight lists an image generation that is missing on the new host, and it
is rebuilt from pkgbase.

**UNVERIFIED:** S3 as a datastore on a real service.
