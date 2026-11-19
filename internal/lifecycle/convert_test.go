package lifecycle

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/lifecycle/fault"
	"jailops.net/jailops/sdk/api"
)

const jroot = "/jailops/projects/alpha/jails/web"

func convert(w *world, layout api.Layout, image string) (ConvertResult, error) {
	return w.S.ConvertJail(bg, "alpha_web", ConvertRequest{JailConvert: api.JailConvert{Layout: layout, Image: image}, Stamp: "100"})
}

// thickCreated makes a stopped thick jail named web.
func (w *world) thickCreated() {
	w.t.Helper()
	j := w.jail("web")
	j.Spec.Layout = api.LayoutThick
	w.attachTo("alpha_web", "db", "/srv/db", false)
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		w.t.Fatal(err)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		w.t.Fatal(err)
	}
}

func thinRunning(t *testing.T) *world {
	w := runningSetup(t)
	w.Z.WriteFile(jc+"/home", "/home/user/notes", "keep me")
	w.Files.Trees[jroot+"/root/home"] = []string{"user/notes"}
	w.Files.Trees[jroot+"/root/usr/local"] = []string{"bin/tool"}
	return w
}

func thickRunning(t *testing.T) *world {
	w := newWorld(t)
	w.thickCreated()
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	w.Files.Trees[jroot+"/root/home"] = []string{"user/notes"}
	return w
}

func TestConvertThinToThick(t *testing.T) {
	w := thinRunning(t)
	res, err := convert(w, api.LayoutThick, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.From != api.LayoutThin || res.To != api.LayoutThick || res.Snapshot != "jo-pre-convert-100" || res.Image != "fb#g1" {
		t.Fatalf("%+v", res)
	}
	if o, _ := w.Z.Origin(bg, jc+"/root"); o != "" {
		t.Fatalf("the thick root has an origin: %q", o)
	}
	if got := w.Z.PropOrEmpty(jc, PropLayout); got != "thick" {
		t.Fatal(got)
	}
	if got := w.Z.PropOrEmpty(jc+"/root", PropGen); got != "1" {
		t.Fatal(got)
	}
	if got := w.Z.PropOrEmpty(jc+"/root", "mountpoint"); got != jroot+"/root" {
		t.Fatal(got)
	}
	// the data was folded into the new root, from the persistent datasets
	if b, ok := w.Files.Data[jroot+"/root.new/home/user/notes"]; !ok || string(b) != "seed:"+jroot+"/root/home/user/notes" {
		t.Fatalf("home was not folded: %q", b)
	}
	if _, ok := w.Files.Data[jroot+"/root.new/usr/local/bin/tool"]; !ok {
		t.Fatal("usr/local was not folded")
	}
	// the persistent datasets are put away below root.old, off
	st, err := w.S.observe(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Persist) != 0 || st.Layout != api.LayoutThick || !st.hasRootOld() {
		t.Fatalf("%+v", st)
	}
	if got := w.Z.PropOrEmpty(jc+"/root.old/home", PropKind); got != "retired" {
		t.Fatalf("kind %q", got)
	}
	if got := w.Z.PropOrEmpty(jc+"/root.old/home", "canmount"); got != "noauto" {
		t.Fatalf("canmount %q", got)
	}
	if got := w.Z.PropOrEmpty(jc+"/root.old", PropCleanupAfter); got != "2026-05-02T12:00:00Z" {
		t.Fatal(got)
	}
	// the snapshots are the rollback point: they went with the datasets that were put away
	for _, d := range []string{jc, jc + "/root.old", jc + "/root.old/home"} {
		if ok, _ := w.Z.Exists(bg, d+"@jo-pre-convert-100"); !ok {
			t.Errorf("no snapshot on %s", d)
		}
	}
	rec, _ := w.S.readRecord(bg, "alpha_web")
	if rec.Spec.Layout != api.LayoutThick {
		t.Fatal(rec.Spec.Layout)
	}
	b, _ := w.Files.ReadFile(bg, jroot+"/bundle.json")
	if !strings.Contains(string(b), `"layout": "thick"`) {
		t.Fatal(string(b))
	}
	if !w.Jails.IsUp("alpha_web") {
		t.Fatal("the jail ran and is stopped")
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "disk limit") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestConvertThickToThin(t *testing.T) {
	w := thickRunning(t)
	res, err := convert(w, api.LayoutThin, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.From != api.LayoutThick || res.To != api.LayoutThin || res.Image != "fb#g1" || res.Replaced == "" {
		t.Fatalf("%+v", res)
	}
	if o, _ := w.Z.Origin(bg, jc+"/root"); o != "tank/jailops/images/fb/g1@ro" {
		t.Fatalf("origin %q", o)
	}
	st, err := w.S.observe(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	if st.Layout != api.LayoutThin || len(st.Persist) != len(DefaultPersistent()) || !st.hasRootOld() {
		t.Fatalf("%d persistent datasets: %+v", len(st.Persist), st)
	}
	for _, d := range st.Persist {
		if w.Z.PropOrEmpty(d.DS, "canmount") == "noauto" {
			t.Errorf("%s canmount %q", d.DS, w.Z.PropOrEmpty(d.DS, "canmount"))
		}
		if ok, _ := w.Z.Exists(bg, d.DS+"@"+BaselineSnap); !ok {
			t.Errorf("%s has no baseline", d.DS)
		}
	}
	if b, ok := w.Files.Data[jroot+"/.seed-home/user/notes"]; ok {
		t.Fatalf("the stage was not removed: %q", b)
	}
	rec, _ := w.S.readRecord(bg, "alpha_web")
	if rec.Spec.Layout != api.LayoutThin {
		t.Fatal(rec.Spec.Layout)
	}
	if !w.Jails.IsUp("alpha_web") {
		t.Fatal("the jail ran and is stopped")
	}
	// and back
	if _, err := convert(w, api.LayoutThick, ""); err == nil {
		t.Fatal("a second conversion while root.old is parked (stamp reused) went through")
	}
}

func TestConvertStoppedJailStaysStopped(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	if _, err := convert(w, api.LayoutThick, ""); err != nil {
		t.Fatal(err)
	}
	if w.Jails.IsUp("alpha_web") {
		t.Fatal("a stopped jail was left running")
	}
}

func TestConvertDryRunAndSameLayout(t *testing.T) {
	w := thinRunning(t)
	before := w.state()
	res, err := w.S.ConvertJail(bg, "alpha_web", ConvertRequest{JailConvert: api.JailConvert{Layout: api.LayoutThick, DryRun: true}, Stamp: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !res.DryRun || len(res.Steps) < 8 || !strings.Contains(strings.Join(res.Steps, "\n"), "zfs send") {
		t.Fatalf("%+v", res)
	}
	if w.state() != before {
		t.Fatal("a dry run changed the pool")
	}
	res, err = convert(w, api.LayoutThin, "")
	if err != nil || res.Changed || len(res.Steps) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if w.state() != before {
		t.Fatal("converting to the layout the jail has changed the pool")
	}
	_, err = convert(w, "fat", "")
	wantCode(t, err, api.CodeInvalid)
}

func TestConvertRefusesLinux(t *testing.T) {
	w := newWorld(t)
	j := w.jail("web")
	j.Spec.Image = "alp"
	j.Spec.Volumes = nil
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	_, err := convert(w, api.LayoutThin, "")
	wantCode(t, err, api.CodeUnsupported)
}

func TestConvertToThinOnAnotherGeneration(t *testing.T) {
	w := thickRunning(t)
	res, err := convert(w, api.LayoutThin, "g2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Image != "fb#g2" || w.Z.PropOrEmpty(jc+"/root", PropGen) != "2" {
		t.Fatalf("%+v gen %s", res, w.Z.PropOrEmpty(jc+"/root", PropGen))
	}
	w = thickRunning(t)
	_, err = convert(w, api.LayoutThin, "alp#g1")
	wantCode(t, err, api.CodeInvalid)
	_, err = convert(w, api.LayoutThin, "fb#g9")
	wantCode(t, err, api.CodeNotFound)
}

func TestConvertRefusesLeftovers(t *testing.T) {
	w := thinRunning(t)
	if err := w.Z.Create(bg, jc+"/root.new", map[string]string{"jailops:managed": "on"}); err != nil {
		t.Fatal(err)
	}
	_, err := convert(w, api.LayoutThick, "")
	wantCode(t, err, api.CodeBusy)
}

func TestConvertFailureSweepThinToThick(t *testing.T) {
	sweep(t, thinRunning, func(w *world) error { _, err := convert(w, api.LayoutThick, ""); return err })
}

func TestConvertFailureSweepThickToThin(t *testing.T) {
	sweep(t, thickRunning, func(w *world) error { _, err := convert(w, api.LayoutThin, ""); return err })
}

// TestConvertCrashRecovery crashes the conversion at every step and rolls it back with the rebuilt plan.
func TestConvertCrashRecovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T) *world
		to    api.Layout
	}{{"thin to thick", thinRunning, api.LayoutThick}, {"thick to thin", thickRunning, api.LayoutThin}} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.setup(t)
			p, res, err := w.S.PlanConvert(bg, "alpha_web", ConvertRequest{JailConvert: api.JailConvert{Layout: tc.to}, Stamp: "100"})
			if err != nil {
				t.Fatal(err)
			}
			n := len(p.Steps)
			for k := 1; k < n; k++ {
				w := tc.setup(t)
				before := w.state()
				p, res, err := w.S.PlanConvert(bg, "alpha_web", ConvertRequest{JailConvert: api.JailConvert{Layout: tc.to}, Stamp: "100"})
				if err != nil {
					t.Fatal(err)
				}
				st := res.State
				crash(t, w, "convert/alpha_web", p, k)
				rp, _, err := w.S.PlanConvert(bg, "alpha_web", ConvertRequest{JailConvert: api.JailConvert{Layout: tc.to}, Stamp: "100", Resume: &st})
				if err != nil {
					t.Fatalf("step %d (%s): rebuild: %v", k, p.Steps[k-1].Name, err)
				}
				w.F.Reset(0)
				if _, err := w.S.RecoverRun(bg, "convert", "alpha_web", rp); err != nil {
					t.Fatalf("step %d (%s): recover: %v", k, p.Steps[k-1].Name, err)
				}
				if after := w.state(); after != before {
					t.Fatalf("crash at step %d (%s) was not rolled back\n--- before\n%s\n--- after\n%s", k, p.Steps[k-1].Name, before, after)
				}
			}
			_ = res
			_ = fault.ErrInjected
		})
	}
}

// ZFS cannot inherit canmount: the model refuses it as the real one does, so a plan that tries fails here.
func TestModelRefusesInheritCanmount(t *testing.T) {
	w := thinRunning(t)
	if err := w.Z.Inherit(bg, jc+"/home", "canmount"); err == nil || !strings.Contains(err.Error(), "cannot be inherited") {
		t.Fatalf("%v", err)
	}
}
