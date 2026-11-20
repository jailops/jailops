package priv

import (
	"os"
	"path/filepath"
	"testing"
)

// pkg runs as pkg itself when it is installed: the program that fetches it refuses -r with a root that has no pkg (a jail tree).
func TestPkgRunsAsPkgItselfWhenItIsInstalled(t *testing.T) {
	defer func(old string) { pkgReal = old }(pkgReal)
	r := &Rule{Name: "pkg", Binary: "/usr/sbin/pkg"}
	pkgReal = filepath.Join(t.TempDir(), "absent")
	if got := binaryOf(r); got != "/usr/sbin/pkg" {
		t.Fatalf("without pkg itself the program that fetches it runs, got %s", got)
	}
	pkgReal = filepath.Join(t.TempDir(), "pkg")
	if err := os.WriteFile(pkgReal, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := binaryOf(r); got != pkgReal {
		t.Fatalf("with pkg itself installed it runs, got %s", got)
	}
	other := &Rule{Name: "zfs", Binary: "/sbin/zfs"}
	if got := binaryOf(other); got != "/sbin/zfs" {
		t.Fatalf("another command keeps its program, got %s", got)
	}
}
