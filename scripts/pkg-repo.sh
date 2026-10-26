#!/bin/sh
# Turns the packages of pkg-build.sh into signed pkg repositories. Runs on FreeBSD.
#
#   scripts/pkg-repo.sh REPODIR PRIVATE_KEY
#
# For every REPODIR/<ABI>/latest it runs `pkg repo` and signs the catalog with the RSA key. The key never
# leaves the release environment; clients verify with the public key.
set -eu
[ $# -eq 2 ] || { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
repo=$1; key=$2
[ -f "$key" ] || { echo "no such key: $key" >&2; exit 1; }
for d in "$repo"/*/latest; do
	[ -d "$d/All" ] || continue
	echo "== $d"
	pkg repo "$d" "$key"
done
