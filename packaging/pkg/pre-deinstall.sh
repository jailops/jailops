# The pre-deinstall script of the jailops package (the recipe of jailops-packages names this file, as it names
# post-install.sh). pkg(8) runs it when the package is removed. The file the helper wrote for the names of the jails
# has interface lines that take the loopback from the host's own resolver, and the helper put the resolver addresses
# of the IPv6 networks on lo0: with the package gone nothing takes them away again, so this does. It leaves resolv.conf
# and every file that is not the helper's alone; it is safe to run twice.
case "${2:-}" in
DEINSTALL | PRE-DEINSTALL) ;;
*) exit 0 ;;
esac
f=/var/unbound/conf.d/jailops.conf
if [ -f "$f" ] && head -n 1 "$f" | grep -q '^# Managed by JailOps'; then
  rm -f "$f"
  service local_unbound restart >/dev/null 2>&1 || true
fi
rec=/var/db/jailops-priv/host/dns-resolver-addrs.json
if [ -f "$rec" ]; then
  for a in $(tr -d '[]" ' < "$rec" | tr ',' ' '); do
    ifconfig lo0 inet6 "$a" -alias >/dev/null 2>&1 || true
  done
  rm -f "$rec"
fi
exit 0
