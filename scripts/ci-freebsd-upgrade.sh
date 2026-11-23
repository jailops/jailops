#!/bin/sh
# An upgrade in place, as an administrator does it (docs/upgrade.md): the newest RELEASED packages are installed from the
# package repository (the steps of the install guide, the key fingerprint checked), a small fleet is made with their
# command line (an image, a network, a project, two jails with data, a volume, a policy, an exposure), and then the
# packages of THIS tree replace them with pkg, the services are restarted, and everything must be as it was: the jails
# still run, their data and addresses are there, doctor has no new failure, the firewall of a jail is back, the console
# loads, and a jail can be restarted. The upgrade guarantee of docs/upgrade-guarantee.md, on a real machine.
#
#   JAILOPS_BIN_DIR=DIR sh scripts/ci-freebsd-upgrade.sh
#
# Runs as root on a disposable machine (the CI VM): it makes and destroys its own scratch pool and changes /etc/pf.conf and
# /etc/rc.conf. It needs pkg.jailops.net, the FreeBSD package mirror and the host's own release for the base image.
#
# JAILOPS_BIN_DIR  the binaries of this tree (jailops, jailopsd, jailops-priv, jailops-sync, jailops-backupd, as
#                  `just build-freebsd` names them), required
#
# Exit status 75 and the line SKIPPED mean a mirror did not answer in four tries: nothing was tested.
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)
pool=jopsup
mount=/jopsup
work=/tmp/jailops-upgrade
poolfile=/tmp/$pool.img
version=$(tr -d '[:space:]' < version.txt)
major=$(freebsd-version -u | cut -d. -f1)
case $(uname -p) in
amd64) goarch=amd64 ;;
aarch64 | arm64) goarch=arm64 ;;
*) echo "unsupported architecture $(uname -p)" >&2; exit 2 ;;
esac
release=$(freebsd-version -u | sed 's/-p[0-9]*$//')
img=freebsd-$(echo "$release" | sed -E 's/^([0-9]+)\.([0-9]+)-.*/\1-\2/')
J=/usr/local/bin/jailops
# the fingerprint of the package key: the value of KEY_FINGERPRINT in the jailops-packages repository and in its README
FINGERPRINT=b9fb3c980044dbd7a37c6e53e6a17510b054ba3af65e538eb2eae0f14be26a70

bin=${JAILOPS_BIN_DIR:-}
[ -n "$bin" ] && [ -d "$bin" ] || { echo "JAILOPS_BIN_DIR must name a directory with the binaries of this tree" >&2; exit 2; }

current=start
trap 'rc=$?; [ $rc -eq 0 ] || [ $rc -eq 75 ] || echo "FAILED (exit $rc) in step: $current" >&2' EXIT
t_start=$(date +%s)
step() { current=$*; printf '\n== %s  [+%ss]\n' "$*" "$(( $(date +%s) - t_start ))"; }
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
waitfor() { # waitfor DESCRIPTION SECONDS COMMAND...: the command must succeed within SECONDS (polled twice a second)
	desc=$1; secs=$2; shift 2
	end=$(( $(date +%s) + secs ))
	until "$@" >"$work/waitfor.out" 2>&1; do
		[ "$(date +%s)" -ge "$end" ] && { cat "$work/waitfor.out" >&2; fail "$desc (not after $secs s)"; }
		sleep 0.5 # sleep-ok: the poll of waitfor
	done
}
net() { # net DESCRIPTION COMMAND...: a command that reaches a mirror, tried four times; a mirror that does not answer ends the run as skipped
	desc=$1; shift; n=0
	until "$@" >"$work/net.out" 2>&1; do
		n=$((n + 1))
		if [ $n -ge 4 ]; then
			cat "$work/net.out" >&2
			echo "SKIPPED: $desc did not work in four tries (a mirror does not answer): nothing was tested" >&2
			exit 75
		fi
		sleep 10 # sleep-ok: the pause between the tries at a mirror
	done
}

rm -rf "$work"; mkdir -p "$work"
echo "machine: $(sysctl -n kern.vm_guest hw.ncpu hw.model | tr '\n' ' ')"

step "scratch pool, pf and forwarding"
# what an earlier run left: services, jails and mounts of the scratch pool
service jailopsd onestop >/dev/null 2>&1 || true
service jailops_priv onestop >/dev/null 2>&1 || true
for j in $(jls -h name path 2>/dev/null | awk -v m="$mount/" 'index($2, m) == 1 { print $1 }'); do jail -r "$j" >/dev/null 2>&1 || true; done
for m in $(mount -p | awk -v p="$pool/" -v m="$mount/" 'index($1, p) == 1 || index($1, m) == 1 { print $2 }' | sort -r); do umount -f "$m" >/dev/null 2>&1 || true; done
zpool destroy -f "$pool" 2>/dev/null || true
# the installed packages and the state of an earlier run on this machine: the run starts from the released packages alone
pkg delete -fy jailops jailops-client jailops-backupd >/dev/null 2>&1 || true
rm -rf /var/db/jailops /var/run/jailops /usr/local/etc/jailops /var/log/jailops
rm -f "$poolfile"
truncate -s "${JAILOPS_UPGRADE_POOL_SIZE:-8G}" "$poolfile"
check "zpool create" zpool create -m none "$pool" "$poolfile"
for m in if_epair nullfs pf; do kldload -n $m 2>/dev/null || true; done
touch /etc/pf.conf
grep -q 'anchor "jailops/\*"' /etc/pf.conf || echo 'anchor "jailops/*"' >> /etc/pf.conf
if ! grep -q '^set state-policy if-bound' /etc/pf.conf; then { echo 'set state-policy if-bound'; cat /etc/pf.conf; } > /etc/pf.conf.new && mv /etc/pf.conf.new /etc/pf.conf; fi
sysrc pf_enable=YES >/dev/null
pfctl -s info 2>/dev/null | grep -q 'Status: Enabled' || service pf onestart >/dev/null 2>&1 || true
check "pf.conf loads" pfctl -f /etc/pf.conf
sysctl net.inet.ip.forwarding=1 net.inet6.ip6.forwarding=1 >/dev/null
sysrc gateway_enable=YES ipv6_gateway_enable=YES >/dev/null

step "the newest released packages, installed as the install guide says"
# the steps of the install guide (the key fingerprint is checked, nothing is changed on a mismatch)
trust() {
	sh -eu <<JAILOPS_TRUST
FINGERPRINT=$FINGERPRINT
tmp=\$(mktemp -d -t jailops-trust.XXXXXXXX)
trap 'rm -rf "\$tmp"' EXIT
fetch -qo "\$tmp/jailops.pub" https://pkg.jailops.net/jailops.pub
if [ "\$(sha256 -q "\$tmp/jailops.pub")" != "\$FINGERPRINT" ]; then
  echo "jailops.pub does not have the fingerprint \$FINGERPRINT: nothing was changed" >&2
  exit 1
fi
cat > "\$tmp/jailops.conf" <<'EOF'
jailops: {
  url: "https://pkg.jailops.net/\${ABI}/latest",
  signature_type: "pubkey",
  pubkey: "/usr/local/etc/pkg/keys/jailops.pub",
  enabled: yes
}
EOF
mkdir -p /usr/local/etc/pkg/keys /usr/local/etc/pkg/repos
install -m 0644 "\$tmp/jailops.pub" /usr/local/etc/pkg/keys/jailops.pub
install -m 0644 "\$tmp/jailops.conf" /usr/local/etc/pkg/repos/jailops.conf
pkg update -r jailops
JAILOPS_TRUST
}
net "the trust steps of the install guide" trust
net "pkg install of the released packages" pkg install -y -r jailops jailops-client jailops
prev=$(pkg query %v jailops)
echo "the previous release: $prev; this tree: $version"
check "users exist" id jailops

step "the services of the previous release"
check "jailops-priv init" /usr/local/libexec/jailops-priv init -pool "$pool" -mount "$mount"
sysrc jailops_priv_enable=YES jailopsd_enable=YES jailopsd_web=YES >/dev/null
check "jailops_priv start" service jailops_priv start
check "jailopsd start" service jailopsd start
n=0
until [ -S /var/run/jailops/jailops.sock ]; do
	n=$((n + 1)); [ $n -gt 30 ] && fail "jailopsd did not create its socket"
	sleep 1 # sleep-ok: poll of a condition
done
waitfor "the daemon answers" 60 $J host show
$J doctor > "$work/doctor.before" 2>&1 || true
grep -E ' FAIL ' "$work/doctor.before" | sed -E 's/^[^ ]+ +//' | sort > "$work/fails.before" || true

step "a small fleet, made with the command line of the previous release"
net "the host holds bash and what it needs in the package cache" pkg fetch -y -d -o PKG_CACHEDIR=$mount/cache/pkg bash
uplink=$(route -n get default 2>/dev/null | awk '/interface:/ {print $2}')
[ -n "$uplink" ] || uplink=$(ifconfig -l | tr ' ' '\n' | grep -v -E '^(lo0|pflog0)$' | head -1)
check "network create" $J network create lan --ipv4 10.77.0.1/24 --uplink "$uplink" --nat
check "project create" $J project create demo
check "grants" sh -c "$J network grant lan --project demo && $J storage grant default --project demo"
check "image create" $J image create "$img" --release "$release" --arch "$goarch"
net "image build" $J image build "$img"
check "an address set" $J firewall address-set create office --project demo --address 192.0.2.7 --comment "the office"
check "a policy" $J firewall policy create web-access --project demo --default-in allow --default-out allow
check "a rule" $J firewall rule add web-access in --project demo --action allow --proto tcp --from set:office --port 443 --comment "https from the office"
check "a volume" $J volume create data --project demo --storage default --size 1GiB
check "web" $J create web --project demo --image "$img" --network lan --storage default --autostart --firewall-policy web-access
check "attach the volume" $J volume attach web data /srv/data --project demo
check "start web" $J start web --project demo
check "db" $J create db --project demo --image "$img" --network lan --storage default
check "expose web" $J expose web 80 --family 4 --project demo
waitfor "web answers a command" 180 $J exec --project demo web -- true
check "a file in the jail" $J exec --project demo web -- sh -c 'echo upgrade-marker > /root/marker'
check "a file in the volume" $J exec --project demo web -- sh -c 'echo volume-marker > /srv/data/marker'
# docs/upgrade.md: a jail of the root layout of 0.9.2 and before that this release does not support is converted with
# the released version before the upgrade (the value below is the identifier that release prints for that layout)
for j in web db; do
	if $J jail show "$j" --project demo -o json | grep -q '"layout": "thin"'; then
		check "convert $j to its own root before the upgrade" $J jail convert "$j" --layout thick --project demo
	fi
done
waitfor "web answers a command after its conversion" 180 $J exec --project demo web -- true
$J list --project demo -c name,state,ipv4,autostart > "$work/list.before"
cat "$work/list.before"
jls -h name | grep '^demo_' | sort > "$work/jls.before"
[ -s "$work/jls.before" ] || fail "no jail of the fleet runs"
zfs list -H -o name -r "$pool/jailops/projects" | sort > "$work/zfs.before"

step "the upgrade: the packages of this tree replace the released ones, the services restart"
check "the packages of this tree" sh scripts/pkg-build.sh "$version" "$goarch" "$bin" "$work/pkgs" "$major"
abi=$(pkg config ABI)
pkgdir=$work/pkgs/$abi/latest/All
check "pkg add of the new packages" pkg add -f "$pkgdir/jailops-client-$version.pkg" "$pkgdir/jailops-$version.pkg"
# docs/upgrade.md: both services keep running the old binary until they restart (the jails are kernel jails: they stay)
check "jailops_priv restart" service jailops_priv restart
check "jailopsd restart" service jailopsd restart
waitfor "the daemon answers again" 90 $J host show

step "everything is as it was"
expect "the server is the new version" "Server: +$version" $J version
expect "the jail list is the same" 'web +running' $J list --project demo -c name,state
$J list --project demo -c name,state,ipv4,autostart > "$work/list.after"
diff "$work/list.before" "$work/list.after" >"$work/list.diff" || { cat "$work/list.diff" >&2; fail "the list of jails changed with the upgrade"; }
jls -h name | grep '^demo_' | sort > "$work/jls.after"
diff "$work/jls.before" "$work/jls.after" >"$work/jls.diff" || { cat "$work/jls.diff" >&2; fail "the kernel jails changed with the upgrade"; }
zfs list -H -o name -r "$pool/jailops/projects" | sort > "$work/zfs.after"
diff "$work/zfs.before" "$work/zfs.after" >"$work/zfs.diff" || { cat "$work/zfs.diff" >&2; fail "the datasets changed with the upgrade"; }
expect "the file in the jail is there" '^upgrade-marker$' $J exec --project demo web -- cat /root/marker
expect "the file in the volume is there" '^volume-marker$' $J exec --project demo web -- cat /srv/data/marker
expect "the network is as it was" '10\.77\.0\.0/24' $J network show lan
expect "the project is as it was" 'demo' $J project show demo
expect "the policy is as it was" 'set:office' $J firewall policy show web-access --project demo
expect "the address set is as it was" '192\.0\.2\.7' $J firewall address-set show office --project demo
expect "the volume is as it was" 'data' $J volume list --project demo
expect "the exposure is as it was" '80/tcp' $J list --project demo -c name,exposed
expect "the firewall of the jail names its policy" 'web-access' $J firewall show web --project demo
waitfor "the firewall anchor of the jail is loaded again" 90 sh -c 'pfctl -a jailops/demo/web -sr 2>/dev/null | grep -q .'
waitfor "the rule of the policy is in the anchor" 90 sh -c 'pfctl -a jailops/demo/web -sr 2>/dev/null | grep -q "port = https"'
$J doctor > "$work/doctor.after" 2>&1 || true
grep -E ' FAIL ' "$work/doctor.after" | sed -E 's/^[^ ]+ +//' | sort > "$work/fails.after" || true
new=$(comm -13 "$work/fails.before" "$work/fails.after")
[ -z "$new" ] || { printf '%s\n' "$new" >&2; fail "doctor has a failure the previous release did not have"; }
waitfor "the web console is served after the upgrade" 60 sh -c "curl -ks https://127.0.0.1:39110/ | grep -qi '<html'"
check "restart web" $J restart web --project demo
waitfor "web answers a command after its restart" 120 $J exec --project demo web -- true
expect "the file in the jail survived the restart" '^upgrade-marker$' $J exec --project demo web -- cat /root/marker
check "a new jail on the old image" $J create after --project demo --image "$img" --network lan --storage default --start
waitfor "the new jail answers" 180 $J exec --project demo after -- true
check "delete the new jail" $J delete --stop --confirm-data-loss after --project demo
echo "OK: the upgrade from $prev to $version kept the fleet"
