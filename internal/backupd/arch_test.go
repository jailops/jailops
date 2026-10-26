package backupd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "jailops.net/jailops/"

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

func goRun(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

// The backup node is a keyless binary of its own. It must not link the host
// daemon's machinery (jails, images, privileged helper, rendering, networking,
// SQLite state), nor the key handling of the repository, nor any decrypt function.
func TestBinaryImportsStayInsideTheNodesBoundary(t *testing.T) {
	root := moduleRoot(t)
	deps := strings.Fields(goRun(t, root, nil, "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/jailops-backupd"))
	forbiddenPrefixes := []string{
		module + "internal/lifecycle", module + "internal/imagegen", module + "internal/priv", module + "internal/sys",
		module + "internal/render", module + "internal/ipam", module + "internal/ndproxy", module + "internal/repo/keys",
		module + "internal/store", module + "internal/state", module + "internal/service", module + "internal/reconcile",
		module + "internal/plan", module + "internal/replicate", module + "internal/fleet", module + "internal/backup",
		module + "internal/webui", module + "internal/daemon", module + "internal/cli",
		module + "internal/node/nodetest", module + "internal/repo/engine", module + "internal/repo/catalog", module + "internal/repo/chunker",
		module + "internal/repo/s3fake", module + "internal/repo/conformance",
		"modernc.org/sqlite", "filippo.io/age", "github.com/spf13/cobra",
	}
	for _, d := range deps {
		for _, f := range forbiddenPrefixes {
			if d == f || strings.HasPrefix(d, f+"/") {
				t.Errorf("cmd/jailops-backupd links %s (forbidden: %s)", d, f)
			}
		}
	}
	for _, must := range []string{module + "internal/node", module + "internal/node/ctl", module + "internal/repo/s3", module + "internal/repo/datastore", module + "internal/backupui"} {
		found := false
		for _, d := range deps {
			found = found || d == must
		}
		if !found {
			t.Errorf("cmd/jailops-backupd does not link %s: the test is not looking at the right binary", must)
		}
	}
}

// The linked binary contains no function that decrypts or derives keys. Dead code
// elimination removes what the node never calls, so the symbol table is the proof.
func TestBinaryHasNoDecryptCode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	root := moduleRoot(t)
	bin := filepath.Join(t.TempDir(), "jailops-backupd")
	goRun(t, root, []string{"CGO_ENABLED=0"}, "build", "-o", bin, "./cmd/jailops-backupd")
	syms := goRun(t, root, nil, "tool", "nm", bin)
	for _, bad := range []string{
		"format.(*Keys).DecodeChunk", "format.(*Keys).OpenManifest", "format.(*Keys).SealChunk", "format.(*Keys).SealManifest",
		"format.(*Keys).EncodeChunk", "format.(*Keys).ChunkID", "format.(*Keys).GroupID", "format.DeriveKeys", "format.GenerateMaster",
		"format.DecodeRecoveryKey", "format.EncodeRecoveryKey",
		"repo/keys.",
	} {
		if strings.Contains(syms, bad) {
			t.Errorf("the backup node binary contains %s", bad)
		}
	}
	if !strings.Contains(syms, "format.VerifyBlob") || !strings.Contains(syms, "format.ParseIndex") {
		t.Fatal("the binary does not contain the keyless checks: the test is not looking at the right symbols")
	}
}

// The node library's own imports: the keyless packages never name a decrypt function.
func TestNodeSourcesNameNoDecryptFunction(t *testing.T) {
	root := moduleRoot(t)
	var files []string
	for _, dir := range []string{"internal/node", "internal/node/ctl", "internal/node/wire", "internal/backupd", "cmd/jailops-backupd"} {
		m, _ := filepath.Glob(filepath.Join(root, dir, "*.go"))
		for _, f := range m {
			if !strings.HasSuffix(f, "_test.go") {
				files = append(files, f)
			}
		}
	}
	if len(files) < 20 {
		t.Fatalf("only %d source files found", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"DecodeChunk", "OpenManifest", "SealChunk", "SealManifest", "DeriveKeys", "GenerateMaster", "chacha20poly1305", "repo/keys\""} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s mentions %s", filepath.Base(f), bad)
			}
		}
	}
}
