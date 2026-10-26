#!/bin/sh
# The packet filter of JailOps, tested with real traffic on a real FreeBSD host. Needs a running jailopsd with an image
# freebsd@stable and the storage "default" (scripts/ci-freebsd-e2e.sh leaves exactly that), PF running with the
# anchor lines of the setup guide, and root.
#
#   sh scripts/ci-freebsd-pf.sh              the whole matrix
#   PF_FAST=1 sh scripts/ci-freebsd-pf.sh    skip the phases that wait for minutes
#   PF_KEEP=1 ...                            leave the projects and jails in place to look at them
#   FORGE_BIN=/path/to/forge-freebsd-<arch>  run phase 0e, the forged-frame probes (scripts/forge); skipped without it
#
# Every line is a probe with an expected outcome: "allow" (the connection or packet arrives) or "deny" (it does not).
# A probe that gives the wrong answer is a FAIL; the script goes on and exits non-zero at the end if any failed.
# It creates and removes its own projects (pfa, pfb), networks, jails, policies and address sets.
set -u

J=${JAILOPS:-/usr/local/bin/jailops}
fails=0
UC=0
passes=0
work=$(mktemp -d /tmp/jailops-pf.XXXXXX)
trap 'cleanup; exit 1' INT TERM

t0=$(date +%s)
say()  { printf '\n== %s  [+%ss]\n' "$*" "$(($(date +%s) - t0))"; }
pass() { passes=$((passes + 1)); printf 'PASS  %s\n' "$*"; }
bad()  { fails=$((fails + 1)); printf 'FAIL  %s\n' "$*"; }
die()  { printf 'ABORT %s\n' "$*" >&2; cleanup; exit 2; }

# --- running things in a jail (as root on the host) --------------------------------------------------------------
# jx PROJECT NAME CMD... : run CMD in the jail
jx() { p=$1; n=$2; shift 2; jexec "${p}_${n}" "$@"; }

# --- probes: each returns 0 when the traffic arrived -------------------------------------------------------------
# icmp_from SRC DST: SRC is "host" or "project:name"
run_from() { # run_from SRC CMD...
	src=$1; shift
	case $src in
	host) "$@" ;;
	*) jx "${src%%:*}" "${src##*:}" "$@" ;;
	esac
}
fam() { case $1 in *:*) echo 6 ;; *) echo 4 ;; esac; }
icmp()  { run_from "$1" ping -"$(fam "$2")" -c1 -t2 "$2" >/dev/null 2>&1; }
tcp()   { run_from "$1" nc -z -w2 "$2" "$3" >/dev/null 2>&1; }
# expect VERDICT DESCRIPTION CMD...
expect() {
	want=$1; desc=$2; shift 2
	if "$@"; then got=allow; else got=deny; fi
	# An allowed probe that fails once is a lost packet or a slow start more often than a rule: look again, twice, and say
	# so (the note keeps it visible). A denied probe is never repeated: one answer through a rule that must deny is a leak.
	if [ "$want" = allow ] && [ "$got" = deny ]; then
		for try in 2 3; do
			sleep 1
			if "$@"; then got=allow; printf 'NOTE  %s: allowed on attempt %s\n' "$desc" "$try"; break; fi
		done
	fi
	if [ "$got" = "$want" ]; then pass "$desc ($want)"; else bad "$desc: expected $want, got $got"; fi
}

# --- addresses ---------------------------------------------------------------------------------------------------
addr4() { $J config get "$2" nic.eth0.ipv4 --project "$1" </dev/null 2>/dev/null | tail -1; }
addr6() { $J config get "$2" nic.eth0.ipv6 --project "$1" </dev/null 2>/dev/null | tail -1; }

# --- setup and cleanup -------------------------------------------------------------------------------------------
cleanup() {
	say "cleanup"
	[ -n "${PF_KEEP:-}" ] || remove_all
	rm -rf "$work"
	printf '\n%d passed, %d failed\n' "$passes" "$fails"
}
retry() { # retry TRIES COMMAND...: stop at the first success (a stopping jail needs a moment before its dataset can go)
	n=$1; shift
	while [ "$n" -gt 0 ]; do
		"$@" >/dev/null 2>&1 && return 0
		[ $? -eq 4 ] && return 0 # exit code 4 is "not found": there is nothing to remove, waiting would only burn time
		n=$((n - 1)); sleep 3
	done
	return 1
}
remove_all() {
	for f in "${TMPDIR:-/tmp}"/jailops-pf.*/h4.pid "${TMPDIR:-/tmp}"/jailops-pf.*/h6.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null; done
	pkill -x nc 2>/dev/null
	for pn in pfa:a pfa:b pfa:c pfa:d pfb:o; do
		$J stop "${pn#*:}" --project "${pn%%:*}" </dev/null >/dev/null 2>&1
	done
	for pn in pfa:a pfa:b pfa:c pfa:d pfb:o; do
		retry 6 $J delete -y "${pn#*:}" --project "${pn%%:*}" </dev/null
	done
	for p in pfa pfb; do
		$J project set $p --firewall "" </dev/null >/dev/null 2>&1
		for x in $($J firewall policy list --project $p --no-headers -o name </dev/null 2>/dev/null); do
			retry 3 $J firewall policy delete -y "$x" --project $p </dev/null
		done
		for x in $($J firewall address-set list --project $p --no-headers -o name </dev/null 2>/dev/null); do
			retry 3 $J firewall address-set delete -y "$x" --project $p </dev/null
		done
		retry 3 $J project delete -y $p </dev/null
	done
	for n in pfa-net pfa-net2 pfb-net; do retry 3 $J network delete -y $n </dev/null; done
}

policy() { # policy PROJECT NAME YAML-BODY: create or replace
	p=$1; n=$2; body=$3
	printf '%s\n' "$body" > "$work/$n.yaml"
	if $J firewall policy show "$n" --project "$p" </dev/null >/dev/null 2>&1; then
		cat > "$work/edit.sh" <<EOF
#!/bin/sh
cat "$work/$n.yaml" > "\$1"
EOF
		chmod +x "$work/edit.sh"
		EDITOR="$work/edit.sh" $J firewall policy edit "$n" --project "$p" </dev/null >/dev/null 2>&1 || die "policy edit $n"
	else
		$J firewall policy create "$n" -f "$work/$n.yaml" --project "$p" </dev/null >/dev/null 2>&1 || die "policy create $n"
	fi
}
# The reconciler loads anchors on its own schedule. settle waits until the anchors have changed after the action that
# came before it and have then stopped changing, and says how long that took (the time is a measurement of the product,
# not a pause that has to be long enough).
snapshot() { for a in jailops/pfa/a jailops/pfa/b jailops/pfa/c jailops/pfb/o jailops/pfa/d jailops/net.pfa-net; do pfctl -a "$a" -sr 2>/dev/null; pfctl -a "$a" -sn 2>/dev/null; done | cksum; }
PRE=$(snapshot)
settle() {
	t=0; cur=$(snapshot)
	while [ "$cur" = "$PRE" ] && [ $t -lt "${SETTLE_WAIT:-60}" ]; do sleep 1; t=$((t + 1)); cur=$(snapshot); done
	same=0
	while [ $same -lt 3 ] && [ $t -lt 90 ]; do
		sleep 1; t=$((t + 1)); n=$(snapshot)
		if [ "$n" = "$cur" ]; then same=$((same + 1)); else same=0; cur=$n; fi
	done
	[ "$cur" = "$PRE" ] && printf 'note: the anchors did not change after the action (%ss)\n' "$t" || printf 'note: anchors converged after %ss\n' "$t"
	PRE=$cur
}


say "the rendered anchors in the repository parse with the real pfctl"
if [ -d internal/render/testdata ]; then
	for f in internal/render/testdata/pf_*.golden; do
		if out=$(pfctl -n -a jailops/x/y -f "$f" 2>&1 | grep -v 'already defined'); [ -z "$out" ]; then pass "$f"; else bad "$f: $out"; fi
	done
fi

say "setup"
[ "$(id -u)" -eq 0 ] || die "run as root"
remove_all # leftovers of an earlier run
sysctl net.inet.ip.forwarding=1 net.inet6.ip6.forwarding=1 >/dev/null
$J network create pfa-net --bridge jo21 --ipv4 10.91.0.1/24 --ipv6 fd91:1::1/64 </dev/null >/dev/null || die "network pfa-net"
$J network create pfb-net --bridge jo22 --ipv4 10.92.0.1/24 --ipv6 fd92:1::1/64 </dev/null >/dev/null || die "network pfb-net"
$J network create pfa-net2 --bridge jo23 --ipv4 10.93.0.1/24 --ipv6 fd93:1::1/64 </dev/null >/dev/null || die "network pfa-net2"
$J project create pfa </dev/null >/dev/null || die "project pfa"
$J project create pfb </dev/null >/dev/null || die "project pfb"
$J project set pfa --network pfa-net --network pfa-net2 --storage default </dev/null >/dev/null || die "grant pfa"
$J project set pfb --network pfb-net --storage default </dev/null >/dev/null || die "grant pfb"
for n in a b; do $J create $n --project pfa --image freebsd@stable --network pfa-net --storage default </dev/null >/dev/null || die "create $n"; done
$J create c --project pfa --image freebsd@stable --network pfa-net --storage default --tag web </dev/null >/dev/null || die "create c"
$J create d --project pfa --image freebsd@stable --network pfa-net2 --storage default </dev/null >/dev/null || die "create d"
$J create o --project pfb --image freebsd@stable --network pfb-net --storage default </dev/null >/dev/null || die "create o"
for pn in pfa:a pfa:b pfa:c pfa:d pfb:o; do $J start "${pn#*:}" --project "${pn%%:*}" </dev/null >/dev/null || die "start $pn"; done
sleep 8

A4=$(addr4 pfa a); A6=$(addr6 pfa a); B4=$(addr4 pfa b); B6=$(addr6 pfa b)
C4=$(addr4 pfa c); C6=$(addr6 pfa c); D4=$(addr4 pfa d); D6=$(addr6 pfa d); O4=$(addr4 pfb o); O6=$(addr6 pfb o)
H4=10.91.0.1; H6=fd91:1::1; HD4=10.93.0.1; HD6=fd93:1::1 # the host on the network of project pfa
for v in "$A4" "$A6" "$B4" "$B6" "$C4" "$C6" "$D4" "$D6" "$O4" "$O6"; do [ -n "$v" ] || die "a jail has no address (v4/v6 missing)"; done
printf 'a=%s %s  b=%s %s  c=%s %s  o=%s %s\n' "$A4" "$A6" "$B4" "$B6" "$C4" "$C6" "$O4" "$O6"

# servers in jail a: tcp 8080, 9090, 2222 and a udp logger on 5353, on both families
for fam in 4 6; do
	if [ $fam = 4 ]; then ad=$A4; else ad=$A6; fi
	for port in 8080 9090 2222; do
		jx pfa a daemon -f nc -"$fam" -k -l "$ad" "$port" || die "nc server $port"
	done
done
udp_server() { # udp_server: (re)start loggers writing into the jail's /var/tmp/udp.log, mirrored to the host
	for ad in "$A4" "$A6"; do
		jx pfa a daemon -f sh -c "while :; do nc -u -l -w3 $ad 5353 >> /var/tmp/udp.log 2>/dev/null; done"
	done
}
udp_server
# udp probes read the jail's file
udp() {
	UC=$((UC + 1)); tok="t$$-$UC"
	run_from "$1" sh -c "echo $tok | nc -u -w1 $2 $3" >/dev/null 2>&1
	sleep 1
	jx pfa a grep -q "$tok" /var/tmp/udp.log 2>/dev/null
}

# the matrix of one source against jail a, both families
matrix() { # matrix SRC ICMP T8080 T9090 T2222 UDP5353   (each allow|deny)
	src=$1
	for ver in 4 6; do
		if [ $ver = 4 ]; then t=$A4; else t=$A6; fi
		expect "$2" "$src -> a v$ver icmp" icmp "$src" "$t"
		expect "$3" "$src -> a v$ver tcp 8080" tcp "$src" "$t" 8080
		expect "$4" "$src -> a v$ver tcp 9090" tcp "$src" "$t" 9090
		expect "$5" "$src -> a v$ver tcp 2222" tcp "$src" "$t" 2222
		expect "$6" "$src -> a v$ver udp 5353" udp "$src" "$t" 5353
	done
}

say "phase 0: no policy anywhere. Open inside a project, closed between projects"
PRE=$(snapshot)
matrix host allow allow allow allow allow
matrix pfa:b allow allow allow allow allow
matrix pfa:c allow allow allow allow allow
matrix pfb:o deny deny deny deny deny
for ver in 4 6; do
	if [ $ver = 4 ]; then b=$B4; o=$O4; else b=$B6; o=$O6; fi
	expect allow "a -> b v$ver icmp" icmp pfa:a "$b"
	expect deny "a -> o (another project) v$ver icmp" icmp pfa:a "$o"
	expect deny "a -> o (another project) v$ver tcp 22" tcp pfa:a "$o" 22
done

say "phase 0b: a jail cannot send with another jail's address (nothing else is filtering yet)"
PRE=$(snapshot)
# UDP listeners that log what arrives: on the host, in jail c (same bridge) and in jail d (another network of the same
# project, reached through the host)
for fam in 4 6; do
	if [ $fam = 4 ]; then h=$H4; c=$C4; d=$D4; else h=$H6; c=$C6; d=$D6; fi
	: > "$work/h$fam.log"
	daemon -f -p "$work/h$fam.pid" sh -c "while :; do nc -u -l -w2 $h 5454 >> $work/h$fam.log 2>/dev/null; done"
	jx pfa c daemon -f sh -c "while :; do nc -u -l -w2 $c 5454 >> /var/tmp/u.log 2>/dev/null; done"
	jx pfa d daemon -f sh -c "while :; do nc -u -l -w2 $d 5454 >> /var/tmp/u.log 2>/dev/null; done"
done
sleep 2
BIF=$(jx pfa a ifconfig -l | tr ' ' '\n' | grep -v lo0 | head -1)
send_from_a() { # send_from_a SOURCE DEST TOKEN
	jx pfa a sh -c "echo $3 | nc -u -w1 -s $1 $2 5454" >/dev/null 2>&1
}
arrived() { # arrived WHERE TOKEN: where is host, c or d
	sleep 1
	case $1 in
	host*) grep -q "$2" "$work/h${1#host}.log" ;;
	c) jx pfa c grep -q "$2" /var/tmp/u.log ;;
	d) jx pfa d grep -q "$2" /var/tmp/u.log ;;
	esac
}
for ver in 4 6; do
	if [ $ver = 4 ]; then a=$A4; b=$B4; fl="inet $B4/32 alias"; fr="inet $B4 -alias"; else a=$A6; b=$B6; fl="inet6 $B6 prefixlen 128 alias"; fr="inet6 $B6 -alias"; fi
	jx pfa a ifconfig "$BIF" $fl >/dev/null 2>&1
	for target in host c d; do
		case $target in
		host) if [ $ver = 4 ]; then dst=$H4; else dst=$H6; fi; w="host$ver" ;;
		c) if [ $ver = 4 ]; then dst=$C4; else dst=$C6; fi; w=c ;;
		d) if [ $ver = 4 ]; then dst=$D4; else dst=$D6; fi; w=d ;;
		esac
		UC=$((UC + 1)); t1="own-$ver-$target-$UC"
		send_from_a "$a" "$dst" "$t1"
		expect allow "a sends from its own address to $target v$ver (control)" arrived "$w" "$t1"
		UC=$((UC + 1)); t2="spoof-$ver-$target-$UC"
		send_from_a "$b" "$dst" "$t2"
		expect deny "a sends from b's address to $target v$ver (spoofed)" arrived "$w" "$t2"
	done
	jx pfa a ifconfig "$BIF" $fr >/dev/null 2>&1
done

say "phase 0c: the anchors carry the ICMPv6 that must never be blocked (path MTU discovery) and the neighbour discovery rules"
PRE=$(snapshot)
# (Not automated: the packet-too-big itself. The host's pf refragments what it forwards instead of answering with the error, so
# an ordinary echo does not produce one; it was checked with a forged error, see docs/networking-ipv6.md.)
anchor_has() { pfctl -a jailops/pfa/a -sr 2>/dev/null | grep -q "$1"; }
expect allow "a's anchor passes packet-too-big in both directions" anchor_has "icmp6-type toobig"
expect allow "a's anchor blocks router advertisements from the jail" anchor_has "block drop in quick on .* icmp6-type routeradv"
expect deny "a's anchor does not allow all of fe80::/10 as source" anchor_has "fe80::/10"
expect deny "a's anchor lets :: send anything but a neighbour solicitation to a solicited-node group" anchor_has "from :: to ff02::/16"
jx pfa a ping -6 -c1 -t2 "$B6" >/dev/null 2>&1

say "phase 0d: a jail can use only the addresses and the MAC address it was given"
PRE=$(snapshot)
IFA=$(jx pfa a ifconfig -l | tr ' ' '\n' | grep '^jo' | head -1)
IFB=$(jx pfa b ifconfig -l | tr ' ' '\n' | grep '^jo' | head -1)
MACA=$(jx pfa a ifconfig "$IFA" | awk '/ether/ {print $2}'); MACB=$(jx pfa b ifconfig "$IFB" | awk '/ether/ {print $2}')
EXTRA4=10.91.0.99; EXTRA6=fd91:1::99
# the jails hold /32 and /128 addresses: nothing is on-link, every flow passes the host (and its PF)
expect allow "a holds its IPv4 address as /32" sh -c "jexec pfa_a ifconfig $IFA | grep -q 'inet $A4 netmask 0xffffffff'"
expect allow "a holds its IPv6 address as /128" sh -c "jexec pfa_a ifconfig $IFA | grep -q 'inet6 $A6 prefixlen 128'"
expect allow "the host has a permanent ARP entry for a on the gateway interface" sh -c "arp -an | grep -q '($A4) at $MACA on .* permanent'"
expect allow "the host has a permanent neighbor entry for a" sh -c "ndp -an | grep -q '^$A6 .*permanent'"
jx pfa a ifconfig "$IFA" inet "$EXTRA4/32" alias >/dev/null 2>&1
jx pfa a ifconfig "$IFA" inet6 "$EXTRA6" prefixlen 128 alias >/dev/null 2>&1
sleep 3
expect deny "host -> an IPv4 address a gave itself" icmp host "$EXTRA4"
expect deny "b -> an IPv4 address a gave itself" icmp pfa:b "$EXTRA4"
expect deny "host -> an IPv6 address a gave itself" icmp host "$EXTRA6"
expect deny "b -> an IPv6 address a gave itself" icmp pfa:b "$EXTRA6"
expect deny "a sends from its extra IPv4 address" sh -c "jexec pfa_a ping -S $EXTRA4 -c1 -t2 $H4"
expect deny "a sends from its extra IPv6 address" sh -c "jexec pfa_a ping -6 -S $EXTRA6 -c1 -t2 $H6"
jx pfa a ifconfig "$IFA" inet "$EXTRA4" -alias >/dev/null 2>&1
jx pfa a ifconfig "$IFA" inet6 "$EXTRA6" -alias >/dev/null 2>&1
# a takes b's MAC address on its own interface: b must still be reachable, a must lose its own connectivity
jx pfa a ifconfig "$IFA" ether "$MACB" >/dev/null 2>&1
jx pfa a ping -c1 -t2 "$H4" >/dev/null 2>&1
expect allow "host -> b over IPv4 after a took b's MAC address" icmp host "$B4"
expect allow "host -> b over IPv6 after a took b's MAC address" icmp host "$B6"
expect deny "a -> host with b's MAC address (nothing is delivered to it)" icmp pfa:a "$H4"
jx pfa a ifconfig "$IFA" ether "$MACA" >/dev/null 2>&1
expect allow "a -> host with its own MAC address again" icmp pfa:a "$H4"
# no router advertisement is ever passed: the anchor drops it on the jail's port and on the gateway interface
anchor_has2() { pfctl -a jailops/pfa/a -sr 2>/dev/null | grep -q "$1"; }
expect allow "a's anchor drops router advertisements on the gateway interface" anchor_has2 "block drop out quick on jo21gb inet6 proto ipv6-icmp all icmp6-type routeradv"
expect allow "a's anchor drops every neighbour advertisement of the jail" anchor_has2 "block drop in quick on .* inet6 proto ipv6-icmp all icmp6-type neighbradv"
expect deny "a's anchor lets the jail send a neighbour advertisement" anchor_has2 "pass in quick .*neighbradv"

if [ -n "${FORGE_BIN:-}" ] && [ -x "$FORGE_BIN" ]; then
say "phase 0e: forged frames from inside a jail (scripts/forge) change nothing real"
PRE=$(snapshot)
GW=jo21gb
EVID="${EVID_DIR:-/var/tmp/jailops-forge}/forgery-$(hostname -s 2>/dev/null || hostname)-$(date +%Y%m%d).txt"
mkdir -p "$(dirname "$EVID")" 2>/dev/null
: > "$EVID"
log() { printf '%s\n' "$*" | tee -a "$EVID"; }
unhide_bpf() { # unhide_bpf PROJECT NAME
	r=$(jls -j "${1}_${2}" devfs_ruleset 2>/dev/null) || return 1
	root=$(jls -j "${1}_${2}" path 2>/dev/null) || return 1
	devfs -m "$root/dev" rule -s "$r" add path 'bpf*' unhide >/dev/null 2>&1
	devfs -m "$root/dev" rule -s "$r" applyset >/dev/null 2>&1
	cp "$FORGE_BIN" "$root/tmp/forge" && chmod +x "$root/tmp/forge"
}
unhide_bpf pfa a || die "forge: could not reach into pfa_a"
GWMAC=$(ifconfig "$GW" | awk '/ether/ {print $2}')
arpcache_b() { arp -an | awk -v a="($B4)" '$2==a {print $4}'; }
ndcache_b() { ndp -an | awk -v a="$B6" '$1==a {print $2}'; }
jexec_b_gw_mac() { jexec pfa_b ndp -an 2>/dev/null | awk '/fe80::1%/ {print $2; exit}'; }
anchorA_neighbradv_pkts() { pfctl -a "jailops/pfa/a" -sr -v 2>/dev/null | grep -A1 "icmp6-type neighbradv$" | awk '/Packets:/ {s+=$5} END {print s+0}'; }
same() { [ "$1" = "$2" ]; } # same OLD NEW
unchanged_arp_b()    { same "$1" "$(arpcache_b)"; }
unchanged_nd_b()     { same "$1" "$(ndcache_b)"; }
unchanged_bgw_cache() { same "$1" "$(jexec_b_gw_mac)"; }
unchanged_a_v6()     { same "$1" "$(jexec pfa_a ifconfig "$IFA" | awk '/inet6/ {print $2}' | sort | tr '\n' ' ')"; }
unchanged_a_v4()     { same "$1" "$(jexec pfa_a ifconfig "$IFA" | awk '/inet /{print $2}')"; }
b_reaches_host_v6()  { jexec pfa_b ping -6 -c1 -t2 "$H6" >/dev/null 2>&1; }
neighbradv_blocked() { n=$(anchorA_neighbradv_pkts); [ "${n:-0}" -gt 0 ]; }

log "== phase 0e forgery evidence: $(date -u '+%Y-%m-%dT%H:%M:%SZ') host $(hostname) =="
log "jails: a=$A4/$A6 mac=$MACA  b=$B4/$B6  gateway=$GW/$GWMAC"

# -- a continuous ping of b from the host runs for the whole phase: the victim must keep flowing throughout.
: > "$work/bg.log"
( n=0; lost=0; while [ $n -lt 60 ]; do if ping -c1 -t1 "$B4" >/dev/null 2>&1; then :; else lost=$((lost + 1)); fi; n=$((n + 1)); sleep 0.5; done; echo "$lost" > "$work/bg.log" ) &
BGPID=$!

echo "-- forged ARP reply: a claims the gateway, to b" | tee -a "$EVID"
before_bgw=$(jexec_b_gw_mac)
before=$(arpcache_b)
jx pfa a /tmp/forge arp -if "$IFA" -eth-dst "$MACB" -eth-src "$MACA" -op reply -sha "$MACA" -spa "$H4" -tha "$MACB" -tpa "$A4" 2>&1 | tee -a "$EVID"
sleep 1
expect allow "ARP reply forgery (a->b, claims the gateway): b's cache for the gateway is unaffected" unchanged_bgw_cache "$before_bgw"

echo "-- forged ARP reply: a claims b, to the host" | tee -a "$EVID"
jx pfa a /tmp/forge arp -if "$IFA" -eth-dst "$GWMAC" -eth-src "$MACA" -op reply -sha "$MACA" -spa "$B4" -tha "$GWMAC" -tpa "$H4" 2>&1 | tee -a "$EVID"
sleep 1
expect allow "ARP reply forgery (a->host, claims b): the host's cache for b is unchanged" unchanged_arp_b "$before"

echo "-- forged ARP request broadcast: a claims b's address" | tee -a "$EVID"
jx pfa a /tmp/forge arp -if "$IFA" -eth-dst ff:ff:ff:ff:ff:ff -eth-src "$MACA" -op request -sha "$MACA" -spa "$B4" -tpa "$H4" 2>&1 | tee -a "$EVID"
sleep 1
expect allow "ARP request forgery (a broadcasts as b): the host's cache for b is still unchanged" unchanged_arp_b "$before"

echo "-- forged unicast NA: a claims the gateway's address, to b" | tee -a "$EVID"
beforeb=$(jexec_b_gw_mac)
jx pfa a /tmp/forge nd -type na -src "$A6" -dst "$B6" -target fe80::1 -if "$IFA" -lladdr "$MACA" 2>&1 | tee -a "$EVID"
sleep 1
expect allow "unicast NA forgery (a->b, claims the gateway): b's cache for the gateway is unchanged" unchanged_bgw_cache "$beforeb"

echo "-- forged multicast NA: a claims b's address, to ff02::1" | tee -a "$EVID"
beforeh=$(ndcache_b)
jx pfa a /tmp/forge nd -type na -src "$A6" -dst ff02::1 -target "$B6" -if "$IFA" -lladdr "$MACA" 2>&1 | tee -a "$EVID"
sleep 1
expect allow "multicast NA forgery (a claims b, to all nodes): the host's cache for b is unchanged" unchanged_nd_b "$beforeh"
expect allow "the anchor's neighbradv-block counter on a saw the attempt" neighbradv_blocked

echo "-- forged NS (unicast, not DAD): a solicits for b's address; b must not be disturbed" | tee -a "$EVID"
jx pfa a /tmp/forge nd -type ns -src "$A6" -dst "$B6" -target "$B6" -if "$IFA" -lladdr "$MACA" 2>&1 | tee -a "$EVID"
sleep 1
expect allow "NS forgery: b -> host v6 still works" b_reaches_host_v6

echo "-- forged RA with an autonomous prefix, spoofing the gateway's link-local source" | tee -a "$EVID"
before_addrs=$(jexec pfa_a ifconfig "$IFA" | awk '/inet6/ {print $2}' | sort | tr '\n' ' ')
ra_out=$(jx pfa a /tmp/forge nd -type ra -src fe80::1 -dst ff02::1 -if "$IFA" 2>&1)
echo "$ra_out" | tee -a "$EVID"
if echo "$ra_out" | grep -q "can't assign requested address"; then
	log "note: the RA was never sent: a raw socket inside the jail cannot bind to fe80::1 (not its own address)."
	log "      PRIV_NETINET_BINDANY is unavailable inside a jail (EADDRNOTAVAIL), independently of pf."
else
	log "the RA was sent (bind succeeded); the anchor's drop is the only defence for it"
fi
sleep 2
expect allow "RA forgery: a's address set is unchanged (no SLAAC), sent or not" unchanged_a_v6 "$before_addrs"

echo "-- forged packet-too-big (sanity: the tool and the anchor do not crash on it; same bind restriction may apply)" | tee -a "$EVID"
jx pfa a /tmp/forge nd -type ptb -src "$H6" -dst "$A6" -target "$B6" -if "$IFA" 2>&1 | tee -a "$EVID"

echo "-- DHCP discover (real dhclient, no forged frame needed): a's address must not change" | tee -a "$EVID"
before4=$(jexec pfa_a ifconfig "$IFA" | awk '/inet /{print $2}')
jx pfa a timeout 5 dhclient -n "$IFA" 2>&1 | tee -a "$EVID"
expect allow "DHCP discover: a's address is unchanged" unchanged_a_v4 "$before4"

echo "-- spoofed-source IPv4 in the same /24 (also covered in phase 0b/0d; repeated here with evidence)" | tee -a "$EVID"
expect deny "a sends from b's address to the host (spoofed source)" sh -c "jexec pfa_a ping -S $B4 -c1 -t2 $H4"

wait "$BGPID" 2>/dev/null
lost=$(cat "$work/bg.log" 2>/dev/null)
log "background ping of b from the host during the attack: lost=${lost:-unknown} of 60"
expect allow "the victim (b) kept answering the host through the whole attack (at most 2 of 60 lost)" sh -c "[ '${lost:-99}' -le 2 ]"

if [ -z "${PF_FAST:-}" ]; then
	echo "-- uplink capture: none of the forged frames or packets leave the host (jails are not bridged to the uplink)"
	UPLINK=$(ifconfig -l | tr ' ' '\n' | grep -vE '^(lo0|pflog0|jo|epair)' | head -1)
	if [ -n "$UPLINK" ]; then
		: > "$work/uplink.pcap.txt"
		timeout 4 tcpdump -ni "$UPLINK" -c 5 "host $B4 or host $B6" > "$work/uplink.pcap.txt" 2>&1 &
		TD=$!
		sleep 1
		jx pfa a /tmp/forge arp -if "$IFA" -eth-dst ff:ff:ff:ff:ff:ff -eth-src "$MACA" -op request -sha "$MACA" -spa "$B4" -tpa "$H4" >/dev/null 2>&1
		wait "$TD" 2>/dev/null
		cat "$work/uplink.pcap.txt" >> "$EVID"
		expect allow "no forged packet reached the uplink ($UPLINK)" sh -c "! grep -qE 'ARP|$B4|$B6' '$work/uplink.pcap.txt'"
	fi
fi
log "evidence saved to $EVID"
jx pfa a ifconfig "$IFA" down 2>/dev/null; jx pfa a ifconfig "$IFA" up 2>/dev/null
else
	say "phase 0e: skipped (set FORGE_BIN=/path/to/forge-freebsd-<arch> to run the forged-frame probes)"
fi

say "phase 1: the project's mandatory policy denies everything in (egress allowed)"
PRE=$(snapshot)
policy pfa base 'default_in: deny
default_out: allow'
$J project set pfa --firewall base </dev/null >/dev/null || die "project firewall"
settle 10
matrix host deny deny deny deny deny
matrix pfa:b deny deny deny deny deny
matrix pfa:c deny deny deny deny deny
matrix pfb:o deny deny deny deny deny
expect allow "a -> host address v4 icmp (egress allowed)" icmp pfa:a "$H4"
expect allow "a -> host address v6 icmp (egress allowed)" icmp pfa:a "$H6"
expect deny "a -> b v4 icmp (b denies in too)" icmp pfa:a "$B4"
expect deny "a -> b v6 icmp (b denies in too)" icmp pfa:a "$B6"

say "phase 2: a's own policy opens exactly what it names"
PRE=$(snapshot)
policy pfa friends-dummy 'default_in: deny
default_out: allow' # a policy to be replaced below, so that "edit" is exercised
$J firewall address-set create friends --address "$B4" --address "$B6" --project pfa </dev/null >/dev/null || die "address set"
$J firewall address-set create office --address "$H4" --address "$H6" --project pfa </dev/null >/dev/null || die "address set office"
$J firewall address-set create common --address "$O4" --address "$O6" --project pfa </dev/null >/dev/null || die "address set common"
policy pfa web "default_in: deny
default_out: allow
rules:
  - {name: from-friends, action: allow, direction: in, proto: tcp, from: ['@friends'], ports: ['8080']}
  - {name: from-web-tag, action: allow, direction: in, proto: tcp, from: ['tag:web'], ports: ['9090']}
  - {name: host-ping4, action: allow, direction: in, proto: icmp, from: ['$H4']}
  - {name: host-ping6, action: allow, direction: in, proto: icmp6, from: ['$H6']}
  - {name: other-project-udp, action: allow, direction: in, proto: udp, from: ['@common'], ports: ['5353']}"
policy pfb toa "default_in: allow
default_out: allow
rules:
  - {name: to-a-udp, action: allow, direction: out, proto: udp, to: ['$A4', '$A6'], ports: ['5353']}"
$J config set a firewall.policy web --project pfa </dev/null >/dev/null || die "attach web"
$J config set o firewall.policy toa --project pfb </dev/null >/dev/null || die "attach toa"
settle 10
matrix host allow deny deny deny deny
matrix pfa:b deny allow deny deny deny
matrix pfa:c deny deny allow deny deny
matrix pfb:o deny deny deny deny allow

say "phase 3: a policy cannot weaken the project's default (the floor)"
PRE=$(snapshot)
policy pfa open 'default_in: allow
default_out: allow'
$J config set b firewall.policy open --project pfa </dev/null >/dev/null || die "attach open"
# this action is meant to change nothing (the project floor wins): anchors converge in about 4s elsewhere, so 15s is plenty
SETTLE_WAIT=15 settle 10
# b has its own policy 'open' (default allow) but the project says default deny in: still denied
for ver in 4 6; do
	if [ $ver = 4 ]; then b=$B4; else b=$B6; fi
	expect deny "host -> b v$ver icmp (own default allow, project default deny)" icmp host "$b"
	expect deny "c -> b v$ver icmp" icmp pfa:c "$b"
done

say "phase 4: egress filtering. c may only open tcp 9090 to a"
PRE=$(snapshot)
policy pfa lock "default_in: deny
default_out: deny
rules:
  - {name: to-a-9090, action: allow, direction: out, proto: tcp, to: ['$A4', '$A6'], ports: ['9090']}"
$J config set c firewall.policy lock --project pfa </dev/null >/dev/null || die "attach lock"
settle 10
for ver in 4 6; do
	if [ $ver = 4 ]; then a=$A4; b=$B4; h=$H4; else a=$A6; b=$B6; h=$H6; fi
	expect allow "c -> a v$ver tcp 9090 (allowed by c's egress and a's ingress)" tcp pfa:c "$a" 9090
	expect deny "c -> a v$ver tcp 8080 (c's egress denies)" tcp pfa:c "$a" 8080
	expect deny "c -> b v$ver icmp (c's egress denies)" icmp pfa:c "$b"
	expect deny "c -> host v$ver icmp (c's egress denies)" icmp pfa:c "$h"
done

say "phase 6: pf anchors are real and well formed"
PRE=$(snapshot)
if pfctl -a jailops/pfa/a -sr 2>&1 | grep -q 'foreign'; then pass "a's anchor has the isolation rules"; else bad "a's anchor lacks the isolation rules"; fi
pfctl -a jailops/pfa/a -sr >/dev/null 2>&1 && pass "a's anchor loads and lists" || bad "a's anchor does not list"
for anchor in jailops/pfa/a jailops/pfa/b jailops/pfa/c jailops/pfb/o; do
	n=$(pfctl -a "$anchor" -sr 2>/dev/null | wc -l)
	[ "$n" -gt 3 ] && pass "$anchor has rules ($n)" || bad "$anchor is empty"
done

say "phase 7: a rule change does not interrupt what stays allowed"
PRE=$(snapshot)
ping -c 40 -i 0.25 -W 1000 "$A4" > "$work/ping.out" 2>&1 &
pp=$!
for i in 1 2 3 4; do
	policy pfa web "default_in: deny
default_out: allow
rules:
  - {name: from-friends, action: allow, direction: in, proto: tcp, from: ['@friends'], ports: ['8080']}
  - {name: from-web-tag, action: allow, direction: in, proto: tcp, from: ['tag:web'], ports: ['9090']}
  - {name: host-ping4, action: allow, direction: in, proto: icmp, from: ['$H4']}
  - {name: host-ping6, action: allow, direction: in, proto: icmp6, from: ['$H6']}
  - {name: other-project-udp, action: allow, direction: in, proto: udp, from: ['@common'], ports: ['5353']}
  - {name: extra-$i, action: allow, direction: in, proto: tcp, from: ['@friends'], ports: ['70$i$i']}"
	sleep 2
done
wait $pp
loss=$(grep -o '[0-9.]*% packet loss' "$work/ping.out" | head -1)
case $loss in
	"0.0% packet loss") pass "no packet lost while the policy was replaced four times" ;;
	*) bad "packets were lost while the policy was replaced: $loss" ;;
esac
# a flow that is still running when its rule is removed must end: traffic that keeps a state alive would otherwise
# go on for ever
ping -c 90 -i 0.5 "$A4" 2>&1 | while read l; do echo "$(date +%s) $l"; done > "$work/live.out" &
lp=$!
sleep 3
PRE=$(snapshot)
policy pfa web "default_in: deny
default_out: allow
rules:
  - {name: from-friends, action: allow, direction: in, proto: tcp, from: ['@friends'], ports: ['8080']}"
settle 10
T=$(date +%s)
sleep 8
late=$(awk -v t="$T" '$1 > t + 3 && /bytes from/' "$work/live.out" | wc -l | tr -d ' ')
[ "$late" = 0 ] && pass "a running ping stopped when its rule was removed" || bad "a running ping went on after its rule was removed ($late replies)"
kill $lp 2>/dev/null; wait $lp 2>/dev/null
expect deny "host -> a icmp after the rule was removed" icmp host "$A4"
expect allow "b -> a tcp 8080 still allowed" tcp pfa:b "$A4" 8080
expect deny "c -> a tcp 9090 after the rule was removed" tcp pfa:c "$A4" 9090

say "phase 8: a restarted jail is never open"
PRE=$(snapshot)
$J restart a --project pfa </dev/null >/dev/null 2>&1 || bad "restart a"
t=0
until jx pfa a true >/dev/null 2>&1; do t=$((t + 1)); [ $t -gt 60 ] && break; sleep 1; done
A4=$(addr4 pfa a); A6=$(addr6 pfa a)
for ad in "$A4" "$A6"; do
	for port in 2222 8080; do jx pfa a daemon -f nc -k -l "$ad" "$port"; done
done
opened=0; reached=0; t=0
while [ $t -lt 20 ]; do
	tcp pfa:b "$A4" 2222 && opened=1
	tcp pfa:b "$A4" 8080 && reached=1
	t=$((t + 1)); sleep 0.5
done
[ $opened -eq 0 ] && pass "the denied port stayed closed after a restart" || bad "the denied port answered after a restart"
[ $reached -eq 1 ] && pass "the allowed port works after a restart" || bad "the allowed port does not answer after a restart"

if [ -z "${PF_FAST:-}" ]; then
	say "phase 9: the reconciler restores a flushed anchor"
	pfctl -a jailops/pfa/a -F rules >/dev/null 2>&1
	t=0; back=0
	while [ $t -lt 90 ]; do
		if pfctl -a jailops/pfa/a -sr 2>/dev/null | grep -q foreign; then back=1; break; fi
		t=$((t + 3)); sleep 3
	done
	[ $back -eq 1 ] && pass "the anchor was restored after $t s" || bad "the flushed anchor was not restored within 90 s"
	expect deny "b -> a tcp 2222 after the restore" tcp pfa:b "$A4" 2222

	say "phase 10: IPv6 neighbour discovery survives its timers"
	sleep 120
	expect allow "b -> a v6 tcp 8080 after two minutes" tcp pfa:b "$A6" 8080
	expect deny "host -> a v6 icmp after two minutes (no rule)" icmp host "$A6"
	expect deny "b -> a v6 tcp 2222 after two minutes" tcp pfa:b "$A6" 2222
fi
cleanup
[ "$fails" -eq 0 ]
