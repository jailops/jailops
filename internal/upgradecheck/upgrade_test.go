package upgradecheck

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/internal/backupd"
	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const fixtureRoot = "../store/testdata/upgrade"

// expect is expect.json of a fixture: what the release put into it.
type expect struct {
	Version       string                       `json:"version"`
	OldSchema     bool                         `json:"old_schema"`
	SchemaVersion int                          `json:"schema_version"`
	Objects       map[string]map[string]string `json:"objects"`
	Jails         []string                     `json:"jails"`
	Project       string                       `json:"project"`
}

func versions(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatalf("no upgrade fixtures: %v (scripts/make-upgrade-fixture.sh VERSION makes one)", err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "v") {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return semver(out[i]) < semver(out[j]) })
	if len(out) == 0 {
		t.Fatal("no upgrade fixtures")
	}
	return out
}

// semver orders vX.Y.Z as one number.
func semver(v string) int {
	n := 0
	for _, p := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
		i, _ := strconv.Atoi(p)
		n = n*1000 + i
	}
	return n
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// tree is every regular file under dir with its bytes: what a second open must leave alone.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dropped lists the keys of orig (a stored document) that are missing from now (the same document after the current
// type decoded and wrote it): data the current code does not know and so silently loses. Zero values do not count:
// omitempty writes them back as nothing.
func dropped(orig, now any, path string) []string {
	var out []string
	switch o := orig.(type) {
	case map[string]any:
		n, _ := now.(map[string]any)
		keys := make([]string, 0, len(o))
		for k := range o {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := o[k]
			nv, ok := n[k]
			if !ok {
				if !isZero(v) {
					out = append(out, path+"."+k)
				}
				continue
			}
			out = append(out, dropped(v, nv, path+"."+k)...)
		}
	case []any:
		n, _ := now.([]any)
		for i, v := range o {
			if i >= len(n) {
				if !isZero(v) {
					out = append(out, path+"["+strconv.Itoa(i)+"]")
				}
				continue
			}
			out = append(out, dropped(v, n[i], path+"["+strconv.Itoa(i)+"]")...)
		}
	}
	return out
}

func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		for _, e := range x {
			if !isZero(e) {
				return false
			}
		}
		return true
	}
	return false
}

func str(v any) string { s, _ := v.(string); return s }

func at(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

// keyFields checks, per object, what every release put into the fixture and the current code must still say.
func keyFields(t *testing.T, e expect, kind api.Kind, name string, spec map[string]any) {
	t.Helper()
	bad := func(what string, got any) { t.Errorf("%s %q: %s is %v", kind, name, what, got) }
	switch {
	case kind == api.KindNetwork && name == "lan":
		if at(spec, "ipv4", "subnet") != "10.77.0.0/24" || at(spec, "ipv4", "nat") != true {
			bad("ipv4", at(spec, "ipv4"))
		}
		// the proxy mode of an IPv6 network became the NDP proxy switch (0.8.4); a new release says it directly
		if at(spec, "ipv6", "ndp_proxy") != true {
			bad("ipv6.ndp_proxy (the stored mode was proxy)", at(spec, "ipv6"))
		}
		if _, has := at(spec, "ipv6").(map[string]any)["mode"]; has {
			bad("ipv6.mode (it must be gone)", at(spec, "ipv6"))
		}
	case kind == api.KindNetwork && name == "routed":
		v6, _ := at(spec, "ipv6").(map[string]any)
		if v6 == nil || v6["subnet"] != "2001:db8:78:1::/64" || v6["ndp_proxy"] == true {
			bad("ipv6 (a routed network stays routed)", v6)
		}
		if _, has := v6["mode"]; has {
			bad("ipv6.mode (it must be gone)", v6)
		}
	case kind == api.KindWireGuard:
		if at(spec, "disabled") != true {
			bad("disabled (the stored tunnel was down)", spec)
		}
		if _, has := spec["down"]; has {
			bad("down (it must be gone)", spec)
		}
	case kind == api.KindImage:
		if _, has := spec["aliases"]; has {
			bad("aliases (image aliases are gone)", spec["aliases"])
		}
		if spec["release"] != "15.1-RELEASE" || at(spec, "source", "type") != "pkgbase" {
			bad("release/source", spec)
		}
	case kind == api.KindFirewallPolicy && name == "web-access":
		rules, _ := spec["rules"].([]any)
		if len(rules) == 0 {
			bad("rules", rules)
			return
		}
		r0, _ := rules[0].(map[string]any)
		if r0["action"] != "allow" || r0["direction"] != "in" || !reflect.DeepEqual(r0["ports"], []any{"443"}) || !reflect.DeepEqual(r0["from"], []any{"set:office"}) {
			bad("the first rule", r0)
		}
		// a redirect rule of 0.8.3 and before is not a policy rule any more: it became an exposure of the jails that used it
		if len(rules) != 1 {
			bad("rules (a redirect rule must have left the policy)", rules)
		}
		for _, x := range rules {
			if _, has := x.(map[string]any)["redirect"]; has {
				bad("redirect (it must be gone)", x)
			}
		}
	case kind == api.KindFirewallPolicy && name == "floor":
		if spec["default_in"] != "deny" || spec["default_out"] != "allow" {
			bad("defaults", spec)
		}
	case kind == api.KindAddressSet:
		if a, _ := spec["addresses"].([]any); len(a) != 2 {
			bad("addresses", spec["addresses"])
		}
	case kind == api.KindVolume:
		if spec["uid"] != float64(1001) || spec["gid"] != float64(1001) || spec["mode"] != "0750" || spec["size"] != "10G" {
			bad("volume", spec)
		}
	case kind == api.KindProject:
		if !reflect.DeepEqual(spec["firewall_policies"], []any{"floor"}) || !reflect.DeepEqual(spec["images"], []any{"freebsd-15-1"}) {
			bad("project", spec)
		}
	}
}

func TestUpgradeFromEveryReleaseKeepsTheState(t *testing.T) {
	vs := versions(t)
	if vs[0] != "v0.8.0" {
		t.Fatalf("the oldest fixture is %s: the guarantee covers every release since v0.8.0", vs[0])
	}
	for _, v := range vs {
		v := v
		t.Run(v, func(t *testing.T) {
			src := filepath.Join(fixtureRoot, v)
			var e expect
			readJSON(t, filepath.Join(src, "expect.json"), &e)
			dir := t.TempDir()
			copyTree(t, src, dir)
			t.Run("objects", func(t *testing.T) { objectsOf(t, e, dir) })
			t.Run("jails", func(t *testing.T) { jailsOf(t, e, dir) })
			t.Run("database", func(t *testing.T) { databaseOf(t, e, dir) })
			t.Run("configuration", func(t *testing.T) { configOf(t, dir) })
		})
	}
}

func objectsOf(t *testing.T, e expect, dir string) {
	stateDir := filepath.Join(dir, "state")
	s, err := state.Open(stateDir, state.Options{})
	if err != nil {
		t.Fatalf("the current code does not open the state of %s: %v", e.Version, err)
	}
	if rec := s.Recoveries(); len(rec) != 0 {
		t.Errorf("recoveries: %+v", rec)
	}
	for kind, names := range e.Objects {
		for name, uuid := range names {
			o, err := s.Get(api.Kind(kind), uuid)
			if err != nil {
				t.Errorf("%s %q is lost: %v", kind, name, err)
				continue
			}
			if o.Metadata.Name != name || o.APIVersion != api.APIVersion {
				t.Errorf("%s %q: name %q apiVersion %q", kind, name, o.Metadata.Name, o.APIVersion)
			}
			var spec map[string]any
			if err := json.Unmarshal(o.Spec, &spec); err != nil {
				t.Errorf("%s %q: spec: %v", kind, name, err)
				continue
			}
			// the migrated spec must fit the current type exactly: a field the type does not know is data the
			// current code would lose
			if _, err := model.DecodeSpec(api.Kind(kind), spec); err != nil {
				t.Errorf("%s %q of %s does not fit the current type after the migration: %v", kind, name, e.Version, err)
			}
			keyFields(t, e, api.Kind(kind), name, spec)
		}
	}
	fw := s.TakeMigratedForwards()
	wantFw := 0
	if e.OldSchema {
		wantFw = 1 // the redirect rule of the policy
	}
	if len(fw) != wantFw {
		t.Errorf("migrated forwards: %d, want %d: %+v", len(fw), wantFw, fw)
	}
	// the forwards stay recorded on disk until the service has written them to their jails: a restart in between
	// loses nothing. The service says so with ForwardsApplied.
	if _, err := os.Stat(filepath.Join(stateDir, "migrated-forwards.json")); (err == nil) != (wantFw > 0) {
		t.Errorf("the record of the pending forwards: %v (pending %d)", err, wantFw)
	}
	if err := s.ForwardsApplied(); err != nil {
		t.Fatal(err)
	}
	first := tree(t, stateDir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// a second open changes nothing: the migration wrote its result once
	s2, err := state.Open(stateDir, state.Options{})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if again := s2.TakeMigratedForwards(); len(again) != 0 {
		t.Errorf("the second open migrated forwards again: %+v", again)
	}
	_ = s2.Close()
	second := tree(t, stateDir)
	delete(first, ".lock")
	delete(second, ".lock")
	if !reflect.DeepEqual(first, second) {
		for k := range first {
			if first[k] != second[k] {
				t.Errorf("the second open rewrote %s", k)
			}
		}
	}
}

func jailsOf(t *testing.T, e expect, dir string) {
	for _, name := range e.Jails {
		base := filepath.Join(dir, "jails", e.Project, name)
		raw, err := os.ReadFile(filepath.Join(base, "spec.json"))
		if err != nil {
			t.Fatal(err)
		}
		rec, err := lifecycle.DecodeRecord(raw)
		if err != nil {
			t.Fatalf("jail %s: the current code cannot read the spec.json of %s: %v", name, e.Version, err)
		}
		if rec.Metadata.Name != name || rec.Metadata.Project != e.Project || rec.Metadata.UUID == "" {
			t.Errorf("jail %s: metadata %+v", name, rec.Metadata)
		}
		// the image reference of a record is what upgrades, updates and restores resolve: it must parse with the
		// current grammar ("name" or "name#gN"), whatever the release wrote
		if _, err := validate.ParseImageRef(rec.Spec.Image); err != nil {
			t.Errorf("jail %s of %s: image %q is not a reference the current code accepts: %v", name, e.Version, rec.Spec.Image, err)
		}
		if len(rec.Spec.Network) == 0 || rec.Spec.Network[0].Network != "lan" || rec.Spec.Network[0].IPv4 == "" {
			t.Errorf("jail %s: network %+v", name, rec.Spec.Network)
		}
		if len(rec.Spec.Firewall.Policies) == 0 || !rec.Spec.Boot.Autostart {
			t.Errorf("jail %s: firewall %+v boot %+v", name, rec.Spec.Firewall, rec.Spec.Boot)
		}
		if name == "web" {
			if len(rec.Spec.Volumes) != 1 || rec.Spec.Volumes[0].Name != "data" || rec.Spec.Volumes[0].Mount != "/srv/data" {
				t.Errorf("jail web: volumes %+v", rec.Spec.Volumes)
			}
			// a proxy device of 0.8.3 and before is an IPv4 entry of the exposure now, with the same host port
			if len(rec.Spec.Expose) != 1 {
				t.Fatalf("jail web: expose %+v", rec.Spec.Expose)
			}
			x := rec.Spec.Expose[0]
			if e.OldSchema {
				if x.HostPort != 8080 || !reflect.DeepEqual(x.Families, []string{"4"}) || !reflect.DeepEqual(x.Ports, []string{"80"}) {
					t.Errorf("jail web: the proxy device became %+v", x)
				}
			} else if !reflect.DeepEqual(x.Ports, []string{"80", "443"}) || !reflect.DeepEqual(x.Families, []string{"4", "6"}) {
				t.Errorf("jail web: expose %+v", x)
			}
		}
		// nothing the record held may be dropped by reading it with the current type
		var orig map[string]any
		if err := json.Unmarshal(raw, &orig); err != nil {
			t.Fatal(err)
		}
		// the conversion of a stored proxy device is the one intended rewrite of the document
		var conv map[string]any
		if err := json.Unmarshal(mustMarshal(t, rec), &conv); err != nil {
			t.Fatal(err)
		}
		if spec, ok := orig["spec"].(map[string]any); ok {
			delete(spec, "proxy")
		}
		if lost := dropped(orig, conv, ""); len(lost) != 0 {
			t.Errorf("jail %s of %s: the current type drops %v", name, e.Version, lost)
		}
		// bundle.json
		braw, err := os.ReadFile(filepath.Join(base, "bundle.json"))
		if err != nil {
			t.Fatal(err)
		}
		bd, err := lifecycle.DecodeBundle(braw)
		if err != nil {
			t.Fatalf("jail %s: bundle.json of %s: %v", name, e.Version, err)
		}
		if bd.Metadata.Name != name || bd.Image.Name != "freebsd-15-1#g1" || bd.Image.Generation != 1 || bd.Spec.Image == "" {
			t.Errorf("jail %s: bundle %+v", name, bd)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func databaseOf(t *testing.T, e expect, dir string) {
	ctx := context.Background()
	path := filepath.Join(dir, "data", "jailops.db")
	latest := latestSchema(t)
	db, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatalf("the current code does not open the database of %s: %v", e.Version, err)
	}
	v, err := db.Version(ctx)
	if err != nil || v != latest {
		t.Errorf("schema version after the open: %d (%v), want %d", v, err, latest)
	}
	users, err := db.Users.List(ctx)
	if err != nil || len(users) != 1 || users[0].Name != "admin" || !users[0].Global {
		t.Errorf("users: %v %+v", err, users)
	}
	tokens, err := db.Tokens.List(ctx)
	if err != nil || len(tokens) != 1 || tokens[0].Name != "ci" {
		t.Errorf("tokens: %v %+v", err, tokens)
	}
	entries, err := db.Audit.List(ctx, store.AuditFilter{})
	if err != nil || len(entries) != 1 || entries[0].Object != "web" {
		t.Errorf("audit: %v %+v", err, entries)
	}
	if h, err := db.Meta.Get(ctx, "host_uuid"); err != nil || h != "00000000-0000-4000-8000-0000000000a1" {
		t.Errorf("meta host_uuid: %q %v", h, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// a second open is idempotent
	db2, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if v2, _ := db2.Version(ctx); v2 != latest {
		t.Errorf("schema version after the second open: %d", v2)
	}
	_ = db2.Close()
}

func latestSchema(t *testing.T) int {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := db.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func configOf(t *testing.T, dir string) {
	cfg := filepath.Join(dir, "config")
	// every key of the daemon's configuration in the release, and the file it ships: the daemon refuses unknown keys,
	// so a key removed without a conversion would stop the daemon after the upgrade
	kb, err := os.ReadFile(filepath.Join(cfg, "jailopsd-keys.txt"))
	if err != nil {
		t.Fatal(err)
	}
	tree := map[string]any{}
	var paths []string
	for _, p := range strings.Fields(string(kb)) {
		paths = append(paths, p)
	}
	isInterior := func(p string) bool {
		for _, q := range paths {
			if strings.HasPrefix(q, p+".") {
				return true
			}
		}
		return false
	}
	for _, p := range paths {
		parts := strings.Split(p, ".")
		cur := tree
		for i, part := range parts {
			if i == len(parts)-1 {
				if !isInterior(p) {
					cur[part] = nil // null: no value, but the key is read
				} else if _, ok := cur[part]; !ok {
					cur[part] = map[string]any{}
				}
				break
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
	}
	yb, err := yaml.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := knownKeys(yb); err != nil {
		t.Errorf("the daemon does not know a key the release knew: %v\n%s", err, yb)
	}
	if b, err := os.ReadFile(filepath.Join(cfg, "jailopsd.yaml")); err == nil {
		if err := knownKeys(b); err != nil {
			t.Errorf("the daemon does not know a key of jailopsd.yaml of the release: %v", err)
		}
	}
	if p := filepath.Join(cfg, "backupd.yaml"); fileExists(p) {
		if _, err := backupd.LoadConfig(p); err != nil && strings.Contains(err.Error(), "field") && strings.Contains(err.Error(), "not found") {
			t.Errorf("jailops-backupd does not accept backupd.yaml of the release: %v", err)
		}
	}
}

// knownKeys reads a daemon configuration the way the daemon does (an unknown key is an error) without the checks of the
// values: only the keys are under test here.
func knownKeys(b []byte) error {
	var c daemon.Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestADatabaseNewerThanTheBinaryIsRefused(t *testing.T) {
	src := filepath.Join(fixtureRoot, versions(t)[0], "data", "jailops.db")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jailops.db")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// a newer binary migrated it, then the old binary is started again
	db, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bumpUserVersion(t, path, latestSchema(t)+1)
	_, err = store.Open(path, store.Options{})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeVersionSkew {
		t.Fatalf("a database newer than the binary: %v, want version_skew", err)
	}
	// the refusal leaves the schema and the rows as they were (opening a SQLite file may touch its header)
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var ver, users int
	if err := ro.QueryRow("PRAGMA user_version").Scan(&ver); err != nil || ver != latestSchema(t)+1 {
		t.Errorf("schema version after the refusal: %d %v", ver, err)
	}
	if err := ro.QueryRow("SELECT count(*) FROM users").Scan(&users); err != nil || users != 1 {
		t.Errorf("users after the refusal: %d %v", users, err)
	}
}

func TestAnObjectNewerThanTheBinaryIsRefused(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join(fixtureRoot, versions(t)[0]), dir)
	files, _ := filepath.Glob(filepath.Join(dir, "state", "objects", "Network", "*.json"))
	if len(files) == 0 {
		t.Fatal("no network in the fixture")
	}
	b, _ := os.ReadFile(files[0])
	b = bytes.Replace(b, []byte(`"apiVersion": "`+api.APIVersion+`"`), []byte(`"apiVersion": "jailops/v99"`), 1)
	if err := os.WriteFile(files[0], b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(dir, "state"), state.Options{})
	if err == nil {
		_ = s.Close()
		t.Fatal("a state directory with an object of a newer apiVersion opened")
	}
	var ae *api.Error
	if !errors.As(err, &ae) && !strings.Contains(err.Error(), "newer") {
		t.Fatalf("the refusal does not say the object is newer: %v", err)
	}
}

var numbered = regexp.MustCompile(`(?m)^## \[?(\d+\.\d+\.\d+)\]?`)

// Every release in the changelog since 0.8.0, except the newest one (it is tagged after its release pull request is
// merged, and its fixture is made then), has a fixture: adding one is one command at release time.
func TestEveryReleaseHasAFixture(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, m := range numbered.FindAllStringSubmatch(string(b), -1) {
		rel = append(rel, "v"+m[1])
	}
	if len(rel) == 0 {
		t.Fatal("no release in CHANGELOG.md")
	}
	have := map[string]bool{}
	for _, v := range versions(t) {
		have[v] = true
	}
	for _, v := range rel[1:] {
		if semver(v) >= semver("v0.8.0") && !have[v] {
			t.Errorf("release %s has no upgrade fixture: run scripts/make-upgrade-fixture.sh %s and commit internal/store/testdata/upgrade/%s", v, v, v)
		}
	}
}

// bumpUserVersion makes the schema version of a database v, as a newer release's migrations leave it.
func bumpUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(DELETE)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(v)); err != nil {
		t.Fatal(err)
	}
}

// The nightly run of the upgrade on a real machine exists, parses, runs on a schedule and on demand, and pins the actions it
// uses to commits (docs/ci-supply-chain.md); the script it runs is shell that parses.
func TestTheNightlyUpgradeRunIsWired(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/upgrade-e2e.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
				With struct {
					Run string `yaml:"run"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatal(err)
	}
	if _, ok := wf.On["schedule"]; !ok {
		t.Error("no schedule")
	}
	if _, ok := wf.On["workflow_dispatch"]; !ok {
		t.Error("no workflow_dispatch")
	}
	if _, ok := wf.On["pull_request"]; ok {
		t.Error("the nightly run must not run on pull requests")
	}
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	var runsScript bool
	for _, j := range wf.Jobs {
		for _, s := range j.Steps {
			// vmactions/freebsd-vm is the tag ci.yml uses (docs/ci-supply-chain.md)
			if s.Uses != "" && !strings.HasPrefix(s.Uses, "./") && !strings.HasPrefix(s.Uses, "vmactions/freebsd-vm@") && !pinned.MatchString(s.Uses) {
				t.Errorf("%s is not pinned to a commit", s.Uses)
			}
			if strings.Contains(s.With.Run, "scripts/ci-freebsd-upgrade.sh") && strings.Contains(s.With.Run, "-eq 75") {
				runsScript = true
			}
		}
	}
	if !runsScript {
		t.Error("the VM step does not run scripts/ci-freebsd-upgrade.sh and treat exit status 75 as skipped")
	}
}
