package state

import (
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Migration upgrades a decoded document from one apiVersion to the next. Fn
// edits the generic map in place and must not set apiVersion; the framework does.
type Migration struct {
	From string
	To   string
	Fn   func(doc map[string]any) error
}

// versionNumber parses "jailops/v<N>".
func versionNumber(v string) (int, bool) {
	rest, ok := strings.CutPrefix(v, "jailops/v")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

// migrate brings doc to api.APIVersion. It returns true if the document was
// changed and should be rewritten. Documents newer than known are refused.
func migrate(doc map[string]any, migs []Migration) (bool, error) {
	return migrateTo(doc, migs, api.APIVersion)
}

func migrateTo(doc map[string]any, migs []Migration, target string) (bool, error) {
	v, _ := doc["apiVersion"].(string)
	if v == target {
		return false, nil
	}
	cur, _ := versionNumber(target)
	n, ok := versionNumber(v)
	if !ok {
		return false, api.Errorf(api.CodeInvalid, "unrecognised apiVersion %q", v)
	}
	if n > cur {
		return false, api.Errorf(api.CodeVersionSkew, "document apiVersion %q is newer than this binary understands (%s)", v, target).
			WithHint("upgrade jailopsd before opening this state directory")
	}
	for steps := 0; v != target; steps++ {
		if steps > 64 {
			return false, api.Errorf(api.CodeInternal, "migration loop from %q", v)
		}
		var m *Migration
		for i := range migs {
			if migs[i].From == v {
				m = &migs[i]
				break
			}
		}
		if m == nil {
			return false, api.Errorf(api.CodeInvalid, "no migration from apiVersion %q", v)
		}
		if err := m.Fn(doc); err != nil {
			return false, api.Errorf(api.CodeInternal, "migrate %s to %s: %v", m.From, m.To, err)
		}
		doc["apiVersion"] = m.To
		v = m.To
	}
	return true, nil
}
