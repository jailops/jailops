# Networking

JailOps runs the network of your jails itself. A network is an address range with an uplink; every jail gets an address
from it and a private link to the host, and the host is the router. There is no bridge, no switch and nothing to
configure on the host besides forwarding.

    jailops network create office --ipv4 192.0.2.1/24 --uplink em0 --nat
    jailops project create shop
    jailops network grant office --project shop && jailops storage grant default --project shop
    jailops create site --project shop --image freebsd-15-1 --network office --storage default
    jailops start site --project shop
    jailops list --project shop

`network create` makes a network of the host. A project may use it only after `jailops network grant NAME --project
PROJECT`, also when a global administrator creates the jail, and a network is granted to one project. `storage create`
and `storage grant` work the same way. `jailops network list` shows the networks granted to the selected project;
`jailops network list --all-projects` (a global administrator) shows every network of the host, with GRANTED (does the selected
project hold it) and PROJECTS (the projects that do), and `jailops network show NAME` names them under `Granted to`.
`storage list --all-projects` and `storage show` do the same for storage. The console has the same switch, "All networks" and
"All storage", on its pages.

## What you get

- **Addresses are allocated for you.** The range of the network is the pool. A jail's address is written into its
  specification and stays with it; there is no DHCP, no router advertisement and no SLAAC. `jailops network leases
  office` lists who has what.
- **One link per jail.** Each interface of a jail is its own point-to-point link to the host. The host end carries the
  network's gateway address (`192.0.2.1`, the same on every link) and the link-local `fe80::1`; the jail holds its address
  as a `/32` and its IPv6 address as a `/128` and sends everything to the gateway.
- **The host is the router.** Traffic between two jails, from a jail to the internet and from the internet to a jail
  all passes through the host and its firewall. Nothing in the subnet that no jail has is reachable: the host answers
  such a packet with an error instead of sending it out of the uplink.
- **A network belongs to one project.** A project is granted whole networks; another project's jails are outside, and
  the firewall of a jail closes what comes in from them unless an `allow in` rule of that jail with a source admits them.
- **`ifconfig` says what each interface is.** The host end of a jail's link says
  `jailops jail=shop/site net=office (host side)`; inside the jail the other end says `(jail side)`.

## Networks belong to a host

A network is made on one host and describes that host's side of the world: its uplink, its ranges and whether it answers for
the addresses on the segment. Some rules follow from that and hold for the whole of 1.x:

- **A jail's attachment is a logical link to a network.** The name of an interface is a binding of one host, and the PF
  anchors, the routes and the link-local addresses are derived from the stored network and jail, never stored as the truth.
- **The same network name on two hosts is two networks.** A definition that the hub or a fleet repository applies to both
  gives each host its own object (a `fleet` object is managed with one definition and is not shared state, see
  [design.md](design.md#71-authority)).
- **Overlap is checked on one host.** `jailops network create` refuses a subnet that overlaps another network of the host,
  in any project, and the fleet check does the same per host. Nothing compares two hosts. A prefix that two hosts advertise
  into the same routing domain, an NDP proxy for the same addresses on the same uplink segment and the same public address
  and port on a shared listener each need **one owner**; JailOps does not enforce that today and has no resource for it. A
  routing domain with one writer would be that resource; it is not built, and the person who runs the hosts keeps the
  prefixes apart.

### Moving a jail to another host (planned, not built)

A cold move is: stop the jail, copy or receive its datasets, validate on the other host, an explicit traffic switch, and keep
the old copy until the new one is confirmed. Today the same result takes `jail export` and `jail import`, with the checks by
hand. The other host may have another uplink, subnet, MTU, public address, volume or WireGuard interface, so the address may stay
only when routing allows it; a renumber needs an explicit mapping of the address, the DNS names and the exposure
(`jailops expose`), and the move shows that mapping before it starts. Unattended failover is not offered: it needs fencing of
the execution, of the routing and of the storage, and until all three exist a person promotes a copy ([roadmap.md](roadmap.md)).

## Isolation between jails

Every jail has a link of its own, so jails never share a network segment:

- **They cannot see each other's traffic.** What a jail sends and receives travels on its own link to the host. A jail
  that captures packets sees its own traffic only.
- **They cannot poison each other.** ARP, neighbour advertisements, router advertisements and DHCP answers of a jail
  reach the host's end of its own link and nobody else. A forged one changes nothing outside that jail.
- **They cannot use each other's addresses.** The host routes an address to the one link of the jail that owns it, and the
  jail's firewall drops everything it sends from another source address and everything addressed to an address it does
  not own. An address a jail gives itself sends and receives nothing.
- **They talk only as far as the firewall allows.** The host forwards between two jails what both jails' policies allow.
  `jailops firewall test` says what happens to a connection before you try it ([firewall-testing](networking-ipv6.md#would-it-pass)).
- **"Closed from the Internet" assumes your `pf.conf` blocks inbound.** The anchors of the jails say what each jail allows; the
  host's own `pf.conf` is the door. With no inbound default block in it a jail with a routed IPv6 address, or an IPv4
  address without NAT, is reachable from outside whatever `firewall test` says. Put `block in all` (and a pass for ssh) in
  `pf.conf` ([install.md](install.md#3-the-firewall-one-line-in-pfconf)); `jailops doctor` warns (`pf.host_default_deny`) when the loaded ruleset has none. The state limit is the same
  kind of host setting: ten thousand states per rule in each jail's anchor against pf's default hundred thousand for the host,
  so a few busy jails can fill it; `pf.states` looks at the running jails and warns for a jail without a limit, and `set
  limit states` in `pf.conf` raises it.
- **Nothing is closed inside a project unless you close it.** A jail without policy is open to its project's jails; a
  policy that states no default leaves it so. To close a project or a jail, put a policy with `default_in: deny` in the
  project's mandatory list (`firewall_policies`) or on the jail, and open what is needed with `allow` rules; deny wins over
  allow, whichever policy states it:

  ```
  jailops firewall policy create default --default-in deny
  jailops project set shop --firewall-policy default
  jailops firewall rule add web in --action allow --proto tcp --port 80,443
  ```

  The uplink is only ever opened by an explicit `allow in` rule with an external source, never by a default.
  `jailops firewall show JAIL` says which policy sets each default.
- **There is no layer 2 between jails.** Multicast, mDNS, VRRP/CARP and other non-IP protocols do not pass from one jail to
  another. Services that need to find each other use addresses or DNS.

## The jail and the host

JailOps' firewall governs the jails; `/etc/pf.conf` governs this host. JailOps never edits `pf.conf`, never opens the
host, and needs exactly one line there: `anchor "jailops/*"`.

**The host is closed to every jail, whatever a policy says.** The anchor drops what a jail sends to an address of the host
before it looks at the allow rules of its policies, so an allow rule to `host`, to a prefix that holds a host address (all
of IPv4 or IPv6, the network's own subnet, the LAN of the uplink) or to a set that holds one never reaches a service of the
host. `allow out ... --to host` is refused, with a message that says so. Two things stay open: DNS to the network's resolver
(udp and tcp port 53) and ping, with the ICMP errors a path needs, to the host's addresses. If a service of the host has to be
reachable from a jail, that is the host administrator's decision and it goes in `/etc/pf.conf`, as a quick pass rule before
the anchor line.

The host may connect to a jail when the jail's policy lets it: `allow in tcp from host port 22` lets the host reach the jail.
A jail without a policy accepts connections from the host and from the jails of its project (other projects need an
`allow in` rule with a source), may connect anywhere but to the host, and gets ping and DNS from the host and nothing else;
`jailops jail info` says so.

**`any` is not the host.** As the destination of an allow rule, `any` (named or left out) is every address except the host's
own: `allow out tcp port 22` lets a jail ssh to the internet, not to the sshd of the host. A deny rule to `any` still denies
the host.

**Other projects reach a jail like the Internet does.** What a jail sends is not closed on its side, not even towards another
project's jails. What comes in from another project is closed until an `allow in` rule of the jail that is reached has a
source that covers it: `any`, an address, a prefix (a wide one such as `0.0.0.0/0` or `::/0` too) or an address set. A rule
without a source is for the project's own jails and the host, a default never opens it, and a deny rule wins. This holds for
every address of the jail, the private IPv4 one included. To keep another project out of a rule that says `from any`, name
the sources instead or add a deny rule. Another project cannot use a forward of a jail through the host's public address or a
name that points at it, because the host is closed to jails: it connects to the jail's own address.

**A rule that names a host address matches the source the host sends from.** The host has the gateway address of a network and may hold more addresses on the same prefix. A connection that the host starts to a jail leaves from the address the kernel chooses among them, and FreeBSD 15 and 16 choose differently. To let the host in with a rule `from: <address>`, send from that address (`ping -S ADDRESS`, `nc -s ADDRESS`), or name the gateway address as well.

**Ping and DNS to the host stay open under every policy**, a deny-all one included, because a jail needs its gateway and its
resolver. To close them, add deny rules, which win over every allow rule: `jailops firewall rule add POLICY out --action deny
--proto udp --port 53 --to host` (and `--proto tcp`, and `--proto icmp` for ping).

**A jail with a link and no address** (`--network lan=none`) has an anchor that drops everything in both directions: there is
no source address it may use and nothing is addressed to it. A jail with a link and no rendered anchor does not start.

A raw PF fragment in a policy is for the administrator of the host: it needs a global administrator, and the helper loads
one that stands on an interface other than a jail's link only when the operator set `admin-raw-pf` in `priv.conf`. It is
rendered after the closing of the host, so it cannot open the host either, and the anchor of one jail never stands on the
link of another jail.

**Names.** With one line in `priv.conf` the jails of a project reach each other by name, `postgres.shop.internal`, answered
by the unbound of the host and limited to the project of the jail that asks: [dns.md](dns.md).

## Several networks, several families

A jail can attach to several networks; each attachment is its own link with its own gateway. A network has IPv4, IPv6 or
both. The IPv6 prefix is either routed to the host by your provider or sits on a shared segment of the uplink; in the second
case the NDP proxy of the network (`jailops network ndp-proxy enable NETWORK`) answers neighbour discovery for the host;
see [networking-ipv6.md](networking-ipv6.md).

## Reaching the outside

`--nat` translates what leaves through the uplink to the uplink's address. A jail that must be reached from outside gets
`jailops expose` ([below](#letting-the-internet-in-jailops-expose)). The host's own `pf.conf` is the door: it
must contain the line `anchor "jailops/*"` ([install.md](install.md#3-the-firewall-one-line-in-pfconf)).

## What reaches a jail

The question "what can reach this jail?" has one answer for each kind of source, and it is computed from the anchor
JailOps loads, not from the policies you typed:

    jailops list                        # the EXPOSED column: what the Internet reaches
    jailops firewall show web           # every kind of source, the rule that gives the reach or stops it
    jailops firewall show web --lines   # the rules in the order pf decides (--pf adds the pf text of each)
    jailops firewall exposed            # everything the Internet reaches, across the jails
    jailops firewall test --from internet --to jail:web --port 443

`firewall show` has one row for the Internet, one for each WireGuard tunnel, and one each for the other jails of the
project, the host and the other projects. A row says the family, protocol and ports, who it is limited to (`any`, an
address, a prefix, a set), and the rule that gives it: "policy web rule 2". "closed" rows name what stops it. Below the
table is what the jail itself reaches.

The Internet reaches a jail in two ways. An IPv6 address is routed: an `allow in` rule with an external source opens
it. An IPv4 address of a NAT network is private: only a forward of a port of the host reaches it, which only
`jailops expose` makes (`HOSTPORT:JAILPORT`), and `list` shows the port of the host. A rule that names only jails, tags or
`host` as its source does not show as Internet. `firewall exposed --tunnels` adds what WireGuard peers reach.

What the view cannot know it says under the table: a raw PF fragment, and the rules of the host's own `pf.conf` before the
anchor line. The console shows the same on the Firewall tab of a jail and on the Exposed tab of the firewall page.

## Limits

A device can be limited in each direction with `network.eth0.egress` (what the jail sends) and `network.eth0.ingress` (what it
receives), for example `10M` for 10 Mbit/s. Limits survive a restart of the jail and apply to everything the jail sends or
receives, whoever started the connection. The dummynet pipes numbered 40000 to 59999 belong to JailOps: it numbers them once,
stores the numbers, and removes a pipe of that range that no jail's loaded anchor names; use other numbers for pipes of your own.

    jailops config set site network.eth0.egress 20M --project shop
    jailops config set site network.eth0.ingress 50M --project shop

## What the host needs

Forwarding on (`gateway_enable=YES`, `ipv6_gateway_enable=YES`), the `pf` and `if_epair` modules, and the one anchor line
in `pf.conf`. `jailops doctor` checks all three and says what to do.

## When pf is off or was flushed

A jail with a network link does not start while pf is disabled (`pfctl -s info` says `Status: Disabled`): its anchor would be
loaded and nothing would evaluate it. `jailops doctor` fails `pf.enabled` in that case. Set `allow-start-without-pf = true`
in `priv.conf` only on a host where that is what you want. `service pf start` and `service pf restart` empty every anchor;
the daemon looks every few seconds and loads the anchors of the running jails again at once, with one warning in its
log for the empty anchor it found ([troubleshooting.md](troubleshooting.md#common-failures) says how to read it).

## Letting the Internet in: `jailops expose`

```
jailops expose web 80,443                      # IPv4 and IPv6, from anyone
jailops expose mail 25,465,587,993 --comment mail
jailops expose web 443 --from set:office       # only from an address set, an address or a prefix
jailops expose web 80 --family 6               # IPv6 only
jailops expose web 80,443,8000-8100 --family 6 # a mix of ports and ranges (a range is let in, not forwarded)
jailops expose app 3000 --host-port 8080       # the host's port 8080 goes to the jail's port 3000 (IPv4 behind NAT)
jailops unexpose web 80
```

```
jail/web exposed: 80,443/tcp
  IPv6  2001:db8::10   open: no NAT, the jail's own address answers on 80, 443
  IPv4  10.20.0.10   forwarded: host 80->80, 443->443 (NAT)
```

One command opens the ports of a jail to the Internet on every family it has. How the traffic arrives depends on the
address: an IPv6 address, and an IPv4 address of a network without NAT, are reached directly, so the ports are let in on
the uplink to the jail's own address; an IPv4 address behind NAT is reached through a **forward** of the same ports of the
host (`--host-port` and `--listen` choose another host port or address). The jail's firewall policies are not touched: the
exposure is stored with the jail (`spec.expose`), rendered as the section "exposed ports" of its anchor, and shown by
`jailops list` (EXPOSED), `jailops info` and `jailops firewall show JAIL`. A network without an uplink reaches nothing from
outside, and the command says so. An allow rule of a policy without `--from` is for the jail's link only (the project's
jails and the host); it never opens the uplink. `--from any` or `jailops expose` does.

Only a global administrator makes an IPv4 forward, because a forward takes traffic that would reach the host: it is refused on a
port that a service of the host listens on (ssh, the console, the other JailOps listeners, the ACME challenge port while ACME
is the source) and on a `--listen` address the host does not have. An administrator of a project exposes IPv6 and
non-NAT IPv4 on their own. A request that needs a forward from someone who may not make one is refused as a whole, with the
hint to ask for IPv6 only (`--family 6`); removing a forward is a global administrator's too.

Two jails cannot forward the same host port: the second is refused when it is made, and the message names the first jail.

A port is exposed once per protocol: `expose web 443 --from set:office` after `expose web 80,443` takes 443 out of the
first entry and leaves 80 open to anyone, and `unexpose web 443` takes only 443 out of an entry that names 80,443. A
jail behind NAT forwards single ports, so a range there needs `--family 6`; `--host-port` is for one port, and
`--proto` takes `tcp`, `udp` or `tcp,udp`.

### Expose or a policy rule

| You want | Use |
|---|---|
| The Internet reaches a service, IPv4 and IPv6 | `jailops expose web 80,443` |
| The Internet reaches it over IPv6 only | `jailops expose web 80 --family 6` (or a rule `--from ::/0`) |
| The Internet reaches it over IPv4 only | `jailops expose web 80 --family 4` |
| Only some sources reach it | `jailops expose web 443 --from set:office`, or a policy rule with that source |
| Jails, projects or the host reach it | a policy rule (`--from jail:mail`, `--from tag:db`) |

The Internet: `expose`. Certain sources: a policy. `expose` is recorded with the jail and makes the IPv4 forward; a
policy rule never does. An IPv4 address behind NAT is reached from the Internet only through `expose`; over IPv6 and
routed IPv4 an allow rule with an external source opens the port too, and `firewall rule add` says so with one note when
the source is `any`, `0.0.0.0/0` or `::/0`. Both can name the same port: what they allow adds up, a deny rule wins, and
`jailops list`, `firewall show` and `firewall exposed` show the result with the rule that gives it. `expose` and
`unexpose` name the policy rule that already lets the port in or still does. `unexpose` removes the exposure only: a
policy rule that opens the port keeps it open.

## Networks

A network is an ordinary unicast range: IPv4 not in 0/8, loopback, link-local (169.254/16), multicast or 240/4; IPv6 a global
(2000::/3) or unique-local (fc00::/7) prefix.
