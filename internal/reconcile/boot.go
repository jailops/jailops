package reconcile

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// StartOrder returns the jails to start, in order, given the autostart jails
// and the set already running. Rules:
//   - a jail starts after everything in boot.depends is running;
//   - a dependency that is not running is started first, even without
//     autostart (the operator declared the need);
//   - among jails that are ready, lower boot.priority starts first, then name;
//   - a dependency cycle or an unknown dependency excludes the jails involved
//     and is reported in skipped, never guessed around.
func StartOrder(jails []JailDesired, running map[string]bool) (order []string, skipped map[string]string) {
	by := map[string]JailDesired{}
	for _, j := range jails {
		by[j.Name] = j
	}
	skipped = map[string]string{}
	// The set to start: autostart jails not running plus their missing dependencies.
	want := map[string]bool{}
	var add func(name string, path []string) bool
	add = func(name string, path []string) bool {
		if running[name] || want[name] {
			return true
		}
		if slices.Contains(path, name) {
			for _, p := range path[slices.Index(path, name):] {
				skipped[p] = "dependency cycle: " + strings.Join(append(slices.Clone(path[slices.Index(path, name):]), name), " -> ")
			}
			return false
		}
		j, ok := by[name]
		if !ok {
			return false
		}
		ok2 := true
		for _, d := range j.Boot.Depends {
			if _, known := by[d]; !known && !running[d] {
				skipped[name] = fmt.Sprintf("depends on %q, which does not exist", d)
				ok2 = false
				continue
			}
			if !add(d, append(slices.Clone(path), name)) {
				if _, s := skipped[name]; !s {
					skipped[name] = fmt.Sprintf("dependency %q cannot be started", d)
				}
				ok2 = false
			}
		}
		if ok2 {
			want[name] = true
		}
		return ok2
	}
	var auto []string
	for _, j := range jails {
		if j.Boot.Autostart && !running[j.Name] {
			auto = append(auto, j.Name)
		}
	}
	slices.Sort(auto)
	for _, n := range auto {
		if _, bad := skipped[n]; bad {
			continue
		}
		if !add(n, nil) {
			if _, s := skipped[n]; !s {
				skipped[n] = "a dependency cannot be started"
			}
		}
	}
	// Kahn with a priority queue by (priority, name).
	done := map[string]bool{}
	for n := range running {
		done[n] = true
	}
	for len(done)-len(running) < len(want) {
		var ready []string
		for n := range want {
			if done[n] {
				continue
			}
			ok := true
			for _, d := range by[n].Boot.Depends {
				if !done[d] {
					ok = false
				}
			}
			if ok {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			for n := range want {
				if !done[n] {
					skipped[n] = "dependency cycle"
				}
			}
			break
		}
		slices.SortFunc(ready, func(a, b string) int {
			if pa, pb := by[a].Boot.Priority, by[b].Boot.Priority; pa != pb {
				return pa - pb
			}
			return strings.Compare(a, b)
		})
		order = append(order, ready[0])
		done[ready[0]] = true
	}
	return order, skipped
}

func (r *Reconciler) bootPass(ctx context.Context, des Desired, obs *Observed, rep *Report, now time.Time) {
	running := map[string]bool{}
	for n := range obs.Running {
		running[n] = true
	}
	order, skipped := StartOrder(des.Jails, running)
	r.mu.Lock()
	for n, why := range skipped {
		r.bootCond[n] = cond(CondBoot, false, "NotStarted", why, now)
		rep.Failures["jail/"+n] = why
	}
	r.mu.Unlock()
	failed := map[string]bool{}
	by := map[string]JailDesired{}
	for _, j := range des.Jails {
		by[j.Name] = j
	}
	for _, name := range order {
		blocked := ""
		for _, d := range by[name].Boot.Depends {
			if failed[d] {
				blocked = d
			}
		}
		if blocked != "" {
			failed[name] = true
			r.setBootCond(name, cond(CondBoot, false, "DependencyFailed", fmt.Sprintf("dependency %q did not start", blocked), now))
			continue
		}
		if err := r.env.Actions.StartJail(ctx, name); err != nil {
			failed[name] = true
			rep.Failures["jail/"+name] = err.Error()
			r.setBootCond(name, cond(CondBoot, false, "StartFailed", err.Error(), now))
			continue
		}
		rep.Started = append(rep.Started, name)
		r.setBootCond(name, cond(CondBoot, true, "Started", "started at boot", now))
	}
}

func (r *Reconciler) setBootCond(name string, c api.Condition) {
	r.mu.Lock()
	r.bootCond[name] = c
	r.mu.Unlock()
}
