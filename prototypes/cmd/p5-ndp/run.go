package main

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/ndp"
)

const conv = "(convention: ok=true means the experiment ran and the OBSERVED/CONFIRMED/REFUTED wording is the finding; ok=false means the experiment could not run or an acceptance test failed)"

func wantPart(p string) bool {
	for _, x := range strings.Split(strings.ToUpper(cfg.parts), ",") {
		if strings.TrimSpace(x) == p {
			return true
		}
	}
	return false
}

func newLab(e *harness.Env) *Lab {
	l := &Lab{e: e}
	if cfg.jailAddr == "" {
		e.Abort("--jail-addr is required (an UNUSED address inside the uplink /64)")
	}
	a, err := netip.ParseAddr(cfg.jailAddr)
	if err != nil || !a.Is6() || a.Zone() != "" || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsLoopback() || a.IsUnspecified() {
		e.Abort("--jail-addr %q must be a global unicast IPv6 address", cfg.jailAddr)
	}
	l.jail = a
	l.partD = cfg.partD && cfg.acceptLoss
	if wantPart("D") && !(cfg.partD && cfg.acceptLoss) {
		e.Abort("part D needs both --part-d-uplink-in-bridge and --i-accept-losing-connectivity")
	}
	if cfg.dSeconds < 5 || cfg.dSeconds > 120 {
		e.Abort("--d-seconds must be 5..120")
	}
	if cfg.peerSSH != "" && !reSSH.MatchString(cfg.peerSSH) {
		e.Abort("--peer-ssh %q is not a plain [user@]host", cfg.peerSSH)
	}
	if !reDev.MatchString(cfg.peerDev) {
		e.Abort("--peer-dev %q invalid", cfg.peerDev)
	}
	return l
}

func (l *Lab) discover() {
	e := l.e
	e.Step("Discover the uplink (read-only)")
	l.uplink = cfg.uplink
	rg := ndp.RouteGet{}
	if r := l.quietNoCheck("route", "-n", "get", "-inet6", "default"); r.Err == nil {
		rg = ndp.ParseRouteGet(r.Stdout)
	}
	if l.uplink == "" {
		l.uplink = rg.Interface
	}
	l.probe = cfg.probeTarget
	if l.probe == "" {
		l.probe = rg.Gateway
	}
	if e.DryRun {
		if l.uplink == "" {
			l.uplink = "<uplink>"
		}
		if l.probe == "" {
			l.probe = "<default-gateway>"
		}
		l.hostMAC = ndp.MAC{}
		e.Note("(dry run) uplink=%s probe=%s jail-addr=%s", l.uplink, l.probe, l.jail)
		return
	}
	if l.uplink == "" || l.probe == "" {
		e.Abort("cannot find the IPv6 default route; pass --uplink and --probe-target")
	}
	info := ndp.ParseIfconfig(l.quiet("ifconfig", l.uplink).Stdout)
	l.upInfo = info
	if info.Name == "" {
		e.Abort("ifconfig %s gave nothing", l.uplink)
	}
	l.hostMAC = info.Ether
	if cfg.hostMAC != "" {
		m, err := ndp.ParseMAC(cfg.hostMAC)
		if err != nil {
			e.Abort("%v", err)
		}
		l.hostMAC = m
	}
	if l.hostMAC.IsZero() {
		e.Abort("no ether address on %s", l.uplink)
	}
	e.Note("uplink=%s hostMAC=%s probe-target=%s jail-addr=%s", l.uplink, l.hostMAC, l.probe, l.jail)
	if info.HasFlag("PROMISC") {
		e.Abort("%s is already PROMISC (tcpdump or a bridge?). Promiscuous-mode experiments would be invalid; stop the other listener first", l.uplink)
	}
	inPrefix := false
	for _, g := range info.Global() {
		if g.Addr == l.jail {
			e.Abort("jail-addr %s is configured on the host", l.jail)
		}
		if ndp.SolicitedNode(g.Addr) == ndp.SolicitedNode(l.jail) {
			e.Abort("jail-addr %s shares the solicited-node group with host address %s; choose another so the group join experiment is clean", l.jail, g.Addr)
		}
		if p, err := g.Addr.Prefix(g.PrefixLen); err == nil && p.Contains(l.jail) {
			inPrefix = true
		}
	}
	if !inPrefix {
		e.Abort("jail-addr %s is not inside any global prefix configured on %s", l.jail, l.uplink)
	}
	if len(ndp.FindNdp(ndp.ParseNdpAn(l.quiet("ndp", "-an").Stdout), l.jail)) > 0 {
		e.Abort("jail-addr %s already has a neighbor entry: it is in use", l.jail)
	}
	if ifs := strings.Fields(l.quiet("ifconfig", "-l").Out()); contains(ifs, bridgeName) || contains(ifs, epairA) {
		e.Abort("%s or %s already exists: run --cleanup", bridgeName, epairA)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// quietNoCheck is used only before the uplink is known (route get on default).
func (l *Lab) quietNoCheck(argv ...string) harness.Result { return l.e.Quiet(argv...) }

func run(e *harness.Env) {
	e.Require("ifconfig", "route", "ndp", "ping", "pfctl")
	l := newLab(e)
	l.discover()
	est := 0
	if wantPart("A") {
		est += 3*cfg.listen + 20
	}
	if wantPart("B") {
		est += 4 * (cfg.listen + max(cfg.listen, 12))
	}
	if wantPart("C") {
		est += 90
	}
	if wantPart("D") {
		est += cfg.dSeconds + 30
	}
	e.Note("estimated duration about %ds; watchdog hard deadline %ds", est, cfg.watchdog)
	if est > cfg.watchdog {
		e.Abort("--watchdog %d is shorter than the estimated run %d: raise it", cfg.watchdog, est)
	}
	if !wantPart("A") && !wantPart("B") && !wantPart("C") && !wantPart("D") {
		e.Abort("--parts selects nothing")
	}
	l.arm()
	if wantPart("A") {
		l.partA()
	}
	if wantPart("B") {
		l.partB()
	}
	if wantPart("C") {
		l.partC()
	}
	if wantPart("D") {
		l.partD_()
	}
	if !wantPart("C") {
		l.statsCheck()
	}
}

func (l *Lab) statsCheck() {
	e := l.e
	if e.DryRun {
		e.Check("C5 host connectivity unchanged", true, "")
		return
	}
	oks, fails, maxRun := l.probeStats()
	e.Check("C5 host connectivity unchanged (continuous probe of "+l.probe+")", fails == 0 && oks > 0,
		fmt.Sprintf("probes ok=%d failed=%d longest failure run=%d", oks, fails, maxRun))
	e.Resolve(fmt.Sprintf("host connectivity during the experiments: %d failed probes of %d", fails, oks+fails))
}

// ---- stimulus and observation ------------------------------------------------------------

type obsOpts struct {
	name    string
	promisc bool
	join    bool
	respond bool
	stim    string // "none", "mcast", "ucast", "host", "peerping"
	window  time.Duration
	types   []uint8
	noGuard bool
}

type obsRes struct {
	T           ndp.Tally
	PromiscFlag bool
	HostNSAll   int
	Echo        []*ndp.Frame
	StimOK      bool // stimulus command exit status 0
	StimRan     bool
	Sent        int
	Total       int
}

func (l *Lab) stimArgv(kind string) []string {
	switch kind {
	case "host":
		return []string{"ping", "-6", "-c", "3", "-t", "6", l.jail.String()}
	}
	tmpl := map[string]string{"mcast": cfg.mcastCmd, "ucast": cfg.ucastCmd, "peerping": cfg.pingCmd}[kind]
	if tmpl == "" || cfg.peerSSH == "" {
		return nil
	}
	return sshArgv(cfg.peerSSH, expand(tmpl, l.jail, l.hostMAC, cfg.peerDev))
}

// observe opens the (optionally promiscuous) listener and group join, starts the stimulus, and
// tallies for the window.
func (l *Lab) observe(o obsOpts) obsRes {
	e := l.e
	var r obsRes
	types := o.types
	if len(types) == 0 {
		types = []uint8{ndp.TypeNS, ndp.TypeNA}
	}
	e.Step("%s: promisc=%v join-solicited-node=%v responder=%v stimulus=%s window=%s", o.name, o.promisc, o.join, o.respond, o.stim, o.window)
	argv := l.stimArgv(o.stim)
	if e.DryRun {
		e.Note("(dry run) would open BPF on %s (BIOCPROMISC only if promisc), join %s if requested, run stimulus %q and tally for %s",
			l.uplink, ndp.SolicitedNode(l.jail), strings.Join(argv, " "), o.window)
		return r
	}
	var j *ndp.Joined
	if o.join {
		var err error
		j, err = ndp.JoinSolicitedNode(l.uplink, []netip.Addr{l.jail})
		if err != nil {
			e.Abort("%v", err)
		}
		defer j.Close()
		g := ndp.SolicitedNode(l.jail).String()
		out := l.quiet("ifmcstat", "-i", l.uplink).Stdout
		e.Note("joined %s; visible in ifmcstat: %v", g, strings.Contains(out, g))
	}
	ls, err := ndp.OpenListener(ndp.Options{Iface: l.uplink, Promisc: o.promisc, Types: types})
	if err != nil {
		e.Abort("%v", err)
	}
	defer ls.Close()
	var wr *ndp.Listener
	if o.respond {
		wr, err = ndp.OpenListener(ndp.Options{Iface: l.uplink, Types: []uint8{ndp.TypeEchoRequest}, WriteTypes: []uint8{ndp.TypeNA}})
		if err != nil {
			e.Abort("%v", err)
		}
		defer wr.Close()
	}
	r.PromiscFlag = ndp.ParseIfconfig(l.quiet("ifconfig", l.uplink).Stdout).HasFlag("PROMISC")
	if r.PromiscFlag != o.promisc {
		e.Note("WARNING: PROMISC flag on %s is %v but this phase asked for %v: another process holds the uplink promiscuous; this phase is not valid for the hypotheses", l.uplink, r.PromiscFlag, o.promisc)
	}
	type stimResult struct {
		out string
		err error
	}
	ch := make(chan stimResult, 1)
	if argv != nil {
		e.Note("stimulus: %s", strings.Join(argv, " "))
		r.StimRan = true
		go func() {
			if checkCmd(argv, l.uplink, l.jail.String(), l.partD) != nil {
				ch <- stimResult{err: fmt.Errorf("refused")}
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
			ch <- stimResult{string(out), err}
		}()
	} else if o.stim != "none" {
		e.Note("OPERATOR ACTION within %s: on a second host on the same /64 run (see --print-recipe):", o.window)
		switch o.stim {
		case "mcast":
			e.Note("  %s", expand(defMcast, l.jail, l.hostMAC, "<DEV>"))
		case "ucast":
			e.Note("  %s", expand(defUcast, l.jail, l.hostMAC, "<DEV>"))
		case "peerping":
			e.Note("  %s", expand(defPing, l.jail, l.hostMAC, "<DEV>"))
		}
	}
	mine := map[netip.Addr]bool{l.jail: true}
	end := time.Now().Add(o.window)
	for time.Now().Before(end) {
		if !o.noGuard {
			l.guard()
		}
		frames, err := ls.ReadFrames()
		if err != nil {
			e.Abort("bpf read: %v", err)
		}
		for _, raw := range frames {
			r.Total++
			f, err := ndp.ParseFrame(raw)
			if err != nil {
				continue
			}
			if f.IsNS() && f.EthSrc == l.hostMAC {
				r.HostNSAll++
			}
			if f.ICMPType == ndp.TypeEchoReply || f.ICMPType == ndp.TypeEchoRequest {
				r.Echo = append(r.Echo, f)
				continue
			}
			r.T.Add(f, l.jail, l.hostMAC)
			if o.respond && f.EthSrc != l.hostMAC {
				if na, ok := ndp.AnswerFor(f, mine, l.hostMAC); ok {
					if err := wr.Write(na); err != nil {
						e.Note("responder write failed: %v", err)
					} else {
						r.Sent++
					}
				}
			}
		}
	}
	if argv != nil {
		select {
		case sr := <-ch:
			r.StimOK = sr.err == nil
			e.Note("stimulus exit: ok=%v %s", r.StimOK, strings.TrimSpace(lastLines(sr.out, 3)))
			if sr.err != nil {
				e.Note("stimulus error: %v", sr.err)
			}
		case <-time.After(80 * time.Second):
			e.Note("stimulus did not finish")
		}
	}
	rc, dr, _ := ls.Stats()
	e.Note("tally: mcastNS=%d unicastNS=%d NA=%d (sent by host MAC: %d) hostNS-for-target=%d other=%d | responder sent %d | bpf recv=%d drop=%d",
		r.T.McastNS, r.T.UcastNS, r.T.NA, r.T.OutNA, r.T.HostNS, r.T.Other, r.Sent, rc, dr)
	return r
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " | ")
}

func (l *Lab) win(unicast bool) time.Duration {
	s := cfg.listen
	if unicast && s < 12 {
		s = 12
	}
	return time.Duration(s) * time.Second
}

// ---- Part A -------------------------------------------------------------------------------

func (l *Lab) partA() {
	e := l.e
	e.Step("PART A: kernel proxy entry (ndp -s %s %s proxy)", l.jail, l.hostMAC)
	l.addUndo("ndp", "-d", l.jail.String())
	l.run("ndp", "-s", l.jail.String(), l.hostMAC.String(), "proxy")
	ents := ndp.FindNdp(ndp.ParseNdpAn(l.quiet("ndp", "-an").Stdout), l.jail)
	if e.DryRun {
		e.Check("A1 proxy entry exists on the uplink", true, "")
	} else {
		ok := len(ents) == 1 && ents[0].Iface == l.uplink
		d := "no entry"
		if len(ents) > 0 {
			d = fmt.Sprintf("%+v", ents[0])
		}
		e.Check("A1 proxy entry exists on the uplink", ok, d+" "+conv)
		e.Resolve(fmt.Sprintf("ndp -s <addr> <host-mac> proxy accepted and attaches to the uplink: %v", ok))
	}
	r := l.observe(obsOpts{name: "A2 multicast NS vs kernel proxy", join: true, stim: "mcast", window: l.win(false)})
	if !e.DryRun {
		l.guard()
		answered := r.T.OutNA > 0
		e.Check("A2 kernel proxy answers multicast NS", r.T.McastNS > 0,
			fmt.Sprintf("OBSERVED: multicast NS seen=%d, NA from host MAC seen=%d => kernel %s. %s", r.T.McastNS, r.T.OutNA,
				map[bool]string{true: "ANSWERED (proxy entry works for multicast NS arriving on the uplink)", false: "did NOT answer"}[answered], conv))
		e.Resolve(fmt.Sprintf("kernel answers multicast NS for an ndp proxy entry: %v", answered))
	}
	u := l.observe(obsOpts{name: "A3 unicast NUD NS vs kernel proxy", join: true, stim: "ucast", window: l.win(true)})
	if !e.DryRun {
		l.guard()
		answered := u.T.OutNA > 0
		e.Check("A3 unicast NUD NS seen and answered by kernel proxy", u.T.UcastNS > 0,
			fmt.Sprintf("OBSERVED: unicast NS seen=%d (promisc off), NA from host MAC=%d => kernel %s. %s", u.T.UcastNS, u.T.OutNA,
				map[bool]string{true: "ANSWERED unicast NUD", false: "did NOT answer unicast NUD"}[answered], conv))
		e.Resolve(fmt.Sprintf("kernel answers unicast NUD NS for an ndp proxy entry: %v", answered))
	}
	l.run("ndp", "-d", l.jail.String())
	if !e.DryRun {
		gone := len(ndp.FindNdp(ndp.ParseNdpAn(l.quiet("ndp", "-an").Stdout), l.jail)) == 0
		e.Check("A4 proxy entry removed", gone, "ndp -d "+l.jail.String())
	}
}

// ---- Part B -------------------------------------------------------------------------------

func (l *Lab) partB() {
	e := l.e
	e.Step("PART B: BPF visibility matrix with the host MAC advertised by a Go responder")
	var rs []ndp.ComboResult
	for _, promisc := range []bool{false, true} {
		for _, join := range []bool{false, true} {
			c := ndp.ComboResult{Promisc: promisc, Joined: join}
			m := l.observe(obsOpts{name: fmt.Sprintf("B multicast promisc=%v join=%v", promisc, join), promisc: promisc, join: join, respond: true, stim: "mcast", window: l.win(false)})
			c.Mcast, c.McastRan, c.PromiscFlag = m.T, m.StimRan || !e.DryRun, m.PromiscFlag
			u := l.observe(obsOpts{name: fmt.Sprintf("B unicast promisc=%v join=%v", promisc, join), promisc: promisc, join: join, respond: true, stim: "ucast", window: l.win(true)})
			c.Ucast, c.UcastRan = u.T, true
			if u.PromiscFlag != promisc {
				c.PromiscFlag = u.PromiscFlag
			}
			rs = append(rs, c)
			l.guard()
		}
	}
	if e.DryRun {
		e.Check("B hypothesis 1", true, "")
		e.Check("B hypothesis 2", true, "")
		return
	}
	e.Printf("\n   promisc join | mcast NS  ucast NS  NA(out) | (multicast phase) -- (unicast phase)\n")
	for _, c := range rs {
		e.Printf("   %-7v %-4v | %d  %d  %d  --  %d  %d  %d\n", c.Promisc, c.Joined, c.Mcast.McastNS, c.Mcast.UcastNS, c.Mcast.OutNA, c.Ucast.McastNS, c.Ucast.UcastNS, c.Ucast.OutNA)
	}
	o1, d1 := ndp.EvalUnicast(rs)
	e.Check("B hypothesis 1: advertising the host MAC makes unicast NUD NS visible without promiscuous mode", o1 != ndp.Inconclusive,
		fmt.Sprintf("hypothesis %s: %s. %s", o1, d1, conv))
	e.Resolve(fmt.Sprintf("hypothesis 1 (host MAC => unicast NUD visible without promisc): %s", o1))
	o2, d2 := ndp.EvalMulticast(rs)
	e.Check("B hypothesis 2: joining solicited-node groups makes multicast NS visible without promiscuous mode", o2 != ndp.Inconclusive,
		fmt.Sprintf("hypothesis %s: %s. %s", o2, d2, conv))
	e.Resolve(fmt.Sprintf("hypothesis 2 (solicited-node join => multicast visible without promisc): %s", o2))
}

// ---- lab ----------------------------------------------------------------------------------

func (l *Lab) createRenamed(kind, want string, sides ...string) []string {
	e := l.e
	r := l.run("ifconfig", kind, "create")
	name := r.Out()
	if e.DryRun {
		name = kind + "0"
		if kind == "epair" {
			name = "epair0a"
		}
	}
	if !reAnyEpair.MatchString(name) {
		e.Abort("unexpected output of ifconfig %s create: %q", kind, name)
	}
	return []string{name}
}

func (l *Lab) setupLab() {
	e := l.e
	if l.labUp {
		return
	}
	e.Step("Build the lab: bridge %s, epair %s/%s, vnet jail %s (nothing on the uplink is touched)", bridgeName, epairA, epairB, jailName)
	fwd := l.quiet("sysctl", "-n", "net.inet6.ip6.forwarding").Out()
	if fwd == "0" {
		if !cfg.allowFwd {
			e.Abort("net.inet6.ip6.forwarding is 0 and the acceptance tests need forwarding; re-run with --allow-forwarding-sysctl if the host may forward (it will be set back to 0 at the end). WARNING: UNVERIFIED whether enabling forwarding stops RA-learned default routes on this host; the watchdog restores it")
		}
		l.addUndo("sysctl", "net.inet6.ip6.forwarding=0")
		l.run("sysctl", "net.inet6.ip6.forwarding=1")
	}
	// UNVERIFIED: `ifconfig bridge create` prints the new name; `ifconfig <if> name <new>` renames
	// bridges and epairs. If the run dies between create and rename a stray bridgeN/epairNa
	// remains: look at `ifconfig -l`.
	br := l.createRenamed("bridge", bridgeName)[0]
	l.run("ifconfig", br, "name", bridgeName)
	l.addUndo("ifconfig", bridgeName, "destroy")
	ep := l.createRenamed("epair", epairA)[0]
	epb := strings.TrimSuffix(ep, "a") + "b"
	l.run("ifconfig", ep, "name", epairA)
	l.run("ifconfig", epb, "name", epairB)
	l.addUndo("ifconfig", epairA, "destroy")
	l.run("ifconfig", epairA, "up")
	l.run("ifconfig", bridgeName, "addm", epairA)
	l.run("ifconfig", bridgeName, "inet6", "-ifdisabled", "auto_linklocal", "up")
	l.addUndo("jail", "-r", jailName)
	// UNVERIFIED: a persistent vnet jail rooted at / with no processes is harmless and
	// accepts vnet.interface.
	l.run("jail", "-c", "name="+jailName, "path=/", "vnet", "vnet.interface="+epairB, "persist", "host.hostname="+jailName)
	l.run("jexec", jailName, "ifconfig", "lo0", "up")
	l.run("jexec", jailName, "ifconfig", epairB, "inet6", "-accept_rtadv", "auto_linklocal", "up")
	l.run("jexec", jailName, "ifconfig", epairB, "inet6", l.jail.String(), "prefixlen", "128", "alias")
	if e.DryRun {
		e.Note("(dry run) then: read the bridge link-local, add the jail default route via it, wait for DAD, and:")
		l.run("jexec", jailName, "route", "add", "-inet6", "default", "<bridge-link-local>%"+epairB)
		l.run("route", "add", "-inet6", l.jail.String()+"/128", "-interface", bridgeName)
		l.labUp = true
		return
	}
	var ll netip.Addr
	for i := 0; i < 10 && !ll.IsValid(); i++ {
		if a, ok := ndp.ParseIfconfig(l.quiet("ifconfig", bridgeName).Stdout).LinkLocal(); ok {
			ll = a
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ll.IsValid() {
		e.Abort("%s has no link-local address", bridgeName)
	}
	l.bridgeLL = ll.String()
	e.Note("bridge link-local gateway: %s", l.bridgeLL)
	l.run("jexec", jailName, "route", "add", "-inet6", "default", l.bridgeLL+"%"+epairB)
	time.Sleep(3 * time.Second) // DAD
	jinfo := ndp.ParseIfconfig(l.quiet("jexec", jailName, "ifconfig", epairB).Stdout)
	okAddr := false
	for _, a := range jinfo.Inet6 {
		if a.Addr == l.jail && len(a.Flags) == 0 {
			okAddr = true
		}
	}
	e.Must("lab: jail address configured and past DAD", okAddr, fmt.Sprintf("%+v", jinfo.Inet6))
	if cfg.noGoRsp {
		// Kernel proxy variant: the entry must exist BEFORE the /128 route so that it attaches
		// to the uplink (the route lookup then still finds the on-link /64).
		l.addUndo("ndp", "-d", l.jail.String())
		l.run("ndp", "-s", l.jail.String(), l.hostMAC.String(), "proxy")
	}
	l.addUndo("route", "delete", "-inet6", l.jail.String()+"/128")
	l.run("route", "add", "-inet6", l.jail.String()+"/128", "-interface", bridgeName)
	l.labUp = true
	l.guard()
}

// ---- Part C -------------------------------------------------------------------------------

func (l *Lab) partC() {
	e := l.e
	e.Step("PART C: routing acceptance tests (design section 5)")
	l.setupLab()
	// C1
	if e.DryRun {
		e.Check("C1 route get selects the bridge", true, "")
	} else {
		rg := ndp.ParseRouteGet(l.quiet("route", "-n", "get", "-inet6", l.jail.String()).Stdout)
		d := fmt.Sprintf("route get %s: interface=%s flags=%s", l.jail, rg.Interface, rg.Flags)
		if cfg.peer != "" {
			if pa, err := netip.ParseAddr(cfg.peer); err == nil {
				pr := ndp.ParseRouteGet(l.quiet("route", "-n", "get", "-inet6", pa.String()).Stdout)
				d += fmt.Sprintf("; peer %s still via %s", pa, pr.Interface)
			}
		}
		e.Check("C1 `route -n get -inet6 <jail>` selects the bridge", rg.Interface == bridgeName, d)
		e.Resolve("route add -inet6 <addr>/128 -interface jopbr0 yields a /128 route chosen by route get: " + fmt.Sprint(rg.Interface == bridgeName))
	}
	// C2
	opts := obsOpts{name: "C2 host sends no NS for the jail address on the uplink", promisc: cfg.cPromisc, join: true, respond: !cfg.noGoRsp, stim: "host", window: 10 * time.Second}
	r := l.observe(opts)
	if !e.DryRun {
		l.guard()
		e.Check("C2 host never sends NS for the jail address out of the uplink", r.T.HostNS == 0 && r.StimOK,
			fmt.Sprintf("NS for jail addr with the host MAC as Ethernet source on %s: %d (must be 0); host ping of the jail address succeeded: %v (must be true or the test is vacuous)", l.uplink, r.T.HostNS, r.StimOK))
		e.Resolve(fmt.Sprintf("host does not emit NS for a /128-routed jail address on the uplink: %v", r.T.HostNS == 0))
	} else {
		e.Check("C2", true, "")
	}
	// C3
	if cfg.peerSSH == "" {
		e.Check("C3 upstream reaches the jail after neighbor expiry (NUD)", false, "NOT RUN: needs --peer-ssh (a second host on the /64 playing the upstream router). Manual: --print-recipe")
		e.Check("C4 replies leave with the right source and PF antispoof does not block them", false, "NOT RUN: needs --peer-ssh")
	} else {
		l.acceptC34()
	}
	// C5
	l.statsCheck()
}

func (l *Lab) acceptC34() {
	e := l.e
	// initial reachability through the proxy (multicast resolution) with echo capture for C4
	r1 := l.observe(obsOpts{name: "C4 first contact: peer pings the jail through the proxy", promisc: cfg.cPromisc, join: true, respond: !cfg.noGoRsp,
		stim: "peerping", window: 10 * time.Second, types: []uint8{ndp.TypeNS, ndp.TypeNA, ndp.TypeEchoReply}})
	if e.DryRun {
		e.Check("C3", true, "")
		e.Check("C4", true, "")
		return
	}
	l.guard()
	rightSrc := false
	for _, f := range r1.Echo {
		if f.ICMPType == ndp.TypeEchoReply && f.Src == l.jail && f.EthSrc == l.hostMAC {
			rightSrc = true
		}
	}
	pfInfo := l.quiet("pfctl", "-si").Out()
	pfOn := strings.Contains(pfInfo, "Status: Enabled")
	anti := ""
	for _, ln := range strings.Split(l.quiet("pfctl", "-sr").Out(), "\n") {
		if strings.Contains(ln, "antispoof") || (strings.Contains(ln, "block drop in on !") && strings.Contains(ln, "inet6")) {
			anti += strings.TrimSpace(ln) + "; "
		}
	}
	d := fmt.Sprintf("peer ping through the jail ok=%v; echo reply seen on uplink with source %s and Ethernet source = host MAC: %v; pf enabled=%v; antispoof-like rules: %q",
		r1.StimOK, l.jail, rightSrc, pfOn, anti)
	switch {
	case !pfOn:
		d += ". NOTE: pf is not enabled, so antispoof was NOT exercised: repeat with pf enabled and an `antispoof for <uplink>` rule to settle that part"
	case anti == "":
		d += ". NOTE: pf enabled but no antispoof rule present: antispoof NOT exercised"
	}
	e.Check("C4 replies leave with the right source and PF antispoof does not block them", r1.StimOK && rightSrc, d)
	e.Resolve(fmt.Sprintf("jail replies reach the upstream with the jail address as source (pf enabled=%v antispoof present=%v): %v", pfOn, anti != "", r1.StimOK && rightSrc))
	// C3: unicast NUD after the peer's entry goes stale
	r3 := l.observe(obsOpts{name: "C3 neighbor entry on the peer goes stale: NUD must keep working", promisc: cfg.cPromisc, join: true, respond: !cfg.noGoRsp,
		stim: "ucast", window: l.win(true)})
	l.guard()
	ok := r3.StimOK && r3.T.UcastNS > 0 && r3.T.OutNA > 0
	e.Check("C3 upstream keeps reaching the jail after neighbor expiry (unicast NUD answered)", ok,
		fmt.Sprintf("peer ping ok=%v; unicast NUD NS seen=%d; NA answers sent=%d (listener promisc=%v). All three must be true. If NS seen=0 the NUD probe was not observed: retry with --c-promisc to learn whether visibility is the problem", r3.StimOK, r3.T.UcastNS, r3.T.OutNA, cfg.cPromisc))
	e.Resolve(fmt.Sprintf("upstream NUD keeps working through the proxy (promisc=%v): %v", cfg.cPromisc, ok))
}

// ---- Part D -------------------------------------------------------------------------------

func (l *Lab) partD_() {
	e := l.e
	e.Step("PART D: failure reproduction with the UPLINK INSIDE the bridge (opt-in; watchdog armed)")
	l.setupLab()
	e.Note("WARNING: the next step adds %s to %s. Connectivity may be lost; the watchdog restores after %ds of failed probes.", l.uplink, bridgeName, cfg.failSecs)
	// Reconfigure the lab to the classic bridged setup: on-link /64 on the jail, no host /128 route.
	l.run("route", "delete", "-inet6", l.jail.String()+"/128")
	l.run("jexec", jailName, "ifconfig", epairB, "inet6", l.jail.String(), "-alias")
	l.run("jexec", jailName, "ifconfig", epairB, "inet6", l.jail.String(), "prefixlen", "64", "alias")
	if e.DryRun {
		l.run("ifconfig", bridgeName, "addm", l.uplink)
		l.run("ifconfig", bridgeName, "deletem", l.uplink)
		e.Check("D bridge membership experiment", true, "")
		return
	}
	_, f0, _ := l.probeStats()
	// the undo is recorded BEFORE the dangerous action
	l.addUndo("ifconfig", bridgeName, "deletem", l.uplink)
	var r obsRes
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(2 * time.Second)
		l.run2("ifconfig", bridgeName, "addm", l.uplink)
	}()
	r = l.observeD(time.Duration(cfg.dSeconds) * time.Second)
	<-done
	_, f1, run1 := l.probeStats()
	l.mu.Lock()
	tripped := l.tripped
	l.mu.Unlock()
	if tripped == "" {
		l.run("ifconfig", bridgeName, "deletem", l.uplink)
	}
	time.Sleep(3 * time.Second)
	members := ndp.ParseIfconfig(l.quiet("ifconfig", bridgeName).Stdout).Members
	restored := !contains(members, l.uplink) || tripped != ""
	lost := f1 - f0
	finding := "NOT reproduced: the host kept its connectivity with the uplink inside the bridge, so bridge membership alone is not sufficient on this setup"
	if tripped != "" {
		finding = "REPRODUCED: connectivity was lost with the uplink inside the bridge (watchdog restored: " + tripped + "); bridge membership is sufficient to cause the failure"
	} else if lost > 0 {
		finding = fmt.Sprintf("PARTIALLY reproduced: %d probes failed (longest run %d) while the uplink was inside the bridge, connectivity recovered after restoring", lost, run1)
	}
	e.Check("D uplink inside the bridge: failure reproduction", true,
		fmt.Sprintf("%s. During the window: probe failures=%d; NS sent by the host MAC (all targets)=%d, of which for the jail address=%d; NS/NA frames seen=%d. %s",
			finding, lost, r.HostNSAll, r.T.HostNS, r.Total, conv))
	e.Resolve("bridge membership of the uplink as cause of host connectivity loss: " + strings.SplitN(finding, ":", 2)[0])
	e.Check("D uplink removed from the bridge again", restored, fmt.Sprintf("members of %s after restore: %v", bridgeName, members))
}

// run2 is run for use from a goroutine: it must not abort via panic.
func (l *Lab) run2(argv ...string) {
	if err := checkCmd(argv, l.uplink, l.jail.String(), l.partD); err != nil {
		l.logf("%v", err)
		return
	}
	if err := execQuiet(argv); err != nil {
		l.logf("%s: %v", strings.Join(argv, " "), err)
	}
}

// observeD listens on the uplink during Part D without a responder and with a peer stimulus.
func (l *Lab) observeD(window time.Duration) obsRes {
	return l.observe(obsOpts{name: "D observation while the uplink is a bridge member", promisc: false, join: false, stim: "mcast", window: window, noGuard: true})
}

func cleanup(e *harness.Env) {
	st, err := ndp.LoadState(cfg.state)
	run := func(argv []string) {
		if err := ndp.ValidateUndo(argv); err != nil {
			e.Note("refusing: %v", err)
			return
		}
		e.Run(argv...)
	}
	if err == nil {
		e.Step("Undo entries from %s (reverse order)", cfg.state)
		for i := len(st.Undo) - 1; i >= 0; i-- {
			run(st.Undo[i])
		}
	} else {
		e.Note("no state file (%v); sweeping known names only", err)
	}
	e.Step("Sweep known jop names")
	names := strings.Fields(e.Quiet("ifconfig", "-l").Out())
	has := func(n string) bool { return e.DryRun || contains(names, n) }
	if e.DryRun || jailExists(e) {
		run([]string{"jail", "-r", jailName})
	}
	if has(bridgeName) {
		for _, m := range ndp.ParseIfconfig(e.Quiet("ifconfig", bridgeName).Stdout).Members {
			run([]string{"ifconfig", bridgeName, "deletem", m})
		}
	}
	if has(epairA) {
		run([]string{"ifconfig", epairA, "destroy"})
	}
	if has(bridgeName) {
		run([]string{"ifconfig", bridgeName, "destroy"})
	}
	if a, err := netip.ParseAddr(cfg.jailAddr); err == nil {
		run([]string{"route", "delete", "-inet6", a.String() + "/128"})
		run([]string{"ndp", "-d", a.String()})
	} else {
		e.Note("no --jail-addr: only the recorded route/ndp entries were removed")
	}
	if err == nil && !e.DryRun {
		st.Done, st.Undo, st.Reason = true, nil, "--cleanup"
		_ = st.Save(cfg.state)
		os.Remove(cfg.state)
	}
	e.Note("check: ifconfig -l | tr ' ' '\\n' | grep jop ; jls ; netstat -rn -f inet6 | grep jop")
}

func jailExists(e *harness.Env) bool {
	r := e.Quiet("jls", "-j", jailName, "jid")
	return r.Err == nil
}
