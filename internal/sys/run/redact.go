package run

import (
	"strings"
)

var secretWords = []string{"password", "passwd", "secret", "token", "passphrase", "key", "psk"}

func secretName(s string) bool {
	s = strings.ToLower(strings.TrimLeft(s, "-"))
	for _, w := range secretWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// RedactArgs returns a copy of args that is safe to log. A value is masked
// when it is written name=value with a secret-looking name, or when it
// follows a flag whose name looks secret (--password hunter2). Encryption
// key locations such as keylocation=file:///... are masked as well.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	mask := false
	for i, a := range args {
		switch {
		case mask:
			out[i] = "***"
			mask = false
		case strings.HasPrefix(a, "-") && !strings.Contains(a, "="):
			out[i] = a
			mask = secretName(a)
		default:
			if k, _, ok := strings.Cut(a, "="); ok && secretName(k) {
				out[i] = k + "=***"
			} else {
				out[i] = a
			}
		}
	}
	return out
}

// Redact renders c as a single log line with secrets masked and arguments
// quoted where needed. The line is for humans only; it is never executed.
func Redact(c Cmd) string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, quote(c.Name))
	for _, a := range RedactArgs(c.Args) {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s == "" {
		return `''`
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,*", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
