#!/bin/sh
# From 1.0 on, the frozen surface only grows: a line that a golden file of the API freeze loses (a route, an error code, an enum
# value, a condition reason, an extension, a command or flag, a member of an envelope) fails this check, unless the major version of
# version.txt is raised in the same change. Before 1.0 it only reports. Usage: check-golden-removals.sh [BASE]; BASE is the
# revision to compare with (default HEAD^1, which is the base branch in the merge commit that CI checks out).
set -e
cd "$(dirname "$0")/.."
base=${1:-HEAD^1}
dirs="internal/server/testdata/freeze internal/cli/testdata/freeze"
git rev-parse --verify -q "$base^{commit}" >/dev/null || { echo "check-golden-removals: no revision $base to compare with" >&2; exit 2; }
major() { sed -n '1s/^v\{0,1\}\([0-9][0-9]*\)\..*/\1/p'; }
old=$(git show "$base:version.txt" 2>/dev/null | major)
new=$(major < version.txt)
removed=$(mktemp)
trap 'rm -f "$removed"' EXIT
# shellcheck disable=SC2086
git diff --no-color -U0 "$base" -- $dirs | awk '
	/^\+\+\+ / { next }
	/^--- a\// { file = substr($0, 7); next }
	/^-/ { print file ": " substr($0, 2) }' > "$removed"
if [ ! -s "$removed" ]; then
	echo "check-golden-removals: no line of the frozen surface was removed"
	exit 0
fi
echo "check-golden-removals: lines of the frozen surface that this change removes or rewrites:"
cat "$removed"
if [ "${old:-0}" -lt 1 ]; then
	echo "check-golden-removals: version $old.x is before the freeze of 1.0, so this is only reported"
	exit 0
fi
if [ "${new:-0}" -gt "${old:-0}" ]; then
	echo "check-golden-removals: the major version is raised ($old to $new): removals are allowed"
	exit 0
fi
echo "check-golden-removals: the surface of $old.x only grows. Keep the old line and add the new one, or raise the major version." >&2
exit 1
