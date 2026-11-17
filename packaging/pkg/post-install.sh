# The post-install script of the jailops package (the recipe of jailops-packages names this file, so a change of the text
# ships with the release and needs no change of the recipe). pkg(8) runs it on a first install and on an upgrade.
# An upgrade (jailops-setup has written priv.conf): only what the new version needs, not the first-install steps.
if [ -f /usr/local/etc/jailops/priv.conf ]; then
  echo "JailOps is upgraded; both programs still run the old binaries. Restart them: service jailopsd restart"
  exit 0
fi
echo "JailOps is installed. Next, as root, one command:"
echo "  jailops-setup"
echo "It sets the host up (the pool, IP forwarding, the host resolver that gives the jails names, the anchor lines in /etc/pf.conf, the service) and runs jailops doctor."
echo "It asks before it changes pf; jailops-setup -n shows what it would do. The CLI works as root afterwards;"
echo "jailops-setup --web adds the web console. See man jailops-setup."
