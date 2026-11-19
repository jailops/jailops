// Package top holds what `jailops top` measures: the parsers of the host tools it reads (sysctl, netstat, ps,
// zpool), the arithmetic of rates, and the sampler that keeps the last readings so that a frame can say how fast
// something moves. Nothing here talks to the helper or to the service: the readings come in as text, so the
// formats are tested without a host.
package top

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Exec runs a read-only host tool that needs no privilege and returns its standard output. The tools are
// sysctl(8), netstat(1) and ps(1); each is called with fixed arguments. A nil Exec means the host cannot
// be read here, and a frame leaves the host part out.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecDirect runs one of the read-only tools (sysctl, netstat, ps and pkg info) with os/exec, with a timeout of its own so that a tool that does not answer cannot hold
// a frame.
func ExecDirect(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	path, ok := toolPaths[name]
	if !ok {
		return nil, fmt.Errorf("top: %q is not one of the tools it reads", name)
	}
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

const toolTimeout = 3 * time.Second

var toolPaths = map[string]string{
	"sysctl":  "/sbin/sysctl",
	"netstat": "/usr/bin/netstat",
	"ps":      "/bin/ps",
	"pkg":     "/usr/sbin/pkg",
}

// SysctlNames are the sysctls of the host header, read with one call of `sysctl -e`: that prints name=value for
// each, so a name this kernel lacks leaves a gap and never shifts the others.
var SysctlNames = []string{
	"vm.loadavg", "kern.cp_time", "hw.ncpu", "hw.physmem",
	"vm.stats.vm.v_wire_count", "vm.stats.vm.v_active_count", "vm.stats.vm.v_page_size",
	"kstat.zfs.misc.arcstats.size",
}

// Host is the raw host reading of one moment.
type Host struct {
	At       time.Time
	Load     [3]float64
	CPUTimes [5]uint64 // user, nice, system, interrupt, idle ticks since boot, all CPUs together
	HaveCPU  bool
	NCPU     int
	PhysMem  uint64
	// Wired and Active are page counts; PageSize is in bytes. HaveMem is set when all three were read.
	Wired, Active, PageSize uint64
	HaveMem                 bool
	ARC                     uint64
	HaveARC                 bool
	HaveLoad                bool
}

// ParseSysctl reads the output of `sysctl -e` for SysctlNames. A name that is missing or malformed leaves its
// part of Host unset; only a reading with nothing usable at all is an error.
func ParseSysctl(out string, at time.Time) (Host, error) {
	h := Host{At: at}
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && name != "" {
			vals[name] = strings.TrimSpace(val)
		}
	}
	if v, ok := vals["vm.loadavg"]; ok {
		f := strings.Fields(strings.Trim(v, "{} "))
		if len(f) >= 3 {
			var l [3]float64
			good := true
			for i := range l {
				x, err := strconv.ParseFloat(f[i], 64)
				if err != nil || x < 0 {
					good = false
				}
				l[i] = x
			}
			if good {
				h.Load, h.HaveLoad = l, true
			}
		}
	}
	if v, ok := vals["kern.cp_time"]; ok {
		f := strings.Fields(v)
		if len(f) == 5 {
			good := true
			for i := range h.CPUTimes {
				x, err := strconv.ParseUint(f[i], 10, 64)
				if err != nil {
					good = false
				}
				h.CPUTimes[i] = x
			}
			h.HaveCPU = good
		}
	}
	num := func(name string) (uint64, bool) {
		v, ok := vals[name]
		if !ok {
			return 0, false
		}
		x, err := strconv.ParseUint(v, 10, 64)
		return x, err == nil
	}
	if n, ok := num("hw.ncpu"); ok && n > 0 && n < 1<<16 {
		h.NCPU = int(n)
	}
	h.PhysMem, _ = num("hw.physmem")
	w, ok1 := num("vm.stats.vm.v_wire_count")
	a, ok2 := num("vm.stats.vm.v_active_count")
	p, ok3 := num("vm.stats.vm.v_page_size")
	if ok1 && ok2 && ok3 && p > 0 {
		h.Wired, h.Active, h.PageSize, h.HaveMem = w, a, p, true
	}
	h.ARC, h.HaveARC = num("kstat.zfs.misc.arcstats.size")
	if !h.HaveLoad && !h.HaveCPU && h.NCPU == 0 && h.PhysMem == 0 && !h.HaveMem && !h.HaveARC {
		return Host{}, fmt.Errorf("sysctl printed none of the host numbers")
	}
	return h, nil
}

// CPUBusy is the share of the CPUs that were not idle between two readings, 0 to 100. It is not known when the
// counters did not move or went backwards.
func CPUBusy(prev, cur Host) (float64, bool) {
	if !prev.HaveCPU || !cur.HaveCPU {
		return 0, false
	}
	var total, idle uint64
	for i := range cur.CPUTimes {
		if cur.CPUTimes[i] < prev.CPUTimes[i] {
			return 0, false
		}
		d := cur.CPUTimes[i] - prev.CPUTimes[i]
		total += d
		if i == 4 {
			idle = d
		}
	}
	if total == 0 {
		return 0, false
	}
	return 100 * float64(total-idle) / float64(total), true
}

// IfCounters are the byte counters of one interface as netstat(1) prints them: In is what the interface received,
// Out what it sent.
type IfCounters struct {
	InBytes, OutBytes uint64
}

// ParseNetstat reads `netstat -ibnW -f link`: one row per interface with the columns
// Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll. The Address column is empty on some
// interfaces, so the numbers are taken from the end of the row: the last eight fields are the counters.
func ParseNetstat(out string) map[string]IfCounters {
	res := map[string]IfCounters{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 9 || f[0] == "Name" {
			continue
		}
		var n [8]uint64
		good := true
		for i := range n {
			x, err := strconv.ParseUint(f[len(f)-8+i], 10, 64)
			if err != nil {
				good = false
				break
			}
			n[i] = x
		}
		if !good {
			continue
		}
		// Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll
		res[f[0]] = IfCounters{InBytes: n[3], OutBytes: n[6]}
	}
	return res
}

// Rate is the bytes per second between two counter readings; ok is false when the counter went backwards (the
// interface was recreated) or no time passed.
func Rate(prev, cur uint64, dt time.Duration) (int64, bool) {
	if cur < prev || dt <= 0 {
		return 0, false
	}
	return int64(float64(cur-prev) / dt.Seconds()), true
}

// ParsePS reads `ps -ax -J JID -o pcpu=,rss=`: the number of processes of the jail, the CPU they use in percent
// of one core and their resident memory in bytes (ps prints kilobytes).
func ParsePS(out string) (procs int, cpu float64, rssBytes int64) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		c, err1 := strconv.ParseFloat(f[0], 64)
		r, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil || c < 0 || r < 0 {
			continue
		}
		procs++
		cpu += c
		rssBytes += r * 1024
	}
	return procs, cpu, rssBytes
}
