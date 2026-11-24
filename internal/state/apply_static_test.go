package state

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Two rules keep the store ready for a log in front of it, and both are checked on the source:
//
//  1. what applying a command does is deterministic: the code reachable from Apply reads no clock, no random source and
//     no environment (the proposer supplies the id, the time and the uuid);
//  2. the files under <state_dir>/objects are written by state.Apply and by nothing else; other packages may read them
//     (the privileged helper does) and the readers are listed, so that a new reader or writer is a decision.

// forbidden is what the apply path must not call: package, function (an empty function stands for the whole package).
var forbidden = map[string]string{
	"time.Now":        "reads the clock",
	"time.Since":      "reads the clock",
	"time.Until":      "reads the clock",
	"os.Getenv":       "reads the environment",
	"os.LookupEnv":    "reads the environment",
	"os.Environ":      "reads the environment",
	"os.Getpid":       "reads the process id",
	"os.Getppid":      "reads the process id",
	"os.Hostname":     "reads the host name",
	"math/rand":       "random numbers",
	"math/rand/v2":    "random numbers",
	"crypto/rand":     "random numbers",
	"os.Getuid":       "reads the user id",
	"runtime.NumCPU":  "reads the machine",
	"runtime.Version": "reads the machine",
}

type funcDecl struct {
	file string
	decl *ast.FuncDecl
	imp  map[string]string // local import name -> import path
}

func importNames(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, is := range f.Imports {
		p, err := strconv.Unquote(is.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(p)
		if p == "math/rand/v2" {
			name = "rand"
		}
		if is.Name != nil {
			name = is.Name.Name
		}
		m[name] = p
	}
	return m
}

// scanForbidden returns the calls of forbidden functions in the functions reachable from roots by name (functions and
// methods of the package; a method call counts by its name, which can only over-approximate), or in every function when
// roots is empty. A finding reads "file:line: func uses pkg.fn (why)".
func scanForbidden(fset *token.FileSet, files map[string]*ast.File, roots []string) []string {
	byName := map[string][]funcDecl{}
	for name, f := range files {
		imp := importNames(f)
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				byName[fd.Name.Name] = append(byName[fd.Name.Name], funcDecl{file: name, decl: fd, imp: imp})
			}
		}
	}
	var queue []string
	seen := map[string]bool{}
	if len(roots) == 0 {
		for n := range byName {
			queue = append(queue, n)
		}
	} else {
		queue = append(queue, roots...)
	}
	var out []string
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, fd := range byName[name] {
			ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					switch fun := x.Fun.(type) {
					case *ast.Ident:
						queue = append(queue, fun.Name)
					case *ast.SelectorExpr:
						if id, ok := fun.X.(*ast.Ident); !ok || fd.imp[id.Name] == "" {
							queue = append(queue, fun.Sel.Name) // a method of the package (or of a value): by name
						}
					}
				case *ast.SelectorExpr:
					id, ok := x.X.(*ast.Ident)
					if !ok {
						return true
					}
					path := fd.imp[id.Name]
					if path == "" {
						return true
					}
					why, bad := forbidden[path+"."+x.Sel.Name]
					if !bad {
						why, bad = forbidden[path]
					}
					if bad {
						pos := fset.Position(x.Pos())
						out = append(out, pos.String()+": "+fd.decl.Name.Name+" uses "+path+"."+x.Sel.Name+" ("+why+")")
					}
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func parseDir(t *testing.T, dir string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files[n] = f
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return fset, files
}

func TestApplyPathReadsNoClockEntropyOrEnvironment(t *testing.T) {
	fset, files := parseDir(t, ".")
	roots := []string{"Apply", "ApplyCommitted", "applyPut", "applyDelete"}
	if got := scanForbidden(fset, files, roots); len(got) > 0 {
		t.Fatalf("the code reachable from Apply is not deterministic: the proposer supplies the id, the time and the uuid of a "+
			"command and applying it must not read anything else:\n%s", strings.Join(got, "\n"))
	}
	// the roots exist: a renamed function must not make the scan empty
	have := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				have[fd.Name.Name] = true
			}
		}
	}
	for _, r := range roots {
		if !have[r] {
			t.Errorf("the scan starts at %s, which no longer exists: update the roots", r)
		}
	}
}

// packageAllowed are the places of the whole package that may read a clock or a random source, and why. Nothing of them
// is reachable from Apply (TestApplyPathReadsNoClockEntropyOrEnvironment).
var packageAllowed = map[string]string{
	"NewUUID": "the helper of proposers: the uuid of a new object is chosen before the command exists",
}

func TestPackageUsesNoClockEntropyOrEnvironmentOutsideTheAllowList(t *testing.T) {
	fset, files := parseDir(t, ".")
	var bad []string
	for _, line := range scanForbidden(fset, files, nil) {
		// "file:line:col: func uses ..."
		rest := line[strings.Index(line, ": ")+2:]
		fn := strings.Fields(rest)[0]
		if _, ok := packageAllowed[fn]; ok {
			continue
		}
		bad = append(bad, line)
	}
	if len(bad) > 0 {
		t.Fatalf("a function of internal/state reads a clock, a random source or the environment; the apply path must not, and "+
			"anything else is added to packageAllowed with its reason:\n%s", strings.Join(bad, "\n"))
	}
}

func parseSource(t *testing.T, src string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return fset, map[string]*ast.File{"x.go": f}
}

// The scanner finds what it is meant to find, through helpers and methods, under an alias, and only from the roots.
func TestScannerFindsForbiddenCalls(t *testing.T) {
	const src = `package x

import (
	"os"
	"time"
	mrand "math/rand"
)

type S struct{}

func Apply() { helper() }
func helper() { var s S; s.deep(); _ = time.Now() }
func (s S) deep() { _ = mrand.Intn(3) }
func unrelated() { _ = os.Getenv("HOME") }
func clean() { _ = time.Second }
`
	fset, files := parseSource(t, src)
	got := strings.Join(scanForbidden(fset, files, []string{"Apply"}), "\n")
	if !strings.Contains(got, "helper uses time.Now") || !strings.Contains(got, "deep uses math/rand.Intn") {
		t.Errorf("the reachable calls were not found:\n%s", got)
	}
	if strings.Contains(got, "unrelated") || strings.Contains(got, "clean") {
		t.Errorf("a function that Apply does not reach was reported:\n%s", got)
	}
	all := strings.Join(scanForbidden(fset, files, nil), "\n")
	if !strings.Contains(all, "unrelated uses os.Getenv") {
		t.Errorf("the whole-package scan missed os.Getenv:\n%s", all)
	}
	if strings.Contains(all, "clean uses") {
		t.Errorf("time.Second is a constant, not a clock read:\n%s", all)
	}
}

// ---- who writes under objects/

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the package")
		}
		dir = parent
	}
}

// writeCalls are the calls that change the file system: package os (and io/ioutil), by function.
var writeCalls = map[string]bool{
	"os.WriteFile": true, "os.Rename": true, "os.Remove": true, "os.RemoveAll": true, "os.Create": true, "os.CreateTemp": true,
	"os.OpenFile": true, "os.Mkdir": true, "os.MkdirAll": true, "os.Truncate": true, "os.Chmod": true, "os.Chown": true,
	"os.Chtimes": true, "os.Symlink": true, "os.Link": true, "ioutil.WriteFile": true,
}

// objectsReaders are the places outside internal/state that build the path of the objects directory: all of them read it.
// The privileged helper reads the stored networks and projects to learn what the daemon has decided (the files are the
// local projection of the state, a contract with the helper). A new place is added here after deciding that it only
// reads; changing what is stored is done with state.Apply, never with a write into the directory.
var objectsReaders = map[string]string{
	"internal/priv/hook.go loadNetwork":                  "the hook of a jail start reads the network of the jail",
	"internal/priv/netops.go storedNetworks":             "the helper lists the stored networks",
	"internal/priv/pfifaces.go networkUplinks":           "the uplinks of the stored networks, for the pf interface check",
	"internal/priv/dnsunbound.go addStoredNetworks":      "the names for the resolver: the networks and the projects that own them",
	"internal/priv/netops.go checkRejectRoute":           "a reject route is accepted only inside a stored network",
	"internal/priv/dnsunbound.go DropStaleResolverAddrs": "reads the stored networks to learn which resolver addresses are still wanted",
}

func isObjectsLit(e ast.Expr) bool {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(bl.Value)
	return err == nil && (s == "objects" || strings.HasPrefix(s, "objects/") || strings.HasSuffix(s, "/objects"))
}

func containsObjectsLit(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if e, ok := x.(ast.Expr); ok && isObjectsLit(e) {
			found = true
		}
		return !found
	})
	return found
}

func callName(imp map[string]string, call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	p := imp[id.Name]
	if p == "" {
		return ""
	}
	return filepath.Base(p) + "." + sel.Sel.Name
}

// objectsSites scans the Go files of the module outside internal/state for two things in every function: a path built with
// the literal "objects" (a Join of path elements), and a write call (os.WriteFile and the rest of writeCalls) that has
// the literal in its arguments or sits in a function that builds the path.
func objectsSites(t *testing.T, root string) (builders map[string]bool, writes []string) {
	t.Helper()
	builders = map[string]bool{}
	fset := token.NewFileSet()
	skip := map[string]bool{".git": true, "node_modules": true, "testdata": true, "vendor": true, "temp": true, "web": true, "bin": true}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if skip[d.Name()] || rel == filepath.Join("internal", "state") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		imp := importNames(f)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			builds := false
			var wr []string
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := callName(imp, call)
				if name == "filepath.Join" || name == "path.Join" {
					for _, a := range call.Args {
						if isObjectsLit(a) {
							builds = true
						}
					}
				}
				if writeCalls[name] {
					wr = append(wr, name+" at "+fset.Position(call.Pos()).String())
					for _, a := range call.Args {
						if containsObjectsLit(a) {
							writes = append(writes, rel+" "+fd.Name.Name+": "+name+" with the objects path")
						}
					}
				}
				return true
			})
			if builds {
				key := filepath.ToSlash(rel) + " " + fd.Name.Name
				builders[key] = true
				for _, w := range wr {
					writes = append(writes, key+": builds the path of the objects directory and calls "+w)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(writes)
	return builders, writes
}

// the scan of the module is the one slow part: both tests share it
var (
	repoScan     sync.Once
	repoBuilders map[string]bool
	repoWrites   []string
)

func scanRepoOnce(t *testing.T) (map[string]bool, []string) {
	t.Helper()
	repoScan.Do(func() { repoBuilders, repoWrites = objectsSites(t, moduleRoot(t)) })
	return repoBuilders, repoWrites
}

func TestNothingOutsideStateWritesTheObjectsDirectory(t *testing.T) {
	_, writes := scanRepoOnce(t)
	if len(writes) > 0 {
		t.Fatalf("a package other than internal/state writes into <state_dir>/objects: stored objects change only through "+
			"state.Apply (a Command with the proposer's id, time and uuid), so that a log in front of the store sees every "+
			"change:\n%s", strings.Join(writes, "\n"))
	}
}

func TestOnlyTheListedPlacesReadTheObjectsDirectory(t *testing.T) {
	builders, _ := scanRepoOnce(t)
	var unknown, gone []string
	for k := range builders {
		if _, ok := objectsReaders[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	for k := range objectsReaders {
		if !builders[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(unknown)
	sort.Strings(gone)
	if len(unknown) > 0 {
		t.Errorf("these functions build the path of <state_dir>/objects and are not listed as readers (objectsReaders): "+
			"reading is allowed after a decision, writing never (use state.Apply):\n%s", strings.Join(unknown, "\n"))
	}
	if len(gone) > 0 {
		t.Errorf("objectsReaders lists places that no longer build the path: remove them\n%s", strings.Join(gone, "\n"))
	}
}

// The scanner of writers finds a write with the objects path, and a write in a function that builds it.
func TestObjectsScannerFindsWriters(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "internal", "other")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package other

import (
	"os"
	"path/filepath"
)

func direct(dir string) { os.WriteFile(filepath.Join(dir, "objects", "Network", "x.json"), nil, 0o600) }

func viaVariable(dir string) {
	p := filepath.Join(dir, "objects", "Network")
	os.Remove(p)
}

func reader(dir string) { os.ReadDir(filepath.Join(dir, "objects")) }

func unrelated(dir string) { os.WriteFile(filepath.Join(dir, "other.json"), nil, 0o600) }
`
	if err := os.WriteFile(filepath.Join(pkg, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	builders, writes := objectsSites(t, dir)
	all := strings.Join(writes, "\n")
	if !strings.Contains(all, "direct: os.WriteFile with the objects path") {
		t.Errorf("a direct write was not found:\n%s", all)
	}
	if !strings.Contains(all, "viaVariable: builds the path of the objects directory and calls os.Remove") {
		t.Errorf("a write after building the path was not found:\n%s", all)
	}
	if strings.Contains(all, "unrelated") || strings.Contains(all, "reader:") {
		t.Errorf("a function that does not write the objects directory was reported:\n%s", all)
	}
	if !builders["internal/other/a.go reader"] {
		t.Errorf("the reader was not listed as a builder of the path: %v", builders)
	}
}
