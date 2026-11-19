package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaleImportFilesAreRemovedAtStart(t *testing.T) {
	s := &Service{d: Deps{StateDir: t.TempDir()}}
	dir := s.importStagingDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"import-1.export", "import-2.export", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.cleanImportStaging()
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "other.txt" {
		t.Fatalf("left: %v", ents)
	}
}
