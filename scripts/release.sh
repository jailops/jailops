#!/usr/bin/env bash
# Cut a release straight on master (no PR): version.txt, CHANGELOG.md, commit, annotated tag, push. No tests here: the
# release workflow checks gofmt and vet on the tagged commit before GoReleaser publishes, and CI runs the tests on every
# push to master.
#
#   scripts/release.sh vX.Y.Z          (normally: just release vX.Y.Z)
#   DRY=1 scripts/release.sh vX.Y.Z    (just release-dry vX.Y.Z) the same checks, changes and pushes nothing
#
# The tag push starts .github/workflows/release.yml (GoReleaser -> GitHub release). Run by a person, never by an agent.
set -euo pipefail
cd "$(dirname "$0")/.."

DRY=${DRY:-0}
TAG=${1:-}
BRANCH=master
REMOTE=origin

die() { echo "release: $*" >&2; exit 1; }
say() { echo "==> $*"; }
act() { if [ "$DRY" = 1 ]; then echo "[dry-run] would: $*"; else say "$*"; fi; }

[ -n "$TAG" ] || die "usage: just release vX.Y.Z"
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'
[[ "$TAG" =~ $semver ]] || die "'$TAG' is not a semantic version tag like v0.1.0 or v0.2.0-rc.1"
VERSION=${TAG#v}
DATE=$(date -u +%Y-%m-%d)

git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"
[ -z "$(git status --porcelain)" ] || die "working tree is not clean (commit or stash first)"
[ "$(git rev-parse --abbrev-ref HEAD)" = "$BRANCH" ] || die "not on $BRANCH (releases are cut from $BRANCH only)"
git remote get-url "$REMOTE" >/dev/null 2>&1 || die "no git remote '$REMOTE'"
git fetch --quiet --tags "$REMOTE" || die "cannot fetch $REMOTE"
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then die "tag $TAG already exists locally"; fi
if git ls-remote --exit-code --tags "$REMOTE" "refs/tags/$TAG" >/dev/null 2>&1; then die "tag $TAG already exists on $REMOTE"; fi
if git rev-parse -q --verify "refs/remotes/$REMOTE/$BRANCH" >/dev/null; then
  [ "$(git rev-list --count "HEAD..$REMOTE/$BRANCH")" = 0 ] || die "$BRANCH is behind $REMOTE/$BRANCH: git pull --ff-only first"
fi
say "git state ok: clean tree on $BRANCH, $TAG is new"

[ -f CHANGELOG.md ] || die "CHANGELOG.md is missing"
grep -q '^## Unreleased' CHANGELOG.md || die "CHANGELOG.md has no '## Unreleased' section"
if grep -q "^## $VERSION\b" CHANGELOG.md; then die "CHANGELOG.md already has a section for $VERSION"; fi
body=$(awk '/^## Unreleased/{f=1;next} /^## /{f=0} f' CHANGELOG.md | tr -d '[:space:]')
[ -n "$body" ] || die "the Unreleased section of CHANGELOG.md is empty"
say "changelog ok"

if [ "$DRY" = 1 ] || [ "${CHECKS:-0}" = 1 ]; then
  say "gofmt"
  bad=$( (gofmt -l . ; cd sdk && gofmt -l .) | grep -v '^web/node_modules/' || true)
  [ -z "$bad" ] || { echo "$bad" >&2; die "gofmt: unformatted files above"; }
  say "go vet (both modules)"
  go vet ./...
  (cd sdk && go vet ./...)
  say "the TypeScript wire types match sdk/api"
  go run ./scripts/gen-ts -check
  say "FreeBSD cross-build"
  for arch in amd64 arm64; do
    for b in jailops jailopsd jailops-priv jailops-sync jailops-backupd; do
      GOOS=freebsd GOARCH=$arch CGO_ENABLED=0 go build -o /dev/null "./cmd/$b"
    done
  done
  say "all checks passed (tests: just test, or CI)"
else
  say "local checks skipped (the release workflow runs gofmt and vet on the tag): use just release-dry for the full set"
fi

act "write version.txt = $VERSION"
act "CHANGELOG.md: rename '## Unreleased' to '## $VERSION - $DATE' and open a new empty Unreleased"
act "git commit -m 'Release $TAG' (version.txt, CHANGELOG.md)"
act "git tag -a $TAG -m 'jailops $TAG'"
act "git push --atomic $REMOTE $BRANCH refs/tags/$TAG   (the tag starts the Release workflow)"
if [ "$DRY" = 1 ]; then
  echo "[dry-run] nothing was changed or pushed"
  exit 0
fi

printf '%s\n' "$VERSION" > version.txt
awk -v v="$VERSION" -v d="$DATE" '
  /^## Unreleased/ && !done { print "## Unreleased"; print ""; print "## " v " - " d; done=1; next }
  { print }' CHANGELOG.md > CHANGELOG.md.new
mv CHANGELOG.md.new CHANGELOG.md

git add version.txt CHANGELOG.md
git commit -q -m "Release $TAG"
git tag -a "$TAG" -m "jailops $TAG"
if ! git push --atomic "$REMOTE" "$BRANCH" "refs/tags/$TAG"; then
  echo "release: push failed. Local commit and tag $TAG exist. Fix the cause, then:" >&2
  echo "  git push --atomic $REMOTE $BRANCH refs/tags/$TAG" >&2
  echo "or undo:  git tag -d $TAG && git reset --hard HEAD~1" >&2
  exit 1
fi
say "released $TAG: watch https://github.com/jailops/jailops/actions (workflow: Release)"
