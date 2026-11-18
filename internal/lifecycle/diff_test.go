package lifecycle

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

const diffC = "tank/jailops/projects/alpha/jails/web"

// A thin jail gets a baseline snapshot on each persistent dataset when it is made, and none on the container, so the
// snapshots of the jail (list, rollback, retention) do not see it.
func TestCreateTakesBaselines(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	for _, d := range []string{"etc", "local", "var-db", "var-log", "home"} {
		if ok, _ := w.Z.Exists(bg, diffC+"/"+d+"@"+BaselineSnap); !ok {
			t.Errorf("no baseline on %s", d)
		}
	}
	l, err := w.S.ListSnapshots(bg, "alpha_web")
	if err != nil || len(l) != 0 {
		t.Fatalf("the baselines show in the snapshots of the jail: %v %v", l, err)
	}
	if _, err := w.S.DestroyJail(bg, "alpha_web", false, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, diffC+"/etc"); ok {
		t.Error("destroy left the persistent datasets")
	}
}

func TestDiffJail(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.Z.WriteFile(diffC+"/root", "/etc/version", "changed")
	w.Z.WriteFile(diffC+"/root", "/boot/loader.conf", "new")
	w.Z.WriteFile(diffC+"/local", "/etc/web.conf", "new")
	w.Z.WriteFile(diffC+"/etc", "/rc.conf", "sshd_enable=YES")
	r, err := w.S.DiffJail(bg, "alpha_web", 100)
	if err != nil {
		t.Fatal(err)
	}
	if r.Image != "fb#g1" || len(r.Datasets) != 9 || r.Datasets[0].Name != "root" || r.Datasets[0].Base != DiffBaseImage {
		t.Fatalf("%+v", r)
	}
	got := map[string]string{}
	for _, d := range r.Datasets {
		for _, e := range d.Entries {
			got[e.Path] = e.Kind
		}
		if d.Name != "root" && d.Base != DiffBaseBaseline {
			t.Errorf("%s compared with %s", d.Name, d.Base)
		}
	}
	want := map[string]string{"/etc/version": "M", "/boot/loader.conf": "+", "/usr/local/etc/web.conf": "+", "/etc/rc.conf": "+"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s: %q, want %q (%v)", p, got[p], k, got)
		}
	}
	// a stopped jail is mounted for the diff only
	for _, ds := range []string{diffC + "/root", diffC + "/etc"} {
		if m, _ := w.Z.Mounted(bg, ds); m {
			t.Errorf("%s stays mounted after the diff", ds)
		}
	}
	r, _ = w.S.DiffJail(bg, "alpha_web", 1)
	if d := r.Datasets[0]; len(d.Entries) != 1 || d.Total != 2 || !d.Truncated {
		t.Errorf("limit: %+v", d)
	}
}

// A jail made before the baseline existed is compared with the image's directory, and says so.
func TestDiffJailWithoutBaseline(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	if err := w.Z.Destroy(bg, diffC+"/etc@"+BaselineSnap); err != nil {
		t.Fatal(err)
	}
	r, err := w.S.DiffJail(bg, "alpha_web", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range r.Datasets {
		if d.Name == "etc" && (d.Base != DiffBaseTree || d.Note == "") {
			t.Errorf("etc: %+v", d)
		}
	}
}

func TestDiffJailThickRefused(t *testing.T) {
	w := newWorld(t)
	j := w.jail("deb")
	j.Spec.Image = "alp"
	j.Spec.Volumes = nil
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.S.DiffJail(bg, "alpha_deb", 10); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("%v", err)
	}
}
