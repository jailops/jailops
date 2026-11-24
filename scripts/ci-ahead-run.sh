#!/bin/sh
# ci-ahead-run.sh: run parts of the FreeBSD tests on a VM that scripts/ci-boot-vm.sh booted, and write what happened.
#
#   sh scripts/ci-ahead-run.sh PORT KEYFILE OUTDIR "e2e:1 e2e:2 pf:a" PKG...
#
# PORT and KEYFILE are the ssh port and the private key of the VM (root). The parts are e2e:1 to e2e:3 and pf:a to pf:d. The VM is
# brought to the newest snapshot of its branch, gets the packages PKG..., and then every part runs from a clean state, one after the
# other: a part that fails does not stop the next. OUTDIR gets summary.md (one line per part with PASS or FAIL, the minutes and the
# first failing lines; also appended to $GITHUB_STEP_SUMMARY when that is set) and logs/PART.log. The binaries are the ones in
# BIN_DIR (default vm-bin: `jailops-freebsd-amd64` and the others, as the CI job builds them). The exit status is 1 when a part
# failed; a VM that cannot be updated or given its packages is written down as "environment" and is no failure of the tree.
# Used by .github/workflows/freebsd-ahead.yml and by hand against any VM of the repository's scripts.
set -u
cd "$(dirname "$0")/.."
[ $# -ge 4 ] || { sed -n 2,5p "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
port=$1 key=$2 out=$3 parts=$4; shift 4
bin=${BIN_DIR:-vm-bin}
[ -f "$bin/jailops-freebsd-amd64" ] || { echo "ci-ahead-run: no binaries in $bin (jailops-freebsd-amd64 ...)" >&2; exit 2; }
# an artifact does not keep the executable bit of its files
chmod +x "$bin"/*-freebsd-amd64
mkdir -p "$out/logs"
sum=$out/summary.md
: > "$sum"
say() { printf '%s\n' "$*" | tee -a "$sum"; [ -z "${GITHUB_STEP_SUMMARY:-}" ] || printf '%s\n' "$*" >> "$GITHUB_STEP_SUMMARY"; }
sshv() {
	ssh -i "$key" -p "$port" -o BatchMode=yes -o ConnectTimeout=10 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no \
		-o UserKnownHostsFile=/dev/null -l root 127.0.0.1 "$@"
}

say "### FreeBSD $(sshv 'freebsd-version -u' 2>/dev/null) on port $port"
say ""
{ sh scripts/ci-boot-vm.sh update "$port" "$key" 2>&1 | sed 's/^/- /' ; } | while IFS= read -r l; do say "$l"; done
if [ $# -gt 0 ]; then
	sh scripts/ci-boot-vm.sh install "$port" "$key" "$@" 2>&1 | tail -3 | sed 's/^/- /' | while IFS= read -r l; do say "$l"; done
fi
say ""
say "| part | result | minutes | first failing lines |"
say "|---|---|---|---|"

# what the VM needs: the scripts and the test data of the tree and the binaries
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bin"
cp "$bin"/* "$stage/bin/"
tar cf "$stage/files.tar" scripts/vm-clean.sh scripts/pkg-build.sh scripts/gen-share.sh scripts/ci-freebsd-e2e.sh scripts/ci-freebsd-pf.sh \
	scripts/e2e-workflows.sh internal/render/testdata packaging test version.txt README.md LICENSE.md docs/design.md \
	-C "$stage" bin
sshv 'rm -rf /tmp/jopsci && mkdir -p /tmp/jopsci && tar xf - -C /tmp/jopsci' < "$stage/files.tar" || { say "environment: the tree could not be copied to the VM"; exit 1; }
release=$(sshv 'freebsd-version -u' | sed 's/-p[0-9]*$//')

rc=0
for part in $parts; do
	kind=${part%%:*}; shard=${part#*:}
	case $kind in
	e2e) envs="JAILOPS_E2E_PART=e2e JAILOPS_E2E_SHARD=$shard JAILOPS_PF_SHARD=all" ;;
	pf) envs="JAILOPS_E2E_PART=pf JAILOPS_E2E_SHARD=all JAILOPS_PF_SHARD=$shard" ;;
	*) say "| $part | skipped | 0 | not a part of this script |"; continue ;;
	esac
	log=$out/logs/$kind-$shard.log
	t0=$(date +%s)
	# the part gets 55 minutes; PF_KEEP_GOING=1 so that a failed phase does not hide the ones after it
	echo "ci-ahead-run: $part starts"
	timeout 3300 ssh -i "$key" -p "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		-o ServerAliveInterval=30 -l root 127.0.0.1 \
		"sh /tmp/jopsci/scripts/vm-clean.sh; cd /tmp/jopsci && kldload pf 2>/dev/null; PF_KEEP_GOING=1 $envs JAILOPS_E2E_IMAGE_RELEASE=$release JAILOPS_BIN_DIR=/tmp/jopsci/bin FORGE_BIN=/tmp/jopsci/bin/forge-freebsd-amd64 sh scripts/ci-freebsd-e2e.sh" \
		> "$log" 2>&1 &
	spid=$!
	# a line every thirty seconds with the step the part is in (the last "== ..." line of its log), so that a long part is seen to move
	shown=""
	while kill -0 "$spid" 2>/dev/null; do
		sleep 30 # sleep-ok: the progress line of a part
		cur=$(grep -a '^== ' "$log" | tail -1 | cut -c1-110)
		if [ "$cur" != "$shown" ]; then echo "ci-ahead-run: $part: $cur  [$(( ($(date +%s) - t0) / 60 )) min]"; shown=$cur
		else echo "ci-ahead-run: $part: still in the same step  [$(( ($(date +%s) - t0) / 60 )) min]"; fi
	done
	if wait "$spid"; then r=PASS; else r=FAIL; rc=1; fi
	first=$(grep -E '^FAIL |^FAILED' "$log" | head -3 | tr '\n|' '  ' | cut -c1-300)
	say "| $part | $r | $(( ($(date +%s) - t0) / 60 )) | ${first:--} |"
	# the end of the log of a failed part goes to the log of the job: the artifact has all of it, this is what is read first
	if [ "$r" = FAIL ]; then echo "---- end of the log of $part"; tail -40 "$log"; echo "----"; fi
	# a run by hand ends at the first failed part (AHEAD_FAIL_FAST=1); the scheduled run runs every part
	if [ "$r" = FAIL ] && [ "${AHEAD_FAIL_FAST:-0}" = 1 ]; then say "(the parts after $part were not run: fail fast)"; break; fi
done
exit $rc
