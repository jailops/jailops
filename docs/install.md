# Installing JailOps on a FreeBSD host

JailOps needs **FreeBSD 15.1 or later**, a ZFS pool, and a firewall that is `pf`. `scripts/ci-freebsd-e2e.sh` is the
whole sequence below on a clean machine, and it is what the CI job runs; if a step here and that script disagree, the
script is right.

## 1. What the host needs

- A ZFS pool you are willing to give a dataset to (below: `tank`). JailOps never runs `zpool upgrade`.
- Kernel modules `if_bridge`, `if_epair`, `nullfs` and `pf`. Load them now and at boot:

      kldload -n if_bridge if_epair nullfs pf
      sysrc -f /boot/loader.conf if_bridge_load=YES if_epair_load=YES nullfs_load=YES pf_load=YES

  (Linux jails also need `linux64`, `linprocfs`, `linsysfs` and `fdescfs`; the daemon loads those itself while a Linux
  jail exists.) `jailops doctor` reports a missing module with the command to load it.
- Network access to `pkg.freebsd.org` for building images from pkgbase. The helper's download rule allows only the
  hosts in `fetch-host` of `priv.conf` (default `dl-cdn.alpinelinux.org` and `pkg.freebsd.org`), over https.

## 2. Install the packages

There are three packages: `jailops-client` (`jailops`, `jailops-sync`), `jailops` (`jailopsd`, `jailops-priv`, the rc.d
scripts and sample configuration; depends on the client) and `jailops-backupd` (the backup node, only for a host that
runs one).

    pkg install jailops

Trust the repository first, once (the steps and the key fingerprint are in the README of
[jailops-packages](https://github.com/jailops/jailops-packages)): the public key at `https://pkg.jailops.net/jailops.pub`
and `/usr/local/etc/pkg/repos/jailops.conf` with `url: "https://pkg.jailops.net/${ABI}/latest"` and
`signature_type: "pubkey"`. The packages are built for the FreeBSD 15 ABI only, for amd64 and aarch64, and need FreeBSD
15.1 or later. The JailOps packages appear in the repository after each release; until the first one is published,
build from source (`just build-freebsd`) or take the static binaries from the GitHub release archives.

## 3. The firewall: three lines in `pf.conf`

JailOps never owns `/etc/pf.conf`. It loads one anchor per jail, per project and per network below `jailops/`, and you
add three lines **once**. pf insists on an order: the translation anchors belong with the translation rules, before the
first `block` or `pass` rule, and `anchor` among the filter rules.

    nat-anchor "jailops/*"
    rdr-anchor "jailops/*"
    # ... your own options, scrub and nat rules, then ...
    anchor "jailops/*"
    # ... your filter rules

Two things to know about a host that already has a firewall:

- A `quick` pass rule that comes **before** `anchor "jailops/*"` passes its traffic for good, so JailOps's policies
  cannot filter it. `jailops doctor` warns (`pf.order`) and names such rules. Put the anchor line before broad `quick`
  passes.
- Reload with `pfctl -f /etc/pf.conf` and check it first with `pfctl -nf /etc/pf.conf`. Do **not** use
  `service pf restart` or `pfctl -F states` on a machine you are logged into over ssh: it flushes the states and cuts
  your session.

- **Normalise packets.** Put `scrub in all fragment reassemble` near the top of `pf.conf` (add `max-mss <n>` when the
  uplink is a tunnel or has a smaller MTU than the jails). The jails' anchors accept `match ... scrub` options (checked
  with `pfctl -a` on FreeBSD 15.1) but JailOps does not render any: the clamp depends on the uplink, which is yours.
  `jailops doctor` warns (`pf.scrub`) when the loaded ruleset has no scrub rule; the format it looks for is the one
  `pfctl -s rules` prints, `scrub in all fragment reassemble`.
- **NAT and `set state-policy`.** On a host whose `pf.conf` blocks by default and NATs the jails' IPv4 (`nat on` the uplink),
  forwarded packets of a jail were translated early by pf's floating state (the bridge passes them through pf on the jail's
  port first) and then dropped by the host: five of six pings never left (darius, FreeBSD 15.1). `set state-policy if-bound`
  at the top of `pf.conf` fixed it (6 of 6, and the PF matrix still passes). A host whose `pf.conf` starts with `pass all`
  (the jailbe test host) did not show it.
- **Traffic from the internet into a jail** is stopped by a host whose `pf.conf` blocks inbound traffic on the uplink by
  default. JailOps does not open the uplink for you: admit the addresses of the jails, and only the ports you want,
  with a rule of your own. The jail's anchor still applies afterwards (its policy decides what reaches the jail), so
  this rule is the host's door, not the jail's. For jails with addresses in `2001:db8:1:2:100::/80` behind `vtnet0`:

      pass in quick on vtnet0 inet6 proto tcp from any to 2001:db8:1:2:100::/80 port 8080

  (Verified on FreeBSD 15.1 amd64 with a jail listening on 8080 and `curl -6` from another host.) ICMPv6 errors, which
  path MTU discovery needs, are admitted by the jails' own anchors; the host's default `pass quick inet6 proto
  ipv6-icmp` line must stay.

Then `sysrc pf_enable=YES`, and start pf only if it is not running (`service pf onestart`).

## 4. Forwarding

JailOps never changes forwarding. The administrator enables it, and `jailops doctor` checks that it is on and
persistent:

    sysrc gateway_enable=YES ipv6_gateway_enable=YES
    sysctl net.inet.ip.forwarding=1 net.inet6.ip6.forwarding=1

## 5. Set the host up

    jailops-priv init -pool tank

This is the one-time setup of what the unprivileged daemon cannot make for itself. It creates the user and group `jailops`, the storage root dataset `tank/jailops` (mounted at `/jailops`, owned by root),
the state dataset `tank/jailops/state` (mounted at `/var/db/jailops/state`, owned by the daemon's user), the
directories, and the two configuration files `/usr/local/etc/jailops/priv.conf` and `jailopsd.yaml`. It is idempotent
(run it twice and nothing changes) and `-dry-run` says what it would do. Options: `-dataset`, `-mount`, `-user`, `-group`,
`-etc`, `-data-dir`.

## 6. Start the services

    sysrc jailops_priv_enable=YES jailopsd_enable=YES
    service jailops_priv checkconfig
    service jailops_priv start
    service jailopsd start
    sudo jailops doctor

Then create the first administrator. On the host, **root is the global administrator of the unix socket** (the recovery access); every
other local user is anonymous until they sign in with a JailOps identity. There is no Unix group that grants a role.

    sudo jailops user create admin --admin --password-prompt     # a global administrator; the password is asked, never an argument
    sudo jailops user set-password admin                         # recovery: root replaces a lost password, no old one needed
    sudo jailops token create ci --permission operator --projects web --expires 720h     # a token for scripts and for one person

Identities (users, API tokens, client certificates) have the same roles, scopes and audit over the socket and over the network.
A person who is not root uses a token (`JAILOPS_TOKEN`, or `jailops remote add`) or signs in to the web console. Run the commands
below as root (`sudo`) or with such a token.

`jailops doctor` should show no `FAIL`. A `WARN` for `sysctl.persistent` means forwarding is on but will not survive a
reboot until you set the `rc.conf` lines of section 4. Section "doctor checks" of [troubleshooting.md](troubleshooting.md)
explains each check.

## 7. The first jail

There is no default project, network or storage: you name them.

    jailops image create freebsd-15 --release 15.1-RELEASE --alias stable
    jailops image build freebsd-15
    jailops network create lan --bridge jo7 --ipv4 10.77.0.1/24 --uplink em0 --nat
    jailops project create web
    jailops project set web --network lan --storage default
    jailops project switch web
    jailops create nginx --image freebsd-15@stable --network lan --storage default
    jailops start nginx
    jailops exec nginx -- uname -a

Without `project switch` pass `--project web` (or set `JAILOPS_PROJECT`) on every jail command. The storage `default` is
the one the daemon creates on its first start (dataset `<pool>/jailops`). IPv6, big pools and backups continue in
[networking-ipv6.md](networking-ipv6.md), [storage.md](storage.md) and [backup-restore.md](backup-restore.md).

## Remote access

By default only the unix socket listens. To manage a host from another machine set `listen.https.address` in
`jailopsd.yaml` (for example `":8443"`), restart `jailopsd`, then on the host create a single-use trust token and use
it on the client (a token is valid for one hour by default):

    jailops trust add --name laptop --projects web                     # on the host: prints a single-use token
    jailops remote add myhost https://host.example:8443 --token-stdin  # on the client: paste the token

The client pins the server fingerprint from the token (there is no trust on first use). The web consoles are served by
the same listener.
