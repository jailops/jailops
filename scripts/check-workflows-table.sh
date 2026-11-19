#!/bin/sh
# Keeps the workflow walkthrough honest (test/workflows/daily-tasks.md, docs/release-engineering.md "Before a release").
# The table of daily tasks, the steps of scripts/e2e-workflows.sh and the gaps of docs/roadmap.md must agree:
#   - every row has a unique ID, a rating of OK, MORE STEPS or MISSING, and a Step that is `-` or the ID itself;
#   - an OK row has no Ref and a step that runs (`row`) or is covered by another script (`row_elsewhere`);
#   - a MISSING row has a Ref and a `row_pending` that starts to run when the command exists;
#   - a MORE STEPS row has a Ref, and its step is `-` or a step that exists;
#   - a Ref `W-name` is an entry of the roadmap section "Gaps found by the workflow walkthrough", and every entry of that
#     section is the Ref of a row (an entry of a command that exists is removed with the change that adds it); the Ref
#     `docs/roadmap.md#long-term-roadmap` names a long-term item (what works between hosts) and is accepted as it is;
#   - every step of the script belongs to a row.
# Usage: sh scripts/check-workflows-table.sh [TABLE [SCRIPT [ROADMAP]]]   Run by `just check`, by CI and by `just release-pr`.
set -eu
cd "$(dirname "$0")/.."
table=${1:-test/workflows/daily-tasks.md}
script=${2:-scripts/e2e-workflows.sh}
roadmap=${3:-docs/roadmap.md}
for f in "$table" "$script" "$roadmap"; do
	[ -f "$f" ] || { echo "check-workflows-table: $f does not exist" >&2; exit 1; }
done

# the rows: id, rating, ref, step, separated by a tab (a `\|` inside a cell is a pipe of the text)
rows=$(awk '
	/^\| *---/ { next }
	/^\| *ID *\|/ { next }
	/^\|/ {
		line = $0
		gsub(/\\\|/, "\001", line)
		n = split(line, c, "|")
		if (n != 8) { printf "BAD\t%s\t\t\n", NR; next }
		for (i = 2; i <= 7; i++) { gsub(/^ +| +$/, "", c[i]); gsub(/\001/, "|", c[i]) }
		gsub(/`/, "", c[6])
		printf "%s\t%s\t%s\t%s\n", c[2], c[5], c[6], c[7]
	}' "$table")
[ -n "$rows" ] || { echo "check-workflows-table: $table has no rows" >&2; exit 1; }

# the steps of the script: kind and id
steps=$(grep -E '^[[:space:]]*row(_pending|_elsewhere)?[[:space:]]+[a-z0-9-]+' "$script" |
	sed -E 's/^[[:space:]]*(row[a-z_]*)[[:space:]]+([a-z0-9-]+).*/\1 \2/' || true)
# the gaps of the roadmap: the W- names of the entries
gaps=$(sed -n '/^## Gaps found by the workflow walkthrough/,$p' "$roadmap" | grep -oE '^- \*\*`W-[a-z0-9-]+`\*\*' | sed -E 's/^- \*\*`(W-[^`]*)`\*\*/\1/' || true)

bad=0
err() { echo "check-workflows-table: $*" >&2; bad=1; }
kind_of() { printf '%s\n' "$steps" | awk -v id="$1" '$2 == id { print $1; exit }'; }

seen=" "
refs=" "
tab=$(printf '\t')
while IFS=$tab read -r id rating ref step; do
	if [ "$id" = BAD ]; then err "$table line $rating: a row needs six cells"; continue; fi
	printf '%s' "$id" | grep -Eq '^[a-z0-9]+(-[a-z0-9]+)*$' || { err "row '$id': the ID is lowercase words joined by hyphens"; continue; }
	case "$seen" in *" $id "*) err "row $id: the ID is used twice" ;; esac
	seen="$seen$id "
	kind=$(kind_of "$id")
	case $step in - | "$id") ;; *) err "row $id: the step is '-' or $id, not '$step'" ;; esac
	case $rating in
	OK)
		[ "$ref" = - ] || err "row $id: an OK row has no Ref (it has '$ref')"
		case $kind in row | row_elsewhere) ;; *) err "row $id: an OK row needs 'row $id' or 'row_elsewhere $id' in $script" ;; esac
		[ "$step" = "$id" ] || err "row $id: an OK row names its step"
		;;
	MISSING)
		case $kind in row_pending) ;; *) err "row $id: a MISSING row needs 'row_pending $id' in $script, a step that starts to run when the command exists" ;; esac
		[ "$step" = "$id" ] || err "row $id: a MISSING row names its pending step"
		;;
	"MORE STEPS")
		if [ "$step" != - ] && [ -z "$kind" ]; then err "row $id: no step $id in $script"; fi
		;;
	*) err "row $id: the rating is OK, MORE STEPS or MISSING, not '$rating'" ;;
	esac
	case $rating in
	MISSING | "MORE STEPS")
		case $ref in
		docs/roadmap.md#long-term-roadmap) ;; # between hosts: a long-term item of the roadmap, whose section another change writes
		W-*)
			printf '%s\n' "$gaps" | grep -qx "$ref" || err "row $id: $ref is not an entry of the section 'Gaps found by the workflow walkthrough' in $roadmap"
			refs="$refs$ref "
			;;
		*) err "row $id: a $rating row needs a Ref W-name of $roadmap (it has '$ref')" ;;
		esac
		;;
	esac
done <<ROWS
$rows
ROWS

# every step belongs to a row, and every gap to a row that is not OK
for id in $(printf '%s\n' "$steps" | awk '{print $2}'); do
	case "$seen" in *" $id "*) ;; *) err "$script: step $id has no row in $table" ;; esac
done
for g in $gaps; do
	case "$refs" in *" $g "*) ;; *) err "$roadmap: the gap $g is the Ref of no row that is not OK (remove it when the command exists)" ;; esac
done

[ "$bad" = 0 ] || exit 1
n=$(printf '%s\n' "$rows" | wc -l | tr -d ' ')
echo "check-workflows-table: ok ($n rows)"
