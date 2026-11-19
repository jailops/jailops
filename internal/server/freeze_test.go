package server_test

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The freeze tests fail on a change of the API surface that nobody meant: a route that appears, goes or changes its
// method, an error code that changes its HTTP status, a field of the envelopes every answer has, an extension that is
// removed or renamed. They compare the surface with golden files in testdata/freeze. A change that is meant is made
// with `just freeze` (go test ./internal/server -run Freeze -freeze-update), which rewrites the files, and the diff of
// the golden files is what a reviewer reads. docs/api.md says what is frozen and what is not.
var freezeUpdate = flag.Bool("freeze-update", false, "rewrite the golden files of the API freeze")

func freezeCheck(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "freeze", name+".golden")
	if *freezeUpdate {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/server -run Freeze -freeze-update writes it)", err)
	}
	if string(want) != got {
		t.Errorf("the API surface %q changed (go test ./internal/server -run Freeze -freeze-update accepts it):\n%s", name, diffLines(string(want), got))
	}
}

// diffLines lists the lines that are in one text and not in the other.
func diffLines(want, got string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var out []string
	for _, l := range strings.Split(want, "\n") {
		if !g[l] {
			out = append(out, "- "+l)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if !w[l] {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}

// Every route of the API, by method and pattern.
func TestFreezeRoutes(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	keys := append([]string(nil), e.D.Server().RouteKeys()...)
	sort.Strings(keys)
	freezeCheck(t, "routes", strings.Join(keys, "\n")+"\n")
}

// Every error code with the HTTP status it answers with.
func TestFreezeErrorCodes(t *testing.T) {
	var codes []string
	for _, c := range codesOf(t) {
		codes = append(codes, fmt.Sprintf("%s %d", c, c.HTTPStatus()))
	}
	sort.Strings(codes)
	freezeCheck(t, "error_codes", strings.Join(codes, "\n")+"\n")
}

// The capability list only grows: a name is never removed or renamed, and a new one is appended.
func TestFreezeExtensionsOnlyGrow(t *testing.T) {
	path := filepath.Join("testdata", "freeze", "extensions.golden")
	got := strings.Join(api.Extensions, "\n") + "\n"
	if *freezeUpdate {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/server -run Freeze -freeze-update writes it)", err)
	}
	frozen := strings.Fields(string(b))
	if len(api.Extensions) < len(frozen) {
		t.Fatalf("extensions were removed: %d frozen, %d now", len(frozen), len(api.Extensions))
	}
	for i, n := range frozen {
		if api.Extensions[i] != n {
			t.Fatalf("extension %d was %q and is %q: names are never removed, renamed or moved", i, n, api.Extensions[i])
		}
	}
	if len(api.Extensions) > len(frozen) {
		t.Errorf("new extensions %v: go test ./internal/server -run Freeze -freeze-update records them", api.Extensions[len(frozen):])
	}
}

// The members of the envelopes every answer has, of the operation of an asynchronous request, of an event, an error
// and the metadata and conditions of every object: their JSON names and types.
func TestFreezeEnvelopeShapes(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var lines []string
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if !f.IsExported() || tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			opt := ""
			if strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero") {
				opt = " optional"
			}
			lines = append(lines, fmt.Sprintf("%s.%s %s%s", t.Name(), name, f.Type, opt))
			walk(f.Type)
		}
	}
	for _, v := range []any{api.Response{}, api.Operation{}, api.Event{}, api.Error{}, api.ObjectMeta{}, api.Condition{}} {
		walk(reflect.TypeOf(v))
	}
	sort.Strings(lines)
	freezeCheck(t, "envelopes", strings.Join(lines, "\n")+"\n")
}

// codesOf lists the error codes declared in sdk/api/errors.go.
func codesOf(t *testing.T) []api.Code {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../../sdk/api/errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []api.Code
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Code" {
				continue
			}
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					out = append(out, api.Code(s))
				}
			}
		}
	}
	return out
}

// wireCheck compares a transcript of the wire tests with its golden file (testdata/wire).
func wireCheck(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "wire", name+".golden")
	if *freezeUpdate {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/server -run Freeze -freeze-update writes it)", err)
	}
	if string(want) != got {
		t.Errorf("the wire transcript %q changed (just freeze accepts it):\n%s", name, diffLines(string(want), got))
	}
}

// Every extension is in exactly one of the two lists of docs/api.md ("Which extensions carry the 1.x promise"), and
// the lists name no extension the server does not have: a new extension is a decision about its promise.
func TestFreezeEveryExtensionHasAPromise(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(b), "### Which extensions carry the 1.x promise")
	if !ok {
		t.Fatal("docs/api.md has no section 'Which extensions carry the 1.x promise'")
	}
	section, _, _ := strings.Cut(after, "\n## ")
	count := map[string]int{}
	for _, row := range strings.Split(section, "\n") {
		if !strings.HasPrefix(row, "| 1.x |") && !strings.HasPrefix(row, "| Not yet |") {
			continue
		}
		for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(row, -1) {
			count[m[1]]++
		}
	}
	for _, name := range api.Extensions {
		if count[name] != 1 {
			t.Errorf("extension %q is in %d of the lists of docs/api.md (\"Which extensions carry the 1.x promise\"): it belongs in exactly one", name, count[name])
		}
		delete(count, name)
	}
	for name := range count {
		t.Errorf("docs/api.md names %q, which the server does not advertise", name)
	}
}
