// Command p5-ndp is JailOps prototype 5: NDP proxy on a switched IPv6 /64 uplink.
//
// HONESTY NOTE: this program was written on Linux without access to FreeBSD. It has been
// compiled for freebsd/amd64 and freebsd/arm64 and its pure logic is unit tested, but no
// FreeBSD command, ioctl or kernel behaviour used here has been observed by the author.
// Every such assumption is marked UNVERIFIED: and the run reports which ones it settled.
//
// SAFETY: the primary uplink configuration is never modified (its addresses, routes, flags
// and sysctls stay as they are). The program creates only: bridge jopbr0, epair jope0a/jope0b,
// jail jop5, one host /128 route and one proxy neighbor entry for the --jail-addr, and,
// only with --allow-forwarding-sysctl, net.inet6.ip6.forwarding=1 (restored afterwards). Part D
// additionally adds the uplink to jopbr0 and needs two explicit flags. A watchdog (in process and
// as a detached helper process) restores everything when the connectivity probe fails or the
// hard deadline passes. Every command goes through an allow-list (checkCmd).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/ndp"
)

type config struct {
	uplink, jailAddr, peer, peerSSH, peerDev       string
	probeTarget, state, parts, mcastCmd, ucastCmd  string
	pingCmd                                        string
	watchdog, failSecs, listen, dSeconds           int
	partD, acceptLoss, allowFwd, cPromisc, noGoRsp bool
	printRecipe                                    bool
	analyze, hostMAC                               string
	helper                                         bool
}

var cfg config

const (
	defMcast = "ip neigh flush to {addr} dev {dev}; ping -6 -c 3 -W 2 {addr}"
	defUcast = "ip neigh replace {addr} lladdr {mac} dev {dev} nud stale; ping -6 -c 10 -i 1 -W 1 {addr}"
	defPing  = "ping -6 -c 3 -W 2 {addr}"
)

func registerFlags(e *harness.Env, fs *flag.FlagSet) {
	fs.StringVar(&cfg.uplink, "uplink", "", "uplink interface (default: interface of the IPv6 default route). Never reconfigured.")
	fs.StringVar(&cfg.jailAddr, "jail-addr", "", "UNUSED global address inside the uplink /64 to use as the jail address (required)")
	fs.StringVar(&cfg.peer, "peer", "", "IPv6 address of a second host on the /64 (informational, used in the report)")
	fs.StringVar(&cfg.peerSSH, "peer-ssh", "", "user@host: ssh target of the second host (key auth, root or sudo-less ip/ping); drives stimulus and acceptance tests 3 and 4")
	fs.StringVar(&cfg.peerDev, "peer-dev", "eth0", "interface name on the peer for {dev}")
	fs.StringVar(&cfg.probeTarget, "probe-target", "", "watchdog probe target (default: the IPv6 default gateway, with zone)")
	fs.StringVar(&cfg.state, "state", "/var/run/jop5-ndp.state", "state file shared with the watchdog helper and --cleanup")
	fs.StringVar(&cfg.parts, "parts", "A,B,C", "parts to run: any of A,B,C,D (D needs the two extra flags)")
	fs.StringVar(&cfg.mcastCmd, "peer-mcast-cmd", defMcast, "command run on the peer to cause multicast NS ({addr} {mac} {dev})")
	fs.StringVar(&cfg.ucastCmd, "peer-ucast-cmd", defUcast, "command run on the peer to cause unicast NUD NS to the host MAC")
	fs.StringVar(&cfg.pingCmd, "peer-ping-cmd", defPing, "command run on the peer whose exit status says the jail address is reachable")
	fs.IntVar(&cfg.watchdog, "watchdog", 900, "hard deadline in seconds after which everything is restored")
	fs.IntVar(&cfg.failSecs, "probe-fail", 10, "restore when the connectivity probe has failed for this many seconds")
	fs.IntVar(&cfg.listen, "listen", 15, "seconds per observation phase (unicast phases use at least 12)")
	fs.IntVar(&cfg.dSeconds, "d-seconds", 20, "Part D: seconds the uplink stays inside the bridge (max 120)")
	fs.BoolVar(&cfg.partD, "part-d-uplink-in-bridge", false, "enable Part D (adds the UPLINK to jopbr0; may cut connectivity; watchdog restores)")
	fs.BoolVar(&cfg.acceptLoss, "i-accept-losing-connectivity", false, "second confirmation required by Part D")
	fs.BoolVar(&cfg.allowFwd, "allow-forwarding-sysctl", false, "allow setting net.inet6.ip6.forwarding=1 when it is 0 (restored at the end). Forwarding on may stop RA-learned routes on the host")
	fs.BoolVar(&cfg.cPromisc, "c-promisc", false, "Part C: run the proxy listener in promiscuous mode (use if hypothesis 1 or 2 was refuted)")
	fs.BoolVar(&cfg.noGoRsp, "no-go-responder", false, "Part C: do not run the Go responder; rely on the kernel proxy entry (ndp -s ... proxy) instead")
	fs.StringVar(&cfg.hostMAC, "host-mac", "", "override the advertised host MAC (default: ether of the uplink)")
	fs.BoolVar(&cfg.printRecipe, "print-recipe", false, "print the tcpdump capture and stimulus recipe and exit")
	fs.StringVar(&cfg.analyze, "analyze-tcpdump", "", "summarise a saved `tcpdump -n -e` text capture for --jail-addr and exit")
	fs.BoolVar(&cfg.helper, "watchdog-helper", false, "internal: run as the detached watchdog helper")
}

func hasArg(name string) bool {
	for _, a := range os.Args[1:] {
		if a == "--"+name || a == "-"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

func argVal(name, def string) string {
	for i, a := range os.Args[1:] {
		for _, p := range []string{"--", "-"} {
			if a == p+name && i+2 < len(os.Args) {
				return os.Args[i+2]
			}
			if strings.HasPrefix(a, p+name+"=") {
				return a[len(p+name)+1:]
			}
		}
	}
	return def
}

func main() {
	switch {
	case hasArg("print-recipe"):
		fmt.Print(ndp.Recipe)
		return
	case hasArg("watchdog-helper"):
		os.Exit(helperMain(argVal("state", "/var/run/jop5-ndp.state")))
	case hasArg("analyze-tcpdump"):
		os.Exit(analyzeMain(argVal("analyze-tcpdump", ""), argVal("jail-addr", ""), argVal("host-mac", "")))
	}
	harness.Exit(spec())
}

func spec() harness.Spec {
	return harness.Spec{
		N:       5,
		Name:    "ndp",
		NoPool:  true,
		Summary: "NDP proxy on a switched IPv6 /64: kernel proxy, BPF visibility, routing acceptance tests, bridge-membership failure.",
		Plan: []string{
			"Never change the uplink's own configuration. Only create: bridge jopbr0, epair jope0a/jope0b, vnet jail jop5, a host /128 route and one ndp proxy entry for --jail-addr",
			"Arm the watchdog first: in-process monitor plus a detached helper that restores everything if the probe (ping to --probe-target) fails for --probe-fail seconds or after --watchdog seconds",
			"Part A: ndp -s <jail-addr> <host-mac> proxy; observe with BPF (no promisc) whether the kernel answers multicast NS and unicast NUD NS",
			"Part B: BPF listener on the uplink in 4 combinations (promisc off/on x solicited-node group joined no/yes) with a Go responder advertising the host MAC; report which NS are visible; evaluate the two hypotheses",
			"Part C: lab bridge+epair+jail; acceptance tests 1-5 of design section 5 (route get, no host NS on uplink, reachability after NUD, reply source and PF antispoof, host connectivity probe)",
			"Part D (only with --part-d-uplink-in-bridge --i-accept-losing-connectivity): add the uplink to jopbr0 for --d-seconds, watch for flooding and loss of connectivity, then restore at once",
			"Tear everything down at the end; --cleanup does the same from the state file",
		},
		Flags:   registerFlags,
		Run:     run,
		Cleanup: cleanup,
	}
}
