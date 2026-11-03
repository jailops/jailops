package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestPromptFileGolden(t *testing.T) {
	got, err := PromptFile(api.DefaultPS1(api.DefaultPromptColor))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "prompt_file", got)
	for _, bad := range []string{"default", "none", "", "it's", "$(id)", "`id`", "a\nb", "\\x"} {
		if _, err := PromptFile(bad); err == nil {
			t.Errorf("PromptFile(%q) was accepted", bad)
		}
	}
}

func TestPromptHook(t *testing.T) {
	got, err := PromptHookSet("")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "prompt_hook_empty", got)
	existing := "# the administrator's own profile\nexport EDITOR=vi\nPS1='admin> '\n"
	got, err = PromptHookSet(existing)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "prompt_hook_existing", got)
	if !strings.HasSuffix(got, existing) {
		t.Fatalf("the file's own lines must follow the block unchanged:\n%s", got)
	}
	// idempotent, one block only
	again, err := PromptHookSet(got)
	if err != nil || again != got || strings.Count(again, "BEGIN jailops prompt") != 1 {
		t.Fatalf("%q %v", again, err)
	}
	// a block somewhere else moves to the top, the rest stays
	moved, err := PromptHookSet(existing + promptBlock())
	if err != nil || moved != got {
		t.Fatalf("%q %v", moved, err)
	}
	// removal gives the file back as it was
	rest, found, err := PromptHookRemove(got)
	if err != nil || !found || rest != existing {
		t.Fatalf("%q %v %v", rest, found, err)
	}
	if rest, found, err := PromptHookRemove(existing); err != nil || found || rest != existing {
		t.Fatalf("%q %v %v", rest, found, err)
	}
	// a block cut in half by a hand edit is left alone
	broken := promptBegin + "\nwhatever\n"
	if _, err := PromptHookSet(broken); err == nil {
		t.Fatal("an unterminated block was accepted")
	}
	if _, _, err := PromptHookRemove(promptBlock() + "x\n" + promptBlock()); err == nil {
		t.Fatal("two blocks were accepted")
	}
}

// hostile prompts that pass CheckPrompt: they look like shell syntax but are only prompt text and escapes
var trickyPrompts = []string{
	api.DefaultPS1(40),
	`\u@\h:\w\$ `,
	`\[\033[1;31m\]\D{%H:%M}\[\033[0m\] \w \$ `,
	`a;b|c&d>e<f"g\$(touch MARK)`,
	`\$(touch MARK) \$HOME !! \#`,
	`→ \W ✓ `,
}

func TestPromptFileIsInertInTheShells(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "MARK")
	for _, ps1 := range trickyPrompts {
		if err := api.CheckPrompt(ps1); err != nil {
			t.Fatalf("CheckPrompt(%q): %v", ps1, err)
		}
		body, err := PromptFile(ps1)
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, "prompt.sh")
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		// POSIX sh reads the file as it is and assigns nothing (BASH_VERSION is unset in sh).
		out, err := exec.Command("sh", "-c", `PS1=x; . "$1"; printf %s "$PS1"`, "sh", f).CombinedOutput()
		if err != nil || string(out) != "x" {
			t.Fatalf("sh with %q: %q %v", ps1, out, err)
		}
		// bash assigns the text exactly and expanding it as a prompt runs nothing.
		bash, err := exec.LookPath("bash")
		if err != nil {
			continue // the sh part above holds on every system; bash is checked where it is installed
		}
		cmd := exec.Command(bash, "--norc", "--noprofile", "-c", `PS1=x; . "$1"; printf '%s\n' "$PS1"; printf '%s' "${PS1@P}" >/dev/null`, "bash", f)
		cmd.Dir = dir
		out, err = cmd.CombinedOutput()
		if err != nil || string(out) != ps1+"\n" {
			t.Fatalf("bash with %q: %q %v", ps1, out, err)
		}
		if _, err := os.Stat(mark); err == nil {
			t.Fatalf("expanding %q ran a command", ps1)
		}
	}
}

// The escapes CheckPrompt refuses are the ones that do run a command when bash expands the prompt: the check is
// not stricter than it has to be by guesswork.
func TestRefusedPromptEscapesWouldRun(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Log("bash is not installed here; CheckPrompt's refusals are still covered by sdk/api")
		return
	}
	for _, ps1 := range []string{`\\\$(touch MARK)`, `\044(touch MARK)`, `\134\$(touch MARK)`, `\140touch MARK\140`, `$(touch MARK)`} {
		if api.CheckPrompt(ps1) == nil {
			t.Fatalf("CheckPrompt accepts %q", ps1)
		}
		dir := t.TempDir()
		cmd := exec.Command(bash, "--norc", "--noprofile", "-c", `PS1=$1; printf '%s' "${PS1@P}" >/dev/null`, "bash", ps1)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%q: %s %v", ps1, out, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "MARK")); err != nil {
			t.Logf("bash %s did not run the command of %q: the refusal may be stricter than needed here (not a safety property)", bash, ps1)
		}
	}
}

// FuzzPromptFile: whatever CheckPrompt accepts is assigned exactly and runs nothing when bash shows it.
func FuzzPromptFile(f *testing.F) {
	for _, p := range trickyPrompts {
		f.Add(p)
	}
	f.Add(`\\\$(touch MARK)`)
	f.Add(`\044(touch MARK)`)
	bash, _ := exec.LookPath("bash")
	f.Fuzz(func(t *testing.T, ps1 string) {
		if api.CheckPrompt(ps1) != nil || ps1 == api.PromptDefault || ps1 == api.PromptNone {
			return
		}
		body, err := PromptFile(ps1)
		if err != nil {
			t.Fatalf("%q passed CheckPrompt but PromptFile refused it: %v", ps1, err)
		}
		if bash == "" {
			return
		}
		dir := t.TempDir()
		file := filepath.Join(dir, "prompt.sh")
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, "--norc", "--noprofile", "-c", `PS1=x; . "$1"; printf '%s' "$PS1"; printf '%s' "${PS1@P}" >/dev/null`, "bash", file)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != ps1 {
			t.Fatalf("%q: %q %v", ps1, out, err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 1 {
			t.Fatalf("expanding %q made files: %v", ps1, ents)
		}
	})
}
