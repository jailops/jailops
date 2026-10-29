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
set -eu

cd "$(dirname "$0")/.."
root=$(pwd)
pool=jopsci
part=${JAILOPS_E2E_PART:-all}
case $part in all | e2e | pf) ;; *) echo "JAILOPS_E2E_PART must be all, e2e or pf" >&2; exit 2 ;; esac
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
trap 'rc=$?; [ $rc -eq 0 ] || echo "FAILED (exit $rc) in step: $current" >&2' EXIT
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
# the catalog is a directory of local files: the repository's starter catalog, copied where the daemon may read it
appdir=$work/apps
rm -rf "$appdir"; cp -R packaging/apps "$appdir"; chmod -R a+rX "$appdir"
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
expect "nginx answers from the host" 'Welcome to nginx' curl -s --max-time 10 "http://$addr/"
expect "the service is enabled in the jail's rc.conf" 'nginx_enable="YES"' $J exec --project demo nginx -- cat /etc/rc.conf
expect "the snapshot of the fresh installation exists" 'app-installed' $J jail snapshot list nginx --project demo
if ! $J stop nginx --project demo >"$work/stop.out" 2>&1; then # show what holds the root when the unmount stays busy
	cat "$work/stop.out" >&2
	echo "--- holders of the jail root:" >&2; fstat -f /jopsci/projects/demo/jails/nginx/root >&2 2>&1 || true
	echo "--- processes:" >&2; ps -axo pid,jid,etimes,command | grep -E 'nginx|syslogd|cron|jexec' | grep -v grep >&2 || true
	fail "stop the app jail"
fi

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
