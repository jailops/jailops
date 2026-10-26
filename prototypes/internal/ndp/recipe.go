package ndp

// Recipe is the operator-facing capture and stimulus recipe (--print-recipe).
// UNVERIFIED: every command here is FreeBSD/Linux knowledge, none was run by the author.
const Recipe = `JailOps prototype 5: capture and stimulus recipe
===============================================

Variables:  UP=<uplink, e.g. igb0>   J=<jail address, e.g. 2001:db8:1::5000>   HOSTMAC=<ifconfig $UP | grep ether>

1. Capture on the HOST uplink. -p is essential: without it tcpdump sets promiscuous mode and
   destroys the very thing prototype 5 measures. -e prints Ethernet addresses (who was addressed).

   tcpdump -p -n -e -l -i $UP -w /var/tmp/p5-uplink.pcap 'icmp6 and (ip6[40] == 135 or ip6[40] == 136)'
   tcpdump -p -n -e -l -i $UP 'icmp6 and (ip6[40] == 135 or ip6[40] == 136)' | tee /var/tmp/p5-uplink.txt

   Also capture on the bridge to see what the lab does there:
   tcpdump -p -n -e -l -i jopbr0 icmp6

   Check the interface never shows PROMISC while measuring:
   ifconfig $UP | head -1        (flags must not contain PROMISC)

2. Stimulus from a SECOND host on the same /64 (Linux, iproute2). Replace DEV, J, HOSTMAC.

   Multicast resolution (peer sends NS to the solicited-node group):
     ip neigh flush to $J dev DEV ; ping -6 -c 1 -W 2 $J

   Unicast NUD (peer holds the host MAC for J and goes STALE -> DELAY -> PROBE, sending
   unicast NS after about 5 seconds):
     ip neigh replace $J lladdr $HOSTMAC dev DEV nud stale ; ping -6 -c 8 -i 1 -W 1 $J
     ip -6 neigh show $J        (watch the state change)

   Without iproute2:  ndisc6 -1 -m $J DEV   (multicast NS)   or   ndp -d $J on a FreeBSD peer.

3. Analyse a saved text capture without running anything privileged:
   p5-ndp --analyze-tcpdump /var/tmp/p5-uplink.txt --jail-addr $J --host-mac $HOSTMAC

4. After the run: ndp -an | grep -i "$J"; netstat -rn -f inet6 | grep jop; ifconfig -l | tr ' ' '\n' | grep jop
   Anything printed with the jop prefix that --cleanup did not remove: report it.
`
