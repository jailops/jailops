package render

import (
	"fmt"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// The bash prompt of a FreeBSD jail (docs/shell.md): one file JailOps owns, api.PromptFile, and a marked block
// at the top of the two system start-up files of the FreeBSD bash package that sources it.

// PromptFile renders api.PromptFile for the PS1 text ps1. The text must pass api.CheckPrompt: it is written as a
// single-quoted assignment, and the check guarantees that it holds no single quote, no line break and nothing
// bash would run when it expands the prompt. The assignment is made only in an interactive bash, so sh, scripts
// and `bash -c` are not affected.
func PromptFile(ps1 string) (string, error) {
	if ps1 == api.PromptDefault || ps1 == api.PromptNone {
		return "", fmt.Errorf("render: %q is a prompt value, not a PS1 text", ps1)
	}
	if err := api.CheckPrompt(ps1); err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	// belt and braces: CheckPrompt refuses both
	if strings.ContainsAny(ps1, "'\n\r\x00") {
		return "", fmt.Errorf("render: the prompt holds a quote or a line break")
	}
	var b strings.Builder
	b.WriteString("# Managed by JailOps: the bash prompt of this jail. JailOps rewrites this file;\n")
	b.WriteString("# change the prompt with: jailops config set JAIL prompt default|none|PS1\n")
	b.WriteString("# Your own ~/.bashrc, ~/.bash_profile or ~/.profile is read after this file and wins.\n")
	b.WriteString("# Only an interactive bash sets the prompt; sh, scripts and bash -c are not affected.\n")
	b.WriteString("if [ -n \"${BASH_VERSION-}\" ] && [ -n \"${PS1-}\" ]; then\n")
	b.WriteString("\tPS1='" + ps1 + "'\n")
	b.WriteString("fi\n")
	return b.String(), nil
}

// The marked block that sources api.PromptFile.
const (
	promptBegin = "# BEGIN jailops prompt: managed by JailOps (jailops config set JAIL prompt none removes it)"
	promptEnd   = "# END jailops prompt"
)

func promptBlock() string {
	return promptBegin + "\n" +
		"if [ -r " + api.PromptFile + " ]; then . " + api.PromptFile + "; fi\n" +
		promptEnd + "\n"
}

// splitPromptBlock returns existing without the marked block, and whether there was one. A begin line without
// an end line is an error: the file was edited by hand and is left alone.
func splitPromptBlock(existing string) (string, bool, error) {
	lines := strings.SplitAfter(existing, "\n")
	var out strings.Builder
	found, in := false, false
	for _, l := range lines {
		t := strings.TrimRight(l, "\n")
		switch {
		case !in && strings.HasPrefix(t, "# BEGIN jailops prompt"):
			if found {
				return "", false, fmt.Errorf("render: the file has two jailops prompt blocks")
			}
			in, found = true, true
		case in && t == promptEnd:
			in = false
		case in:
		default:
			out.WriteString(l)
		}
	}
	if in {
		return "", false, fmt.Errorf("render: the jailops prompt block has no end line (%q)", promptEnd)
	}
	return out.String(), found, nil
}

// PromptHookSet returns existing (the content of api.ProfilePath or api.BashrcPath, empty when the file does not
// exist) with the marked block at its top, so the file's own lines and the user's start-up files come after it
// and win. A block that is there already is replaced; every other line is kept as it is.
func PromptHookSet(existing string) (string, error) {
	rest, _, err := splitPromptBlock(existing)
	if err != nil {
		return "", err
	}
	return promptBlock() + rest, nil
}

// PromptHookRemove returns existing without the marked block and whether there was one.
func PromptHookRemove(existing string) (string, bool, error) {
	return splitPromptBlock(existing)
}
