package server_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The vocabulary of the wire is frozen like its routes: the members of the enums, the conditions and their reasons, the
// actions of operations and events, and whether a route answers at once or with an operation. A new member is added
// with `just freeze` and shows in the diff of the golden file; a member that goes or changes is a breaking change.

// parseDir parses the Go files of a directory that are not tests.
func parseDir(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fs := token.NewFileSet()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no Go files in %s: %v", dir, err)
	}
	var files []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fs, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return fs, files
}

func exprText(fs *token.FileSet, e ast.Expr) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, fs, e)
	return b.String()
}

func strLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

// The members of the enums: the constants of every named string type of sdk/api, and the fixed sets of the plain
// string fields (api.Enums).
func TestFreezeEnums(t *testing.T) {
	_, files := parseDir(t, "../../sdk/api")
	var lines []string
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, sp := range g.Specs {
				vs := sp.(*ast.ValueSpec)
				typ, ok := vs.Type.(*ast.Ident)
				if !ok || len(vs.Values) != 1 || typ.Name == "Code" || typ.Name == "OperationAction" {
					continue
				}
				if v, ok := strLit(vs.Values[0]); ok {
					lines = append(lines, typ.Name+" "+v)
				}
			}
		}
	}
	for k, v := range api.Enums {
		lines = append(lines, k+": "+strings.Join(v, " "))
	}
	sort.Strings(lines)
	freezeCheck(t, "enums", strings.Join(lines, "\n")+"\n")
}

// The conditions of every kind of object with their reasons; a condition type or reason in the code that the list does
// not have fails the test.
func TestFreezeConditions(t *testing.T) {
	var lines []string
	types, reasons := map[string]bool{}, map[string]bool{}
	for _, c := range api.Conditions {
		rs := append([]string(nil), c.Reasons...)
		sort.Strings(rs)
		lines = append(lines, fmt.Sprintf("%s %s: %s", c.Kind, c.Type, strings.Join(rs, " ")))
		types[c.Type] = true
		for _, r := range c.Reasons {
			reasons[r] = true
		}
	}
	sort.Strings(lines)
	freezeCheck(t, "conditions", strings.Join(lines, "\n")+"\n")

	// the constants that name a condition type or reason
	consts := map[string]string{"ConditionFirewallApplied": api.ConditionFirewallApplied, "ShellInstalling": api.ShellInstalling}
	for _, dir := range []string{"../reconcile", "../service", "."} {
		_, files := parseDir(t, dir)
		for _, f := range files {
			for _, d := range f.Decls {
				if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
					for _, sp := range g.Specs {
						vs := sp.(*ast.ValueSpec)
						if len(vs.Names) == 1 && len(vs.Values) == 1 {
							if v, ok := strLit(vs.Values[0]); ok {
								consts[vs.Names[0].Name] = v
							}
						}
					}
				}
			}
		}
	}
	resolve := func(e ast.Expr) (string, bool) {
		if s, ok := strLit(e); ok {
			return s, true
		}
		switch x := e.(type) {
		case *ast.Ident:
			v, ok := consts[x.Name]
			return v, ok
		case *ast.SelectorExpr:
			v, ok := consts[x.Sel.Name]
			return v, ok
		}
		return "", false
	}
	for _, dir := range []string{"../reconcile", "../service", "."} {
		fs, files := parseDir(t, dir)
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					id, ok := x.Fun.(*ast.Ident)
					if !ok {
						return true
					}
					switch {
					case (id.Name == "cond" || id.Name == "setCond") && len(x.Args) >= 3:
						// cond(type, ok, reason, ...) and setCond(network, ok, reason, ...)
						if id.Name == "cond" {
							if v, ok := resolve(x.Args[0]); ok && !types[v] {
								t.Errorf("%s: condition type %q is not in api.Conditions", fs.Position(x.Pos()), v)
							}
						}
						if v, ok := strLit(x.Args[2]); ok && !reasons[v] {
							t.Errorf("%s: condition reason %q is not in api.Conditions", fs.Position(x.Pos()), v)
						}
					case (id.Name == "fail" || id.Name == "unknown") && len(x.Args) == 2 && condHelperFile(fs.Position(x.Pos()).Filename):
						if v, ok := strLit(x.Args[0]); ok && !reasons[v] {
							t.Errorf("%s: condition reason %q is not in api.Conditions", fs.Position(x.Pos()), v)
						}
					}
				case *ast.CompositeLit:
					if !strings.HasSuffix(exprText(fs, x.Type), "Condition") && x.Type != nil {
						return true
					}
					for _, e := range x.Elts {
						kv, ok := e.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, _ := kv.Key.(*ast.Ident)
						if k == nil {
							continue
						}
						if v, ok := resolve(kv.Value); ok {
							if k.Name == "Type" && !types[v] && !strings.HasPrefix(v, "Host:") {
								t.Errorf("%s: condition type %q is not in api.Conditions", fs.Position(kv.Pos()), v)
							}
							if k.Name == "Reason" && !reasons[v] {
								t.Errorf("%s: condition reason %q is not in api.Conditions", fs.Position(kv.Pos()), v)
							}
						}
					}
				}
				return true
			})
		}
	}
}

// condHelperFile says whether the file has a local fail or unknown helper that makes a condition of its reason.
func condHelperFile(name string) bool {
	return strings.HasSuffix(name, "reconcile/reconciler.go") || strings.HasSuffix(name, "service/storage.go")
}

// The actions of operations; every operation the service starts names one.
func TestFreezeOperationActions(t *testing.T) {
	var lines []string
	listed := map[string]bool{}
	for _, a := range api.OperationActions {
		lines = append(lines, string(a))
		listed[string(a)] = true
	}
	sort.Strings(lines)
	freezeCheck(t, "operation_actions", strings.Join(lines, "\n")+"\n")

	_, files := parseDir(t, "../../sdk/api")
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, sp := range g.Specs {
				vs := sp.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "OperationAction" && len(vs.Values) == 1 {
					if v, _ := strLit(vs.Values[0]); !listed[v] {
						t.Errorf("OperationAction %q is not in api.OperationActions", v)
					}
				}
			}
		}
	}

	fs, svc := parseDir(t, "../service")
	for _, f := range svc {
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || exprText(fs, cl.Type) != "OpSpec" {
				return true
			}
			for _, e := range cl.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Action" {
						return true
					}
				}
			}
			t.Errorf("%s: an operation without an Action", fs.Position(cl.Pos()))
			return true
		})
	}
}

// The actions of lifecycle events: the names the service gives to Emit. A name that goes is a breaking change.
func TestFreezeEventActions(t *testing.T) {
	_, files := parseDir(t, "../service")
	seen := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "emit" && sel.Sel.Name != "emitIn") {
				return true
			}
			// emit(ctx, action, source) and emitIn(ctx, project, action, source)
			i := 1
			if sel.Sel.Name == "emitIn" {
				i = 2
			}
			if len(c.Args) > i {
				if v, ok := strLit(c.Args[i]); ok {
					seen[v] = true
				}
			}
			return true
		})
	}
	var lines []string
	for a := range seen {
		lines = append(lines, a)
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		t.Fatal("no event actions found")
	}
	freezeCheck(t, "event_actions", strings.Join(lines, "\n")+"\n")
}

// Every route by how it answers: sync (the answer is the result), async (202 and an operation) or either (an operation,
// or the plan at once for ?dry_run=true), and stream for the routes whose body or answer is not a JSON envelope.
func TestFreezeRouteReplies(t *testing.T) {
	fs, files := parseDir(t, "../server")
	got := map[string]string{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var method, pattern string
			var h ast.Expr
			for _, e := range cl.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				k, _ := kv.Key.(*ast.Ident)
				if k == nil {
					continue
				}
				switch k.Name {
				case "method":
					method, _ = strLit(kv.Value)
				case "pattern":
					if p, ok := strLit(kv.Value); ok {
						pattern = p
					} else if exprText(fs, kv.Value) == `base + "/{name}/rename"` {
						pattern = "*/rename"
					}
				case "h":
					h = kv.Value
				}
			}
			if method == "" || pattern == "" || h == nil {
				return true
			}
			async, sync := false, false
			if _, isFunc := h.(*ast.FuncLit); isFunc {
				ast.Inspect(h, func(m ast.Node) bool {
					if c, ok := m.(*ast.CallExpr); ok {
						if s, ok := c.Fun.(*ast.SelectorExpr); ok {
							async = async || s.Sel.Name == "async"
							sync = sync || s.Sel.Name == "sync"
						}
					}
					return true
				})
			}
			kind := "sync"
			switch {
			case async && sync:
				kind = "either"
			case async:
				kind = "async"
			}
			if _, isFunc := h.(*ast.FuncLit); !isFunc {
				kind = map[string]string{"s.handleImport": "either", "s.handleAudit": "sync"}[exprText(fs, h)]
				if kind == "" {
					kind = "stream"
				}
			}
			got[method+" "+pattern] = kind
			return true
		})
	}
	e := dtest.New(t, dtest.Options{})
	var lines []string
	for _, k := range e.D.Server().AllRouteKeys() {
		kind, ok := got[k]
		if !ok {
			if i := strings.Index(k, "/{name}/rename"); i > 0 && strings.HasPrefix(k, "POST ") {
				kind, ok = got["POST */rename"], true
			}
		}
		if !ok {
			kind = "sync" // a plain collection or item route, answered at once
		}
		lines = append(lines, k+" "+kind)
	}
	sort.Strings(lines)
	freezeCheck(t, "route_replies", strings.Join(lines, "\n")+"\n")
}
