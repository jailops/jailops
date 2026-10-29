package apps

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/sdk/api"
)

// good is a complete, valid definition.
func good() api.App {
	return api.App{
		Version: 1, Name: "demo", Title: "Demo", Summary: "A demo application.",
		Description: "First paragraph.\n\nSecond paragraph.", Category: api.AppWeb,
		Homepage: "https://example.org/demo", License: "BSD-2-Clause",
		Image:     api.AppImage{OS: "freebsd", ReleaseMin: "15.1"},
		Packages:  []api.AppPackage{{Name: "demo-server", Version: "1.2.3_1"}},
		Services:  []api.AppService{{Name: "demo", RC: map[string]string{"demo_flags": "-v"}}},
		Ports:     []api.AppPort{{Name: "http", Port: 8080, Description: "the web interface", Expose: true}},
		Data:      []api.AppPath{{Path: "/var/db/demo", Description: "state"}},
		Config:    []api.AppConfig{{Path: "/usr/local/etc/demo.conf", Restart: "demo"}},
		Logs:      []api.AppLog{{Name: "server", Path: "/var/log/demo/server.log"}},
		Setup:     [][]string{{"/usr/sbin/chown", "demo:demo", "/var/db/demo"}},
		Health:    &api.AppHealth{HTTP: &api.AppHTTPCheck{Port: 8080, Path: "/healthz"}},
		Resources: api.AppResources{Memory: "512M", Disk: "2G", CPU: "1"},
		Notes:     "Open http://ADDRESS:8080/.",
	}
}

func marshal(t *testing.T, app api.App) []byte {
	t.Helper()
	b, err := yaml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidFillsDefaults(t *testing.T) {
	app, err := Parse("demo", marshal(t, good()))
	if err != nil {
		t.Fatal(err)
	}
	if app.Packages[0].Repo != "ports" || app.Ports[0].Proto != "tcp" || app.Services[0].Start == nil || !*app.Services[0].Start ||
		app.Health.WaitSeconds != 60 || app.Health.HTTP.Status != 200 {
		t.Fatalf("defaults not filled: %+v", app)
	}
}

func TestInvalidDefinitions(t *testing.T) {
	long := strings.Repeat("x", 3000)
	cases := []struct {
		name   string
		mutate func(*api.App)
		want   string // a fragment of the error
	}{
		{"version 2", func(a *api.App) { a.Version = 2 }, "version: must be 1"},
		{"version 0", func(a *api.App) { a.Version = 0 }, "version: must be 1"},
		{"bad name", func(a *api.App) { a.Name = "Demo" }, "name"},
		{"name with slash", func(a *api.App) { a.Name = "a/b" }, "name"},
		{"no title", func(a *api.App) { a.Title = "" }, "title: is required"},
		{"title control", func(a *api.App) { a.Title = "De\x07mo" }, "title: holds a control"},
		{"title spaces", func(a *api.App) { a.Title = " Demo" }, "white space"},
		{"long summary", func(a *api.App) { a.Summary = strings.Repeat("s", 141) }, "summary: is longer than 140"},
		{"multi-line summary", func(a *api.App) { a.Summary = "one\ntwo" }, "summary: holds a control"},
		{"long description", func(a *api.App) { a.Description = long }, "description: is longer than 2000"},
		{"description control", func(a *api.App) { a.Description = "a\tb" }, "description: holds a control"},
		{"bad category", func(a *api.App) { a.Category = "games" }, "category: \"games\" is not one of database, web"},
		{"no category", func(a *api.App) { a.Category = "" }, "category"},
		{"http homepage", func(a *api.App) { a.Homepage = "http://example.org/" }, "homepage: \"http://example.org/\" must be an https URL"},
		{"homepage no host", func(a *api.App) { a.Homepage = "https:///x" }, "homepage"},
		{"homepage user", func(a *api.App) { a.Homepage = "https://u:p@example.org/" }, "homepage"},
		{"homepage junk", func(a *api.App) { a.Homepage = "example.org" }, "homepage"},
		{"long license", func(a *api.App) { a.License = strings.Repeat("l", 65) }, "license: is longer"},
		{"icon path", func(a *api.App) { a.Icon = "../x.png" }, "icon"},
		{"icon slash", func(a *api.App) { a.Icon = "icons/x.png" }, "icon"},
		{"icon type", func(a *api.App) { a.Icon = "x.gif" }, "icon"},
		{"image os", func(a *api.App) { a.Image.OS = "linux" }, "image.os: must be freebsd"},
		{"image release form", func(a *api.App) { a.Image.ReleaseMin = "15.1-RELEASE" }, "image.release_min"},
		{"image release old", func(a *api.App) { a.Image.ReleaseMin = "14.3" }, "older than 15.1"},
		{"image release 15.0", func(a *api.App) { a.Image.ReleaseMin = "15.0" }, "older than 15.1"},
		{"image release empty", func(a *api.App) { a.Image.ReleaseMin = "" }, "image.release_min"},
		{"no packages", func(a *api.App) { a.Packages = nil }, "packages: at least one"},
		{"bad package name", func(a *api.App) { a.Packages[0].Name = "-rf" }, "packages[0].name"},
		{"package space", func(a *api.App) { a.Packages[0].Name = "a b" }, "packages[0].name"},
		{"package repo", func(a *api.App) { a.Packages[0].Repo = "pypi" }, "packages[0].repo: \"pypi\" must be ports or jailops"},
		{"package version", func(a *api.App) { a.Packages[0].Version = "v1 2" }, "packages[0].version"},
		{"package twice", func(a *api.App) { a.Packages = append(a.Packages, api.AppPackage{Name: "demo-server"}) }, "packages[1].name: \"demo-server\" is listed twice"},
		{"service name", func(a *api.App) { a.Services[0].Name = "Demo Service" }, "services[0].name"},
		{"service twice", func(a *api.App) { a.Services = append(a.Services, api.AppService{Name: "demo"}) }, "services[1].name: \"demo\" is listed twice"},
		{"rc key", func(a *api.App) { a.Services[0].RC = map[string]string{"demo-flags": "x"} }, "services[0].rc: \"demo-flags\" is not an rc.conf variable"},
		{"rc key injection", func(a *api.App) { a.Services[0].RC = map[string]string{"demo_flags=\"x\"\nevil": "y"} }, "services[0].rc"},
		{"rc enable", func(a *api.App) { a.Services[0].RC = map[string]string{"demo_enable": "YES"} }, "demo_enable is written by the installer"},
		{"rc enable of a converted name", func(a *api.App) {
			a.Services = append(a.Services, api.AppService{Name: "demo-worker.d"})
			a.Services[0].RC = map[string]string{"demo_worker_d_enable": "NO"}
		}, "services[0].rc: demo_worker_d_enable is written by the installer"},
		{"services clash in rc.conf", func(a *api.App) {
			a.Services = []api.AppService{{Name: "foo-bar"}, {Name: "foo_bar"}}
			a.Config[0].Restart = ""
		}, "services[1].name: \"foo-bar\" and \"foo_bar\" are the same name in rc.conf (foo_bar)"},
		{"services clash with a dot", func(a *api.App) {
			a.Services = []api.AppService{{Name: "foo.bar"}, {Name: "foo-bar"}}
			a.Config[0].Restart = ""
		}, "are the same name in rc.conf"},
		{"rc value newline", func(a *api.App) { a.Services[0].RC = map[string]string{"demo_flags": "a\nb_enable=YES"} }, "services[0].rc: demo_flags holds a control"},
		{"rc value long", func(a *api.App) { a.Services[0].RC = map[string]string{"demo_flags": long} }, "services[0].rc: demo_flags is longer"},
		{"port name", func(a *api.App) { a.Ports[0].Name = "HTTP" }, "ports[0].name"},
		{"port zero", func(a *api.App) { a.Ports[0].Port = 0 }, "ports[0].port: 0 is not a port"},
		{"port high", func(a *api.App) { a.Ports[0].Port = 70000 }, "ports[0].port"},
		{"port proto", func(a *api.App) { a.Ports[0].Proto = "sctp" }, "ports[0].proto"},
		{"port twice", func(a *api.App) { a.Ports = append(a.Ports, api.AppPort{Name: "alt", Port: 8080}) }, "ports[1].port: 8080/tcp is listed twice"},
		{"port name twice", func(a *api.App) { a.Ports = append(a.Ports, api.AppPort{Name: "http", Port: 8081}) }, "ports[1].name: \"http\" is listed twice"},
		{"port description", func(a *api.App) { a.Ports[0].Description = strings.Repeat("d", 141) }, "ports[0].description"},
		{"data relative", func(a *api.App) { a.Data[0].Path = "var/db/demo" }, "data[0].path"},
		{"data dotdot", func(a *api.App) { a.Data[0].Path = "/var/db/../../etc" }, "data[0].path"},
		{"data etc", func(a *api.App) { a.Data[0].Path = "/etc/demo" }, "data[0].path: /etc/demo is a path of the base system"},
		{"data usr bin", func(a *api.App) { a.Data[0].Path = "/usr/bin" }, "base system"},
		{"data local bin", func(a *api.App) { a.Data[0].Path = "/usr/local/bin/demo" }, "base system"},
		{"data rc.d", func(a *api.App) { a.Data[0].Path = "/usr/local/etc/rc.d/demo" }, "base system"},
		{"data var run", func(a *api.App) { a.Data[0].Path = "/var/run/demo" }, "base system"},
		{"data root", func(a *api.App) { a.Data[0].Path = "/" }, "data[0].path"},
		{"data var itself", func(a *api.App) { a.Data[0].Path = "/var" }, "must lie below one of /var, /usr/local, /usr/local/etc, /home"},
		{"data usr local itself", func(a *api.App) { a.Data[0].Path = "/usr/local" }, "data[0].path: /usr/local is a persistent dataset of every jail"},
		{"data var db itself", func(a *api.App) { a.Data[0].Path = "/var/db" }, "data[0].path: /var/db is a persistent dataset of every jail"},
		{"data var log itself", func(a *api.App) { a.Data[0].Path = "/var/log" }, "data[0].path: /var/log is a persistent dataset"},
		{"data var spool itself", func(a *api.App) { a.Data[0].Path = "/var/spool" }, "data[0].path: /var/spool is a persistent dataset"},
		{"data var mail itself", func(a *api.App) { a.Data[0].Path = "/var/mail" }, "data[0].path: /var/mail is a persistent dataset"},
		{"data home itself", func(a *api.App) { a.Data[0].Path = "/home" }, "data[0].path: /home is a persistent dataset"},
		{"data root dir", func(a *api.App) { a.Data[0].Path = "/root" }, "data[0].path: /root is a persistent dataset"},
		{"config var db itself", func(a *api.App) { a.Config[0].Path = "/var/db" }, "config[0].path: /var/db is a persistent dataset"},
		{"data elsewhere", func(a *api.App) { a.Data[0].Path = "/opt/demo" }, "must lie below"},
		{"data trailing slash", func(a *api.App) { a.Data[0].Path = "/var/db/demo/" }, "data[0].path"},
		{"data space", func(a *api.App) { a.Data[0].Path = "/var/db/de mo" }, "data[0].path"},
		{"data overlap", func(a *api.App) { a.Data = append(a.Data, api.AppPath{Path: "/var/db/demo/sub"}) }, "data[1].path: /var/db/demo/sub overlaps /var/db/demo"},
		{"data twice", func(a *api.App) { a.Data = append(a.Data, api.AppPath{Path: "/var/db/demo"}) }, "overlaps"},
		{"data long", func(a *api.App) { a.Data[0].Path = "/var/db/" + strings.Repeat("d", 1100) }, "data[0].path: is longer than 1024"},
		{"config etc", func(a *api.App) { a.Config[0].Path = "/etc/rc.conf" }, "config[0].path: /etc/rc.conf is a path of the base system"},
		{"config twice", func(a *api.App) { a.Config = append(a.Config, api.AppConfig{Path: "/usr/local/etc/demo.conf"}) }, "config[1].path: /usr/local/etc/demo.conf is listed twice"},
		{"config restart unknown", func(a *api.App) { a.Config[0].Restart = "other" }, "config[0].restart: \"other\" is not one of the services"},
		{"log name", func(a *api.App) { a.Logs[0].Name = "Server Log" }, "logs[0].name"},
		{"log twice", func(a *api.App) { a.Logs = append(a.Logs, api.AppLog{Name: "server", Path: "/var/log/x"}) }, "logs[1].name: \"server\" is listed twice"},
		{"log etc", func(a *api.App) { a.Logs[0].Path = "/etc/demo.log" }, "logs[0].path"},
		{"log home", func(a *api.App) { a.Logs[0].Path = "/home/demo/x.log" }, "logs[0].path: /home/demo/x.log must lie below one of /var/log, /var/db, /usr/local"},
		{"log var tmp", func(a *api.App) { a.Logs[0].Path = "/var/tmp/x.log" }, "base system"},
		{"setup empty argv", func(a *api.App) { a.Setup = [][]string{{}} }, "setup[0]: is empty"},
		{"setup relative", func(a *api.App) { a.Setup = [][]string{{"chown", "x"}} }, "setup[0]: \"chown\" must be an absolute path"},
		{"setup shell", func(a *api.App) { a.Setup = [][]string{{"/bin/sh", "-c", "rm -rf /\n"}} }, "setup[0]: an argument holds a control character"},
		{"setup nul", func(a *api.App) { a.Setup = [][]string{{"/bin/echo", "a\x00b"}} }, "setup[0]"},
		{"setup many", func(a *api.App) {
			for range 40 {
				a.Setup = append(a.Setup, []string{"/bin/true"})
			}
		}, "setup: has more than 32 commands"},
		{"setup long arg", func(a *api.App) { a.Setup = [][]string{{"/bin/echo", long}} }, "setup[0]: an argument is longer"},
		{"health none", func(a *api.App) { a.Health = &api.AppHealth{} }, "health: exactly one of tcp, http and exec"},
		{"health two", func(a *api.App) { a.Health.TCP = 80 }, "health: exactly one"},
		{"health tcp", func(a *api.App) { a.Health = &api.AppHealth{TCP: 99999} }, "health.tcp: 99999 is not a port"},
		{"health http port", func(a *api.App) { a.Health.HTTP.Port = 0 }, "health.http.port"},
		{"health http path", func(a *api.App) { a.Health.HTTP.Path = "healthz" }, "health.http.path"},
		{"health http path space", func(a *api.App) { a.Health.HTTP.Path = "/a b" }, "health.http.path"},
		{"health http status", func(a *api.App) { a.Health.HTTP.Status = 99 }, "health.http.status: 99 is not an HTTP status"},
		{"health exec relative", func(a *api.App) { a.Health = &api.AppHealth{Exec: []string{"pg_isready"}} }, "health.exec: \"pg_isready\" must be an absolute path"},
		{"health exec empty", func(a *api.App) { a.Health = &api.AppHealth{Exec: []string{}} }, "health"},
		{"health wait", func(a *api.App) { a.Health.WaitSeconds = 301 }, "health.wait_seconds: must be between 1 and 300"},
		{"health wait negative", func(a *api.App) { a.Health.WaitSeconds = -1 }, "health.wait_seconds"},
		{"memory", func(a *api.App) { a.Resources.Memory = "lots" }, "resources.memory: \"lots\" is not a size"},
		{"disk", func(a *api.App) { a.Resources.Disk = "0" }, "resources.disk"},
		{"cpu", func(a *api.App) { a.Resources.CPU = "two" }, "resources.cpu: \"two\" is not a number of CPUs"},
		{"notes long", func(a *api.App) { a.Notes = long }, "notes: is longer than 2000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := good()
			c.mutate(&app)
			_, err := Parse("demo", marshal(t, app))
			if err == nil {
				t.Fatalf("accepted: %+v", app)
			}
			if !api.IsCode(err, api.CodeInvalid) {
				t.Fatalf("not an invalid error: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestParseRejectsBadDocuments(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"empty", "", "the definition is empty"},
		{"comment only", "# nothing\n", "the definition is empty"},
		{"unknown key", "version: 1\nname: demo\ncolour: red\n", "field colour not found"},
		{"unknown nested key", marshalWith(t, "image", map[string]any{"os": "freebsd", "release_min": "15.1", "arch": "amd64"}), "field arch not found"},
		{"wrong type", "version: one\nname: demo\n", "cannot unmarshal"},
		{"not a map", "- a\n- b\n", "cannot unmarshal"},
		{"two documents", string(marshal(t, good())) + "---\nversion: 1\n", "one YAML document"},
		{"syntax", "version: 1\nname: [\n", "did not find expected"},
		{"name mismatch", string(marshal(t, good())), "name \"demo\" does not match the file name other.yaml"},
		{"binary", "\x00\x01\x02", "control characters are not allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name := "demo"
			if c.name == "name mismatch" {
				name = "other"
			}
			_, err := Parse(name, []byte(c.doc))
			if err == nil || !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%v, want %q", err, c.want)
			}
		})
	}
}

// marshalWith marshals the good definition with one top-level key replaced.
func marshalWith(t *testing.T, key string, value any) string {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(marshal(t, good()), &m); err != nil {
		t.Fatal(err)
	}
	m[key] = value
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingDirectoryIsEmpty(t *testing.T) {
	es, err := Load(filepath.Join(t.TempDir(), "none"))
	if err != nil || len(es) != 0 {
		t.Fatalf("%v %v", es, err)
	}
}

func TestLoadDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "file", nil)
	if _, err := Load(filepath.Join(dir, "file")); err == nil {
		t.Fatal("a file as the catalog directory was accepted")
	}
}

func TestLoadMixedCatalog(t *testing.T) {
	dir := t.TempDir()
	b := good()
	b.Name = "bravo"
	write(t, dir, "bravo.yaml", marshal(t, b))
	a := good()
	a.Name = "alpha"
	a.Packages[0].Version = ""
	write(t, dir, "alpha.yaml", marshal(t, a))
	write(t, dir, "broken.yaml", []byte("version: 1\nname: broken\n"))
	write(t, dir, "syntax.yaml", []byte("{{{"))
	write(t, dir, "README.md", []byte("not a definition"))
	write(t, dir, ".hidden.yaml", marshal(t, good()))
	write(t, dir, "notes.yml", marshal(t, good()))
	d := good()
	d.Name = "bravo" // a second file claiming an existing name
	write(t, dir, "dup.yaml", marshal(t, d))
	write(t, dir, "huge.yaml", bytes.Repeat([]byte("#"), MaxFileBytes+1))
	if err := os.Mkdir(filepath.Join(dir, "dir.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "alpha.yaml"), filepath.Join(dir, "link.yaml")); err != nil {
			t.Fatal(err)
		}
	}
	es, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]Entry{}
	for _, e := range es {
		names = append(names, e.Name)
		byName[e.Name] = e
	}
	want := "alpha bravo broken dir dup huge syntax"
	if runtime.GOOS != "windows" {
		want = "alpha bravo broken dir dup huge link syntax"
	}
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("entries %q, want %q", got, want)
	}
	for _, n := range []string{"alpha", "bravo"} {
		e := byName[n]
		if e.Err != nil || e.App.Name != n || e.File != n+".yaml" || !strings.HasPrefix(e.Hash, "sha256:") || len(e.Hash) != 7+64 {
			t.Errorf("%s: %+v", n, e)
		}
	}
	if byName["alpha"].Hash == byName["bravo"].Hash {
		t.Error("two different files hash alike")
	}
	for n, frag := range map[string]string{
		"broken": "title: is required",
		"syntax": "did not find expected",
		"dup":    "name \"bravo\" does not match the file name dup.yaml",
		"huge":   "huge.yaml is larger than",
		"dir":    "not a regular file",
		"link":   "not a regular file (a symbolic link is refused)",
	} {
		if n == "link" && runtime.GOOS == "windows" {
			continue
		}
		e := byName[n]
		if e.Err == nil || !strings.Contains(e.Err.Error(), frag) {
			t.Errorf("%s: %v, want %q", n, e.Err, frag)
		}
	}
	// Summaries: the valid ones carry their fields, the invalid ones the reason.
	s := byName["bravo"].Summary()
	if !s.Valid || s.Title != "Demo" || s.Category != api.AppWeb || s.Version != "1.2.3_1" || s.Error != "" {
		t.Errorf("%+v", s)
	}
	if s := byName["alpha"].Summary(); s.Version != "" {
		t.Errorf("%+v", s)
	}
	s = byName["dup"].Summary()
	if s.Valid || s.Error == "" || s.Title != "" {
		t.Errorf("a definition whose name does not match its file must not show the other app's fields: %+v", s)
	}
	s = byName["broken"].Summary()
	if s.Valid || !strings.Contains(s.Error, "title") {
		t.Errorf("%+v", s)
	}
	// Find.
	if e, ok, err := Find(dir, "alpha"); err != nil || !ok || e.Name != "alpha" {
		t.Fatalf("%+v %v %v", e, ok, err)
	}
	if _, ok, _ := Find(dir, "none"); ok {
		t.Fatal("found an app that does not exist")
	}
	if _, ok, _ := Find(dir, "../alpha"); ok {
		t.Fatal("a hostile name found something")
	}
	if e, ok, _ := Find(dir, "dup"); !ok || e.Err == nil {
		t.Fatal("an invalid entry is found with its error")
	}
}

func TestLoadIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zulu", "alpha", "mike"} {
		a := good()
		a.Name = n
		write(t, dir, n+".yaml", marshal(t, a))
	}
	first, _ := Load(dir)
	for range 3 {
		again, _ := Load(dir)
		if len(again) != 3 || again[0].Name != "alpha" || again[2].Name != "zulu" {
			t.Fatalf("%+v", again)
		}
		for i := range first {
			if first[i].Hash != again[i].Hash {
				t.Fatal("hash changed between loads")
			}
		}
	}
}

func TestIcons(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	svg := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")
	cases := []struct {
		name  string
		icon  string
		file  []byte
		setup func(dir string)
		want  string // error fragment; empty for success
		uri   string
	}{
		{"png", "demo.png", png, nil, "", "data:image/png;base64,"},
		{"svg", "demo.svg", svg, nil, "", "data:image/svg+xml;base64,"},
		{"svg with bom", "demo.svg", append([]byte("\xef\xbb\xbf\n"), svg...), nil, "", "data:image/svg+xml;base64,"},
		{"missing", "demo.png", nil, nil, "icon: ", ""},
		{"not png", "demo.png", svg, nil, "is not a PNG image", ""},
		{"not svg", "demo.svg", png, nil, "is not an SVG document", ""},
		{"too large", "demo.png", append(png, bytes.Repeat([]byte{0}, MaxIconBytes)...), nil, "larger than", ""},
		{"symlink", "demo.png", nil, func(dir string) {
			if runtime.GOOS == "windows" {
				return
			}
			write(t, dir, "real.png", png)
			_ = os.Symlink(filepath.Join(dir, "real.png"), filepath.Join(dir, "demo.png"))
		}, "is not a regular file", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "symlink" && runtime.GOOS == "windows" {
				t.Skip("no symlinks")
			}
			dir := t.TempDir()
			a := good()
			a.Icon = c.icon
			write(t, dir, "demo.yaml", marshal(t, a))
			if c.file != nil {
				write(t, dir, c.icon, c.file)
			}
			if c.setup != nil {
				c.setup(dir)
			}
			// the list reads no icon, so a bad one does not show there
			es, err := Load(dir)
			if err != nil || len(es) != 1 || es[0].Err != nil || es[0].Icon != "" {
				t.Fatalf("%v %v", es, err)
			}
			e, ok, err := Find(dir, "demo")
			if err != nil || !ok {
				t.Fatalf("%v %v", ok, err)
			}
			if c.want == "" {
				if e.Err != nil || !strings.HasPrefix(e.Icon, c.uri) {
					t.Fatalf("%v %q", e.Err, e.Icon[:min(len(e.Icon), 40)])
				}
				if d := e.Detail(); d.Icon != e.Icon || d.Hash != e.Hash || d.File != "demo.yaml" || d.Definition.Name != "demo" {
					t.Fatalf("%+v", d)
				}
				return
			}
			if e.Err == nil || !strings.Contains(e.Err.Error(), c.want) || e.Icon != "" {
				t.Fatalf("%v, want %q", e.Err, c.want)
			}
		})
	}
}

// The starter catalog the jailops-apps package installs is valid as shipped.
func TestStarterCatalog(t *testing.T) {
	es, err := Load(filepath.Join("..", "..", "packaging", "apps"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"forgejo", "nginx", "postgresql", "prometheus", "valkey"}
	if len(es) != len(want) {
		t.Fatalf("%d entries, want %v", len(es), want)
	}
	for i, e := range es {
		if e.Name != want[i] {
			t.Errorf("entry %d is %s, want %s", i, e.Name, want[i])
		}
		if e.Err != nil {
			t.Errorf("%s: %v", e.File, e.Err)
			continue
		}
		a := e.App
		if a.Image.ReleaseMin != "15.1" || len(a.Packages) == 0 || len(a.Services) == 0 || len(a.Ports) == 0 || len(a.Data) == 0 ||
			len(a.Config) == 0 || len(a.Logs) == 0 || a.Health == nil || a.Homepage == "" || a.License == "" || a.Notes == "" ||
			a.Resources.Memory == "" || a.Packages[0].Version == "" {
			t.Errorf("%s: a starter definition fills every section: %+v", e.File, a)
		}
	}
}

// The persistent paths a data or config path may not be are the persistent
// datasets of the default layout.
func TestPersistentPathsMatchTheLayout(t *testing.T) {
	var want []string
	for _, p := range lifecycle.DefaultPersistent() {
		want = append(want, p.Path)
	}
	if !slices.Equal(PersistentPaths, want) {
		t.Fatalf("PersistentPaths %v, the default layout %v", PersistentPaths, want)
	}
	// a host that keeps another path persistent: the installer checks its list with the same function
	a := good()
	a.Data[0].Path = "/var/db/demo"
	if err := CheckPersistent(a, []string{"/var/db/demo"}); err == nil || !strings.Contains(err.Error(), "data[0].path: /var/db/demo is a persistent dataset") {
		t.Fatal(err)
	}
}

// A catalog with more definitions than a listing reads: the first left out
// is listed once as invalid, the others are not read.
func TestLoadCapsDefinitions(t *testing.T) {
	dir := t.TempDir()
	for i := range MaxDefinitions + 5 {
		write(t, dir, fmt.Sprintf("a%04d.yaml", i), []byte("not read"))
	}
	es, err := Load(dir)
	if err != nil || len(es) != MaxDefinitions+1 {
		t.Fatalf("%v %d", err, len(es))
	}
	last := es[MaxDefinitions]
	if last.Name != fmt.Sprintf("a%04d", MaxDefinitions) || last.Err == nil || !strings.Contains(last.Err.Error(), "catalog has too many definitions") {
		t.Fatalf("%+v", last)
	}
	if strings.Contains(es[0].Err.Error(), "too many") {
		t.Fatalf("%v", es[0].Err)
	}
	// one definition beyond the cap is still found by name
	if e, ok, err := Find(dir, fmt.Sprintf("a%04d", MaxDefinitions+3)); err != nil || !ok || strings.Contains(e.Err.Error(), "too many") {
		t.Fatalf("%v %v %v", ok, err, e.Err)
	}
}
