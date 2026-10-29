package jailos

import (
	"context"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Usage is the resource accounting of one running jail as rctl(8) reports
// it: `rctl -u jail:NAME` prints one "resource=amount" line per racct
// counter. The three that matter are named; the rest stay in Counters.
//
// UNVERIFIED: the format follows rctl(8); it has not been read from a host.
type Usage struct {
	Processes   int   // maxproc: number of processes
	CPUSeconds  int64 // cputime: CPU time in seconds, all cores added up
	MemoryBytes int64 // memoryuse: resident set size in bytes
	// Counters holds every counter of the output by its rctl(8) name.
	Counters map[string]int64
}

// ParseRctlUsage parses the output of `rctl -u`. Blank lines are skipped; a
// line that is not "name=integer" is an error, and so is empty output.
func ParseRctlUsage(out string) (Usage, error) {
	u := Usage{Counters: map[string]int64{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		if !ok || name == "" {
			return Usage{}, api.Errorf(api.CodeInternal, "cannot parse rctl output: %q", line)
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil || n < 0 {
			return Usage{}, api.Errorf(api.CodeInternal, "cannot parse rctl output: %q", line)
		}
		u.Counters[name] = n
	}
	if len(u.Counters) == 0 {
		return Usage{}, api.Errorf(api.CodeInternal, "rctl printed no counters")
	}
	u.Processes = int(u.Counters["maxproc"])
	u.CPUSeconds = u.Counters["cputime"]
	u.MemoryBytes = u.Counters["memoryuse"]
	return u, nil
}

// ReadUsage reads the accounting of the running jail name through r.
func ReadUsage(ctx context.Context, r run.Runner, name string) (Usage, error) {
	if err := ValidateName(name); err != nil {
		return Usage{}, err
	}
	res, err := r.Run(ctx, run.Cmd{Name: "rctl", Args: []string{"-u", "jail:" + name}, Timeout: 10 * time.Second})
	if err != nil {
		if ee, ok := run.IsExit(err); ok && strings.TrimSpace(ee.Stderr) != "" {
			return Usage{}, api.Errorf(api.CodeInternal, "rctl: %s", strings.TrimSpace(ee.Stderr))
		}
		return Usage{}, api.Errorf(api.CodeInternal, "rctl: %v", err)
	}
	return ParseRctlUsage(string(res.Stdout))
}
