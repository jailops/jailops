package priv

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// rctlRules are the rctl(8) rules that enforce the resource limits of a jail, one per limit that is set. The kernel
// keeps a rule by the jail's name after the jail is gone, and a second start would add the rules again, so they are
// removed when the jail stops and before they are added.
//
//   - limits.memory is swapuse: the anonymous memory the jail may reserve. An allocation beyond it fails and nothing
//     is killed. vmemoryuse counts every mapping, shared libraries and sparse arenas included (an idle PostgreSQL
//     maps 1.8 GB of address space for 0.3 GB of memory), and memoryuse does not refuse an allocation: deny on it is
//     not enforced, and sigkill kills the process.
//   - limits.pids is maxproc: fork fails when the jail has that many processes.
//   - limits.cpu is pcpu in percent of one core: a jail over its share is slowed down, not killed.
func rctlRules(jail string, r api.Resources) ([]string, error) {
	var out []string
	if r.Memory != "" {
		b, err := validate.ParseSize(r.Memory)
		if err != nil {
			return nil, fmt.Errorf("limits.memory %q: %w", r.Memory, err)
		}
		if b > 0 {
			out = append(out, fmt.Sprintf("jail:%s:swapuse:deny=%d", jail, b))
		}
	}
	if r.PIDs > 0 {
		out = append(out, fmt.Sprintf("jail:%s:maxproc:deny=%d", jail, r.PIDs))
	}
	if r.CPU > 0 {
		out = append(out, fmt.Sprintf("jail:%s:pcpu:deny=%d", jail, r.CPU*100))
	}
	return out, nil
}

// applyLimits adds the rctl rules of a jail that has just started. A host where kern.racct.enable is 0 refuses every
// rule; the jail still starts, doctor fails and the jail reports that its limits are not enforced.
func (l *Local) applyLimits(ctx context.Context, jn JailName) error {
	rec, err := l.loadRecord(ctx, jn)
	if err != nil {
		return err
	}
	// the cpuset needs no accounting: it is set whether or not racct is on
	if cs := rec.Spec.Resources.CPUSet; cs != "" {
		if err := l.applyCPUSet(ctx, jn, cs); err != nil {
			return err
		}
	}
	rules, err := rctlRules(jn.Internal(), rec.Spec.Resources)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}
	if err := l.dropLimits(ctx, jn); err != nil {
		return err
	}
	for _, rule := range rules {
		_, err := l.d.Runner.Run(ctx, run.Cmd{Name: "rctl", Args: []string{"-a", rule}, Timeout: 10 * time.Second})
		if ee, ok := run.IsExit(err); ok {
			if strings.Contains(strings.ToLower(ee.Stderr), "disabled") {
				return nil
			}
			return api.Errorf(api.CodeInternal, "rctl -a %s: %s", rule, strings.TrimSpace(ee.Stderr))
		} else if err != nil {
			return fmt.Errorf("rctl -a %s: %w", rule, err)
		}
	}
	return nil
}

// dropLimits removes every rctl rule of a jail. A jail without rules, and a host without racct, are not an error.
func (l *Local) dropLimits(ctx context.Context, jn JailName) error {
	_, err := l.d.Runner.Run(ctx, run.Cmd{Name: "rctl", Args: []string{"-r", "jail:" + jn.Internal()}, Timeout: 10 * time.Second})
	if ee, ok := run.IsExit(err); ok {
		low := strings.ToLower(ee.Stderr)
		if strings.Contains(low, "no such process") || strings.Contains(low, "disabled") {
			return nil
		}
		return api.Errorf(api.CodeInternal, "rctl -r jail:%s: %s", jn.Internal(), strings.TrimSpace(ee.Stderr))
	}
	return err
}

// applyCPUSet pins a jail that has just started to the CPUs of limits.cpuset (cpuset(1) -l LIST -j JID). The set of
// the jail is the one its processes share, so the processes the jail has started already move with it. A CPU the
// host does not have stops the start: a limit that is not enforced would be a promise nobody keeps, and the message
// says what the host has.
func (l *Local) applyCPUSet(ctx context.Context, jn JailName, set string) error {
	cpus, err := api.ParseCPUSet(set)
	if err != nil {
		return fmt.Errorf("limits.cpuset %q: %w", set, err)
	}
	res, err := l.d.Runner.Run(ctx, run.Cmd{Name: "sysctl", Args: []string{"-n", "hw.ncpu"}, Timeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("sysctl hw.ncpu: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil || n < 1 {
		return fmt.Errorf("sysctl hw.ncpu answered %q", strings.TrimSpace(string(res.Stdout)))
	}
	if top := cpus[len(cpus)-1]; top >= n {
		return api.Errorf(api.CodeInvalid, "limits.cpuset %s names CPU %d, and this host has %d CPUs (0-%d)", set, top, n, n-1)
	}
	res, err = l.d.Runner.Run(ctx, run.Cmd{Name: "jls", Args: []string{"-j", jn.Internal(), "jid"}, Timeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("jls -j %s jid: %w", jn.Internal(), err)
	}
	jid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil || jid < 1 {
		return fmt.Errorf("jls -j %s jid answered %q", jn.Internal(), strings.TrimSpace(string(res.Stdout)))
	}
	if _, err := l.d.Runner.Run(ctx, run.Cmd{Name: "cpuset", Args: []string{"-l", set, "-j", strconv.Itoa(jid)}, Timeout: 10 * time.Second}); err != nil {
		if ee, ok := run.IsExit(err); ok {
			return api.Errorf(api.CodeInternal, "cpuset -l %s -j %d: %s", set, jid, strings.TrimSpace(ee.Stderr))
		}
		return fmt.Errorf("cpuset -l %s -j %d: %w", set, jid, err)
	}
	return nil
}
