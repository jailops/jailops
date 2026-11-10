package reconcile

import (
	"context"
	"fmt"
	"time"

	"jailops.net/jailops/sdk/api"
)

// StartOrder returns the jails to start, in order, given the jails and the set already running (the rules are those
// of api.BootOrder, which the boot plan of the API shares).
func StartOrder(jails []JailDesired, running map[string]bool) (order []string, skipped map[string]string) {
	nodes := make([]api.BootNode, len(jails))
	for i, j := range jails {
		nodes[i] = api.BootNode{Key: j.Name, Boot: j.Boot}
	}
	return api.BootOrder(nodes, running)
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
			if api.IsCode(err, api.CodeLocked) {
				// Not a failure: the jail waits for the passphrase of an encrypted volume and starts after its unlock.
				r.setBootCond(name, cond(CondBoot, false, "Locked", "waiting for the passphrase of an encrypted volume (jailops jail unlock)", now))
				continue
			}
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
