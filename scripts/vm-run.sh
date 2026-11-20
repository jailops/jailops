#!/bin/sh
# vm-run.sh PORT PART [SHARD]    run a part of the FreeBSD end-to-end tests on a test VM, from a clean state
#
#   sh scripts/vm-run.sh 2229 e2e 1       the end-to-end flow, shard 1 (1, 2 or 3)
#   sh scripts/vm-run.sh 2229 pf a        the packet filter test, shard a (a, b, c or d)
#   sh scripts/vm-run.sh 2229 workflows   the daily tasks of docs/testing-on-freebsd.md
#   sh scripts/vm-run.sh 2229 linux       the Linux jail block
#   sh scripts/vm-run.sh 2229 prepare     the machine set up with an image, a network and a project, left running
#
# PORT is the local ssh port of the VM (root, key in $VM_KEY, default temp/vm/key). The run takes the lock of the VM
# (scripts/vm-lock.sh), builds the binaries with the web consoles embedded, copies them and the scripts over, removes what an
# earlier run left (services, jails, the test pool, interfaces) and starts the script. The VM is disposable: see
# docs/testing-on-freebsd.md.
set -eu
cd "$(dirname "$0")/.."
[ $# -ge 2 ] || { sed -n 2,9p "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
port=$1 part=$2 shard=${3:-}
[ "$port" != 2222 ] || { echo "vm-run: port 2222 is not a test VM" >&2; exit 2; }
case $part in e2e) : "${shard:=1}" ;; pf) : "${shard:=all}" ;; workflows | linux | prepare) ;; *) echo "vm-run: part is e2e, pf, workflows, linux or prepare" >&2; exit 2 ;; esac
if [ "${VM_RUN_LOCKED:-}" != 1 ]; then
	VM_RUN_LOCKED=1 exec sh scripts/vm-lock.sh run "$port" "${VM_LOCK_WAIT:-30}" -- sh "$0" "$@"
fi
test -s internal/webui/dist/index.html && test -s internal/backupui/dist/index.html || { echo "vm-run: the web consoles are not built: just web-all" >&2; exit 1; }
key=${VM_KEY:-temp/vm/key}
[ -r "$key" ] || { echo "vm-run: no ssh key at $key (set VM_KEY)" >&2; exit 1; }
ssh_vm() { ssh -i "$key" -p "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -l root 127.0.0.1 "$@"; }
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bin"
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(tr -d '[:space:]' < version.txt)" -o "$stage/bin/" ./cmd/jailops ./cmd/jailopsd ./cmd/jailops-priv ./cmd/jailops-sync ./cmd/jailops-backupd
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -trimpath -o "$stage/bin/forge" ./scripts/forge
tar cf "$stage/files.tar" scripts/vm-clean.sh scripts/pkg-build.sh scripts/gen-share.sh scripts/ci-freebsd-e2e.sh scripts/ci-freebsd-pf.sh scripts/e2e-workflows.sh \
	internal/render/testdata internal/apps/testdata packaging test version.txt README.md LICENSE.md docs/design.md \
	-C "$stage" bin
ssh_vm 'rm -rf /tmp/jopsci && mkdir -p /tmp/jopsci && tar xf - -C /tmp/jopsci' < "$stage/files.tar"
release=$(ssh_vm 'freebsd-version -u' | sed 's/-p[0-9]*$//')
case $part in
e2e) envs="JAILOPS_E2E_PART=e2e JAILOPS_E2E_SHARD=$shard" ;;
pf) envs="JAILOPS_E2E_PART=pf JAILOPS_PF_SHARD=$shard" ;;
*) envs="JAILOPS_E2E_PART=$part" ;;
esac
echo "vm-run: $part $shard on port $port ($release)"
ssh_vm "sh /tmp/jopsci/scripts/vm-clean.sh; cd /tmp/jopsci && $envs JAILOPS_E2E_IMAGE_RELEASE=$release JAILOPS_BIN_DIR=/tmp/jopsci/bin FORGE_BIN=/tmp/jopsci/bin/forge sh scripts/ci-freebsd-e2e.sh"
