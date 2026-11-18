package reconcile_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// recordingPF logs loads in order and can swallow the rules of an anchor, as
// a pfctl that accepted a load which did not take effect would.
type recordingPF struct {
	pfctl.Client
	loads []string
	drop  string
}

func (r *recordingPF) Load(ctx context.Context, anchor, rs string) error {
	r.loads = append(r.loads, anchor)
	if anchor == r.drop {
		return nil // accepted, but nothing is there
	}
	return r.Client.Load(ctx, anchor, rs)
}

func TestProjectAnchorIsLoadedBeforeTheJailAnchorAndVerified(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	pf := &recordingPF{Client: w.PF}
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: pf, Jails: w.Jails, Run: privateRunner(), Sysctl: sysctl, Clock: w.clock})
	j := jailDesired("web_nginx")
	j.Project = "web"
	j.AnchorName = render.JailAnchor("web_nginx")
	j.ParentAnchors = map[string]string{render.ProjectAnchor("web"): render.ProjectAnchorRules()}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web_nginx/jail.conf", "web_nginx")
	h := j.Epairs[0]
	_, _, _ = w.Net.EpairCreate(bg, h[:len(h)-1])
	_ = w.Net.SetDescription(bg, h, "jailops:"+jailID)
	rep := w.R.Pass(bg)
	if len(rep.Drift["web_nginx"]) != 0 {
		t.Fatalf("%+v", rep.Drift)
	}
	if strings.Join(pf.loads, ",") != "jailops/web,jailops/web/nginx" {
		t.Fatalf("load order: %v", pf.loads)
	}
	// Rules that were accepted but are not there afterwards are drift, not success.
	_ = w.PF.Flush(bg, "jailops/web/nginx")
	pf.loads, pf.drop = nil, "jailops/web/nginx"
	rep = w.R.Pass(bg)
	d := strings.Join(rep.Drift["web_nginx"], ";")
	if !strings.Contains(d, "jailops/web/nginx") || !strings.Contains(d, "after loading") {
		t.Fatalf("a load that did not take effect was reported as success: %v", rep.Drift)
	}
}

func TestStatesOfRemovedRulesAreEnded(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	pf, r := w.PF, w.R
	v1 := "pass quick on e inet proto tcp to 10.0.0.2 port 80 label \"jo-aaaaaaaaaaaa\"\npass quick on e inet proto tcp to 10.0.0.2 port 22 label \"jo-bbbbbbbbbbbb\"\n"
	v2 := "pass quick on e inet proto tcp to 10.0.0.2 port 80 label \"jo-aaaaaaaaaaaa\"\n"
	if err := r.LoadAnchorForTest(bg, "jailops/p/j", v1); err != nil {
		t.Fatal(err)
	}
	if len(pf.Killed) != 0 {
		t.Fatalf("first load killed %v", pf.Killed)
	}
	if err := r.LoadAnchorForTest(bg, "jailops/p/j", v2); err != nil {
		t.Fatal(err)
	}
	if len(pf.Killed) != 1 || pf.Killed[0] != "jo-bbbbbbbbbbbb" {
		t.Fatalf("killed %v, want only the removed rule's label", pf.Killed)
	}
	// an unchanged ruleset ends nothing
	if err := r.LoadAnchorForTest(bg, "jailops/p/j", v2); err != nil || len(pf.Killed) != 1 {
		t.Fatalf("%v %v", err, pf.Killed)
	}
}

// A rule inserted above unchanged ones gives them new labels (the renderer hashes what is above a rule into its
// label), and the flows of the old labels end; nothing else is touched.
func TestAnInsertedDenyEndsTheFlowsBelowIt(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	v1 := "pass out quick on e all label \"jo-aaaaaaaaaaaa\"\npass in quick on e all label \"jo-bbbbbbbbbbbb\"\n"
	v2 := "pass out quick on e all label \"jo-aaaaaaaaaaaa\"\nblock drop out quick on e to 10.0.0.0/8\npass in quick on e all label \"jo-cccccccccccc\"\n"
	if err := w.R.LoadAnchorForTest(bg, "jailops/p/j", v1); err != nil {
		t.Fatal(err)
	}
	if err := w.R.LoadAnchorForTest(bg, "jailops/p/j", v2); err != nil {
		t.Fatal(err)
	}
	if len(w.PF.Killed) != 1 || w.PF.Killed[0] != "jo-bbbbbbbbbbbb" {
		t.Fatalf("killed %v", w.PF.Killed)
	}
}

// When the loaded rules cannot be read, nothing is replaced: the flows of a removed rule would never be ended, as the
// next pass would find nothing to compare with.
func TestAnAnchorThatCannotBeReadIsNotReplaced(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	v1 := "pass quick on e all label \"jo-aaaaaaaaaaaa\"\n"
	if err := w.R.LoadAnchorForTest(bg, "jailops/p/j", v1); err != nil {
		t.Fatal(err)
	}
	w.PF.ReadErr = errors.New("pfctl: the helper did not answer")
	loads := w.PF.LoadCalls["jailops/p/j"]
	err := w.R.LoadAnchorForTest(bg, "jailops/p/j", "pass quick on e inet all label \"jo-dddddddddddd\"\n")
	if err == nil || !strings.Contains(err.Error(), "before replacing") {
		t.Fatalf("%v", err)
	}
	if w.PF.LoadCalls["jailops/p/j"] != loads {
		t.Fatal("the anchor was replaced although its old content is unknown")
	}
	// an anchor that does not exist yet is not an error
	w.PF.ReadErr = api.Errorf(api.CodeNotFound, "pfctl read: Anchor does not exist.")
	if err := w.R.LoadAnchorForTest(bg, "jailops/p/new", v1); err != nil {
		t.Fatalf("%v", err)
	}
}

// failingKill is a pf whose KillLabel refuses while fail is set.
type failingKill struct {
	pfctl.Client
	fail bool
	left []string
}

func (f *failingKill) KillLabel(ctx context.Context, l string) error {
	if f.fail {
		return errors.New("pfctl: busy")
	}
	f.left = append(f.left, l)
	return f.Client.KillLabel(ctx, l)
}

func firewallCond(w *world, jail string) api.Condition {
	for _, c := range w.jailSt[jail] {
		if c.Type == reconcile.CondFirewallApplied {
			return c
		}
	}
	return api.Condition{}
}

// A label whose states pf refused to end is kept: the rule is gone from the anchor, so no later pass would find it.
// FirewallApplied says so until the states are ended, and the next pass ends them.
func TestStatesThatCouldNotBeEndedAreEndedAtALaterPass(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	fk := &failingKill{Client: w.PF}
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: fk, Jails: w.Jails, Run: privateRunner(), Sysctl: sysctl, Clock: w.clock})
	v1 := "pass quick on e inet proto tcp to 10.20.0.10 port 80 label \"jo-aaaaaaaaaaaa\"\npass quick on e inet proto tcp to 10.20.0.10 port 22 label \"jo-bbbbbbbbbbbb\"\n"
	v2 := "pass quick on e inet proto tcp to 10.20.0.10 port 80 label \"jo-aaaaaaaaaaaa\"\n"
	j := jailDesired("web")
	j.Ruleset = v1
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	h := j.Epairs[0]
	_, _, _ = w.Net.EpairCreate(bg, h[:len(h)-1])
	_ = w.Net.SetDescription(bg, h, "jailops jail=p/web net=lan (host side)")
	w.R.Pass(bg)
	if c := firewallCond(w, "web"); c.Status != api.ConditionTrue || c.Reason != "Applied" {
		t.Fatalf("%+v", c)
	}
	fk.fail = true
	j.Ruleset = v2
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	c := firewallCond(w, "web")
	if c.Status != api.ConditionFalse || c.Reason != "StatesPending" || !strings.Contains(c.Message, "1 removed rule") {
		t.Fatalf("a refused kill is not shown: %+v", c)
	}
	w.R.Pass(bg) // still refused: still pending, and the same reason keeps its time
	if c2 := firewallCond(w, "web"); c2.Reason != "StatesPending" || !c2.Since.Equal(c.Since) {
		t.Fatalf("%+v", c2)
	}
	fk.fail = false
	w.R.Pass(bg)
	if len(fk.left) != 1 || fk.left[0] != "jo-bbbbbbbbbbbb" {
		t.Fatalf("the label was not retried: %v", fk.left)
	}
	if c := firewallCond(w, "web"); c.Status != api.ConditionTrue || c.Reason != "Applied" {
		t.Fatalf("%+v", c)
	}
	fk.left = nil
	w.R.Pass(bg)
	if len(fk.left) != 0 {
		t.Fatalf("an ended label was tried again: %v", fk.left)
	}
}

// pf disabled, or a main ruleset without the anchor line, leaves the anchors loaded and applied by nothing: the
// condition says so as soon as a pass looks, and says Applied again when pf is right.
func TestFirewallAppliedFollowsPF(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	var info, rules string
	f := run.NewFake()
	f.Add(run.Rule{Name: "ifconfig", Args: []string{run.RestArgs}})
	f.Add(run.Rule{Name: "pfctl", Args: []string{"-s", "info"}, Fn: func(run.Cmd) (run.Result, error) { return run.Result{Stdout: []byte(info)}, nil }})
	f.Add(run.Rule{Name: "pfctl", Args: []string{"-s", "rules"}, Fn: func(run.Cmd) (run.Result, error) { return run.Result{Stdout: []byte(rules)}, nil }})
	w.R = reconcile.New(reconcile.Config{}, reconcile.Env{Source: w, Sink: w, Actions: w, Net: w.Net, PF: w.PF, Jails: w.Jails, Run: f, Sysctl: sysctl, Clock: w.clock})
	j := jailDesired("web")
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	h := j.Epairs[0]
	_, _, _ = w.Net.EpairCreate(bg, h[:len(h)-1])
	_ = w.Net.SetDescription(bg, h, "jailops jail=p/web net=lan (host side)")
	for _, c := range []struct{ info, rules, reason, has string }{
		{"Status: Enabled\n", "anchor \"jailops/*\" all\n", "Applied", "are loaded"},
		{"Status: Disabled\n", "anchor \"jailops/*\" all\n", "PFNotApplying", "pf is disabled"},
		{"Status: Enabled\n", "block all\n", "PFNotApplying", `no line anchor "jailops/*"`},
		{"Status: Enabled\n", "anchor \"jailops/*\" all\n", "Applied", "are loaded"},
	} {
		info, rules = c.info, c.rules
		w.R.Pass(bg)
		got := firewallCond(w, "web")
		if got.Reason != c.reason || !strings.Contains(got.Message, c.has) || (c.reason == "Applied") != (got.Status == api.ConditionTrue) {
			t.Fatalf("%q %q: %+v", c.info, c.rules, got)
		}
	}
}

// An address set that lost its last member is rendered with its braces, so that pf empties the table; the loop
// compares the members of every table with the kernel's, the empty one included.
func TestAnEmptiedAddressSetIsEmptiedInTheKernel(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	j := jailDesired("web")
	j.Ruleset = "table <s_office> persist { }\nblock in quick from <s_office> to any\n"
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/web/jail.conf", "web")
	h := j.Epairs[0]
	_, _, _ = w.Net.EpairCreate(bg, h[:len(h)-1])
	_ = w.Net.SetDescription(bg, h, "jailops jail=p/web net=lan (host side)")
	// a member the kernel still holds, left by an earlier load
	if err := w.PF.TableAdd(bg, "jailops/web", "s_office", "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	rep := w.R.Pass(bg)
	if len(w.PF.Killed) != 0 || strings.Contains(strings.Join(rep.Drift["web"], ";"), "is missing") {
		t.Fatalf("%+v", rep.Drift)
	}
	if m, _ := w.PF.TableShow(bg, "jailops/web", "s_office"); len(m) != 0 {
		t.Fatalf("the table still holds %v", m)
	}
}
