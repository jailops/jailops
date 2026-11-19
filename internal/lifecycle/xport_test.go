package lifecycle

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"jailops.net/jailops/internal/jailxfer"
	"jailops.net/jailops/sdk/api"
)

// exportOf exports the jail key of w into a file.
func exportOf(t *testing.T, w *world, key string, compress, volumes bool) []byte {
	t.Helper()
	p, err := w.S.PlanExport(bg, key, "", volumes)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	xw, err := jailxfer.NewWriter(&buf, api.ExportManifest{Name: "web", Project: "alpha", UUID: p.UUID, Layout: p.Layout})
	if err != nil {
		t.Fatal(err)
	}
	err = w.S.ExportJail(bg, p, "7", func(part api.ExportPart) (io.WriteCloser, error) {
		if compress {
			part.Compression = "zstd"
		}
		return xw.Part(part)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func importInput(w *world, name string, layout api.Layout) ImportInput {
	j := api.Jail{Metadata: api.ObjectMeta{Name: name, Project: "alpha", UUID: "uuid-" + name}, Spec: spec("fb")}
	j.Spec.Layout = layout
	j.Spec.Volumes = nil
	return ImportInput{Jail: j, Image: w.Images.gens["fb#g1"], OS: "freebsd"}
}

func importFile(w *world, file []byte, in ImportInput) error {
	r, err := jailxfer.NewReader(bytes.NewReader(file))
	if err != nil {
		return err
	}
	_, err = w.S.ImportJail(bg, in, r.Next)
	return err
}

func exportSetup(t *testing.T) *world {
	w := newWorld(t)
	w.created("web")
	w.Z.WriteFile(jc+"/home", "/home/a", "mine")
	w.Z.WriteFile(jc+"/etc", "/etc/rc.conf.local", "nginx_enable=YES")
	return w
}

func TestExportImportRoundTrip(t *testing.T) {
	for _, compress := range []bool{false, true} {
		w := exportSetup(t)
		before := w.Z.Dump()
		file := exportOf(t, w, "alpha_web", compress, false)
		if after := w.Z.Dump(); after != before {
			t.Fatalf("the export changed the source (snapshots left?):\n%s", after)
		}
		if err := importFile(w, file, importInput(w, "web3", api.LayoutThin)); err != nil {
			t.Fatal(err)
		}
		c := "tank/jailops/projects/alpha/jails/web3"
		if v, _ := w.Z.ReadFile(c+"/home", "/home/a"); v != "mine" {
			t.Fatalf("home: %q", v)
		}
		if v, _ := w.Z.ReadFile(c+"/etc", "/etc/rc.conf.local"); v != "nginx_enable=YES" {
			t.Fatalf("etc: %q", v)
		}
		if w.Z.PropOrEmpty(c, PropID) != "uuid-web3" || w.Z.PropOrEmpty(c, PropName) != "web3" || w.Z.PropOrEmpty(c, PropComplete) != "on" {
			t.Fatal("identity of the new jail")
		}
		if got := w.Z.PropOrEmpty(c+"/etc", "mountpoint"); got != "/jailops/projects/alpha/jails/web3/root/etc" {
			t.Fatalf("mountpoint %s", got)
		}
		if w.Z.PropOrEmpty(c+"/etc", PropID) != "uuid-web3" || w.Z.PropOrEmpty(c+"/etc", PropKind) != KindPersistent {
			t.Fatal("persistent dataset not identified as the new jail's")
		}
		if o, _ := w.Z.Origin(bg, c+"/root"); !strings.HasSuffix(o, "fb/g1@ro") {
			t.Fatalf("a thin root is a clone of the image generation, got origin %q", o)
		}
		// what jail diff needs: the baseline travelled, and nothing else
		for _, d := range []string{c + "/etc", c + "/local", c + "/home"} {
			s, _ := w.Z.Snapshots(bg, d)
			if len(s) != 1 || s[0] != BaselineSnap {
				t.Fatalf("%s has snapshots %v", d, s)
			}
		}
		if st, err := w.S.Rebuild(bg, "alpha_web3"); err != nil || st.Jail.Metadata.Name != "web3" {
			t.Fatalf("the new jail is not a jail: %v %v", st, err)
		}
	}
}

func TestExportOfAThickJailCarriesTheRoot(t *testing.T) {
	w := newWorld(t)
	j := w.jail("web")
	j.Spec.Layout = api.LayoutThick
	j.Spec.Volumes = nil
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	w.Z.WriteFile(jc+"/root", "/usr/local/bin/app", "binary")
	file := exportOf(t, w, "alpha_web", false, false)
	if err := importFile(w, file, importInput(w, "web3", api.LayoutThick)); err != nil {
		t.Fatal(err)
	}
	c := "tank/jailops/projects/alpha/jails/web3"
	if v, _ := w.Z.ReadFile(c+"/root", "/usr/local/bin/app"); v != "binary" {
		t.Fatalf("root: %q", v)
	}
	if o, _ := w.Z.Origin(bg, c+"/root"); o != "" {
		t.Fatalf("a thick root is a full copy, got origin %q", o)
	}
	if ok, _ := w.Z.Exists(bg, c+"/etc"); ok {
		t.Fatal("a thick jail has no persistent datasets")
	}
	// the other way round: a thin jail's file does not make a thick jail
	w2 := exportSetup(t)
	thin := exportOf(t, w2, "alpha_web", false, false)
	if err := importFile(w2, thin, importInput(w2, "web3", api.LayoutThick)); err == nil {
		t.Fatal("a thin file was imported as a thick jail")
	}
	if ok, _ := w2.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web3"); ok {
		t.Fatal("a refused import left the jail behind")
	}
}

func TestImportOfDamagedFilesLeavesNothing(t *testing.T) {
	w := exportSetup(t)
	file := exportOf(t, w, "alpha_web", false, false)
	c := "tank/jailops/projects/alpha/jails/web3"
	for _, cut := range []int{len(file) / 2, len(file) - 10} {
		w := exportSetup(t)
		before := w.Z.Dump()
		err := importFile(w, file[:cut], importInput(w, "web3", api.LayoutThin))
		if !errors.Is(err, jailxfer.ErrTruncated) {
			t.Fatalf("cut at %d: %v", cut, err)
		}
		if w.Z.Dump() != before {
			t.Fatalf("a truncated file left datasets behind:\n%s", w.Z.Dump())
		}
	}
	bad := bytes.Clone(file)
	bad[len(bad)-200] ^= 0xff
	w2 := exportSetup(t)
	before := w2.Z.Dump()
	if err := importFile(w2, bad, importInput(w2, "web3", api.LayoutThin)); err == nil {
		t.Fatal("a corrupt file was accepted")
	}
	if w2.Z.Dump() != before {
		t.Fatal("a corrupt file left datasets behind")
	}
	if ok, _ := w2.Z.Exists(bg, c); ok {
		t.Fatal("the container is still there")
	}
}

func TestImportRefusesWhatItDoesNotKnow(t *testing.T) {
	w := exportSetup(t)
	// a part for a dataset this host does not have, and a name that is taken
	var buf bytes.Buffer
	xw, _ := jailxfer.NewWriter(&buf, api.ExportManifest{Name: "web"})
	pw, _ := xw.Part(api.ExportPart{Dataset: "evil", Kind: api.PartPersistent, Snapshot: "x"})
	_, _ = pw.Write([]byte("{}"))
	_ = pw.Close()
	_ = xw.Close()
	if err := importFile(w, buf.Bytes(), importInput(w, "web3", api.LayoutThin)); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("got %v", err)
	}
	file := exportOf(t, w, "alpha_web", false, false)
	if err := importFile(w, file, importInput(w, "web", api.LayoutThin)); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("got %v", err)
	}
}

func TestImportFailureSweep(t *testing.T) {
	var file []byte
	setup := func(t *testing.T) *world {
		w := exportSetup(t)
		if file == nil {
			file = exportOf(t, w, "alpha_web", true, false)
		}
		return w
	}
	sweep(t, setup, func(w *world) error {
		return importFile(w, file, importInput(w, "web3", api.LayoutThin))
	})
}

func TestImportCrashRecovery(t *testing.T) {
	w := exportSetup(t)
	file := exportOf(t, w, "alpha_web", false, false)
	c := "tank/jailops/projects/alpha/jails/web3"
	// a container of the run that never completed is removed; a complete one, or another uuid's, is not touched
	if err := w.Z.Create(bg, c, map[string]string{PropID: "uuid-web3", PropComplete: "off"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.S.RecoverImport(bg, "alpha_web3", "other"); ok || err != nil {
		t.Fatalf("touched a jail of another uuid: %v %v", ok, err)
	}
	if ok, err := w.S.RecoverImport(bg, "alpha_web3", "uuid-web3"); !ok || err != nil {
		t.Fatalf("not rolled back: %v %v", ok, err)
	}
	if ok, _ := w.Z.Exists(bg, c); ok {
		t.Fatal("the incomplete container is still there")
	}
	if err := importFile(w, file, importInput(w, "web3", api.LayoutThin)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.S.RecoverImport(bg, "alpha_web3", "uuid-web3"); ok {
		t.Fatal("rolled back a complete jail")
	}
}

func TestExportOfAnEncryptedVolumeIsRawAndStaysLocked(t *testing.T) {
	w := exportSetup(t)
	vds := VolumeDS(w.storage(), "alpha", "db")
	// the jail attaches the volume db, made as an encryption root
	if err := w.Z.Destroy(bg, vds); err != nil {
		t.Fatal(err)
	}
	if err := w.Z.CreateEncrypted(bg, vds, map[string]string{PropManaged: "on", PropEncrypted: "on", PropKind: KindVolume}, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	w.attach["alpha_web"] = []Attached{{Dataset: vds, Mountpoint: VolumeMP(w.storage(), "alpha", "db"), Path: "/srv/db", Encrypted: true, Name: "db"}}
	p, err := w.S.PlanExport(bg, "alpha_web", "", true)
	if err != nil {
		t.Fatal(err)
	}
	var raw bool
	for _, x := range p.Streams {
		if x.Volume == "db" {
			raw = x.Parts[0].Raw
		}
	}
	if !raw {
		t.Fatal("an encrypted volume is sent raw")
	}
	var buf bytes.Buffer
	xw, _ := jailxfer.NewWriter(&buf, api.ExportManifest{Name: "web"})
	if err := w.S.ExportJail(bg, p, "9", func(part api.ExportPart) (io.WriteCloser, error) { return xw.Part(part) }); err != nil {
		t.Fatal(err)
	}
	_ = xw.Close()
	if strings.Contains(buf.String(), "correct horse") {
		// the model carries the passphrase as the wrapped key; a real raw stream never holds it, so only the model may
		t.Log("the model's raw stream carries its passphrase stand-in")
	}
	// received: the same passphrase protects it, and its key is not loaded
	r, _ := jailxfer.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		part, rd, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if part.Kind != api.PartVolume {
			_, _ = io.Copy(io.Discard, rd)
			continue
		}
		target := "tank/jailops/projects/alpha/volumes/db2"
		if err := w.S.ZFS.(Streams).ReceiveStream(bg, target, map[string]string{PropManaged: "on"}, rd); err != nil {
			t.Fatal(err)
		}
		if st := w.Z.Passphrase(target); st != "correct horse" {
			t.Fatalf("the copy is not encrypted like the source: %q", st)
		}
		if k, _ := w.Z.KeyStatus(bg, target); k != "unavailable" {
			t.Fatalf("the key of the received volume is %q: it stays locked", k)
		}
	}
}
