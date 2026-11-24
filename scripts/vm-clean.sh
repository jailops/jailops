#!/bin/sh
# vm-clean.sh: remove what an earlier run of the FreeBSD tests left on a test VM (services, jails, the test pool, interfaces, state). Runs ON the VM,
# as root; scripts/vm-run.sh and scripts/ci-ahead-run.sh copy it over and run it before a part.
for s in postgresql jailopsd jailops_priv jailops_backupd; do service $s onestop >/dev/null 2>&1; done
pkill -x postgres; rm -rf /var/db/postgres/data17 /var/db/postgres/data16
pkill -x jailopsd; pkill -x jailops-priv; pkill -x jailops-backupd; sleep 1
for j in $(jls -N name path 2>/dev/null | awk '$2 ~ /^\/jopsci\// {print $1}') $(jls -N name 2>/dev/null | grep -E '^(demo_|jobuild)'); do jail -r $j; done
sleep 1
for m in $(mount -p | awk '$2 ~ /^\/jopsci\// {print $2}' | sort -r); do umount -f $m 2>/dev/null; done
zpool destroy -f jopsci 2>/dev/null; zpool destroy -f jopsfresh 2>/dev/null
for b in $(ifconfig -l | tr ' ' '\n' | grep -E '^(jo[0-9]+|jo[a-f0-9]{8,}|wg-)'); do ifconfig $b destroy 2>/dev/null; done
rm -rf /var/db/jailops-priv/wireguard
# the resolver addresses the helper put on the loopback (its record lists them) stay when it is stopped: the next run's network
# with the same subnet is refused ("overlaps ... on the host interface lo0")
for a in $(tr -d '[]"' < /var/db/jailops-priv/host/dns-resolver-addrs.json 2>/dev/null | tr ',' ' '); do ifconfig lo0 inet6 "$a" -alias 2>/dev/null; done
rm -f /var/db/jailops-priv/host/dns-resolver-addrs.json
pkg delete -fy jailops jailops-client jailops-backupd >/dev/null 2>&1
rm -rf /var/tmp/jailops-drill* /usr/local/etc/jailops /usr/local/etc/jailops-fresh /var/db/jailops /var/db/jailops-fresh /var/db/jailops-backup /var/run/jailops /var/run/jailops-backupd /var/tmp/jailops
# the anchors of an earlier run stay in pf until they are flushed
for a in $(pfctl -sA 2>/dev/null | grep jailops); do pfctl -a "$a" -F all >/dev/null 2>&1; done
# a run that was stopped can leave the nameserver of the machine on a resolver that is gone
service local_unbound onestop >/dev/null 2>&1
sysrc -x local_unbound_enable >/dev/null 2>&1
# the resolver the machine had before the first run: a run that enabled local_unbound leaves resolv.conf on loopback
[ -f /root/resolv.conf.orig ] || { grep -q '^nameserver 127\.' /etc/resolv.conf || cp /etc/resolv.conf /root/resolv.conf.orig; }
[ ! -f /root/resolv.conf.orig ] || cp /root/resolv.conf.orig /etc/resolv.conf
