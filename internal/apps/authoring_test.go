package apps

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// Every definition of the reference catalog written as a draft reads back as
// the same definition: the writer loses nothing and quotes what needs it.
func TestDraftRoundTrip(t *testing.T) {
	es, err := Load(referenceCatalog)
	if err != nil || len(es) == 0 {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Err != nil {
			t.Fatalf("%s: %v", e.Name, e.Err)
		}
		out := Draft{App: e.App, Todo: map[string]string{"health": "check it"}, Header: []string{"a header line", ""}}.YAML()
		back, err := Parse(e.Name, out)
		if err != nil {
			t.Fatalf("%s: %v\n%s", e.Name, err, out)
		}
		if !reflect.DeepEqual(back, e.App) {
			t.Errorf("%s: read back differently:\n%+v\n%+v\n%s", e.Name, back, e.App, out)
		}
		if got := TodoLines(out); len(got) != 1 {
			t.Errorf("%s: TODO lines %v", e.Name, got)
		}
	}
	// strings a plain scalar would change: numbers, booleans, comments, flow characters, colons, white space
	tricky := []string{"1.0", "true", "no", "a #b", "a: b", "x:", " lead", "trail ", "-1", "[x]", "a,b", "{x}", "'q'", "\"q\"", "~", "%x", "*x", "&x", "!x", "@x", "`x`", "|x", ">x", "é", "tab\there"}
	a := good()
	a.Setup = [][]string{append([]string{"/bin/echo"}, tricky...)}
	a.Health = &api.AppHealth{Exec: append([]string{"/bin/echo", "-n", "--x=y"}, tricky...), WaitSeconds: 5}
	a.Services = []api.AppService{{Name: "demo", RC: map[string]string{}}}
	for i, s := range tricky[:len(tricky)-1] { // an rc value is one printable line, without a tab
		a.Services[0].RC["demo_k"+string(rune('a'+i))] = s
	}
	a.Summary = "a: b # c"
	a.Description = "first\n\n  indented\nlast"
	a.Notes = "keep\n\n"
	a.Ports = []api.AppPort{{Name: "p", Port: 1, Proto: "udp", Description: "yes"}}
	if err := Validate(&a); err != nil {
		t.Fatal(err)
	}
	out := Draft{App: a}.YAML()
	back, err := Parse("demo", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, a) {
		t.Errorf("read back differently:\n%+v\n%+v\n%s", back, a, out)
	}
	// a description that starts with white space cannot be a literal block
	a.Description = "  starts indented\n"
	if back, err := Parse("demo", Draft{App: a}.YAML()); err != nil || back.Description != a.Description {
		t.Errorf("%q %v", back.Description, err)
	}
}

// A starter parses and validates as it is; validate reports its TODO lines.
func TestStarter(t *testing.T) {
	dir := t.TempDir()
	d, err := Starter(StarterOptions{Name: "memcached", Packages: []string{"memcached"}, Services: []string{"memcached"},
		Ports: []string{"11211/tcp", "11211/udp"}, Category: "database"})
	if err != nil {
		t.Fatal(err)
	}
	a := d.App
	if a.Category != api.AppDatabase || a.Packages[0].Name != "memcached" || a.Services[0].Name != "memcached" ||
		len(a.Ports) != 2 || a.Ports[0].Name != "memcached" || a.Ports[1].Name != "memcached-11211" || a.Ports[1].Proto != "udp" ||
		a.Health == nil || a.Health.TCP != 11211 || a.Config[0].Restart != "memcached" || a.Icon != "memcached.svg" {
		t.Fatalf("%+v", a)
	}
	write(t, dir, "memcached.yaml", d.YAML())
	write(t, dir, "memcached.svg", StarterIcon("memcached"))
	r := Check(filepath.Join(dir, "memcached.yaml"))
	if r.Definitions != 1 || r.Icons != 1 || len(r.Problems) == 0 {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(string(d.YAML()), "\n")
	for _, p := range r.Problems {
		var n int
		if _, err := fmt.Sscanf(p.Reason, "line %d: replace TODO", &n); err != nil || !strings.Contains(lines[n-1], "TODO") {
			t.Errorf("%+v: %v", p, err)
		}
	}

	// without options: every unknown is a TODO, and the file still validates
	d, err = Starter(StarterOptions{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("x", d.YAML()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"category", "packages", "services", "ports", "health", "data", "config", "logs"} {
		if d.Todo[k] == "" {
			t.Errorf("no TODO for %s", k)
		}
	}
	if d.App.Health != nil || len(d.App.Services) != 0 || d.App.Packages[0].Name != TodoMarker {
		t.Errorf("%+v", d.App)
	}
	// every top-level key is written
	for _, k := range []string{"version", "name", "title", "summary", "description", "category", "homepage", "license", "icon", "image",
		"packages", "services", "ports", "data", "config", "logs", "setup", "sysvipc", "health", "resources", "notes"} {
		if !bytes.Contains(d.YAML(), []byte("\n"+k+":")) {
			t.Errorf("key %s is missing", k)
		}
	}

	for _, c := range []struct {
		o    StarterOptions
		want string
	}{
		{StarterOptions{Name: "Bad"}, "not an app name"},
		{StarterOptions{Name: "a", Category: "games"}, `unknown category "games"`},
		{StarterOptions{Name: "a", Ports: []string{"0"}}, "must be N or N/tcp or N/udp"},
		{StarterOptions{Name: "a", Ports: []string{"80/sctp"}}, "must be N or N/tcp or N/udp"},
		{StarterOptions{Name: "a", Ports: []string{"80", "80/tcp"}}, "80/tcp is listed twice"},
		{StarterOptions{Name: "a", Packages: []string{"-f"}}, "is not a package name"},
		{StarterOptions{Name: "a", Services: []string{"Bad Name"}}, "is not an rc.d service name"},
	} {
		if _, err := Starter(c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.o, err, c.want)
		}
	}
	// a long app name gives the ports a name within 32 characters
	long := strings.Repeat("a", 40)
	if d, err := Starter(StarterOptions{Name: long, Ports: []string{"1", "2"}}); err != nil || d.App.Ports[0].Name != "main" || d.App.Ports[1].Name != "main-2" {
		t.Errorf("%v %+v", err, d.App.Ports)
	}
}

// The placeholder icon is an SVG document the loader accepts.
func TestStarterIcon(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.svg", StarterIcon("x"))
	uri, err := loadIcon(dir, "x.svg")
	if err != nil || !strings.HasPrefix(uri, "data:image/svg+xml;base64,") {
		t.Fatal(uri, err)
	}
	if !bytes.Contains(StarterIcon("memcached"), []byte(">M</text>")) || !bytes.Contains(StarterIcon(""), []byte(">?</text>")) {
		t.Error(string(StarterIcon("memcached")))
	}
}

// TODO markers are reported per line, in comments too; a definition without one has none.
func TestTodoLines(t *testing.T) {
	got := TodoLines([]byte("a: 1\n# TODO: x\nb: TODO\nc: TODOS\nd: todo\ne: [TODO:TODO]\n"))
	if !reflect.DeepEqual(got, []int{2, 3, 6}) {
		t.Fatal(got)
	}
	r := Check(referenceCatalog)
	for _, p := range r.Problems {
		t.Errorf("%+v", p)
	}
}

// The worked example of docs/apps.md ("Write your own app") is a valid
// definition with its icon and no TODO left, and its icon is the placeholder
// app new writes.
func TestWorkedExample(t *testing.T) {
	dir := filepath.Join("testdata", "authoring")
	r := Check(filepath.Join(dir, "memcached.yaml"))
	if r.Definitions != 1 || r.Icons != 1 || len(r.Problems) != 0 {
		t.Fatalf("%+v", r)
	}
	icon, err := os.ReadFile(filepath.Join(dir, "memcached.svg"))
	if err != nil || !bytes.Equal(icon, StarterIcon("memcached")) {
		t.Errorf("%v %s", err, icon)
	}
	e, ok, err := Find(dir, "memcached")
	if err != nil || !ok || e.Err != nil {
		t.Fatal(err, ok, e.Err)
	}
	a := e.App
	if a.Ports[0].Port != 11211 || a.Ports[0].Proto != "tcp" || a.Health.TCP != 11211 || len(a.Data) != 0 ||
		!reflect.DeepEqual(a.Setup, [][]string{{"/bin/mkdir", "-p", "/var/run/memcached"}, {"/usr/sbin/chown", "nobody:nobody", "/var/run/memcached"}}) {
		t.Errorf("%+v", a)
	}
	// the documentation carries the same file
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "apps.md"))
	if err != nil {
		t.Fatal(err)
	}
	def, _ := os.ReadFile(filepath.Join(dir, "memcached.yaml"))
	body := string(def)
	body = body[strings.Index(body, "version: 1"):]
	if !strings.Contains(string(doc), body) {
		t.Error("docs/apps.md does not carry testdata/authoring/memcached.yaml from version: 1 on")
	}
}

// The local directory is read with the packaged catalog: a local definition
// is listed with its source, wins over a packaged one of the same name (also
// when it is invalid) and is found first.
func TestLoadAllLocal(t *testing.T) {
	cat, loc := t.TempDir(), t.TempDir()
	rename := func(name string) []byte {
		return []byte(strings.Replace(string(marshal(t, good())), "name: demo", "name: "+name, 1))
	}
	write(t, cat, "demo.yaml", marshal(t, good()))
	write(t, cat, "shared.yaml", rename("shared"))
	write(t, cat, "broken.yaml", rename("broken"))
	write(t, loc, "mine.yaml", rename("mine"))
	write(t, loc, "shared.yaml", rename("shared"))
	write(t, loc, "broken.yaml", []byte("version: 1\nname: broken\n"))
	es, err := LoadAll(cat, loc)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		name             string
		source, override api.AppSource
		valid            bool
	}
	var got []row
	for _, e := range es {
		s := e.Summary()
		got = append(got, row{s.Name, s.Source, s.Overrides, s.Valid})
	}
	want := []row{
		{"broken", api.AppSourceLocal, api.AppSourceCatalog, false},
		{"demo", api.AppSourceCatalog, "", true},
		{"mine", api.AppSourceLocal, "", true},
		{"shared", api.AppSourceLocal, api.AppSourceCatalog, true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	for _, c := range []struct {
		name             string
		source, override api.AppSource
	}{{"shared", api.AppSourceLocal, api.AppSourceCatalog}, {"mine", api.AppSourceLocal, ""}, {"demo", api.AppSourceCatalog, ""}} {
		e, ok, err := FindAll(cat, loc, c.name)
		if err != nil || !ok || e.Err != nil || e.Source != c.source || e.Overrides != c.override {
			t.Errorf("%s: %v %v %+v", c.name, err, ok, e)
		}
		if d := e.Detail(); d.Source != c.source || d.Overrides != c.override {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
	if _, ok, _ := FindAll(cat, loc, "missing"); ok {
		t.Error("found a missing app")
	}
	// a missing local directory is nothing; the same directory twice is read once
	es, err = LoadAll(cat, filepath.Join(loc, "missing"))
	if err != nil || len(es) != 3 {
		t.Fatal(err, len(es))
	}
	if es, _ := LoadAll(cat, cat); len(es) != 3 || es[0].Source != api.AppSourceCatalog {
		t.Fatal(es)
	}
	// a local directory that cannot be read is an error that names it
	write(t, loc, "notadir", []byte("x"))
	if _, err := LoadAll(cat, filepath.Join(loc, "notadir")); err == nil || !strings.Contains(err.Error(), "notadir") {
		t.Fatal(err)
	}
}
