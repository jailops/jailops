#!/bin/sh
# The workflow walkthrough on a real host: what a jail administrator does every day, one step per row of
# test/workflows/daily-tasks.md. This file is sourced by scripts/ci-freebsd-e2e.sh when JAILOPS_E2E_PART=workflows, after
# the image, the network lan and the project demo exist; it uses that script's helpers (check, expect, waitfor, step, fail).
#
#   JAILOPS_E2E_PART=workflows sh scripts/ci-freebsd-e2e.sh      (as root, on a disposable FreeBSD machine: just e2e-workflows)
#
# A step is `row ID "the task"` and the commands of that task, checked as the administrator would see them. A command that
# does not exist yet has `row_pending ID "the command"`, which prints PENDING and runs nothing: it becomes a `row` in the change
# that adds the command. A task that another script covers has `row_elsewhere ID "where"`. scripts/check-workflows-table.sh
# keeps these steps and the table in step. The rules of scripts/ci-freebsd-e2e.sh hold here too (no bare sleep, wait for
# conditions, no step that depends on an earlier one unless it made what it needs).

wf_rows=0 wf_pending=0 wf_elsewhere=0
row() { wf_rows=$((wf_rows + 1)); step "row $1: $2"; }
row_pending() { wf_pending=$((wf_pending + 1)); printf '\nPENDING row %s: %s\n' "$1" "$2"; }
row_elsewhere() { wf_elsewhere=$((wf_elsewhere + 1)); printf '\nELSEWHERE row %s: %s\n' "$1" "$2"; }

run_workflows() {
	P="--project demo"
	wf_exec() { j=$1; shift; $J exec $P "$j" -- "$@"; }
	wf_up() { waitfor "the jail $1 answers (an exec waits for the first start)" 180 $J exec $P "$1" -- true; }
	wf_listed() { $J list $P -o name 2>/dev/null | grep -qx "$1"; }
	wf_gone() { ! wf_listed "$1"; }

	row bootstrap-release "the image of the release is ready"
	expect "the image is listed" "$img" $J image list
	expect "it has a first generation" 'g1' $J image show "$img"

	row create-thin-thick "a thin and a thick jail"
	check "a thin jail" $J create wft $P --image "$img" --network lan --storage default --layout thin --start
	check "a thick jail" $J create wfk $P --image "$img" --network lan --storage default --layout thick --start
	wf_up wft
	wf_up wfk
	expect "the thin jail says thin" 'thin' $J jail show wft $P -o yaml
	expect "the thick jail says thick" 'thick' $J jail show wfk $P -o yaml
	expect "both run" 'FreeBSD' wf_exec wfk uname -s

	row patch-one "patch the operating system of one jail"
	check "a file in the jail's data" wf_exec wft sh -c 'echo wf-keep > /var/db/wf-keep'
	check "the next generation of the image" $J image build "$img" --from-generation g1
	expect "a dry run names the move" 'g1 to g2' $J jail upgrade wft $P --dry-run
	check "the jail moves to g2" $J jail upgrade wft $P --generation g2
	wf_up wft
	expect "the data is where it was" 'wf-keep' wf_exec wft cat /var/db/wf-keep
	expect "the jail runs on g2" 'g2' $J list $P

	row patch-all "patch every jail of an image"
	expect "a dry run of the image upgrade names the jails it would move" 'wft|wfk' $J image upgrade "$img" --dry-run

	row_elsewhere upgrade-minor "the jail upgrade block of ci-freebsd-e2e.sh (shard 3: jail upgrade --image to a second image, data kept, rollback across images)"
	row_pending upgrade-major "the same command across a major release also reinstalls the packages of the jail for the new ABI"

	row copy-spec "a jail with the configuration of another"
	check "copy the specification" $J jail copy wft wfc $P
	waitfor "the copy is listed" 30 wf_listed wfc

	row_elsewhere clone-data "the copy block of ci-freebsd-e2e.sh (shard 2: jail copy --data of a running jail)"
	row_elsewhere clone-snapshot "the copy block of ci-freebsd-e2e.sh (jail copy --data --snapshot)"
	row_pending move-host "jailops jail move NAME --to HOST"
	row_elsewhere export-import "the export and import block of ci-freebsd-e2e.sh (a jail leaves as a file and comes back under another name and in another project)"
	row_elsewhere move-project "the export and import block of ci-freebsd-e2e.sh (import into another project on its network)"

	row rename "rename a jail"
	check "rename the copy" $J rename wfc wfr $P -y
	waitfor "the new name is listed" 30 wf_listed wfr
	waitfor "the old name is gone" 30 wf_gone wfc

	row_elsewhere convert-layout "the copy and conversion block of ci-freebsd-e2e.sh (thin to thick and back, data kept)"

	row shell-and-command "a command in a jail, and a shell"
	expect "a command runs and prints" 'FreeBSD' wf_exec wft uname -s
	expect "a command's exit status comes back" '^7$' sh -c "$J exec $P wft -- sh -c 'exit 7'; echo \$?"
	check "the shell of the jail is sh (bash comes from a package mirror: ci-freebsd-e2e.sh covers it)" $J config set wft $P shell sh
	expect "a shell takes input and exits" 'wf-shell' sh -c "printf 'echo wf-shell; exit\n' | $J shell wft $P 2>&1"

	row_elsewhere exec-many "the exec and cp block of ci-freebsd-e2e.sh (exec -l on two jails)"

	row profiles "the same settings for several jails"
	check "a profile" $J profile create wfp $P --set boot.autostart=true
	check "a jail with the profile" $J create wfq $P --image "$img" --network lan --storage default --profile wfp
	expect "the jail carries the profile" 'profiles +wfp' $J config show wfq $P
	expect "and the boot plan has the setting of the profile (autostart)" 'wfq +[0-9]+ +autostart' $J boot

	row_elsewhere apps "the app block of ci-freebsd-e2e.sh (the catalog, app install, app allow)"

	row_elsewhere copy-files "the exec and cp block of ci-freebsd-e2e.sh (checksums both ways, a symlink escape refused)"

	row volume "a volume for a jail, then for another"
	check "a volume" $J volume create wfv $P
	check "attach it to the running jail" $J volume attach wft wfv /srv/wfv $P
	check "the jail writes to it" wf_exec wft sh -c 'echo wf-volume > /srv/wfv/f'
	check "detach it" $J volume detach wft wfv $P
	check "attach it to another jail" $J volume attach wfq wfv /srv/wfv $P
	check "start the other jail" $J start wfq $P
	wf_up wfq
	expect "the data moved with the volume" 'wf-volume' wf_exec wfq cat /srv/wfv/f
	check "detach it again" $J volume detach wfq wfv $P

	row_elsewhere host-dir "the volume block of ci-freebsd-e2e.sh (volume create --from-dir copies a host directory, owners and modes kept, an escape refused)"

	row limits "limits on memory, processes and CPU share"
	if [ "$racct" = 1 ]; then
		check "a jail with limits" $J create wfl $P --image "$img" --network lan --storage default --memory 256M --pids 200 --cpu 1 --start
		wf_up wfl
		expect "the kernel holds the rules" 'jail:demo_wfl' rctl
	else
		echo "SKIPPED: kern.racct.enable is $racct on this host: the rctl rules of the limits are not checked"
	fi
	check "a limit is a key that changes at once" $J config set wft $P limits.disk=5G
	expect "and reads back, in bytes" '^5368709120$' $J config get wft limits.disk $P

	row_elsewhere cpu-pinning "the copy and conversion block of ci-freebsd-e2e.sh (limits.cpuset shows in cpuset -g -j)"

	row expose "let the Internet reach a port of a jail"
	check "expose a port" $J expose wft 8080 $P
	expect_retry "the list names it (the column is read from the rendered firewall, which settles a few seconds after the change)" '8080/tcp' $J list $P
	check "take it back" $J unexpose wft $P

	row snapshot-rollback "snapshot a jail, roll it back"
	check "a snapshot" $J jail snapshot create wft wfs1 $P
	check "data written after it" wf_exec wft sh -c 'echo after > /var/db/wf-after'
	check "roll the data back" $J jail rollback wft $P --data --snapshot wfs1 --confirm-data-loss
	wf_up wft
	expect_refused "what was written after the snapshot is gone" wf_exec wft test -e /var/db/wf-after
	expect "what was written before it stays" 'wf-keep' wf_exec wft cat /var/db/wf-keep

	row boot-order "which jails start at boot, and in which order"
	check "a jail that starts at boot, after wft" $J create wfb $P --image "$img" --network lan --storage default --autostart --boot-priority 20 --depends wft
	expect "the boot plan names it" 'wfb' $J boot

	row destroy "destroy a jail"
	check "destroy the jail" $J delete --stop --confirm-data-loss wfb $P
	waitfor "it is gone" 60 wf_gone wfb

	row edit-config "read and change the configuration of a jail"
	check "set a key" $J config set wft $P env.WF_EDIT=changed
	expect "read it back" 'changed' $J config get wft env.WF_EDIT $P

	row list-monitor "see which jails run and what they use"
	expect "the list names the running jail" 'wft' $J list $P
	expect "the info of a jail" 'wft' $J info wft $P

	row_elsewhere live-usage "the live usage step of ci-freebsd-e2e.sh (jailops top --once -o json)"
	row_pending service-monitor "jailops jail monitor enable NAME restarts a dead service"
	row_elsewhere restore-after-loss "the backup block of ci-freebsd-e2e.sh (the recovery key removed and imported again before a restore)"

	row_elsewhere previous-release "needs two releases on one host: a jail keeps the image it was made from (docs/release-engineering.md, Update a host)"

	step "the walkthrough's own jails go"
	for j in wft wfk wfr wfq wfl; do
		wf_listed "$j" && check "delete $j" $J delete --stop --confirm-data-loss "$j" $P
	done
	$J volume delete wfv $P --confirm-data-loss >/dev/null 2>&1 || true
	$J profile delete wfp $P -y >/dev/null 2>&1 || true
	echo "workflows: $wf_rows rows run, $wf_pending pending (a command that does not exist yet), $wf_elsewhere covered by another script"
}
