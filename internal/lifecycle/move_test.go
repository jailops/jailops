package lifecycle

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// movable is a running jail on image fb (15.0-RELEASE) on a host that runs 15.1, so that image fc (15.1) is a
// release the host can run.
func movable(t *testing.T) *world {
	w := runningSetup(t)
	w.Jails.Release = "15.1-RELEASE"
	return w
}

func move(w *world, image string) (UpgradeResult, error) {
	return upgrade(w, api.JailUpgrade{Image: image})
}

func specImage(t *testing.T, w *world) (string, int64) {
	t.Helper()
	r, err := w.S.readRecord(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	return r.Spec.Image, r.Metadata.Generation
}

func TestMoveToANewerReleaseKeepsTheDataAndFollowsTheNewImage(t *testing.T) {
	w := movable(t)
	w.Z.WriteFile(jc+"/home", "/home/user/notes", "keep me")
	w.Z.WriteFile(dbVol, "/db/rows", "rows")
	_, gen0 := specImage(t, w)
	w.started = nil
	res, err := move(w, "fc")
	if err != nil {
		t.Fatal(err)
	}
	// the newest generation of the image, with the releases of both ends
	if !res.Changed || !res.Rebase || res.From != "fb#g1" || res.To != "fc#g2" || res.FromRelease != "15.0-RELEASE" || res.ToRelease != "15.1-RELEASE" {
		t.Fatalf("%+v", res)
	}
	if o, _ := w.Z.Origin(bg, jc+"/root"); o != "tank/jailops/images/fc/g2@ro" {
		t.Fatal(o)
	}
	if o, _ := w.Z.Origin(bg, jc+"/root.old"); o != "tank/jailops/images/fb/g1@ro" {
		t.Fatal(o)
	}
	for ds, want := range map[string]map[string]string{
		jc:           {PropImage: "fc"},
		jc + "/root": {PropImage: "fc", PropGen: "2", PropPrevGen: "1", PropPrevImage: "fb", PropPrevRef: "fb"},
	} {
		for k, v := range want {
			if got := w.Z.PropOrEmpty(ds, k); got != v {
				t.Errorf("%s %s = %q, want %q", ds, k, got, v)
			}
		}
	}
	// from now on the jail follows image fc: spec.image names it and the spec has a new generation (a new ETag)
	if img, gen := specImage(t, w); img != "fc" || gen != gen0+1 {
		t.Fatalf("spec image %q generation %d (was %d)", img, gen, gen0)
	}
	b, _ := w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/bundle.json")
	if !strings.Contains(string(b), `"name": "fc#g2"`) || !strings.Contains(string(b), `"ref": "fc"`) {
		t.Fatal(string(b))
	}
	// the data, the volume and the merge are those of any upgrade
	if c, _ := w.Z.ReadFile(jc+"/home", "/home/user/notes"); c != "keep me" {
		t.Fatal(c)
	}
	if c, _ := w.Z.ReadFile(dbVol, "/db/rows"); c != "rows" {
		t.Fatal(c)
	}
	if c, _ := w.Z.ReadFile(jc+"/etc", "/etc/merged"); !strings.Contains(c, "new-etc.tar") {
		t.Fatalf("no merge: %q", c)
	}
	if !w.Jails.IsUp("alpha_web") || len(w.started) != 1 || w.started[0] != "2" {
		t.Fatalf("started on %v", w.started)
	}
	if len(res.Warnings) != 0 || len(res.Kept) == 0 {
		t.Fatal(res.Warnings, res.Kept)
	}
	// the jail is on the newest generation of its new image: nothing more to move
	if r2, err := move(w, "fc"); err != nil || r2.Changed {
		t.Fatal(r2, err)
	}
	// and a generation of the image can be named
	if r3, err := w.S.UpgradeJail(bg, "alpha_web", UpgradeRequest{JailUpgrade: api.JailUpgrade{Image: "fc", Generation: "g1"}, Stamp: "101"}); err != nil || !r3.Changed || r3.To != "fc#g1" {
		t.Fatal(r3, err)
	}
}

func TestMoveToAnotherImageOfTheSameReleaseAndAGeneration(t *testing.T) {
	w := movable(t)
	res, err := upgrade(w, api.JailUpgrade{Image: "fd", Generation: "g1"})
	if err != nil || !res.Rebase || res.To != "fd#g1" || res.FromRelease != res.ToRelease {
		t.Fatal(res, err)
	}
	if img, _ := specImage(t, w); img != "fd" {
		t.Fatal(img)
	}
	// a full reference of the same image works too
	w2 := movable(t)
	if res, err := upgrade(w2, api.JailUpgrade{Image: "fc", Generation: "fc#g1"}); err != nil || res.To != "fc#g1" {
		t.Fatal(res, err)
	}
}

func TestMoveRefusals(t *testing.T) {
	tests := []struct {
		name string
		mut  func(w *world)
		req  api.JailUpgrade
		code api.Code
		want string
	}{
		{"older release", func(w *world) {}, api.JailUpgrade{Image: "fe"}, api.CodeInvalid, "older release"},
		{"another OS", func(w *world) {}, api.JailUpgrade{Image: "alp"}, api.CodeInvalid, "one OS"},
		{"newer than the host kernel", func(w *world) { w.Jails.Release = "15.0-RELEASE" }, api.JailUpgrade{Image: "fc"}, api.CodeVersionSkew, "upgrade the host first"},
		{"thick jail", func(w *world) { w.Z.Set(bg, jc, map[string]string{PropLayout: "thick"}) }, api.JailUpgrade{Image: "fc"}, api.CodeUnsupported, "managed updates"},
		{"a generation instead of an image", func(w *world) {}, api.JailUpgrade{Image: "fc#g1"}, api.CodeInvalid, "names an image"},
		{"a generation of another image", func(w *world) {}, api.JailUpgrade{Image: "fc", Generation: "fd#g1"}, api.CodeInvalid, "is not of image"},
		{"unknown image", func(w *world) {}, api.JailUpgrade{Image: "nope"}, api.CodeNotFound, ""},
		{"unknown generation", func(w *world) {}, api.JailUpgrade{Image: "fc", Generation: "g9"}, api.CodeNotFound, ""},
		{"bad generation", func(w *world) {}, api.JailUpgrade{Image: "fc", Generation: "latest"}, api.CodeInvalid, ""},
		{"leftover root.new", func(w *world) { w.Z.Create(bg, jc+"/root.new", nil) }, api.JailUpgrade{Image: "fc"}, api.CodeBusy, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := movable(t)
			tc.mut(w)
			before := w.state()
			w.F.Reset(0)
			_, err := upgrade(w, tc.req)
			wantCode(t, err, tc.code)
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v lacks %q", err, tc.want)
			}
			if w.state() != before || w.F.Count() != 0 {
				t.Fatal("a refusal must not change anything")
			}
		})
	}
}

func TestMoveDryRunPlansTheReleaseChangeAndChangesNothing(t *testing.T) {
	w := movable(t)
	before := w.state()
	w.F.Reset(0)
	res, err := upgrade(w, api.JailUpgrade{Image: "fc", DryRun: true})
	if err != nil || !res.Changed || !res.Rebase || res.To != "fc#g2" || w.state() != before || w.F.Count() != 0 {
		t.Fatal(res, err)
	}
	p, _, err := w.S.PlanUpgrade(bg, "alpha_web", UpgradeRequest{JailUpgrade: api.JailUpgrade{Image: "fc"}, Stamp: "100"})
	if err != nil {
		t.Fatal(err)
	}
	d := strings.Join(p.Describe(), "\n")
	for _, want := range []string{"zfs clone tank/jailops/images/fc/g2@ro " + jc + "/root.new", "tar the /etc of fb#g1 and fc#g2",
		"zfs set " + PropImage + "=fc " + jc, "write /jailops/projects/alpha/jails/web/spec.json atomically (image fb to fc)"} {
		if !strings.Contains(d, want) {
			t.Errorf("plan lacks %q\n%s", want, d)
		}
	}
}

func TestMoveFailureSweepRestoresTheImageToo(t *testing.T) {
	n := sweep(t, movable, func(w *world) error { _, err := move(w, "fc"); return err })
	t.Logf("%d injected failures, each left the jail on fb, spec.image fb, running, with the pool unchanged", n)
}

func TestMoveFailureSweepStoppedJail(t *testing.T) {
	setup := func(t *testing.T) *world {
		w := newWorld(t)
		w.Jails.Release = "15.1-RELEASE"
		w.created("web")
		return w
	}
	sweep(t, setup, func(w *world) error { _, err := move(w, "fc"); return err })
}

func TestMoveHealthCheckFailureSwapsBackTheImageAndTheSpec(t *testing.T) {
	w := movable(t)
	before := w.state()
	w.Jails.ExecFn = func(string, []string) ([]byte, error) { return []byte("refused"), errors.New("exit 1") }
	w.started = nil
	if _, err := move(w, "fc"); err == nil || !strings.Contains(err.Error(), "health check") {
		t.Fatal(err)
	}
	if w.state() != before {
		t.Fatalf("not swapped back:\n%s", w.state())
	}
	if img, _ := specImage(t, w); img != "fb" {
		t.Fatal(img)
	}
	if got := w.Z.PropOrEmpty(jc, PropImage); got != "fb" {
		t.Fatal(got)
	}
}

func TestMoveCrashRecovery(t *testing.T) {
	steps := func() int {
		w := movable(t)
		p, _, err := w.S.PlanUpgrade(bg, "alpha_web", UpgradeRequest{JailUpgrade: api.JailUpgrade{Image: "fc"}, Stamp: "100"})
		if err != nil {
			t.Fatal(err)
		}
		return len(p.Steps)
	}()
	for k := 1; k <= steps; k++ {
		w := movable(t)
		before := w.state()
		req := UpgradeRequest{JailUpgrade: api.JailUpgrade{Image: "fc"}, Stamp: "100"}
		p, res, err := w.S.PlanUpgrade(bg, "alpha_web", req)
		if err != nil {
			t.Fatal(err)
		}
		crash(t, w, "upgrade/alpha_web", p, k)
		req.Resume = &res.State
		rp, _, err := w.S.PlanUpgrade(bg, "alpha_web", req)
		if err != nil {
			t.Fatalf("after step %d (%s): %v", k, p.Steps[k-1].Name, err)
		}
		if found, err := w.S.RecoverRun(bg, "upgrade", "alpha_web", rp); !found || err != nil {
			t.Fatalf("recover after %d: %v %v", k, found, err)
		}
		if w.state() != before {
			t.Fatalf("crash after step %d (%s) not rolled back\n--- before\n%s\n--- after\n%s", k, p.Steps[k-1].Name, before, w.state())
		}
	}
}

func moved(t *testing.T) *world {
	w := movable(t)
	if _, err := move(w, "fc"); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestRollbackAfterAMoveReturnsToTheOldImageAndItsReference(t *testing.T) {
	w := moved(t)
	w.Z.WriteFile(jc+"/home", "/home/user/after", "written on the new release")
	res, err := rollback(w, RollbackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Upgrade == nil || res.Upgrade.To != "fb#g1" || !res.Upgrade.Rebase {
		t.Fatalf("%+v", res.Upgrade)
	}
	if o, _ := w.Z.Origin(bg, jc+"/root"); o != "tank/jailops/images/fb/g1@ro" {
		t.Fatal(o)
	}
	if got := w.Z.PropOrEmpty(jc, PropImage); got != "fb" {
		t.Fatal(got)
	}
	if img, _ := specImage(t, w); img != "fb" {
		t.Fatal(img)
	}
	// the data written after the move stays: a root rollback never touches data
	if c, _ := w.Z.ReadFile(jc+"/home", "/home/user/after"); c != "written on the new release" {
		t.Fatal(c)
	}
	if !w.Jails.IsUp("alpha_web") {
		t.Fatal("jail not running")
	}
	b, _ := w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/bundle.json")
	if !strings.Contains(string(b), `"name": "fb#g1"`) {
		t.Fatal(string(b))
	}
}

func TestRollbackAfterAMoveFailureSweep(t *testing.T) {
	sweep(t, moved, func(w *world) error { _, err := rollback(w, RollbackOptions{}); return err })
}

func TestRollbackAfterAMoveCrashRecovery(t *testing.T) {
	steps := func() int {
		w := moved(t)
		p, _, err := w.S.PlanRollbackRoot(bg, "alpha_web", RollbackOptions{Stamp: "300"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(p.Steps)
	}()
	for k := 1; k <= steps; k++ {
		w := moved(t)
		before := w.state()
		p, res, err := w.S.PlanRollbackRoot(bg, "alpha_web", RollbackOptions{Stamp: "300"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		crash(t, w, "rollback/alpha_web", p, k)
		rp, _, err := w.S.PlanRollbackRoot(bg, "alpha_web", RollbackOptions{Stamp: "300", Resume: &res.State}, 1)
		if err != nil {
			t.Fatalf("after step %d (%s): %v", k, p.Steps[k-1].Name, err)
		}
		if found, err := w.S.RecoverRun(bg, "rollback", "alpha_web", rp); !found || err != nil {
			t.Fatal(found, err)
		}
		if w.state() != before {
			t.Fatalf("crash after step %d (%s) not rolled back\n--- before\n%s\n--- after\n%s", k, p.Steps[k-1].Name, before, w.state())
		}
	}
}

func TestRestoringASnapshotOfAnotherImageNeedsTheRootRollbackFirst(t *testing.T) {
	w := movable(t)
	if _, err := w.S.SnapshotJail(bg, "alpha_web", "before", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := move(w, "fc"); err != nil {
		t.Fatal(err)
	}
	_, err := rollback(w, RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackAll, Snapshot: "before", ConfirmDataLoss: true}})
	wantCode(t, err, api.CodePreconditionFailed)
	if err == nil || !strings.Contains(err.Error(), "jailops jail rollback") {
		t.Fatal(err)
	}
}

func TestMoveToANewMajorReleaseWarnsAboutThePackages(t *testing.T) {
	w := movable(t)
	w.Jails.Release = "16.0-RELEASE"
	res, err := upgrade(w, api.JailUpgrade{Image: "ff", DryRun: true})
	if err != nil || !res.Rebase || res.ToRelease != "16.0-RELEASE" {
		t.Fatal(res, err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "a new major release (15.0-RELEASE to 16.0-RELEASE)") || !strings.Contains(res.Warnings[0], "pkg upgrade -f") {
		t.Fatalf("warnings %q", res.Warnings)
	}
	// a minor release of the same major says nothing
	w.Jails.Release = "15.1-RELEASE"
	if res, err = upgrade(w, api.JailUpgrade{Image: "fc", DryRun: true}); err != nil || len(res.Warnings) != 0 {
		t.Fatal(res.Warnings, err)
	}
}

// A dry run only looks: it never asks for an image to be built or received.
func TestDryRunNeverBuildsAnImage(t *testing.T) {
	w := movable(t)
	w.Images.ensures = 0
	if res, err := upgrade(w, api.JailUpgrade{Image: "fc", DryRun: true}); err != nil || !res.Changed {
		t.Fatal(res, err)
	}
	if w.Images.ensures != 0 || w.Images.peeks == 0 {
		t.Fatalf("a dry run asked to provide an image %d times and to look %d times", w.Images.ensures, w.Images.peeks)
	}
	_, err := upgrade(w, api.JailUpgrade{Image: "nope", DryRun: true})
	if !api.IsCode(err, api.CodeNotFound) || w.Images.ensures != 0 {
		t.Fatalf("a dry run for an image that does not exist: %v (provided %d times)", err, w.Images.ensures)
	}
}
