// Package model defines the typed content of a jailops-sync fleet repository.
//
// Objects that the API owns (Jail, Network, FirewallPolicy, ...) use the wire
// types of package api. Objects that exist only in the repository (Host,
// HostGroup, JailTemplate, Rollout) are defined here. Every file in the repo
// is one document with the same envelope (File); the spec is decoded strictly
// according to its kind, so a misspelled key is an error, never ignored.
package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// KindProfile is a reusable fragment of a jail spec. It exists in the
// repository only: sync expands it into the jails that list it.
const KindProfile api.Kind = "Profile"

// KindInfo describes one API-backed kind in the repository.
type KindInfo struct {
	Kind api.Kind
	// Dir is the directory name under shared/, hosts/<host>/ and projects/<p>/.
	Dir string
	// HostOnly kinds may not appear under shared/.
	HostOnly bool
	// ProjectLevel kinds belong to a named project (metadata.project or the
	// projects/<project>/ directory); the others are host-level.
	ProjectLevel bool
	// NoDir kinds are loaded from their own place (projects/<name>.yaml).
	NoDir bool
	// Extension is the api_extensions name the server must advertise.
	Extension string
}

// Kinds lists the managed API kinds in dependency order: a kind only refers to
// kinds before it. Create and update run in this order, delete in reverse.
// A project's mandatory firewall policy is the one back reference: the apply
// step creates the project first and sets that field after the policies.
var Kinds = []KindInfo{
	{Kind: api.KindImage, Dir: "images", Extension: "images_pkgbase"},
	{Kind: api.KindNetwork, Dir: "networks", Extension: "networks_vnet"},
	// A tunnel's endpoint and keys belong to one host, so it is declared under hosts/<host>/wireguard/ only.
	{Kind: api.KindWireGuard, Dir: "wireguard", HostOnly: true, Extension: "wireguard"},
	{Kind: api.KindStorage, Dir: "storage", HostOnly: true, Extension: "storage"},
	{Kind: api.KindProject, Dir: "projects", HostOnly: true, NoDir: true, Extension: "projects"},
	{Kind: api.KindAddressSet, Dir: "address-sets", ProjectLevel: true, Extension: "firewall_anchors"},
	{Kind: api.KindFirewallPolicy, Dir: "firewall-policies", ProjectLevel: true, Extension: "firewall_anchors"},
	{Kind: api.KindBackupTarget, Dir: "backup-targets", Extension: "backup_policies"},
	{Kind: api.KindBackupPolicy, Dir: "backup-policies", ProjectLevel: true, Extension: "backup_policies"},
	// A volume is a dataset of one host; a jail that attaches it comes after it.
	{Kind: api.KindVolume, Dir: "volumes", HostOnly: true, ProjectLevel: true, Extension: "volumes"},
	{Kind: api.KindJail, Dir: "jails", HostOnly: true, ProjectLevel: true, Extension: "jails"},
}

// KindOf returns the info of a managed kind.
func KindOf(k api.Kind) (KindInfo, bool) {
	for _, ki := range Kinds {
		if ki.Kind == k {
			return ki, true
		}
	}
	return KindInfo{}, false
}

// Order returns the dependency rank of a managed kind (-1 when unmanaged).
func Order(k api.Kind) int {
	for i, ki := range Kinds {
		if ki.Kind == k {
			return i
		}
	}
	return -1
}

// File is the envelope of every YAML document in the repository.
type File struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       api.Kind       `yaml:"kind"`
	Metadata   api.ObjectMeta `yaml:"metadata"`
	// Hosts selects the hosts a shared object applies to: host names, @group
	// references. Empty means every host. Not allowed outside shared/.
	Hosts []string `yaml:"hosts,omitempty"`
	// Templates lists JailTemplates merged under a Jail's own spec, in order.
	Templates []string `yaml:"templates,omitempty"`
	// Start starts a Jail after the sync creates it. It is never re-applied.
	Start bool `yaml:"start,omitempty"`
	// Build builds the first generation of an Image when the host has none (a fresh host has none, and a jail
	// cannot be created from an image without one). It is never re-applied once a generation exists.
	Build bool `yaml:"build,omitempty"`
	// Recipes are applied to a Jail, in order, once it exists and runs: configuration inside the jail.
	Recipes []RecipeRef `yaml:"recipes,omitempty"`
	// Needs lists jails of the same host and project whose recipes must be settled before this jail's recipes run.
	Needs []string `yaml:"needs,omitempty"`
	// Vars are values for the variables of every recipe of the Jail that declares the name.
	Vars map[string]string `yaml:"vars,omitempty"`
	Spec map[string]any    `yaml:"spec"`
}

// Meta is fleet.yaml.
type Meta struct {
	// ID identifies the fleet. Objects created by sync carry
	// managed_by: sync:<id>, and only those are ever changed or deleted.
	ID string `yaml:"id"`
	// Hub, when set, is the hub through which hosts with via_hub are reached.
	Hub *Endpoint `yaml:"hub,omitempty"`
	// DriftInterval is how often sync looks inside a running jail again to see whether a recipe still holds
	// (a duration such as 30m; default 1h). A manual plan or drift always looks.
	DriftInterval string `yaml:"drift_interval,omitempty"`
}

// Endpoint says how to reach a daemon.
type Endpoint struct {
	URL         string `yaml:"url,omitempty" json:"url,omitempty"`
	Socket      string `yaml:"socket,omitempty" json:"socket,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	// TokenSecret names secrets/<name>.age holding the API token.
	TokenSecret string `yaml:"token_secret,omitempty" json:"token_secret,omitempty"`
}

// HostSpec is the spec of a Host document: how to reach one host.
type HostSpec struct {
	Endpoint
	// ViaHub reaches the host through the fleet's hub (?host=<name>).
	ViaHub bool `json:"via_hub,omitempty"`
}

// HostGroupSpec selects hosts by name, by other groups and by label.
type HostGroupSpec struct {
	// Members are host names or @group references. Cycles are rejected.
	Members []string `json:"members,omitempty"`
	// Selector adds every host whose labels contain all of these.
	Selector map[string]string `json:"selector,omitempty"`
}

// RecipeRef names a recipe directory of the repository for a jail and the values of its variables.
type RecipeRef struct {
	// Path is the recipe directory (recipe.yaml and the files beside it), relative to the repository root.
	Path string `yaml:"path"`
	// Vars are the values of the plain variables of the recipe.
	Vars map[string]string `yaml:"vars,omitempty"`
	// Secrets give the secret variables of the recipe as references into the secret store (secrets/NAME.age, or a
	// dotted path of the SOPS file). A secret is never written in a fleet file.
	Secrets map[string]string `yaml:"secrets,omitempty"`
}

// TemplateSpec is the spec of a JailTemplate document.
type TemplateSpec struct {
	Comment string `json:"comment,omitempty"`
	// Jail is a fragment of a JailSpec merged under every jail using the template.
	Jail map[string]any `json:"jail,omitempty"`
}

// RolloutSpec is the spec of a Rollout document: a staged operation over jails.
type RolloutSpec struct {
	Comment string `json:"comment,omitempty"`
	// Hosts limits the hosts; empty means every host.
	Hosts []string `json:"hosts,omitempty"`
	// Project is the project whose jails are selected. Required.
	Project string `json:"project,omitempty"`
	// Selector matches jails by label (all must match). Required.
	Selector map[string]string `json:"selector"`
	// Action is "upgrade" (the only action).
	Action string `json:"action,omitempty"`
	// Canary is the number of jails upgraded first, alone. Default 1.
	Canary int `json:"canary,omitempty"`
	// BatchSize is the size of every later batch. Default 1.
	BatchSize int `json:"batch_size,omitempty"`
	// Pause waits between batches, e.g. "30s".
	Pause string `json:"pause,omitempty"`
	// HealthGate must pass after every batch before the next one starts.
	HealthGate HealthGate `json:"health_gate,omitempty"`
}

// HealthGate says when a batch counts as healthy.
type HealthGate struct {
	// Timeout is how long to wait for the batch to become healthy. Default 60s.
	Timeout string `json:"timeout,omitempty"`
	// Interval is the poll interval. Default 2s.
	Interval string `json:"interval,omitempty"`
}

// Defaults and parsed durations of a rollout.
func (r RolloutSpec) Durations() (pause, timeout, interval time.Duration, err error) {
	parse := func(s string, def time.Duration, what string) (time.Duration, error) {
		if s == "" {
			return def, nil
		}
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("invalid %s %q", what, s)
		}
		return d, nil
	}
	if pause, err = parse(r.Pause, 0, "pause"); err != nil {
		return
	}
	if timeout, err = parse(r.HealthGate.Timeout, 60*time.Second, "health_gate.timeout"); err != nil {
		return
	}
	interval, err = parse(r.HealthGate.Interval, 2*time.Second, "health_gate.interval")
	if err == nil && interval == 0 {
		interval = 2 * time.Second
	}
	return
}

// Validate checks the rollout spec.
func (r RolloutSpec) Validate() error {
	if len(r.Selector) == 0 {
		return fmt.Errorf("selector is required (a rollout never targets every jail implicitly)")
	}
	if r.Action != "" && r.Action != "upgrade" {
		return fmt.Errorf("unsupported action %q (only upgrade)", r.Action)
	}
	if r.Canary < 0 || r.BatchSize < 0 {
		return fmt.Errorf("canary and batch_size must not be negative")
	}
	_, _, _, err := r.Durations()
	return err
}

// CheckName enforces the API's name rule (api.NamePattern): lowercase letters, digits and hyphens, starting with
// a letter.
func CheckName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if !api.IsName(name, api.MaxName) {
		return fmt.Errorf("name %q must use lowercase letters, digits and hyphens, start with a letter, not end in a hyphen, at most %d characters", name, api.MaxName)
	}
	return nil
}

// CheckSimpleName is the stricter rule for hosts, groups and templates, which
// appear in references: no dots.
func CheckSimpleName(name string) error {
	if err := CheckName(name); err != nil {
		return err
	}
	for _, c := range name {
		if c == '.' {
			return fmt.Errorf("name %q must not contain '.'", name)
		}
	}
	return nil
}

// DecodeStrict decodes a generic map into v and rejects unknown keys.
func DecodeStrict(raw map[string]any, v any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("spec is not representable: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// DecodeSpec decodes the spec of an API-backed kind into its typed form. A spec written for an earlier release is
// read as the current shapes (see state.UpgradeSpec); DecodeSpecNotes also says what it rewrote.
func DecodeSpec(kind api.Kind, raw map[string]any) (any, error) {
	spec, _, err := DecodeSpecNotes(kind, raw)
	return spec, err
}

// DecodeSpecNotes is DecodeSpec that also returns what it rewrote because the spec had the shapes of an earlier release,
// one sentence each, so that the administrator can change the file once.
func DecodeSpecNotes(kind api.Kind, raw map[string]any) (any, []string, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	// the rewrite is made on a copy: the caller's document stays as it was read
	if b, err := json.Marshal(raw); err == nil {
		var cp map[string]any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if dec.Decode(&cp) == nil {
			raw = cp
		}
	}
	notes := api.UpgradeSpec(kind, raw)
	if kind == api.KindFirewallPolicy {
		// a redirect rule of a policy is an exposure of a jail now: a policy does not name the jails that use it, so the file
		// cannot be rewritten on its own: say what to write instead of "unknown field"
		if rules, _ := raw["rules"].([]any); len(rules) > 0 {
			for i, r := range rules {
				if m, _ := r.(map[string]any); m != nil {
					if _, has := m["redirect"]; has {
						return nil, notes, fmt.Errorf("rules[%d]: a redirect rule is an exposure of a jail now: delete the rule and write expose: [{proto: tcp, ports: [\"80\"], families: [\"4\"], host_port: 8080}] in the spec of each jail that used the policy (docs/exposure.md)", i)
					}
				}
			}
		}
	}
	spec, err := decodeSpec(kind, raw)
	return spec, notes, err
}

func decodeSpec(kind api.Kind, raw map[string]any) (any, error) {
	switch kind {
	case api.KindJail:
		var s api.JailSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindImage:
		var s api.ImageSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindNetwork:
		var s api.NetworkSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindFirewallPolicy:
		var s api.FirewallPolicySpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindAddressSet:
		var s api.AddressSetSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindVolume:
		var s api.VolumeSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindBackupPolicy:
		var s api.BackupPolicySpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindBackupTarget:
		var s api.BackupTargetSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindProject:
		var s api.ProjectSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindStorage:
		var s api.StorageSpec
		err := DecodeStrict(raw, &s)
		return s, err
	case api.KindWireGuard:
		var s api.WireGuardSpec
		err := DecodeStrict(raw, &s)
		return s, err
	}
	return nil, fmt.Errorf("kind %q is not managed by jailops-sync", kind)
}

// deviceLists are the jail spec lists whose elements are named devices.
// Merging two specs merges these lists name by name (a later entry overrides
// only the keys it sets) instead of replacing the whole list.
var deviceLists = map[string]bool{"network": true, "volumes": true, "devfs": true}

// deviceKey is the key an element of a device list is named by: the volumes of a jail by the volume they attach.
func deviceKey(list string) string {
	if list == "volumes" {
		return "volume"
	}
	return "name"
}

// MergeSpec merges a jail spec fragment over a base: maps merge, null
// deletes, device lists (network, volumes, devfs) merge by device
// name, every other value replaces. Unnamed network devices are named
// eth0, eth1, ... by position first. dst is modified and returned.
func MergeSpec(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		switch sv := v.(type) {
		case nil:
			delete(dst, k)
		case map[string]any:
			dm, _ := dst[k].(map[string]any)
			dst[k] = MergeSpec(cloneMap(dm), sv)
		case []any:
			if deviceLists[k] {
				dst[k] = mergeDevices(k, dst[k], sv)
			} else {
				dst[k] = v
			}
		default:
			dst[k] = v
		}
	}
	return dst
}

func nameDevices(kind string, l []any) []any {
	out := make([]any, 0, len(l))
	n := 0
	for _, e := range l {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		m = cloneMap(m)
		if name, _ := m[deviceKey(kind)].(string); name == "" && kind == "network" {
			m["name"] = autoName + "eth" + strconv.Itoa(n)
		}
		if kind == "network" {
			n++
		}
		out = append(out, m)
	}
	return out
}

// autoName marks a device name assigned by position while merging; it is
// stripped again so the server's own naming stays authoritative.
const autoName = "~auto:"

func sameName(a any, b string) bool {
	as, _ := a.(string)
	return strings.TrimPrefix(as, autoName) == strings.TrimPrefix(b, autoName)
}

// StripAutoNames removes device names that MergeSpec only assigned by position.
func StripAutoNames(spec map[string]any) {
	for k := range deviceLists {
		l, _ := spec[k].([]any)
		for _, e := range l {
			if m, ok := e.(map[string]any); ok {
				if n, _ := m["name"].(string); strings.HasPrefix(n, autoName) {
					delete(m, "name")
				}
			}
		}
	}
}

func mergeDevices(kind string, base any, over []any) any {
	b, _ := base.([]any)
	out := nameDevices(kind, b)
	for _, e := range nameDevices(kind, over) {
		m, ok := e.(map[string]any)
		key := deviceKey(kind)
		name, _ := m[key].(string)
		if !ok || name == "" {
			out = append(out, e)
			continue
		}
		found := false
		for i, x := range out {
			if xm, ok := x.(map[string]any); ok && sameName(xm[key], name) {
				merged := Merge(xm, m)
				if xn, _ := xm[key].(string); !strings.HasPrefix(xn, autoName) {
					merged[key] = xn
				}
				out[i] = merged
				found = true
				break
			}
		}
		if !found {
			out = append(out, m)
		}
	}
	return out
}

// Merge applies src over dst as a JSON merge patch (RFC 7386): maps merge,
// everything else replaces, null deletes. dst is modified and returned.
func Merge(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		switch sv := v.(type) {
		case nil:
			delete(dst, k)
		case map[string]any:
			dm, _ := dst[k].(map[string]any)
			dst[k] = Merge(cloneMap(dm), sv)
		default:
			dst[k] = v
		}
	}
	return dst
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if mm, ok := v.(map[string]any); ok {
			out[k] = cloneMap(mm)
		} else {
			out[k] = v
		}
	}
	return out
}

// CloneMap deep-copies the map parts of a generic value tree.
func CloneMap(m map[string]any) map[string]any { return cloneMap(m) }

// ToMap converts a typed value into its generic JSON form (snake_case keys, omitempty applied).
func ToMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
