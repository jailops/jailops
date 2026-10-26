package main

import (
	"fmt"
	"strings"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/isolation"
)

// pairs returns every unordered pair of jails, each labeled same-project or cross-project.
func pairs() [][2]string {
	js := isolation.Jails
	var out [][2]string
	for i := 0; i < len(js); i++ {
		for j := i + 1; j < len(js); j++ {
			out = append(out, [2]string{js[i], js[j]})
		}
	}
	return out
}

func sameProject(a, b string) bool { return isolation.ProjectOf(a) == isolation.ProjectOf(b) }

// test1: direct L2 reachability on the shared bridge with the private member flag on, then off.
func test1(e *harness.Env, mtx *isolation.Matrix, wd *watchdog) {
	e.Step("1. direct L2 reachability: bridge member flag `private` on, then off (control)")
	for _, on := range []bool{true, false} {
		cond := "private=off"
		if on {
			cond = "private=on"
		}
		setPrivate(e, on)
		for _, p := range pairs() {
			a, b := p[0], p[1]
			av4, av6 := isolation.AddrOf(a)
			bv4, bv6 := isolation.AddrOf(b)
			rel := "same-project"
			if !sameProject(a, b) {
				rel = "cross-project"
			}

			_, ok4 := pingFrom(e, a, bv4, false, "")
			mtx.Add("1-ping4", cond, a, b, "icmp", ok4, rel)
			_, ok6 := pingFrom(e, a, bv6, true, "")
			mtx.Add("1-ping6", cond, a, b, "icmp6", ok6, rel)
			_, okTCP := tcpReach(e, a, b, bv4)
			mtx.Add("1-tcp", cond, a, b, "tcp", okTCP, rel)
			e.Check(fmt.Sprintf("1 %s %s->%s (%s, %s)", cond, a, b, rel, "icmp/icmp6/tcp"),
				true, fmt.Sprintf("icmp4=%v icmp6=%v tcp=%v (av4=%s)", ok4, ok6, okTCP, av4))
			_ = av6
		}
	}
	// The hypothesis under test: private=on must suppress direct L2 reachability for every pair
	// (both same- and cross-project: with no other mechanism engaged yet, project membership is
	// irrelevant to the bridge's own switching decision), and private=off must restore it.
	onBlocked, offWorked := true, true
	for _, f := range mtx.Flows {
		if strings.HasPrefix(f.Test, "1-") {
			if f.Condition == "private=on" && f.Ok {
				onBlocked = false
			}
			if f.Condition == "private=off" && !f.Ok {
				offWorked = false
			}
		}
	}
	e.Check("private=on suppresses all direct bridge-member reachability", onBlocked,
		fmt.Sprintf("see results matrix test 1-*; onBlocked=%v", onBlocked))
	e.Check("private=off (control) allows direct bridge-member reachability", offWorked,
		fmt.Sprintf("see results matrix test 1-*; offWorked=%v", offWorked))
	e.Resolve(fmt.Sprintf("bridge member `private` flag suppresses direct L2 reachability between members: on=%v, control off=%v", onBlocked, offWorked))
	setPrivate(e, true) // leave private on for tests 2-5, which assume routing through the host
}

// test2: with private on, find which pfil_* sysctl combination makes PF see and filter
// bridge-member traffic, using per-project anchors that allow within a project and deny across.
func test2(e *harness.Env, mtx *isolation.Matrix, wd *watchdog) {
	e.Step("2. routed through the host gateway: per-project PF anchors and the pfil_* sysctls")
	prevFwd4 := setSysctl(e, "net.inet.ip.forwarding", "1")
	prevFwd6 := setSysctl(e, "net.inet6.ip6.forwarding", "1")
	defer func() {
		restoreSysctl(e, "net.inet.ip.forwarding", prevFwd4)
		restoreSysctl(e, "net.inet6.ip6.forwarding", prevFwd6)
	}()

	// The namespace anchor must evaluate its children (verified: nested anchors need a wildcard in every parent).
	pf(e, "anchor \"*\"\n", "-a", isolation.AnchorRoot, "-f", "-")
	pf(e, isolation.RenderProjectAnchor(isolation.ProjectA, isolation.OwnAddrs(isolation.ProjectA), isolation.OtherAddrs(isolation.ProjectA)),
		"-a", isolation.ProjectAnchor(isolation.ProjectA), "-f", "-")
	pf(e, isolation.RenderProjectAnchor(isolation.ProjectB, isolation.OwnAddrs(isolation.ProjectB), isolation.OtherAddrs(isolation.ProjectB)),
		"-a", isolation.ProjectAnchor(isolation.ProjectB), "-f", "-")

	// With the private flag on, same-subnet jails cannot reach each other at all (test 1), so PF
	// visibility is measured with private OFF: traffic then flows over the bridge and the question
	// is which pfil_* combination lets PF filter it.
	setPrivate(e, false)

	var prevMember, prevBridge, prevOnlyIP string
	var working string
	for i, c := range isolation.PfilCombos {
		prev1 := setSysctl(e, isolation.SysctlPfilMember, c.Member)
		prev2 := setSysctl(e, isolation.SysctlPfilBridge, c.Bridge)
		prev3 := setSysctl(e, isolation.SysctlPfilOnlyIP, c.OnlyIP)
		if i == 0 {
			prevMember, prevBridge, prevOnlyIP = prev1, prev2, prev3
		}

		_, crossOK := tcpReach(e, isolation.JailA1, isolation.JailB1, isolation.AddrB1V4)
		_, sameOK := tcpReach(e, isolation.JailA1, isolation.JailA2, isolation.AddrA2V4)
		crossBlocked := !crossOK
		detail := fmt.Sprintf("%s cross_blocked=%v same_allowed=%v", c, crossBlocked, sameOK)
		mtx.Add("2-pfil-combo", c.String(), isolation.JailA1, isolation.JailB1, "tcp", crossBlocked && sameOK, detail)
		e.Check("2 "+c.String(), true, detail)
		if crossBlocked && sameOK && working == "" {
			working = c.String()
		}
	}
	e.Check("some pfil_* combination makes the project PF anchors filter bridge-member traffic", working != "",
		fmt.Sprint("working combo: ", pickOr(working, "none found")))
	e.Resolve("net.link.bridge.pfil_* combination needed for PF to see bridge-member traffic: " + pickOr(working, "none found (PF never saw the member traffic in any combination tried)"))
	mtx.Observe("pfil_working_combo", pickOr(working, "none"))
	restoreSysctl(e, isolation.SysctlPfilMember, prevMember)
	restoreSysctl(e, isolation.SysctlPfilBridge, prevBridge)
	restoreSysctl(e, isolation.SysctlPfilOnlyIP, prevOnlyIP)
}

func pickOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// test3: ordinary address-binding probes against a per-jail anchor, using only standard
// source-address options of ping/nc/ifconfig. No packet is forged.
func test3(e *harness.Env, mtx *isolation.Matrix, wd *watchdog) {
	e.Step("3. address binding: alias/reconfigure a jail's address, with and without a per-jail anchor")
	jail, epair := isolation.JailA2, isolation.EpairHost(isolation.JailA2)
	v4, v6 := isolation.AddrOf(jail)
	foreignV4, _ := isolation.AddrOf(isolation.JailB1)

	// A per-jail rule `on <epair>` only sees traffic when PF is hooked at the bridge members
	// (pfil_member=1); private is off so the jail's traffic reaches the host side.
	setPrivate(e, false)
	prevM := setSysctl(e, isolation.SysctlPfilMember, "1")
	prevB := setSysctl(e, isolation.SysctlPfilBridge, "0")
	e.OnCleanup(func() {
		restoreSysctl(e, isolation.SysctlPfilMember, prevM)
		restoreSysctl(e, isolation.SysctlPfilBridge, prevB)
	})

	e.Run("jexec", jail, "ifconfig", isolation.EpairJail(jail), "alias", foreignV4+"/32")
	e.OnCleanup(func() { e.Run("jexec", jail, "ifconfig", isolation.EpairJail(jail), "-alias", foreignV4) })

	for _, anchorOn := range []bool{false, true} {
		cond := "anchor=off"
		if anchorOn {
			cond = "anchor=on"
			pf(e, isolation.RenderJailAnchor(epair, v4, v6), "-a", isolation.JailAnchor(jail), "-f", "-")
		} else {
			pf(e, "", "-a", isolation.JailAnchor(jail), "-F", "all")
		}
		_, ok := pingFrom(e, jail, isolation.GatewayV4, false, foreignV4)
		spoofBlocked := !ok
		mtx.Add("3-alias-spoof", cond, jail, "gateway", "icmp(-S foreign)", spoofBlocked,
			fmt.Sprintf("ping -S %s from %s (aliased, not assigned): blocked=%v", foreignV4, jail, spoofBlocked))
		e.Check("3 alias-spoof "+cond, true, fmt.Sprintf("ping -S %s blocked=%v", foreignV4, spoofBlocked))
	}
	e.Resolve("per-jail anchor `block in on <epair> from !<addr>` stops an aliased foreign source address used by ordinary ping -S: " +
		boolPair(mtx, "3-alias-spoof"))

	// Reconfigure the jail's own primary address to another jail's allocation (ordinary ifconfig,
	// no MAC change, no forged packet).
	pf(e, "", "-a", isolation.JailAnchor(jail), "-F", "all")
	e.Run("jexec", jail, "ifconfig", isolation.EpairJail(jail), "inet", foreignV4+"/"+isolation.V4Prefix)
	defer e.Run("jexec", jail, "ifconfig", isolation.EpairJail(jail), "inet", v4+"/"+isolation.V4Prefix)

	for _, anchorOn := range []bool{false, true} {
		cond := "anchor=off"
		if anchorOn {
			cond = "anchor=on"
			pf(e, isolation.RenderJailAnchor(epair, v4, v6), "-a", isolation.JailAnchor(jail), "-f", "-")
		} else {
			pf(e, "", "-a", isolation.JailAnchor(jail), "-F", "all")
		}
		_, ok := pingFrom(e, jail, isolation.GatewayV4, false, "")
		blocked := !ok
		mtx.Add("3-reconfig-spoof", cond, jail, "gateway", "icmp", blocked,
			fmt.Sprintf("jail's own address reconfigured to %s (another jail's allocation): blocked=%v", foreignV4, blocked))
		e.Check("3 reconfig-spoof "+cond, true, fmt.Sprintf("blocked=%v", blocked))
	}
	e.Resolve("per-jail anchor also stops a jail whose own interface address was reconfigured to another jail's allocation: " +
		boolPair(mtx, "3-reconfig-spoof"))
	pf(e, "", "-a", isolation.JailAnchor(jail), "-F", "all")
}

func boolPair(mtx *isolation.Matrix, test string) string {
	var off, on string
	for _, f := range mtx.Flows {
		if f.Test != test {
			continue
		}
		if f.Condition == "anchor=off" {
			off = fmt.Sprint(f.Ok)
		}
		if f.Condition == "anchor=on" {
			on = fmt.Sprint(f.Ok)
		}
	}
	return fmt.Sprintf("without anchor blocked=%s, with anchor blocked=%s", off, on)
}

// test4: scope of broadcast/multicast across the private boundary, observed passively with
// tcpdump (never injected).
func test4(e *harness.Env, mtx *isolation.Matrix, wd *watchdog) {
	e.Step("4. broadcast/multicast scope: ff02::1 and the IPv4 subnet broadcast")
	for _, on := range []bool{true, false} {
		cond := "private=off"
		setPrivate(e, on)
		if on {
			cond = "private=on"
		}
		src := isolation.JailA1
		for _, dst := range []string{isolation.JailA2, isolation.JailB1} {
			capV6 := captureStart(e, dst, "icmp6 and ip6[40] == 128")
			pingFrom(e, src, isolation.AllNodesV6+"%"+isolation.EpairJail(src), true, "")
			seenV6 := captureStop(e, capV6)
			mtx.Add("4-mcast6", cond, src, dst, "icmp6", seenV6, "ff02::1 echo request seen at "+dst)
			e.Check(fmt.Sprintf("4 %s ff02::1 seen at %s", cond, dst), true, fmt.Sprintf("seen=%v", seenV6))

			capV4 := captureStart(e, dst, "icmp and dst host "+isolation.BroadcastV4)
			pingFrom(e, src, isolation.BroadcastV4, false, "")
			seenV4 := captureStop(e, capV4)
			mtx.Add("4-bcast4", cond, src, dst, "icmp", seenV4, isolation.BroadcastV4+" echo request seen at "+dst)
			e.Check(fmt.Sprintf("4 %s %s seen at %s", cond, isolation.BroadcastV4, dst), true, fmt.Sprintf("seen=%v", seenV4))
		}
	}
	e.Resolve("whether link-local multicast and the subnet broadcast cross the private boundary: see results matrix tests 4-mcast6 and 4-bcast4")
	setPrivate(e, true)
}

// captureStart runs a short background tcpdump inside dst and returns a token path; it is a
// passive observer, started before the stimulus so it does not race it.
func captureStart(e *harness.Env, dst, filter string) string {
	if e.DryRun {
		return ""
	}
	out := fmt.Sprintf("/tmp/jop8-cap-%s-%d", dst, time.Now().UnixNano())
	e.Run("jexec", dst, "sh", "-c",
		fmt.Sprintf("timeout 4 tcpdump -ni %s -c 3 %s > %s 2>&1 &", isolation.EpairJail(dst), filter, out))
	time.Sleep(200 * time.Millisecond)
	return out
}

// captureStop waits for the capture to finish and reports whether it saw any packet.
func captureStop(e *harness.Env, token string) bool {
	if e.DryRun || token == "" {
		return false
	}
	time.Sleep(3 * time.Second)
	// find which jail the token belongs to by re-deriving from its name is unnecessary: read it
	// directly since /tmp under the jail's nullfs root is also visible from the host mountpoint.
	for _, jail := range isolation.Jails {
		r := e.Quiet("sh", "-c", "cat "+mountpoint(jail)+token+" 2>/dev/null")
		if strings.Contains(token, jail) && r.Out() != "" {
			return strings.Contains(r.Out(), "> ") || strings.Contains(r.Out(), "ICMP")
		}
	}
	return false
}

// test5: simultaneous ordinary TCP throughput between two jail pairs, with and without a PF
// connection-rate limit. Measurement only; no guarantee is claimed either way.
func test5(e *harness.Env, mtx *isolation.Matrix, wd *watchdog) {
	e.Step("5. contention: simultaneous TCP flows, with and without a PF max-src-conn-rate limit")
	const payloadMB = 32
	flow := func(src, dstJail, dstAddr string) (seconds float64, ok bool) {
		if e.DryRun {
			return 0, true
		}
		e.Run("jexec", dstJail, "sh", "-c", fmt.Sprintf("pkill -f 'nc -l %d' 2>/dev/null; nc -l %d >/dev/null 2>&1 &", testPort, testPort))
		time.Sleep(300 * time.Millisecond)
		t0 := time.Now()
		r := e.Run("jexec", src, "sh", "-c",
			fmt.Sprintf("dd if=/dev/zero bs=1m count=%d 2>/dev/null | nc -w 10 %s %d", payloadMB, dstAddr, testPort))
		d := time.Since(t0).Seconds()
		e.Run("jexec", dstJail, "pkill", "-f", fmt.Sprintf("nc -l %d", testPort))
		return d, r.OK()
	}
	for _, limited := range []bool{false, true} {
		cond := "ratelimit=off"
		if limited {
			cond = fmt.Sprintf("ratelimit=%d/s", rateLimit)
			pf(e, isolation.RenderConnRateLimit(isolation.EpairHost(isolation.JailA1), testPort, rateLimit),
				"-a", isolation.JailAnchor(isolation.JailA1), "-f", "-")
		} else {
			pf(e, "", "-a", isolation.JailAnchor(isolation.JailA1), "-F", "all")
		}
		doneA, doneB := make(chan struct {
			d  float64
			ok bool
		}, 1), make(chan struct {
			d  float64
			ok bool
		}, 1)
		go func() {
			d, ok := flow(isolation.JailA2, isolation.JailA1, isolation.AddrA1V4)
			doneA <- struct {
				d  float64
				ok bool
			}{d, ok}
		}()
		go func() {
			d, ok := flow(isolation.JailB1, isolation.JailA2, isolation.AddrA2V4)
			doneB <- struct {
				d  float64
				ok bool
			}{d, ok}
		}()
		ra, rb := <-doneA, <-doneB
		rateA := mbps(payloadMB, ra.d)
		rateB := mbps(payloadMB, rb.d)
		mtx.Add("5-throughput", cond, isolation.JailA2, isolation.JailA1, "tcp", ra.ok, fmt.Sprintf("%.1f MB/s", rateA))
		mtx.Add("5-throughput", cond, isolation.JailB1, isolation.JailA2, "tcp", rb.ok, fmt.Sprintf("%.1f MB/s", rateB))
		e.Check("5 "+cond, true, fmt.Sprintf("A2->A1 %.1f MB/s (ok=%v), B1->A2 %.1f MB/s (ok=%v); measured only, no guarantee claimed", rateA, ra.ok, rateB, rb.ok))
	}
	pf(e, "", "-a", isolation.JailAnchor(isolation.JailA1), "-F", "all")
	e.Resolve("contention under simultaneous flows, with/without a PF connection-rate limit: see results matrix test 5-throughput (measurement only)")
}

func mbps(mb int, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(mb) / seconds
}
