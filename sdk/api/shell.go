package api

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The shell of a FreeBSD jail and its bash prompt (docs/shell.md).
//
// A new FreeBSD jail gets bash, installed from the host with pkg -r into the jail's /usr/local, as the shell of
// `jailops shell` and the console, and a coloured prompt that bash reads for every user. The prompt lives in one file
// JailOps owns (PromptFile); the two system start-up files of the FreeBSD bash package source it. A user's own
// ~/.bashrc, ~/.bash_profile or ~/.profile is read after it and wins.

// Shell paths and names.
const (
	// BashPath is where the FreeBSD bash package installs bash.
	BashPath = "/usr/local/bin/bash"
	// ShPath is FreeBSD's own shell.
	ShPath = "/bin/sh"
	// BashPackage is the package that provides BashPath.
	BashPackage = "bash"
	// ShellBash and ShellSh are the names the shell key, `jail create --shell` and jails.shell of jailopsd.yaml
	// accept for BashPath and ShPath.
	ShellBash = "bash"
	ShellSh   = "sh"
)

// Prompt values besides a PS1 text.
const (
	// PromptDefault is the default prompt in the colour of jails.prompt_color (DefaultPS1).
	PromptDefault = "default"
	// PromptNone writes no prompt file; bash then uses its own prompt or the user's.
	PromptNone = "none"
	// DefaultPromptColor is the 256-colour number of the default prompt of jails: 39, a light blue.
	DefaultPromptColor = 39
	// MaxPromptLen bounds a PS1 text, in bytes.
	MaxPromptLen = 256
)

// Files JailOps writes into a jail root for the prompt. The FreeBSD bash package (built with its SYSBASHRC option)
// reads BashrcPath in every interactive shell that is not a login shell and ProfilePath in every login shell; neither
// file belongs to a package. JailOps puts a marked block that sources PromptFile at the top of both, so whatever else
// they and the user's own files set comes later and wins.
const (
	PromptFile  = "/usr/local/etc/profile.d/jailops-prompt.sh"
	ProfilePath = "/usr/local/etc/profile"
	BashrcPath  = "/usr/local/etc/bash.bashrc"
)

// ShellPath turns the names bash and sh into their paths; anything else is returned as it is.
func ShellPath(v string) string {
	switch v {
	case ShellBash:
		return BashPath
	case ShellSh:
		return ShPath
	}
	return v
}

// DefaultPS1 is the default prompt: user@host:directory and # or $, in 256-colour number color, with the colour
// sequences marked as non-printing so bash measures the line correctly.
func DefaultPS1(color int) string {
	return fmt.Sprintf(`\[\e[38;5;%dm\]\u@\h:\w\$\[\e[0m\]`, color)
}

// CheckPromptColor reports whether n is a 256-colour number.
func CheckPromptColor(n int) error {
	if n < 0 || n > 255 {
		return fmt.Errorf("the prompt colour %d must be a number between 0 and 255", n)
	}
	return nil
}

// promptEscapes are the characters bash accepts after a backslash in PS1 (bash(1), PROMPTING), besides the octal
// digits and \D{format}.
const promptEscapes = `adehHjlnrstT@AuvVwW!#$[]`

// CheckPrompt reports whether v is a prompt JailOps writes: default, none, or a PS1 text. The text is written as
// one single-quoted shell assignment, and bash expands parameters and commands in PS1 each time it shows the
// prompt, so the check is an allow-list: at most MaxPromptLen bytes of printable UTF-8 on one line, no single
// quote and no backquote, a dollar sign only as the escape \$, and a backslash only before one of bash's prompt
// escapes (\u, \h, \w, \e, \[, \], \D{...}, ...). Escapes that put a character bash would expand into the
// prompt are refused: \\ (a backslash) and octal escapes other than \033 and \007 (\044 is a $). Nothing in
// such a text can end the quoting or run a command; colours are written as \e[...m or \033[...m between \[
// and \].
func CheckPrompt(v string) error {
	switch v {
	case PromptDefault, PromptNone:
		return nil
	case "":
		return fmt.Errorf("the prompt is empty: give a PS1 text, default or none")
	}
	if len(v) > MaxPromptLen {
		return fmt.Errorf("the prompt is %d bytes long; at most %d are accepted", len(v), MaxPromptLen)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("the prompt is not valid UTF-8")
	}
	rs := []rune(v)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\'':
			return fmt.Errorf("the prompt may not contain a single quote")
		case r == '`':
			return fmt.Errorf("the prompt may not contain a backquote: commands in the prompt are not accepted")
		case r == '$':
			return fmt.Errorf("the prompt may contain $ only as \\$ (# for root, $ for everyone else): variables and commands in the prompt are not accepted")
		case r != ' ' && (unicode.IsControl(r) || !unicode.IsPrint(r)):
			return fmt.Errorf("the prompt contains a control or non-printable character (U+%04X); write the escape character as \\e or \\033", r)
		case r == '\\':
			if i+1 == len(rs) {
				return fmt.Errorf("the prompt ends with a lone backslash")
			}
			i++
			n := rs[i]
			switch {
			case n >= '0' && n <= '7':
				// \nnn, up to three octal digits: bash puts the character into the prompt before it expands
				// it, so \044 would be a $ and \140 a backquote. Only the escape character (\033) and the bell
				// (\007) are accepted.
				val := int(n - '0')
				for k := 0; k < 2 && i+1 < len(rs) && rs[i+1] >= '0' && rs[i+1] <= '7'; k++ {
					i++
					val = val*8 + int(rs[i]-'0')
				}
				if val != 0o33 && val != 0o7 {
					return fmt.Errorf("the octal escape \\%03o in the prompt is not accepted: only \\033 (escape) and \\007 (bell)", val)
				}
			case n == '\\':
				// a backslash put into the prompt would quote what follows when bash expands it: \\\$(cmd)
				// would run cmd
				return fmt.Errorf("the prompt may not contain \\\\ (a literal backslash)")
			case n == 'D':
				if i+1 >= len(rs) || rs[i+1] != '{' {
					return fmt.Errorf("\\D in the prompt must be followed by {format}")
				}
				end := -1
				for k := i + 2; k < len(rs); k++ {
					if rs[k] == '}' {
						end = k
						break
					}
					if rs[k] == '\\' || rs[k] == '\'' || rs[k] == '`' || rs[k] == '$' || unicode.IsControl(rs[k]) {
						return fmt.Errorf("the format of \\D{...} in the prompt may hold only printable characters other than \\, ', ` and $")
					}
				}
				if end < 0 {
					return fmt.Errorf("\\D{ in the prompt is not closed with }")
				}
				i = end
			case strings.ContainsRune(promptEscapes, n):
			default:
				return fmt.Errorf("\\%c is not a prompt escape bash knows (\\u user, \\h host, \\w directory, \\$ # or $, \\e escape, \\[ \\] around colours, ...)", n)
			}
		}
	}
	return nil
}

// EffectivePS1 is the PS1 text a prompt value stands for: DefaultPS1(color) for default, "" for none and for an
// empty value, the text itself otherwise.
func EffectivePS1(prompt string, color int) string {
	switch prompt {
	case PromptDefault:
		return DefaultPS1(color)
	case PromptNone, "":
		return ""
	}
	return prompt
}

// ShellSetupRequest is the body of POST /api/v1/jails/{name}/shell-setup (extension jail_shell_setup). It has no
// fields yet: the jail's shell and prompt keys say what is set up, and the host's defaults (jails.shell, jails.prompt
// of jailopsd.yaml) fill the keys the jail does not set.
type ShellSetupRequest struct{}

// ShellSetupResult is the result of the shell-setup operation.
type ShellSetupResult struct {
	Jail string `json:"jail"`
	// Shell is the shell `jailops shell` and the console start; Prompt the jail's prompt key (default, none or a PS1
	// text) and PS1 the text written into PromptFile, empty for none.
	Shell  string `json:"shell"`
	Prompt string `json:"prompt"`
	PS1    string `json:"ps1,omitempty"`
	// Steps lists what was done, in order; a step that found the jail as it should be says so.
	Steps []string `json:"steps"`
}
