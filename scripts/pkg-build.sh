#!/bin/sh
# Builds the FreeBSD packages of JailOps from cross-built binaries. Runs on FreeBSD (it needs pkg(8)).
#
#   scripts/pkg-build.sh VERSION GOARCH BINDIR OUTDIR [MAJOR...]
#
# BINDIR holds the binaries jailops, jailops-sync, jailopsd, jailops-priv and jailops-backupd for GOARCH (amd64 or
# arm64), as `just build-freebsd` or goreleaser leaves them (a -freebsd-<goarch> suffix is accepted). The packages
# go to OUTDIR/<ABI>/latest/All for every FreeBSD MAJOR (default: 15; JailOps targets FreeBSD 15.1 and later).
set -eu

[ $# -ge 4 ] || { sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
version=$1; goarch=$2; bindir=$3; outdir=$4; shift 4
majors=${*:-"15"}
case $goarch in
amd64) arch=amd64 ;;
arm64) arch=aarch64 ;;
*) echo "unknown GOARCH $goarch" >&2; exit 2 ;;
esac
here=$(cd "$(dirname "$0")/.." && pwd)
pk=$here/packaging
work=$(mktemp -d /tmp/jailops-pkg.XXXXXX)
trap 'rm -rf "$work"' EXIT

bin() { # the binary named $1 in BINDIR, with or without the suffix
	for f in "$bindir/$1-freebsd-$goarch" "$bindir/$1"; do
		[ -f "$f" ] && { echo "$f"; return; }
	done
	echo "binary $1 not found in $bindir" >&2; exit 1
}

stage() { # stage NAME: install the files of one package under $work/NAME
	root=$work/$1; mkdir -p "$root"
	case $1 in
	jailops-client)
		install -d "$root/usr/local/bin"
		install -m 755 "$(bin jailops)" "$root/usr/local/bin/jailops"
		install -m 755 "$(bin jailops-sync)" "$root/usr/local/bin/jailops-sync" ;;
	jailops)
		install -d "$root/usr/local/sbin" "$root/usr/local/libexec" "$root/usr/local/etc/rc.d" "$root/usr/local/etc/jailops" "$root/usr/local/share/doc/jailops"
		install -m 755 "$(bin jailopsd)" "$root/usr/local/sbin/jailopsd"
		install -m 755 "$(bin jailops-priv)" "$root/usr/local/libexec/jailops-priv"
		install -m 555 "$pk/rc.d/jailopsd" "$pk/rc.d/jailops_priv" "$root/usr/local/etc/rc.d/"
		install -m 644 "$pk/jailopsd.yaml" "$root/usr/local/etc/jailops/jailopsd.yaml.sample"
		install -m 644 "$pk/priv.conf.sample" "$root/usr/local/etc/jailops/priv.conf.sample"
		install -m 644 "$here/README.md" "$here/LICENSE.md" "$here/docs/design.md" "$root/usr/local/share/doc/jailops/" ;;
	jailops-backupd)
		install -d "$root/usr/local/sbin" "$root/usr/local/etc/rc.d" "$root/usr/local/etc/jailops" "$root/usr/local/share/doc/jailops-backupd"
		install -m 755 "$(bin jailops-backupd)" "$root/usr/local/sbin/jailops-backupd"
		install -m 555 "$pk/rc.d/jailops_backupd" "$root/usr/local/etc/rc.d/"
		install -m 644 "$pk/backupd.yaml" "$root/usr/local/etc/jailops/backupd.yaml.sample"
		install -m 644 "$here/LICENSE.md" "$root/usr/local/share/doc/jailops-backupd/" ;;
	esac
}

# the files of a package as a plist, relative to the prefix /usr/local
plist() {
	root=$work/$1
	( cd "$root/usr/local" && find . -type f | sed 's#^\./##' | sort ) > "$work/$1.plist"
}

comment() {
	case $1 in
	jailops-client) echo "JailOps command line client and fleet tool" ;;
	jailops) echo "JailOps, ZFS-native jail management: daemon and privileged helper" ;;
	jailops-backupd) echo "JailOps backup node" ;;
	esac
}

desc() {
	case $1 in
	jailops-client) echo "The jailops command line client and jailops-sync, the declarative fleet tool." ;;
	jailops) echo "jailopsd, the JailOps daemon (REST API, web console), and jailops-priv, the small privileged helper it uses. After installing run: jailops-priv init -pool POOL" ;;
	jailops-backupd) echo "The JailOps backup node: stores encrypted, deduplicated backups and never holds the key. Needs PostgreSQL for its control plane." ;;
	esac
}

deps() {
	case $1 in
	jailops) printf '"deps": {"jailops-client": {"origin": "sysutils/jailops-client", "version": "%s"}},' "$version" ;;
	esac
}

scripts() {
	case $1 in
	jailops)
		cat <<'JSON'
"scripts": {
  "pre-install": "pw groupshow jailops >/dev/null 2>&1 || pw groupadd jailops; pw usershow jailops >/dev/null 2>&1 || pw useradd jailops -g jailops -d /var/db/jailops -s /usr/sbin/nologin -c 'JailOps daemon'; true",
  "post-install": "install -d -m 0750 -o jailops -g jailops /var/db/jailops; echo 'JailOps is installed. Next: jailops-priv init -pool POOL; sysrc jailops_priv_enable=YES jailopsd_enable=YES; service jailops_priv start; service jailopsd start'"
},
JSON
		;;
	jailops-backupd)
		cat <<'JSON'
"scripts": {
  "pre-install": "pw groupshow jailops-backup >/dev/null 2>&1 || pw groupadd jailops-backup; pw usershow jailops-backup >/dev/null 2>&1 || pw useradd jailops-backup -g jailops-backup -d /var/db/jailops-backup -s /usr/sbin/nologin -c 'JailOps backup node'; true",
  "post-install": "install -d -m 0750 -o jailops-backup -g jailops-backup /var/db/jailops-backup; true"
},
JSON
		;;
	esac
}

manifest() { # manifest NAME ABI
	cat <<JSON
{
  "name": "$1",
  "origin": "sysutils/$1",
  "version": "$version",
  "comment": "$(comment "$1")",
  "maintainer": "packages@jailops.net",
  "www": "https://jailops.net",
  "abi": "$2",
  "arch": "$2",
  "prefix": "/usr/local",
  "licenses": ["BSD2CLAUSE"],
  "licenselogic": "single",
  "categories": ["sysutils"],
  $(deps "$1")
  $(scripts "$1")
  "desc": "$(desc "$1")"
}
JSON
}

for name in jailops-client jailops jailops-backupd; do
	stage "$name"
	plist "$name"
done
for major in $majors; do
	abi="FreeBSD:$major:$arch"
	dest=$outdir/$abi/latest/All
	mkdir -p "$dest"
	for name in jailops-client jailops jailops-backupd; do
		manifest "$name" "$abi" > "$work/$name.$major.json"
		pkg create -M "$work/$name.$major.json" -p "$work/$name.plist" -r "$work/$name" -o "$dest"
	done
done
echo "packages in $outdir:"
find "$outdir" -name '*.pkg' | sort
