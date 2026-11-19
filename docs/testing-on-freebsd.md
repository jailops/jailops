# Running the FreeBSD tests on a VM

This page is for a contributor who runs `scripts/ci-freebsd-e2e.sh` or `scripts/ci-freebsd-pf.sh` on a FreeBSD VM of their own. The unit tests (`just test`) need no VM.

The two scripts install, start and tear down JailOps, its helper, jails, interfaces and a test pool on the machine they run on, so they belong on a disposable VM and never on a host that runs jails you care about. Both are driven from outside: a runner script builds the binaries, copies the scripts to the VM over ssh and starts them there.

## One run at a time

Two runs on one VM disturb each other: the cleanup of one run removes what the other is using, and the second run fails in a step that has nothing to do with its change. Looking for a running `ci-freebsd` process does not prevent that, because a run can start a moment after the look. Take the lock of the VM instead:

    sh scripts/vm-lock.sh run PORT 30 -- sh runner-script.sh

`PORT` is the local ssh port of the VM; the lock is one directory per port on the machine that runs the ssh commands. The command runs when the lock is free, the call waits up to 30 minutes otherwise (exit status 75 when the wait runs out, with the name of the holder), and the lock is released when the command ends, however it ends. The lock of a process that is gone is taken over. `scripts/vm-lock.sh status PORT` says who holds it, and `release PORT --force` removes a lock by hand.

Set `VM_LOCK_LABEL` to a name that tells the next person what is running. A runner script that has to hold the lock for several commands calls `scripts/vm-lock.sh acquire PORT` first (its own process is then the owner) and `release PORT` at the end.
