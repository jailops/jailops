# The upgrade guarantee

What an administrator who runs JailOps on many hosts can rely on when a new release comes, and how it is kept.

## What is promised

- **A host of any release from 0.8.0 on upgrades in place** to the current release: the packages are replaced, both services
  are restarted ([upgrade.md](upgrade.md)), and nothing has to be recreated, reinstalled or exported and imported. Jails
  keep running while the services restart. Their data, volumes, addresses, networks, projects, policies and exposures are
  there afterwards.
- **Stored state moves forward by itself.** Every shape a release stores is converted when the next release reads it: at
  the first start for the database and the objects of the store, when a jail's record is read for the records of jails,
  when a file is loaded for the files an administrator wrote. No step of an upgrade asks the administrator to edit stored
  state first. Each conversion is one function, written in the release that changes the shape, with a test that opens what
  the earlier release wrote.
- **The database is copied before it is migrated.** When a start finds a database of an older schema, the daemon writes a
  consistent copy next to it (`jailops.db.before-schema-N`, once per target schema) and then migrates. Migrations only go
  forward, so the copy is the way back for a downgrade of the binary.
- **Nothing newer than the binary is opened.** A database, or a stored object, written by a newer release is refused with
  `version_skew` and left alone: upgrade the binary instead of downgrading the data.
- **Configuration keys that a release renames keep working.** `priv.conf` and the flags of `jailops-priv` read a retired
  key as the key that replaced it and say which name to use (`root-dataset` is `storage-root`). Keys that never existed are
  still errors, so a typo is found.
- **Files of a fleet repository and the YAML given to the CLI** that carry a field an earlier release renamed or removed are
  read as the current shape. `jailops-sync validate` reports each such file once, with what to change in it; the CLI reads it
  silently. A redirect rule of a policy cannot be rewritten without the jails that used the policy, so the error names the
  `expose` entry to write instead.

## What is not promised before 1.0

The wording of the command line and the API can change in a release: a flag is renamed, a command is replaced, a field is
removed. The changes are in [CHANGELOG.md](../CHANGELOG.md), `jailops doctor` names what needs an action, and a stored shape
that a change touches is converted as above. From 1.0 the API and the command line keep their shape; what is frozen is in
[api.md](api.md).

## Going back

The release you came from is a boot environment away: `bectl activate`, reboot ([upgrade.md](upgrade.md)). Without a boot
environment, `pkg add -f` the previous packages from `/var/cache/pkg`, put the database copy
`jailops.db.before-schema-N` back with the daemon stopped, and restart both services. A database that a newer release
migrated and that has no copy is refused by the older binary (`version_skew`), by design.

## How it is tested

- **A fixture of every release.** `internal/store/testdata/upgrade/vX.Y.Z` is what that release wrote for a small fleet:
  a project, a storage, two networks (one with the NDP proxy), an image, two policies (one with a redirect rule in the
  releases that had it), an address set, a volume, a WireGuard tunnel, two jails with their records, a database with a user,
  a token and an audit entry, the files the release ships and the keys of its configuration. The release itself wrote them:
  `scripts/make-upgrade-fixture.sh vX.Y.Z` checks the tag out, runs a generator with the code of that release, and the
  generator decodes every spec strictly with the types of that release, so a fixture cannot describe a shape the release
  did not have. The fixtures cover every release from 0.8.0.
- **`go test ./internal/upgradecheck`** opens every fixture with the current code. It checks that nothing is lost (names,
  uuids, addresses, policies, volumes, the exposure of the jails), that what a conversion rewrites is rewritten (an IPv6
  proxy mode is the NDP proxy switch, a proxy device is an expose entry, a redirect rule leaves the policy and is recorded
  as a pending forward, an image alias is dropped), that every spec fits its current type exactly (a field the type does not
  know is data the code would lose), that the jails' records are read without dropping a key, that a second open changes
  nothing, that the database reaches the current schema, that every key of the daemon's configuration in that release is
  still known, and that every key of the helper's configuration is known or retired.
- **Newer than the binary** is a test of its own for the database and for an object.
- **Every release has its fixture:** a test fails when a release of `CHANGELOG.md` from 0.8.0 on has none, and
  `just release-pr` adds the fixture of the newest tag to the release pull request when master does not have it
  (`scripts/make-upgrade-fixture.sh` makes it from the tag), so no step of its own is left to do.

- **On a real machine, every night:** the workflow `upgrade-e2e` installs the newest released packages from
  `pkg.jailops.net` on a FreeBSD VM, makes a fleet with their command line, replaces them with the packages of master, and
  checks that the jails still run, their data and addresses are there, `jailops doctor` has no new failure, the firewall of
  a jail is loaded again, the console is served and a jail restarts (`scripts/ci-freebsd-upgrade.sh`). A mirror that does not
  answer ends it as skipped.

## The stored shapes

| Shape | Where | Converted by | Test |
|---|---|---|---|
| Database | `data/jailops.db` | forward-only migrations (`internal/store/migrations`), a copy first, newer refused | `internal/store`, `internal/upgradecheck` |
| Objects of the store | `state/objects/<Kind>/<uuid>.json` | `apiVersion` migrations, and at load: IPv6 `mode` is `ndp_proxy`, a tunnel's `down` is `disabled`, image aliases are dropped, redirect rules of policies and proxy keys of profiles become exposures of the jails (kept in `migrated-forwards.json` until the service has written them) | `internal/state`, `internal/upgradecheck` |
| Records of jails | `spec.json` and `bundle.json` in the jail's dataset | at read: proxy devices are expose entries, an image alias is dropped | `internal/lifecycle`, `internal/upgradecheck` |
| Daemon configuration | `jailopsd.yaml` | unknown keys are refused; every key of every release from 0.8.0 on is known | `internal/upgradecheck` |
| Helper configuration | `priv.conf`, flags of `jailops-priv` | retired keys are read as their replacement (`retiredKeys`) | `cmd/jailops-priv` |
| Backup node configuration | `backupd.yaml` | unknown keys are refused; the file each release ships is read | `internal/upgradecheck` |
| Fleet repository, YAML given to the CLI | files of an administrator | read as the current shapes (`api.UpgradeSpec`), reported by `jailops-sync validate` | `sdk/api`, `internal/fleet`, `internal/cli` |

Not covered by a fixture: the generations of an image and the user properties of datasets (they need ZFS: the nightly run
on a real machine and the end-to-end script exercise them through the fleet they make), the chunk repositories of backup
nodes and the client file under `~/.config/jailops`.

## Changing a stored shape

A release that renames or removes a field, a key or a file:

1. Writes the conversion where the shape is read: `api.UpgradeSpec` for the spec of an object, a function of the store for
   a document, an entry of `retiredKeys` for a key of `priv.conf`.
2. Lets the current type reject what never was valid: a misspelt key stays an error.
3. Runs `go test ./internal/upgradecheck`: the fixtures of the earlier releases fail when a shape lost its conversion, and
   a fixture of a release that had the old shape is the test of the new conversion.
4. The next `just release-pr` makes the fixture of this release (`scripts/make-upgrade-fixture.sh`).
