#!/bin/sh
# ci-boot-vm.sh: boot a FreeBSD snapshot VM (the coming 15.x or CURRENT) with qemu and KVM and give it root ssh, without a console.
#
#   sh scripts/ci-boot-vm.sh resolve VERSION          the dated file name of the newest snapshot image (the key of the image cache)
#   sh scripts/ci-boot-vm.sh fetch VERSION DIR        download it, check its sha256, unpack it to DIR/base.qcow2
#   sh scripts/ci-boot-vm.sh boot DIR PORT KEYFILE    boot an overlay of DIR/base.qcow2, ssh as root on 127.0.0.1:PORT
#   sh scripts/ci-boot-vm.sh update PORT KEYFILE      bring the VM to the newest snapshot of its branch (pkg upgrade; a reboot when the kernel changed)
#   sh scripts/ci-boot-vm.sh install PORT KEYFILE PKG...   packages the tests need, bounded and tried three times
#   sh scripts/ci-boot-vm.sh stop DIR                 stop it
#
# VERSION is a directory of https://download.freebsd.org/snapshots/VM-IMAGES/ (15.2-STABLE, 16.0-CURRENT). The image is the
# BASIC-CLOUDINIT one with ZFS. `boot` hands the public key of KEYFILE.pub to the image as a NoCloud seed (a cidata ISO made with
# pycdlib, in a virtualenv of DIR), so the first boot needs no console; when ssh does not answer within SEED_WAIT seconds the same
# key is put in place through the serial console, and the boot waits again. KVM only: without /dev/kvm it stops. The size of the
# machine is QEMU_SMP (default 3) and QEMU_MEM (default 8G). Used by .github/workflows/freebsd-ahead.yml and by hand.
set -eu
base_url=https://download.freebsd.org/snapshots/VM-IMAGES

die() { echo "ci-boot-vm: $*" >&2; exit 1; }

resolve() {
	[ -n "${1:-}" ] || die "usage: resolve VERSION"
	curl -fsS --retry 3 --max-time 60 "$base_url/$1/amd64/Latest/CHECKSUM.SHA256" |
		sed -n 's/^SHA256 (\(FreeBSD-.*-BASIC-CLOUDINIT-.*-zfs\.qcow2\.xz\)) = .*/\1/p' | head -1
}

fetch() {
	version=${1:-}; dir=${2:-}
	[ -n "$version" ] && [ -n "$dir" ] || die "usage: fetch VERSION DIR"
	mkdir -p "$dir"
	sums=$(curl -fsS --retry 3 --max-time 60 "$base_url/$version/amd64/Latest/CHECKSUM.SHA256") || die "no checksum file for $version"
	dated=$(printf '%s\n' "$sums" | sed -n 's/^SHA256 (\(FreeBSD-.*-BASIC-CLOUDINIT-.*-zfs\.qcow2\.xz\)) = .*/\1/p' | head -1)
	[ -n "$dated" ] || die "no BASIC-CLOUDINIT zfs qcow2 image in the checksums of $version"
	want=$(printf '%s\n' "$sums" | sed -n "s/^SHA256 ($dated) = //p")
	# the directory Latest serves the file without the date and the commit in its name
	plain=$(printf '%s' "$dated" | sed 's/-BASIC-CLOUDINIT-[0-9]*-[0-9a-f]*-[0-9]*-/-BASIC-CLOUDINIT-/')
	if [ ! -s "$dir/base.qcow2" ] || [ "$(cat "$dir/image-name" 2>/dev/null)" != "$dated" ]; then
		rm -f "$dir/image.qcow2.xz" "$dir/base.qcow2"
		curl -fSL --retry 5 --retry-delay 10 --max-time 1800 -o "$dir/image.qcow2.xz" "$base_url/$version/amd64/Latest/$plain" || die "download of $plain failed"
		got=$(sha256sum "$dir/image.qcow2.xz" | cut -d' ' -f1)
		[ "$got" = "$want" ] || die "sha256 of $plain is $got, the checksum file says $want (a snapshot that was replaced while it was fetched: run again)"
		xz -dc "$dir/image.qcow2.xz" > "$dir/base.qcow2"
		rm -f "$dir/image.qcow2.xz"
		printf '%s\n' "$dated" > "$dir/image-name"
	fi
	echo "$dated"
}

# the NoCloud seed: an ISO 9660 volume with the label cidata and the files user-data and meta-data
make_seed() {
	dir=$1; pub=$2
	[ -x "$dir/venv/bin/python" ] || { python3 -m venv "$dir/venv" && "$dir/venv/bin/pip" install -q 'pycdlib==1.22.0'; } >&2
	"$dir/venv/bin/python" - "$dir/seed.iso" "$pub" <<'PY'
import io, sys
import pycdlib
out, pub = sys.argv[1], sys.argv[2].strip()
user = f"""#cloud-config
ssh_pwauth: false
runcmd:
  - mkdir -p /root/.ssh
  - chmod 700 /root/.ssh
  - sh -c 'echo "{pub}" > /root/.ssh/authorized_keys'
  - chmod 600 /root/.ssh/authorized_keys
  - sh -c 'grep -q "^PermitRootLogin" /etc/ssh/sshd_config || echo "PermitRootLogin prohibit-password" >> /etc/ssh/sshd_config'
  - sysrc sshd_enable=YES
""".encode()
meta = b"instance-id: ahead-1\nlocal-hostname: freebsd\n"
iso = pycdlib.PyCdlib()
iso.new(interchange_level=3, joliet=3, rock_ridge="1.09", vol_ident="cidata")
for name, data in (("user-data", user), ("meta-data", meta)):
    iso.add_fp(io.BytesIO(data), len(data), "/" + name.upper().replace("-", "_") + ";1",
               rr_name=name, joliet_path="/" + name)
iso.write(out)
iso.close()
PY
}

# the public key through the serial console: the way the first VMs were made, when the seed did not apply
console_login() {
	dir=$1; pub=$2
	python3 - "$dir/serial.sock" "$pub" <<'PY'
import socket, sys, time
sock, pub = sys.argv[1], sys.argv[2].strip()
s = socket.socket(socket.AF_UNIX); s.connect(sock); s.settimeout(1)
def rd(t=1.0):
    out = b''; end = time.time() + t
    while time.time() < end:
        try:
            d = s.recv(4096)
            if d: out += d; end = time.time() + t
        except socket.timeout: pass
    return out.decode(errors='replace')
buf = ''
for _ in range(60):
    buf += rd(2)
    if 'login:' in buf: break
s.sendall(b'root\r'); time.sleep(2); rd(2)
for c in ['mkdir -p /root/.ssh && chmod 700 /root/.ssh',
          "echo '%s' > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys" % pub,
          'grep -q "^PermitRootLogin" /etc/ssh/sshd_config || echo "PermitRootLogin prohibit-password" >> /etc/ssh/sshd_config',
          'sysrc sshd_enable=YES; service sshd restart']:
    s.sendall((c + '\r').encode()); time.sleep(2); rd(2)
PY
}

sshq() { # sshq PORT KEY COMMAND...
	p=$1; k=$2; shift 2
	ssh -i "$k" -p "$p" -o BatchMode=yes -o ConnectTimeout=5 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no \
		-o UserKnownHostsFile=/dev/null -l root 127.0.0.1 "$@"
}

wait_ssh() { # wait_ssh PORT KEY SECONDS
	end=$(( $(date +%s) + $3 ))
	until sshq "$1" "$2" true >/dev/null 2>&1; do
		[ "$(date +%s)" -lt "$end" ] || return 1
		sleep 5 # sleep-ok: poll of the VM coming up
	done
}

boot() {
	dir=${1:-}; port=${2:-}; key=${3:-}
	[ -n "$dir" ] && [ -n "$port" ] && [ -r "$key" ] && [ -r "$key.pub" ] || die "usage: boot DIR PORT KEYFILE (with KEYFILE.pub)"
	[ "$port" != 2222 ] || die "port 2222 is not a test VM"
	[ -s "$dir/base.qcow2" ] || die "no $dir/base.qcow2: fetch first"
	test -e /dev/kvm || die "no /dev/kvm: the VM would run on TCG"
	for f in /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_VARS_4M.fd; do [ -r "$f" ] || die "$f is missing (package ovmf)"; done
	rm -f "$dir/vm.qcow2" "$dir/serial.sock"
	qemu-img create -q -f qcow2 -F qcow2 -b "$(cd "$dir" && pwd)/base.qcow2" "$dir/vm.qcow2" 24G
	cp /usr/share/OVMF/OVMF_VARS_4M.fd "$dir/vars.fd"
	make_seed "$dir" "$(cat "$key.pub")"
	( cd "$dir" && qemu-system-x86_64 -accel kvm -machine q35 -cpu host -smp "${QEMU_SMP:-3}" -m "${QEMU_MEM:-8G}" \
		-drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd -drive if=pflash,format=raw,file=vars.fd \
		-drive file=vm.qcow2,if=virtio,format=qcow2 -drive file=seed.iso,if=none,id=seed,format=raw,readonly=on -device ide-cd,drive=seed \
		-netdev user,id=n0,hostfwd=tcp:127.0.0.1:"$port"-:22 -device virtio-net-pci,netdev=n0 \
		-display none -serial unix:serial.sock,server,nowait -daemonize -pidfile qemu.pid )
	if wait_ssh "$port" "$key" "${SEED_WAIT:-300}"; then
		echo "ci-boot-vm: root ssh through the seed"
	else
		echo "ci-boot-vm: the seed did not give ssh in ${SEED_WAIT:-300} s: the key goes through the serial console"
		console_login "$dir" "$(cat "$key.pub")"
		wait_ssh "$port" "$key" 240 || die "no ssh on port $port after the console login"
		echo "ci-boot-vm: root ssh through the console"
	fi
	sshq "$port" "$key" 'freebsd-version -kru; uname -m'
}

# update PORT KEY: the newest packages of the branch the VM follows, the kernel that was installed running after a reboot. A VM that
# cannot be updated is not a failure of JailOps: the line says "environment" and the tests run on what the VM has.
update() {
	port=${1:-}; key=${2:-}
	[ -n "$port" ] && [ -r "$key" ] || die "usage: update PORT KEYFILE"
	if ! out=$(sshq "$port" "$key" 'env IP_VERSION=4 pkg update -f >/dev/null 2>&1 && env ASSUME_ALWAYS_YES=yes pkg upgrade -y 2>&1' 2>&1); then
		echo "environment: the VM did not update: $(echo "$out" | tail -1) (tested as it is)"; return 0
	fi
	# a new kernel runs after a reboot, and the modules of the new one do not load into the old one (pf.ko with it): two snapshots of one
	# branch have the same version string, so freebsd-version cannot tell; the packages that were upgraded can
	if printf '%s\n' "$out" | grep -q 'FreeBSD-kernel' ||
		[ "$(sshq "$port" "$key" 'freebsd-version -k')" != "$(sshq "$port" "$key" 'freebsd-version -r')" ]; then
		sshq "$port" "$key" 'shutdown -r now' >/dev/null 2>&1 || true
		sleep 20 # sleep-ok: the VM has to go down before it can be seen to come back
		wait_ssh "$port" "$key" 300 || { echo "environment: the VM did not come back after the update within 5 minutes"; return 1; }
	fi
	echo "updated: $(sshq "$port" "$key" 'freebsd-version -kru' | tr '\n' ' ')"
}

# install PORT KEY PKG...: what the parts need on top of the image. One unreachable mirror must not cost the job its half hour: the
# fetches are bounded, the VM has no IPv6 route, and the install is tried three times with the catalogue fetched again between the tries.
install_pkgs() {
	port=${1:-}; key=${2:-}; shift 2 || true
	[ -n "$port" ] && [ -r "$key" ] && [ $# -gt 0 ] || die "usage: install PORT KEYFILE PKG..."
	sshq "$port" "$key" "WANT='$*' sh -s" <<'VM' || { echo "environment: the package mirror did not give the packages in three attempts"; return 1; }
have() {
	case "$1" in
	postgresql17-server) [ -x /usr/local/bin/postgres ] ;;
	git-lite) command -v git >/dev/null ;;
	*) command -v "$1" >/dev/null ;;
	esac
}
want=""
for p in $WANT; do have "$p" || want="$want $p"; done
[ -n "$want" ] || { echo "the VM has $WANT: nothing to install"; exit 0; }
mkdir -p /usr/local/etc/pkg/repos
printf '%s\n' 'IP_VERSION = 4;' 'FETCH_TIMEOUT = 30;' 'FETCH_RETRY = 3;' > /usr/local/etc/pkg.conf
export ASSUME_ALWAYS_YES=yes
case "$want" in *postgresql17-server*) limit=420 ;; *) limit=120 ;; esac
n=0
while [ "$n" -lt 3 ]; do
	n=$((n + 1))
	# shellcheck disable=SC2086
	if timeout "$limit" pkg install -y $want; then exit 0; fi
	echo "pkg install of$want failed (attempt $n of 3)"
	sleep $((n * 15))
	timeout 120 pkg update -f || true
done
exit 1
VM
}

stop() {
	dir=${1:-}; [ -n "$dir" ] || die "usage: stop DIR"
	if [ -s "$dir/qemu.pid" ]; then kill "$(cat "$dir/qemu.pid")" 2>/dev/null || true; rm -f "$dir/qemu.pid"; fi
}

cmd=${1:-}; [ $# -gt 0 ] && shift
case $cmd in
resolve) resolve "$@" ;;
fetch) fetch "$@" ;;
boot) boot "$@" ;;
update) update "$@" ;;
install) install_pkgs "$@" ;;
stop) stop "$@" ;;
*) die "usage: ci-boot-vm.sh resolve|fetch|boot|stop ..." ;;
esac
