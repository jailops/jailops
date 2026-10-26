package ndp

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// NamePrefix is the mandatory prefix of everything prototype 5 creates.
const NamePrefix = "jop"

// WD is the watchdog state machine: restore when the connectivity probe has failed for
// FailAfter without a success in between, or when the hard deadline passes. It also keeps the
// probe statistics used by acceptance test 5.
type WD struct {
	Deadline  time.Time
	FailAfter time.Duration

	Oks, Fails int
	MaxRun     int // longest run of consecutive failed probes
	run        int
	failSince  time.Time
}

// Observe feeds one probe result; restore is true when the state must be rolled back now.
func (w *WD) Observe(now time.Time, ok bool) (restore bool, reason string) {
	if ok {
		w.Oks++
		w.run = 0
		w.failSince = time.Time{}
	} else {
		w.Fails++
		w.run++
		if w.run > w.MaxRun {
			w.MaxRun = w.run
		}
		if w.failSince.IsZero() {
			w.failSince = now
		}
		if w.FailAfter > 0 && now.Sub(w.failSince) >= w.FailAfter {
			return true, fmt.Sprintf("probe failing for %s", now.Sub(w.failSince).Round(time.Second))
		}
	}
	if !w.Deadline.IsZero() && !now.Before(w.Deadline) {
		return true, "hard deadline reached"
	}
	return false, ""
}

// State is the file shared by the main process, the detached watchdog helper and --cleanup.
type State struct {
	Version     int        `json:"version"`
	Pid         int        `json:"pid"`
	Deadline    time.Time  `json:"deadline"`
	ProbeTarget string     `json:"probe_target"`
	FailSecs    int        `json:"fail_secs"`
	Undo        [][]string `json:"undo"` // executed in reverse order
	Done        bool       `json:"done"`
	Reason      string     `json:"reason,omitempty"`
}

// Save writes the state atomically (temp file and rename).
func (s *State) Save(path string) error {
	s.Version = 1
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

// LoadState reads a state file.
func LoadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

var (
	jopName  = regexp.MustCompile(`^jop[a-z0-9_]{1,11}$`)
	ifName   = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,14}$`)
	fwdValue = regexp.MustCompile(`^net\.inet6\.ip6\.forwarding=[01]$`)
)

// ValidateUndo is the allow-list for the undo commands, enforced both when an undo is
// recorded and again when the helper or --cleanup executes it from the state file, so a
// tampered state file cannot run anything else. Allowed:
//
//	ifconfig jop<x> destroy
//	ifconfig jop<x> deletem <member>
//	route delete -inet6 <addr>/128
//	ndp -d <addr>
//	jail -r jop<x>
//	sysctl net.inet6.ip6.forwarding=0|1
func ValidateUndo(a []string) error {
	bad := fmt.Errorf("undo command not allowed: %q", strings.Join(a, " "))
	switch {
	case len(a) == 3 && a[0] == "ifconfig" && jopName.MatchString(a[1]) && a[2] == "destroy":
		return nil
	case len(a) == 4 && a[0] == "ifconfig" && jopName.MatchString(a[1]) && a[2] == "deletem" && ifName.MatchString(a[3]):
		return nil
	case len(a) == 4 && a[0] == "route" && a[1] == "delete" && a[2] == "-inet6" && validHost128(a[3]):
		return nil
	case len(a) == 3 && a[0] == "ndp" && a[1] == "-d" && validAddr(a[2]):
		return nil
	case len(a) == 3 && a[0] == "jail" && a[1] == "-r" && jopName.MatchString(a[2]):
		return nil
	case len(a) == 2 && a[0] == "sysctl" && fwdValue.MatchString(a[1]):
		return nil
	}
	return bad
}

func validAddr(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && !a.IsUnspecified() && !a.IsLoopback() && !a.IsMulticast() && a.Zone() == ""
}

func validHost128(s string) bool {
	p, err := netip.ParsePrefix(s)
	return err == nil && p.Bits() == 128 && validAddr(p.Addr().String())
}

// RunUndo executes the undo list in reverse through run, validating each entry. It never stops
// at an error (a half-built lab must still be torn down as far as possible).
func RunUndo(undo [][]string, run func([]string) error) []error {
	var errs []error
	for i := len(undo) - 1; i >= 0; i-- {
		if err := ValidateUndo(undo[i]); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := run(undo[i]); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", strings.Join(undo[i], " "), err))
		}
	}
	return errs
}
