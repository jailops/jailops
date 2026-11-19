package server_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The Go API of the client library only grows. The previous-client baseline (testdata/prevsdk) is the client of an
// earlier release; every exported name of it (function, method, type, struct field, interface method, constant with its
// value, variable) has to be in sdk/ today with the same signature. A new name is fine. A name that goes or changes is a
// break of every program that imports the library, and fails here until the baseline is made again on purpose
// (`just freeze sdk`, which is for a release that may break the library).
func TestSDKGoAPIOnlyGrows(t *testing.T) {
	for _, pkg := range []struct{ old, cur string }{
		{"testdata/prevsdk", "../../sdk"},
		{"testdata/prevsdk/api", "../../sdk/api"},
	} {
		old, cur := exportedAPI(t, pkg.old), exportedAPI(t, pkg.cur)
		if len(old) == 0 {
			t.Fatalf("no exported names in %s", pkg.old)
		}
		var gone []string
		for l := range old {
			if !cur[l] {
				gone = append(gone, l)
			}
		}
		sort.Strings(gone)
		if len(gone) > 0 {
			t.Errorf("%s lost or changed %d exported names of the baseline:\n  %s", pkg.cur, len(gone), strings.Join(gone, "\n  "))
		}
	}
}

// A check that the check sees: a changed signature is a different line, a removed field is a missing one.
func TestSDKGoAPIDumpSeesChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(src string) map[string]bool {
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return exportedAPI(t, dir)
	}
	a := write("package p\ntype T struct{ X int; y int }\nfunc (t *T) M(a string) error { return nil }\nconst C = \"v\"\n")
	b := write("package p\ntype T struct{ X string }\nfunc (t *T) M(a int) error { return nil }\nconst C = \"w\"\n")
	for _, want := range []string{"field T.X int", "method (*T) M(a string) error", `const C = "v"`} {
		if !a[want] || b[want] {
			t.Errorf("%q: in the first dump %v, in the changed one %v", want, a[want], b[want])
		}
	}
	if a["field T.y int"] {
		t.Error("an unexported field is part of the API")
	}
}

func exportedAPI(t *testing.T, dir string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	str := func(n ast.Node) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, token.NewFileSet(), n); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	out := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					sig := strings.TrimPrefix(str(d.Type), "func")
					if d.Recv == nil {
						out["func "+d.Name.Name+sig] = true
						continue
					}
					recv := str(d.Recv.List[0].Type)
					if !ast.IsExported(strings.TrimPrefix(recv, "*")) {
						continue
					}
					out[fmt.Sprintf("method (%s) %s%s", recv, d.Name.Name, sig)] = true
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							if !s.Name.IsExported() {
								continue
							}
							switch ty := s.Type.(type) {
							case *ast.StructType:
								out["type "+s.Name.Name+" struct"] = true
								for _, fl := range ty.Fields.List {
									for _, n := range fl.Names {
										if n.IsExported() {
											tag := ""
											if fl.Tag != nil {
												tag = " " + fl.Tag.Value
											}
											out[fmt.Sprintf("field %s.%s %s%s", s.Name.Name, n.Name, str(fl.Type), tag)] = true
										}
									}
									if len(fl.Names) == 0 {
										out[fmt.Sprintf("embedded %s.%s", s.Name.Name, str(fl.Type))] = true
									}
								}
							case *ast.InterfaceType:
								out["type "+s.Name.Name+" interface"] = true
								for _, m := range ty.Methods.List {
									for _, n := range m.Names {
										out[fmt.Sprintf("imethod %s.%s%s", s.Name.Name, n.Name, strings.TrimPrefix(str(m.Type), "func"))] = true
									}
									if len(m.Names) == 0 {
										out[fmt.Sprintf("iembedded %s.%s", s.Name.Name, str(m.Type))] = true
									}
								}
							default:
								out["type "+s.Name.Name+" "+str(s.Type)] = true
							}
						case *ast.ValueSpec:
							kind := "var"
							if d.Tok == token.CONST {
								kind = "const"
							}
							for i, n := range s.Names {
								if !n.IsExported() {
									continue
								}
								line := kind + " " + n.Name
								if s.Type != nil {
									line += " " + str(s.Type)
								}
								if kind == "const" && i < len(s.Values) {
									line += " = " + str(s.Values[i])
								}
								out[line] = true
							}
						}
					}
				}
			}
		}
	}
	return out
}
