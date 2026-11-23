#!/bin/sh
# make-upgrade-fixture.sh VERSION    write the upgrade fixture of a release: the state a host of that release holds
#
# The fixture is what the release itself writes for a small fleet (a project, a storage, two networks, an image, two
# firewall policies, an address set, a volume, a WireGuard tunnel, two jails, a database with a user, a token and an
# audit entry), plus the configuration files the release ships and the keys it knows. internal/upgradecheck opens every
# fixture with the CURRENT code on every `go test`: that is the guarantee that a host of any release since 0.8.0
# upgrades in place.
#
# `just release-pr` runs it for the newest tag when master lacks that tag's fixture, and puts the directory into the
# release PR. By hand: `scripts/make-upgrade-fixture.sh X` for a release that exists, then commit the directory.
#
#   VERSION     a tag: v0.8.6 or 0.8.6
#   GOPROXY     passed through (set it to off to use the module cache only)
set -eu
export GOTOOLCHAIN=auto # the go.work asks for a newer Go than a distribution may have; the Justfile does the same
cd "$(dirname "$0")/.."
[ $# -eq 1 ] || { echo "usage: scripts/make-upgrade-fixture.sh VERSION (v0.8.6 or 0.8.6)" >&2; exit 2; }
v=${1#v}
tag=v$v
git rev-parse -q --verify "refs/tags/$tag" >/dev/null || { echo "make-upgrade-fixture: the tag $tag does not exist here (git fetch --tags)" >&2; exit 1; }
out=$PWD/internal/store/testdata/upgrade/$tag
gen=$PWD/scripts/upgrade-fixture/gen.go.txt
work=$(mktemp -d "${TMPDIR:-/tmp}/upgrade-fixture.XXXXXX")
trap 'git worktree remove --force "$work/tree" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT INT TERM

git worktree add --detach -q "$work/tree" "$tag"
mkdir -p "$work/tree/cmd/upgrade-fixture-gen"
cp "$gen" "$work/tree/cmd/upgrade-fixture-gen/main.go"
rm -rf "$out"
(cd "$work/tree" && go run ./cmd/upgrade-fixture-gen -version "$tag" -out "$out")

# what the release ships to a host, and the keys it knows
mkdir -p "$out/config"
for f in jailopsd.yaml backupd.yaml priv.conf.sample; do
	git show "$tag:packaging/$f" > "$out/config/$f" 2>/dev/null || rm -f "$out/config/$f"
done
git show "$tag:cmd/jailops-priv/main.go" |
	grep -oE 'fs\.(String|Bool|Var|Int|Duration)(Var)?\([^,]*, *"[a-z0-9-]+"' | grep -oE '"[a-z0-9-]+"' | tr -d '"' |
	grep -vx config | sort -u > "$out/config/priv-keys.txt"
[ -s "$out/config/priv-keys.txt" ] || { echo "make-upgrade-fixture: no priv.conf keys found at $tag" >&2; exit 1; }

# SQLite side files of an open database are not part of the fixture
rm -f "$out"/data/*-wal "$out"/data/*-shm "$out"/state/.lock
echo "fixture of $tag written to ${out#"$PWD"/}"
