package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// UpgradeSpec rewrites the spec of one object, in place, from the shapes of earlier releases to the current ones, and
// says what it rewrote, one sentence each. It is the one function that every place that reads a spec written by an
// earlier release calls: the store when it loads an object, lifecycle when it reads the spec.json of a jail, the fleet
// loader and the CLI when they read the YAML of an administrator. Without it a field that a release renamed or removed
// would make the strict decoders of those places refuse a document that was valid when it was written.
//
// The rewrites, each with the release that made it:
//   - 0.8.4: a network's ipv6.mode is the ndp_proxy switch (`proxy` is `ndp_proxy: true`, `routed` is nothing, `none`
//     is no IPv6 block);
//   - 0.8.4: a WireGuard tunnel's `down` is `disabled`;
//   - 0.8.4: image aliases are gone: the `aliases` of an image are dropped and `image@alias` is the bare image name in a
//     jail and in a project's image list;
//   - 0.8.4: the proxy devices of a jail are IPv4 entries of its exposure (spec.expose).
//
// The redirect rules of firewall policies are not rewritten here: they became exposures of the jails that used the
// policy, which a spec alone does not name (the store does it when it loads the policy).
func UpgradeSpec(kind Kind, spec map[string]any) []string {
	notes := UpgradeFields(kind, spec)
	switch kind {
	case KindJail:
		if ref, _ := spec["image"].(string); strings.Contains(ref, "@") {
			name, _, _ := strings.Cut(ref, "@")
			spec["image"] = name
			notes = append(notes, fmt.Sprintf("the image %q is read as %q (image aliases are gone)", ref, name))
		}
	case KindProject:
		imgs, _ := spec["images"].([]any)
		for i, x := range imgs {
			if ref, _ := x.(string); strings.Contains(ref, "@") {
				name, _, _ := strings.Cut(ref, "@")
				imgs[i] = name
				notes = append(notes, fmt.Sprintf("the image %q is read as %q (image aliases are gone)", ref, name))
			}
		}
	}
	return notes
}

// UpgradeFields is the part of UpgradeSpec that renames or removes fields and needs no more than the spec: the store
// applies it to every object it loads and reads the image references itself (it reports them as events).
func UpgradeFields(kind Kind, spec map[string]any) []string {
	var notes []string
	switch kind {
	case KindNetwork:
		v6, ok := spec["ipv6"].(map[string]any)
		if !ok {
			break
		}
		mode, has := v6["mode"]
		if !has {
			break
		}
		delete(v6, "mode")
		switch mode {
		case "proxy":
			v6["ndp_proxy"] = true
			notes = append(notes, "ipv6.mode proxy is read as ipv6.ndp_proxy: true")
		case "none":
			delete(spec, "ipv6")
			notes = append(notes, "ipv6.mode none is read as no ipv6 block")
		default:
			notes = append(notes, fmt.Sprintf("ipv6.mode %v is dropped (a routed network is the default)", mode))
		}
	case KindWireGuard:
		if v, has := spec["down"]; has {
			delete(spec, "down")
			if _, done := spec["disabled"]; !done {
				spec["disabled"] = v
			}
			notes = append(notes, "down is read as disabled")
		}
	case KindImage:
		if _, has := spec["aliases"]; has {
			delete(spec, "aliases")
			notes = append(notes, "aliases are dropped (image aliases are gone)")
		}
	case KindJail:
		if proxyToExpose(spec) {
			notes = append(notes, "the proxy devices are read as spec.expose entries")
		}
	}
	return notes
}

// proxyToExpose turns the proxy devices of a jail spec into expose entries: a proxy device forwarded one IPv4 port, so
// it becomes one IPv4 entry with a host port. It reports whether it changed the spec.
func proxyToExpose(spec map[string]any) bool {
	raw, ok := spec["proxy"]
	if !ok {
		return false
	}
	delete(spec, "proxy")
	devs, _ := raw.([]any)
	entries, _ := spec["expose"].([]any)
	for _, x := range devs {
		d, _ := x.(map[string]any)
		jailPort := specInt(d["jail_port"])
		if jailPort <= 0 {
			continue
		}
		proto, _ := d["proto"].(string)
		if proto == "" {
			proto = "tcp"
		}
		name, _ := d["name"].(string)
		e := map[string]any{"proto": proto, "ports": []any{strconv.Itoa(jailPort)}, "families": []any{"4"}, "comment": "migrated from proxy " + name}
		listen, _ := d["listen"].(string)
		if i := strings.LastIndex(listen, ":"); i >= 0 {
			if hp, err := strconv.Atoi(listen[i+1:]); err == nil && hp != jailPort {
				e["host_port"] = hp
			}
			if l := strings.Trim(listen[:i], "[]"); l != "" {
				e["listen"] = l
			}
		}
		entries = append(entries, e)
	}
	if len(entries) > 0 {
		spec["expose"] = entries
	}
	return true
}

func specInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case uint64:
		return int(n)
	}
	return 0
}
