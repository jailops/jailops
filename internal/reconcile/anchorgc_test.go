package reconcile_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/run"
)

// gcWorld is a world whose pf holds the anchors of live objects and stale ones, and whose pfctl lists them the way
// "pfctl -a jailops -s Anchors" does (the whole tree, one full path per line).
func gcWorld(t *testing.T, listErr *error) (*world, []string, run.Runner) {
	t.Helper()
	w := newWorld(t, reconcile.Config{})
	names := []string{
		"jailops/p", "jailops/p/web", // a live project and its jail
		"jailops/p/broken",               // a jail whose configuration cannot be rendered now
		"jailops/p/old",                  // a deleted jail of a live project
		"jailops/gone", "jailops/gone/x", // a deleted project and its jail
		"jailops/net.lan", "jailops/net.old", // a live network and a deleted one
		"jailops/wg.t",               // a tunnel: left to its own code
		"jailops/q", "jailops/q/run", // a jail the kernel runs that the store does not know
	}
	for _, an := range names {
		rules := "pass in quick on e all label \"jo-" + strings.Repeat("a", 12) + "\"\n"
		if an == "jailops/p/old" {
			rules = "pass in quick on e inet proto tcp to 10.20.0.11 port 22 label \"jo-0123456789ab\"\n"
		}
		if err := w.PF.Load(bg, an, rules); err != nil {
			t.Fatal(err)
		}
	}
	f := run.NewFake()
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	healthyPF(f)
	f.Add(run.Rule{Name: "pfctl", Args: []string{"-a", run.RestArgs}, Fn: func(c run.Cmd) (run.Result, error) {
		if *listErr != nil {
			return run.Result{}, *listErr
		}
		if len(c.Args) != 4 || c.Args[2] != "-s" || c.Args[3] != "Anchors" {
			return run.Result{}, errors.New("unexpected pfctl call " + strings.Join(c.Args, " "))
		}
		var out strings.Builder
		for _, an := range names {
			if s, err := w.PF.Read(bg, an); err == nil && s != "" && strings.HasPrefix(an, c.Args[1]+"/") {
				out.WriteString("  " + an + "\n")
			}
		}
		return run.Result{Stdout: []byte(out.String())}, nil
	}})
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Run: f, Sysctl: sysctl, Clock: w.clock})
	t.Cleanup(w.R.Close)
	j := jailDesired("p_web")
	j.Project, j.AnchorName = "p", render.JailAnchor("p_web")
	j.ParentAnchors = map[string]string{render.ProjectAnchor("p"): render.ProjectAnchorRules()}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j, {Name: "p_broken", Project: "p", Problem: "a set is missing"}},
		WireGuard: []reconcile.WireGuardDesired{}}
	if err := w.Jails.Create(bg, "/etc/jailops/jails/q_run/jail.conf", "q_run"); err != nil {
		t.Fatal(err)
	}
	return w, names, f
}

func loaded(w *world, names []string) []string {
	var out []string
	for _, an := range names {
		if s, err := w.PF.Read(bg, an); err == nil && s != "" {
			out = append(out, an)
		}
	}
	return out
}

// An anchor below jailops/ that no jail, project or network owns is flushed once it stayed so for a while, and the
// flows of its rules end; the anchors of live objects, of a jail the kernel runs and of tunnels are never touched.
func TestStaleAnchorsAreFlushed(t *testing.T) {
	var listErr error
	w, names, _ := gcWorld(t, &listErr)
	w.R.Pass(bg)
	if got := loaded(w, names); len(got) != len(names) {
		t.Fatalf("flushed at first sight, before the grace: %v", got)
	}
	w.clock.Advance(time.Minute)
	w.R.Pass(bg)
	if got := loaded(w, names); len(got) != len(names) {
		t.Fatalf("flushed within the grace: %v", got)
	}
	w.clock.Advance(2 * time.Minute)
	rep := w.R.Pass(bg)
	want := []string{"jailops/net.lan", "jailops/p", "jailops/p/broken", "jailops/p/web", "jailops/q", "jailops/q/run", "jailops/wg.t"}
	got := loaded(w, names)
	slices.Sort(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("left %v, want %v (applied %v)", got, want, rep.Applied)
	}
	if !slices.Contains(w.PF.Killed, "jo-0123456789ab") {
		t.Fatalf("the flows of the stale anchor's rules were not ended: %v", w.PF.Killed)
	}
}

// What cannot be listed flushes nothing, and neither does a desired state that could not be computed.
func TestStaleAnchorsAreKeptWhenNothingCanBeTold(t *testing.T) {
	listErr := errors.New("pfctl: the helper did not answer")
	w, names, f := gcWorld(t, &listErr)
	for range 3 {
		w.R.Pass(bg)
		w.clock.Advance(3 * time.Minute)
	}
	if got := loaded(w, names); len(got) != len(names) {
		t.Fatalf("a listing error flushed anchors: %v", got)
	}
	// the listing works again, but the store cannot be read: Pass stops before it looks at pf
	listErr = nil
	src := &failingSource{err: errors.New("store unreadable")}
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: src, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Run: f, Sysctl: sysctl, Clock: w.clock})
	for range 3 {
		w.R.Pass(bg)
		w.clock.Advance(3 * time.Minute)
	}
	if got := loaded(w, names); len(got) != len(names) {
		t.Fatalf("a desired state that failed flushed anchors: %v", got)
	}
}

// A stale anchor whose jail an operation holds is left for a later pass.
func TestStaleAnchorOfABusyJailIsKept(t *testing.T) {
	var listErr error
	w, names, _ := gcWorld(t, &listErr)
	w.locks["jail/p_old"] = true
	w.R.Pass(bg)
	w.clock.Advance(3 * time.Minute)
	w.R.Pass(bg)
	if !slices.Contains(loaded(w, names), "jailops/p/old") {
		t.Fatal("the anchor of a locked jail was flushed")
	}
	w.locks["jail/p_old"] = false
	w.R.Pass(bg)
	if slices.Contains(loaded(w, names), "jailops/p/old") {
		t.Fatal("the anchor was not flushed once the lock was released")
	}
}

type failingSource struct{ err error }

func (f *failingSource) Desired(context.Context) (reconcile.Desired, error) {
	return reconcile.Desired{}, f.err
}
