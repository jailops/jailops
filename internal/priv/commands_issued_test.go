package priv

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// issuedCommand is one command a package issues through the daemon's runner (the helper's CommandRules): the name,
// the arguments when every one of them is a string literal, and where it is written.
type issuedCommand struct {
	name  string
	argv  []string // nil when an argument is not a literal
	where string
}

// TestEveryCommandTheDaemonIssuesIsOnTheAllowList reads the sources of the packages that run commands through the
// helper's runner and requires a rule for each command name, and, when the arguments are literals, that the rule accepts
// exactly that vector. A command the helper refuses does not fail loudly where it is used: a check that reads it reports
// "not available" for ever. The test turns that into a failure in CI.
func TestEveryCommandTheDaemonIssuesIsOnTheAllowList(t *testing.T) {
	cfg := newEnv(t).cfg
	var found []issuedCommand
	for _, dir := range []string{"../service", "../reconcile"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			ast.Inspect(af, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					// runOK(ctx, "name", "arg", ...) and s.runOK(ctx, "name", ...)
					if !calls(x.Fun, "runOK") || len(x.Args) < 2 {
						return true
					}
					name, ok := lit(x.Args[1])
					if !ok {
						return true
					}
					ic := issuedCommand{name: name, where: fset.Position(x.Pos()).String()}
					argv, all := []string{name}, true
					for _, a := range x.Args[2:] {
						v, ok := lit(a)
						if !ok {
							all = false
							break
						}
						argv = append(argv, v)
					}
					if all && !x.Ellipsis.IsValid() {
						ic.argv = argv
					}
					found = append(found, ic)
				case *ast.CompositeLit:
					// run.Cmd{Name: "name", Args: []string{"arg", ...}}
					sel, ok := x.Type.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Cmd" {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "run" {
						return true
					}
					var ic issuedCommand
					for _, e := range x.Elts {
						kv, ok := e.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, _ := kv.Key.(*ast.Ident)
						if key == nil {
							continue
						}
						switch key.Name {
						case "Name":
							ic.name, _ = lit(kv.Value)
							ic.where = fset.Position(x.Pos()).String()
						case "Args":
							if cl, ok := kv.Value.(*ast.CompositeLit); ok {
								argv, all := []string{""}, true
								for _, a := range cl.Elts {
									v, ok := lit(a)
									if !ok {
										all = false
										break
									}
									argv = append(argv, v)
								}
								if all {
									ic.argv = argv
								}
							}
						}
					}
					if ic.name == "" {
						return true
					}
					if ic.argv != nil {
						ic.argv[0] = ic.name
					}
					found = append(found, ic)
				}
				return true
			})
		}
	}
	if len(found) < 5 {
		t.Fatalf("the scan found only %d commands: it no longer reads the sources", len(found))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].where < found[j].where })
	ctx := WithAdmin(context.Background(), "jailopsd")
	for _, ic := range found {
		if ruleFor(ic.name) == nil {
			t.Errorf("%s: the command %q is not on the helper's allow-list (CommandRules): the daemon's call is refused", ic.where, ic.name)
			continue
		}
		if ic.argv == nil {
			continue
		}
		if _, _, err := cfg.CheckCommandCtx(ctx, ic.argv); err != nil {
			t.Errorf("%s: %q is refused by the allow-list: %v", ic.where, ic.argv, err)
		}
	}
}

// calls reports whether the call target is the function or method called name.
func calls(fun ast.Expr, name string) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == name
	case *ast.SelectorExpr:
		return f.Sel.Name == name
	}
	return false
}

// lit returns the value of a string literal expression.
func lit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}
