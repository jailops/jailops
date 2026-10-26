package reconcile

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// hostPass asserts the kernel state of HostDesired: modules first, in the
// order given, then the sysctls (sorted). Everything goes through the runner,
// which in production is the root helper with its allow-lists; a value it
// refuses is reported as a failure, never worked around.
func (r *Reconciler) hostPass(ctx context.Context, h HostDesired, rep *Report) {
	if r.env.Run == nil {
		return
	}
	for _, m := range h.Modules {
		if _, err := r.env.Run.Run(ctx, run.Cmd{Name: "kldstat", Args: []string{"-q", "-n", m}, Timeout: 10 * time.Second}); err == nil {
			continue
		} else if _, isExit := run.IsExit(err); !isExit {
			rep.Failures["host/module/"+m] = err.Error()
			continue
		}
		if _, err := r.env.Run.Run(ctx, run.Cmd{Name: "kldload", Args: []string{m}, Timeout: 30 * time.Second}); err != nil {
			rep.Failures["host/module/"+m] = "kldload " + m + ": " + errText(err)
			continue
		}
		rep.Applied = append(rep.Applied, "kldload "+m)
	}
	names := make([]string, 0, len(h.Sysctls))
	for k := range h.Sysctls {
		names = append(names, k)
	}
	sort.Strings(names)
	set := func(name, want string) {
		got, err := r.readSysctl(ctx, name)
		if err != nil {
			rep.Failures["host/sysctl/"+name] = err.Error()
			return
		}
		if got == want {
			return
		}
		if _, err := r.env.Run.Run(ctx, run.Cmd{Name: "sysctl", Args: []string{name + "=" + want}, Timeout: 10 * time.Second}); err != nil {
			rep.Failures["host/sysctl/"+name] = "sysctl " + name + "=" + want + ": " + errText(err)
			return
		}
		rep.Applied = append(rep.Applied, "sysctl "+name+"="+want)
		r.mu.Lock()
		r.setSysctl[name] = true
		r.mu.Unlock()
	}
	for _, n := range names {
		set(n, h.Sysctls[n])
	}
	// Give back what nothing needs anymore, but only what this daemon set.
	rel := make([]string, 0, len(h.Release))
	for k := range h.Release {
		rel = append(rel, k)
	}
	sort.Strings(rel)
	for _, n := range rel {
		if _, wanted := h.Sysctls[n]; wanted {
			continue
		}
		r.mu.Lock()
		ours := r.setSysctl[n]
		r.mu.Unlock()
		if !ours {
			continue
		}
		set(n, h.Release[n])
		r.mu.Lock()
		delete(r.setSysctl, n)
		r.mu.Unlock()
	}
}

// AssertHost applies the host state h now, as a pass does (modules, then sysctls), and returns what failed. The image
// builder uses it to have the Linux modules loaded before it runs a build jail, without waiting for the loop.
func (r *Reconciler) AssertHost(ctx context.Context, h HostDesired) error {
	r.passMu.Lock()
	defer r.passMu.Unlock()
	rep := Report{Failures: map[string]string{}}
	r.hostPass(ctx, h, &rep)
	if len(rep.Failures) == 0 {
		return nil
	}
	keys := make([]string, 0, len(rep.Failures))
	for k := range rep.Failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+": "+rep.Failures[k])
	}
	return errors.New(strings.Join(parts, "; "))
}

func (r *Reconciler) readSysctl(ctx context.Context, name string) (string, error) {
	if r.env.Sysctl != nil {
		v, err := r.env.Sysctl(ctx, name)
		return strings.TrimSpace(v), err
	}
	return Sysctl(ctx, r.env.Run, name)
}

func errText(err error) string {
	if ee, ok := run.IsExit(err); ok && strings.TrimSpace(ee.Stderr) != "" {
		return strings.TrimSpace(ee.Stderr)
	}
	return err.Error()
}

var _ = api.CodeInternal
