package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A jail leaves as a file and comes back under another name; the CLI refuses to overwrite, to write a terminal, to
// import a file that is cut off, and --dry-run changes nothing.
func TestJailExportImport(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15@stable", "--storage", "default")
	file := filepath.Join(h.dir, "web.jailexport")
	tr.ok("jail", "export", "web", "--to", file)
	tr.do("", "jail", "export", "web", "--to", file)
	tr.ok("jail", "export", "web", "--to", file, "--force", "--zstd")
	tr.do("", "jail", "export", "web")
	tr.do("", "jail", "export", "nobody", "--to", filepath.Join(h.dir, "none"))
	tr.do("", "jail", "import", file)
	tr.ok("--dry-run", "jail", "import", file, "--as", "web2")
	tr.do("", "jail", "get", "web2")
	tr.ok("jail", "import", file, "--as", "web2")
	tr.ok("jail", "get", "web2")
	tr.do("", "jail", "import", file, "--as", "web3", "--network-map", "lan")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(h.dir, "cut.jailexport")
	if err := os.WriteFile(cut, b[:len(b)-5], 0o600); err != nil {
		t.Fatal(err)
	}
	tr.do("", "jail", "import", cut, "--as", "web4")
	tr.do("", "jail", "get", "web4")
	tr.do(string(b), "jail", "import", "-", "--as", "web5")
	tr.do("not a file", "jail", "import", "-", "--as", "web6")
	tr.golden("jail_export_import")

	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(h.dir, ".*partial*"))
	if len(left) != 0 {
		t.Errorf("leftovers: %v", left)
	}
	if out, _, code := h.run("", "jail", "get", "web5"); code != 0 || !strings.Contains(out, "web5") {
		t.Fatalf("import from the standard input: %d %s", code, out)
	}
}
