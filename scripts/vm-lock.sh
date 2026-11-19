#!/bin/sh
# A lock for the shared FreeBSD test VMs. Two runs on one VM disturb each other (a run's cleanup removes what another run
# is using), and `pgrep` is not a lock: it can say "free" in the moment before the other run starts. The lock lives on the
# machine that runs the ssh commands, one directory per VM port, made with mkdir (atomic), and holds who owns it.
#
#   scripts/vm-lock.sh run PORT [WAIT_MINUTES] -- COMMAND...   take the lock, run the command, release it (the usual form)
#   scripts/vm-lock.sh acquire PORT [WAIT_MINUTES]             take the lock for the calling process (its pid is the owner)
#   scripts/vm-lock.sh release PORT [--force]                  release it (only the owner, or --force)
#   scripts/vm-lock.sh status PORT                             who holds it (exit 0 when free, 1 when held)
#
# WAIT_MINUTES defaults to 30. A lock whose owner process is gone is stale and is taken over. Exit status 75 means the
# wait ran out; the message names the owner. VM_LOCK_LABEL names the holder in that message (default: the command), and
# VM_LOCK_DIR the directory that holds the lock directories (default: $TMPDIR or /tmp). VM_LOCK_POLL is the seconds
# between looks (default 5).
set -u

base=${VM_LOCK_DIR:-${TMPDIR:-/tmp}}
poll=${VM_LOCK_POLL:-5}

die() { printf 'vm-lock: %s\n' "$*" >&2; exit 2; }

# check_port PORT: stops the script unless PORT is a number (not in a subshell: die must end the script)
check_port() {
	case ${1:-} in
	'' | *[!0-9]*) die "the VM port must be a number, got '${1:-}'" ;;
	esac
}

dir_of() { printf '%s/jailops-vm-lock-%s' "$base" "$1"; }

alive() { kill -0 "$1" 2>/dev/null; }

# owner_of DIR: prints "pid label epoch" of a lock, nothing when it cannot be read
owner_of() { cat "$1/owner" 2>/dev/null; }

describe() {
	set -- $(owner_of "$1")
	[ $# -eq 3 ] || { printf 'an unknown holder'; return; }
	printf '%s (pid %s, held for %s minutes)' "$2" "$1" $(( ($(date +%s) - $3) / 60 ))
}

# try_take DIR PID LABEL: 0 when the lock is now held by PID
try_take() {
	d=$1; pid=$2; label=$3
	if mkdir "$d" 2>/dev/null; then
		printf '%s %s %s\n' "$pid" "$label" "$(date +%s)" > "$d/owner"
		return 0
	fi
	cur=$(owner_of "$d")
	set -- $cur
	owner=${1:-}
	if [ -z "$owner" ]; then
		# no owner written: its maker is between mkdir and the write, or died there (older than a minute: stale)
		[ -n "$(find "$d" -maxdepth 0 -mmin +1 2>/dev/null)" ] || return 1
	elif [ "$owner" = "$pid" ]; then
		return 0 # already held by this process
	fi
	if [ -z "$owner" ] || ! alive "$owner"; then
		# stale: its owner is gone. Rename first: only one of several waiters wins the rename, so only one takes it over.
		stale="$d.stale.$$"
		if mv "$d" "$stale" 2>/dev/null; then
			rm -rf "$stale"
			if mkdir "$d" 2>/dev/null; then
				printf '%s %s %s\n' "$pid" "$label" "$(date +%s)" > "$d/owner"
				printf 'vm-lock: took over a stale lock (its owner is gone)\n' >&2
				return 0
			fi
		fi
	fi
	return 1
}

acquire() { # acquire PORT WAIT PID LABEL
	port=$1; wait_min=$2; pid=$3; label=$4
	d=$(dir_of "$port")
	deadline=$(( $(date +%s) + wait_min * 60 ))
	announced=0
	while :; do
		if try_take "$d" "$pid" "$label"; then
			return 0
		fi
		if [ "$(date +%s)" -ge "$deadline" ]; then
			printf 'vm-lock: the VM on port %s is held by %s; waited %s minutes\n' "$port" "$(describe "$d")" "$wait_min" >&2
			return 75
		fi
		if [ "$announced" = 0 ]; then
			printf 'vm-lock: the VM on port %s is held by %s; waiting up to %s minutes\n' "$port" "$(describe "$d")" "$wait_min" >&2
			announced=1
		fi
		sleep "$poll" # sleep-ok: the poll of the lock
	done
}

release() { # release PORT PID FORCE
	port=$1; pid=$2; force=$3
	d=$(dir_of "$port")
	[ -d "$d" ] || { printf 'vm-lock: the VM on port %s is not locked\n' "$port" >&2; return 0; }
	cur=$(owner_of "$d")
	set -- $cur
	if [ "$force" != 1 ] && [ "${1:-}" != "$pid" ]; then
		printf 'vm-lock: the lock of port %s belongs to %s, not to this process: not released (--force overrides)\n' "$port" "$(describe "$d")" >&2
		return 1
	fi
	rm -rf "$d"
}

cmd=${1:-}
[ $# -gt 0 ] && shift
case $cmd in
run)
	check_port "${1:-}"; port=$1; shift
	wait_min=30
	case ${1:-} in
	'' | --) ;;
	*) wait_min=$1; shift ;;
	esac
	[ "${1:-}" = "--" ] || die "usage: vm-lock.sh run PORT [WAIT_MINUTES] -- COMMAND..."
	shift
	[ $# -gt 0 ] || die "no command to run"
	label=${VM_LOCK_LABEL:-$1}
	label=$(printf '%s' "$label" | tr ' ' '_')
	acquire "$port" "$wait_min" "$$" "$label" || exit $?
	d=$(dir_of "$port")
	trap 'rm -rf "$d"' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	"$@"
	;;
acquire)
	check_port "${1:-}"; port=$1; wait_min=${2:-30}
	pid=${VM_LOCK_OWNER_PID:-$PPID}
	label=${VM_LOCK_LABEL:-$(ps -o comm= -p "$pid" 2>/dev/null)}
	label=$(printf '%s' "${label:-unknown}" | tr ' ' '_')
	acquire "$port" "$wait_min" "$pid" "$label"
	;;
release)
	check_port "${1:-}"; port=$1; force=0
	[ "${2:-}" = "--force" ] && force=1
	release "$port" "${VM_LOCK_OWNER_PID:-$PPID}" "$force"
	;;
status)
	check_port "${1:-}"; port=$1
	d=$(dir_of "$port")
	if [ -d "$d" ]; then
		cur=$(owner_of "$d")
		set -- $cur
		if [ -n "${1:-}" ] && ! alive "$1"; then
			printf 'vm-lock: the VM on port %s has a stale lock (%s)\n' "$port" "$(describe "$d")"
			exit 1
		fi
		printf 'vm-lock: the VM on port %s is held by %s\n' "$port" "$(describe "$d")"
		exit 1
	fi
	printf 'vm-lock: the VM on port %s is free\n' "$port"
	;;
*)
	die "usage: vm-lock.sh run|acquire|release|status PORT ..."
	;;
esac
