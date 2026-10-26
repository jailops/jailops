package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionAndCheckConfig(t *testing.T) {
	if run([]string{"version"}) != 0 {
		t.Fatal("version")
	}
	if run([]string{"-check-config", "-config", "../../packaging/jailopsd.yaml"}) != 0 {
		t.Fatal("the sample configuration does not pass")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(bad, []byte("pool: tank\nnope: 1\n"), 0o600)
	if run([]string{"-check-config", "-config", bad}) == 0 {
		t.Fatal("a bad configuration passed")
	}
	if run([]string{"extra-arg"}) != 2 {
		t.Fatal("unexpected arguments")
	}
}
