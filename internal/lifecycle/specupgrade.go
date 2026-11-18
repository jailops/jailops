package lifecycle

import (
	"encoding/json"
	"strconv"
	"strings"
)

// upgradeSpec rewrites the proxy devices of a spec.json or bundle.json written before the exposure of jails
// (spec.expose) into expose entries: a proxy device forwarded one IPv4 port, so it becomes one IPv4 entry with a host port.
// A document without proxy devices is returned as it is.
func upgradeSpec(b []byte) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(b, &doc) != nil {
		return b
	}
	var spec map[string]json.RawMessage
	if json.Unmarshal(doc["spec"], &spec) != nil {
		return b
	}
	raw, ok := spec["proxy"]
	if !ok {
		return b
	}
	var devs []struct {
		Name     string `json:"name"`
		Listen   string `json:"listen"`
		JailPort int    `json:"jail_port"`
		Proto    string `json:"proto"`
	}
	if json.Unmarshal(raw, &devs) != nil {
		return b
	}
	delete(spec, "proxy")
	var entries []map[string]any
	if cur, ok := spec["expose"]; ok {
		_ = json.Unmarshal(cur, &entries)
	}
	for _, d := range devs {
		if d.JailPort <= 0 {
			continue
		}
		proto := d.Proto
		if proto == "" {
			proto = "tcp"
		}
		e := map[string]any{"proto": proto, "ports": []string{strconv.Itoa(d.JailPort)}, "families": []string{"4"},
			"comment": "migrated from proxy " + d.Name}
		if i := strings.LastIndex(d.Listen, ":"); i >= 0 {
			if hp, err := strconv.Atoi(d.Listen[i+1:]); err == nil && hp != d.JailPort {
				e["host_port"] = hp
			}
			if l := strings.Trim(d.Listen[:i], "[]"); l != "" {
				e["listen"] = l
			}
		}
		entries = append(entries, e)
	}
	if len(entries) > 0 {
		spec["expose"], _ = json.Marshal(entries)
	}
	doc["spec"], _ = json.Marshal(spec)
	out, err := json.Marshal(doc)
	if err != nil {
		return b
	}
	return out
}
