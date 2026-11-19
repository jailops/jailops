#!/bin/sh
# Tests of scripts/vm-lock.sh: two takers serialize, a lock whose owner is gone is taken over, only the owner releases,
# and a run releases the lock whatever its command does. POSIX sh, no VM, no network.
set -u
cd "$(dirname "$0")/.."
lock="sh scripts/vm-lock.sh"
work=$(mktemp -d)
trap 'kill $holder 2>/dev/null; rm -rf "$work"' EXIT INT TERM
export VM_LOCK_DIR="$work" VM_LOCK_POLL=1
holder=
fails=0
ok() { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; fails=$((fails + 1)); }
expect_rc() { # expect_rc WANT DESCRIPTION COMMAND...
	want=$1; desc=$2; shift 2
	"$@" >"$work/out" 2>&1
	rc=$?
	if [ "$rc" = "$want" ]; then ok "$desc"; else bad "$desc (exit $rc, want $want: $(tr '\n' ' ' <"$work/out"))"; fi
}
has() { # has TEXT DESCRIPTION: the last command's output names TEXT
	if grep -q "$1" "$work/out"; then ok "$2"; else bad "$2 (output: $(tr '\n' ' ' <"$work/out"))"; fi
}

expect_rc 0 "a VM nobody holds is free" $lock status 2229
expect_rc 2 "a port that is not a number is refused" $lock status abc

# a holder process that stays alive
sleep 120 &
holder=$!
VM_LOCK_OWNER_PID=$holder VM_LOCK_LABEL=other-run $lock acquire 2229 0 >/dev/null 2>&1
expect_rc 1 "a held VM is not free" $lock status 2229
has other-run "status names the holder"
expect_rc 75 "an acquire that cannot wait gives up with 75" $lock acquire 2229 0
has other-run "and names the holder"
expect_rc 0 "another VM is not affected" $lock acquire 2228 0
VM_LOCK_OWNER_PID=$$ $lock release 2228 >/dev/null 2>&1

expect_rc 1 "a process that does not own the lock cannot release it" $lock release 2229
expect_rc 1 "and the VM is still held" $lock status 2229
expect_rc 0 "--force releases it" $lock release 2229 --force
expect_rc 0 "after the release the VM is free" $lock status 2229

# a lock whose owner is gone is taken over
sh -c 'exit 0' & gone=$!
wait $gone
mkdir "$work/jailops-vm-lock-2229"
printf '%s dead-run %s\n' "$gone" "$(date +%s)" >"$work/jailops-vm-lock-2229/owner"
expect_rc 0 "a lock of a process that is gone is taken over" env VM_LOCK_OWNER_PID=$$ $lock acquire 2229 0
has "took over" "and it says so"
$lock release 2229 >/dev/null 2>&1
# a lock directory without an owner file and older than a minute is stale too
mkdir "$work/jailops-vm-lock-2229"
touch -t 200001010000 "$work/jailops-vm-lock-2229"
expect_rc 0 "an old lock without an owner is taken over" env VM_LOCK_OWNER_PID=$$ $lock acquire 2229 0
$lock release 2229 >/dev/null 2>&1

# two takers serialize: their start and end lines never interleave
log="$work/log"
: >"$log"
take() { $lock run 2229 1 -- sh -c "echo start $1 >>'$log'; sleep 2; echo end $1 >>'$log'"; }
take a & p1=$!
take b & p2=$!
wait $p1 $p2
if [ "$(wc -l <"$log" | tr -d ' ')" = 4 ] && { [ "$(sed -n '1p;2p' "$log" | tr '\n' ' ')" = "start a end a " ] || [ "$(sed -n '1p;2p' "$log" | tr '\n' ' ')" = "start b end b " ]; }; then
	ok "two runs on one VM do not overlap"
else
	bad "two runs on one VM overlapped: $(tr '\n' ' ' <"$log")"
fi

# run releases the lock, and passes the exit status of its command on
expect_rc 7 "run passes the status of its command" $lock run 2229 1 -- sh -c 'exit 7'
expect_rc 0 "and the VM is free after a failing command" $lock status 2229
expect_rc 2 "run without a command is refused" $lock run 2229 1 --

[ "$fails" = 0 ] || { echo "vm-lock-test: $fails failed" >&2; exit 1; }
echo "vm-lock-test: ok"
