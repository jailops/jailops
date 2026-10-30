package apps

import (
	"path/filepath"
	"strings"
	"testing"
)

// The reference catalog passes the check: every definition and icon read, nothing wrong.
func TestCheckReferenceCatalog(t *testing.T) {
	r := Check(referenceCatalog)
	if len(r.Problems) != 0 || r.Definitions != 8 || r.Icons != 3 {
		t.Fatalf("%+v", r)
	}
	// one file of it, named directly
	r = Check(filepath.Join(referenceCatalog, "nsd.yaml"))
	if len(r.Problems) != 0 || r.Definitions != 1 || r.Icons != 1 {
		t.Fatalf("%+v", r)
	}
}

// Every invalid file is reported with the daemon's reason; the valid ones are not.
func TestCheckReportsEveryProblem(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "demo.yaml", marshal(t, good()))
	write(t, dir, "noicon.yaml", []byte(strings.Replace(marshalWith(t, "icon", "noicon.svg"), "name: demo", "name: noicon", 1)))
	write(t, dir, "badicon.yaml", []byte(strings.Replace(marshalWith(t, "icon", "badicon.svg"), "name: demo", "name: badicon", 1)))
	write(t, dir, "badicon.svg", []byte("not an svg"))
	write(t, dir, "other.yaml", marshal(t, good()))
	write(t, dir, "notes.txt", []byte("not a definition"))
	r := Check(dir)
	if r.Definitions != 4 || r.Icons != 2 || len(r.Problems) != 3 {
		t.Fatalf("%+v", r)
	}
	want := []struct{ file, reason string }{
		{"badicon.yaml", "icon badicon.svg is not an SVG document"},
		{"noicon.yaml", "icon: lstat " + filepath.Join(dir, "noicon.svg") + ": no such file or directory"},
		{"other.yaml", `name "demo" does not match the file name other.yaml`},
	}
	for i, w := range want {
		p := r.Problems[i]
		if p.File != filepath.Join(dir, w.file) || !strings.Contains(p.Reason, w.reason) {
			t.Errorf("problem %d: %+v, want %s: %s", i, p, w.file, w.reason)
		}
	}

	for _, c := range []struct{ path, want string }{
		{filepath.Join(dir, "missing"), "no such file or directory"},
		{filepath.Join(dir, "notes.txt"), "a definition is a .yaml file"},
		{t.TempDir(), "no definitions (*.yaml) in this directory"},
	} {
		r := Check(c.path)
		if len(r.Problems) != 1 || r.Problems[0].File != c.path || !strings.Contains(r.Problems[0].Reason, c.want) {
			t.Errorf("%s: %+v, want %q", c.path, r, c.want)
		}
	}
}

// A definition written for a newer daemon (a key this one does not know, a
// format version it does not know, a category it does not know) is listed as
// invalid with the reason; the other definitions are unaffected.
func TestNewerDefinitionsAreListedInvalid(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "demo.yaml", marshal(t, good()))
	rename := func(s, name string) []byte { return []byte(strings.Replace(s, "name: demo", "name: "+name, 1)) }
	write(t, dir, "newkey.yaml", rename(marshalWith(t, "replicas", 2), "newkey"))
	write(t, dir, "newformat.yaml", rename(marshalWith(t, "version", 2), "newformat"))
	write(t, dir, "newcategory.yaml", rename(marshalWith(t, "category", "games"), "newcategory"))
	es, err := Load(dir)
	if err != nil || len(es) != 4 {
		t.Fatalf("%v %d", err, len(es))
	}
	want := map[string]string{
		"demo":        "",
		"newcategory": `category: "games" is not one of`,
		"newformat":   "version: must be 1",
		"newkey":      "field replicas not found",
	}
	for _, e := range es {
		s := e.Summary()
		if w := want[e.Name]; w == "" {
			if !s.Valid {
				t.Errorf("%s: %+v", e.Name, s)
			}
		} else if s.Valid || !strings.Contains(s.Error, w) {
			t.Errorf("%s: %+v, want %q", e.Name, s, w)
		}
	}
}
