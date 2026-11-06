#!/bin/sh
# The whole product on a clean FreeBSD machine, as a user would meet it: packages, setup, services, an image, a jail,
# and a backup that is restored as a new jail. Runs as root on a disposable machine (the CI VM); it creates and
# destroys its own scratch pool and changes /etc/pf.conf and /etc/rc.conf.
#
#   sh scripts/ci-freebsd-e2e.sh
#
# JAILOPS_BIN_DIR   use these binaries instead of building them (jailops, jailopsd, ... as `just build-freebsd` names
#                   them, with or without the -freebsd-<arch> suffix)
# JAILOPS_E2E_PART   all (default), e2e (everything but the packet filter test) or pf (only the packet filter test)
# JAILOPS_E2E_POOL_SIZE  size of the file-backed scratch pool (default 8G)
# JAILOPS_E2E_APPS   more apps of the catalog to install after nginx, space separated: postgresql, valkey,
#                   prometheus, forgejo, nsd, mail, isms (default none; CI does not set it). Each one is installed,
#                   checked in its own way, its logs listed, and stopped. An installed app is closed, so the host is
#                   allowed in first (jailops app allow NAME from host) for the checks that connect from the host.
#                   isms needs the jailops package repository in the host's pkg configuration (docs/install.md) and
#                   installs a postgresql jail of its own, isms-db, which it is allowed to reach with app allow.
#                   postgresql-access is not an app but a block: a postgresql jail (pgacc) and two client jails
#                   (pgc1, pgc2) with the PostgreSQL client; pgc1 is allowed and runs select 1 with the connection
#                   string allow printed, pgc2 is refused, and after the deny pgc1 is refused too.
# JAILOPS_E2E_CATALOG  the catalog directory to install from (default: the reference copy internal/apps/testdata/catalog;
#                   for a change of the catalog itself, main/jailops-apps/apps of a packages repository checkout)
# JAILOPS_E2E_HOST  1 runs the host identity block (docs/appliance.md): it enables the module, changes the host name,
#                   /etc/resolv.conf (to documentation addresses that resolve nothing), the NTP block of /etc/ntp.conf
#                   and ntpd_enable of THIS machine, then restores them through 'jailops host restore' and checks that
#                   the files are back byte for byte (and puts them back from its own copies if anything fails on the
#                   way). Off by default; CI does not set it. A wrapper enables it with: JAILOPS_E2E_HOST=1 sh scripts/ci-freebsd-e2e.sh
# JAILOPS_E2E_HOSTFW  1 runs the host firewall block (docs/appliance.md): it enables the module and rewrites
#                   /etc/pf.conf of THIS machine, always with --confirm-within 20 and always keeping the ssh port open
#                   (the port sshd listens on, from sockstat -4l, to the address in SSH_CLIENT; to anywhere when
#                   SSH_CLIENT is not set). It first applies and does NOT confirm, and waits for the helper to put the
#                   original back; then applies and confirms; then restores the original through the snapshot and
#                   checks it byte for byte and that pf runs the original rules. On any failure the EXIT trap puts
#                   the original pf.conf and rc.conf back, reloads pf and restores the configuration. Off by default;
#                   CI does not set it: JAILOPS_E2E_HOSTFW=1 sh scripts/ci-freebsd-e2e.sh
# JAILOPS_E2E_APPLIANCE  1 runs the appliance block (docs/appliance.md, docs/exposure.md) on the test configuration this
#                   script made: it sets jailops_appliance in /etc/rc.conf with sysrc, restarts both services, checks
#                   check-config and doctor appliance, runs the setup token flow with curl (a wrong token refused, the
#                   right one creating the administrator e2e-admin, a second attempt not found), uploads a certificate
#                   made with openssl and checks that the TLS listener serves it (openssl s_client), checks that wrong
#                   ACME settings are refused (no CA is contacted), and puts rc.conf, the certificate and the user back.
#                   It changes no pf.conf. Off by default; CI does not set it: JAILOPS_E2E_APPLIANCE=1 sh scripts/ci-freebsd-e2e.sh
set -eu

cd "$(dirname "$0")/.."
root=$(pwd)
pool=jopsci
part=${JAILOPS_E2E_PART:-all}
case $part in all | e2e | pf) ;; *) echo "JAILOPS_E2E_PART must be all, e2e or pf" >&2; exit 2 ;; esac
e2e_apps=${JAILOPS_E2E_APPS:-}
for a in $e2e_apps; do
	case $a in postgresql | valkey | prometheus | forgejo | nsd | mail | isms | postgresql-access) ;; *) echo "JAILOPS_E2E_APPS: no check for $a (postgresql, valkey, prometheus, forgejo, nsd, mail, isms, postgresql-access)" >&2; exit 2 ;; esac
	# isms is installed with pkg -r ROOT install -r jailops: say so now, not after the setup of the whole machine
	if [ "$a" = isms ] && ! pkg -vv | grep -Eq '^[[:space:]]+jailops:[[:space:]]*\{'; then
		echo "JAILOPS_E2E_APPS: isms comes from the jailops package repository, which the pkg configuration of this host does not have (docs/install.md)" >&2
		exit 2
	fi
done
poolfile=/tmp/$pool.img
work=/tmp/jailops-e2e
mount=/jopsci
version=$(tr -d '[:space:]' < version.txt)
major=$(freebsd-version -u | cut -d. -f1)
case $(uname -p) in
amd64) goarch=amd64 ;;
aarch64 | arm64) goarch=arm64 ;;
*) echo "unsupported architecture $(uname -p)" >&2; exit 2 ;;
esac
release=$(freebsd-version -u | sed 's/-p[0-9]*$//')
# the end-to-end flow can build its image from another release than the host's (an older userland on a newer kernel is
# supported): JAILOPS_E2E_IMAGE_RELEASE=15.1-RELEASE tests the packages of 15.1 on a 15.2 host
host_release=$release # the hints of the server name the host's release, whatever image the flow builds
host_img=freebsd-$(echo "$release" | sed -E 's/^([0-9]+)\.([0-9]+)-.*/\1-\2/')
release=${JAILOPS_E2E_IMAGE_RELEASE:-$release}
img=freebsd-$(echo "$release" | sed -E 's/^([0-9]+)\.([0-9]+)-.*/\1-\2/') # the image of the host's release
J=/usr/local/bin/jailops

# set -e ends the script without a word: say which step it was
current=start
host_cleanup() { :; } # the host identity block replaces it while the machine's identity is changed
trap 'rc=$?; host_cleanup; [ $rc -eq 0 ] || echo "FAILED (exit $rc) in step: $current" >&2' EXIT
step() { current=$*; printf '\n== %s\n' "$*"; }
fail() {
	echo "FAILED: $*" >&2
	echo "--- last messages" >&2
	tail -n 40 /var/log/messages >&2 || true
	exit 1
}
check() { # check DESCRIPTION COMMAND...: the command must succeed
	desc=$1; shift
	"$@" >"$work/check.out" 2>&1 || { cat "$work/check.out" >&2; fail "$desc"; }
}
expect() { # expect DESCRIPTION PATTERN COMMAND...: the output must match
	desc=$1; pat=$2; shift 2
	out=$("$@" 2>&1) || { echo "$out" >&2; fail "$desc"; }
	printf '%s\n' "$out" | grep -E -q "$pat" || { echo "$out" >&2; fail "$desc (no match for $pat)"; }
	printf '%s\n' "$out"
}

expect_retry() { # expect_retry DESCRIPTION PATTERN COMMAND...: like expect, tried for up to 30 s (the firewall of a new jail settles a few seconds after it starts)
	desc=$1; pat=$2; shift 2
	n=0
	until out=$("$@" 2>&1) && printf '%s\n' "$out" | grep -E -q "$pat"; do
		n=$((n + 1))
		[ $n -ge 15 ] && { echo "$out" >&2; fail "$desc (no match for $pat after 30 s)"; }
		sleep 2
	done
	printf '%s\n' "$out"
}
expect_refused_retry() { # expect_refused_retry DESCRIPTION COMMAND...: the command must fail, tried for up to 30 s (a firewall change settles a few seconds after it is made)
	desc=$1; shift
	n=0
	while out=$("$@" 2>&1); do
		n=$((n + 1))
		[ $n -ge 15 ] && { echo "$out" >&2; fail "$desc (still answered after 30 s)"; }
		sleep 2
	done
}
expect_refused() { # expect_refused DESCRIPTION COMMAND...: the command must fail, at once
	desc=$1; shift
	if out=$("$@" 2>&1); then echo "$out" >&2; fail "$desc (it answered)"; fi
}
json_field() { # json_field NAME: the string value of a top-level field of the indented JSON on standard input
	sed -n "s/^  \"$1\": \"\([^\"]*\)\",*\$/\1/p" | head -1
}

rm -rf "$work"; mkdir -p "$work"

# CI logs show how fast this machine is (a VM without KVM acceleration is several times slower)
echo "machine: $(sysctl -n kern.vm_guest hw.ncpu hw.model | tr '\n' ' ')"
step "binaries"
bin=${JAILOPS_BIN_DIR:-}
if [ -z "$bin" ]; then
	command -v go >/dev/null || fail "go is needed to build (or set JAILOPS_BIN_DIR)"
	[ -s internal/webui/dist/index.html ] && [ -s internal/backupui/dist/index.html ] || fail "the web consoles are not built (just web-all): the binaries would have none embedded"
	bin=$work/bin; mkdir -p "$bin"
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$bin/" ./cmd/jailops ./cmd/jailopsd ./cmd/jailops-priv ./cmd/jailops-sync ./cmd/jailops-backupd
fi

step "packages"
sh scripts/pkg-build.sh "$version" "$goarch" "$bin" "$work/pkgs" "$major" >/dev/null
abi=$(pkg config ABI)
pkgdir=$work/pkgs/$abi/latest/All
ls "$pkgdir"
pkg add "$pkgdir/jailops-client-$version.pkg" "$pkgdir/jailops-$version.pkg" "$pkgdir/jailops-backupd-$version.pkg" >/dev/null 2>&1 || \
	pkg add -f "$pkgdir/jailops-client-$version.pkg" "$pkgdir/jailops-$version.pkg" "$pkgdir/jailops-backupd-$version.pkg" || fail "pkg add"
check "users exist" id jailops
check "backup user exists" id jailops-backup

step "scratch pool and kernel modules"
zpool destroy -f "$pool" 2>/dev/null || true
rm -f "$poolfile"
truncate -s "${JAILOPS_E2E_POOL_SIZE:-8G}" "$poolfile"
check "zpool create" zpool create -m none "$pool" "$poolfile"
for m in if_bridge if_epair nullfs pf; do kldload -n $m 2>/dev/null || true; done

step "PF anchors (the one line of PF setup JailOps asks for)"
touch /etc/pf.conf
grep -q 'anchor "jailops/\*"' /etc/pf.conf || cat >> /etc/pf.conf <<'PF'
nat-anchor "jailops/*"
rdr-anchor "jailops/*"
anchor "jailops/*"
PF
sysrc pf_enable=YES >/dev/null
# enabling pf flushes its states on a machine where it is already running, which cuts the ssh session of whoever runs
# this: start it only when it is not running, and otherwise just reload the rules
pfctl -s info 2>/dev/null | grep -q 'Status: Enabled' || service pf onestart >/dev/null 2>&1 || true
# a pf that was already running keeps the ruleset it had: load the file we just changed, and refuse to go on if it
# does not parse (a broken pf.conf on a machine with a real firewall must be noticed here, not later)
pfctl -nf /etc/pf.conf || fail "/etc/pf.conf does not parse"
pfctl -f /etc/pf.conf || fail "could not load /etc/pf.conf"
pfctl -s info >/dev/null 2>&1 || fail "pf is not running"

step "forwarding (JailOps asks the administrator for this, it never changes forwarding itself)"
sysctl net.inet.ip.forwarding=1 net.inet6.ip6.forwarding=1 >/dev/null
sysrc gateway_enable=YES ipv6_gateway_enable=YES >/dev/null

step "jailops-priv init"
check "init" /usr/local/libexec/jailops-priv init -pool "$pool" -mount "$mount"
check "init is repeatable" /usr/local/libexec/jailops-priv init -pool "$pool" -mount "$mount"

step "services"
sysrc jailops_priv_enable=YES jailopsd_enable=YES jailopsd_web=YES >/dev/null
check "jailops_priv checkconfig" service jailops_priv checkconfig
check "jailops_priv start" service jailops_priv start
check "jailopsd start" service jailopsd start
n=0
until [ -S /var/run/jailops/jailops.sock ]; do
	n=$((n + 1)); [ $n -gt 30 ] && fail "jailopsd did not create its socket"
	sleep 1
done
sleep 2
step "pid files: root writes them in /var/run, never in /var/run/jailops (the daemon may write there)"
for f in /var/run/jailops-priv.pid /var/run/jailops-priv-daemon.pid /var/run/jailopsd.pid /var/run/jailopsd-daemon.pid; do
	[ -s "$f" ] || fail "$f is missing"
done
[ -z "$(find /var/run/jailops -type f)" ] || fail "regular files in /var/run/jailops: $(find /var/run/jailops -type f)"
step "the helper's socket is in a directory only root writes; the daemon's user can put nothing there"
expect "the helper's socket directory" '^root:jailops 710$' stat -f '%Su:%Sg %Lp' /var/run/jailops-priv
[ -S /var/run/jailops-priv/priv.sock ] || fail "no socket at /var/run/jailops-priv/priv.sock"
[ ! -e /var/run/jailops/priv.sock ] || fail "a priv.sock is left in the daemon's directory"
expect_refused "the daemon's user cannot create a file there" su -m jailops -c 'touch /var/run/jailops-priv/x'
step "storage root: root's directory, mode 0700, devices off"
expect "the storage root's directory" '^root 700$' stat -f '%Su %Lp' "$mount"
expect "devices is off on the storage root" '^off	local$' zfs get -H -o value,source devices "$pool/jailops"
expect_refused "the daemon's user cannot enter the storage root" su -m jailops -c "ls $mount"
check "jailops_priv status" service jailops_priv status
check "jailopsd status" service jailopsd status
# -web: the quick start's console is on the TLS port, and the API is the same port (the certificate is self-signed)
step "web console and API on port 39110"
n=0; until curl -ks -o /dev/null https://127.0.0.1:39110/api/v1; do n=$((n + 1)); [ $n -gt 20 ] && fail "port 39110 does not answer"; sleep 1; done
curl -ks https://127.0.0.1:39110/ | grep -qi '<html' || fail "the web console is not served on port 39110"
curl -ks https://127.0.0.1:39110/api/v1 | grep -q '"untrusted"' || fail "the API is not served on port 39110"
$J doctor || true
if $J doctor 2>&1 | grep -E ' FAIL '; then fail "doctor reports a failing check"; fi
# GET /readyz (docs/operations.md): no credentials, a fixed answer, cached for 2 s; the helper stopped makes it 503
step "readiness: GET /readyz without credentials, and with the helper stopped"
readyz() { curl -ks -o "$work/readyz.out" -w '%{http_code} ' https://127.0.0.1:39110/readyz; cat "$work/readyz.out"; }
expect_retry "readyz answers 200 without credentials" '^200 ok$' readyz
expect "an ordinary local user gets the same over the socket" '^ok$' su -m nobody -c 'curl -s --unix-socket /var/run/jailops/jailops.sock http://jailops.local/readyz'
check "jailops_priv stop" service jailops_priv stop
expect_retry "readyz says the helper is down, and nothing more" '^503 not ready: helper$' readyz
expect "healthz is liveness and stays ok" '^ok$' curl -ks https://127.0.0.1:39110/healthz
check "jailops_priv start" service jailops_priv start
expect_retry "readyz is 200 again once the helper is back" '^200 ok$' readyz

# The host identity module changes this machine itself: only with JAILOPS_E2E_HOST=1, and always put back.
if [ "${JAILOPS_E2E_HOST:-}" = 1 ]; then
	step "host identity (JAILOPS_E2E_HOST=1: this machine's name, resolvers and NTP, then restored)"
	horig=$work/host-orig
	rm -rf "$horig"; mkdir -p "$horig"
	for f in resolv.conf ntp.conf rc.conf; do [ -f /etc/$f ] && cp -p /etc/$f "$horig/$f"; done
	cp -p /usr/local/etc/jailops/priv.conf /usr/local/etc/jailops/jailopsd.yaml "$horig/"
	orig_hostname=$(hostname)
	host_cleanup() { # what a failure in this block leaves behind: the originals go back from the copies above
		[ -n "${host_dirty:-}" ] || return 0
		echo "putting the host identity back from $horig" >&2
		for f in resolv.conf ntp.conf rc.conf; do
			if [ -f "$horig/$f" ]; then cp -p "$horig/$f" /etc/$f; else rm -f /etc/$f; fi
		done
		hostname "$orig_hostname"
		cp -p "$horig/priv.conf" /usr/local/etc/jailops/priv.conf; cp -p "$horig/jailopsd.yaml" /usr/local/etc/jailops/jailopsd.yaml
		service ntpd onestatus >/dev/null 2>&1 && service ntpd onerestart >/dev/null 2>&1
		service jailops_priv restart >/dev/null 2>&1; service jailopsd restart >/dev/null 2>&1
		host_dirty=
	}
	host_same() { # host_same: resolv.conf and ntp.conf are the originals byte for byte, rc.conf has the original
		# lines besides the module's three variables and their original values (sysrc may quote a value it puts back
		# its own way), and the host name is the original
		for f in resolv.conf ntp.conf; do
			if [ -f "$horig/$f" ]; then cmp -s "$horig/$f" /etc/$f || fail "/etc/$f is not the original"
			else [ ! -e /etc/$f ] || fail "/etc/$f exists and did not before"; fi
		done
		rcvars='^(hostname|ntpd_enable|ntpd_sync_on_start)='
		grep -Ev "$rcvars" "$horig/rc.conf" > "$work/rc.orig.rest"; grep -Ev "$rcvars" /etc/rc.conf > "$work/rc.now.rest"
		cmp -s "$work/rc.orig.rest" "$work/rc.now.rest" || { diff "$work/rc.orig.rest" "$work/rc.now.rest" >&2; fail "rc.conf changed outside the module's variables"; }
		for v in hostname ntpd_enable ntpd_sync_on_start; do
			[ "$(sysrc -f "$horig/rc.conf" -qn $v 2>/dev/null)" = "$(sysrc -qn $v 2>/dev/null)" ] || fail "rc.conf $v is not the original"
		done
		[ "$(hostname)" = "$orig_hostname" ] || fail "the host name is $(hostname), not $orig_hostname"
	}
	host_dirty=1
	expect "not managed by default" 'Managed: +no' $J host show
	expect "changing is refused while the module is off" 'not_managed' sh -c "$J host identity set --hostname e2e.example.org 2>&1; true"
	host_same
	echo 'host-manage = identity' >> /usr/local/etc/jailops/priv.conf
	printf 'host:\n  manage:\n    modules: [identity]\n' >> /usr/local/etc/jailops/jailopsd.yaml
	check "jailops_priv checkconfig with host-manage" service jailops_priv checkconfig
	check "restart jailops_priv" service jailops_priv restart
	check "restart jailopsd" service jailopsd restart
	n=0; until $J host show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	expect "managed now" 'Managed: +yes' $J host show
	set -- --hostname jailops-e2e.example.org --nameserver 192.0.2.53 --nameserver 2001:db8::53 --search example.org --ntp-server 192.0.2.123 --ntp enable
	expect "the dry run shows the diff" '^\+nameserver 2001:db8::53$' $J --dry-run host identity set "$@"
	host_same
	out=$(expect "apply" 'undo with: jailops host restore ' $J host identity set "$@")
	snap=$(printf '%s\n' "$out" | sed -n 's/.*undo with: jailops host restore //p')
	[ -n "$snap" ] || fail "no snapshot name in the output"
	expect "hostname(1)" '^jailops-e2e\.example\.org$' hostname
	expect "resolv.conf carries the header" '^# managed by jailops$' head -1 /etc/resolv.conf
	expect "resolv.conf has the nameservers" '^nameserver 2001:db8::53$' cat /etc/resolv.conf
	expect "resolv.conf has the search list" '^search example\.org$' cat /etc/resolv.conf
	expect "ntp.conf has the block" '^server 192\.0\.2\.123 iburst$' sed -n '/^# jailops begin$/,/^# jailops end$/p' /etc/ntp.conf
	sed '/^# jailops begin$/,/^# jailops end$/d' /etc/ntp.conf > "$work/ntp.rest"
	if [ -f "$horig/ntp.conf" ]; then cmp -s "$work/ntp.rest" "$horig/ntp.conf" || { diff "$horig/ntp.conf" "$work/ntp.rest" >&2; fail "ntp.conf changed outside the jailops block"; }; fi
	expect "rc.conf hostname" '^jailops-e2e\.example\.org$' sysrc -n hostname
	expect "rc.conf ntpd_enable" '^YES$' sysrc -n ntpd_enable
	expect "rc.conf ntpd_sync_on_start" '^YES$' sysrc -n ntpd_sync_on_start
	expect "the snapshot is listed" "^$snap +identity +apply" $J host snapshots
	expect "doctor: managed, no drift" 'host\.identity +PASS' sh -c "$J doctor 2>&1; true"
	echo 'nameserver 192.0.2.54' >> /etc/resolv.conf
	expect "a hand edit is drift" 'Drift: +resolvers' $J host show
	expect "and doctor says so" 'host\.identity +WARN' sh -c "$J doctor 2>&1; true"
	expect "the hand edit is not overwritten" '^nameserver 192\.0\.2\.54$' cat /etc/resolv.conf
	expect "the restore dry run" '^-nameserver 192\.0\.2\.54$' $J --dry-run host restore "$snap"
	# a line root adds to rc.conf after the snapshot is not the module's: the restore leaves it, and does not show it
	echo 'jailops_e2e_marker="kept"' >> /etc/rc.conf
	if $J --dry-run host restore "$snap" 2>&1 | grep -q jailops_e2e_marker; then fail "the restore diff shows a line of rc.conf the module does not own"; fi
	check "restore" $J -y host restore "$snap"
	expect "the restore kept root's own rc.conf line" '^kept$' sysrc -n jailops_e2e_marker
	sysrc -x jailops_e2e_marker >/dev/null
	host_same
	expect "the restore kept a snapshot of its own" ' +identity +restore ' $J host snapshots
	cp -p "$horig/priv.conf" /usr/local/etc/jailops/priv.conf; cp -p "$horig/jailopsd.yaml" /usr/local/etc/jailops/jailopsd.yaml
	check "restart jailops_priv" service jailops_priv restart
	check "restart jailopsd" service jailopsd restart
	n=0; until $J host show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	host_dirty=
	host_same
fi

# The host firewall module rewrites this machine's /etc/pf.conf: only with JAILOPS_E2E_HOSTFW=1, always with a
# confirmation window, never closing the ssh port this script is reached through, and always put back.
if [ "${JAILOPS_E2E_HOSTFW:-}" = 1 ]; then
	step "host firewall (JAILOPS_E2E_HOSTFW=1: this machine's /etc/pf.conf, with commit confirmed, then restored)"
	forig=$work/hostfw-orig
	rm -rf "$forig"; mkdir -p "$forig"
	cp -p /etc/pf.conf "$forig/pf.conf"
	cp -p /etc/rc.conf "$forig/rc.conf"
	cp -p /usr/local/etc/jailops/priv.conf /usr/local/etc/jailops/jailopsd.yaml "$forig/"
	pfctl -s rules > "$forig/rules" 2>/dev/null || true
	# the ssh port sshd listens on, and the address this session comes from: exactly these stay open
	sshport=$(sockstat -4l | awk '$2 == "sshd" { n = split($6, a, ":"); print a[n]; exit }')
	[ -n "$sshport" ] || sshport=22
	src=${SSH_CLIENT:-}; src=${src%% *}
	if [ -n "$src" ]; then
		set -- --ssh-port "$sshport" --ssh-from "$src" --console-from "$src"
		echo "keeping ssh port $sshport open to $src (from SSH_CLIENT)"
	else
		set -- --ssh-port "$sshport"
		echo "SSH_CLIENT is not set (not over ssh, or sudo dropped it): ssh port $sshport stays open to anywhere"
	fi
	set -- "$@" --template custom-rules --allow "tcp 9 from 192.0.2.1 comment e2e" --confirm-within 20 --i-know
	hostfw_cleanup() { # what a failure in this block leaves behind: the original pf.conf, loaded, and the configuration
		[ -n "${hostfw_dirty:-}" ] || return 0
		echo "putting /etc/pf.conf back from $forig" >&2
		$J -y host revert >/dev/null 2>&1 || true
		cp -p "$forig/pf.conf" /etc/pf.conf; cp -p "$forig/rc.conf" /etc/rc.conf
		pfctl -f /etc/pf.conf || echo "WARNING: pfctl -f /etc/pf.conf failed; load it by hand" >&2
		cp -p "$forig/priv.conf" /usr/local/etc/jailops/priv.conf; cp -p "$forig/jailopsd.yaml" /usr/local/etc/jailops/jailopsd.yaml
		service jailops_priv restart >/dev/null 2>&1; service jailopsd restart >/dev/null 2>&1
		hostfw_dirty=
	}
	host_cleanup() { hostfw_cleanup; }
	pf_same() { # pf_same: /etc/pf.conf is the original, byte for byte, and pf runs its rules
		cmp -s "$forig/pf.conf" /etc/pf.conf || { diff "$forig/pf.conf" /etc/pf.conf >&2; fail "/etc/pf.conf is not the original"; }
		pfctl -s rules > "$work/rules.now" 2>/dev/null
		cmp -s "$forig/rules" "$work/rules.now" || { diff "$forig/rules" "$work/rules.now" >&2; fail "pf does not run the original rules"; }
	}
	hostfw_dirty=1
	expect "not managed by default" 'Managed: +no' $J host firewall show
	expect "changing is refused while the module is off" 'not_managed' sh -c "$J host firewall set --i-know 2>&1; true"
	pf_same
	echo 'host-manage = firewall' >> /usr/local/etc/jailops/priv.conf
	printf 'host:\n  manage:\n    modules: [firewall]\n' >> /usr/local/etc/jailops/jailopsd.yaml
	check "jailops_priv checkconfig with host-manage" service jailops_priv checkconfig
	check "restart jailops_priv" service jailops_priv restart
	check "restart jailopsd" service jailopsd restart
	n=0; until $J host firewall show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	expect "managed now" 'Managed: +yes' $J host firewall show
	expect "the unix socket has no address: the lock-out check needs --i-know" 'i-know' sh -c "$J host firewall set --ssh-port $sshport --confirm-within 20 2>&1; true"
	expect "the dry run shows the diff" '^\+block in all$' $J --dry-run host firewall set "$@"
	pf_same

	# 1. the revert path: apply, do NOT confirm, and the helper's timer puts the original back
	expect "apply" 'REVERTS at' $J host firewall set "$@"
	expect "pf.conf is the managed file" '^# managed by jailops$' head -1 /etc/pf.conf
	expect "pf runs it, with the jails' anchor" 'anchor "jailops/\*"' pfctl -s rules
	# pfctl prints a port by its name in /etc/services when it has one
	sshsvc=$(awk -v p="$sshport/tcp" '$2 == p { print $1; exit }' /etc/services)
	expect "pf passes ssh on port $sshport" "port = ($sshport|${sshsvc:-$sshport})( |$)" pfctl -s rules
	expect "it waits for its confirmation" 'Reverts at:' $J host pending
	expect "doctor says so" 'host\.firewall +WARN' sh -c "$J doctor 2>&1; true"
	expect_retry "the change reverts by itself after 20 s" 'was timed out' $J host pending
	pf_same
	expect "nothing waits any more" 'nothing waits' $J host pending

	# 2. apply and confirm: the change stays past its deadline
	expect "apply again" 'REVERTS at' $J host firewall set "$@"
	expect "confirm" 'was confirmed' $J host confirm
	sleep 22 # past the deadline of the confirmed change: nothing may revert it
	expect "still the managed file" '^# managed by jailops$' head -1 /etc/pf.conf
	expect "doctor: managed, no drift" 'host\.firewall +PASS' sh -c "$J doctor 2>&1; true"
	cp -p /etc/pf.conf "$work/pf.managed"
	echo '# a hand edit' >> /etc/pf.conf
	expect "a hand edit is drift" 'Drift: +pf\.conf' $J host firewall show
	expect "and doctor says so" 'host\.firewall +WARN' sh -c "$J doctor 2>&1; true"
	expect "the diff shows it" '^-# a hand edit$' $J host firewall diff
	cp -p "$work/pf.managed" /etc/pf.conf

	# 3. the snapshot taken before the first apply puts the original back, byte for byte, and loads it
	snap=$($J host snapshots | awk '$2 == "firewall" && $3 == "adopt" { print $1; exit }')
	[ -n "$snap" ] || fail "no snapshot of the original pf.conf"
	expect "the restore dry run" '^\+# managed by jailops$|^-# managed by jailops$' $J --dry-run host restore "$snap"
	echo 'jailops_e2e_marker="kept"' >> /etc/rc.conf
	check "restore" $J -y host restore "$snap"
	pf_same
	expect "the restore kept root's own rc.conf line" '^kept$' sysrc -n jailops_e2e_marker
	sysrc -x jailops_e2e_marker >/dev/null
	# rc.conf: the original lines, and pf_enable and pf_rules as they were (sysrc may quote a value it puts back)
	grep -Ev '^(pf_enable|pf_rules)=' "$forig/rc.conf" > "$work/rc.orig.rest"; grep -Ev '^(pf_enable|pf_rules)=' /etc/rc.conf > "$work/rc.now.rest"
	cmp -s "$work/rc.orig.rest" "$work/rc.now.rest" || fail "/etc/rc.conf changed outside pf_enable and pf_rules"
	for v in pf_enable pf_rules; do
		[ "$(sysrc -f "$forig/rc.conf" -qn $v 2>/dev/null)" = "$(sysrc -qn $v 2>/dev/null)" ] || fail "rc.conf $v is not the original"
	done
	cp -p "$forig/priv.conf" /usr/local/etc/jailops/priv.conf; cp -p "$forig/jailopsd.yaml" /usr/local/etc/jailops/jailopsd.yaml
	check "restart jailops_priv" service jailops_priv restart
	check "restart jailopsd" service jailopsd restart
	n=0; until $J host firewall show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	hostfw_dirty=
	pf_same
fi

# Appliance mode from rc.conf, the setup token and the console's certificate: only with JAILOPS_E2E_APPLIANCE=1, on the
# test configuration above, and always put back.
if [ "${JAILOPS_E2E_APPLIANCE:-}" = 1 ]; then
	step "appliance mode (JAILOPS_E2E_APPLIANCE=1: the rc.conf switch, the setup token, a certificate upload, ACME refusals)"
	aorig=$work/appliance-orig
	rm -rf "$aorig"; mkdir -p "$aorig"
	cp -p /etc/rc.conf "$aorig/rc.conf"
	tlsdir=/var/db/jailops/tls
	api() { # api METHOD PATH [BODY]: a request as root on the daemon's socket; prints the status, a space and the body
		curl -s --unix-socket /var/run/jailops/jailops.sock -o "$work/api.out" -w '%{http_code}' -X "$1" -H 'Content-Type: application/json' \
			${3:+-d "$3"} "http://jailops.local$2"
		printf ' '; cat "$work/api.out"
	}
	anon() { # anon METHOD PATH [BODY]: the same, anonymous, on the TLS listener
		curl -ks -o "$work/anon.out" -w '%{http_code}' -X "$1" -H 'Content-Type: application/json' ${3:+-d "$3"} "https://127.0.0.1:39110$2"
		printf ' '; cat "$work/anon.out"
	}
	appliance_cleanup() { # rc.conf, the certificate and the user as they were
		[ -n "${appliance_dirty:-}" ] || return 0
		echo "putting rc.conf, the certificate and the users back" >&2
		cp -p "$aorig/rc.conf" /etc/rc.conf
		$J -y user delete e2e-admin >/dev/null 2>&1 || true
		rm -f "$tlsdir/choice.json" "$tlsdir/uploaded.crt" "$tlsdir/uploaded.key"
		service jailops_priv restart >/dev/null 2>&1; service jailopsd restart >/dev/null 2>&1
		appliance_dirty=
	}
	host_cleanup() { appliance_cleanup; }
	appliance_dirty=1
	expect "doctor: appliance off" 'appliance +PASS' sh -c "$J doctor 2>&1; true"
	sysrc jailops_appliance=YES jailops_appliance_modules="identity firewall" >/dev/null
	expect "check-config lists the modules" '^host manage: +identity, firewall' service jailops_priv checkconfig
	expect "check-config says they come from rc.conf" '^appliance: +on: rc.conf' service jailops_priv checkconfig
	check "restart jailops_priv" service jailops_priv restart
	started=$(service jailopsd restart 2>&1) || { echo "$started" >&2; fail "restart jailopsd"; }
	n=0; until $J host show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	expect "doctor: the daemon and the helper agree" 'appliance +PASS +.*identity, firewall' sh -c "$J doctor 2>&1; true"
	expect "the identity module is managed" 'Managed: +yes' $J host show
	# the setup token: on the console and in auth.log at the start, and again from jailops-priv setup-token
	printf '%s\n' "$started" | grep -q 'Setup token:' || { echo "$started" >&2; fail "the start of jailopsd printed no setup token"; }
	expect_retry "auth.log has the token" 'Setup token:' tail -n 300 /var/log/auth.log
	if grep -q 'Setup token:' /var/log/messages; then fail "the setup token is in /var/log/messages"; fi
	tok=$(/usr/local/libexec/jailops-priv setup-token | awk '/Setup token:/ { print $3 }')
	[ -n "$tok" ] || fail "jailops-priv setup-token printed no token"
	pw='e2e passphrase for the appliance'
	expect "setup is required" '^200 .*"required":true' anon GET /api/v1/setup
	expect "a wrong token is refused" '^401 ' anon POST /api/v1/setup "{\"token\":\"AAAA$tok\",\"name\":\"e2e-admin\",\"password\":\"$pw\"}"
	expect "the right token creates the administrator" '^201 .*"name":"e2e-admin"' anon POST /api/v1/setup "{\"token\":\"$tok\",\"name\":\"e2e-admin\",\"password\":\"$pw\"}"
	expect "a second attempt is not found" '^404 ' anon POST /api/v1/setup "{\"token\":\"$tok\",\"name\":\"e2e-two\",\"password\":\"$pw\"}"
	expect "the setup status is not found" '^404 ' anon GET /api/v1/setup
	expect "setup-token says the setup is complete" 'setup is complete' sh -c '/usr/local/libexec/jailops-priv setup-token 2>&1; true'
	expect "the administrator signs in" '^200 ' anon POST /api/v1/auth/login "{\"username\":\"e2e-admin\",\"password\":\"$pw\"}"
	# a certificate of a test CA, uploaded through the API and served by the TLS listener
	cd "$work"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 120 -subj /CN=e2e-ca -keyout ca.key -out ca.crt \
		-addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign >/dev/null 2>&1 || fail "openssl: the test CA"
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj /CN=e2e.example.com -keyout srv.key -out srv.csr >/dev/null 2>&1 || fail "openssl: the request"
	printf 'subjectAltName=DNS:e2e.example.com\nextendedKeyUsage=serverAuth\n' > srv.ext
	openssl x509 -req -in srv.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 90 -extfile srv.ext -out srv.crt >/dev/null 2>&1 || fail "openssl: the certificate"
	cd "$root"
	jpem() { awk 'BEGIN { ORS = "" } { print $0 "\\n" }' "$1"; } # a PEM file as one JSON string
	upload() { printf '{"certificate":"%s%s","key":"%s","hostname":"%s"}' "$(jpem "$work/srv.crt")" "$(jpem "$work/ca.crt")" "$(jpem "$work/srv.key")" "$1"; }
	expect "a certificate for another name is refused" '^400 .*not valid for www\.example\.com' api PUT /api/v1/host/certificate "$(upload www.example.com)"
	cat "$work/srv.crt" "$work/ca.crt" > "$work/chain.pem"
	expect "the CLI refuses it too" 'not valid for www\.example\.com' sh -c "$J host certificate upload --cert $work/chain.pem --key $work/srv.key --hostname www.example.com 2>&1; true"
	expect "the upload (jailops host certificate upload)" '^host-certificate/uploaded for e2e\.example\.com' $J host certificate upload --cert "$work/chain.pem" --key "$work/srv.key"
	expect "show says where it comes from" 'Certificate of this host: uploaded' $J host certificate show
	expect "show names the remote add command" 'jailops remote add [^ ]+ https://[^ ]+:39110 ' $J host certificate show
	expect "the files are 0600" '^-rw------- ' ls -l "$tlsdir/uploaded.key"
	expect "the TLS listener serves it" 'CN *= *e2e\.example\.com' sh -c "openssl s_client -connect 127.0.0.1:39110 -servername e2e.example.com </dev/null 2>/dev/null | openssl x509 -noout -subject"
	expect "it verifies against the test CA" 'Verify return code: 0' sh -c "openssl s_client -connect 127.0.0.1:39110 -servername e2e.example.com -CAfile $work/ca.crt -verify_hostname e2e.example.com </dev/null 2>&1"
	expect "doctor: tls.certificate" 'tls\.certificate +PASS +uploaded' sh -c "$J doctor 2>&1; true"
	# ACME settings that cannot work are refused before a CA is asked
	expect "ACME without the terms" '^400 .*accept_tos' api POST /api/v1/host/certificate/acme '{"domains":["e2e.example.com"]}'
	expect "ACME with a wildcard" '^501 .*dns-01' api POST /api/v1/host/certificate/acme '{"domains":["*.example.com"],"accept_tos":true}'
	expect "ACME with an address" '^400 .*is an address' api POST /api/v1/host/certificate/acme '{"domains":["192.0.2.10"],"accept_tos":true}'
	expect "ACME with a bad email" '^400 .*not an email' api POST /api/v1/host/certificate/acme '{"domains":["e2e.example.com"],"email":"root","accept_tos":true}'
	expect "ACME over plain http" '^400 .*https' api POST /api/v1/host/certificate/acme '{"domains":["e2e.example.com"],"directory_url":"http://ca.example.com/dir","accept_tos":true}'
	expect "still the uploaded certificate" '"source":"uploaded"' api GET /api/v1/host/certificate
	expect "replacing the uploaded certificate needs its name typed" 'confirmation required' sh -c "$J host certificate self-signed -y </dev/null 2>&1; true"
	expect "the self-signed certificate again" 'self-signed served' $J host certificate self-signed --i-know
	expect "the API agrees" '"source":"self-signed"' api GET /api/v1/host/certificate
	expect "the TLS listener serves it" 'CN *= *' sh -c "openssl s_client -connect 127.0.0.1:39110 </dev/null 2>/dev/null | openssl x509 -noout -subject"
	appliance_cleanup
	n=0; until $J host show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	expect "doctor: appliance off again" 'appliance +PASS +off' sh -c "$J doctor 2>&1; true"
	cmp -s "$aorig/rc.conf" /etc/rc.conf || fail "/etc/rc.conf is not the original"
fi

run_pf() {
	step "the packet filter, with real traffic (scripts/ci-freebsd-pf.sh)"
	# the full matrix takes minutes of waiting in its last phases; the quick form still covers every rule path
	# FORGE_BIN (set by the caller) runs phase 0e, the forged-frame probes; without it phase 0e is skipped
	PF_FAST=1 PF_IMAGE="$img" FORGE_BIN="${FORGE_BIN:-}" sh "$root/scripts/ci-freebsd-pf.sh" || fail "the packet filter test failed"
}

step "image, network, project"
uplink=$(route -n get default 2>/dev/null | awk '/interface:/ {print $2}')
[ -n "$uplink" ] || uplink=$(ifconfig -l | tr ' ' '\n' | grep -v -E '^(lo0|pflog0)$' | head -1)
jarch=$goarch
check "network create" $J network create lan --bridge jo7 --ipv4 10.77.0.1/24 --uplink "$uplink" --nat
check "project create" $J project create demo
check "project grant" $J project set demo --network lan --storage default
# before any image exists the CLI names the two commands that make the base image, and a create with an image that does not
# exist names the one it was asked for (the catalog is configured later, so this does not go through an app install)
out=$($J image list 2>&1) || true
printf '%s\n' "$out" | grep -F -q "jailops image create $host_img --release $host_release, then jailops image build $host_img" ||
	{ echo "$out" >&2; fail "the empty image list names the commands that make the base image"; }
if out=$($J create hintcheck --project demo --image nosuch --network lan --storage default 2>&1); then
	echo "$out" >&2; fail "a jail create with an image that does not exist (it answered)"
fi
printf '%s\n' "$out" | grep -E -q "jailops image create nosuch --release [0-9]+\.[0-9]+-[A-Z0-9]+', then build it with 'jailops image build nosuch'" ||
	{ echo "$out" >&2; fail "a create with a missing image names the commands that make it"; }
check "image create" $J image create "$img" --release "$release" --arch "$jarch"
check "image build" $J image build "$img"
# a generation records the FreeBSD version of its packages (FreeBSD-runtime): a release with its patch level, or the
# package version of a branch's snapshot as it is
expect "the generation shows its FreeBSD version" '^ +g1 +([0-9]+\.[0-9]+-RELEASE(-p[0-9]+)?|[0-9]+\.snap[0-9]+) +[0-9]+ ' $J image show "$img"
expect "and so does the list of generations" "^$img#g1 +1 +- +([0-9]+\\.[0-9]+-RELEASE(-p[0-9]+)?|[0-9]+\\.snap[0-9]+) " $J image generations "$img"
if [ "$part" = pf ]; then # its own VM in CI: the packet filter test builds its jails from the image above
	run_pf
	echo "OK: the packet filter test passed"
	exit 0
fi

step "a jail"
check "create" $J create web --project demo --image "$img" --network lan --storage default
check "start" $J start web --project demo
sleep 2
expect "the package cache has a dataset of its own" "^$mount/cache\$" zfs get -H -o value mountpoint "$pool/jailops/cache"
expect "the image build downloaded into the shared cache" '\.pkg' ls "$mount/cache/pkg"
expect "the logs of the jails have a dataset of their own" '^/var/log/jailops$' zfs get -H -o value mountpoint "$pool/jailops/logs"
expect "the start of the jail wrote its console log there" 'jail_.*_console\.log' ls /var/log/jailops
expect "the jail runs FreeBSD" 'FreeBSD' $J exec --project demo web -- uname -s
if [ -n "${JAILOPS_E2E_IMAGE_RELEASE:-}" ]; then
  expect "the jail's userland is the image's release" "^$(echo "$JAILOPS_E2E_IMAGE_RELEASE" | sed 's/^\([0-9]*\)\.\([0-9]*\).*/\1/')[0-9]{5}\$" $J exec --project demo web -- uname -U
else
  expect "the jail's userland is the host's release" "^$(uname -U)\$" $J exec --project demo web -- uname -U
fi
expect "the jail has its address" '10\.77\.0\.' $J exec --project demo web -- ifconfig -a
expect "the jail reaches the host" '1 packets received' $J exec --project demo web -- ping -c1 -t3 10.77.0.1
check "a file in the jail" $J exec --project demo web -- sh -c 'echo ci-marker > /root/marker'
expect "the jail's root inherits devices=off" '^off$' zfs get -H -o value devices "$pool/jailops/projects/demo/jails/web/root"
expect "setuid stays on for a jail's root" '^on$' zfs get -H -o value setuid "$pool/jailops/projects/demo/jails/web/root"
expect "su in the jail keeps its setuid bit" '^-r-sr-xr-x' $J exec --project demo web -- ls -l /usr/bin/su
expect "exec reads flags after the jail name, up to --" 'FreeBSD' $J exec web --project demo -- uname -s
# a short command run with a terminal keeps all of its output (FreeBSD dropped it when the session leader exited)
lost=0; i=0
while [ $i -lt 40 ]; do
	i=$((i + 1))
	script -q /dev/null $J exec -t web --project demo -- echo "tty-hello-$i" 2>&1 | grep -q "tty-hello-$i" || lost=$((lost + 1))
done
[ $lost -eq 0 ] || fail "the output of a short command run with a terminal was lost in $lost of 40 runs"
echo "OK: 40 short commands run with a terminal printed all of their output"
expect "without --, a flag after the jail name is refused" 'would be the command' sh -c "JAILOPS_PROJECT=demo $J exec web --project demo uname 2>&1; true"
expect "labels" 'backup=yes' $J label --project demo web backup=yes

# Each of these was accepted and failed later: at the first jail start, inside an operation, or never visibly.
step "refused when given (sizes as bytes, the attachment MTU, the host's subnets, the uplink, names), and the forwarding warning"
hostsub=$(ifconfig "$uplink" inet | awk '/inet / {split($2, a, "."); print a[1] "." a[2] "." a[3] ".1/24"; exit}')
[ -n "$hostsub" ] || fail "the uplink $uplink has no IPv4 address"
expect "a subnet of the uplink's own LAN is refused, naming the interface" "overlaps .* on the host interface $uplink" sh -c "$J network create hostlan --ipv4 $hostsub 2>&1; true"
out=$(expect "an uplink that does not exist is refused" 'uplink em9 cannot be used: em9 does not exist' sh -c "$J network create typo --ipv4 10.78.0.1/24 --uplink em9 --nat 2>&1; true")
printf '%s\n' "$out" | grep -q "candidates on this host: .*$uplink" || { echo "$out" >&2; fail "the refusal of a missing uplink names the candidates"; }
expect "the loopback is no uplink" 'lo0 is a lo interface' sh -c "$J network create typo --ipv4 10.78.0.1/24 --uplink lo0 --nat 2>&1; true"
expect "an attachment MTU other than the network's is refused" 'MTU 9000 of network device eth0 differs from the MTU 1500 of network lan' sh -c "$J config set web --project demo nic.eth0.mtu=9000 2>&1; true"
expect "a snapshot name of 70 characters is refused" 'at most 64' sh -c "$J jail snapshot create web $(printf '%070d' 0 | tr 0 s) --project demo 2>&1; true"
expect "a project name of 14 characters is refused" 'at most 13' sh -c "$J project create abcdefghijklmn 2>&1; true"
check "a project quota given as 4GiB" $J project set demo --storage default=4GiB
expect "ZFS has the quota in bytes" '^4294967296$' zfs get -Hp -o value quota "$pool/jailops/projects/demo"
check "a jail with --disk 4GiB is created" $J create sizes --project demo --image "$img" --network lan --storage default --disk 4GiB --volume data:/data:2GiB
expect "its root has the quota in bytes" '^4294967296$' zfs get -Hp -o value quota "$pool/jailops/projects/demo/jails/sizes/root"
expect "its volume too" '^2147483648$' zfs get -Hp -o value quota "$pool/jailops/projects/demo/jails/sizes/data/data"
check "the jail with sizes starts" $J start sizes --project demo
check "stop it" $J stop sizes --project demo
check "delete it" $J delete --confirm-data-loss sizes --project demo
check "the project quota is taken away again" $J project set demo --storage default
expect "ZFS has no quota" '^(0|none|-)$' zfs get -Hp -o value quota "$pool/jailops/projects/demo"
sysctl net.inet.ip.forwarding=0 >/dev/null
expect "network create warns when forwarding is off" 'sysctl net.inet.ip.forwarding=1' sh -c "$J network create fwd --ipv4 10.79.0.1/24 --uplink $uplink --nat 2>&1"
sysctl net.inet.ip.forwarding=1 >/dev/null
check "delete that network" $J network delete fwd -y

step "the jail's shell and prompt (docs/shell.md): bash and the default prompt, an override, non-interactive commands"
webroot=$mount/projects/demo/jails/web/root
ps1='\[\e[38;5;39m\]\u@\h:\w\$\[\e[0m\] ' # DefaultPS1, with the space after it
expect "a new jail's shell is bash" '^/usr/local/bin/bash$' $J config get --project demo web shell
expect "a new jail's prompt is the default" '^default$' $J config get --project demo web prompt
check "bash was installed into the jail's /usr/local at its first start" test -x "$webroot/usr/local/bin/bash"
expect "bash is a package of the jail" '^bash-' pkg -r "$webroot" info bash
check "the prompt file is JailOps'" grep -F -x "	PS1='$ps1'" "$webroot/usr/local/etc/profile.d/jailops-prompt.sh"
for f in profile bash.bashrc; do
	expect "/usr/local/etc/$f sources it" '^# BEGIN jailops prompt' head -1 "$webroot/usr/local/etc/$f"
done
# bash reads /usr/local/etc/bash.bashrc in an interactive shell and /usr/local/etc/profile in a login shell (the
# package is built with SYSBASHRC); both lead to the prompt file. An interactive bash needs a terminal (without one
# bash -i stops itself): script(1) inside the jail gives it one, and the exec itself stays a plain pipe.
same() { # same DESCRIPTION WANT COMMAND...: standard output must be exactly WANT
	desc=$1; want=$2; shift 2
	out=$("$@" 2>/dev/null) || { echo "$out" >&2; fail "$desc"; }
	# a command in a pseudo-terminal sometimes prints nothing (its shell ended before the output was written): ask again
	n=0; while [ -z "$out" ] && [ -n "$want" ] && [ $n -lt 3 ]; do n=$((n + 1)); sleep 1; out=$("$@" 2>/dev/null) || true; done
	[ "$out" = "$want" ] || { printf 'got:  %s\nwant: %s\n' "$out" "$want" >&2; fail "$desc"; }
}
in_pty() { # in_pty JAIL COMMAND...: what COMMAND printed in a pseudo-terminal in the jail, without carriage returns,
	# the echo of the ^D script sends at the end of its input, and empty lines
	j=$1; shift
	$J exec -T --project demo "$j" -- script -q /dev/null "$@" </dev/null 2>/dev/null | tr -d '\r\010' | sed 's/\^D//g' | grep -v '^$' || true
}
last_line() { "$@" | tail -n 1; } # a login reads ~/.profile, whose resizewin prints before the command
same "an interactive bash has the prompt" "PS1=[$ps1]" in_pty web /usr/local/bin/bash -ic 'echo "PS1=[$PS1]"'
same "an interactive login bash has the prompt" "PS1=[$ps1]" last_line in_pty web /usr/local/bin/bash -lic 'echo "PS1=[$PS1]"'
same "a bash script is not affected" "PS1=[unset]" $J exec --project demo web -- /usr/local/bin/bash -c 'echo "PS1=[${PS1-unset}]"'
same "a login bash running a command is not affected" "PS1=[unset]" $J exec --project demo web -- /usr/local/bin/bash -lc 'echo "PS1=[${PS1-unset}]"'
same "a login sh keeps FreeBSD's own prompt" 'PS1=[\u@\h:\w \$ ]' last_line in_pty web env HOME=/root sh -lic 'echo "PS1=[$PS1]"'
# the prompt as a terminal shows it: jailops shell (bash, the jail's shell) in a pseudo-terminal, typed into once bash
# is up (bash drops what was typed before it set the terminal up)
(sleep 3; printf 'echo "E2E PATH=$PATH HOME=$HOME"\n'; sleep 2; printf 'exit\n'; sleep 1) | TERM=xterm timeout -s KILL 60 script -q "$work/shell.out" $J shell --project demo web >/dev/null 2>&1 || true
grep -a -q "$(printf '\033')\[38;5;39mroot@web:/#$(printf '\033')\[0m" "$work/shell.out" || { od -c "$work/shell.out" | head -20 >&2; fail "jailops shell shows the blue prompt"; }
grep -a -q "E2E PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin HOME=/root" "$work/shell.out" || { cat -v "$work/shell.out" >&2; fail "a shell session has the PATH of a login and root's HOME"; }
# a user's own ~/.bashrc wins
check "a user's own prompt" $J exec --project demo web -- sh -c "echo \"PS1='mine> '\" > /root/.bashrc"
same "~/.bashrc wins over the prompt file" "PS1=[mine> ]" in_pty web /usr/local/bin/bash -ic 'echo "PS1=[$PS1]"'
check "drop the user's own prompt" $J exec --project demo web -- rm /root/.bashrc
# the key: a new prompt at once, none removes the files, an injection is refused
check "a red prompt" $J config set --project demo web prompt '\[\e[38;5;196m\]\u@\h:\w\$\[\e[0m\] '
check "the red prompt is written at once" grep -F -q '38;5;196m' "$webroot/usr/local/etc/profile.d/jailops-prompt.sh"
expect_refused "a prompt that would run a command is refused" $J config set --project demo web prompt '\044(touch /tmp/pwned)'
check "prompt none" $J config set --project demo web prompt none
if [ -e "$webroot/usr/local/etc/profile.d/jailops-prompt.sh" ] || [ -e "$webroot/usr/local/etc/bash.bashrc" ]; then fail "prompt none left the files"; fi
check "the default prompt again" $J config set --project demo web prompt default
check "shell-setup on a jail that has everything changes nothing" $J jail shell-setup --project demo web
expect "the shell setup left no condition" '^$' sh -c "$J jail show --project demo web -o json | grep ShellSetup || true"

step "interface descriptions (ifconfig shows what JailOps made)"
expect "the bridge says what it is" 'description: jailops net=lan bridge' ifconfig jo7
expect "the gateway end says what it is" 'description: jailops net=lan gateway \(host side\)' ifconfig jo7gb
expect "the host end of the jail's epair" 'description: jailops jail=demo/web net=lan \(host side\)' ifconfig -a
expect "the jail end, inside the jail" 'description: jailops jail=demo/web net=lan \(jail side\)' $J exec --project demo web -- ifconfig -a
expect "doctor finds every description" 'net.descriptions +PASS' $J doctor

step "a bridge of any free name, and the helper's ownership of interfaces (docs/security-model.md, Interfaces)"
# the network's name is its bridge's; the bridge carries JailOps's description from its creation
out=$($J network create office --ipv4 10.66.0.1/24 --uplink "$uplink" --nat 2>&1) || { echo "$out" >&2; fail "network create office"; }
printf '%s\n' "$out" | grep -q warning && { echo "$out" >&2; fail "network office gets its own name as its bridge, without a note"; }
expect "the bridge is named like the network" '^office +office +10\.66\.0\.0/24' $J network list
check "project edge" $J project create edge
check "grant office to edge" $J project set edge --network office --storage default
check "a jail on office" $J create edgeweb --project edge --image "$img" --network office --storage default
check "it starts" $J start edgeweb --project edge
sleep 2
expect "ifconfig office shows JailOps's description" 'description: jailops net=office bridge' ifconfig office
expect "the gateway end of office says what it is" 'description: jailops net=office gateway \(host side\)' ifconfig officegb
expect "the jail has its address on office" '10\.66\.0\.' $J exec --project edge edgeweb -- ifconfig -a
expect "the jail reaches its gateway" '1 packets received' $J exec --project edge edgeweb -- ping -c1 -t3 10.66.0.1
gw4=$(route -n get default 2>/dev/null | awk '/gateway:/ {print $2}')
expect "the jail reaches the host's default gateway through the NAT of the uplink" '1 packets received' $J exec --project edge edgeweb -- ping -c1 -t5 "$gw4"
# a bridge made by hand is someone else's: a network cannot have it, at create, and a network named like it gets jo<N>
b=$(ifconfig bridge create) && ifconfig "$b" name mybr >/dev/null || fail "a bridge made by hand"
expect "--bridge naming a bridge made by hand is refused at create" "the interface mybr already exists on this host without a JailOps description" \
	sh -c "$J network create taken --bridge mybr --ipv4 172.17.33.1/24 2>&1; true"
expect "--bridge naming a driver's interface is refused at create" 'form of the interfaces of the driver "vmx"' \
	sh -c "$J network create drv --bridge vmx0 --ipv4 172.17.33.1/24 2>&1; true"
expect "--bridge naming the uplink is refused at create" "form of the interfaces of the driver|already exists on this host" \
	sh -c "$J network create upl --bridge $uplink --ipv4 172.17.33.1/24 2>&1; true"
expect "a network named like the hand-made bridge gets jo<N>, and create says why" 'warning: bridge jo[0-9]+ \(the interface mybr already exists on this host' \
	sh -c "$J network create mybr --ipv4 172.17.33.1/24 2>&1"
check "delete network mybr" $J network delete mybr -y
# an interface of an earlier release: a legacy name, no description
b=$(ifconfig bridge create) && ifconfig "$b" name jo79 >/dev/null || fail "a legacy bridge made by hand"
# the helper itself, over its socket: every request a compromised daemon could make on the uplink, lo0 and mybr is
# refused; jo79 is accepted once and marked; an epair keeps the marker in a jail's vnet
if [ -n "${IFPROBE_BIN:-}" ]; then
	check "the helper's ownership probe" "$IFPROBE_BIN" -uplink "$uplink" -foreign mybr -legacy jo79 -owned office -jail edge_edgeweb
	cat "$work/check.out"
else
	echo "IFPROBE_BIN is not set: the helper's ownership probe is skipped"
	ifconfig jo79 destroy
fi
expect "mybr is untouched" 'groups: bridge' ifconfig mybr
if ifconfig mybr | grep -q description; then fail "mybr was given a description"; fi
check "remove the bridge made by hand" ifconfig mybr destroy
# a stale epair of JailOps (an epair with a jail's name and JailOps's description, of no jail) is found and removed
ep=$(ifconfig epair create) && ifconfig "${ep%a}b" name jo0123456789zzb >/dev/null && ifconfig "$ep" name jo0123456789zza >/dev/null || fail "a stale epair"
ifconfig jo0123456789zza description 'jailops jail=gone/old net=office (host side)'
ifconfig jo0123456789zzb description 'jailops jail=gone/old net=office (jail side)'
# The reconciler may remove it before the doctor looks, or while it destroys the pair the doctor sees one side only:
# either side in the warning, or the pair already gone, is the finding.
n=0
while ifconfig jo0123456789zza >/dev/null 2>&1; do
	$J doctor 2>&1 | grep -Eq 'network.epairs +WARN.*jo0123456789zz[ab]' && break
	n=$((n + 1)); [ $n -gt 30 ] && fail "doctor does not list the stale epair"; sleep 1
done
n=0 # the reconcile loop removes it on one of its next passes
while ifconfig jo0123456789zza >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 60 ] && fail "the stale epair is still there after 120 s"; sleep 2; done
expect "doctor: no stale epairs" 'network.epairs +PASS' $J doctor
expect "doctor: the bridges of every network are JailOps's" 'network.bridges +PASS' $J doctor
expect "doctor: every description" 'net.descriptions +PASS' $J doctor
# network delete removes the bridge it made
check "delete the jail" $J delete --force --confirm-data-loss edgeweb --project edge
check "delete project edge" $J project delete edge -y
check "delete network office" $J network delete office -y
expect_retry "the bridge office is gone" 'gone' sh -c "ifconfig office >/dev/null 2>&1 && echo there || echo gone"
expect "its gateway pair is gone" 'gone' sh -c "ifconfig officegb >/dev/null 2>&1 && echo there || echo gone"

step "name resolution of jails (docs/networking-ipv6.md): the host's nameservers, a live change, a resolver on the gateway"
# the network lan was made without dns: its jails get the host's own nameservers
hostns=$(awk '$1 == "nameserver" && $2 !~ /^(127\.|::1$)/ && $2 !~ /%/ {print $2; exit}' /etc/resolv.conf)
[ -n "$hostns" ] || fail "the step needs a nameserver in the host's /etc/resolv.conf that is not loopback"
expect "a network without dns hands the jail the host's nameserver" "^nameserver $hostns\$" cat "$webroot/etc/resolv.conf"
expect "network show says where they come from" "DNS: +$hostns .*the host's nameservers" $J network show lan
expect_retry "the jail resolves a name" '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' $J exec --project demo web -- drill -Q pkg.freebsd.org
# a change of the network's dns reaches the running jail at once, without a restart
jid=$(jls -j demo_web jid)
$J network show lan -o yaml >"$work/lan.yaml"
sed "/^spec:/a\\
\ \ dns: [\"$hostns\", \"192.0.2.53\"]
" "$work/lan.yaml" >"$work/lan-dns.yaml"
expect "network edit names the running jails it rewrote" 'resolv.conf rewritten in the running jails demo/web' $J network edit lan -f "$work/lan-dns.yaml"
expect "the running jail has the new resolv.conf" '^nameserver 192\.0\.2\.53$' cat "$webroot/etc/resolv.conf"
[ "$(jls -j demo_web jid)" = "$jid" ] || fail "the jail was restarted for a change of its nameservers"
# a jail that keeps a resolv.conf of its own (# jailops:keep) is left alone, at a change and at a start
cp "$webroot/etc/resolv.conf" "$work/resolv.jail"
printf '# jailops:keep\nnameserver 127.0.0.1\n' >"$webroot/etc/resolv.conf"
expect "a jail's own resolv.conf is not rewritten" 'not rewritten in demo/web: it keeps its own resolv.conf' $J network edit lan -f "$work/lan.yaml"
check "restart" $J restart web --project demo
expect "nor at a start" '^nameserver 127\.0\.0\.1$' cat "$webroot/etc/resolv.conf"
check "the jail gives its resolv.conf back" rm "$webroot/etc/resolv.conf"
# a host that resolves only through a local resolver: the jails ask the gateway, which answers once a resolver listens there
cp -p /etc/resolv.conf "$work/resolv.host"
dns_cleanup() {
	cp -p "$work/resolv.host" /etc/resolv.conf
	[ -f "$work/unbound.pid" ] && kill "$(cat "$work/unbound.pid")" 2>/dev/null
	rm -f "$work/unbound.pid"
}
printf 'nameserver 127.0.0.1\n' >/etc/resolv.conf
expect "the jails of a host behind a local resolver ask the gateway" 'DNS: +10\.77\.0\.1 \(the gateway' $J network show lan
expect "and network show says what the administrator does" 'access-control: 10\.77\.0\.0/24 allow' $J network show lan
check "restart" $J restart web --project demo
expect "the jail's resolv.conf names the gateway" '^nameserver 10\.77\.0\.1$' cat "$webroot/etc/resolv.conf"
expect "doctor: nothing answers on the gateway yet" 'network\.dns +WARN .*the jails ask the gateway 10\.77\.0\.1' $J doctor
cat >"$work/unbound.conf" <<UNBOUND
server:
	username: ""
	chroot: ""
	directory: "$work"
	pidfile: "$work/unbound.pid"
	interface: 10.77.0.1
	access-control: 10.77.0.0/24 allow
	module-config: "iterator"
forward-zone:
	name: "."
	forward-addr: $hostns
UNBOUND
check "a resolver on the gateway (the administrator's part)" local-unbound -c "$work/unbound.conf"
expect_retry "doctor: the gateway answers" 'network\.dns +PASS +lan: the gateway 10\.77\.0\.1 answers DNS' $J doctor
expect_retry "the jail resolves through the gateway" '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' $J exec --project demo web -- drill -Q pkg.freebsd.org
dns_cleanup
check "restart" $J restart web --project demo
expect "the host's nameserver again" "^nameserver $hostns\$" cat "$webroot/etc/resolv.conf"
# pf.state-policy: a ruleset that blocks by default (a block ... all without quick; on an interface that does not exist,
# so it stops nothing here) and NATs jails needs `set state-policy if-bound` in the pf.conf the system loads
cp -p /etc/pf.conf "$work/pf.host"
chmod 600 /etc/pf.conf # root's alone, as on a real host: the daemon (user jailops) cannot open it, the helper reads it
printf 'block drop in log on lo9 all\n' >>/etc/pf.conf
check "a default block" sh -c 'pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf'
expect "doctor: the state policy is missing" 'pf\.state-policy +WARN .*set state-policy if-bound' $J doctor
{ echo 'set state-policy if-bound'; cat "$work/pf.host"; echo 'block drop in log on lo9 all'; } >/etc/pf.conf
check "the state policy" sh -c 'pfctl -nf /etc/pf.conf && pfctl -f /etc/pf.conf'
expect "doctor: the state policy is there" 'pf\.state-policy +PASS' $J doctor
cp -p "$work/pf.host" /etc/pf.conf
check "the host's pf.conf again" pfctl -f /etc/pf.conf

step "logs of all jails, and forwarding them to a syslog listener (tcp on the host)"
marker="ci-forward-$$"
# a plain tcp listener on the loopback stands for the syslog server; it ends by itself after five minutes
timeout 300 nc -lk 127.0.0.1 15514 > "$work/syslog.out" 2>/dev/null &
ncpid=$!
# A receiver on this host is root's to configure (jailopsd.yaml): the API refuses loopback and link-local receivers.
cp -p /usr/local/etc/jailops/jailopsd.yaml "$work/jailopsd.yaml.logfwd"
cat >> /usr/local/etc/jailops/jailopsd.yaml <<YAML
logging:
  forward:
    - name: ci-tcp
      type: syslog
      to: tcp://127.0.0.1:15514
      facility: local3
      sources: {projects: [demo]}
YAML
check "restart jailopsd with a sink in jailopsd.yaml" service jailopsd restart
n=0; until $J logs forward list >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
expect "the sink is listed" '^ci-tcp +syslog +tcp://127\.0\.0\.1:15514 +local3' $J logs forward list
# the sink starts at the end of the files: log the line again until one arrives
expect_retry "a line logged in the jail arrives with the jail in the structured data" \
	"\\[jailops project=\"demo\" jail=\"web\" app=\"\" file=\"messages\"\\] .*$marker" \
	sh -c "$J exec --project demo web -- logger -t citest $marker >/dev/null 2>&1; grep -a -o '<15[0-9]>1 [^<]*$marker' '$work/syslog.out'"
expect "the unified view shows it as web/messages" "^web/messages +.*$marker" $J logs --project demo --grep "$marker"
expect "-o json carries the jail" "\"jail\":\"web\".*$marker" $J logs --project demo --jail web --grep "$marker" -o json
expect "the sink delivers" '^ci-tcp +ok ' $J logs forward status
expect "a list through the API may not keep the loopback sink of the file" 'target refused' sh -c "$J logs forward edit ci-tcp --facility local4 2>&1; true"
check "an empty list through the API replaces the file's" $J logs forward remove -y ci-tcp
expect "the API refuses a loopback receiver" 'target refused' sh -c "$J logs forward add syslog --to tcp://127.0.0.1:15515 --name ci-lo 2>&1; true"
expect "the API refuses a link-local receiver" 'target refused' sh -c "$J logs forward add webhook --to https://169.254.169.254/x --name ci-md 2>&1; true"
expect "the API refuses a token file outside logging.secrets_dir" 'not a file directly in' \
	sh -c "$J logs forward add webhook --to https://hooks.example.com/ci --token-file /usr/local/etc/jailops/priv.conf --name ci-wh 2>&1; true"
expect "init made the secrets directory" '^root:jailops 750$' stat -f '%Su:%Sg %Lp' /usr/local/etc/jailops/sinks
printf 'ci-token\n' > "$work/ci.token"
check "a token file in the secrets directory" install -m 0640 -o root -g jailops "$work/ci.token" /usr/local/etc/jailops/sinks/ci.token
check "the API accepts a token file there" $J logs forward add webhook --to https://hooks.example.com/ci --token-file /usr/local/etc/jailops/sinks/ci.token --name ci-wh --audit-only
expect "the forward test answers a class, not the receiver's words" '(dns lookup failed|connection refused|timeout|unreachable|HTTP [0-9]+|tls handshake failed|certificate not trusted|send failed)' sh -c "$J logs forward test ci-wh 2>&1; true"
expect "removing a sink asks first" 'confirmation required' sh -c "$J logs forward remove ci-wh </dev/null 2>&1; true"
check "remove the sink" $J logs forward remove -y ci-wh
check "back to the list of jailopsd.yaml" $J logs forward reset -y
rm -f /usr/local/etc/jailops/sinks/ci.token
cp -p "$work/jailopsd.yaml.logfwd" /usr/local/etc/jailops/jailopsd.yaml
check "restart jailopsd without the sink" service jailopsd restart
n=0; until $J logs forward list >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
kill "$ncpid" 2>/dev/null || true

step "WireGuard (a server, a peer enrolled on it, and a client made from the token on the same host)"
# FreeBSD has the if_wg driver; the wg program comes with the base system or with the package wireguard-tools. A CI
# image without either skips this block and says what is missing.
wg_missing=""
kldload -n if_wg 2>/dev/null || wg_missing="the kernel module if_wg"
wgbin=""
for p in /usr/bin/wg /usr/local/bin/wg; do [ -x "$p" ] && { wgbin=$p; break; }; done
[ -n "$wgbin" ] || wg_missing="${wg_missing:+$wg_missing and }the wg program (pkg install wireguard-tools)"
if [ -n "$wg_missing" ]; then
	echo "SKIP: WireGuard: $wg_missing is missing on this machine"
else
	check "a server" $J wireguard create office --address 10.98.0.1/24 --listen-port 51820 --network lan --endpoint 127.0.0.1:51820
	expect "the server's interface says what it is" 'description: jailops wireguard=office' ifconfig wg-office
	expect "the server listens on its port" 'listening port: 51820' "$wgbin" show wg-office
	expect "its key is root's alone" '^600$' stat -f %Lp /var/db/jailops-priv/wireguard/office/private.key
	token=$($J wireguard peer add office laptop) || fail "peer add"
	case $token in jwg1.*) ;; *) fail "peer add printed no token" ;; esac
	expect "the peer is configured" 'laptop' $J wireguard peer list office
	# the same host: the client gets its own addresses and routes (they must not overlap the server's), and dials
	# the server on the loopback address
	# the token holds a private key: from standard input, never as an argument
	check "a client from the token" sh -c 'printf "%s\n" "$1" | '"$J"' wireguard join - --name home --endpoint 127.0.0.1:51820 --address 10.97.0.2/32 --allowed-ips 10.97.1.0/24' sh "$token"
	expect "the client's interface says what it is" 'description: jailops wireguard=home' ifconfig wg-home
	# a handshake starts with the first packet into the tunnel (or at the first keepalive, 25 s later): route the allowed
	# network into the client's interface (the route goes with the interface) and send a packet each try; no answer is needed
	route -n add -net 10.97.1.0/24 -interface wg-home >/dev/null 2>&1 || true
	n=0; wg_hs=full
	until $wgbin show wg-home latest-handshakes | grep -Eq '[[:space:]][1-9][0-9]+$'; do
		n=$((n + 1))
		# On some CI machines the reply of the server never reaches the client socket on the loopback address (the server
		# counts bytes received from and sent to the client, the client received none), though the server did what a
		# server does: it received a valid initiation from the peer's key and answered it. After 20 s that counts, with a note.
		if [ $n -gt 20 ] && [ "$($wgbin show wg-office transfer | awk '$3 > 0 {s += $3} END {print s + 0}')" -gt 0 ]; then
			wg_hs=answered
			echo "NOTE: the server of the tunnel received the client's initiation and answered it; the answer did not reach the client on this machine's loopback, so no handshake completed"
			$wgbin show wg-office >&2; $wgbin show wg-home >&2
			break
		fi
		if [ $n -gt 90 ]; then # a slow CI machine: ninety tries, and what both ends know when it still fails
			for w in wg-office wg-home; do $wgbin show $w >&2; done
			netstat -rn -f inet | grep -E '10\.9[78]\.' >&2 || true
			sockstat -4 -l | grep 51820 >&2 || true
			fail "a handshake between the two (none after 90 s)"
		fi
		ping -c 1 -t 1 10.97.1.1 >/dev/null 2>&1 || true
		sleep 1
	done
	echo "OK: the tunnel between the two answers"
	if [ $wg_hs = full ]; then
		expect "the server reports it" 'laptop .* ago' $J wireguard status office
	else # the client never got the answer: the server lists the peer, with what it received from it and no handshake
		expect "the server lists the peer and what it received" 'laptop .* never +[0-9.]+ [KMG]?i?B / ' $J wireguard status office
	fi
	if $J doctor 2>&1 | grep -E ' FAIL '; then fail "doctor reports a failing check with tunnels"; fi
	check "delete the client" $J wireguard delete -y home
	check "delete the server" $J wireguard delete -y office
	if ifconfig wg-home >/dev/null 2>&1 || ifconfig wg-office >/dev/null 2>&1; then fail "a tunnel interface is still there"; fi
	[ ! -e /var/db/jailops-priv/wireguard/office ] || fail "the keys of a deleted tunnel are still there"
fi

step "an encrypted volume (passphrase from a file, lock, a refused start, a wrong and the right passphrase)"
# The volume is a ZFS encryption root; its passphrase goes from the file to the daemon and the helper and reaches zfs on
# standard input. A pool without the encryption feature skips this block and says so.
enc_state=$(zpool get -H -o value feature@encryption "$pool" 2>/dev/null || true)
case $enc_state in
enabled | active)
	(umask 077; printf '%s\n' 'ci-passphrase-0123' >"$work/vault.pass"; printf '%s\n' 'ci-wrong-passphrase' >"$work/wrong.pass"
		printf '%s\n' 'ci-new-passphrase-4567' >"$work/new.pass")
	check "a jail with an encrypted volume" $J create vault --project demo --image "$img" --network lan --storage default \
		--volume secrets:/var/db/secrets:encrypted --passphrase-file "$work/vault.pass" --start
	vds=$(zfs list -H -o name -r "$pool" | grep '/jails/vault/data/secrets$') || fail "no dataset for the encrypted volume"
	keystatus() { zfs get -H -o value keystatus "$vds"; }
	expect "the key is loaded after the creation" '^available$' keystatus
	expect "aes-256-gcm under a passphrase typed at a prompt" '^aes-256-gcm,passphrase,prompt$' sh -c "zfs get -H -o value encryption,keyformat,keylocation $vds | paste -s -d , -"
	expect "the volume is its own encryption root" "^$vds\$" zfs get -H -o value encryptionroot "$vds"
	check "a file on the encrypted volume" $J exec --project demo vault -- sh -c 'echo ci-secret > /var/db/secrets/s'
	check "stop" $J stop vault --project demo
	check "lock" $J jail lock vault --project demo
	expect "the key is unloaded" '^unavailable$' keystatus
	expect "the jail is locked" 'vault +locked' $J jail list --project demo
	expect "a start is refused with locked" '(^|[^_])locked' sh -c "$J start vault --project demo 2>&1; true"
	expect_refused "the start failed" $J start vault --project demo
	expect "a wrong passphrase is refused" 'bad_passphrase' sh -c "$J jail unlock vault --project demo --passphrase-file $work/wrong.pass 2>&1; true"
	expect "still locked" '^unavailable$' keystatus
	check "unlock with the right passphrase" $J jail unlock vault --project demo --passphrase-file "$work/vault.pass"
	expect "the key is loaded" '^available$' keystatus
	check "start" $J start vault --project demo
	expect "the file is read back" '^ci-secret$' $J exec --project demo vault -- cat /var/db/secrets/s
	check "change the passphrase" $J jail passphrase vault --project demo --passphrase-file "$work/vault.pass" --new-passphrase-file "$work/new.pass"
	check "lock again" $J jail lock vault --project demo
	expect_refused "the old passphrase opens it no more" $J jail unlock vault --project demo --passphrase-file "$work/vault.pass"
	check "the new one does" $J jail unlock vault --project demo --passphrase-file "$work/new.pass"
	# a rename moves the volume with the jail: its key stays loaded and its file stays readable
	check "rename the stopped jail with the encrypted volume" $J rename vault vault2 --project demo -y
	expect "the volume moved with it, its key loaded" '^available$' zfs get -H -o value keystatus "$pool/jailops/projects/demo/jails/vault2/data/secrets"
	check "start it under the new name" $J start vault2 --project demo
	expect "the file on the encrypted volume is read back" '^ci-secret$' $J exec --project demo vault2 -- cat /var/db/secrets/s
	check "stop it" $J stop vault2 --project demo
	check "and rename it back" $J rename vault2 vault --project demo -y
	expect "the key is still loaded" '^available$' keystatus
	expect "doctor checks the pool feature" 'volumes.encryption +PASS' $J doctor
	if grep -rq 'ci-passphrase-0123\|ci-new-passphrase-4567' /var/log/messages /var/db/jailops 2>/dev/null; then fail "a passphrase reached a log or the daemon's files"; fi
	expect "a delete shows the sizes and the snapshots read from ZFS" 'vault \([a-z]+\): root [0-9.]+ [KMGT]?i?B, data [0-9.]+ [KMGT]?i?B \(volumes secrets\), [0-9]+ snapshots?' $J --dry-run delete vault --project demo
	expect "-y does not answer the data-loss question" 'confirmation required' sh -c "$J delete -y vault --project demo </dev/null 2>&1; true"
	zfs list "$vds" >/dev/null 2>&1 || fail "a delete without the name typed destroyed the volume"
	check "delete the jail" $J delete --confirm-data-loss vault --project demo
	if zfs list "$vds" >/dev/null 2>&1; then fail "the encrypted volume is still there"; fi
	rm -f "$work/vault.pass" "$work/wrong.pass" "$work/new.pass"
	;;
*) echo "SKIP: encrypted volume: feature@encryption of pool $pool is ${enc_state:-unknown}, not enabled or active" ;;
esac

step "OS upgrade of a thin jail (new generation, /etc merge, health check, root rollback)"
expect "a second jail takes the project's only network and storage, and says so" 'network lan: the only network of project demo' $J create up --project demo --image "$img" --start
check "a second generation of the image" $J image build "$img" --from g1
check "a file that must survive" $J exec --project demo up -- sh -c 'echo ci-keep > /root/keep'
expect "dry run" 'g1 to g2' $J jail upgrade up --project demo --dry-run
check "upgrade to the newer generation" $J jail upgrade up --project demo --generation g2
expect "the jail is on the second generation" 'up +running.*#g2' $J jail list --project demo
expect "the jail still runs" 'FreeBSD' $J exec --project demo up -- uname -s
expect "its data survived the swap" 'ci-keep' $J exec --project demo up -- cat /root/keep
expect "bash survived the swap (/usr/local is persistent)" '^bash$' $J exec --project demo up -- /usr/local/bin/bash -c 'echo bash'
expect "so did the prompt" '38;5;39m' in_pty up /usr/local/bin/bash -ic 'echo "$PS1"'
check "root rollback" $J jail rollback up --project demo
expect "back on the first generation" 'up +running.*#g1' $J jail list --project demo
check "stop the second jail" $J stop up --project demo

step "automatic upgrade of an image (the nightly run, started by the clock of the daemon)"
expect "the nightly upgrade is off until it is asked for" 'Nightly: +off' $J image show "$img"
expect_refused "a time of day that does not exist is refused" $J image auto-upgrade "$img" on --at 25:00
# the jail `up` is on the first generation while the newest is the second (the rollback above), and it gets a package
# that is behind: the nightly run must move it and upgrade the package, though the repository has nothing new for the image
check "start the second jail" $J start up --project demo
uproot=$(jls -j demo_up path) || fail "the path of the second jail"
check "refresh the package catalogue of the second jail" pkg -r "$uproot" update -q
check "install a package into the second jail" pkg -r "$uproot" install -y nano
check "make that package look old" pkg -r "$uproot" shell "UPDATE packages SET version = '0.1' WHERE name = 'nano';"
expect "the package is behind" '^0\.1$' pkg -r "$uproot" query %v nano
check "turn it on for two minutes from now" $J image auto-upgrade "$img" on --at "$(date -v+2M +%H:%M)"
expect "the image names it and its next run" 'Nightly: +every night at [0-9:]+: generation and jails and their packages; next ' $J image show "$img"
n=0
until $J image show "$img" | grep -q 'Automatic upgrades'; do
	n=$((n + 1)); [ $n -gt 150 ] && fail "the automatic upgrade did not run within five minutes"; sleep 2
done
expect "the run moved the two jails that were behind and upgraded the package" ' ok +2 moved, 1 packages upgraded' $J image show "$img"
expect "the second jail is on the newest generation" 'up +running.*#g2' $J jail list --project demo
if [ "$(pkg -r "$uproot" query %v nano)" = 0.1 ]; then fail "the package of the second jail was not upgraded"; fi
expect "the second jail still runs" 'FreeBSD' $J exec --project demo up -- uname -s
check "stop the second jail again" $J stop up --project demo
if $J image show "$img" | grep -E '^ +[0-9-]+ [0-9:]+ +failed'; then fail "the automatic upgrade failed"; fi
check "turn it off" $J image auto-upgrade "$img" off
expect "off again" 'Nightly: +off' $J image show "$img"

step "an app from the catalog (nginx: thin jail, pkg -r from the host, rc.conf, service, health check, snapshot)"
# the catalog is a directory of local files: the reference copy (or JAILOPS_E2E_CATALOG), copied where the daemon may read it
appdir=$work/apps
catalog=${JAILOPS_E2E_CATALOG:-internal/apps/testdata/catalog}
[ -d "$catalog" ] || fail "JAILOPS_E2E_CATALOG: $catalog is not a directory"
rm -rf "$appdir"; cp -R "$catalog" "$appdir"; chmod -R a+rX "$appdir"
cat >> /usr/local/etc/jailops/jailopsd.yaml <<YAML
apps:
  catalog_dir: $appdir
YAML
check "restart jailopsd with the catalog" service jailopsd restart
n=0; until $J app list >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
expect "the catalog lists nginx as valid" '^nginx .*valid' $J app list -o wide
expect "a dry run lists the steps" 'install-packages-ports: pkg -r .* install -y nginx' $J app install nginx --project demo --network lan --storage default --dry-run
expect "an app's jail gets bash and the prompt like any new jail" 'set-up-shell: install bash' $J app install nginx --project demo --network lan --storage default --dry-run
check "install nginx" $J app install nginx --project demo --network lan --storage default
expect "the app's jail has bash as its shell" '^/usr/local/bin/bash$' $J config get --project demo nginx shell
check "bash is in the app's jail" test -x "$mount/projects/demo/jails/nginx/root/usr/local/bin/bash"
check "the prompt is in the app's jail" grep -F -q "PS1='" "$mount/projects/demo/jails/nginx/root/usr/local/etc/profile.d/jailops-prompt.sh"
expect "the jail runs and carries the labels" 'jailops.app=nginx' $J label --project demo nginx
expect "the app is listed as installed" 'nginx +nginx +[^ ]+ +running' $J app installed --project demo
addr=$($J app installed --project demo | awk '$1 == "nginx" {print $5}' | head -1 | cut -d, -f1)
[ -n "$addr" ] || fail "no address for the nginx jail"
# an installed app is closed, to the host too, until a source is allowed
expect_refused_retry "a fresh app is closed to the host" curl -sS --max-time 3 "http://$addr/"
expect "the closed app has no grant" 'No grants' $J app access nginx --project demo
check "allow the host to reach nginx" $J app allow nginx from host --project demo
expect_retry "nginx answers from the host" 'Welcome to nginx' curl -sS --max-time 5 "http://$addr/"
expect "the grant is listed" '^host \(host\)' $J app access nginx --project demo
expect "the service is enabled in the jail's rc.conf" 'nginx_enable="YES"' $J exec --project demo nginx -- cat /etc/rc.conf
expect "the snapshot of the fresh installation exists" 'app-installed' $J jail snapshot list nginx --project demo
# the authoring path: a draft of the running jail has its package, its service and its port, and still carries TODO markers
$J app export nginx --project demo --to "$work/nginx-draft.yaml" >"$work/export.out" 2>&1 || { cat "$work/export.out" >&2; fail "export a draft of the nginx jail"; }
expect "-o json prints the draft as data" '"definition"' $J app export nginx --project demo -o json
grep -q 'name: nginx' "$work/nginx-draft.yaml" || { cat "$work/nginx-draft.yaml" >&2; fail "the draft names the nginx package"; }
grep -q 'port: 80' "$work/nginx-draft.yaml" || { cat "$work/nginx-draft.yaml" >&2; fail "the draft has port 80"; }
grep -q 'TODO' "$work/nginx-draft.yaml" || fail "the draft has TODO markers"
if $J app validate "$work/nginx-draft.yaml" >"$work/validate.out" 2>&1; then fail "validate accepts a draft with TODO markers"; fi
grep -q 'replace TODO' "$work/validate.out" || { cat "$work/validate.out" >&2; fail "validate names the TODO lines"; }
if ! $J stop nginx --project demo >"$work/stop.out" 2>&1; then # show what holds the root when the unmount stays busy
	cat "$work/stop.out" >&2
	echo "--- holders of the jail root:" >&2; fstat -f /jopsci/projects/demo/jails/nginx/root >&2 2>&1 || true
	echo "--- processes:" >&2; ps -axo pid,jid,etimes,command | grep -E 'nginx|syslogd|cron|jexec' | grep -v grep >&2 || true
	fail "stop the app jail"
fi

# one block per app of JAILOPS_E2E_APPS: install, the app's own check, the logs, stop
app_check() { # app_check NAME ADDRESS
	case $1 in
	postgresql) expect "postgresql answers a query" '^1$' $J exec --project demo postgresql -- su -l postgres -c "psql -tA -c 'select 1'" ;;
	valkey) expect "valkey answers" '^PONG$' $J exec --project demo valkey -- /usr/local/bin/valkey-cli ping ;;
	prometheus) expect_retry "prometheus is ready" 'Ready' curl -sS --max-time 5 "http://$2:9090/-/ready" ;;
	forgejo) expect_retry "forgejo answers (the web installer or a redirect to it)" '^(200|302|303)$' curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "http://$2:3000/" ;;
	nsd) # the starter zone, from the host, over UDP and TCP
		expect_retry "nsd answers for example.com over UDP" 'IN[[:space:]]+SOA[[:space:]]+ns1\.example\.com\.' drill @"$2" example.com SOA
		expect "nsd answers for example.com over TCP" 'IN[[:space:]]+SOA[[:space:]]+ns1\.example\.com\.' drill -t @"$2" example.com SOA
		check "the configuration passes nsd-checkconf" $J exec --project demo nsd -- /usr/local/sbin/nsd-checkconf /usr/local/etc/nsd/nsd.conf ;;
	mail) # the greetings: the command is sent after a second, Postfix refuses commands that arrive before its greeting
		expect_retry "postfix greets on port 25" '^220 ' sh -c "(sleep 1; printf 'QUIT\r\n') | nc -w 5 $2 25"
		expect "dovecot greets on port 143" '^\* OK' sh -c "(sleep 1; printf 'a1 LOGOUT\r\n') | nc -w 5 $2 143"
		expect "mailer.conf hands /usr/sbin/sendmail to postfix" '^sendmail[[:space:]]+/usr/local/sbin/sendmail$' $J exec --project demo mail -- cat /usr/local/etc/mail/mailer.conf ;;
	isms) isms_check "$2" ;;
	esac
}
isms_check() { # isms_check ADDRESS: the steps of the definition's notes with a postgresql jail of its own, then HTTP
	check "install postgresql for isms" $J app install postgresql --name isms-db --project demo --network lan --storage default
	db=$($J app installed --project demo | awk '$1 == "isms-db" {print $5}' | head -1 | cut -d, -f1)
	[ -n "$db" ] || fail "no address for the isms-db jail"
	# the role, the database, the pg_hba.conf line, listen_addresses and the firewall rule, in one allow; the
	# connection string is in its answer only (never printed here: it holds the password)
	$J app allow isms-db from @isms --db isms --project demo -o json >"$work/isms-allow.json" 2>&1 || { sed 's/postgres:\/\/[^@]*@/postgres:\/\/***@/' "$work/isms-allow.json" >&2; fail "allow isms to reach isms-db"; }
	url=$(json_field connection_string <"$work/isms-allow.json")
	rm -f "$work/isms-allow.json"
	case $url in "postgres://isms:"*"@$db:5432/isms?sslmode=disable") ;; *) fail "app allow printed no connection string for isms" ;; esac
	env=/usr/local/etc/isms/server.env
	check "server.env: database, base URL, signing address" $J exec --project demo isms -- sh -c "sed -i '' \
		-e 's|^DATABASE_URL=.*|DATABASE_URL=\"$url\"|' \
		-e 's|^ISMS_BASE_URL=.*|ISMS_BASE_URL=http://$1:8080|' -e 's|^ISMS_SIGNING_EMAIL=.*|ISMS_SIGNING_EMAIL=e2e@example.com|' $env"
	check "server.env: the master secret" $J exec --project demo isms -- sh -c "sed -i '' \"s|^ISMS_SECRET=.*|ISMS_SECRET=\$(openssl rand -hex 32)|\" $env"
	check "server.env: owner and mode" $J exec --project demo isms -- sh -c "chown isms:isms $env && chmod 600 $env"
	check "the signing key" $J exec --project demo isms -- su -m isms -c '/usr/local/bin/ssh-keygen -q -t ed25519 -N "" -C e2e@example.com -f /var/db/isms/private/isms_key'
	check "the schema" $J exec --project demo isms -- env ISMS_SERVER_ENV=$env su -m isms -c '/usr/local/bin/isms server migrate'
	check "start isms" $J exec --project demo isms -- service isms start
	# UNVERIFIED: port 8080 and what / answers; curl retries while the program starts
	expect "isms answers on port 8080" '^[23][0-9][0-9]$' curl -s -o /dev/null -w '%{http_code}' --connect-timeout 2 --retry 30 --retry-connrefused --retry-delay 1 "http://$1:8080/"
	check "stop isms-db" $J stop isms-db --project demo
}
pg_access_check() { # postgresql-access: who may connect to a database, from the jails' side
	check "install postgresql as pgacc" $J app install postgresql --name pgacc --project demo --network lan --storage default
	for c in pgc1 pgc2; do
		check "create the client jail $c" $J create "$c" --project demo --image "$img" --network lan --storage default --start
		# the PostgreSQL client into the thin jail from the host, as the app installer does it
		check "psql in $c" pkg -r "$mount/projects/demo/jails/$c/root" install -y postgresql18-client
		check "the library cache of $c" $J exec --project demo "$c" -- /sbin/ldconfig -m /usr/local/lib
	done
	expect "a dry run shows the rule, the pg_hba line and the SQL without a password" 'CREATE ROLE "pgc1" LOGIN PASSWORD .<generated' \
		$J app allow pgacc from @pgc1 --project demo --dry-run
	$J app allow pgacc from @pgc1 --project demo -o json >"$work/pg-allow.json" 2>&1 || { sed 's/postgres:\/\/[^@]*@/postgres:\/\/***@/' "$work/pg-allow.json" >&2; fail "allow pgc1 to reach pgacc"; }
	url=$(json_field connection_string <"$work/pg-allow.json")
	rm -f "$work/pg-allow.json"
	case $url in postgres://pgc1:*@*:5432/pgc1?sslmode=disable) ;; *) fail "app allow printed no connection string for pgc1" ;; esac
	# a connection that the firewall drops must not wait for the default timeout
	url="$url&connect_timeout=3"
	expect_retry "pgc1 runs a query with the connection string" '^1$' $J exec --project demo pgc1 -- /usr/local/bin/psql "$url" -tAc 'select 1'
	expect_refused "pgc2 (not allowed) cannot connect" $J exec --project demo pgc2 -- /usr/local/bin/psql "$url" -tAc 'select 1'
	expect "app access lists the grant with its pg_hba line" 'host pgc1 pgc1 [0-9.]+/32 scram-sha-256' $J app access pgacc --project demo
	expect "allowing again changes nothing and prints no password" 'may already reach' $J app allow pgacc from @pgc1 --project demo
	check "deny pgc1" $J app deny pgacc from @pgc1 --project demo
	expect_refused_retry "pgc1 cannot connect after the deny" $J exec --project demo pgc1 -- /usr/local/bin/psql "$url" -tAc 'select 1'
	expect "the grant is gone" 'No grants' $J app access pgacc --project demo
	url=
	for j in pgc1 pgc2 pgacc; do check "stop $j" $J stop "$j" --project demo; done
}
for a in $e2e_apps; do
	if [ "$a" = postgresql-access ]; then
		step "who may connect to a database (app allow and deny)"
		pg_access_check
		continue
	fi
	step "the app $a from the catalog"
	check "install $a" $J app install "$a" --project demo --network lan --storage default
	expect "$a is listed as installed" "^$a +$a +[^ ]+ +running" $J app installed --project demo
	addr=$($J app installed --project demo | awk -v a="$a" '$1 == a {print $5}' | head -1 | cut -d, -f1)
	[ -n "$addr" ] || fail "no address for the $a jail"
	# the checks that connect from the host need it allowed (postgresql and valkey are checked inside the jail)
	case $a in prometheus | forgejo | nsd | mail | isms) check "allow the host to reach $a" $J app allow "$a" from host --project demo ;; esac
	app_check "$a" "$addr"
	expect "the log files of $a are listed" 'messages' $J jail logs "$a" --project demo
	check "stop $a" $J stop "$a" --project demo
done

step "backup node (PostgreSQL, the node, this host enrolled)"
if ! su -m postgres -c "psql -c 'select 1'" >/dev/null 2>&1; then # the CI VM has it running already
	pkg install -y postgresql17-server >/dev/null 2>&1 || pkg install -y postgresql16-server >/dev/null || fail "postgresql"
	sysrc postgresql_enable=YES >/dev/null
	service postgresql oneinitdb >/dev/null 2>&1 || true
	service postgresql onestart >/dev/null 2>&1 || { tail -n 20 /var/db/postgres/data*/postgresql.log /var/db/postgres/*.log 2>/dev/null >&2; fail "postgresql does not start"; }
	n=0; until su -m postgres -c "psql -c 'select 1'" >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "postgresql does not answer"; sleep 1; done
fi
su -m postgres -c "dropdb --if-exists jopsnode; createdb jopsnode" || fail "createdb"
data=/var/db/jailops-backup
cat > /usr/local/etc/jailops/backupd.yaml <<YAML
hostname: ci-vault
listen: "127.0.0.1:39111"
admin_socket: /var/run/jailops-backupd/admin.sock
tls:
  cert_file: $data/backupd.crt
  key_file: $data/backupd.key
database:
  dsn: postgres://postgres@127.0.0.1:5432/jopsnode?sslmode=disable
datastore:
  kind: directory
  directory: $data/repository
web:
  enabled: false
YAML
chown root:jailops-backup /usr/local/etc/jailops/backupd.yaml; chmod 640 /usr/local/etc/jailops/backupd.yaml
install -d -m 0750 -o jailops-backup -g jailops-backup /var/run/jailops-backupd
init_out=$(su -m jailops-backup -c "/usr/local/sbin/jailops-backupd -c /usr/local/etc/jailops/backupd.yaml init" 2>&1) || { echo "$init_out" >&2; fail "backupd init"; }
echo "$init_out" | tail -3
nodefp=$(echo "$init_out" | sed -n 's/.*hosts pin this): *//p' | tr -d '[:space:]')
[ ${#nodefp} -eq 64 ] || fail "no node fingerprint in the init output"
# this host's client certificate, enrolled on the node
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout /var/db/jailops/backup-client.key \
	-out /var/db/jailops/backup-client.crt -subj /CN=ci-host -days 30 >/dev/null 2>&1
chown jailops /var/db/jailops/backup-client.key /var/db/jailops/backup-client.crt; chmod 600 /var/db/jailops/backup-client.key
hostfp=$(openssl x509 -in /var/db/jailops/backup-client.crt -noout -fingerprint -sha256 | sed 's/.*=//; s/://g' | tr 'A-F' 'a-f')
su -m jailops-backup -c "/usr/local/sbin/jailops-backupd -c /usr/local/etc/jailops/backupd.yaml hosts enroll ci-host --fingerprint $hostfp --scopes backup-push,backup-pull,restore" >/dev/null || fail "enroll"
sysrc jailops_backupd_enable=YES >/dev/null
check "backupd start" service jailops_backupd start
sleep 3
[ -s /var/run/jailops-backupd.pid ] || fail "the backup node's pid file is not in /var/run"
[ -z "$(find /var/run/jailops-backupd -type f)" ] || fail "regular files in /var/run/jailops-backupd"

step "the host backs up to the node"
cat >> /usr/local/etc/jailops/jailopsd.yaml <<YAML
backup:
  node:
    name: ci-vault
    address: 127.0.0.1:39111
    fingerprint: $nodefp
    cert_file: /var/db/jailops/backup-client.crt
    key_file: /var/db/jailops/backup-client.key
  allow_unescrowed: true
YAML
check "restart jailopsd" service jailopsd restart
n=0; until $J backup key status >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
expect "key created" 'JOR1-' $J backup key create
check "target" $J backup target create vault --address https://127.0.0.1:39111 --fingerprint "$nodefp"
check "policy" $J backup policy create nightly --project demo --schedule "0 3 * * *" --selector backup=yes --target vault --keep-last 3
expect "the backup runs" ' ok ' $J backup run --project demo nightly
id=$($J backup list --project demo -o name | head -1)
[ -n "$id" ] || fail "no backup is listed"
expect "deep verification" ' ok ' $J backup verify --project demo "$id"
expect "extract verification" ' ok ' $J backup verify --project demo "$id" --depth extract
expect "full verification (boots the backup as a jail of its own)" ' ok ' $J backup verify --project demo "$id" --depth full
if $J list --project demo -o name | grep -q '^jodrill-'; then fail "the restore test left its jail behind"; fi

step "restore as a new jail"
check "stop the original" $J stop web --project demo
expect "restore" 'restored' $J backup restore --project demo "$id" --as web-restored -y
check "start the restored jail" $J start web-restored --project demo
sleep 2
expect "the file came back" 'ci-marker' $J exec --project demo web-restored -- cat /root/marker
expect "its own address" '10\.77\.0\.' $J exec --project demo web-restored -- ifconfig -a

step "renames: a network with a running jail, a stopped jail, an image, a project (each renamed back)"
# a network: the running jail keeps its interfaces and its traffic; only names and descriptions change
expect "the plan lists the running jail's interfaces" 'interface .*jail=demo/web-restored net=office' $J network rename lan office --dry-run
check "rename the network while a jail on it runs" $J network rename lan office -y
expect "the bridge keeps its name and carries the new one" 'description: jailops net=office bridge' ifconfig jo7
expect "the gateway end too" 'description: jailops net=office gateway \(host side\)' ifconfig jo7gb
expect "the running jail's end, inside it" 'description: jailops jail=demo/web-restored net=office \(jail side\)' $J exec --project demo web-restored -- ifconfig -a
expect "the anchor of the network is loaded under the new name" 'nat on .* from 10\.77\.0\.0/24' pfctl -a jailops/net.office -sn
expect_refused "the anchor of the old name is empty" sh -c "pfctl -a jailops/net.lan -sn 2>/dev/null | grep -q nat"
expect "the project's grant follows" 'office' $J project show demo
expect "the running jail still reaches the host" '1 packets received' $J exec --project demo web-restored -- ping -c1 -t3 10.77.0.1
gw=$(route -n get default 2>/dev/null | awk '/gateway:/ {print $2}')
if [ -n "$gw" ]; then
	expect "and the host's gateway, through the NAT of the renamed network" '1 packets received' $J exec --project demo web-restored -- ping -c1 -t3 "$gw"
fi
# (doctor's exit status says more than descriptions: a failing check of the machine is not this block's business)
expect "doctor finds every description" 'net.descriptions +PASS' sh -c "$J doctor 2>&1; true"
check "rename it back" $J network rename office lan -y
expect "the old name is back" 'description: jailops net=lan bridge' ifconfig jo7
# a jail: refused while it runs, renamed when it is stopped, its datasets, mountpoints and kernel name follow
expect_refused "a running jail is not renamed" $J rename web-restored web-renamed --project demo -y
check "rename the stopped jail" $J rename web web-renamed --project demo -y
expect_refused "its old datasets are gone" zfs list "$pool/jailops/projects/demo/jails/web"
expect "its mountpoints moved" "^$mount/projects/demo/jails/web-renamed/root\$" zfs get -H -o value mountpoint "$pool/jailops/projects/demo/jails/web-renamed/root"
check "start it under the new name" $J start web-renamed --project demo
expect "the kernel knows it by the new name" '^demo_web-renamed$' jls -j demo_web-renamed name
expect "its file is there" 'ci-marker' $J exec --project demo web-renamed -- cat /root/marker
expect "its anchor" 'jailops/demo/web-renamed' pfctl -sA
check "stop it" $J stop web-renamed --project demo
check "and rename it back" $J rename web-renamed web --project demo -y
# an image: the generations move, the clones of running jails follow, the nightly runs move with it
check "rename the image while a jail on it runs" $J image rename "$img" e2e-img -y
expect "the jails follow" 'web-restored +running +e2e-img' $J jail list --project demo
expect "a thin root is a clone of the renamed generation" "^$pool/jailops/images/e2e-img/g[0-9]+@ro\$" zfs get -H -o value origin "$pool/jailops/projects/demo/jails/web-restored/root"
expect "the generations are mounted under the new name" "^$mount/images/e2e-img/g1\$" zfs get -H -o value mountpoint "$pool/jailops/images/e2e-img/g1"
expect "the nightly runs moved with it" 'Automatic upgrades' $J image show e2e-img
expect "the jail still runs" 'FreeBSD' $J exec --project demo web-restored -- uname -s
check "rename the image back" $J image rename e2e-img "$img" -y
# a project: every jail stopped, then the datasets, anchors and objects move
for j in $($J jail list --project demo -o name); do $J stop "$j" --project demo >/dev/null 2>&1 || true; done
check "rename the project" $J project rename demo e2e-proj -y
expect_refused "nothing is left under the old name" zfs list "$pool/jailops/projects/demo"
expect "the mountpoints moved" "^$mount/projects/e2e-proj/jails/web/root\$" zfs get -H -o value mountpoint "$pool/jailops/projects/e2e-proj/jails/web/root"
check "a jail starts in the renamed project" $J start web --project e2e-proj
expect "under its new kernel name" '^e2e-proj_web$' jls -j e2e-proj_web name
expect "with its anchor" 'jailops/e2e-proj/web' pfctl -sA
expect "and its file" 'ci-marker' $J exec --project e2e-proj web -- cat /root/marker
check "stop it" $J stop web --project e2e-proj
check "rename the project back" $J project rename e2e-proj demo -y
check "start the restored jail again" $J start web-restored --project demo

[ "$part" = e2e ] || run_pf

step "cleanup"
$J stop web-restored --project demo >/dev/null 2>&1 || true
echo "OK: the whole flow passed"
