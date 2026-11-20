# Running the FreeBSD tests on a VM

This page is for a contributor who runs `scripts/ci-freebsd-e2e.sh` or `scripts/ci-freebsd-pf.sh` on a FreeBSD VM of their own. The unit tests (`just test`) need no VM.

The two scripts install, start and tear down JailOps, its helper, jails, interfaces and a test pool on the machine they run on, so they belong on a disposable VM and never on a host that runs jails you care about. Both are driven from outside: a runner script builds the binaries, copies the scripts to the VM over ssh and starts them there.

## One run at a time

Two runs on one VM disturb each other: the cleanup of one run removes what the other is using, and the second run fails in a step that has nothing to do with its change. Looking for a running `ci-freebsd` process does not prevent that, because a run can start a moment after the look. Take the lock of the VM instead:

    sh scripts/vm-lock.sh run PORT 30 -- sh runner-script.sh

`PORT` is the local ssh port of the VM; the lock is one directory per port on the machine that runs the ssh commands. The command runs when the lock is free, the call waits up to 30 minutes otherwise (exit status 75 when the wait runs out, with the name of the holder), and the lock is released when the command ends, however it ends. The lock of a process that is gone is taken over. `scripts/vm-lock.sh status PORT` says who holds it, and `release PORT --force` removes a lock by hand.

Set `VM_LOCK_LABEL` to a name that tells the next person what is running. A runner script that has to hold the lock for several commands calls `scripts/vm-lock.sh acquire PORT` first (its own process is then the owner) and `release PORT` at the end.

## Running a part

`scripts/vm-run.sh PORT PART [SHARD]` runs one part on a VM from a clean state. It takes the lock, builds the binaries with the web consoles embedded (`just web-all` first), copies them and the scripts over, removes what an earlier run left and starts the test:

    sh scripts/vm-run.sh 2229 e2e 1        # the end-to-end flow, shards 1 to 3
    sh scripts/vm-run.sh 2229 pf a         # the packet filter test, shards a to d
    sh scripts/vm-run.sh 2229 workflows    # the daily tasks of a jail administrator
    sh scripts/vm-run.sh 2229 linux        # the Linux jail block

The packet filter test stops at the first failed phase and writes down what pf holds; `PF_KEEP_GOING=1` runs every phase.

## The releases that are not the gate

The gate is a RELEASE (15.1 today). A VM with the coming release (15.2-PRERELEASE, later its BETA and RC) and one with FreeBSD 16-CURRENT show what is coming. `scripts/vm-pulse.sh` runs the end-to-end shards and the packet filter shards on them, one after the other, and writes the result of every part to `temp/pulse-DATE.txt`:

    sh scripts/vm-pulse.sh            # the VMs on 2228 and 2230
    sh scripts/vm-pulse.sh 2230       # one VM
    sh scripts/vm-pulse.sh --update   # first bring each VM to the newest snapshot of its branch

With `--update` a VM gets `pkg upgrade` and a reboot when the installed kernel is not the running one; a VM that cannot be updated says so in the summary and is tested as it is, so a snapshot that is broken for a few days is not taken for a failure of JailOps.

A failure there is something to read, not a reason to hold a change. The rhythm: the gate (a RELEASE) runs every night in CI. The coming release and CURRENT run every Sunday in CI too, in the workflow `freebsd-ahead.yml`: it boots the snapshot image of `stable/15` and of 16.0-CURRENT with qemu on the runner (`scripts/ci-boot-vm.sh`), brings it to the newest packages of its branch, runs the end-to-end shards and the packet filter shards one after the other (`scripts/ci-ahead-run.sh`) and writes a table of the parts, with the result and the minutes of each, to the summary of the run:

    gh workflow run freebsd-ahead.yml                    # both branches now
    gh workflow run freebsd-ahead.yml -f which=current   # one branch
    gh workflow run freebsd-ahead.yml -f fail_fast=true  # end the run at the first red job

A VM that cannot be booted, updated or given its packages is written as `environment` in the summary: a snapshot that is broken for a few days is not a failure of JailOps. `scripts/vm-pulse.sh` is the same look on local VMs, by hand, before a BETA or an RC of a new minor release is trusted.
