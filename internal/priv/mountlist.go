package priv

import (
	"context"
	"os/exec"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// ReadMounts lists the mount points of the host with mount(8) -p (no privilege needed), for the reconcile loop to
// compare with the fstab of a running jail.
func ReadMounts() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netstatTimeout)
	defer cancel()
	b, err := exec.CommandContext(ctx, "/sbin/mount", "-p").Output()
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "mount: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 3 {
			out = append(out, f[1])
		}
	}
	return out, nil
}
