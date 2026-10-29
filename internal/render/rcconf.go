package render

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// RcVar is one rc.conf variable to set.
type RcVar struct {
	Name, Value string
}

var rcNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RcConfSet returns existing (the content of a jail's /etc/rc.conf) with
// every variable of set assigned: a line that already assigns the variable is
// replaced in place (later duplicates are dropped), the others are appended.
// Names are rc.conf variable names, values one printable line; a value is
// written double-quoted with the four characters sh interprets inside double
// quotes escaped, so no value is ever read as shell syntax. Other lines of the
// file, comments included, are kept as they are.
func RcConfSet(existing string, set []RcVar) (string, error) {
	want := map[string]string{}
	var order []string
	for _, v := range set {
		if !rcNameRE.MatchString(v.Name) {
			return "", fmt.Errorf("render: %q is not an rc.conf variable name", v.Name)
		}
		if _, dup := want[v.Name]; dup {
			return "", fmt.Errorf("render: rc.conf variable %s is set twice", v.Name)
		}
		for _, r := range v.Value {
			if !unicode.IsPrint(r) {
				return "", fmt.Errorf("render: the value of %s holds a control or non-printable character", v.Name)
			}
		}
		want[v.Name] = v.Value
		order = append(order, v.Name)
	}
	var out []string
	done := map[string]bool{}
	var lines []string
	if body := strings.TrimRight(existing, "\n"); body != "" {
		lines = strings.Split(body, "\n")
	}
	for _, l := range lines {
		name, ok := rcAssignedName(l)
		if val, set := want[name]; ok && set {
			if done[name] {
				continue
			}
			out = append(out, rcLine(name, val))
			done[name] = true
			continue
		}
		out = append(out, l)
	}
	for _, n := range order {
		if !done[n] {
			out = append(out, rcLine(n, want[n]))
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

// rcAssignedName returns the variable a line assigns ("name=..."), if any.
func rcAssignedName(line string) (string, bool) {
	t := strings.TrimLeft(line, " \t")
	i := strings.IndexByte(t, '=')
	if i <= 0 || !rcNameRE.MatchString(t[:i]) {
		return "", false
	}
	return t[:i], true
}

var rcEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")

func rcLine(name, value string) string {
	return name + `="` + rcEscaper.Replace(value) + `"`
}
