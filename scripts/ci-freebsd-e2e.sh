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
# -web: the quick start's console is on the TLS port, and the API is the same port (the certificate is self-signed)
step "web console and API on port 39110"
n=0; until curl -ks -o /dev/null https://127.0.0.1:39110/api/v1; do n=$((n + 1)); [ $n -gt 20 ] && fail "port 39110 does not answer"; sleep 1; done
curl -ks https://127.0.0.1:39110/ | grep -qi '<html' || fail "the web console is not served on port 39110"
curl -ks https://127.0.0.1:39110/api/v1 | grep -q '"untrusted"' || fail "the API is not served on port 39110"
$J doctor || true
if $J doctor 2>&1 | grep -E ' FAIL '; then fail "doctor reports a failing check"; fi

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
	host_same() { # host_same: every file and the host name are the originals, byte for byte
		for f in resolv.conf ntp.conf rc.conf; do
			if [ -f "$horig/$f" ]; then cmp -s "$horig/$f" /etc/$f || fail "/etc/$f is not the original"
			else [ ! -e /etc/$f ] || fail "/etc/$f exists and did not before"; fi
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
	check "restore" $J -y host restore "$snap"
	host_same
	expect "the restore kept a snapshot of its own" ' +identity +restore ' $J host snapshots
	cp -p "$horig/priv.conf" /usr/local/etc/jailops/priv.conf; cp -p "$horig/jailopsd.yaml" /usr/local/etc/jailops/jailopsd.yaml
	check "restart jailops_priv" service jailops_priv restart
	check "restart jailopsd" service jailopsd restart
	n=0; until $J host show >/dev/null 2>&1; do n=$((n + 1)); [ $n -gt 30 ] && fail "the daemon does not answer after the restart"; sleep 1; done
	host_dirty=
	host_same
fi

run_pf() {
	step "the packet filter, with real traffic (scripts/ci-freebsd-pf.sh)"
	# the full matrix takes minutes of waiting in its last phases; the quick form still covers every rule path
	# FORGE_BIN (set by the caller) runs phase 0e, the forged-frame probes; without it phase 0e is skipped
	PF_FAST=1 FORGE_BIN="${FORGE_BIN:-}" sh "$root/scripts/ci-freebsd-pf.sh" || fail "the packet filter test failed"
}

step "image, network, project"
uplink=$(route -n get default 2>/dev/null | awk '/interface:/ {print $2}')
[ -n "$uplink" ] || uplink=$(ifconfig -l | tr ' ' '\n' | grep -v -E '^(lo0|pflog0)$' | head -1)
jarch=$goarch
check "image create" $J image create freebsd --release "$release" --arch "$jarch" --alias stable
check "image build" $J image build freebsd
check "network create" $J network create lan --bridge jo7 --ipv4 10.77.0.1/24 --uplink "$uplink" --nat
check "project create" $J project create demo
check "project grant" $J project set demo --network lan --storage default
if [ "$part" = pf ]; then # its own VM in CI: the packet filter test builds its jails from the image above
	run_pf
	echo "OK: the packet filter test passed"
	exit 0
fi

step "a jail"
check "create" $J create web --project demo --image freebsd@stable --network lan --storage default
check "start" $J start web --project demo
sleep 2
expect "the jail runs FreeBSD" 'FreeBSD' $J exec --project demo web -- uname -s
expect "the jail's userland is the host's release" "^$(uname -U)\$" $J exec --project demo web -- uname -U
expect "the jail has its address" '10\.77\.0\.' $J exec --project demo web -- ifconfig -a
expect "the jail reaches the host" '1 packets received' $J exec --project demo web -- ping -c1 -t3 10.77.0.1
check "a file in the jail" $J exec --project demo web -- sh -c 'echo ci-marker > /root/marker'
expect "labels" 'backup=yes' $J label --project demo web backup=yes

step "interface descriptions (ifconfig shows what JailOps made)"
expect "the bridge says what it is" 'description: jailops net=lan bridge' ifconfig jo7
expect "the gateway end says what it is" 'description: jailops net=lan gateway \(host side\)' ifconfig jo7gb
expect "the host end of the jail's epair" 'description: jailops jail=demo/web net=lan \(host side\)' ifconfig -a
expect "the jail end, inside the jail" 'description: jailops jail=demo/web net=lan \(jail side\)' $J exec --project demo web -- ifconfig -a
expect "doctor finds every description" 'net.descriptions +PASS' $J doctor

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
	check "a client from the token" $J wireguard join "$token" --name home --endpoint 127.0.0.1:51820 --address 10.97.0.2/32 --allowed-ips 10.97.1.0/24
	expect "the client's interface says what it is" 'description: jailops wireguard=home' ifconfig wg-home
	expect_retry "a handshake between the two" '[[:space:]][1-9][0-9]+$' "$wgbin" show wg-home latest-handshakes
	expect "the server reports it" 'laptop .* ago' $J wireguard status office
	if $J doctor 2>&1 | grep -E ' FAIL '; then fail "doctor reports a failing check with tunnels"; fi
	check "delete the client" $J wireguard delete -y home
	check "delete the server" $J wireguard delete -y office
	if ifconfig wg-home >/dev/null 2>&1 || ifconfig wg-office >/dev/null 2>&1; then fail "a tunnel interface is still there"; fi
	[ ! -e /var/db/jailops-priv/wireguard/office ] || fail "the keys of a deleted tunnel are still there"
fi

step "OS upgrade of a thin jail (new generation, /etc merge, health check, root rollback)"
check "create a second jail" $J create up --project demo --image freebsd@stable --network lan --storage default --start
check "a second generation of the image" $J image build freebsd --from g1
check "a file that must survive" $J exec --project demo up -- sh -c 'echo ci-keep > /root/keep'
expect "dry run" 'g1 to g2' $J jail upgrade up --project demo --dry-run
check "upgrade to the newer generation" $J jail upgrade up --project demo --generation g2
expect "the jail is on the second generation" 'up +running.*#g2' $J jail list --project demo
expect "the jail still runs" 'FreeBSD' $J exec --project demo up -- uname -s
expect "its data survived the swap" 'ci-keep' $J exec --project demo up -- cat /root/keep
check "root rollback" $J jail rollback up --project demo
expect "back on the first generation" 'up +running.*#g1' $J jail list --project demo
check "stop the second jail" $J stop up --project demo

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
check "install nginx" $J app install nginx --project demo --network lan --storage default
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
$J app export nginx --project demo -o "$work/nginx-draft.yaml" >"$work/export.out" 2>&1 || { cat "$work/export.out" >&2; fail "export a draft of the nginx jail"; }
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
		check "create the client jail $c" $J create "$c" --project demo --image freebsd@stable --network lan --storage default --start
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

[ "$part" = e2e ] || run_pf

step "cleanup"
$J stop web-restored --project demo >/dev/null 2>&1 || true
echo "OK: the whole flow passed"
