package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The configuration of a host set up with a key an earlier release accepted is read as it was meant: root-dataset was
// a storage root, and a helper that refused it (or read it as nothing) would leave the jails without their storage.
func TestARetiredKeyOfTheConfigurationFileIsReadAsItsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "priv.conf")
	if err := os.WriteFile(path, []byte("# an old host\nroot-dataset = tank/jailops\nstorage-root = tank/more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var o options
	var out bytes.Buffer
	fs := newFlags(&o, &out)
	if err := loadConfigFile(fs, path, true); err != nil {
		t.Fatalf("the helper refuses the configuration of an earlier release: %v", err)
	}
	if got := fs.Lookup("storage-root").Value.String(); !strings.Contains(got, "tank/jailops") || !strings.Contains(got, "tank/more") {
		t.Errorf("storage roots: %q (the retired key must still give its root)", got)
	}
	if !strings.Contains(out.String(), `"root-dataset" is read as "storage-root"`) {
		t.Errorf("the helper does not say which key to rename: %q", out.String())
	}
}

func TestAKeyThatNeverExistedIsStillAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "priv.conf")
	if err := os.WriteFile(path, []byte("storage-rot = tank/jailops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var o options
	fs := newFlags(&o, &bytes.Buffer{})
	if err := loadConfigFile(fs, path, true); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("a mistyped key: %v", err)
	}
}

func TestARetiredFlagOfTheCommandLineIsReadAsItsReplacement(t *testing.T) {
	var out bytes.Buffer
	got := renameRetiredArgs([]string{"-root-dataset", "tank/a", "--root-dataset=tank/b", "-socket", "/x", "root-dataset"}, &out)
	want := []string{"-storage-root", "tank/a", "-storage-root=tank/b", "-socket", "/x", "root-dataset"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("%q, want %q", got, want)
	}
	var o options
	fs := newFlags(&o, &bytes.Buffer{})
	if err := fs.Parse(got); err != nil {
		t.Fatal(err)
	}
	if v := fs.Lookup("storage-root").Value.String(); !strings.Contains(v, "tank/a") || !strings.Contains(v, "tank/b") {
		t.Errorf("storage roots: %q", v)
	}
}

// Every key of the helper's configuration in every release since 0.8.0 is a flag of this release or a retired key.
func TestEveryKeyOfEveryReleaseIsKnownOrRetired(t *testing.T) {
	files, err := filepath.Glob("../../internal/store/testdata/upgrade/*/config/priv-keys.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("no key lists of earlier releases: %v", err)
	}
	var o options
	fs := newFlags(&o, &bytes.Buffer{})
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range strings.Fields(string(b)) {
			if fs.Lookup(k) != nil {
				continue
			}
			if _, ok := retiredKeys[k]; !ok {
				t.Errorf("%s: the key %q was valid in that release and is neither known nor retired: a host that has it would not start its helper after the upgrade (add it to retiredKeys with its replacement)", filepath.Base(filepath.Dir(filepath.Dir(f))), k)
			}
		}
	}
}
