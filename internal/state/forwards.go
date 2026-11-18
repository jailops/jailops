package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// MigratedForward is a port forward that a stored object held in a form that no longer exists: a redirect rule of a
// firewall policy, or the proxy keys of a profile. It is taken out of the object when the store loads it; the service turns
// it into an entry of the exposure of the jails that had it (jailops expose), so the same ports stay forwarded.
type MigratedForward struct {
	Project string
	// Policy and Rule say which rule of which policy it was; Profile and Device which proxy device of which profile.
	Policy  string
	Rule    int
	Profile string
	Device  string

	Proto    string
	Listen   string
	HostPort int
	JailPort int
	From     []string
	Comment  string
	// Disabled is a redirect rule that was switched off: it forwarded nothing, and the service says so instead of dropping it unseen.
	Disabled bool
}

// forwardsFile holds the forwards taken out of stored objects until the service has written them to the jails: the objects
// are rewritten at load, so without it a stop in between would lose the forwards for good.
const forwardsFile = "migrated-forwards.json"

func (s *Store) forwardsPath() string { return filepath.Join(s.dir, forwardsFile) }

// readPendingForwards loads the forwards a start before this one took out and could not hand to the jails.
func (s *Store) readPendingForwards() error {
	b, err := os.ReadFile(s.forwardsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.forwards)
}

// keepForwards writes the pending forwards (all of them) before a document is rewritten without them.
func (s *Store) keepForwards() error {
	b, err := json.Marshal(s.forwards)
	if err != nil {
		return err
	}
	return atomicWrite(s.forwardsPath(), b, nil)
}

// ForwardsApplied removes the record of the pending forwards: the service has written every one to its jail.
func (s *Store) ForwardsApplied() error {
	if err := os.Remove(s.forwardsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// TakeMigratedForwards returns the forwards the load took out of policies and profiles (and those an earlier start left
// unapplied). They stay recorded on disk until ForwardsApplied.
func (s *Store) TakeMigratedForwards() []MigratedForward {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.forwards
	s.forwards = nil
	return out
}

// extractForwards takes the redirect rules out of a policy document and the proxy keys out of a profile document and returns
// them. It reports whether it changed doc. One-off rewrite of stored objects of releases before the exposure of jails: the
// old names are read here, when the document is loaded, and the document is written back without them.
func extractForwards(doc map[string]any) (out []MigratedForward, changed bool) {
	kind, _ := doc["kind"].(string)
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return nil, false
	}
	meta, _ := doc["metadata"].(map[string]any)
	project, _ := meta["project"].(string)
	name, _ := meta["name"].(string)
	switch api.Kind(kind) {
	case api.KindFirewallPolicy:
		rules, _ := spec["rules"].([]any)
		var keep []any
		for i, r := range rules {
			m, _ := r.(map[string]any)
			rd, has := m["redirect"].(map[string]any)
			if !has {
				keep = append(keep, r)
				continue
			}
			f := MigratedForward{Project: project, Policy: name, Rule: i + 1}
			f.Proto, _ = m["proto"].(string)
			f.Comment, _ = m["comment"].(string)
			f.Listen, _ = rd["listen"].(string)
			f.HostPort = intOf(rd["host_port"])
			f.JailPort = intOf(rd["jail_port"])
			if from, ok := m["from"].([]any); ok {
				for _, x := range from {
					if t, ok := x.(string); ok {
						f.From = append(f.From, t)
					}
				}
			}
			f.Disabled, _ = m["disabled"].(bool)
			out = append(out, f)
			changed = true
		}
		if changed {
			spec["rules"] = keep
		}
	case api.KindProfile:
		cfg, _ := spec["config"].(map[string]any)
		devices := map[string]*MigratedForward{}
		var keys []string
		for k := range cfg {
			if !strings.HasPrefix(k, "proxy.") {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rest := strings.TrimPrefix(k, "proxy.")
			i := strings.LastIndex(rest, ".")
			if i < 0 {
				continue
			}
			dev, field := rest[:i], rest[i+1:]
			f := devices[dev]
			if f == nil {
				f = &MigratedForward{Project: project, Profile: name, Device: dev, Proto: "tcp"}
				devices[dev] = f
			}
			v, _ := cfg[k].(string)
			switch field {
			case "listen":
				if j := strings.LastIndex(v, ":"); j >= 0 {
					f.Listen = strings.Trim(v[:j], "[]")
					f.HostPort, _ = strconv.Atoi(v[j+1:])
				}
			case "jail_port":
				f.JailPort, _ = strconv.Atoi(v)
			case "proto":
				f.Proto = v
			}
			delete(cfg, k)
			changed = true
		}
		var devs []string
		for d := range devices {
			devs = append(devs, d)
		}
		sort.Strings(devs)
		for _, d := range devs {
			if f := devices[d]; f.HostPort > 0 && f.JailPort > 0 {
				out = append(out, *f)
			}
		}
	}
	return out, changed
}

func intOf(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	}
	return 0
}
