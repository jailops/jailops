// Package zfsmodel is an in-memory model of the ZFS semantics JailOps relies
// on: datasets, snapshots, clones with an origin, "cannot destroy snapshot that
// has clones", holds, rename that follows clone origins, rollback that destroys
// later snapshots, written accounting and atomic multi-dataset snapshots. It
// exists so lifecycle code is tested against ZFS's rules without FreeBSD.
//
// The model also holds file contents per dataset so tests can prove that data
// survives or is discarded by a rollback.
package zfsmodel

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"jailops.net/jailops/internal/lifecycle/fault"
)

type snapshot struct {
	name  string
	props map[string]string
	files map[string]string
	cum   int64
	holds map[string]bool
}

type dataset struct {
	name    string
	props   map[string]string
	snaps   []*snapshot
	origin  string // full snapshot name, "" if none
	files   map[string]string
	cum     int64 // cumulative bytes written
	mounted bool
}

// Model is the in-memory pool set. It is safe for concurrent use.
type Model struct {
	mu sync.Mutex
	ds map[string]*dataset
	// F injects failures into mutating operations.
	F *fault.Injector
}

// New returns a model with the given pools (root datasets) already created.
func New(f *fault.Injector, pools ...string) *Model {
	m := &Model{ds: map[string]*dataset{}, F: f}
	for _, p := range pools {
		m.ds[p] = newDS(p)
	}
	return m
}

func newDS(name string) *dataset {
	return &dataset{name: name, props: map[string]string{}, files: map[string]string{}}
}

var compRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

func validName(n string) error {
	base, snap, isSnap := strings.Cut(n, "@")
	if isSnap && !compRe.MatchString(snap) {
		return fmt.Errorf("invalid snapshot name %q", n)
	}
	for _, c := range strings.Split(base, "/") {
		if !compRe.MatchString(c) {
			return fmt.Errorf("invalid dataset name %q", n)
		}
	}
	return nil
}

func parent(n string) string {
	i := strings.LastIndex(n, "/")
	if i < 0 {
		return ""
	}
	return n[:i]
}

func copyMap(m map[string]string) map[string]string {
	o := make(map[string]string, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

func (m *Model) get(n string) (*dataset, error) {
	d, ok := m.ds[n]
	if !ok {
		return nil, fmt.Errorf("dataset does not exist: %s", n)
	}
	return d, nil
}

func (d *dataset) snap(n string) *snapshot {
	for _, s := range d.snaps {
		if s.name == n {
			return s
		}
	}
	return nil
}

func (m *Model) lookupSnap(full string) (*dataset, *snapshot, error) {
	base, sn, ok := strings.Cut(full, "@")
	if !ok {
		return nil, nil, fmt.Errorf("not a snapshot name: %s", full)
	}
	d, err := m.get(base)
	if err != nil {
		return nil, nil, err
	}
	s := d.snap(sn)
	if s == nil {
		return nil, nil, fmt.Errorf("snapshot does not exist: %s", full)
	}
	return d, s, nil
}

func (m *Model) clonesOf(full string) []string {
	var out []string
	for n, d := range m.ds {
		if d.origin == full {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Model) descendants(n string) []string {
	var out []string
	for k := range m.ds {
		if strings.HasPrefix(k, n+"/") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Model) autoMount(d *dataset) {
	cm := d.props["canmount"]
	d.mounted = cm != "off" && cm != "noauto" && d.props["mountpoint"] != "none" && d.props["mountpoint"] != ""
}

// ---- operations (the lifecycle.ZFS and imagegen.ZFS surface) ----

func (m *Model) Exists(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Contains(name, "@") {
		_, _, err := m.lookupSnap(name)
		return err == nil, nil
	}
	_, ok := m.ds[name]
	return ok, nil
}

func (m *Model) Create(_ context.Context, name string, props map[string]string) error {
	if err := m.F.Hit("zfs create " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validName(name); err != nil || strings.Contains(name, "@") {
		return fmt.Errorf("cannot create %q: invalid name", name)
	}
	if _, ok := m.ds[name]; ok {
		return fmt.Errorf("cannot create '%s': dataset already exists", name)
	}
	if _, ok := m.ds[parent(name)]; !ok {
		return fmt.Errorf("cannot create '%s': parent does not exist", name)
	}
	d := newDS(name)
	d.props = copyMap(props)
	m.autoMount(d)
	m.ds[name] = d
	return nil
}

func (m *Model) Clone(_ context.Context, snap, target string, props map[string]string) error {
	if err := m.F.Hit("zfs clone " + snap + " " + target); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.lookupSnap(snap)
	if err != nil {
		return err
	}
	if err := validName(target); err != nil || strings.Contains(target, "@") {
		return fmt.Errorf("cannot create %q: invalid name", target)
	}
	if _, ok := m.ds[target]; ok {
		return fmt.Errorf("cannot create '%s': dataset already exists", target)
	}
	if _, ok := m.ds[parent(target)]; !ok {
		return fmt.Errorf("cannot create '%s': parent does not exist", target)
	}
	d := newDS(target)
	d.props = copyMap(props)
	d.origin = snap
	d.files = copyMap(s.files)
	d.cum = s.cum
	m.autoMount(d)
	m.ds[target] = d
	return nil
}

// Snapshot creates all snapshots or none (one transaction group).
func (m *Model) Snapshot(_ context.Context, props map[string]string, snaps ...string) error {
	if err := m.F.Hit("zfs snapshot " + strings.Join(snaps, " ")); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, full := range snaps {
		base, sn, ok := strings.Cut(full, "@")
		if !ok || validName(full) != nil {
			return fmt.Errorf("cannot create snapshot '%s': invalid name", full)
		}
		d, err := m.get(base)
		if err != nil {
			return err
		}
		if d.snap(sn) != nil || seen[full] {
			return fmt.Errorf("cannot create snapshot '%s': dataset already exists", full)
		}
		seen[full] = true
	}
	for _, full := range snaps {
		base, sn, _ := strings.Cut(full, "@")
		d := m.ds[base]
		d.snaps = append(d.snaps, &snapshot{name: sn, props: copyMap(props), files: copyMap(d.files), cum: d.cum, holds: map[string]bool{}})
	}
	return nil
}

func (m *Model) destroySnapLocked(full string) error {
	d, s, err := m.lookupSnap(full)
	if err != nil {
		return err
	}
	if len(s.holds) > 0 {
		return fmt.Errorf("cannot destroy snapshot %s: dataset is busy (held)", full)
	}
	if c := m.clonesOf(full); len(c) > 0 {
		return fmt.Errorf("cannot destroy '%s': snapshot has dependent clones: %s", full, strings.Join(c, ","))
	}
	for i, x := range d.snaps {
		if x == s {
			d.snaps = append(d.snaps[:i:i], d.snaps[i+1:]...)
			break
		}
	}
	return nil
}

// Destroy removes one snapshot, or one dataset with no snapshots or children.
func (m *Model) Destroy(_ context.Context, name string) error {
	if err := m.F.Hit("zfs destroy " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Contains(name, "@") {
		return m.destroySnapLocked(name)
	}
	d, err := m.get(name)
	if err != nil {
		return err
	}
	if len(d.snaps) > 0 {
		return fmt.Errorf("cannot destroy '%s': filesystem has children (snapshots)", name)
	}
	if len(m.descendants(name)) > 0 {
		return fmt.Errorf("cannot destroy '%s': filesystem has children", name)
	}
	delete(m.ds, name)
	return nil
}

// DestroyRecursive is zfs destroy -r on a dataset: the dataset, its children
// and all their snapshots. It fails, changing nothing, when a snapshot is held
// or has a clone outside the destroyed tree.
func (m *Model) DestroyRecursive(_ context.Context, name string) error {
	if err := m.F.Hit("zfs destroy -r " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(name); err != nil {
		return err
	}
	tree := append([]string{name}, m.descendants(name)...)
	in := map[string]bool{}
	for _, t := range tree {
		in[t] = true
	}
	for _, t := range tree {
		for _, s := range m.ds[t].snaps {
			full := t + "@" + s.name
			if len(s.holds) > 0 {
				return fmt.Errorf("cannot destroy %s: dataset is busy (held)", full)
			}
			for _, c := range m.clonesOf(full) {
				if !in[c] {
					return fmt.Errorf("cannot destroy '%s': snapshot %s has dependent clones: %s", name, full, c)
				}
			}
		}
	}
	for _, t := range tree {
		delete(m.ds, t)
	}
	return nil
}

// Rename renames a dataset with its children; clone origins follow.
func (m *Model) Rename(_ context.Context, from, to string) error {
	if err := m.F.Hit("zfs rename " + from + " " + to); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Contains(from, "@") || strings.Contains(to, "@") {
		return fmt.Errorf("snapshot rename is not modelled")
	}
	if _, err := m.get(from); err != nil {
		return err
	}
	if validName(to) != nil {
		return fmt.Errorf("cannot rename to '%s': invalid name", to)
	}
	if _, ok := m.ds[to]; ok {
		return fmt.Errorf("cannot rename to '%s': dataset already exists", to)
	}
	if _, ok := m.ds[parent(to)]; !ok {
		return fmt.Errorf("cannot rename to '%s': parent does not exist", to)
	}
	if strings.HasPrefix(to, from+"/") {
		return fmt.Errorf("cannot rename to a child of itself")
	}
	moved := append([]string{from}, m.descendants(from)...)
	for _, o := range moved {
		d := m.ds[o]
		delete(m.ds, o)
		nn := to + strings.TrimPrefix(o, from)
		d.name = nn
		m.ds[nn] = d
	}
	for _, d := range m.ds {
		if d.origin == "" {
			continue
		}
		base, sn, _ := strings.Cut(d.origin, "@")
		if base == from || strings.HasPrefix(base, from+"/") {
			d.origin = to + strings.TrimPrefix(base, from) + "@" + sn
		}
	}
	return nil
}

// Rollback restores a dataset to a snapshot. With destroyLater false it fails
// when later snapshots exist; with true it destroys them (they must have no
// clones and no holds).
func (m *Model) Rollback(_ context.Context, snap string, destroyLater bool) error {
	if err := m.F.Hit("zfs rollback " + snap); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s, err := m.lookupSnap(snap)
	if err != nil {
		return err
	}
	idx := 0
	for i, x := range d.snaps {
		if x == s {
			idx = i
		}
	}
	later := d.snaps[idx+1:]
	if len(later) > 0 && !destroyLater {
		return fmt.Errorf("cannot rollback to '%s': more recent snapshots exist; use -r", snap)
	}
	for _, l := range later {
		if len(l.holds) > 0 || len(m.clonesOf(d.name+"@"+l.name)) > 0 {
			return fmt.Errorf("cannot rollback to '%s': snapshot %s is held or has clones", snap, l.name)
		}
	}
	d.snaps = d.snaps[:idx+1]
	d.files = copyMap(s.files)
	d.cum = s.cum
	return nil
}

// Get returns a local property value ("" if not set locally). On a snapshot
// name it returns the snapshot's property.
func (m *Model) Get(_ context.Context, name, prop string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Contains(name, "@") {
		_, s, err := m.lookupSnap(name)
		if err != nil {
			return "", err
		}
		return s.props[prop], nil
	}
	d, err := m.get(name)
	if err != nil {
		return "", err
	}
	return d.props[prop], nil
}

func (m *Model) Set(_ context.Context, name string, props map[string]string) error {
	if err := m.F.Hit("zfs set " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var target map[string]string
	var d *dataset
	if strings.Contains(name, "@") {
		_, s, err := m.lookupSnap(name)
		if err != nil {
			return err
		}
		target = s.props
	} else {
		var err error
		if d, err = m.get(name); err != nil {
			return err
		}
		target = d.props
	}
	for k, v := range props {
		target[k] = v
	}
	if d != nil {
		if _, ok := props["canmount"]; ok {
			if d.props["canmount"] == "off" {
				d.mounted = false
			}
		}
	}
	return nil
}

func (m *Model) Inherit(_ context.Context, name, prop string) error {
	if err := m.F.Hit("zfs inherit " + prop + " " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Contains(name, "@") {
		_, s, err := m.lookupSnap(name)
		if err != nil {
			return err
		}
		delete(s.props, prop)
		return nil
	}
	d, err := m.get(name)
	if err != nil {
		return err
	}
	delete(d.props, prop)
	return nil
}

// Descendants lists all datasets below name, sorted (parents first).
func (m *Model) Descendants(_ context.Context, name string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(name); err != nil {
		return nil, err
	}
	return m.descendants(name), nil
}

// Snapshots lists the short snapshot names of a dataset, oldest first.
func (m *Model) Snapshots(_ context.Context, name string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(d.snaps))
	for i, s := range d.snaps {
		out[i] = s.name
	}
	return out, nil
}

// Written returns the bytes written to a dataset since the snapshot.
func (m *Model) Written(_ context.Context, snap string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s, err := m.lookupSnap(snap)
	if err != nil {
		return 0, err
	}
	return d.cum - s.cum, nil
}

func (m *Model) Origin(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		return "", err
	}
	return d.origin, nil
}

func (m *Model) Clones(_ context.Context, snap string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, _, err := m.lookupSnap(snap); err != nil {
		return nil, err
	}
	return m.clonesOf(snap), nil
}

func (m *Model) Hold(_ context.Context, snap, tag string) error {
	if err := m.F.Hit("zfs hold " + tag + " " + snap); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.lookupSnap(snap)
	if err != nil {
		return err
	}
	if s.holds[tag] {
		return fmt.Errorf("cannot hold snapshot '%s': tag already exists", snap)
	}
	s.holds[tag] = true
	return nil
}

func (m *Model) Release(_ context.Context, snap, tag string) error {
	if err := m.F.Hit("zfs release " + tag + " " + snap); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.lookupSnap(snap)
	if err != nil {
		return err
	}
	if !s.holds[tag] {
		return fmt.Errorf("cannot release hold from snapshot '%s': no such tag", snap)
	}
	delete(s.holds, tag)
	return nil
}

func (m *Model) Holds(_ context.Context, snap string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.lookupSnap(snap)
	if err != nil {
		return nil, err
	}
	var out []string
	for t := range s.holds {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// Mount and Unmount are idempotent here; the model only tracks the flag.
func (m *Model) Mount(_ context.Context, name string) error {
	if err := m.F.Hit("zfs mount " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		return err
	}
	if d.props["canmount"] == "off" {
		return fmt.Errorf("cannot mount '%s': canmount is off", name)
	}
	d.mounted = true
	return nil
}

func (m *Model) Unmount(_ context.Context, name string) error {
	if err := m.F.Hit("zfs unmount " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		return err
	}
	d.mounted = false
	return nil
}

func (m *Model) Mounted(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		return false, err
	}
	return d.mounted, nil
}

// Replicate models a full single-dataset "zfs send -p <snap> | zfs receive
// <dst>": an independent dataset (no origin) holding exactly that snapshot,
// with the local properties of the source overridden by props. Children are
// not sent.
// UNVERIFIED: a full stream of a clone has no origin dependency on FreeBSD.
func (m *Model) Replicate(_ context.Context, snap, dst string, props map[string]string) error {
	if err := m.F.Hit("zfs send " + snap + " | zfs receive " + dst); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s, err := m.lookupSnap(snap)
	if err != nil {
		return err
	}
	if _, ok := m.ds[dst]; ok {
		return fmt.Errorf("cannot receive: destination '%s' exists", dst)
	}
	if _, ok := m.ds[parent(dst)]; !ok || validName(dst) != nil {
		return fmt.Errorf("cannot receive: bad destination '%s'", dst)
	}
	nd := newDS(dst)
	nd.props = copyMap(d.props)
	for k, v := range props {
		nd.props[k] = v
	}
	nd.snaps = append(nd.snaps, &snapshot{name: s.name, props: copyMap(s.props), files: copyMap(s.files), cum: s.cum, holds: map[string]bool{}})
	nd.files = copyMap(s.files)
	nd.cum = s.cum
	m.autoMount(nd)
	m.ds[dst] = nd
	return nil
}

// ---- test helpers (not part of the ZFS interfaces) ----

// WriteFile writes content to a file in a dataset and accounts the bytes.
func (m *Model) WriteFile(name, path, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		panic(err)
	}
	d.files[path] = content
	d.cum += int64(len(content))
}

// ReadFile reads a file from a dataset.
func (m *Model) ReadFile(name, path string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.ds[name]
	if !ok {
		return "", false
	}
	c, ok := d.files[path]
	return c, ok
}

// Names lists every dataset (not snapshots), sorted.
func (m *Model) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.ds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Dump renders the whole model deterministically, for before/after comparison.
func (m *Model) Dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	var names []string
	for n := range m.ds {
		names = append(names, n)
	}
	sort.Strings(names)
	kv := func(p map[string]string) string {
		var ks []string
		for k := range p {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var o []string
		for _, k := range ks {
			o = append(o, k+"="+p[k])
		}
		return strings.Join(o, ",")
	}
	for _, n := range names {
		d := m.ds[n]
		fmt.Fprintf(&b, "%s origin=%s mounted=%v cum=%d props{%s} files{%s}\n", n, d.origin, d.mounted, d.cum, kv(d.props), kv(d.files))
		for _, s := range d.snaps {
			var h []string
			for t := range s.holds {
				h = append(h, t)
			}
			sort.Strings(h)
			fmt.Fprintf(&b, "  @%s cum=%d holds=%v props{%s} files{%s}\n", s.name, s.cum, h, kv(s.props), kv(s.files))
		}
	}
	return b.String()
}

// PropOrEmpty returns a local property or "" (test helper, no error path).
func (m *Model) PropOrEmpty(name, prop string) string {
	v, _ := m.Get(context.Background(), name, prop)
	return v
}
