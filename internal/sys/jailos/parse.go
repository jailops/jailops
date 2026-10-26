package jailos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "new", "inherit":
		return true
	}
	return false
}

func fromParams(m map[string]string) (Jail, bool) {
	jid, err := strconv.Atoi(m["jid"])
	if err != nil || jid <= 0 {
		return Jail{}, false
	}
	j := Jail{JID: jid, Name: m["name"], Path: m["path"], Params: map[string]string{}}
	j.Hostname = m["host.hostname"]
	if j.Hostname == "" {
		j.Hostname = m["hostname"]
	}
	j.VNet = truthy(m["vnet"])
	j.Dying = truthy(m["dying"])
	for k, v := range m {
		switch k {
		case "jid", "name", "path", "host.hostname", "hostname", "vnet", "dying":
		default:
			j.Params[k] = v
		}
	}
	if j.Name == "" {
		j.Name = strconv.Itoa(jid)
	}
	return j, true
}

// ParseJLSJSON parses `jls -v --libxo=json`. It accepts the "jail" element
// as a list or a single object, numbers or strings for values, and ignores
// entries without a positive jid.
func ParseJLSJSON(b []byte) ([]Jail, error) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&root); err != nil {
		return nil, api.Errorf(api.CodeInternal, "cannot parse jls json: %v", err)
	}
	info, _ := root["jail-information"].(map[string]any)
	if info == nil {
		return nil, api.Errorf(api.CodeInternal, "cannot parse jls json: no jail-information")
	}
	var entries []any
	switch v := info["jail"].(type) {
	case []any:
		entries = v
	case map[string]any:
		entries = []any{v}
	case nil:
	default:
		return nil, api.Errorf(api.CodeInternal, "cannot parse jls json: unexpected jail element")
	}
	var out []Jail
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		p := make(map[string]string, len(m))
		for k, v := range m {
			if _, nested := v.(map[string]any); nested {
				continue
			}
			if _, list := v.([]any); list {
				continue
			}
			p[k] = scalar(v)
		}
		if j, ok := fromParams(p); ok {
			out = append(out, j)
		}
	}
	return out, nil
}

var kvStart = regexp.MustCompile(`(?:^|\s)([A-Za-z][A-Za-z0-9_.\-]*)=`)

// ParseJLSNamed parses `jls -n` output: one jail per line, name=value pairs
// separated by spaces. Values may contain spaces, so a value runs until the
// next " key=" token. Lines without a valid jid are skipped.
func ParseJLSNamed(s string) []Jail {
	var out []Jail
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := kvStart.FindAllStringSubmatchIndex(line, -1)
		m := map[string]string{}
		for i, loc := range idx {
			key := line[loc[2]:loc[3]]
			end := len(line)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			m[key] = strings.TrimSpace(line[loc[1]:end])
		}
		if j, ok := fromParams(m); ok {
			out = append(out, j)
		}
	}
	return out
}
