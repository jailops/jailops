package state

import (
	"strings"

	"jailops.net/jailops/sdk/api"
)

type selReq struct {
	key    string
	op     string // "=", "!=", "exists", "!exists", "in", "notin"
	values []string
}

// Selector is a parsed label selector. The zero value matches everything.
type Selector struct{ reqs []selReq }

// ParseSelector parses "a=b,c!=d,e,!f,g in (x,y),h notin (z)".
func ParseSelector(s string) (Selector, error) {
	var sel Selector
	s = strings.TrimSpace(s)
	if s == "" {
		return sel, nil
	}
	for _, part := range splitTop(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			return sel, api.Errorf(api.CodeInvalid, "empty requirement in selector %q", s)
		}
		r, err := parseReq(part)
		if err != nil {
			return sel, err
		}
		sel.reqs = append(sel.reqs, r)
	}
	return sel, nil
}

// splitTop splits on commas outside parentheses.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func validKey(k string) bool {
	if k == "" || len(k) > 253 {
		return false
	}
	return !strings.ContainsAny(k, " \t=!(),")
}

func parseReq(p string) (selReq, error) {
	bad := func() (selReq, error) {
		return selReq{}, api.Errorf(api.CodeInvalid, "invalid selector requirement %q", p)
	}
	if i := strings.Index(p, "("); i >= 0 {
		if !strings.HasSuffix(p, ")") {
			return bad()
		}
		head := strings.Fields(p[:i])
		if len(head) != 2 || (head[1] != "in" && head[1] != "notin") || !validKey(head[0]) {
			return bad()
		}
		var vals []string
		for _, v := range strings.Split(p[i+1:len(p)-1], ",") {
			vals = append(vals, strings.TrimSpace(v))
		}
		return selReq{key: head[0], op: head[1], values: vals}, nil
	}
	if i := strings.Index(p, "!="); i >= 0 {
		k, v := strings.TrimSpace(p[:i]), strings.TrimSpace(p[i+2:])
		if !validKey(k) {
			return bad()
		}
		return selReq{key: k, op: "!=", values: []string{v}}, nil
	}
	if i := strings.Index(p, "="); i >= 0 {
		k, v := strings.TrimSpace(p[:i]), strings.TrimSpace(p[i+1:])
		v = strings.TrimPrefix(v, "=")
		if !validKey(k) {
			return bad()
		}
		return selReq{key: k, op: "=", values: []string{strings.TrimSpace(v)}}, nil
	}
	if strings.HasPrefix(p, "!") {
		k := strings.TrimSpace(p[1:])
		if !validKey(k) {
			return bad()
		}
		return selReq{key: k, op: "!exists"}, nil
	}
	if !validKey(p) {
		return bad()
	}
	return selReq{key: p, op: "exists"}, nil
}

// Matches reports whether labels satisfy every requirement.
func (s Selector) Matches(labels map[string]string) bool {
	for _, r := range s.reqs {
		v, ok := labels[r.key]
		switch r.op {
		case "=":
			if !ok || v != r.values[0] {
				return false
			}
		case "!=":
			if ok && v == r.values[0] {
				return false
			}
		case "exists":
			if !ok {
				return false
			}
		case "!exists":
			if ok {
				return false
			}
		case "in":
			if !ok || !contains(r.values, v) {
				return false
			}
		case "notin":
			if ok && contains(r.values, v) {
				return false
			}
		}
	}
	return true
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}
