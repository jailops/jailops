package recipe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// vector is one case of testdata/render_vectors.json. The console renders in the browser with the same file: a change of
// the rules changes the file and both tests.
type vector struct {
	Name      string            `json:"name"`
	Recipe    string            `json:"recipe"`
	Files     map[string]string `json:"files"`
	Set       map[string]string `json:"set"`
	SetFile   map[string]string `json:"set_file"`
	Vars      map[string]string `json:"vars"`
	Want      json.RawMessage   `json:"want"`
	Sensitive bool              `json:"sensitive"`
	Error     string            `json:"error"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	b, err := os.ReadFile("testdata/render_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestRenderVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			fsys := fstest.MapFS{"dir/recipe.yaml": {Data: []byte(v.Recipe)}}
			for k, c := range v.Files {
				fsys["dir/"+k] = &fstest.MapFile{Data: []byte(c)}
			}
			l, err := Load(fsys, "dir", Values{Set: v.Set, SetFile: v.SetFile, Vars: v.Vars})
			if v.Error != "" {
				if err == nil || !strings.Contains(err.Error(), v.Error) {
					t.Fatalf("want an error with %q, got %v", v.Error, err)
				}
				// an error names the step and the field, never a value
				for _, m := range []map[string]string{v.Set, v.SetFile, v.Vars} {
					for k, val := range m {
						if len(val) >= 8 && strings.Contains(err.Error(), val) {
							t.Fatalf("the error holds the value of %s: %v", k, err)
						}
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(l.Recipe)
			var g, w any
			if err := json.Unmarshal(got, &g); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(v.Want, &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Fatalf("rendered\n%s\nwant\n%s", got, v.Want)
			}
			if l.Sensitive != v.Sensitive {
				t.Fatalf("sensitive %v, want %v", l.Sensitive, v.Sensitive)
			}
		})
	}
}

func TestDeclarationsAreListedInOrder(t *testing.T) {
	src := `
vars:
  site: {required: true}
  port: {default: 22}
  pw: {secret: true}
  pkgs: {default: "a b", split: true}
steps:
  - run: [echo, "${site}", "${port}", "${pkgs}"]
  - file: {path: /usr/local/etc/a, template: true, content: "${pw}"}
`
	l, err := LoadBytes([]byte(src), Values{Set: map[string]string{"site": "x"}, SetFile: map[string]string{"pw": "p"}})
	if err != nil {
		t.Fatal(err)
	}
	port, pkgs := "22", "a b"
	want := []VarDecl{{Name: "site", Required: true}, {Name: "port", Default: &port}, {Name: "pw", Secret: true}, {Name: "pkgs", Default: &pkgs, Split: true}}
	if !reflect.DeepEqual(l.Decls, want) || !l.Sensitive {
		t.Fatalf("%+v %v", l.Decls, l.Sensitive)
	}
	if r := l.Request(true); !r.DryRun || !r.Sensitive {
		t.Fatalf("%+v", r)
	}
}

func TestParseVarsFile(t *testing.T) {
	got, err := ParseVarsFile([]byte("# a comment\n\nSITE=example.test   # trailing\nTIME=\"17 2 * * *\"  # quoted\nexport A='x y'\nEMPTY=\nPLAIN=a#b\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"SITE": "example.test", "TIME": "17 2 * * *", "A": "x y", "EMPTY": "", "PLAIN": "a#b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	for _, bad := range []string{"no equals", "9X=1", "A=\"open", "A=\"x\" junk"} {
		if _, err := ParseVarsFile([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestReadValueFileDropsOneLineBreak(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v")
	if err := os.WriteFile(p, []byte("secret\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := ReadValueFile(p); err != nil || v != "secret\n" {
		t.Fatalf("%q %v", v, err)
	}
}

// A source is read below the directory of the recipe: a link that stays inside is fine, one that leads out (to a file or
// through a directory) is refused and the error names the file.
func TestSourceStaysInsideTheDirectory(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "real.conf"), []byte("real\n"), 0o644))
	must(os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	must(os.Symlink("real.conf", filepath.Join(dir, "inside.conf")))
	must(os.Symlink("../real.conf", filepath.Join(dir, "sub", "up.conf")))
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "file.conf")))
	must(os.Symlink(outside, filepath.Join(dir, "linkdir")))
	recipe := func(src string) string {
		return "steps:\n  - file:\n      path: /usr/local/etc/a.conf\n      source: " + src + "\n"
	}
	for src, want := range map[string]string{"real.conf": "real\n", "inside.conf": "real\n", "sub/up.conf": "real\n"} {
		must(os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe(src)), 0o644))
		for _, p := range []string{dir, filepath.Join(dir, "recipe.yaml")} { // the directory and the file in it
			l, err := LoadPath(p, Values{})
			if err != nil || *l.Recipe.Steps[0].File.Content != want {
				t.Errorf("%s %s: %v", src, p, err)
			}
		}
	}
	for _, src := range []string{"file.conf", "linkdir/secret"} {
		must(os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe(src)), 0o644))
		_, err := LoadPath(dir, Values{})
		if err == nil || !strings.Contains(err.Error(), src) || strings.Contains(err.Error(), "secret\n") {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestStandardInputHasNoFilesBesideIt(t *testing.T) {
	_, err := LoadBytes([]byte("steps:\n  - file: {path: /usr/local/etc/a, source: a.conf}\n"), Values{})
	if err == nil || !strings.Contains(err.Error(), "use content") {
		t.Fatal(err)
	}
}

// The rendered recipe goes to the validator of the daemon, and what it says carries no value.
func TestValidateNamesTheStepAndNoValue(t *testing.T) {
	l, err := LoadBytes([]byte("vars:\n  p: {}\nsteps:\n  - package: [\"${p}\"]\n"), Values{Set: map[string]string{"p": "not a package; rm -rf"}})
	if err != nil {
		t.Fatal(err)
	}
	err = l.Validate()
	if err == nil || !strings.Contains(err.Error(), "steps[0].package") || strings.Contains(err.Error(), "rm -rf") {
		t.Fatal(err)
	}
}

func TestASourceThatIsNotText(t *testing.T) {
	fsys := fstest.MapFS{"recipe.yaml": {Data: []byte("steps:\n  - file: {path: /usr/local/etc/a, source: a.bin}\n")}, "a.bin": {Data: []byte{0xff, 0xfe, 0}}}
	if _, err := Load(fsys, ".", Values{}); err == nil || !strings.Contains(err.Error(), "a.bin is not text") {
		t.Fatal(err)
	}
}
