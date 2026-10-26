package zfs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"jailops.net/jailops/sdk/api"
)

const compatSample = `# Features supported by a release line
allocation_classes
async_destroy, bookmarks, bookmark_v2
 lz4_compress   # trailing comment
extensible_dataset
`

func TestCheckCompat(t *testing.T) {
	tests := []struct {
		name    string
		active  []string
		content string
		want    Verdict
		block   []string
	}{
		{"all allowed", []string{"async_destroy", "lz4_compress"}, compatSample, Verified, nil},
		{"none active", nil, compatSample, Verified, nil},
		{"active missing", []string{"async_destroy", "zstd_compress", "encryption"}, compatSample, No, []string{"encryption", "zstd_compress"}},
		{"prefix tolerated", []string{"feature@bookmarks"}, compatSample, Verified, nil},
		{"no file", []string{"encryption"}, "", Unknown, nil},
		{"blank file", []string{"encryption"}, " \n\t\n", Unknown, nil},
		{"comment only file is a real empty set", []string{"x"}, "# nothing\n", No, []string{"x"}},
	}
	for _, tc := range tests {
		r := CheckCompatDetail(tc.active, tc.content)
		if r.Verdict != tc.want || !reflect.DeepEqual(r.Blocking, tc.block) {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if CheckCompat(tc.active, tc.content) != tc.want {
			t.Errorf("%s: CheckCompat differs", tc.name)
		}
		if tc.want.Blocks() != (tc.want == No) {
			t.Error("Blocks")
		}
	}
}

func TestLoadCompat(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "openzfs-2.2"), []byte(compatSample), 0o644)
	got, err := LoadCompat(dir, "openzfs-2.2")
	if err != nil || got != compatSample {
		t.Fatal(got, err)
	}
	for name, code := range map[string]api.Code{
		"missing": api.CodeNotFound, "../etc/passwd": api.CodeInvalid, "": api.CodeInvalid,
		"a,b": api.CodeUnsupported, "-x": api.CodeInvalid,
	} {
		if _, err := LoadCompat(dir, name); !api.IsCode(err, code) {
			t.Errorf("%q: %v", name, err)
		}
	}
}
