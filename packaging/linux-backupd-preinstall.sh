#!/bin/sh
set -e
getent group jailops-backup >/dev/null || groupadd --system jailops-backup
getent passwd jailops-backup >/dev/null || useradd --system --gid jailops-backup --home-dir /var/lib/jailops-backup --shell /usr/sbin/nologin --comment "JailOps backup node" jailops-backup
install -d -m 0750 -o jailops-backup -g jailops-backup /var/lib/jailops-backup
