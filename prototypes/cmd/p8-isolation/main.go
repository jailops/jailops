// Prototype 8: do private bridge members plus per-jail PF anchors isolate jails of different
// projects on a shared bridge (design 8.1, row 13 of docs/design.md section 16)?
//
// HONESTY: written on Linux without access to FreeBSD. Every FreeBSD behaviour it assumes is
// marked UNVERIFIED: and settled (either way) by the run.
//
// SCOPE: this prototype verifies CONFIGURATION and CONNECTIVITY with ordinary tools only (ping,
// nc, ifconfig, pfctl, sysctl, and tcpdump as a passive observer). It deliberately does NOT
// forge, inject or craft any packet: no raw Ethernet frames, no crafted ARP/NDP/router-
// advertisement/DHCP messages, no poisoning tooling. See the README for why, and for how an
// operator runs an active spoofing test by hand in an isolated lab if that stronger assurance
// is wanted.
//
// SAFETY: creates only bridge jop8br0, jails jop8a1/jop8a2/jop8b1, their epairs, two nullfs
// mounts per jail under --mount-base, and PF anchors under jailops/jop8/*. pf.conf and the admin
// ruleset are never written (pf.conf needs the same three jailops/* lines as prototype 3). The
// two forwarding sysctls and the three pfil sysctls are restored to what they were. A watchdog
// (in-process and a detached helper) restores everything if the operator's session stalls.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/isolation"
)

var (
	baseDir    string
	mountBase  string
	watchdogS  int
	rateLimit  int
	testPort   int
	skipThru   bool
	helper     bool
	cleanupArg bool
)

func main() {
	switch {
	case hasArg("watchdog-helper"):
		os.Exit(helperMain(argVal("state", defaultState)))
	}
	harness.Exit(harness.Spec{
		N:       8,
		Name:    "isolation",
		NoPool:  true,
		Summary: "shared-bridge project isolation: private members, per-project/per-jail PF anchors, pfil sysctls (configuration and connectivity only; no packet forging)",
		Plan: []string{
			"arm the watchdog: in-process monitor plus a detached helper that restores sysctls/pf/network if the probe fails or the deadline passes",
			"create bridge jop8br0 (host gateway 192.0.2.1/2001:db8:8::1); never touch any other bridge or the uplink",
			"create three vnet jails: jop8a1, jop8a2 (project jop8a), jop8b1 (project jop8b); rootfs is a read-only nullfs mount of --base-dir; one epair per jail",
			"test 1: ordinary reachability (ping, ping6, nc TCP) between every pair with the bridge member flag `private` set on all epairs, then cleared (control); record which flows work",
			"test 2: with private set, try combinations of net.link.bridge.pfil_member/pfil_bridge/pfil_onlyip plus per-project PF anchors (allow within project, deny across) and record which combination is needed for PF to actually see and filter bridge-member traffic",
			"test 3: ordinary address-binding probes: alias another jail's address on a jail's epair and source `ping -S`/`nc -s` from it, and reconfigure a jail's own address to another jail's address; test each with and without a per-jail anchor `block in on <epair> from !<addr>`",
			"test 4: ff02::1 and the IPv4 subnet broadcast pinged from one jail, observed (tcpdump, not injected) at the others, with private on and off",
			"test 5: simultaneous ordinary TCP throughput between two jail pairs, measured with and without a PF max-src-conn-rate limit (a connection-rate limit, not a bandwidth shaper); numbers only, no guarantee claimed",
			"--cleanup: remove the jails, epairs, bridge, nullfs mounts and every jailops/jop8/* anchor; restore the five sysctls",
		},
		Flags: func(e *harness.Env, fs *flag.FlagSet) {
			fs.StringVar(&baseDir, "base-dir", "", "read-only directory nullfs-mounted as each jail's rootfs (required; e.g. a clone of the host's /, or a minimal base tree)")
			fs.StringVar(&mountBase, "mount-base", "/jop-proto-8", "directory the per-jail nullfs mountpoints are created under; last element must start with jop")
			fs.IntVar(&watchdogS, "watchdog", 600, "hard deadline in seconds after which everything is restored")
			fs.IntVar(&rateLimit, "rate-limit", 5, "max-src-conn-rate value (connections/second) tried in test 5")
			fs.IntVar(&testPort, "test-port", 9000, "TCP port used by the nc reachability and throughput probes")
			fs.BoolVar(&skipThru, "skip-throughput", false, "skip test 5 (useful on a slow or shared build host)")
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

const defaultState = "/var/run/jop8-isolation.state"

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

func mountpoint(jail string) string { return mountBase + "/" + jail }

func run(e *harness.Env) {
	if baseDir == "" {
		e.Abort("--base-dir is required: a read-only tree to nullfs-mount as each jail's rootfs")
	}
	if !strings.HasPrefix(lastElem(mountBase), "jop") {
		e.Abort("--mount-base's last path element must start with jop, got %q", mountBase)
	}
	e.Require("ifconfig", "jail", "jexec", "jls", "mount", "umount", "pfctl", "sysctl", "nc", "ping", "ping6", "tcpdump")
	wd := armWatchdog(e)
	defer wd.finish(e)

	e.Step("0. base rootfs and jails")
	setupJails(e)
	mtx := &isolation.Matrix{}

	test1(e, mtx, wd)
	test2(e, mtx, wd)
	test3(e, mtx, wd)
	test4(e, mtx, wd)
	if !skipThru {
		test5(e, mtx, wd)
	} else {
		e.Note("test 5 (throughput) skipped by --skip-throughput")
	}

	e.Step("results matrix")
	e.Check("results matrix", true, mtx.JSON())
}

func lastElem(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// setupJails creates the bridge, the three vnet jails and their epairs, and addresses them.
func setupJails(e *harness.Env) {
	// Verified on FreeBSD 15.1: a bridge cannot be created under an arbitrary name; create the
	// generic one and rename it.
	if br := e.Run("ifconfig", "bridge", "create"); br.OK() || e.DryRun {
		gen := strings.TrimSpace(br.Stdout)
		if e.DryRun {
			gen = "bridge0"
		}
		e.Run("ifconfig", gen, "name", isolation.Bridge)
	}
	e.Run("ifconfig", isolation.Bridge, "inet", isolation.GatewayV4+"/"+isolation.V4Prefix, "up")
	e.Run("ifconfig", isolation.Bridge, "inet6", isolation.GatewayV6, "prefixlen", isolation.V6Prefix)
	for _, jail := range isolation.Jails {
		mp := mountpoint(jail)
		if !e.DryRun {
			os.MkdirAll(mp, 0o755)
		}
		e.Run("mount", "-t", "nullfs", "-o", "ro", baseDir, mp)
		// The base is read-only; a small tmpfs gives each jail a scratch /tmp (test 4's capture
		// files land there). Unmounted before the nullfs root in cleanup and in the watchdog.
		if !e.DryRun {
			os.MkdirAll(mp+"/tmp", 0o1777)
		}
		e.Run("mount", "-t", "tmpfs", "-o", "mode=1777", "tmpfs", mp+"/tmp")

		r := e.Run("ifconfig", "epair", "create")
		a := r.Out()
		if e.DryRun {
			a = "epair0a"
		}
		b := strings.TrimSuffix(a, "a") + "b"
		host, jifn := isolation.EpairHost(jail), isolation.EpairJail(jail)
		e.Run("ifconfig", a, "name", host)
		e.Run("ifconfig", b, "name", jifn)
		e.Run("ifconfig", isolation.Bridge, "addm", host, "up")
		e.Run("ifconfig", host, "up")

		r = e.Run("jail", "-c", "name="+jail, "host.hostname="+jail, "path="+mp,
			"persist", "vnet", "vnet.interface="+jifn, "mount.devfs", "allow.raw_sockets=1",
			"enforce_statfs=1", "sysvshm=new")
		e.Check("jail-create "+jail, r.OK(), r.Combined())

		v4, v6 := isolation.AddrOf(jail)
		e.Run("jexec", jail, "ifconfig", jifn, "inet", v4+"/"+isolation.V4Prefix, "up")
		e.Run("jexec", jail, "ifconfig", jifn, "inet6", v6, "prefixlen", isolation.V6Prefix)
		e.Run("jexec", jail, "route", "add", "default", isolation.GatewayV4)
		e.Run("jexec", jail, "route", "-6", "add", "default", isolation.GatewayV6)
	}
	e.Check("three jails running", e.DryRun || allJailed(e), "jls output for jop8*")
}

func allJailed(e *harness.Env) bool {
	out := e.Quiet("jls", "-N", "name").Out()
	for _, j := range isolation.Jails {
		if !strings.Contains(out, j) {
			return false
		}
	}
	return true
}

// pf runs a guarded pfctl command.
func pf(e *harness.Env, stdin string, args ...string) harness.Result {
	if err := isolation.GuardPfctl(args); err != nil {
		e.Abort("%v", err)
	}
	r := e.RunStdin(stdin, append([]string{"pfctl"}, args...)...)
	// A ruleset that pf refuses to load must never be mistaken for "rules had no effect".
	if !r.OK() && stdin != "" {
		e.Check("pf load "+strings.Join(args, " "), false, r.Combined())
	}
	return r
}

func pfq(e *harness.Env, args ...string) harness.Result {
	if err := isolation.GuardPfctl(args); err != nil {
		e.Abort("%v", err)
	}
	return e.Quiet(append([]string{"pfctl"}, args...)...)
}

// setPrivate sets or clears the bridge "private" member flag on every jail epair.
func setPrivate(e *harness.Env, on bool) {
	flag := "private"
	if !on {
		flag = "-private"
	}
	for _, jail := range isolation.Jails {
		e.Run("ifconfig", isolation.Bridge, flag, isolation.EpairHost(jail))
	}
}

// setSysctl writes one allow-listed sysctl and returns the previous value (empty on a dry run or
// read failure). The caller is responsible for restoring it.
func setSysctl(e *harness.Env, name, value string) string {
	if !isolation.ValidSysctlName(name) {
		e.Abort("refusing to set sysctl %q: not on the allow-list", name)
	}
	prev := e.Quiet("sysctl", "-n", name).Out()
	e.Run("sysctl", name+"="+value)
	return prev
}

func restoreSysctl(e *harness.Env, name, prev string) {
	if prev != "" && isolation.ValidSysctlName(name) {
		e.Run("sysctl", name+"="+prev)
	}
}

// ping runs ping (v4) or ping6 (v6) from inside src toward dst and reports success.
func pingFrom(e *harness.Env, src, dst string, v6 bool, srcAddr string) (harness.Result, bool) {
	argv := []string{"jexec", src}
	if v6 {
		argv = append(argv, "ping6", "-c", "2", "-t", "3")
	} else {
		argv = append(argv, "ping", "-c", "2", "-t", "3")
	}
	if srcAddr != "" {
		argv = append(argv, "-S", srcAddr)
	}
	argv = append(argv, dst)
	r := e.Run(argv...)
	return r, r.OK()
}

// tcpReach starts a listener in dst and probes it from src with nc -z, returning success.
func tcpReach(e *harness.Env, src, dstJail, dstAddr string) (harness.Result, bool) {
	if e.DryRun {
		return harness.Result{Dry: true}, true
	}
	e.Run("jexec", dstJail, "sh", "-c", fmt.Sprintf("pkill -f 'nc -l %d' 2>/dev/null; nc -l %d >/dev/null 2>&1 &", testPort, testPort))
	time.Sleep(300 * time.Millisecond)
	r := e.Run("jexec", src, "nc", "-z", "-w", "2", dstAddr, fmt.Sprint(testPort))
	e.Run("jexec", dstJail, "pkill", "-f", fmt.Sprintf("nc -l %d", testPort))
	return r, r.OK()
}

func cleanup(e *harness.Env) {
	e.Step("cleanup")
	cleanupWatchdog(e)
	for _, jail := range isolation.Jails {
		if e.Quiet("jls", "-j", jail, "name").OK() || e.DryRun {
			e.Run("jail", "-r", jail)
		}
	}
	for _, jail := range isolation.Jails {
		pf(e, "", "-a", isolation.JailAnchor(jail), "-F", "all")
	}
	for _, p := range []string{isolation.ProjectA, isolation.ProjectB} {
		pf(e, "", "-a", isolation.ProjectAnchor(p), "-F", "all")
	}
	for _, jail := range isolation.Jails {
		if e.Quiet("ifconfig", isolation.EpairHost(jail)).OK() || e.DryRun {
			e.Run("ifconfig", isolation.EpairHost(jail), "destroy")
		}
		mp := mountpoint(jail)
		mounts := e.Quiet("mount", "-p").Out()
		if strings.Contains(mounts, mp+"/tmp") || e.DryRun {
			e.Run("umount", mp+"/tmp")
		}
		if strings.Contains(mounts, mp) || e.DryRun {
			e.Run("umount", mp)
		}
	}
	if e.Quiet("ifconfig", isolation.Bridge).OK() || e.DryRun {
		e.Run("ifconfig", isolation.Bridge, "destroy")
	}
	if s, err := loadState(defaultState); err == nil && len(s.Sysctls) > 0 {
		e.Note("restoring the five sysctls to the values recorded before the run started")
		restoreSysctlsRaw(s.Sysctls)
	} else {
		for _, name := range []string{isolation.SysctlPfilMember, isolation.SysctlPfilBridge, isolation.SysctlPfilOnlyIP,
			"net.inet.ip.forwarding", "net.inet6.ip6.forwarding"} {
			e.Note("sysctl %s: no recorded baseline found; check it by hand", name)
		}
	}
}
