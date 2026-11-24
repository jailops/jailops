package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A jail that jailops-sync keeps is put back by its next sync: apply says so, and says nothing for a jail sync does not keep.
func TestJailApplyWarnsWhenSyncManagesTheJail(t *testing.T) {
	h := newHarness(t)
	h.seed()
	running := api.JailStatus{Power: api.PowerRunning}
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "kept", Project: "main", Labels: map[string]string{"sync.jailops.net/hash": "abc"}}, Spec: api.JailSpec{Image: "freebsd-15"}, Status: running})
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "mine", Project: "main"}, Spec: api.JailSpec{Image: "freebsd-15"}, Status: running})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte("steps:\n  - run: [echo, hi]\n    creates: /tmp/hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := h.run("", "jail", "apply", "kept", "-f", dir)
	if code != 0 || !strings.Contains(errOut, "the jail is managed by sync, the next sync may revert this") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = h.run("", "jail", "apply", "mine", "-f", dir)
	if code != 0 || strings.Contains(errOut, "managed by sync") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	// a dry run changes nothing, so it does not warn
	_, errOut, _ = h.run("", "jail", "apply", "kept", "-f", dir, "--dry-run")
	if strings.Contains(errOut, "managed by sync") {
		t.Fatalf("a dry run warned: %q", errOut)
	}
}
