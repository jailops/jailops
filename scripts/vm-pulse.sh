#!/bin/sh
# vm-pulse.sh [--update] [PORT...]    run the FreeBSD tests on the test VMs that are not the gate, to see what is coming
#
#   sh scripts/vm-pulse.sh              the VMs of 15.2 (2228) and 16-CURRENT (2230)
#   sh scripts/vm-pulse.sh 2230         one VM
#   sh scripts/vm-pulse.sh --update     first bring each VM to the newest snapshot of its branch (pkg upgrade, a reboot when the kernel changed)
#
# Each VM gets the end-to-end shards 1 to 3 and the packet filter shards a to d, one after the other, through scripts/vm-run.sh
# (so under the lock of the VM). A part that fails does not stop the run: the summary, temp/pulse-DATE.txt, lists every part with
# its result and its minutes, and the log of each part is next to it (temp/pulse-DATE-PORT-PART.log). A VM that does not answer
# is skipped with a line in the summary; a VM that cannot be updated says so and is tested as it is, so a broken snapshot is not
# taken for a failure of JailOps. The exit status is 1 when any part failed. It is for a person (the weekly run is the workflow
# freebsd-ahead.yml, which boots the snapshots on the runner), not for CI: these releases are not the gate, and a failure here is news to read, not a reason to stop a change.
set -u
cd "$(dirname "$0")/.."
update=0
[ "${1:-}" = --update ] && { update=1; shift; }
[ $# -gt 0 ] || set -- 2228 2230
day=$(date +%Y-%m-%d)
sum=temp/pulse-$day.txt
key=${VM_KEY:-temp/vm/key}
mkdir -p temp
: > "$sum"
rc=0
sshv() { port=$1; shift; ssh -i "$key" -p "$port" -o BatchMode=yes -o ConnectTimeout=10 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no \
	-o UserKnownHostsFile=/dev/null -l root 127.0.0.1 "$@"; }
# update_vm PORT: the newest packages of the branch the VM follows (scripts/ci-boot-vm.sh update: a reboot when the kernel changed). A VM
# that cannot be updated says so in the summary and is tested as it is.
update_vm() { sh scripts/ci-boot-vm.sh update "$1" "$key" 2>&1 | sed 's/^/  /' | tee -a "$sum"; }
for port in "$@"; do
	if ! ssh -i "$key" -p "$port" -o BatchMode=yes -o ConnectTimeout=10 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no \
		-o UserKnownHostsFile=/dev/null -l root 127.0.0.1 true >/dev/null 2>&1; then
		echo "$port: no answer (the VM is not running): skipped" | tee -a "$sum"
		continue
	fi
	rel=$(ssh -i "$key" -p "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		-l root 127.0.0.1 'freebsd-version -u' 2>/dev/null)
	echo "$port: FreeBSD $rel" | tee -a "$sum"
	[ $update = 1 ] && update_vm "$port"
	for part in "e2e 1" "e2e 2" "e2e 3" "pf a" "pf b" "pf c" "pf d"; do
		log=temp/pulse-$day-$port-$(echo "$part" | tr ' ' '-').log
		t0=$(date +%s)
		# a shell cannot be told to run two words as one argument: the part and the shard are separate arguments
		# shellcheck disable=SC2086
		if PF_KEEP_GOING=1 sh scripts/vm-run.sh "$port" $part >"$log" 2>&1; then r=PASS; else r=FAIL; rc=1; fi
		printf '  %-6s %s  %s min\n' "$part" "$r" "$(( ($(date +%s) - t0) / 60 ))" | tee -a "$sum"
		[ "$r" = PASS ] || grep -E '^FAIL |^FAILED' "$log" | head -3 | sed 's/^/         /' | tee -a "$sum"
	done
done
echo "summary: $sum"
exit $rc
