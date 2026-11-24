#!/bin/sh
# Tests of scripts/check-workflows-table.sh: a table, a script and a roadmap that agree pass, and each way they can disagree
# fails with a message that names it. Run by `just check` and by CI.
set -u
cd "$(dirname "$0")/../.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fails=0

head='| ID | A jail admin expects to | Command | Rating | Ref | Step |
|---|---|---|---|---|---|'
good_script='row make "make"
row_pending upgrade "upgrade"
row_elsewhere sample "sample"'
good_roadmap='# Roadmap

## Gaps found by the workflow walkthrough

- **`W-upgrade`**: the command.
- **`W-two-steps`**: the other command.'
good_table="$head
| make | make a jail | \`jailops create A\\|B\` | OK | - | make |
| upgrade | upgrade a jail | none | MISSING | W-upgrade | upgrade |
| sample | do a thing | \`jailops sample\` | OK | - | sample |
| two | do two things | a, then b | MORE STEPS | W-two-steps | - |"

# case NAME WANT-RC WANT-MESSAGE TABLE SCRIPT ROADMAP
run() {
	name=$1 want=$2 msg=$3 table=$4 script=$5 roadmap=$6
	printf '%s\n' "$table" > "$tmp/table.md"; printf '%s\n' "$script" > "$tmp/script.sh"; printf '%s\n' "$roadmap" > "$tmp/roadmap.md"
	out=$(sh scripts/check-workflows-table.sh "$tmp/table.md" "$tmp/script.sh" "$tmp/roadmap.md" 2>&1); rc=$?
	if [ "$want" = 0 ] && [ $rc -eq 0 ]; then echo "ok: $name"; return; fi
	if [ "$want" = fail ] && [ $rc -ne 0 ] && printf '%s\n' "$out" | grep -Fq -- "$msg"; then echo "ok: $name"; return; fi
	echo "FAIL: $name (rc $rc, wanted $want '$msg')"; printf '%s\n' "$out" | sed 's/^/    /'; fails=$((fails + 1))
}
repl() { printf "%s\n" "$1" | sed "s@$2@$3@"; }

run "rows, steps and gaps that agree" 0 "" "$good_table" "$good_script" "$good_roadmap"
run "an OK row without a step" fail "an OK row needs 'row make'" "$good_table" "row_pending upgrade \"x\"
row_elsewhere sample \"a\"" "$good_roadmap"
run "an OK row whose step is pending" fail "an OK row needs 'row make'" "$good_table" "row_pending make \"x\"
row_pending upgrade \"x\"
row_elsewhere sample \"a\"" "$good_roadmap"
run "a MISSING row without a pending step" fail "a MISSING row needs 'row_pending upgrade'" "$good_table" "row make \"m\"
row upgrade \"u\"
row_elsewhere sample \"a\"" "$good_roadmap"
run "a MISSING row without a Ref" fail "needs a Ref W-name" "$(repl "$good_table" 'MISSING | W-upgrade' 'MISSING | -')" "$good_script" "$good_roadmap"
run "a Ref that is not in the roadmap" fail "W-gone is not an entry" "$(repl "$good_table" 'W-upgrade' 'W-gone')" "$good_script" "$good_roadmap"
run "a gap of the roadmap that no row names" fail "the gap W-extra is the Ref of no row" "$good_table" "$good_script" "$good_roadmap
- **\`W-extra\`**: nothing refers to it."
run "an OK row that has a Ref" fail "an OK row has no Ref" "$(repl "$good_table" 'OK | - | make' 'OK | W-upgrade | make')" "$good_script" "$good_roadmap"
run "a step that has no row" fail "step orphan has no row" "$good_table" "$good_script
row orphan \"o\"" "$good_roadmap"
run "an ID used twice" fail "the ID is used twice" "$good_table
| make | again | x | OK | - | make |" "$good_script" "$good_roadmap"
run "a rating that does not exist" fail "the rating is OK, MORE STEPS or MISSING" "$(repl "$good_table" '| OK | - | sample' '| FINE | - | sample')" "$good_script" "$good_roadmap"
run "a row with the wrong number of cells" fail "a row needs six cells" "$good_table
| half | row |" "$good_script" "$good_roadmap"
run "an ID that is not lowercase words" fail "the ID is lowercase words" "$(repl "$good_table" '| make |' '| Make_It |')" "$good_script" "$good_roadmap"
run "a step that is not the row's ID" fail "the step is '-' or" "$(repl "$good_table" '| OK | - | make |' '| OK | - | other |')" "$good_script" "$good_roadmap"
run "a MORE STEPS row names a step that does not exist" fail "no step two in" "$(repl "$good_table" '| - |$' '| two |')" "$good_script" "$good_roadmap"
run "a long-term item is accepted before its section exists" 0 "" "$good_table
| later | do it between hosts | none (later) | MISSING | docs/roadmap.md#long-term-roadmap | later |" "$good_script
row_pending later \"l\"" "$good_roadmap"
run "another anchor of the roadmap is refused" fail "needs a Ref W-name" "$good_table
| later | do it between hosts | none | MISSING | docs/roadmap.md#elsewhere | later |" "$good_script
row_pending later \"l\"" "$good_roadmap"
run "the real table, script and roadmap" 0 "" "$(cat test/workflows/daily-tasks.md)" "$(cat scripts/e2e-workflows.sh)" "$(cat docs/roadmap.md)"

[ $fails -eq 0 ] || { echo "$fails of the cases failed" >&2; exit 1; }
echo "check-workflows-table tests: ok"
