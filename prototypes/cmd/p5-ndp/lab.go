package main

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/ndp"
)

const (
	bridgeName = "jopbr0"
	epairA     = "jope0a"
	epairB     = "jope0b"
	jailName   = "jop5"
)

var (
	reOurName  = regexp.MustCompile(`^jop[a-z0-9_]{1,11}$`)
	reAnyEpair = regexp.MustCompile(`^(bridge|epair)[0-9]+[ab]?$`)
	reSSH      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9_][A-Za-z0-9_.:-]*$|^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	reDev      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
)

// checkCmd is the allow-list for every command this prototype runs locally. It exists so that
// a bug in the experiment code cannot touch anything that is not ours. uplink is only ever
// read, except for `ifconfig jopbr0 addm <uplink>` in Part D.
func checkCmd(a []string, uplink, jailAddr string, partD bool) error {
	no := fmt.Errorf("command not allowed by the prototype 5 safety list: %q", strings.Join(a, " "))
	if len(a) == 0 {
		return no
	}
	ours := func(s string) bool { return reOurName.MatchString(s) }
	switch a[0] {
	case "ifconfig":
		switch {
		case len(a) == 2 && a[1] == "-l":
			return nil
		case len(a) == 2 && (a[1] == uplink || ours(a[1]) || reAnyEpair.MatchString(a[1])):
			return nil // read-only
		case len(a) == 3 && (a[1] == "bridge" || a[1] == "epair") && a[2] == "create":
			return nil
		case len(a) == 4 && reAnyEpair.MatchString(a[1]) && a[2] == "name" && ours(a[3]):
			return nil
		case len(a) >= 3 && ours(a[1]):
			if a[2] == "addm" || a[2] == "deletem" {
				if len(a) == 4 && (ours(a[3]) || (partD && a[3] == uplink && a[1] == bridgeName)) {
					return nil
				}
				return no
			}
			return nil
		}
	case "route":
		switch {
		case len(a) == 5 && a[1] == "-n" && a[2] == "get" && a[3] == "-inet6":
			return nil
		case len(a) == 6 && a[1] == "add" && a[2] == "-inet6" && a[3] == jailAddr+"/128" && a[4] == "-interface" && ours(a[5]):
			return nil
		case len(a) == 4 && a[1] == "delete" && a[2] == "-inet6" && a[3] == jailAddr+"/128":
			return nil
		}
	case "ndp":
		switch {
		case len(a) == 2 && a[1] == "-an":
			return nil
		case len(a) == 5 && a[1] == "-s" && a[2] == jailAddr && a[4] == "proxy":
			return nil
		case len(a) == 3 && a[1] == "-d" && a[2] == jailAddr:
			return nil
		}
	case "jail":
		if len(a) >= 3 && a[1] == "-r" && ours(a[2]) {
			return nil
		}
		if len(a) >= 3 && a[1] == "-c" && a[2] == "name="+jailName {
			return nil
		}
	case "jexec":
		if len(a) >= 3 && a[1] == jailName {
			return nil
		}
	case "jls":
		return nil
	case "sysctl":
		if len(a) == 3 && a[1] == "-n" && a[2] == "net.inet6.ip6.forwarding" {
			return nil
		}
		if len(a) == 2 && ndp.ValidateUndo(a) == nil {
			return nil
		}
	case "ping":
		return nil
	case "ifmcstat", "netstat", "uname":
		return nil
	case "pfctl":
		if len(a) >= 2 && strings.HasPrefix(a[1], "-s") {
			return nil
		}
	case "ssh":
		return nil // arguments are built by sshArgv from validated pieces
	}
	return no
}

// Lab is the run context shared by the parts.
type Lab struct {
	e        *harness.Env
	uplink   string
	hostMAC  ndp.MAC
	jail     netip.Addr
	probe    string
	partD    bool
	upInfo   ndp.IfInfo
	bridgeLL string

	mu      sync.Mutex
	undo    [][]string
	armed   bool
	tornDn  bool
	tripped string
	wd      *ndp.WD
	stop    chan struct{}
	done    sync.WaitGroup
	st      ndp.State
	labUp   bool
}

func (l *Lab) logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[p5 "+time.Now().Format("15:04:05")+"] "+format+"\n", a...)
}

// run is e.Run behind the allow-list.
func (l *Lab) run(argv ...string) harness.Result {
	if err := checkCmd(argv, l.uplink, l.jail.String(), l.partD); err != nil {
		l.e.Abort("%v", err)
	}
	return l.e.Run(argv...)
}

// quiet is a read-only run (no echo unless it fails).
func (l *Lab) quiet(argv ...string) harness.Result {
	if err := checkCmd(argv, l.uplink, l.jail.String(), l.partD); err != nil {
		l.e.Abort("%v", err)
	}
	return l.e.Quiet(argv...)
}

// addUndo records an undo command (validated against the undo allow-list) before the action.
func (l *Lab) addUndo(argv ...string) {
	if err := ndp.ValidateUndo(argv); err != nil {
		l.e.Abort("internal: %v", err)
	}
	if l.e.DryRun {
		l.e.Note("(undo on exit: %s)", strings.Join(argv, " "))
		return
	}
	l.mu.Lock()
	l.undo = append(l.undo, argv)
	l.st.Undo = append([][]string(nil), l.undo...)
	err := l.st.Save(cfg.state)
	l.mu.Unlock()
	if err != nil {
		l.e.Abort("cannot save state file %s: %v", cfg.state, err)
	}
}

func execQuiet(argv []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// teardown runs the undo list once; reason is recorded in the state file.
func (l *Lab) teardown(reason string) {
	l.mu.Lock()
	if l.tornDn || !l.armed {
		l.mu.Unlock()
		return
	}
	l.tornDn = true
	undo := append([][]string(nil), l.undo...)
	l.mu.Unlock()
	l.logf("teardown (%s): %d undo entries", reason, len(undo))
	for _, err := range ndp.RunUndo(undo, execQuiet) {
		l.logf("  undo: %v (ignored, may already be gone)", err)
	}
	l.mu.Lock()
	l.st.Done, l.st.Reason, l.st.Undo = true, reason, nil
	_ = l.st.Save(cfg.state)
	l.mu.Unlock()
}

// finish is the normal end: stop the monitor, tear down.
func (l *Lab) finish() {
	if l.stop != nil {
		select {
		case <-l.stop:
		default:
			close(l.stop)
		}
		l.done.Wait()
	}
	l.teardown("normal end")
}

// guard aborts the prototype when the watchdog has restored the system.
func (l *Lab) guard() {
	l.mu.Lock()
	t := l.tripped
	l.mu.Unlock()
	if t != "" {
		l.e.Abort("watchdog restored the system: %s", t)
	}
}

// pingOnce is the connectivity probe (also used by the helper).
func pingOnce(target string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	// UNVERIFIED: FreeBSD 15 ping accepts -6, -c 1 and -t <seconds> (timeout).
	return exec.CommandContext(ctx, "ping", "-6", "-c", "1", "-t", "2", target).Run() == nil
}

// arm writes the state file, starts the detached helper and the in-process monitor.
func (l *Lab) arm() {
	e := l.e
	if e.DryRun {
		e.Note("(dry run) would write %s, start the detached helper `%s --watchdog-helper` in its own session, and probe %s every second",
			cfg.state, os.Args[0], l.probe)
		return
	}
	if old, err := ndp.LoadState(cfg.state); err == nil && !old.Done && len(old.Undo) > 0 {
		e.Abort("state file %s has %d unfinished undo entries from an earlier run: run --cleanup first", cfg.state, len(old.Undo))
	}
	e.Must("pre-flight connectivity probe", pingOnce(l.probe), "ping -6 "+l.probe+" must work before anything is changed")
	deadline := time.Now().Add(time.Duration(cfg.watchdog) * time.Second)
	l.st = ndp.State{Pid: os.Getpid(), Deadline: deadline, ProbeTarget: l.probe, FailSecs: cfg.failSecs}
	if err := l.st.Save(cfg.state); err != nil {
		e.Abort("state file: %v", err)
	}
	l.armed = true
	e.OnCleanup(l.finish)
	if err := spawnHelper(cfg.state); err != nil {
		e.Abort("cannot start the detached watchdog helper: %v (refusing to continue without it)", err)
	}
	l.wd = &ndp.WD{Deadline: deadline, FailAfter: time.Duration(cfg.failSecs) * time.Second}
	l.stop = make(chan struct{})
	l.done.Add(1)
	go func() {
		defer l.done.Done()
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-tk.C:
			}
			ok := pingOnce(l.probe)
			l.mu.Lock()
			restore, why := l.wd.Observe(time.Now(), ok)
			l.mu.Unlock()
			if restore {
				l.mu.Lock()
				l.tripped = why
				l.mu.Unlock()
				l.logf("WATCHDOG: %s: restoring now", why)
				l.teardown("watchdog: " + why)
				return
			}
		}
	}()
	e.Note("watchdog armed: probe %s every 1s, restore after %ds of failures or at %s; helper detached (log %s.log)",
		l.probe, cfg.failSecs, deadline.Format("15:04:05"), cfg.state)
}

// probeStats returns the probe counters.
func (l *Lab) probeStats() (oks, fails, maxRun int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.wd == nil {
		return 0, 0, 0
	}
	return l.wd.Oks, l.wd.Fails, l.wd.MaxRun
}

// helperMain is the detached watchdog: independent of the main process.
func helperMain(state string) int {
	fmt.Printf("helper %d started for %s\n", os.Getpid(), state)
	var wd *ndp.WD
	for {
		st, err := ndp.LoadState(state)
		if err != nil || st.Done {
			fmt.Println("state gone or done: helper exits")
			return 0
		}
		if wd == nil {
			wd = &ndp.WD{Deadline: st.Deadline, FailAfter: time.Duration(st.FailSecs) * time.Second}
		}
		restore, why := wd.Observe(time.Now(), pingOnce(st.ProbeTarget))
		if !alive(st.Pid) {
			restore, why = true, "main process is gone"
		}
		if restore {
			fmt.Printf("%s: HELPER RESTORING: %s\n", time.Now().Format(time.RFC3339), why)
			for _, err := range ndp.RunUndo(st.Undo, execQuiet) {
				fmt.Printf("  undo: %v\n", err)
			}
			st.Done, st.Reason, st.Undo = true, "helper: "+why, nil
			_ = st.Save(state)
			return 0
		}
		time.Sleep(time.Second)
	}
}

// analyzeMain summarises a saved tcpdump text file.
func analyzeMain(path, addr, mac string) int {
	a, err := netip.ParseAddr(addr)
	if err != nil || path == "" || mac == "" {
		fmt.Println("usage: --analyze-tcpdump FILE --jail-addr ADDR --host-mac MAC")
		return 2
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Println(err)
		return 2
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	m, u, na, h := ndp.SummarizeTcpdump(lines, a, mac)
	fmt.Printf("multicast NS for %s: %d\nunicast NS: %d\nNA: %d\nNS sent by host MAC %s (host asking for the jail address): %d\n", a, m, u, na, mac, h)
	return 0
}

func sshArgv(peer, remote string) []string {
	return []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", peer, remote}
}

func expand(tmpl string, addr netip.Addr, mac ndp.MAC, dev string) string {
	r := strings.NewReplacer("{addr}", addr.String(), "{mac}", mac.String(), "{dev}", dev)
	return r.Replace(tmpl)
}
