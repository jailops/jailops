package reconcile

import (
	"context"
	"slices"
	"strings"
	"time"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
)

// staleAnchorGrace is how long an anchor below jailops/ must have belonged to no object, pass after pass, before the
// loop flushes it. An object that is being made or renamed loads its anchor before every pass sees the object: the
// anchor is left alone until that is over.
const staleAnchorGrace = 2 * time.Minute

// staleAnchorPass flushes the anchors below jailops/ that belong to no jail, project or network: a jail deleted while
// its poststop did not run, a project or network removed by an older version, a jail renamed by hand. A stale anchor
// is not harmless: the address it names is handed to the next jail, and a forward or an uplink pass in it would expose
// that jail. Only these shapes are ever flushed: jailops/<project>/<jail>, jailops/<project> and jailops/net.<network>;
// the tunnels (jailops/wg.*) and any other name with a dot are left to their own code. Nothing is flushed when the
// anchors cannot be listed, when an anchor belongs to a jail the kernel runs, or before it was stale for
// staleAnchorGrace. The caller computed des without an error.
func (r *Reconciler) staleAnchorPass(ctx context.Context, des Desired, obs *Observed, rep *Report) {
	if r.env.PF == nil || r.env.Run == nil {
		return
	}
	listed, err := r.listAnchors(ctx)
	if err != nil {
		r.log.Debug("reconcile: the anchors below jailops cannot be listed: none is flushed in this pass", "err", err)
		return
	}
	keep := map[string]bool{}
	for _, j := range des.Jails {
		keep[render.JailAnchor(j.Name)] = true
		if j.AnchorName != "" {
			keep[j.AnchorName] = true
		}
		if j.Project != "" {
			keep[render.ProjectAnchor(j.Project)] = true
		}
		for an := range j.ParentAnchors {
			keep[an] = true
		}
	}
	for _, n := range des.Networks {
		keep[render.NetAnchor(n.Name)] = true
		for an := range n.Anchors {
			keep[an] = true
		}
	}
	for _, w := range des.WireGuard {
		if w.Anchor != "" {
			keep[w.Anchor] = true
		}
	}
	now := r.env.Clock.Now()
	stale := map[string]bool{}
	for _, an := range listed {
		if !keep[an] && staleShape(an) && !runsUnder(an, obs) {
			stale[an] = true
		}
	}
	r.mu.Lock()
	if r.staleSince == nil {
		r.staleSince = map[string]time.Time{}
	}
	for an := range r.staleSince {
		if !stale[an] {
			delete(r.staleSince, an) // it belongs to an object again, or it is gone
		}
	}
	var due []string
	for an := range stale {
		since, seen := r.staleSince[an]
		if !seen {
			r.staleSince[an] = now
			continue
		}
		if now.Sub(since) >= staleAnchorGrace {
			due = append(due, an)
		}
	}
	r.mu.Unlock()
	// the deepest first: a project anchor goes only once the anchors of its jails are gone
	slices.SortFunc(due, func(a, b string) int {
		if da, db := strings.Count(a, "/"), strings.Count(b, "/"); da != db {
			return db - da
		}
		return strings.Compare(a, b)
	})
	left := map[string]bool{}
	for _, an := range listed {
		left[an] = true
	}
	for _, an := range due {
		if hasChild(an, left) {
			continue // a child is not stale yet, or could not be flushed
		}
		if r.flushStale(ctx, an) {
			delete(left, an)
			r.mu.Lock()
			delete(r.staleSince, an)
			r.mu.Unlock()
			rep.Applied = append(rep.Applied, "pfctl -a "+an+" -F all (belongs to no jail, project or network)")
			r.log.Info("reconcile: flushed an anchor that belongs to no jail, project or network", "anchor", an)
		}
	}
}

// flushStale ends the flows of a stale anchor's rules and flushes it, under the locks of what it belonged to.
func (r *Reconciler) flushStale(ctx context.Context, an string) bool {
	rest := strings.TrimPrefix(an, render.AnchorRoot+"/")
	var keys []string
	switch p, n, nested := strings.Cut(rest, "/"); {
	case nested:
		keys = []string{"jail/" + p + "_" + n, "anchor/" + p + "_" + n}
	case strings.HasPrefix(rest, "net."):
		keys = []string{"network/" + strings.TrimPrefix(rest, "net.")}
	default:
		keys = []string{"project/" + rest, "jail/" + rest, "anchor/" + rest}
	}
	unlock, err := r.env.Actions.TryLock("reconcile stale "+an, keys...)
	if err != nil {
		return false
	}
	defer unlock()
	old, err := r.env.PF.Read(ctx, an)
	if err != nil {
		return false
	}
	var labels []string
	for l := range pfctl.LabelsOf(old) {
		labels = append(labels, l)
	}
	slices.Sort(labels)
	added, err := r.keepKills(an, labels...)
	if err != nil {
		r.dropKills(an, added...)
		return false
	}
	if err := r.env.PF.Flush(ctx, an); err != nil {
		r.dropKills(an, added...)
		r.log.Warn("reconcile: flushing an anchor that belongs to nothing failed: tried again at the next pass", "anchor", an, "err", err)
		return false
	}
	for _, l := range labels {
		if err := r.env.PF.KillLabel(ctx, l); err == nil {
			r.dropKills(an, l)
		}
	}
	return true
}

// staleShape reports whether a name is one of the shapes the loop may flush when nothing owns it.
func staleShape(an string) bool {
	rest, ok := strings.CutPrefix(an, render.AnchorRoot+"/")
	if !ok || pfctl.ValidateAnchor(an) != nil {
		return false
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		return !strings.Contains(rest, ".") || strings.HasPrefix(rest, "net.") && len(rest) > len("net.")
	case 2:
		return !strings.Contains(parts[0], ".") && !strings.Contains(parts[1], ".")
	}
	return false
}

// runsUnder reports whether the kernel runs a jail whose anchor is an, or lies below it.
func runsUnder(an string, obs *Observed) bool {
	rest := strings.TrimPrefix(an, render.AnchorRoot+"/")
	if p, n, nested := strings.Cut(rest, "/"); nested {
		_, up := obs.Running[p+"_"+n]
		return up
	}
	if strings.HasPrefix(rest, "net.") {
		return false
	}
	if _, up := obs.Running[rest]; up {
		return true
	}
	for name := range obs.Running {
		if strings.HasPrefix(name, rest+"_") {
			return true
		}
	}
	return false
}

func hasChild(an string, left map[string]bool) bool {
	for o := range left {
		if strings.HasPrefix(o, an+"/") {
			return true
		}
	}
	return false
}

// listAnchors lists the anchors below jailops/ through the read-only "pfctl -a <anchor> -s Anchors": the top level,
// then every project anchor (pfctl prints the whole tree on FreeBSD 15.2; an older one prints one level).
func (r *Reconciler) listAnchors(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	list := func(an string) ([]string, error) {
		res, err := r.env.Run.Run(ctx, run.Cmd{Name: "pfctl", Args: []string{"-a", an, "-s", "Anchors"}, Timeout: 10 * time.Second})
		if err != nil {
			return nil, err
		}
		var got []string
		for _, ln := range strings.Split(string(res.Stdout), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, render.AnchorRoot+"/") && pfctl.ValidateAnchor(ln) == nil && !seen[ln] {
				seen[ln] = true
				got = append(got, ln)
			}
		}
		return got, nil
	}
	top, err := list(render.AnchorRoot)
	if err != nil {
		return nil, err
	}
	out = append(out, top...)
	for _, an := range top {
		rest := strings.TrimPrefix(an, render.AnchorRoot+"/")
		if strings.Contains(rest, "/") || strings.Contains(rest, ".") {
			continue
		}
		kids, err := list(an)
		if err != nil {
			return nil, err
		}
		out = append(out, kids...)
	}
	slices.Sort(out)
	return out, nil
}
