package lifecycle

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestCreateCopiesTheGeneration(t *testing.T) {
	w := newWorld(t)
	res, err := w.S.CreateJail(bg, w.jail("web"), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Jail.Status.ImageGeneration != "fb#g1" {
		t.Fatalf("%+v", res.Jail)
	}
	c := "tank/jailops/projects/alpha/jails/web"
	// the root is a dataset of its own with the files of the generation: no origin, nothing depends on the image
	if o, _ := w.Z.Origin(bg, c+"/root"); o != "" {
		t.Fatalf("root origin %q", o)
	}
	if cl, _ := w.Z.Clones(bg, "tank/jailops/images/fb/g1@ro"); len(cl) != 0 {
		t.Fatal(cl)
	}
	var below []string
	for _, n := range w.Z.Names() {
		if strings.HasPrefix(n, c+"/") {
			below = append(below, n)
		}
	}
	if len(below) != 1 || below[0] != c+"/root" {
		t.Fatalf("the jail has datasets besides its root: %v", below)
	}
	checks := map[[2]string]string{
		{c, PropManaged}: "on", {c, PropID}: "uuid-web", {c, PropComplete}: "on", {c, PropKind}: KindContainer, {c, PropLayout}: "",
		{c + "/root", PropGen}: "1", {c, "quota"}: "10G", {c + "/root", "quota"}: "", {c + "/root", "mountpoint"}: "/jailops/projects/alpha/jails/web/root",
	}
	for k, v := range checks {
		if got := w.Z.PropOrEmpty(k[0], k[1]); got != v {
			t.Errorf("%s %s = %q want %q", k[0], k[1], got, v)
		}
	}
	copied := false
	for _, l := range w.F.Log {
		if strings.HasPrefix(l, "copytree ") && strings.HasSuffix(l, " /jailops/projects/alpha/jails/web/root") {
			copied = true
		}
	}
	if !copied {
		t.Fatal("the files of the generation were not copied", w.F.Log)
	}
	var rec Record
	b, _ := w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/spec.json")
	if err := json.Unmarshal(b, &rec); err != nil || rec.Metadata.Name != "web" || rec.Spec.Image != "fb" {
		t.Fatal(err, rec)
	}
	if strings.Contains(string(b), "layout") {
		t.Fatalf("spec.json names a layout: %s", b)
	}
	var bd Bundle
	b, _ = w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/bundle.json")
	if err := json.Unmarshal(b, &bd); err != nil || bd.Image.Name != "fb#g1" || bd.Image.Manifest != "sha256:m1" {
		t.Fatal(err, bd)
	}
	if _, err := w.S.CreateJail(bg, w.jail("web"), false); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatal(err)
	}
}

func TestCreateLinux(t *testing.T) {
	w := newWorld(t)
	j := w.jail("deb")
	j.Spec.Image = "alp"
	j.Spec.Volumes = nil
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	c := "tank/jailops/projects/alpha/jails/deb"
	if o, _ := w.Z.Origin(bg, c+"/root"); o != "" {
		t.Fatalf("the root must be independent, origin %q", o)
	}
	if s, _ := w.Z.Snapshots(bg, c+"/root"); len(s) != 0 {
		t.Fatalf("copied @ro left on the root: %v", s)
	}
	// The emulation directory exists in the real root before the first start.
	if !w.Files.Dirs["/jailops/projects/alpha/jails/deb/root/dev/shm"] {
		t.Fatalf("dev/shm was not created: %v", w.Files.Dirs)
	}
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name string
		mut  func(j *api.Jail)
		code api.Code
	}{
		{"bad name", func(j *api.Jail) { j.Metadata.Name = "Web/1" }, api.CodeInvalid},
		{"no uuid", func(j *api.Jail) { j.Metadata.UUID = "" }, api.CodeInvalid},
		{"no image", func(j *api.Jail) { j.Spec.Image = "" }, api.CodeInvalid},
		{"unknown image", func(j *api.Jail) { j.Spec.Image = "nope" }, api.CodeNotFound},
		{"encrypted", func(j *api.Jail) { j.Spec.Encrypted = true }, api.CodeUnsupported},
		{"packages", func(j *api.Jail) { j.Spec.Packages = []string{"nginx"} }, api.CodeUnsupported},
		{"bad quota", func(j *api.Jail) { j.Spec.Resources.Disk = "lots" }, api.CodeInvalid},
		{"relative mount", func(j *api.Jail) { j.Spec.Volumes[0].Mount = "srv/db" }, api.CodeInvalid},
		{"dotdot mount", func(j *api.Jail) { j.Spec.Volumes[0].Mount = "/srv/../etc" }, api.CodeInvalid},
		{"mount over the system", func(j *api.Jail) { j.Spec.Volumes[0].Mount = "/etc" }, api.CodeInvalid},
		{"dup volume", func(j *api.Jail) { j.Spec.Volumes = append(j.Spec.Volumes, j.Spec.Volumes[0]) }, api.CodeInvalid},
		{"bad volume name", func(j *api.Jail) { j.Spec.Volumes[0].Name = "a/b" }, api.CodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			before := w.state()
			j := w.jail("web")
			j.Spec.Volumes = append([]api.VolumeAttach(nil), j.Spec.Volumes...)
			tc.mut(&j)
			if _, err := w.S.CreateJail(bg, j, false); !api.IsCode(err, tc.code) {
				t.Fatalf("got %v", err)
			}
			if w.state() != before {
				t.Fatal("state changed")
			}
		})
	}
}

func TestCreateDryRun(t *testing.T) {
	w := newWorld(t)
	before := w.state()
	w.F.Reset(0)
	r, err := w.S.CreateJail(bg, w.jail("web"), true)
	if err != nil || w.F.Count() != 0 || w.state() != before || len(r.Plan) < 7 {
		t.Fatal(err, w.F.Count(), len(r.Plan))
	}
}

func TestCreateFailureSweep(t *testing.T) {
	for name, img := range map[string]string{"freebsd": "fb", "linux": "alp"} {
		t.Run(name, func(t *testing.T) {
			sweep(t, newWorld, func(w *world) error {
				j := w.jail("web")
				j.Spec.Image = img
				_, err := w.S.CreateJail(bg, j, false)
				return err
			})
		})
	}
}

func TestCreateNeverTouchesForeignDataset(t *testing.T) {
	w := newWorld(t)
	// a foreign dataset appears at the container name while the plan is built
	p, _, err := w.S.PlanCreate(bg, w.jail("web"), false)
	if err != nil {
		t.Fatal(err)
	}
	w.Z.Create(bg, "tank/jailops/projects/alpha/jails/web", map[string]string{"foreign": "yes"})
	if err := w.S.executor().Run(bg, "create/web", p); err == nil {
		t.Fatal("create over a foreign dataset succeeded")
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web"); !ok {
		t.Fatal("foreign dataset destroyed by undo")
	}
}

func TestCreateCrashRecovery(t *testing.T) {
	w := newWorld(t)
	p, _, err := w.S.PlanCreate(bg, w.jail("web"), false)
	if err != nil {
		t.Fatal(err)
	}
	steps := len(p.Steps)
	for k := 1; k <= steps; k++ {
		w := newWorld(t)
		before := w.state()
		p, _, _ := w.S.PlanCreate(bg, w.jail("web"), false)
		crash(t, w, "create/alpha_web", p, k)
		rp, _, err := w.S.PlanCreate(bg, w.jail("web"), true)
		if err != nil {
			t.Fatal(err)
		}
		if found, err := w.S.RecoverRun(bg, "create", "alpha_web", rp); !found || err != nil {
			t.Fatal(found, err)
		}
		if w.state() != before {
			t.Fatalf("crash after %d steps: not restored\n%s", k, w.Z.Dump())
		}
	}
}

func TestAJailBelongsToANamedProject(t *testing.T) {
	w := newWorld(t)
	j := w.jail("web")
	j.Metadata.Project = ""
	if _, err := w.S.CreateJail(bg, j, false); err == nil {
		t.Fatal("a jail without a project was created")
	}
	if _, err := KeyOf(j); err == nil {
		t.Fatal("a key without a project")
	}
	if p, n := splitKey("web"); p != "" || n != "" {
		t.Fatalf("a plain key split into %q %q", p, n)
	}
	if k, err := qualify("default", "web"); err != nil || k != "default_web" {
		t.Fatalf("%q %v: default is only a name", k, err)
	}
}

// The disk limit and the reservation of a jail are properties of its container, which cover everything below it; the
// root clone carries neither.
func TestDiskLimitIsOnTheContainer(t *testing.T) {
	w := newWorld(t)
	w.Files.Dirs["/jailops/projects/alpha/jails/web/root/etc"] = true
	j := w.jail("web")
	j.Spec.Resources.DiskReservation = "3G"
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	c := "tank/jailops/projects/alpha/jails/web"
	for _, k := range [][3]string{{c, "quota", "10G"}, {c, "reservation", "3G"}, {c + "/root", "quota", ""}, {c + "/root", "reservation", ""}, {c + "/var-db", "quota", ""}} {
		if got := w.Z.PropOrEmpty(k[0], k[1]); got != k[2] {
			t.Errorf("%s %s = %q want %q", k[0], k[1], got, k[2])
		}
	}
}
