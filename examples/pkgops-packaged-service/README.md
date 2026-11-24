# pkgops-packaged-service

A Go service packaged with **PkgOps** and installed into a JailOps jail by a recipe (`jailops jail apply`). The service is the PkgOps
example `hello` (`pkgops/examples/hello`: a daemon, an rc.d script, a config file and a service user, built without a FreeBSD
builder). This page says exactly which parts work today.

    pkgops build -o dist                      # in pkgops/examples/hello: cross-builds hello-0.1.0.pkg for FreeBSD:15:amd64
    pkgops key generate --out repo.key        # once
    pkgops repo --repo site --key repo.key dist/*/*.pkg
    pkgops publish --from site --to /var/www/pkg   # any static web server serves it (pkgops docs: hosting.md)

## What works today

1. **PkgOps builds and publishes** a signed pkg repository (verified against real FreeBSD pkg, see PkgOps's `docs/hosting.md`).
2. **A JailOps jail can install from it**: `recipe/recipe.yaml` writes the repository configuration and public key (files beside
   the recipe, in `recipe/pkg/`), updates the catalogue, installs `hello`, sets its rc variables and starts the service. The steps run in
   the order of the file, so the repository's configuration is in the jail before the package step:

       jailops jail apply hello -f recipe/

   Put the real content of PkgOps's `repo.pub` into `recipe/pkg/hello.pub` and your URL into `recipe/pkg/hello.conf` first.
3. `jailops-sync` applies the jail itself declaratively (the example is validated and applied to in-memory hosts by the test suite,
   like every example); the recipe is run by hand, when you want.

To ship a new version run the recipe again, or `jailops exec hello -- pkg upgrade -y hello`.

## Planned, not implemented

- A first-class "package source" in the fleet (a repository, a package and a version declared next to the jail, upgraded with a
  rollout). Today a package is a step of a recipe.

## Run it

    jailops-sync -C examples/pkgops-packaged-service/fleet validate
    jailops-sync -C examples/pkgops-packaged-service/fleet plan
