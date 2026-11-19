package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// kindConstants returns the names of the Kind constants declared in this package.
func kindConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, sp := range gd.Specs {
					vs := sp.(*ast.ValueSpec)
					id, ok := vs.Type.(*ast.Ident)
					if !ok || id.Name != "Kind" {
						continue
					}
					for _, n := range vs.Names {
						if strings.HasPrefix(n.Name, "Kind") {
							out = append(out, n.Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// Every Kind has an owner: a new kind without an entry of Authorities fails here, and so does an entry of a kind that
// does not exist.
func TestEveryKindHasAnAuthority(t *testing.T) {
	declared := kindConstants(t)
	if len(declared) < 20 {
		t.Fatalf("found only %d Kind constants: the scan is broken: %v", len(declared), declared)
	}
	have := map[string]bool{}
	for k := range Authorities {
		have[string(k)] = true
	}
	// the constants name the kinds: compare by the value through the table of values
	values := map[string]Kind{}
	for _, k := range []Kind{KindJail, KindImage, KindNetwork, KindFirewallPolicy, KindAddressSet, KindBackupPolicy, KindBackupTarget,
		KindHostGroup, KindRollout, KindJailTemplate, KindRemote, KindUser, KindToken, KindCertificate, KindHost, KindImageGeneration,
		KindJailSnapshot, KindBackup, KindOperation, KindProject, KindStorage, KindVolume, KindWireGuard, KindHostIdentity,
		KindLogForward, KindHostFirewall, KindHostCertificate, KindAppAccess, KindProfile} {
		values["Kind"+string(k)] = k
	}
	for _, name := range declared {
		k, ok := values[name]
		if !ok {
			t.Errorf("%s is a Kind constant that the test does not know: add it to the list of this test and to Authorities", name)
			continue
		}
		if _, ok := Authorities[k]; !ok {
			t.Errorf("kind %s has no entry in Authorities: say who owns it and where it lives", k)
		}
	}
	if len(Authorities) != len(declared) {
		t.Errorf("Authorities has %d entries, the package declares %d kinds", len(Authorities), len(declared))
	}
	for k, a := range Authorities {
		if a.Owner == "" || a.Writer == "" || a.Lives == "" {
			t.Errorf("kind %s has an incomplete entry: %+v", k, a)
		}
		if a.Scope != "" && a.Scope != ScopeHost && a.Scope != ScopeFleet {
			t.Errorf("kind %s has the scope %q", k, a.Scope)
		}
	}
}

// docs/design.md holds the table that AuthorityTable renders, between two markers, so that the document cannot drift
// from the code.
func TestDesignHoldsTheAuthorityTable(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "design.md"))
	if err != nil {
		t.Skipf("docs/design.md is not next to the module: %v", err)
	}
	s := string(b)
	const begin, end = "<!-- authority:begin -->\n", "<!-- authority:end -->"
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatal("docs/design.md has no authority:begin and authority:end markers")
	}
	got := strings.TrimSpace(s[i+len(begin) : j])
	want := strings.TrimSpace(AuthorityTable())
	if got != want {
		t.Errorf("the table in docs/design.md differs from api.AuthorityTable(); replace the text between the markers with:\n\n%s", want)
	}
}
