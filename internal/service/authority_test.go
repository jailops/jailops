package service

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The scope that the service registers for a kind is the scope of api.Authorities, which docs/design.md prints as the
// table "Resource authority": a kind registered with another scope, or with none, fails here.
func TestRegisteredScopesAreTheDocumentedAuthority(t *testing.T) {
	re := regexp.MustCompile(`new(?:Project)?KindStore\[[^\]]*\]\(s, api\.(Kind\w+), api\.(Scope\w+),`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]api.Kind{}
	for k := range api.Authorities {
		byName["Kind"+string(k)] = k
	}
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			seen++
			k, ok := byName[m[1]]
			if !ok {
				t.Errorf("%s registers %s, which api.Authorities does not list", f, m[1])
				continue
			}
			want := api.Authorities[k].Scope
			if got := api.Scope(strings.ToLower(strings.TrimPrefix(m[2], "Scope"))); got != want {
				t.Errorf("%s registers %s with the scope %q, api.Authorities says %q", f, m[1], got, want)
			}
		}
	}
	if seen < 10 {
		t.Fatalf("found only %d registrations of kinds: the scan is broken", seen)
	}
}
