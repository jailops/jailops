package api

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxCPUSetCPU is the highest CPU number a cpuset names (FreeBSD's MAXCPU is 1024).
const MaxCPUSetCPU = 1023

// ParseCPUSet reads a CPU list in the syntax of cpuset(1) -l: numbers and ranges separated by commas, "2,3" or
// "0-3,6". It answers the CPUs, sorted and without duplicates.
func ParseCPUSet(v string) ([]int, error) {
	if v == "" {
		return nil, fmt.Errorf("a cpuset names at least one CPU")
	}
	var cpus []int
	for _, part := range strings.Split(v, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := cpuNumber(lo)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CPU list such as 2,3 or 0-3,6", v)
		}
		b := a
		if isRange {
			if b, err = cpuNumber(hi); err != nil || b < a {
				return nil, fmt.Errorf("%q is not a CPU list such as 2,3 or 0-3,6", v)
			}
		}
		for c := a; c <= b; c++ {
			cpus = append(cpus, c)
		}
	}
	slices.Sort(cpus)
	return slices.Compact(cpus), nil
}

func cpuNumber(s string) (int, error) {
	if s == "" || len(s) > 4 || strings.TrimLeft(s, "0123456789") != "" {
		return 0, fmt.Errorf("not a number")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > MaxCPUSetCPU {
		return 0, fmt.Errorf("not a CPU")
	}
	return n, nil
}

// CanonicalCPUSet is the list in its canonical form: sorted, merged into ranges ("0,1,2,5" is "0-2,5").
func CanonicalCPUSet(cpus []int) string {
	var out []string
	for i := 0; i < len(cpus); {
		j := i
		for j+1 < len(cpus) && cpus[j+1] == cpus[j]+1 {
			j++
		}
		if j > i {
			out = append(out, fmt.Sprintf("%d-%d", cpus[i], cpus[j]))
		} else {
			out = append(out, strconv.Itoa(cpus[i]))
		}
		i = j + 1
	}
	return strings.Join(out, ",")
}

// NormalizeCPUSet checks a CPU list and answers it in canonical form.
func NormalizeCPUSet(v string) (string, error) {
	cpus, err := ParseCPUSet(v)
	if err != nil {
		return "", err
	}
	return CanonicalCPUSet(cpus), nil
}
