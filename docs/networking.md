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

`network create` makes a network of the host; it is listed for the host's administrators at once. A project may use it
only after `jailops network grant NAME --project PROJECT`, also when a global administrator creates the jail, and a
network is granted to one project. `storage create` and `storage grant` work the same way.

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
  the firewall of a jail closes them out unless a rule opens the way.
- **`ifconfig` says what each interface is.** The host end of a jail's link says
  `jailops jail=shop/site net=office (host side)`; inside the jail the other end says `(jail side)`.

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
A jail without a policy accepts connections from the host and from the jails of its project, may connect anywhere but to
the host and to other projects, and gets ping and DNS from the host and nothing else; `jailops jail info` says so.

**`any` is not the host.** As the destination of an allow rule, `any` (named or left out) is every address except the host's
own and those of the networks of other projects: `allow out tcp port 22` lets a jail ssh to the internet, not to the sshd of
the host or to the jails of another project. Name the other project's prefix to open its jails. A deny rule to `any` still
denies the host.

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
    jailops firewall show web --all     # the rules in the order pf decides (--pf adds the pf text of each)
    jailops firewall exposed            # everything the Internet reaches, across the jails
    jailops firewall test --from internet --to jail:web --port 443

`firewall show` has one row for the Internet, one for each WireGuard tunnel, and one each for the other jails of the
project, the host and the other projects. A row says the family, protocol and ports, who it is limited to (`any`, an
address, a prefix, a set), and the rule that gives it: "policy web rule 2". "closed" rows name what stops it. Below the
table is what the jail itself reaches.

The Internet reaches a jail in two ways. An IPv6 address is routed: an `allow in` rule with an external source opens
it. An IPv4 address of a NAT network is private: only a forward of a port of the host (an `allow in` rule with
`redirect`, `HOSTPORT:JAILPORT`) reaches it, and `list` shows the port of the host. A rule that names only jails, tags or
`host` as its source does not show as Internet. `firewall exposed --tunnels` adds what WireGuard peers reach.

What the view cannot know it says under the table: a raw PF fragment, and the rules of the host's own `pf.conf` before the
anchor line. The console shows the same on the Firewall tab of a jail and on the Exposed tab of the firewall page.

## Limits

A device can be limited in each direction with `nic.eth0.egress` (what the jail sends) and `nic.eth0.ingress` (what it
receives), for example `10M` for 10 Mbit/s. Limits survive a restart of the jail and apply to everything the jail sends or
receives, whoever started the connection. The dummynet pipes numbered 40000 to 59999 belong to JailOps: it numbers them once,
stores the numbers, and removes a pipe of that range that no jail's loaded anchor names; use other numbers for pipes of your own.

    jailops config set site nic.eth0.egress 20M --project shop
    jailops config set site nic.eth0.ingress 50M --project shop

## What the host needs

Forwarding on (`gateway_enable=YES`, `ipv6_gateway_enable=YES`), the `pf` and `if_epair` modules, and the one anchor line
in `pf.conf`. `jailops doctor` checks all three and says what to do.

## When pf is off or was flushed

A jail with a network link does not start while pf is disabled (`pfctl -s info` says `Status: Disabled`): its anchor would be
loaded and nothing would evaluate it. `jailops doctor` fails `pf.enabled` in that case. Set `allow-start-without-pf = true`
in `priv.conf` only on a host where that is what you want. `service pf start` and `service pf restart` empty every anchor;
the daemon looks every few seconds and loads the anchors of the running jails again at once.

## Letting the Internet in: `jailops expose`

```
jailops expose web 80,443                      # IPv4 and IPv6, from anyone
jailops expose mail 25,465,587,993 --comment mail
jailops expose web 443 --from set:office       # only from an address set, an address or a prefix
jailops expose web 80 --family 6               # IPv6 only
jailops expose app 3000 --host-port 8080       # the host's port 8080 goes to the jail's port 3000 (IPv4 behind NAT)
jailops unexpose web 80
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

## Networks

A network is an ordinary unicast range: IPv4 not in 0/8, loopback, link-local (169.254/16), multicast or 240/4; IPv6 a global
(2000::/3) or unique-local (fc00::/7) prefix.
