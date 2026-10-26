package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/isolation"
)

// wdState is the file shared by the main process, the detached helper and --cleanup. Restoring
// means: remove the three jails, flush the jop8 PF anchors, destroy the three epairs and the
// bridge, and unmount the per-jail nullfs mounts. None of it depends on the uplink or any other
// bridge, so unlike prototype 5 there is no connectivity probe to the outside world here.
type wdState struct {
	Pid       int               `json:"pid"`
	Deadline  time.Time         `json:"deadline"`
	MountBase string            `json:"mount_base"`
	Sysctls   map[string]string `json:"sysctls,omitempty"` // name -> value before this run touched it
	Done      bool              `json:"done"`
}

func (s *wdState) save(path string) error {
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadState(path string) (*wdState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s wdState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// watchdog is the in-process half; the detached helper (helperMain) is the half that survives
// the operator's session dying.
type watchdog struct {
	stop    chan struct{}
	done    sync.WaitGroup
	mu      sync.Mutex
	tripped bool
}

// armWatchdog writes the state file, starts the detached helper, and starts an in-process
// monitor for the hard deadline. It must run before anything else is created.
func armWatchdog(e *harness.Env) *watchdog {
	w := &watchdog{}
	if e.DryRun {
		e.Note("(dry run) would write %s, start the detached helper `%s --watchdog-helper`, and restore everything after %ds or if this process dies",
			defaultState, os.Args[0], watchdogS)
		return w
	}
	if old, err := loadState(defaultState); err == nil && !old.Done {
		e.Abort("state file %s has an unfinished run (pid %d): run --cleanup first", defaultState, old.Pid)
	}
	st := &wdState{Pid: os.Getpid(), Deadline: time.Now().Add(time.Duration(watchdogS) * time.Second), MountBase: mountBase}
	if err := st.save(defaultState); err != nil {
		e.Abort("state file: %v", err)
	}
	if err := spawnHelper(defaultState); err != nil {
		e.Abort("cannot start the detached watchdog helper: %v (refusing to continue without it)", err)
	}
	w.stop = make(chan struct{})
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-tk.C:
			}
			if time.Now().After(st.Deadline) {
				w.mu.Lock()
				w.tripped = true
				w.mu.Unlock()
				return
			}
		}
	}()
	e.Note("watchdog armed: hard deadline %ds from now; detached helper logs to %s.log", watchdogS, defaultState)
	w.recordSysctlBaseline(e, st)
	return w
}

// recordSysctlBaseline snapshots the five sysctls this prototype may change into the state file
// before touching any of them, so the detached helper can restore them even if the main process
// dies mid-run (its own in-memory defers would then never run).
func (w *watchdog) recordSysctlBaseline(e *harness.Env, st *wdState) {
	st.Sysctls = map[string]string{}
	for _, name := range []string{isolation.SysctlPfilMember, isolation.SysctlPfilBridge, isolation.SysctlPfilOnlyIP,
		"net.inet.ip.forwarding", "net.inet6.ip6.forwarding"} {
		st.Sysctls[name] = e.Quiet("sysctl", "-n", name).Out()
	}
	_ = st.save(defaultState)
}

// finish stops the in-process monitor and marks the state done so the helper exits quietly.
func (w *watchdog) finish(e *harness.Env) {
	if w.stop != nil {
		close(w.stop)
		w.done.Wait()
	}
	if e.DryRun {
		return
	}
	if s, err := loadState(defaultState); err == nil {
		s.Done = true
		_ = s.save(defaultState)
	}
	w.mu.Lock()
	tripped := w.tripped
	w.mu.Unlock()
	if tripped {
		e.Abort("watchdog hard deadline was reached during the run")
	}
}

// cleanupWatchdog marks any state file done so a stray helper exits; --cleanup still does the
// real teardown itself (through the harness, with its printed commands).
func cleanupWatchdog(e *harness.Env) {
	if s, err := loadState(defaultState); err == nil {
		s.Done = true
		_ = s.save(defaultState)
	} else if e.DryRun {
		e.Note("(dry run) would mark %s done if it existed", defaultState)
	}
}

// helperMain is the detached watchdog process: independent of the main process and its Env.
func helperMain(state string) int {
	fmt.Printf("helper %d started for %s\n", os.Getpid(), state)
	for {
		st, err := loadState(state)
		if err != nil || st.Done {
			fmt.Println("state gone or done: helper exits")
			return 0
		}
		if !alive(st.Pid) || time.Now().After(st.Deadline) {
			fmt.Printf("%s: HELPER RESTORING (pid alive=%v deadline passed=%v)\n",
				time.Now().Format(time.RFC3339), alive(st.Pid), time.Now().After(st.Deadline))
			restoreAll(st.MountBase)
			restoreSysctlsRaw(st.Sysctls)
			st.Done = true
			_ = st.save(state)
			return 0
		}
		time.Sleep(2 * time.Second)
	}
}

// restoreAll tears down everything this prototype can have created, without the harness: the
// helper runs detached and may outlive the main process. Every command is built from the fixed
// jop8 names in internal/isolation, never from external input.
func restoreAll(mountBase string) {
	run := func(argv ...string) { _ = exec.Command(argv[0], argv[1:]...).Run() }
	for _, jail := range isolation.Jails {
		run("jail", "-r", jail)
	}
	for _, jail := range isolation.Jails {
		run("pfctl", "-a", isolation.JailAnchor(jail), "-F", "all")
	}
	for _, p := range []string{isolation.ProjectA, isolation.ProjectB} {
		run("pfctl", "-a", isolation.ProjectAnchor(p), "-F", "all")
	}
	if mountBase == "" {
		mountBase = "/jop-proto-8"
	}
	for _, jail := range isolation.Jails {
		run("umount", mountBase+"/"+jail+"/tmp")
		run("umount", mountBase+"/"+jail)
	}
	for _, jail := range isolation.Jails {
		run("ifconfig", isolation.EpairHost(jail), "destroy")
	}
	run("ifconfig", isolation.Bridge, "destroy")
}

// restoreSysctlsRaw restores the five sysctls to their pre-run values, if they were recorded and
// are non-empty, using only the fixed allow-listed names.
func restoreSysctlsRaw(prev map[string]string) {
	for _, name := range []string{isolation.SysctlPfilMember, isolation.SysctlPfilBridge, isolation.SysctlPfilOnlyIP,
		"net.inet.ip.forwarding", "net.inet6.ip6.forwarding"} {
		if v := prev[name]; v != "" && isolation.ValidSysctlName(name) {
			_ = exec.Command("sysctl", name+"="+v).Run()
		}
	}
}
