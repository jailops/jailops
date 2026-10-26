#!/usr/bin/env bash
# scripts/ci-race.sh [a|b|c|rest]   go test -race on one shard of the packages.
#
# CI runs the four shards in parallel instead of one long job. The split is by measured run time (about 100 s, 110 s,
# 80 s and 40 s of package time with the race detector on a 4-core machine). Shard "rest" is every package that none
# of a, b and c names, so a new package is tested without anyone editing this file. Without an argument every
# package runs, one after the other as `go test ./...` does.
#
#   scripts/ci-race.sh a            one shard
#   scripts/ci-race.sh --list a     the packages of a shard, one per line
#   scripts/ci-race.sh --check      every package is in exactly one shard (exit 1 otherwise)
set -euo pipefail
cd "$(dirname "$0")/.."

A=(./internal/server/... ./internal/daemon/... ./internal/service/... ./internal/cli/... ./internal/webui/... ./internal/backupui/...)
B=(./internal/repo/...)
C=(./internal/hostbackup/... ./internal/backupd/... ./internal/fleet/... ./internal/priv/... ./internal/node/... ./internal/fsagent/...)

pkgs() { go list "$@" | sort -u; }
named() { pkgs "${A[@]}" "${B[@]}" "${C[@]}"; }

shard_pkgs() {
	case "$1" in
	a) pkgs "${A[@]}" ;;
	b) pkgs "${B[@]}" ;;
	c) pkgs "${C[@]}" ;;
	rest) comm -23 <(pkgs ./...) <(named) ;;
	all) pkgs ./... ;;
	*) echo "shard must be a, b, c or rest (got '$1')" >&2; exit 2 ;;
	esac
}

case "${1:-}" in
--list) shard_pkgs "${2:?usage: ci-race.sh --list SHARD}"; exit 0 ;;
--check)
	total=$(pkgs ./... | wc -l)
	sum=0
	for s in a b c rest; do n=$(shard_pkgs "$s" | wc -l); echo "shard $s: $n packages"; sum=$((sum + n)); done
	dup=$( (shard_pkgs a; shard_pkgs b; shard_pkgs c; shard_pkgs rest) | sort | uniq -d | wc -l)
	echo "total $total, in shards $sum, in more than one shard: $dup"
	[ "$sum" = "$total" ] && [ "$dup" = 0 ] || { echo "the shards do not partition the packages" >&2; exit 1; }
	exit 0 ;;
esac

shard=${1:-${RACE_SHARD:-all}}
mapfile -t list < <(shard_pkgs "$shard")
[ "${#list[@]}" -gt 0 ] || { echo "shard $shard has no packages" >&2; exit 1; }
echo "race shard $shard: ${#list[@]} packages"
CGO_ENABLED=1 go test -race -count=1 "${list[@]}"
