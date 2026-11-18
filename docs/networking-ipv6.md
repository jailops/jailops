# Networks, IPv6 and the firewall

## The model

- A **network** is an address range with an uplink policy, with an IPv4 subnet and optionally an IPv6 subnet, each with an
  explicit allocation **range** ([networking.md](networking.md) explains the model). JailOps allocates from the range,
  writes the address into the jail's spec and configures it. **No DHCP, no router advertisements, no SLAAC**:
  `accept_rtadv` is off in the jail, and no jail is ever sent a router advertisement or a DHCP answer (below).
- **A jail has exactly the addresses JailOps gave it.** It holds its IPv4 address as a `/32` and its IPv6 address as a
  `/128`, so nothing is on-link: its default routes lead to the host (an on-link host route to the gateway for IPv4,
  `fe80::1` for IPv6), and every flow, one jail to another included, passes the host and its PF. Every jail interface is a
  point-to-point link to the host (an epair whose host end carries the gateway), so frames never pass from jail to jail.
  The host routes each of the jail's addresses to that one link. An address or MAC address a jail adds for itself sends
  and receives nothing.
- A network **belongs to one project**; a project is granted whole networks. Storage may be shared. A jail names its network
  and storage explicitly (or a profile does); nothing is filled in.
- The host must forward (see [install.md](install.md)).
- **`ifconfig` says what each interface is.** A jail's epair says `jailops jail=<project>/<jail> net=<network> (host
  side)` on the host and `(jail side)` inside the jail, a WireGuard tunnel `jailops wireguard=<name>`
  ([wireguard.md](wireguard.md)). The reconcile loop puts a missing or changed description back on an interface it owns
  (`jailops doctor`: `net.descriptions`) and touches no other; an interface without a description is not taken as its own.
- A jail attached with plain `--network NAME` gets one address of every family the network has, the default route, and
  a `resolv.conf` (see [name resolution](#name-resolution-of-jails) below).

    jailops network create web --ipv4 10.88.0.1/24 --nat --ipv6 2001:db8:1:2:1::/80 --uplink vtnet0
    jailops project create shop
    jailops network grant web --project shop && jailops storage grant default --project shop
    jailops create site --project shop --image freebsd-15-1 --network web --storage default
    jailops start site --project shop
    jailops list --project shop          # ADDRESSES shows the IPv4 and the IPv6 address

`--ipv4 ADDR/PREFIX` makes the rest of the subnet the range; `--ipv6 PREFIX` gives the network its IPv6 prefix. For anything finer
(explicit ranges, DNS servers, exclusions) use a file: `jailops network create NAME -f file.yaml`; a prefix on a shared
segment needs the NDP proxy of the network, switched on after the create with `jailops network ndp-proxy enable NAME`
(see [Switched](#switched-the-ndp-proxy)).

## Name resolution of jails

JailOps writes a jail's `/etc/resolv.conf` when the jail starts, with the nameservers of the first rule that applies:

1. the `dns` of the jail's networks, when one of them sets it (`jailops network edit NAME`, `dns: [192.0.2.53]`);
2. else the host's own nameservers in `/etc/resolv.conf` that a jail can reach (not loopback, not link-local; at most
   three; the host's `search` line is not copied). The jails reach them through the network's NAT or routing;
3. else, when the host resolves only through loopback (`nameserver 127.0.0.1`, typically the base system's unbound),
   the network's **gateway address**, the host's own address on every jail's link. With the base system's resolver
   (`local_unbound`) that is all it takes: `jailops-setup` enables it and the helper writes the one file it needs under
   `/var/unbound/conf.d` (the gateway addresses it listens on, the subnets it answers and the names of the jails); see
   [dns.md](dns.md). Every jail reaches the gateway on udp and tcp port 53 whatever its
   firewall policy (a deny rule closes it), and `/etc/pf.conf` needs nothing for it: JailOps' firewall governs the jails. On a network with IPv6 the jail asks a second address, or only that one on an IPv6 only network: the
   network's **resolver address** (the second address of the IPv6 subnet, for example `2001:db8:1:2:100::1`), which
   the helper makes the host hold on its loopback and unbound listen on while the names of the jails are on
   ([dns.md](dns.md#nothing-to-configure)). A link-local gateway cannot be written into a `resolv.conf`.

`jailops network show NAME` says which rule applies (`DNS: ... (the host's nameservers)`, `(the gateway ...)` with the
lines above) and `network create` prints them once as a warning; `jailops doctor` (`network.dns`) sends the gateway one
query (`. NS`, one second) once a jail on the network runs and says whether it answers, refuses (access control) or is
silent.

A change of the network's `dns` reaches the **running** jails at once: their `resolv.conf` is written again and
`network edit` names them ("resolv.conf rewritten in the running jails ..."); a stopped jail gets it at its next start.
A change of the host's own `/etc/resolv.conf` reaches a jail at its next start (or at the next change of the network).

A jail that runs its own resolver, or wants its own file, removes the first line (`# written by JailOps`) of its `/etc/resolv.conf`, or
writes a file of its own before the first start: JailOps then never writes the file, neither at a start nor at a change of the network. The file is written through the
helper's file operations, which replace only a regular file inside the jail's root and refuse a symlink.

## Two kinds of prefix

| | Routed (the NDP proxy is off) | On a shared segment (the NDP proxy is on) |
|---|---|---|
| The provider gives you | a prefix routed to the host's address | a `/64` on the shared segment of the uplink |
| Who answers neighbor discovery | nobody has to; the upstream router already knows the prefix lives behind the host | the NDP proxy of the network, for the whole range |
| The jails' prefix | a `/80` (or any length) carved out of the routed prefix | a `/80` inside the shared `/64` |
| Jail gateway | the link-local `fe80::1` on the jail's link | the link-local `fe80::1` on the jail's link |
| Depends on the proxy | never | always |

Nothing uses SLAAC, so prefixes longer than `/64` are fine: a routed `/64` holds 65,536 networks of `/80`, each with 2^48
addresses.

### Routed

The upstream sends the prefix to the host; the host forwards. Give each network its own `/80`
(`2001:db8:1:2:1::/80`, `2001:db8:1:2:2::/80`, ...). The host routes each allocated address to its jail and answers a packet
for any other address of the prefix with an error, so an address nobody has is never sent back out of the uplink. You
choose each `/80`; JailOps does not carve them.

### Switched (the NDP proxy)

On many cloud servers you get a `/64` on a segment shared with other customers. The provider's router believes every
address in it is on-link and asks neighbor discovery who has each one; a jail behind the host never hears the question.
JailOps answers it itself.

1. **Check the prefix.** For a network with the NDP proxy an administrator creates, the helper lets the daemon capture neighbor
   discovery on the uplink (a read-only, locked BPF descriptor that delivers only ICMPv6 NS and NA) and advertise
   **addresses inside the network's prefix**, at a bounded rate (200 a second). The prefix is between /48 and /120, global
   or unique-local, not one the host holds an address in on any interface, and not overlapping another network; the
   uplink is a plain interface (not a bridge, a bridge member or an interface of JailOps).

2. **Create the network** with flags (the jails' gateway is `fe80::1` and the range starts at `::10`):

       jailops network create web --ipv4 10.94.0.1/24 --nat --uplink vtnet0 \
         --ipv6 2001:db8:1:2:100::/80
       jailops network ndp-proxy enable web

   or, to choose the range, DNS servers and exclusions, from a file:

       uplink: vtnet0
       ipv4: {subnet: 10.94.0.0/24, gateway: 10.94.0.1, range: 10.94.0.2-10.94.0.254, nat: true}
       ipv6:
         ndp_proxy: true
         subnet: "2001:db8:1:2:100::/80"
         range: "2001:db8:1:2:100::10-2001:db8:1:2:100::ff"
       dns: [1.1.1.1, 2606:4700:4700::1111]

   `jailops network create web -f web.yaml`. The uplink must be a plain interface, not a member of one of your bridges (JailOps checks).

   A network that exists switches with one command, for good (the setting is kept in the network and the proxy runs again
   after a reboot):

       jailops network ndp-proxy enable web      # the prefix is on a shared segment: the host answers for the range
       jailops network ndp-proxy disable web     # the provider routes the prefix to this host

   `--ipv6-range a-b` chooses the allocation range of the prefix. By default it is 240 addresses, `::10` to `::ff` of a
   `/80`, so the low addresses stay free for you.

   **The subnet lies inside the segment of the uplink.** JailOps refuses a proxy subnet outside the segment the uplink is
   on. The segment is the prefix of the host's global address on the uplink; the host usually holds that address as a
   `/128` (`inet6 2001:db8:1:2::1 prefixlen 128`), and then the segment is the `/64` around it, so any subnet inside
   `2001:db8:1:2::/64` is accepted. Take a part of the `/64` that the host's own address is not in (`2001:db8:1:2:100::/80`
   above), and a different part for each network: a subnet that holds an address of the host (the whole `/64`, say) is
   refused when the network is made, because the proxy never answers for the host's own address.

   **Limits JailOps checks for you.** The helper accepts proxied prefixes of `/48` to `/120` only, so an NDP proxy
   subnet must be `/48` to `/120`; a routed subnet is `/32` to `/126`. Two networks
   may not have overlapping subnets (IPv4 or IPv6, in any project): `network create` names the one that does. In every
   `/64` the subnet-router anycast address (`::`), the all-ones end and the RFC 2526 reserved anycast identifiers (the top
   128 interface identifiers) are never allocated; in other prefix lengths the network address and the all-ones end are not.

   **The uplink** is the interface the segment is on: a physical interface (without vlan children) or a vlan(4)
   interface. The physical parent of a vlan is not suitable (the proxy captures and sends the frames of the interface it
   is given); `jailops doctor` fails `network.uplink` for it, for a bridge member and for a virtual interface (lo, epair,
   tun, gif, wg, lagg and so on). The proxy runs on a physical (virtio) uplink; **UNVERIFIED** on a vlan(4).

3. **What the proxy does.** One proxy per uplink answers for the **allocation range of each network with the NDP proxy, whether
   a jail runs on an address or not**: there is nothing to register when a jail starts, so the first packet of a new jail
   never waits for the proxy. It answers for the first 1024 addresses of the range and for the address of every running
   jail; a range larger than that is answered above the limit only while the jail runs (`network ndp-proxy status` says
   so). It does the following:
   - answers the router's neighbor solicitation with the **host's own MAC** (the router then sends the traffic to the host,
     which routes it), within a millisecond; unicast solicitations of the router's reachability checks are answered too;
   - announces every address of the range once when the proxy starts and whenever the capture is reopened, and a jail's
     address three times when the jail starts, so a router that cached a failed resolution while the jail was down
     learns the host's MAC at once; announcements are rate-limited (10 a second);
   - captures with the uplink in promiscuous mode (`ifconfig` shows PROMISC while the daemon runs) so that no multicast
     filter of the network card or the hypervisor can hide a solicitation, and joins the solicited-node multicast group
     of every address of the range, which a snooping switch needs to forward the question to this port;
   - installs a `/128` host route through the jail's own link for a **running** jail only, and removes it **and the host's
     cached neighbor entry** (`ndp -d`) when the jail stops, so a new jail that gets the same address is reachable at once.
     A route that outlives its jail would blackhole the address: the reconcile loop puts back the `/128` route of every
     running jail on every pass, and so at daemon start;
   - sends a packet for an address of the range that no jail runs into a reject route of the prefix: the sender gets an
     ICMPv6 error and the packet never leaves the uplink again;
   - **defends** an address when another host of the segment runs duplicate address detection for it (a solicitation
     from `::`): the proxy answers with an advertisement to all nodes, so that host sees the duplicate. The capture sees the
     uplink only (BPF, inbound direction), and a jail runs its own DAD on its own link, which never
     reaches it, so a jail's DAD is not answered;
   - never answers for the host's own addresses (its kernel does), for the resolver address of the network (the second
     address of the prefix, held on the loopback), or for an address outside the range, and rate-limits itself. An
     announcement that the helper refuses (it reads the routing table at most every second and a quarter, and a route
     that was installed a moment ago may not be in its reading yet) is tried again after 1.2 seconds, up to 20 times,
     and does not use up one of its three repeats.

   **A neighbor that sends too many solicitations** cannot make the host spend its time on them: the proxy looks at no
   more than 50 solicitations a second (a burst of 100) from one hardware address and at 2000 a second (a burst of
   4000) from all of them together, and drops the rest without an answer, so the upstream router, which sends a few,
   is still answered. The numbers are fixed, far above what a segment of jails needs, and the table of requesters
   is bounded. What was dropped is counted, written to the log at most once a minute and shown by `jailops doctor`
   (`network.ndproxy` warns, with the count of the last hour) and in the network's `NDPFlood` condition.

4. **Check it:** `jailops doctor` fails `network.ndproxy` when a proxy network is not
   answering; `jailops network show web` has an `NDPProxy` condition (`Answering`, `StartFailed` and so on). In the
   first minutes after `jailopsd` starts the condition is `Starting` (the capture of the uplink is being opened) and
   the doctor warns instead of failing; it opens on its own.

#### One host setting that matters on some segments

    sysctl net.inet6.icmp6.nd6_onlink_ns_rfc4861=1          # now
    echo 'net.inet6.icmp6.nd6_onlink_ns_rfc4861=1' >> /etc/sysctl.conf   # after a reboot

On some shared segments the routers check that the host's own address is still there with unicast neighbor
solicitations sent from their global addresses. The host holds its address as a `/128` and no router advertisement
describes the `/64`, so for FreeBSD the sender is not an on-link neighbour and, with the default `0`, the solicitation is
dropped and counted as bad. Not every network is fully RFC compliant: some routers probe from a global address as
above, and other systems answer such a solicitation where FreeBSD does not by default. A router that gets no answer may
mark the host unreachable for a while, and the jails behind it with it. With `1` every probe is answered (the packet must
still arrive with hop limit 255). The setting is the administrator's decision, because it makes the host a little less
strict, and JailOps never sets it. `jailops doctor` shows the value as information
(`sysctl.net.inet6.icmp6.nd6_onlink_ns_rfc4861`) while a network with the NDP proxy exists, and `network create` prints a note
that points here.

**Recognise the case:** the counter of bad neighbor solicitations grows over minutes while the host or its jails answer
from outside only some of the time. A segment whose routers do not probe from global addresses never shows it, and then
the default is fine. Look at the counter, and at the probes:

    netstat -s -p icmp6 | grep -i 'bad neighbor solicitation'
    tcpdump -n -e -i vtnet0 'icmp6 and ip6[40] == 135 and ip6 dst host 2001:db8:1:2::2'

The second command shows the probes for the host's address (`2001:db8:1:2::2`) arriving from the routers' global
addresses; without the sysctl no advertisement (`ip6[40] == 136`) follows them.

On a host that is not in appliance mode, set a static default route (`ipv6_defaultrouter="fe80::1%vtnet0"` in `rc.conf`,
with the router's link-local address): a default route learned from router advertisements does not last on a host that
forwards, which JailOps needs, because FreeBSD ignores advertisements on a forwarding host.

**Verified** on FreeBSD 15.1 **amd64** with a shared switched `/64`, the jails in a `/80`: the upstream routers were answered
at once; a jail pinged the internet for 50 minutes (3,000 packets, none lost) and another machine pinged the jail for 50
minutes (3,000 sent, 2,992 received, all eight lost in the first minute, while the router resolved a new address); a new
address was reachable from outside within about three seconds of its jail starting; the host's own address and ssh sessions
were unaffected; and the PF traffic matrix passed while the proxy ran (design 18.10, 18.11).
**UNVERIFIED:** the defence against another host's duplicate address detection (unit tested; it needs a second machine on the segment, and the test hosts have none), the retry of a refused announcement on a live host (unit tested), providers that filter by MAC address or limit neighbor discovery, hundreds of proxied addresses on one
uplink, MLD-snooping switches, removing and recreating the uplink while the proxy runs.

### Seeing what the proxy does

A jail on a switched network that has no IPv6 outside is usually a router that asked "who has this address" and got no
answer. `jailops network ndp-proxy status NETWORK` shows whether the proxy is on, the range it answers for, how many
questions it got, answered and dropped, when the last question, answer and announcement were, and a table of the addresses
that were asked for or have a running jail, with what happened to the last question of each:

    jailops network ndp-proxy status web
    jailops network ndp-proxy watch web        # each question and answer as it happens; --count N stops after N

An address with `seen` above zero and `answered` zero is one the router asked for and the proxy did not answer: the
last drop says why (a rate limit, a refusal of the helper). An address the router never asked for does not appear. `-o json`
prints the same for scripts. The last 256 events are kept in memory; `status` and `watch` need a global identity, a read-only one is enough; `enable` and `disable` need a global operator.

## Bandwidth limits

A device of a jail can have a rate limit in each direction, in bits per second with decimal units (`10M` is 10 Mbit/s,
`1G` is 1 Gbit/s):

    jailops config set web nic.eth0.egress 10M       # what the jail sends through eth0
    jailops config set web nic.eth0.ingress 20M      # what the jail receives through eth0
    jailops config unset web nic.eth0.egress         # no limit

`egress` limits everything the jail sends through the device, whoever started the connection: an upload, the answers of
a server in the jail, a transfer to another jail. `ingress` limits everything the jail receives, from the host, from
other jails and from the internet. The host enforces each with a dummynet pipe that the jail's anchor sends the traffic
into, on the jail's own link. The limit is per device and direction. A pipe keeps its number for as long as its limit
exists, and the limits come back with the jail when it restarts.

The kernel module `dummynet` must be loaded (JailOps loads it when a jail has a limit; add `dummynet_load="YES"` to
`/boot/loader.conf` to have it at boot), and `jailops doctor` fails `kernel.module.dummynet` when a limit exists and the
module does not.

## Firewall

PF only, **one anchor per jail** (`jailops/<project>/<jail>`), one per project (`jailops/<project>`, with the project's
mandatory policies and a table of the other projects' subnets) and one per network (`jailops/net.<network>`, with the
NAT). You add one line to `pf.conf` once ([install.md](install.md)); JailOps never owns that
file. A ruleset is rendered, checked with `pfctl -n`, loaded in one operation and **read back**; a load that did not take
effect is reported as drift. When a rule is removed, the states that rule created are ended too, so a running connection
stops when its rule goes.

- **Projects are closed to each other.** A jail without a policy still gets an anchor with isolation. Cross-project traffic is
  opened only by a rule you write, for example an address set shared by a `common` project on its own network.
- **A project's mandatory policies are a floor:** a jail's policies can add rules but cannot weaken the project's defaults.
- **Per-jail anti-spoofing** binds a jail to its own addresses and its **own link-local address** (derived from the MAC
  JailOps sets, so the planner knows it; the whole of `fe80::/10` is allowed only when it cannot be derived), plus `::` for
  duplicate address detection only: a neighbour solicitation to a solicited-node group, nothing else. A jail that changes
  its own link-local address loses neighbour discovery: it is its own doing.
- **IPv6 control traffic of a jail.** A jail is never a router: its router advertisements and redirects are dropped. Its
  neighbour solicitations and advertisements carry only its own addresses as source, and an advertisement never goes to a
  multicast group. It may send MLD reports to link-local groups. Only neighbour messages addressed to its own addresses
  and their solicited-node groups reach it.
- **Each jail is alone on its link.** A jail holds its address as a `/32` and `/128` and its default route is the host's
  link-local `fe80::1` (IPv4: the gateway, reached by a host route), so it has no on-link neighbour: everything, another jail
  included, goes through the host and its PF. The only other party on the link is the host, whose end answers for the
  gateway addresses. What a jail forges (ARP replies and requests, neighbour and router advertisements, DHCP discovers)
  reaches the host's end of its own link and changes nothing outside it: the neighbour entries of other jails and of the
  host stay as they were, a jail that takes another jail's MAC address takes none of its traffic, an extra address is
  reached by nobody and cannot be used as a source, and a jail that routes another jail's address onto its own link
  reaches nothing, because nothing on the link answers for it. A capture inside a jail sees its own traffic only.
- **ICMPv6 errors are never blocked.** Destination unreachable, packet too big, time exceeded and parameter problem pass in
  both directions, whatever the policy says (path MTU discovery depends on
  them; a jail with `default_in: deny` still learns its path MTU). Echo is policy. Router solicitations and advertisements
  are not passed: jails use no SLAAC and `accept_rtadv` is off (a Linux guest is configured by the host).
  That the error reaches the jail was verified with a forged packet-too-big from the host (the rule's counter shows it
  passing); **not automated**, because the host's pf refragments what it forwards instead of answering with the error,
  so an ordinary oversized echo never produces one.
- **PF scrub.** Anchors accept `match ... scrub (...)` and `scrub` rules (checked with `pfctl -a` on FreeBSD 15.1), but
  JailOps renders none: the host's `pf.conf` owns normalisation (`scrub in all fragment reassemble`, see
  [install.md](install.md)), and `jailops doctor` warns (`pf.scrub`) when the loaded main ruleset has no scrub rule.
- **Inbound connections from the internet** into a jail on a proxy or routed network are opened by the jail's own firewall,
  not by `pf.conf`. An `allow in` rule with an external source in a policy of the jail
  (`jailops firewall rule add web in --action allow --proto tcp --from any --port 8080`, or `jailops expose web 8080`) renders `pass in quick on <uplink> ... to
  <jail address> port 8080` in the jail's anchor, which wins over the host's `block ... all` wherever the anchor line
  stands. A jail with only defaults stays closed from the internet, and `jailops firewall test --from <address outside>
  --to jail:web --port 8080` walks that hop too. See [install.md](install.md) section 3.
- A rule has an action (`allow` or `deny`), a direction (`in` or `out`) and, as it needs them, a protocol, `from` and `to` endpoints, ports, `log`, a name, a comment and `disabled`. The rules of a policy are added and changed one at a time with `jailops firewall rule` (`add`, `list`, `edit`, `move`, `enable`, `disable`, `remove`), each rule addressed by its number or its name, or all at once in a file.
  A rule belongs to the jail that has the policy: an inbound rule is always to the jail itself and an outbound rule always from it, so an inbound rule names only `from` and an outbound rule only `to` (`to` on an inbound rule and `from` on an outbound rule are refused).
  An `allow in` rule with an external source (`any`, an address, a prefix or an address set) is rendered the
  same way, on the uplink of each routed address of the jail (an IPv6 address of a routed or proxy network, an IPv4
  address of a network without NAT): `pass in quick on <uplink> ... to <jail address> port N`. That is what exposes a
  jail to the internet; `jailops expose` writes such a rule for you, for both families, and forwards IPv4 behind NAT. A rule without a source is for the jail's link only. The defaults, and the rules whose source is `host`, a jail or a tag, never open the uplink; a
  `deny in` rule is rendered on the uplink first, so it wins at the door as on the jail's link, and a packet that comes
  in on the uplink from a subnet of the jail's own networks or of another project, or that pf's reverse path check
  refuses (`urpf-failed`: an address of a tunnel or of the host's own network from outside), is dropped as forged. Every
  pass on the uplink limits its states (`limits.states` of the jail, else `jails.max_states` of the host, else 5000), so a flood of connection attempts fills
  the rule and not the host's state table.
  An endpoint is one of these, the same words wherever a firewall names a source or a destination:

  | Endpoint | Stands for | Where it is accepted |
  |---|---|---|
  | `192.0.2.7`, `192.0.2.0/24`, `2001:db8::/48` | an address or a prefix | rules, address-set members, `app allow` |
  | `jail:name` | a jail of the project: its addresses, read at every render, so the rule follows the jail; a jail that does not exist matches nobody and `jailops doctor` says so (`firewall.refs`) | rules, address-set members, `app allow`, `firewall test` |
  | `set:name` | an address set of the project | rules, address-set members, `app allow` |
  | `tag:name` | every jail of the project with that tag | rules only |
  | `any` | every address | rules only |
  | `host` | the host itself: the gateway addresses of the app jail's networks | `app allow`, `firewall test` |

  A reference is always `kind:name`; `@` is not used (`@web` is refused with the words to write instead). The kinds are
  separate namespaces, so a jail and an address set of one project may have the same name; a jail name and an address set name have
  at most 29 characters, so each can be named in a rule and in a pf table. The pf tables are `j_<jail>`, `s_<set>` and
  `t_<tag>`, with every hyphen of the name written as `_`; names have no underscore, so two names never give one table. A set may hold one address: give it a name and say in its comment what it is.
  `jailops firewall rule add --help` has the flags.

  **A policy always says its defaults.** `default_in` and `default_out` are required, `allow` or `deny`, on every policy
  (`policy create` without `-f` needs `--default-in` and `--default-out`; a file without them is refused, and so is a
  policy stored without them: the jails that use it are not rendered again and `jailops doctor` fails `firewall.defaults`
  naming the policy). A jail without a policy is open apart from the isolation of projects and from the host, which answers it with ping and
  DNS only; `jailops jail info` says so: `Firewall: open: no firewall policy (the host answers ping and DNS only)`; with policies it lists them in the order they are rendered, the project's marked `(project)` and the managed one `(managed by app-access)`. A jail names the policies of its own in `firewall.policies`, a list
  (`jailops config set DB firewall.policies postgres,ssh-office`, or `--firewall-policy` more than once at `jailops create`); it cannot name a
  mandatory policy of the project, which is rendered for every jail already, nor a policy that something else manages
  (`app-JAIL`), and it names none twice.

  **Several policies.** A project has a list of mandatory policies (`jailops project set PROJECT --firewall-policy default
  --firewall-policy monitoring`, `firewall_policies` in a file; an empty value clears the list) and a jail has a list of its
  own. The anchor of a jail holds them in this order: the project's policies in the order of their list, the managed policy
  `app-JAIL` when the jail has one, then the jail's own policies in the order of theirs. Each policy is a section of the anchor with
  its own heading (`# project policy NAME` or `# policy NAME`, then its comment) and its own rule numbers, so `policy show NAME`
  and the anchor read the same. The defaults are the strictest of all the policies in play: `deny` wins over `allow` in each
  direction, and a policy of a jail can open ports but never turn a default deny of the project into allow. A policy that a list
  names is not deleted (the refusal names who uses it) and is renamed in every list. The usual shape: the project's mandatory
  policy `default` (deny inbound, ping allowed) for every jail, and on the PostgreSQL jail `firewall.policies: [postgres]`,
  where `postgres` allows the address set `postgresql-allow` on port 5432; a policy such as `ssh-office` is a block that
  many jails name.

  **Deny wins over allow, and the order of the policies does not matter.** When any policy of a jail has an enabled deny
  rule, the anchor holds the deny rules of all the policies first (under the line `# deny rules of all policies: they win
  over every allow rule`), then the allow rules of the policies (`# allow rules of the policies, in the order of the
  policies`). Each group keeps the order of the policies and of the rules, and each rule keeps its heading and its number, so
  rule 3 of `postgres` is still rule 3 of `postgres` in the anchor; a policy that has rules of both kinds appears in both
  groups. Every rule is `quick`, so a connection that a deny rule matches is blocked whichever policy comes first, and an
  allow rule decides only where no deny rule matches. An anchor without a deny rule is written policy by policy. The closing of the host (ping and the
  resolver excepted) stands after the deny rules and before every allow rule, so no allow rule reaches a service of the host. A raw PF
  fragment stays after the allow rules of its policy, and so after the closing of the host.

  **A rule can be switched off without deleting it.** `jailops firewall rule disable web 2` (`--disabled` on `rule add`, `disabled: true` in a file, a checkbox in the console) keeps the rule in the policy: it is validated
  like any other, it keeps its number, `policy show` marks it, and the anchor has it as a comment line (`# rule 2
  (disabled): name`, then its comment) so a reviewer sees what is off. It is not loaded: a disabled allow rule lets nothing
  through and a disabled deny rule stops nothing, and a disabled deny rule does not group the anchor.

  **Projects are boundaries.** Another project is treated exactly like an external host: only addresses and address sets
  cross it, never a jail reference. Do two things need to talk to each other as one system? Then they belong in the same
  project. Across projects, write the addresses explicitly on both sides: the project that serves allows the addresses in,
  the project that connects allows the addresses out.

      # project shop: the web jail of project blog (10.30.0.5) may reach the database
      jailops firewall address-set create blog --comment "the web jail of project blog"
      jailops firewall address-set add blog 10.30.0.5
      jailops firewall policy create db --default-in deny --default-out allow
      jailops firewall rule add db in --action allow --proto tcp --from set:blog --port 5432 --comment "the blog's web jail"
      # project blog: the jail may go out to the database at 10.20.0.10 (the closed default of a project says no otherwise)
      jailops firewall policy create out-db --default-in deny --default-out allow
      jailops firewall rule add out-db out --action allow --proto tcp --to 10.20.0.10 --port 5432 --comment "the shop database"

      jailops firewall address-set create office --comment "the office"
      jailops firewall address-set add office 192.0.2.0/24 --comment "the office network"
      jailops firewall address-set create git-allow --address 198.51.100.7 --address jail:web
      jailops firewall policy create web --default-in deny --default-out allow --comment "the public web server"
      jailops firewall rule add web in --action allow --proto tcp --from any --port 80,443 --comment "the site"
      jailops firewall rule add web in --action allow --proto tcp --from set:office --port 22 --comment "ssh from the office"
      jailops firewall policy create db --default-in deny --default-out allow
      jailops firewall rule add db in --action allow --proto tcp --from jail:web --port 5432 --comment "the web jail"

  A set is made empty and filled one member at a time, like a policy and its rules: `address-set add NAME MEMBER...` adds
  members (one that is already there is left as it is) and `address-set remove NAME MEMBER...` takes them out; a set may be
  empty, and a rule that names an empty set matches nothing. A member of an address set is an address, a CIDR, `jail:name` or `set:name`: a list such as
  `git-allow` can hold jails and be added to later. A jail that a rule or a set names is not renamed or deleted until the reference is changed (the refusal names the policy or the set), so a rule never names a jail that is gone, and a jail made later never takes the place of one.

  **Comments are for the review of the firewall.** An address set, each of its entries, a policy and each rule has a
  `comment`: what it is, why it is there (one line of printable text, at most 200 characters, without double quotes or backslashes because it becomes a comment line of the ruleset; it is not part of any rule). In a file or in
  `jailops firewall address-set edit` the entries of a set are pairs:

      spec:
        comment: Who may reach the git server
        addresses:
          - address: 198.51.100.7
            comment: the office
          - address: "jail:web"
            comment: the web jail

  `--comment` of `address-set create` says what the set is and `--comment` of `address-set add` says what the members it adds
  are; an IPv4 and an IPv6 network of the same office are two entries added with a comment each, so that each says what it is. A rule's comment is `--comment` of `rule add` and `rule edit`. `address-set show` and `policy show` list the comments in a table, the rendered anchor of a jail carries them as
  comment lines (`# set git-allow: 198.51.100.7 the office`, `# policy NAME` and `#   what it is for`, `# rule 1: name` and `#   why`), and the
  console has a comment field on a set, a policy and every rule, and takes the comment of a member at the end of its line
  (`198.51.100.0/24  # the office`).

**Reviewing a file.** `jailops firewall validate -f FILE --review` adds warnings a reviewer would raise and never changes the
exit status: a policy, rule, address set or member without a comment, a rule left `disabled`, a very broad rule (allow from
or to any address with no port), an address set that no rule refers to, a policy that no jail or project uses, and a
`jail:` that does not exist. Without a server it says what it could not check.

### Would it pass?

`jailops firewall test` says whether a new connection would pass the firewall, and which rule decides at each anchor it
meets. It walks the anchors JailOps renders and loads (`render.BuildAnchor`: the rules in the order they are loaded, each
with what it matches and where it comes from), not a second copy of the policies, so the answer is about the rules as
they are rendered.

    jailops firewall test --from jail:web --to jail:db --port 5432
    jailops firewall test --from jail:other --to jail:db --port 5432
    jailops firewall test --from 192.0.2.7 --to jail:web --port 443 -o json

An end is a jail of the project (`jail:web`), one address (the jail that has it, when there is one), or `host` (the host's
address on the gateway of the other end's network); a set is not an end. What the source jail sends is judged where it
arrives on the host, its own link (`in`); what reaches the destination jail is judged where it leaves the host for that
jail, its own link (`out`). A connection from an address outside every network of JailOps to a jail's routed address meets
the jail's anchor on the **uplink** first (`in`): no rule of the jail that admits it there is a block, because the host's own
pf.conf decides then and blocks inbound traffic by default. All hops must pass; each
prints the decision (`policy postgres rule 1 "who may reach PostgreSQL"`, a project policy, the managed policy of an app,
the isolation of projects, or the default of the anchor) and the rendered pf line. An address that belongs to no jail of
the project, another project's jail included, is an external host: only the other end's anchor is evaluated.

For the example above, with the project policy `default` (deny inbound, ping allowed) and `postgres` on the db jail
(`allow in tcp from set:postgresql-allow port 5432`, the set holding `jail:web`): `web` to `db` port 5432 passes, port 5433 is
blocked by the default of the anchor, and `other` to `db` port 5432 is blocked too; ping from `other` passes by the
project policy. The exit status is 0 pass, 1 block, 8 unknown. `icmp` and `icmp6` test an echo request unless
`--icmp-type` names another type, and say so.

**What it cannot answer, and says so** (`unknown`, or a line under "Not covered", never a guess): a raw PF fragment that
sits before the decision (`unknown`, naming the policy); a packet that no rule of the anchor matches (the host's rules
decide); the host's own `pf.conf` (the anchors hang under it and its rules are not evaluated); a connection that is
already open (pf keeps its state; the answer is for a new one); NAT (the source address is the jail's own, as on its
port); and the anchors of other jails (only the destination's is evaluated).
When a deny rule decides, the hop lists under `skipped` the allow rules that also match and lost to it (a deny rule wins
over every allow rule, whatever the order of the policies); a disabled rule that the packet would have met (by direction,
protocol and port; its addresses are not resolved) is listed there too, as `disabled`. A reference to a
jail that does not exist is an empty table that matches nobody, and the test says so.
`loaded_matches_rendered` in the JSON says whether the rule lines loaded in pf for each anchor are the rendered ones
(`true`, `false`, or `unknown` when pf cannot be asked).

**Not covered:** Ethernet-level attacks outside what `scripts/forge` sends (see [security-model.md](security-model.md)).

**In the console** the same test is the Test tab of the Firewall page (`/firewall?tab=test`, also `/firewall/test`;
the extension `firewall_test`, operator in the project). From and To take a jail (`jail:web`, completed from the project's
jails), one address or `host`; Protocol and Port (or an ICMP type) follow the command; the IP family can be chosen when
both ends have both. **Run the test** (or Enter) asks; nothing runs while you type. The answer is drawn as a flow, From,
what the source sends, what reaches the destination, To, each step with an icon and the word PASS, BLOCK or CANNOT SAY,
the anchor, what decided in words (the policy and the number of the rule with its comment, a project policy, the managed
policy of an app, the isolation of projects, the default of the anchor), and Details with the rendered pf line and the
rules passed over (a disabled rule, the allow rules a deny rule won over). The question is kept in the address of the
page (`from`, `to`, `proto`, `port`, `type`, `family`), so a link asks it again, and the equivalent
`jailops firewall test` line is under the form with a Copy button. The answer is the server's: the console evaluates
no rule.
