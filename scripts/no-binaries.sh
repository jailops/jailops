#!/bin/sh
# Fails when a compiled executable or a very large file is tracked by git. Binaries belong in releases and in
# packages, never in the repository (they cannot be reviewed and stay in the history for ever).
#
#   sh scripts/no-binaries.sh
set -eu
cd "$(dirname "$0")/.."

bad=0
for f in $(git ls-files); do
	[ -f "$f" ] || continue
	case $(head -c 4 "$f" | od -An -tx1 | tr -d ' \n') in
	7f454c46 | cffaedfe | cefaedfe | feedface | feedfacf | cafebabe)
		echo "x compiled executable in git: $f" >&2
		bad=1
		;;
	esac
	size=$(wc -c < "$f")
	if [ "$size" -gt 5000000 ]; then
		echo "x $f is $size bytes: files over 5 MB do not belong in git" >&2
		bad=1
	fi
done
[ "$bad" = 0 ] && echo "no binaries in git"
exit "$bad"
