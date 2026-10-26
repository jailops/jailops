package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// The enum table cannot be derived with reflect, so this test reads the constants from the source of
// sdk/api and fails when the table has drifted (a new error code, kind or state without a table entry).
func TestEnumTableMatchesSource(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "sdk", "api", "*.go"))
	re := regexp.MustCompile(`(?m)^\s*\w+\s+(\w+)\s*=\s*"([^"]*)"`)
	found := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = append(found[m[1]], m[2])
		}
	}
	for typ, vals := range enums {
		want := append([]string(nil), found[typ.Name()]...)
		got := append([]string(nil), vals...)
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("enum %s: table %v, source %v", typ.Name(), got, want)
		}
	}
}
