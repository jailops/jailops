// Package pfanchor holds the pure parts of prototype 3: ruleset rendering, anchor name and
// pfctl argument guards, and classification of `pfctl -sr` output. Nothing here was run on
// FreeBSD by its author.
package pfanchor

import (
	"fmt"
	"regexp"
	"strings"
)

// Prefix is the only anchor namespace the prototype may touch.
const Prefix = "jailops/jop3"

var anchorRe = regexp.MustCompile(`^jailops/jop3[a-z0-9_]{1,24}$`)

// ValidAnchor reports whether name is an anchor this prototype may modify.
func ValidAnchor(name string) bool { return anchorRe.MatchString(name) }

// GuardPfctl refuses any pfctl invocation that could change state outside a valid prototype
// anchor. Read-only queries (-s ...) without -a are allowed. Anything that loads (-f), flushes (-F),
// edits tables (-T), enables/disables (-e/-d), or kills states (-k) needs -a <valid anchor>,
// and -f must read stdin ("-") so pf.conf is never loaded by accident.
func GuardPfctl(args []string) error {
	anchor := ""
	mutating := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-a":
			if i+1 >= len(args) {
				return fmt.Errorf("pfctl: -a without anchor")
			}
			anchor = args[i+1]
			i++
		case "-F", "-T", "-k", "-K", "-e", "-d", "-O", "-R", "-z":
			mutating = true
		case "-f":
			mutating = true
			if i+1 >= len(args) || args[i+1] != "-" {
				return fmt.Errorf("pfctl: -f must read stdin, got %v", args)
			}
		}
		if a == "-e" || a == "-d" {
			return fmt.Errorf("pfctl: refusing to enable or disable pf: %v", args)
		}
	}
	if mutating {
		if !ValidAnchor(anchor) {
			return fmt.Errorf("pfctl: mutating command needs -a %s*, got %q in %v", Prefix, anchor, args)
		}
	} else if anchor != "" && !strings.HasPrefix(anchor, "jailops") {
		return fmt.Errorf("pfctl: query of foreign anchor %q", anchor)
	}
	return nil
}

// Marker labels let a reader tell which ruleset version is loaded.
const (
	LabelV1 = "jop3-v1"
	LabelV2 = "jop3-v2"
)

// Render returns the ruleset text for a version ("v1" or "v2"). Addresses are RFC 5737
// documentation addresses, so the rules cannot affect real traffic.
func Render(version string) string {
	var b strings.Builder
	b.WriteString("table <jop3_set> persist { 192.0.2.10 }\n")
	// translation rules come before filter rules in a pf ruleset
	b.WriteString("rdr pass on lo0 proto tcp from any to 192.0.2.50 port 8080 -> 127.0.0.1 port 80\n")
	switch version {
	case "v1":
		b.WriteString(`block drop quick proto tcp from 192.0.2.1 to 192.0.2.2 port 9 label "` + LabelV1 + `"` + "\n")
		b.WriteString("pass quick proto tcp from <jop3_set> to 192.0.2.3 port 9\n")
	case "v2":
		b.WriteString(`block drop quick proto tcp from 192.0.2.4 to 192.0.2.5 port 9 label "` + LabelV2 + `"` + "\n")
		b.WriteString("pass quick proto tcp from <jop3_set> to 192.0.2.6 port 9\n")
	default:
		panic("unknown version " + version)
	}
	return b.String()
}

// Broken is a ruleset that must fail validation: a syntax error.
const Broken = "block drop quick proto tcp from 192.0.2.7 to 192.0.2.8 port 9 label \"jop3-bad\"\nthis is not pf\n"

// BrokenSemantic references a table that is not defined in the anchor.
const BrokenSemantic = "pass quick proto tcp from <jop3_undefined> to 192.0.2.9 port 9\n"

// State classifies filter rule output.
type State string

// Classification results.
const (
	StateEmpty State = "empty"
	StateV1    State = "v1"
	StateV2    State = "v2"
	StateMixed State = "mixed"
	StateOther State = "other"
)

// Classify says which version a `pfctl -a ... -sr` output shows. Anything with both labels is
// mixed, which would prove a non-atomic replacement.
func Classify(sr string) State {
	sr = strings.TrimSpace(sr)
	v1, v2 := strings.Contains(sr, `"`+LabelV1+`"`), strings.Contains(sr, `"`+LabelV2+`"`)
	switch {
	case v1 && v2:
		return StateMixed
	case v1:
		return StateV1
	case v2:
		return StateV2
	case sr == "":
		return StateEmpty
	}
	return StateOther
}

// PfStatusEnabled parses the first lines of `pfctl -s info`.
func PfStatusEnabled(info string) (enabled, known bool) {
	for _, l := range strings.Split(info, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Status:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				return f[1] == "Enabled", true
			}
		}
	}
	return false, false
}

// AnchorLines reports which of the three administrator lines a pf.conf text contains.
type AnchorLines struct{ NatAnchor, RdrAnchor, Anchor bool }

var lineRe = regexp.MustCompile(`^\s*(nat-anchor|rdr-anchor|anchor)\s+"jailops/\*"`)

// ParseAnchorLines scans pf.conf text.
func ParseAnchorLines(conf string) AnchorLines {
	var r AnchorLines
	for _, l := range strings.Split(conf, "\n") {
		m := lineRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		switch m[1] {
		case "nat-anchor":
			r.NatAnchor = true
		case "rdr-anchor":
			r.RdrAnchor = true
		case "anchor":
			r.Anchor = true
		}
	}
	return r
}

// All reports whether all three lines exist.
func (a AnchorLines) All() bool { return a.NatAnchor && a.RdrAnchor && a.Anchor }

// SplitTable returns the entries of `pfctl -T show` output.
func SplitTable(out string) []string {
	var r []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			r = append(r, l)
		}
	}
	return r
}
