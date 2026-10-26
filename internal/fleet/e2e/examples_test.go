package e2e

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"gopkg.in/yaml.v3"

	"jailops.net/jailops/internal/fleet/cli"
	"jailops.net/jailops/internal/fleet/fleettest"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/internal/fleet/vault"
	"jailops.net/jailops/sdk/fake"
)

// examplesDir holds every runnable example. Each directory that contains a fleet.yaml is a fleet and is tested.
const examplesDir = "../../../examples"

// exampleFleets lists the fleet directories of examples/ (including nested ones such as provisioning/fleet).
func exampleFleets(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(examplesDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "fleet.yaml" {
			out = append(out, filepath.Dir(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 6 {
		t.Fatalf("expected at least six example fleets, found %v", out)
	}
	return out
}

// exampleRel is the name of an example directory relative to examples/.
func exampleRel(dir string) string {
	r, _ := filepath.Rel(examplesDir, dir)
	return filepath.ToSlash(r)
}

func exampleIdentityFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(examplesDir, "example-identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runFleet runs the real jailops-sync command line against a fleet directory, with the example's throwaway identity.
func runFleet(t *testing.T, dir, idf string, args ...string) result {
	t.Helper()
	t.Setenv(vault.EnvKeyFile, idf)
	os.Unsetenv(vault.EnvKey)
	os.Unsetenv(vault.EnvKeyCmd)
	var out, errb bytes.Buffer
	code := cli.Run(cli.Env{Stdout: &out, Stderr: &errb, Getenv: func(k string) string {
		switch k {
		case secrets.EnvIdentityFile, vault.EnvKeyFile:
			return idf
		case cli.EnvStateDir:
			return filepath.Join(t.TempDir(), "state")
		}
		return ""
	}}, append([]string{"-C", dir}, args...))
	return result{code, out.String(), errb.String()}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pointAtFakes copies a fleet to a temporary directory and points every host at an in-memory host: the host.yaml
// keeps its name and labels but reaches a fake server, and the client-certificate file (which names a host that
// does not exist here) is dropped. Nothing else changes, so what runs is what the example declares.
func pointAtFakes(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, src, dst)
	hosts, _ := filepath.Glob(filepath.Join(dst, "hosts", "*", "host.yaml"))
	for _, hf := range hosts {
		b, err := os.ReadFile(hf)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(filepath.Dir(hf))
		srv := fake.New(fake.WithHostName(name))
		ts := httptest.NewServer(srv)
		t.Cleanup(func() { ts.Close(); srv.Close() })
		doc["spec"] = map[string]any{"url": ts.URL}
		out, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hf, out, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(filepath.Dir(hf), "remote.sops.yaml"))
	}
	return dst
}

const planGolden = "EXPECTED-PLAN.md"

// goldenDoc wraps a plan as the example's EXPECTED-PLAN.md (a .md file may sit beside fleet.yaml).
func goldenDoc(plan string) string {
	return "# The first plan\n\nWhat `jailops-sync plan` prints for this fleet against a host that has nothing yet. The test suite compares it with\nthe real output on every run, so this page cannot go stale (regenerate with `JAILOPS_SYNC_REGEN_EXAMPLE=1 go test ./internal/fleet/e2e`).\n\n```\n" + plan + "```\n"
}

// TestExamplesNeverRot runs every example fleet: it validates, renders, plans against empty in-memory hosts (the
// plan is compared with the example's EXPECTED-PLAN.md), applies, and then plans again and must find nothing to do.
// Regenerate the expected plans with JAILOPS_SYNC_REGEN_EXAMPLE=1.
func TestExamplesNeverRot(t *testing.T) {
	regen := os.Getenv("JAILOPS_SYNC_REGEN_EXAMPLE") != ""
	for _, dir := range exampleFleets(t) {
		t.Run(exampleRel(dir), func(t *testing.T) {
			idf := exampleIdentityFile(t)
			r := mustOK(t, runFleet(t, dir, idf, "validate"))
			contains(t, r.out, "ok: fleet")
			mustOK(t, runFleet(t, dir, idf, "check")) // `check` is the same command as `validate`
			mustOK(t, runFleet(t, dir, idf, "render"))

			live := pointAtFakes(t, dir)
			plan := mustOK(t, runFleet(t, live, idf, "plan")).out
			plan = goldenDoc(normalizePlan(plan))
			gold := filepath.Join(dir, planGolden)
			if regen {
				if err := os.WriteFile(gold, []byte(plan), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(gold)
			if err != nil {
				t.Fatalf("%v (JAILOPS_SYNC_REGEN_EXAMPLE=1 writes it)", err)
			}
			if string(want) != plan {
				t.Fatalf("the plan of %s changed; regenerate %s with JAILOPS_SYNC_REGEN_EXAMPLE=1 if that is intended\n--- want\n%s\n--- got\n%s", dir, planGolden, want, plan)
			}
			if strings.Contains(plan, "ERROR") || strings.Contains(plan, "refuse") {
				t.Fatalf("the example's plan has problems:\n%s", plan)
			}

			ap := mustOK(t, runFleet(t, live, idf, "sync", "--apply"))
			contains(t, ap.out, "0 failed")
			again := mustOK(t, runFleet(t, live, idf, "plan")).out
			contains(t, again, "0 create, 0 update, 0 delete")
			mustOK(t, runFleet(t, live, idf, "drift"))
		})
	}
}

// normalizePlan drops what differs between runs: nothing today, but fake host addresses must never leak into a golden file.
func normalizePlan(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "127.0.0.1:") {
			l = "<address>"
		}
		b.WriteString(l + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// TestGenerateExampleSecrets writes the throwaway-encrypted files of the examples that use secrets. It runs only on
// request, so the files in git stay as they are:
//
//	JAILOPS_SYNC_REGEN_EXAMPLE=1 go test ./internal/fleet/e2e -run GenerateExampleSecrets
//
// Every file is encrypted to the example identity examples/example-identity.txt, which protects nothing real.
func TestGenerateExampleSecrets(t *testing.T) {
	if os.Getenv("JAILOPS_SYNC_REGEN_EXAMPLE") == "" {
		t.Skip("set JAILOPS_SYNC_REGEN_EXAMPLE=1 to regenerate the secrets of the examples")
	}
	idf := exampleIdentityFile(t)
	b, _ := os.ReadFile(idf)
	var id *age.X25519Identity
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "AGE-SECRET-KEY-") {
			var err error
			if id, err = age.ParseX25519Identity(strings.TrimSpace(l)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if id == nil {
		t.Fatal("no identity in examples/example-identity.txt")
	}
	t.Setenv(vault.EnvKeyFile, idf)
	os.Unsetenv(vault.EnvKey)
	os.Unsetenv(vault.EnvKeyCmd)

	token := func(dir, name string) {
		os.MkdirAll(filepath.Join(dir, "secrets"), 0o755)
		fleettest.WriteSecret(t, filepath.Join(dir, "secrets"), name, "jot_example_token_not_a_real_secret_"+name, id.Recipient().String())
	}
	sops := func(dir, rel, plain string) {
		enc, err := vault.EncryptYAML(dir, rel, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		if err := vault.WriteFileAtomic(filepath.Join(dir, rel), enc, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := filepath.Join(examplesDir, "web-stack")
	token(ws, "app1-token")
	sops(ws, vault.SecretsRel, "db:\n  password: example-db-password-not-real\n")

	mh := filepath.Join(examplesDir, "multi-host-fleet")
	token(mh, "eu1-token")
	token(mh, "eu2-token")
	sops(mh, vault.SecretsRel, "site:\n  api_key: example-api-key-not-real\n")
	certPEM, keyPEM, _, err := vault.NewClientIdentity("jailops-sync-eu2", time.Now(), 10*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ind := func(s string) string {
		return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ") + "\n"
	}
	sops(mh, vault.RemoteRel("eu2"), "url: https://eu2.example.org:8443\nserver_fingerprint: \"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\"\n"+
		"client_cert: |\n"+ind(string(certPEM))+"client_key: |\n"+ind(string(keyPEM)))
}

// The provisioning script is the one thing in examples/ that is not YAML: it must at least parse. (UNVERIFIED: it has
// not been run on a fresh FreeBSD VM from this repository.)
func TestProvisioningScriptParses(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh is needed to check the script: %v", err)
	}
	for _, f := range []string{"bootstrap.sh", "user-data"} {
		if out, err := exec.Command(sh, "-n", filepath.Join(examplesDir, "provisioning", f)).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
	}
	b, _ := os.ReadFile(filepath.Join(examplesDir, "provisioning", "bootstrap.sh"))
	for _, want := range []string{"jailops-priv init", "jailops-sync", "freebsd-version", "doctor"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("bootstrap.sh no longer mentions %q", want)
		}
	}
}
