package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
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

func TestImportsBeingReceivedAreLimitedAndTheSlotComesBack(t *testing.T) {
	s := &Service{d: Deps{StateDir: t.TempDir()}}
	for i := 0; i < importSlots; i++ {
		importSlot <- struct{}{}
	}
	if _, err := s.stageImport(strings.NewReader("x")); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("with every slot taken the import is busy, got %v", err)
	}
	for i := 0; i < importSlots; i++ {
		<-importSlot
	}
	st, err := s.stageImport(strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.discard()
	if len(importSlot) != 0 {
		t.Fatal("a file that has arrived keeps no slot")
	}
	st.discard()
	if ents, _ := os.ReadDir(s.importStagingDir()); len(ents) != 0 {
		t.Fatalf("the file is removed: %v", ents)
	}
}
