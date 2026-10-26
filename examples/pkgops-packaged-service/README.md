# pkgops-packaged-service

A Go service packaged with **PkgOps** and installed into a JailOps jail by a `provision:` step. The service is the PkgOps
example `hello` (`pkgops/examples/hello`: a daemon, an rc.d script, a config file and a service user, built without a FreeBSD
builder). This page says exactly which parts work today.

    pkgops build -o dist                      # in pkgops/examples/hello: cross-builds hello-0.1.0.pkg for FreeBSD:15:amd64
    pkgops key generate --out repo.key        # once
    pkgops repo --repo site --key repo.key dist/*/*.pkg
    pkgops publish --from site --to /var/www/pkg   # any static web server serves it (pkgops docs: hosting.md)

## What works today

1. **PkgOps builds and publishes** a signed pkg repository (verified against real FreeBSD pkg, see PkgOps's `docs/hosting.md`).
2. **A JailOps jail can install from it**: `provision:` runs inside the jail at its first start. The order inside `provision`
   is fixed: packages, files, services, commands. A private repository needs its configuration file in the jail **before**
   `pkg install`, and `packages:` runs before `files:`, so `fleet/projects/apps/jails/hello.yaml` copies the repository
   configuration and public key with `files:` and does everything else as `commands:` (`pkg update -f`, `pkg install -y hello`,
   `sysrc`, `service hello start`; argv only, never a shell).
3. `jailops-sync` applies that jail declaratively (the example is validated and applied to in-memory hosts by the test suite, like
   every example).

## What you have to do by hand today

- **The two files** `provision.files` copies (`project-files/pkg/hello.conf` and `hello.pub`) are files **of the project on the host**,
  in `<state_dir>/project-files/apps/` (default `/var/db/jailops/state/project-files/apps/`). `jailops-sync` does not ship them: copy
  them there once (the [provisioning](../provisioning) script is a good place):

      install -d /var/db/jailops/state/project-files/apps/pkg
      install -m 0644 project-files/pkg/hello.* /var/db/jailops/state/project-files/apps/pkg/

  and put the real content of PkgOps's `repo.pub` into `hello.pub` and your URL into `hello.conf`.
- Provisioning runs **once** per jail. Changing the declaration does nothing until `jailops provision hello --again`; to ship a new
  version run `pkg upgrade -y hello` in the jail (`jailops exec hello -- pkg upgrade -y hello`) or rebuild the jail.

## Planned, not implemented

- A first-class "package source" in the fleet (a repository, a package and a version declared next to the jail, upgraded with a
  rollout). Today a package is a command in `provision:`.
- Shipping project files with the fleet instead of by hand.
- Installing from a repository in `provision.packages` before `provision.files` (the order is fixed today).

## UNVERIFIED

The `provision:` step on FreeBSD (the design marks every provisioning command as run through `jexec` and not yet verified), and
whether a minimal pkgbase jail carries a working `pkg` that can fetch from an https repository.

## Run it

    jailops-sync -C examples/pkgops-packaged-service/fleet validate
    jailops-sync -C examples/pkgops-packaged-service/fleet plan
