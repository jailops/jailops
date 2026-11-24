package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const varsRecipe = `vars:
  site: {required: true}
  port: {default: 8080}
  db_password: {secret: true, required: true}
steps:
  - dir: {path: "/var/db/${site}", mode: "0750"}
  - file:
      path: /usr/local/etc/${site}.conf
      template: true
      mode: "0600"
      content: |
        listen ${port};
        password ${db_password}
  - run: [service, app, reload]
`

func recipeDir(t *testing.T, recipe string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "recipe")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// --validate renders and checks the recipe in the client: it needs no jail name and makes no connection, so it works with
// no daemon behind the configured remote. A mistake exits 2, names the step and field and carries no value.
func TestJailApplyValidateNeedsNoDaemon(t *testing.T) {
	h := newHarness(t)
	h.writeConfig("current: t\nremotes:\n  t:\n    url: http://127.0.0.1:1\nprojects:\n  t: main\n") // nothing listens there
	h.ts.Close()
	dir := recipeDir(t, varsRecipe)
	secretFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(secretFile, []byte("Zx9-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code := h.run("", "jail", "apply", "-f", dir, "--set", "site=example.test", "--set-file", "db_password="+secretFile, "--validate")
	if code != 0 || !strings.Contains(out, "recipe is valid: 3 steps") || strings.Contains(out+errs, "Zx9-secret-value") {
		t.Fatalf("validate: %d %q %q", code, out, errs)
	}
	if n := len(h.srv.Requests()); n != 0 {
		t.Fatalf("the daemon got %d requests", n)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--set-file", "db_password=" + secretFile}, "site is required"},
		{[]string{"--set", "site=x", "--set", "db_password=Zx9-secret-value"}, "db_password is secret"},
		{[]string{"--set", "site=x", "--set-file", "db_password=" + secretFile, "--set", "typo=1"}, "the recipe declares no such variable"},
		{[]string{"--set", "site=bad name", "--set-file", "db_password=" + secretFile}, "steps[0].dir.path"},
	} {
		out, errs, code := h.run("", append([]string{"jail", "apply", "-f", dir, "--validate"}, c.args...)...)
		if code != 2 || !strings.Contains(errs, c.want) || strings.Contains(out+errs, "Zx9-secret-value") || strings.Contains(errs, "bad name") {
			t.Errorf("%v: %d %q %q", c.args, code, out, errs)
		}
	}
	// without --validate the jail is named
	if _, errs, code := h.run("", "jail", "apply", "-f", dir, "--set", "site=x", "--set-file", "db_password="+secretFile); code != 2 || !strings.Contains(errs, "name the jail") {
		t.Errorf("no name: %d %q", code, errs)
	}
}

// The secret goes from its file into the request, marked sensitive, and is not in what the command prints or in a file of
// the client.
func TestJailApplySendsTheRenderedRecipeAndTheSecretOnlyInTheFile(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default")
	h.run("", "start", "web")
	const secret = "Zx9-secret-value"
	secretFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := recipeDir(t, varsRecipe)
	args := []string{"jail", "apply", "web", "-f", dir, "--set", "site=example.test", "--set-file", "db_password=" + secretFile}
	out, errs, code := h.run("", args...)
	if code != 0 || strings.Contains(out+errs, secret) || !strings.Contains(out, "dir") {
		t.Fatalf("apply: %d %q %q", code, out, errs)
	}
	out, errs, code = h.run("", append([]string{"--dry-run"}, args...)...)
	if code != 0 || strings.Contains(out+errs, secret) {
		t.Fatalf("dry run: %d %q %q", code, out, errs)
	}
	var sent []byte
	for _, r := range h.srv.Requests() {
		if strings.HasSuffix(r.Path, "/jails/web/apply") {
			sent = r.Body
		}
	}
	for _, want := range []string{`"sensitive":true`, `"path":"/usr/local/etc/example.test.conf"`, `listen 8080;\npassword ` + secret, `"path":"/var/db/example.test"`} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("the request has no %s: %s", want, sent)
		}
	}
	if strings.Contains(string(sent), "vars") || strings.Contains(string(sent), "template") {
		t.Errorf("the request carries the variables or the template flag: %s", sent)
	}
	// no file of the client has it but the secret file itself
	err := filepath.WalkDir(h.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secret) {
				t.Errorf("the secret is in %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// An unquoted false in a command is a mistake of YAML with a hint, and the CLI names the step.
func TestJailApplyUnquotedFalse(t *testing.T) {
	h := newHarness(t)
	dir := recipeDir(t, "steps:\n  - run: [false]\n")
	_, errs, code := h.run("", "jail", "apply", "-f", dir, "--validate")
	if code != 2 || !strings.Contains(errs, "steps[0].run[0]") || !strings.Contains(errs, "put quotes around it") {
		t.Fatalf("%d %q", code, errs)
	}
}

// A Volume in an apply file is made when it is missing, left alone when it exists with the same spec, updated in what a
// file may change (the comment) and refused when the file asks for another size or owner: the data is never re-owned or
// resized by a file.
func TestApplyVolumeKind(t *testing.T) {
	h := newHarness(t)
	h.seed()
	doc := func(spec string) string {
		return writeFile(t, h.dir, "vol.yaml", "kind: Volume\nmetadata: {name: pgdata}\nspec: {storage: default, "+spec+"}\n", 0o644)
	}
	tr := h.transcript()
	if out := tr.ok("apply", "-f", doc("size: 10G, uid: 770, gid: 770, comment: data")); !strings.Contains(out, "volume/pgdata created") {
		t.Fatalf("create: %q", out)
	}
	if out := tr.ok("apply", "-f", doc("size: 10G, uid: 770, gid: 770, comment: data")); !strings.Contains(out, "unchanged") {
		t.Fatalf("same spec: %q", out)
	}
	if out := tr.ok("apply", "-f", doc("comment: other words")); !strings.Contains(out, "updated") {
		t.Fatalf("a field the file leaves out is not compared; the comment is updated: %q", out)
	}
	for _, c := range [][2]string{{"size: 20G", "size"}, {"uid: 1", "uid"}, {"mode: \"0700\"", "mode"}, {"encrypted: true", "encrypted"}} {
		spec, want := c[0], c[1]
		_, errs, code := tr.do("", "apply", "-f", doc(spec))
		if code != 2 || !strings.Contains(errs, "differs in "+want) || !strings.Contains(errs, "its data stays as it is") {
			t.Errorf("%s: %d %q", spec, code, errs)
		}
	}
	tr.golden("apply_volume")
}

// --validate needs no project either: nothing is sent, so there is no jail and no project to name.
func TestJailApplyValidateNeedsNoProject(t *testing.T) {
	h := newHarness(t)
	h.writeConfig("current: t\nremotes:\n  t:\n    url: http://127.0.0.1:1\n") // no project chosen, nothing listens
	h.ts.Close()
	dir := recipeDir(t, "steps:\n  - package: [nginx]\n")
	out, errs, code := h.run("", "jail", "apply", "-f", dir, "--validate")
	if code != 0 || !strings.Contains(out, "recipe is valid: 1 steps") {
		t.Fatalf("validate without a project: %d %q %q", code, out, errs)
	}
	// without --validate the project is still required
	if _, errs, code := h.run("", "jail", "apply", "web", "-f", dir); code == 0 || !strings.Contains(errs, "project") {
		t.Fatalf("apply without a project: %d %q", code, errs)
	}
}
