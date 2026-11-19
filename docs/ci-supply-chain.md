# CI supply chain

What the workflows in `.github/workflows` run that is not ours, and how to pin it. The workflows name actions by a
tag (`@v7`), which the action's owner can move to other code at any time; a full commit SHA cannot move.

## What runs, with what rights

| Workflow | Permissions of `GITHUB_TOKEN` | Actions |
|---|---|---|
| `ci.yml` (every push and pull request) | `contents: read` for every job | `actions/checkout`, `actions/setup-go`, `actions/setup-node`, `actions/cache`, `actions/upload-artifact`, `actions/download-artifact`, **`vmactions/freebsd-vm`** |
| `release.yml` (a tag `v*`) | `contents: read`, and `contents: write` in the one job that publishes the release (the check job before it cannot write) | `actions/checkout`, `actions/setup-go`, `actions/setup-node`, **`goreleaser/goreleaser-action`** |
| `ci-rerun-mirror.yml` (a finished CI run of this repository) | `actions: write` to start one rerun, `contents: read` | none: the `gh` of the runner reads the job logs through the API and runs no code of the run |
| `linux-e2e.yml` (every night and on demand; no pull request, not part of `CI ok`) | `contents: read` | `actions/checkout`, `actions/setup-node`, `actions/setup-go` (by commit), **`vmactions/freebsd-vm`** (by tag, like `ci.yml`) |

`release.yml` names every action by its commit and GoReleaser by an exact version. The two in bold come from third parties: `vmactions/freebsd-vm` runs the FreeBSD VM that builds and tests the tree, and
`goreleaser/goreleaser-action` runs with the token that can write the release. They matter most; the `actions/*` ones
are GitHub's own and should be pinned the same way.

The web-e2e job rewrites the runner's apt mirror list as root; every mirror in it is an `https` URL, and apt checks the
signed package indexes whatever the transport.

## The FreeBSD package mirror

The VM image of `vmactions/freebsd-vm` brings `rsync` (the action's own file sync runs on it) and no `curl`. A VM job
looks in the image for what its part needs and reads the package mirror (`pkg.FreeBSD.org`, `FreeBSD-ports`, the
quarterly branch) only for what is missing: `curl`, and for the Go tests and e2e shard 2 a PostgreSQL server. The
prepared image is cached, so a run whose prepare step has the same text does not read the mirror at all. Packages are
checked by pkg against the FreeBSD keys in `/usr/share/keys/pkg`.

When the mirror does not answer, the install is tried three times and then the job fails with the line `the package
mirror ... did not give ... in three attempts`. `ci-rerun-mirror.yml` runs the failed jobs once more when every failed
job of a CI run ended with that line; a run with another failure is left alone, and a run is retried at most once.

## The nightly Linux jail test

`linux-e2e.yml` runs the Linux jail block of `scripts/ci-freebsd-e2e.sh` (`JAILOPS_E2E_PART=linux`) on its own FreeBSD VM: a thick-only layout, a Linux
shell, a volume attached before the start and to the running jail (mounted in the root and below `compat/linux`) and the detach that takes both away. It
builds an Alpine image from `dl-cdn.alpinelinux.org`; when that host does not answer in four tries the script ends with status 75 and the line `SKIPPED`,
and the workflow ends green with a notice. Every other failure is a red run in the Actions tab; no issue is opened.

## Pinning an action to a commit

Run, with the GitHub CLI signed in, for each action and the tag the workflow uses now:

    gh api repos/vmactions/freebsd-vm/commits/v1 --jq .sha
    gh api repos/goreleaser/goreleaser-action/commits/v6 --jq .sha
    gh api repos/actions/checkout/commits/v7 --jq .sha
    gh api repos/actions/setup-go/commits/v7 --jq .sha
    gh api repos/actions/setup-node/commits/v7 --jq .sha
    gh api repos/actions/cache/commits/v4 --jq .sha
    gh api repos/actions/upload-artifact/commits/v7 --jq .sha
    gh api repos/actions/download-artifact/commits/v8 --jq .sha

A tag that names a release can also be read as `gh api repos/OWNER/REPO/git/ref/tags/TAG --jq .object.sha`; when that
object is an annotated tag, `gh api repos/OWNER/REPO/git/tags/SHA --jq .object.sha` gives the commit. Both ways must
give the same commit as the first command.

Then replace each `uses:` line, keeping the tag as a comment so that a reader (and an update tool) knows the version:

    - uses: vmactions/freebsd-vm@<40-character sha> # v1

Check before you commit:

    grep -n 'uses:' .github/workflows/*.yml | grep -v '@[0-9a-f]\{40\} # '

prints nothing when every action is pinned.

## Updating a pinned action

Read the action's release notes and the diff between the pinned commit and the new one
(`gh api repos/OWNER/REPO/compare/OLD...NEW --jq '.files[].filename'`), above all for `vmactions/freebsd-vm` and
`goreleaser/goreleaser-action`, then pin the new commit as above.
