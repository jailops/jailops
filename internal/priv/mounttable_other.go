//go:build !freebsd

package priv

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// systemMountTable on a non-FreeBSD host (development and unit tests only; the helper runs on FreeBSD) falls back to
// mount(8). It is not a security boundary here, so the exec is acceptable.
func systemMountTable() ([]mountEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "mount", "-p").Output()
	if err != nil {
		return nil, err
	}
	var out []mountEntry
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 3 {
			e := mountEntry{dev: f[0], mp: f[1], typ: f[2]}
			if len(f) >= 4 {
				e.nosuid = slices.Contains(strings.Split(f[3], ","), "nosuid")
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func systemForceUnmount(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "umount", "-f", path).Run()
}
