package lifecycle

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func copySetup(t *testing.T) *world {
	w := newWorld(t)
	j := w.jail("web")
	j.Spec.Network = []api.NetworkAttach{{Network: "lan", IPv4: "10.0.0.5", IPv6: "none", MAC: "02:00:00:00:00:01"}}
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	w.Z.WriteFile(jc+"/root", "/home/a", "data")
	if _, err := w.S.SnapshotJail(bg, "alpha_web", "keep", ""); err != nil { // source has snapshots
		t.Fatal(err)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCopyIsIndependentFullCopy(t *testing.T) {
	w := copySetup(t)
	res, err := w.S.CopyJail(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatal(res.Warnings)
	}
	// a copy attaches no volume: two jails writing one by accident is not what a copy is for
	if r, err := w.S.Rebuild(bg, "alpha_web2"); err != nil || len(r.Jail.Spec.Volumes) != 0 || len(w.attach["alpha_web2"]) != 0 {
		t.Fatalf("the copy attaches %v (%v)", r.Jail.Spec.Volumes, err)
	}
	c2 := "tank/jailops/projects/alpha/jails/web2"
	for _, d := range []string{c2, c2 + "/root"} {
		if o, _ := w.Z.Origin(bg, d); o != "" {
			t.Errorf("%s is a clone of %s: a copy must be a full send/receive", d, o)
		}
	}
	// the source's snapshots are not pinned by the copy, and no temp snapshot is left
	for _, d := range []string{jc, jc + "/root", c2, c2 + "/root"} {
		s, _ := w.Z.Snapshots(bg, d)
		for _, n := range s {
			if strings.HasPrefix(n, "jo-copy-") {
				t.Errorf("%s@%s left behind", d, n)
			}
		}
	}
	if v, _ := w.Z.ReadFile(c2+"/root", "/home/a"); v != "data" {
		t.Fatal(v)
	}
	if got := w.Z.PropOrEmpty(c2+"/root", PropID); got != "uuid-web2" || w.Z.PropOrEmpty(c2, PropName) != "web2" || w.Z.PropOrEmpty(c2, PropComplete) != "on" {
		t.Fatal("identity not rewritten")
	}
	if got := w.Z.PropOrEmpty(c2+"/root", "mountpoint"); got != "/jailops/projects/alpha/jails/web2/root" {
		t.Fatal(got)
	}
	var rec Record
	b, _ := w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web2/spec.json")
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	n := rec.Spec.Network[0]
	if rec.Metadata.Name != "web2" || rec.Metadata.UUID != "uuid-web2" || n.IPv4 != "auto" || n.IPv6 != "none" || n.MAC != "" {
		t.Fatalf("%+v", rec)
	}
	// the copy starts and the source is untouched
	if err := w.S.StartJail(bg, "alpha_web2"); err != nil {
		t.Fatal(err)
	}
	b, _ = w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/spec.json")
	if !strings.Contains(string(b), "10.0.0.5") {
		t.Fatal("source spec changed")
	}
	if _, err := w.S.CopyJail(bg, "alpha_web", "alpha_web2", "u", "8", false); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatal(err)
	}
	if _, err := w.S.CopyJail(bg, "alpha_nope", "x", "u", "8", false); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
}

func TestCopyFailureSweep(t *testing.T) {
	sweep(t, copySetup, func(w *world) error {
		r, err := w.S.CopyJail(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", false)
		if err == nil && len(r.Warnings) > 0 {
			return nil
		}
		return err
	})
}

func TestCopyCrashRecovery(t *testing.T) {
	w := copySetup(t)
	p, _, err := w.S.PlanCopy(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", false)
	if err != nil {
		t.Fatal(err)
	}
	for k := 1; k <= len(p.Steps); k++ {
		w := copySetup(t)
		before := w.state()
		p, _, _ := w.S.PlanCopy(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", false)
		crash(t, w, "copy/alpha_web2", p, k)
		rp, _, err := w.S.PlanCopy(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", true)
		if err != nil {
			t.Fatal(err)
		}
		if found, err := w.S.RecoverRun(bg, "copy", "alpha_web2", rp); !found || err != nil {
			t.Fatal(found, err)
		}
		if w.state() != before {
			t.Fatalf("crash after step %d (%s) not rolled back", k, p.Steps[k-1].Name)
		}
	}
}

func TestRebuildFromDatasets(t *testing.T) {
	w := newWorld(t)
	w.running("web")
	r, err := w.S.Rebuild(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	if r.Jail.Metadata.Name != "web" || r.Jail.Metadata.UUID != "uuid-web" || r.Jail.Status.ImageGeneration != "fb#g1" || r.Jail.Status.Power != api.PowerRunning || r.UnsupportedLayout {
		t.Fatalf("%+v", r)
	}
	if len(r.Problems) != 0 || len(r.Notes) != 0 {
		t.Fatalf("%v %v", r.Problems, r.Notes)
	}
	if r.Jail.Spec.Volumes[0].Name != "db" {
		t.Fatal(r.Jail.Spec)
	}
}

func TestRebuildProblems(t *testing.T) {
	tests := []struct {
		name string
		mut  func(w *world)
		want string
		bund bool
	}{
		{"corrupt spec falls back to bundle", func(w *world) {
			w.Files.Data["/jailops/projects/alpha/jails/web/spec.json"] = []byte("{oops")
			delete(w.Files.Data, "/jailops/projects/alpha/jails/web/spec.json.previous")
		}, "spec.json", true},
		{"missing volume", func(w *world) { w.Z.DestroyRecursive(bg, "tank/jailops/projects/alpha/volumes/db") }, "volume dataset is missing: db", false},
		{"incomplete creation", func(w *world) { w.Z.Set(bg, jc, map[string]string{PropComplete: "off"}) }, "creation did not complete", false},
		{"uuid mismatch", func(w *world) { w.Z.Set(bg, jc, map[string]string{PropID: "other"}) }, "uuid mismatch", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.created("web")
			tc.mut(w)
			r, err := w.S.Rebuild(bg, "alpha_web")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(r.Problems, "|"), tc.want) || r.FromBundle != tc.bund {
				t.Fatalf("%v", r.Problems)
			}
			if r.Jail.Metadata.Name != "web" {
				t.Fatal("entry not rebuilt")
			}
		})
	}
	w := newWorld(t)
	w.created("web")
	delete(w.Files.Data, "/jailops/projects/alpha/jails/web/spec.json")
	delete(w.Files.Data, "/jailops/projects/alpha/jails/web/bundle.json")
	if _, err := w.S.Rebuild(bg, "alpha_web"); !api.IsCode(err, api.CodeInternal) {
		t.Fatalf("no records at all must be an error: %v", err)
	}
}

func TestRebuildAllIgnoresForeign(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.created("db")
	w.Z.Create(bg, "tank/jailops/projects/alpha/jails/alien", nil)
	w.Z.Create(bg, "tank/jailops/projects/alpha/jails/halfdone", map[string]string{PropManaged: "on", PropKind: KindContainer, PropID: "x"})
	got, errs, err := w.S.RebuildAll(bg)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.Jail.Metadata.Name] = true
	}
	if !names["web"] || !names["db"] || len(got) != 2 {
		t.Fatal(names)
	}
	if errs["alpha_halfdone"] == nil || errs["alpha_alien"] != nil {
		t.Fatal(errs)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/alien"); !ok {
		t.Fatal("foreign dataset touched")
	}
}

// A copy from a snapshot of the source holds what the snapshot held, not what was written after it; the snapshot stays on
// the source and the copy does not carry it.
func TestCopyFromASnapshotOfTheSource(t *testing.T) {
	w := copySetup(t)
	w.Z.WriteFile(jc+"/root", "/home/later", "newer")
	if _, err := w.S.CopyJailFrom(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", "keep", false); err != nil {
		t.Fatal(err)
	}
	c2 := "tank/jailops/projects/alpha/jails/web2"
	if v, _ := w.Z.ReadFile(c2+"/root", "/home/a"); v != "data" {
		t.Fatalf("the data of the snapshot is not in the copy: %q", v)
	}
	if v, ok := w.Z.ReadFile(c2+"/root", "/home/later"); ok {
		t.Fatalf("a file written after the snapshot is in the copy: %q", v)
	}
	for _, d := range []string{jc, jc + "/root"} {
		if ok, _ := w.Z.Exists(bg, d+"@keep"); !ok {
			t.Errorf("%s@keep was dropped from the source", d)
		}
	}
	for _, d := range []string{c2, c2 + "/root"} {
		s, _ := w.Z.Snapshots(bg, d)
		if len(s) != 0 {
			t.Errorf("%s carries snapshots %v", d, s)
		}
	}
	if _, err := w.S.CopyJailFrom(bg, "alpha_web", "alpha_web3", "uuid-web3", "8", "nope", false); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("a snapshot the source does not have: %v", err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web3"); ok {
		t.Fatal("a refused copy left datasets")
	}
	if _, err := w.S.CopyJailFrom(bg, "alpha_web", "alpha_web3", "uuid-web3", "8", "bad name", false); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("a bad snapshot name: %v", err)
	}
}

// The copy of a jail takes over no public port of the source.
func TestCopyKeepsNoExposure(t *testing.T) {
	w := copySetup(t)
	b, _ := w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web/spec.json")
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	rec.Spec.Expose = []api.ExposeEntry{{Proto: "tcp", Ports: []string{"80"}}}
	w.Files.Data["/jailops/projects/alpha/jails/web/spec.json"], _ = json.Marshal(rec)
	if _, err := w.S.CopyJail(bg, "alpha_web", "alpha_web2", "uuid-web2", "7", false); err != nil {
		t.Fatal(err)
	}
	b, _ = w.Files.ReadFile(bg, "/jailops/projects/alpha/jails/web2/spec.json")
	var got Record
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Expose) != 0 {
		t.Fatalf("the copy exposes %+v", got.Spec.Expose)
	}
}
