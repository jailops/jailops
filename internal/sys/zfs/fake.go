package zfs

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

type fakeDS struct {
	typ     string // filesystem or volume
	props   map[string]string
	origin  string
	written uint64
	guid    uint64
}

type fakeSnap struct {
	guid     uint64
	seq      uint64
	props    map[string]string
	holds    map[string]time.Time
	deferred bool
}

type fakeBM struct{ guid uint64 }

// Fake is an in-memory Client with the ZFS semantics JailOps relies on:
// parents, snapshots, clones and origins, "cannot destroy snapshot with
// clones", holds, deferred destroy, rename that follows clones, rollback
// that destroys newer snapshots, bookmarks and a simple send stream format.
// It is safe for concurrent use. Unknown native properties read as "-".
type Fake struct {
	mu       sync.Mutex
	seq      uint64
	ds       map[string]*fakeDS
	snaps    map[string]*fakeSnap
	bms      map[string]*fakeBM
	features map[string][]string
	clock    func() time.Time
}

var _ Client = (*Fake)(nil)

// NewFake returns a Fake with the given empty pools.
func NewFake(pools ...string) *Fake {
	f := &Fake{
		ds: map[string]*fakeDS{}, snaps: map[string]*fakeSnap{}, bms: map[string]*fakeBM{},
		features: map[string][]string{}, clock: time.Now,
	}
	for _, p := range pools {
		f.ds[p] = &fakeDS{typ: "filesystem", props: map[string]string{}, guid: f.next()}
	}
	return f
}

func (f *Fake) next() uint64 { f.seq++; return f.seq*7919 + 1000 }

// SetWritten sets the bytes "written" since the last snapshot (test helper).
func (f *Fake) SetWritten(dataset string, n uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d := f.ds[dataset]; d != nil {
		d.written = n
	}
}

// SetFeatures sets the active features reported for a pool (test helper).
func (f *Fake) SetFeatures(pool string, active ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.features[pool] = append([]string(nil), active...)
}

func nf(name string) error {
	return api.Errorf(api.CodeNotFound, "cannot open '%s': dataset does not exist", name)
}

func busy(code api.Code, why, format string, a ...any) error {
	return api.Errorf(code, format, a...).WithDetail("reason", why)
}

func parentOf(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return ""
}

func pool(name string) string {
	n, _, _ := strings.Cut(name, "/")
	n, _, _ = strings.Cut(n, "@")
	n, _, _ = strings.Cut(n, "#")
	return n
}

// descendants returns name and every dataset below it, sorted.
func (f *Fake) subtree(name string) []string {
	var out []string
	for n := range f.ds {
		if n == name || strings.HasPrefix(n, name+"/") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f *Fake) snapsOf(ds string) []string {
	var out []string
	for n := range f.snaps {
		if d, _ := SplitSnapshot(n); d == ds {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return f.snaps[out[i]].seq < f.snaps[out[j]].seq })
	return out
}

func (f *Fake) clonesOf(snap string) []string {
	var out []string
	for n, d := range f.ds {
		if d.origin == snap {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f *Fake) exists(name string) bool {
	switch {
	case strings.Contains(name, "@"):
		return f.snaps[name] != nil
	case strings.Contains(name, "#"):
		return f.bms[name] != nil
	}
	return f.ds[name] != nil
}

func (f *Fake) mkdsLocked(name, typ string, props map[string]string, parents bool) error {
	if f.ds[name] != nil {
		return api.Errorf(api.CodeAlreadyExists, "cannot create '%s': dataset already exists", name)
	}
	p := parentOf(name)
	if p == "" {
		return nf(name)
	}
	if f.ds[p] == nil {
		if !parents {
			return nf(p)
		}
		if err := f.mkdsLocked(p, "filesystem", nil, true); err != nil {
			return err
		}
	}
	d := &fakeDS{typ: typ, props: map[string]string{}, guid: f.next()}
	for k, v := range props {
		d.props[k] = v
	}
	f.ds[name] = d
	return nil
}

// getProp returns the value and source of a property of any object.
func (f *Fake) getProp(name, prop string) (Prop, bool) {
	unset := Prop{Value: "-", Source: "-"}
	switch {
	case strings.Contains(name, "@"):
		s := f.snaps[name]
		if s == nil {
			return unset, false
		}
		switch prop {
		case "name":
			return Prop{name, "-"}, true
		case "type":
			return Prop{"snapshot", "-"}, true
		case "guid":
			return Prop{strconv.FormatUint(s.guid, 10), "-"}, true
		case "creation":
			return Prop{strconv.FormatUint(s.seq, 10), "-"}, true
		case "clones":
			if c := f.clonesOf(name); len(c) > 0 {
				return Prop{strings.Join(c, ","), "-"}, true
			}
			return unset, true
		case "userrefs":
			return Prop{strconv.Itoa(len(s.holds)), "-"}, true
		}
		if v, ok := s.props[prop]; ok {
			return Prop{v, "local"}, true
		}
		return unset, true
	case strings.Contains(name, "#"):
		b := f.bms[name]
		if b == nil {
			return unset, false
		}
		switch prop {
		case "name":
			return Prop{name, "-"}, true
		case "type":
			return Prop{"bookmark", "-"}, true
		case "guid":
			return Prop{strconv.FormatUint(b.guid, 10), "-"}, true
		}
		return unset, true
	}
	d := f.ds[name]
	if d == nil {
		return unset, false
	}
	switch prop {
	case "name":
		return Prop{name, "-"}, true
	case "type":
		return Prop{d.typ, "-"}, true
	case "guid":
		return Prop{strconv.FormatUint(d.guid, 10), "-"}, true
	case "origin":
		if d.origin != "" {
			return Prop{d.origin, "-"}, true
		}
		return unset, true
	case "written":
		return Prop{strconv.FormatUint(d.written, 10), "-"}, true
	}
	if strings.HasPrefix(prop, "written@") {
		if f.snaps[name+"@"+strings.TrimPrefix(prop, "written@")] == nil {
			return unset, false
		}
		return Prop{strconv.FormatUint(d.written, 10), "-"}, true
	}
	if v, ok := d.props[prop]; ok {
		return Prop{v, "local"}, true
	}
	// inheritance for user properties and a few natives
	for p := parentOf(name); p != ""; p = parentOf(p) {
		if pd := f.ds[p]; pd != nil {
			if v, ok := pd.props[prop]; ok && strings.Contains(prop, ":") {
				return Prop{v, "inherited from " + p}, true
			}
		}
	}
	return unset, true
}

func (f *Fake) allProps(name string) map[string]Prop {
	out := map[string]Prop{}
	for _, p := range []string{"name", "type", "guid", "origin", "written", "creation", "clones"} {
		if v, ok := f.getProp(name, p); ok && v.Value != "-" {
			out[p] = v
		}
	}
	if d := f.ds[name]; d != nil {
		for k := range d.props {
			out[k], _ = f.getProp(name, k)
		}
	} else if s := f.snaps[name]; s != nil {
		for k, v := range s.props {
			out[k] = Prop{v, "local"}
		}
	}
	return out
}

// List implements Client.
func (f *Fake) List(_ context.Context, o ListOpts) ([]Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	types := o.Types
	if len(types) == 0 {
		types = []string{"filesystem", "volume"}
	}
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	for _, p := range o.Props {
		if err := ValidateProperty(p); err != nil {
			return nil, err
		}
	}
	if o.Root != "" {
		if _, err := ValidateAny(o.Root); err != nil {
			return nil, err
		}
		if !f.exists(o.Root) {
			return nil, nf(o.Root)
		}
	}
	rec := o.Recursive || o.Depth > 0
	inScope := func(dsName string) bool {
		if o.Root == "" {
			return true
		}
		if dsName == o.Root {
			return true
		}
		if !rec || !strings.HasPrefix(dsName, o.Root+"/") {
			return false
		}
		if o.Depth > 0 {
			return strings.Count(dsName[len(o.Root)+1:], "/") < o.Depth
		}
		return true
	}
	var names []string
	for n, d := range f.ds {
		if want[d.typ] && inScope(n) {
			names = append(names, n)
		}
	}
	if want["snapshot"] {
		for n := range f.snaps {
			if d, _ := SplitSnapshot(n); inScope(d) || n == o.Root {
				names = append(names, n)
			}
		}
	}
	if want["bookmark"] {
		for n := range f.bms {
			if d, _, _ := strings.Cut(n, "#"); inScope(d) {
				names = append(names, n)
			}
		}
	}
	sort.Slice(names, func(i, j int) bool {
		di, _, _ := strings.Cut(strings.Replace(names[i], "#", "@", 1), "@")
		dj, _, _ := strings.Cut(strings.Replace(names[j], "#", "@", 1), "@")
		if di != dj {
			return di < dj
		}
		si, sj := f.snaps[names[i]], f.snaps[names[j]]
		if si != nil && sj != nil {
			return si.seq < sj.seq
		}
		return names[i] < names[j]
	})
	var out []Dataset
	for _, n := range names {
		t, _ := f.getProp(n, "type")
		d := Dataset{Name: n, Type: t.Value, Props: map[string]string{}}
		for _, p := range o.Props {
			if v, _ := f.getProp(n, p); v.Value != "-" {
				d.Props[p] = v.Value
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// Get implements Client.
func (f *Fake) Get(_ context.Context, name string, props ...string) (map[string]Prop, error) {
	if _, err := ValidateAny(name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists(name) {
		return nil, nf(name)
	}
	if len(props) == 0 || (len(props) == 1 && props[0] == "all") {
		return f.allProps(name), nil
	}
	out := map[string]Prop{}
	for _, p := range props {
		if err := ValidateProperty(p); err != nil {
			return nil, err
		}
		v, ok := f.getProp(name, p)
		if !ok {
			return nil, api.Errorf(api.CodeInvalid, "bad property list: invalid property '%s'", p)
		}
		out[p] = v
	}
	return out, nil
}

// Exists implements Client.
func (f *Fake) Exists(_ context.Context, name string) (bool, error) {
	if _, err := ValidateAny(name); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists(name), nil
}

// Set implements Client.
func (f *Fake) Set(_ context.Context, name string, props map[string]string) error {
	if _, err := ValidateAny(name); err != nil {
		return err
	}
	if err := validateProps(props); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.ds[name] != nil:
		for k, v := range props {
			f.ds[name].props[k] = v
		}
	case f.snaps[name] != nil:
		s := f.snaps[name]
		if s.props == nil {
			s.props = map[string]string{}
		}
		for k, v := range props {
			s.props[k] = v
		}
	default:
		return nf(name)
	}
	return nil
}

// Inherit implements Client.
func (f *Fake) Inherit(_ context.Context, name string, props ...string) error {
	if _, err := ValidateAny(name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists(name) {
		return nf(name)
	}
	for _, p := range props {
		if err := ValidateProperty(p); err != nil {
			return err
		}
		if d := f.ds[name]; d != nil {
			delete(d.props, p)
		} else if s := f.snaps[name]; s != nil {
			delete(s.props, p)
		}
	}
	return nil
}

// Create implements Client.
func (f *Fake) Create(_ context.Context, o CreateOpts) error {
	if err := ValidateDataset(o.Name); err != nil {
		return err
	}
	if err := validateProps(o.Props); err != nil {
		return err
	}
	typ := "filesystem"
	if o.VolumeSize != "" {
		if !validSize(o.VolumeSize) {
			return api.Errorf(api.CodeInvalid, "invalid volume size %q", o.VolumeSize)
		}
		typ = "volume"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mkdsLocked(o.Name, typ, o.Props, o.Parents)
}

// Clone implements Client.
func (f *Fake) Clone(_ context.Context, snapshot, target string, props map[string]string, parents bool) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if err := ValidateDataset(target); err != nil {
		return err
	}
	if err := validateProps(props); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snaps[snapshot] == nil {
		return nf(snapshot)
	}
	if pool(snapshot) != pool(target) {
		return api.Errorf(api.CodeInvalid, "cannot create '%s': source and target pools differ", target)
	}
	src, _ := SplitSnapshot(snapshot)
	if err := f.mkdsLocked(target, f.ds[src].typ, props, parents); err != nil {
		return err
	}
	f.ds[target].origin = snapshot
	return nil
}

// Snapshot implements Client. It is all or nothing, like the real command.
func (f *Fake) Snapshot(_ context.Context, o SnapshotOpts) error {
	if len(o.Names) == 0 {
		return api.Errorf(api.CodeInvalid, "no snapshots requested")
	}
	for _, n := range o.Names {
		if err := ValidateSnapshot(n); err != nil {
			return err
		}
	}
	if err := validateProps(o.Props); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []string
	seen := map[string]bool{}
	for _, n := range o.Names {
		if pool(n) != pool(o.Names[0]) {
			return api.Errorf(api.CodeInvalid, "snapshots must be in the same pool")
		}
		d, s := SplitSnapshot(n)
		if f.ds[d] == nil {
			return nf(d)
		}
		targets := []string{d}
		if o.Recursive {
			targets = f.subtree(d)
		}
		for _, t := range targets {
			full := t + "@" + s
			if f.snaps[full] != nil || seen[full] {
				return api.Errorf(api.CodeAlreadyExists, "cannot create snapshot '%s': dataset already exists", full)
			}
			seen[full] = true
			all = append(all, full)
		}
	}
	for _, n := range all {
		f.seq++
		s := &fakeSnap{guid: f.next(), seq: f.seq, props: map[string]string{}, holds: map[string]time.Time{}}
		for k, v := range o.Props {
			s.props[k] = v
		}
		f.snaps[n] = s
		f.ds[strings.SplitN(n, "@", 2)[0]].written = 0
	}
	return nil
}

// removeSnap deletes a snapshot unconditionally.
func (f *Fake) removeSnap(n string) { delete(f.snaps, n) }

// removeDS deletes a dataset with its snapshots and bookmarks.
func (f *Fake) removeDS(n string) {
	for _, s := range f.snapsOf(n) {
		f.removeSnap(s)
	}
	for b := range f.bms {
		if d, _, _ := strings.Cut(b, "#"); d == n {
			delete(f.bms, b)
		}
	}
	delete(f.ds, n)
}

// gc destroys snapshots marked for deferred destroy once nothing holds them.
func (f *Fake) gc() {
	for n, s := range f.snaps {
		if s.deferred && len(s.holds) == 0 && len(f.clonesOf(n)) == 0 {
			f.removeSnap(n)
		}
	}
}

// Destroy implements Client.
func (f *Fake) Destroy(_ context.Context, name string, o DestroyOpts) error {
	kind, err := ValidateAny(name)
	if err != nil {
		return err
	}
	if o.Defer && kind != KindSnapshot {
		return api.Errorf(api.CodeInvalid, "deferred destroy applies to snapshots only")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists(name) {
		return nf(name)
	}
	switch kind {
	case KindBookmark:
		delete(f.bms, name)
		return nil
	case KindSnapshot:
		return f.destroySnapLocked(name, o)
	}
	// dataset: collect the doomed set
	doomed := map[string]bool{}
	for _, n := range f.subtree(name) {
		doomed[n] = true
	}
	if !o.Recursive && !o.Dependents {
		if len(doomed) > 1 || len(f.snapsOf(name)) > 0 {
			return busy(api.CodeBusy, ReasonHasKids, "cannot destroy '%s': filesystem has children", name)
		}
	}
	for changed := true; changed; {
		changed = false
		for n := range doomed {
			for _, s := range f.snapsOf(n) {
				for _, c := range f.clonesOf(s) {
					if doomed[c] {
						continue
					}
					if !o.Dependents {
						return busy(api.CodeBusy, ReasonHasClones, "cannot destroy '%s': snapshot '%s' has dependent clones", name, s)
					}
					for _, cs := range f.subtree(c) {
						doomed[cs] = true
					}
					changed = true
				}
			}
		}
	}
	for n := range doomed {
		for _, s := range f.snapsOf(n) {
			if len(f.snaps[s].holds) > 0 {
				return busy(api.CodeBusy, ReasonHeld, "cannot destroy snapshot '%s': it's being held", s)
			}
		}
	}
	for n := range doomed {
		f.removeDS(n)
	}
	f.gc()
	return nil
}

func (f *Fake) destroySnapLocked(name string, o DestroyOpts) error {
	s := f.snaps[name]
	clones := f.clonesOf(name)
	if len(clones) > 0 && o.Dependents {
		if len(s.holds) > 0 {
			return busy(api.CodeBusy, ReasonHeld, "cannot destroy snapshot '%s': it's being held", name)
		}
		for _, c := range clones {
			for _, n := range f.subtree(c) {
				f.removeDS(n)
			}
		}
		f.removeSnap(name)
		f.gc()
		return nil
	}
	if o.Defer {
		s.deferred = true
		f.gc()
		return nil
	}
	if len(clones) > 0 {
		return busy(api.CodeBusy, ReasonHasClones, "cannot destroy '%s': snapshot has dependent clones", name)
	}
	if len(s.holds) > 0 {
		return busy(api.CodeBusy, ReasonHeld, "cannot destroy snapshot '%s': it's being held", name)
	}
	f.removeSnap(name)
	return nil
}

// Rename implements Client.
func (f *Fake) Rename(_ context.Context, from, to string, o RenameOpts) error {
	k1, err := ValidateAny(from)
	if err != nil {
		return err
	}
	k2, err := ValidateAny(to)
	if err != nil {
		return err
	}
	if k1 != k2 || k1 == KindBookmark {
		return api.Errorf(api.CodeInvalid, "rename needs two datasets or two snapshots")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists(from) {
		return nf(from)
	}
	if f.exists(to) {
		return api.Errorf(api.CodeAlreadyExists, "cannot rename to '%s': dataset already exists", to)
	}
	if pool(from) != pool(to) {
		return api.Errorf(api.CodeInvalid, "cannot rename to '%s': pools must be identical", to)
	}
	if k1 == KindSnapshot {
		d1, s1 := SplitSnapshot(from)
		d2, s2 := SplitSnapshot(to)
		if d1 != d2 {
			return api.Errorf(api.CodeInvalid, "snapshot rename must stay in the dataset")
		}
		targets := []string{d1}
		if o.Recursive {
			targets = f.subtree(d1)
		}
		for _, t := range targets {
			if t != d1 && f.snaps[t+"@"+s1] == nil {
				continue
			}
			f.renameSnap(t+"@"+s1, t+"@"+s2)
		}
		return nil
	}
	if strings.HasPrefix(to, from+"/") {
		return api.Errorf(api.CodeInvalid, "cannot rename a dataset into itself")
	}
	if p := parentOf(to); f.ds[p] == nil {
		if !o.Parents {
			return nf(p)
		}
		if err := f.mkdsLocked(p, "filesystem", nil, true); err != nil {
			return err
		}
	}
	remap := func(n string) string {
		if n == from || strings.HasPrefix(n, from+"/") || strings.HasPrefix(n, from+"@") || strings.HasPrefix(n, from+"#") {
			return to + n[len(from):]
		}
		return n
	}
	nds, nsn, nbm := map[string]*fakeDS{}, map[string]*fakeSnap{}, map[string]*fakeBM{}
	for n, d := range f.ds {
		d.origin = remap(d.origin)
		nds[remap(n)] = d
	}
	for n, s := range f.snaps {
		nsn[remap(n)] = s
	}
	for n, b := range f.bms {
		nbm[remap(n)] = b
	}
	f.ds, f.snaps, f.bms = nds, nsn, nbm
	return nil
}

func (f *Fake) renameSnap(from, to string) {
	f.snaps[to] = f.snaps[from]
	delete(f.snaps, from)
	for _, d := range f.ds {
		if d.origin == from {
			d.origin = to
		}
	}
}

// Rollback implements Client.
func (f *Fake) Rollback(_ context.Context, snapshot string, o RollbackOpts) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snaps[snapshot]
	if s == nil {
		return nf(snapshot)
	}
	ds, _ := SplitSnapshot(snapshot)
	var newer []string
	for _, n := range f.snapsOf(ds) {
		if f.snaps[n].seq > s.seq {
			newer = append(newer, n)
		}
	}
	if len(newer) > 0 && !o.DestroyNewer && !o.DestroyClones {
		return busy(api.CodeBusy, ReasonNewerSnaps, "cannot rollback to '%s': more recent snapshots or bookmarks exist", snapshot)
	}
	for _, n := range newer {
		if len(f.snaps[n].holds) > 0 {
			return busy(api.CodeBusy, ReasonHeld, "cannot destroy snapshot '%s': it's being held", n)
		}
		if len(f.clonesOf(n)) > 0 && !o.DestroyClones {
			return busy(api.CodeBusy, ReasonHasClones, "cannot rollback: snapshot '%s' has dependent clones", n)
		}
	}
	for _, n := range newer {
		for _, c := range f.clonesOf(n) {
			for _, x := range f.subtree(c) {
				f.removeDS(x)
			}
		}
		f.removeSnap(n)
	}
	f.ds[ds].written = 0
	f.gc()
	return nil
}

// Hold implements Client.
func (f *Fake) Hold(_ context.Context, tag string, snaps ...string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	for _, s := range snaps {
		if err := ValidateSnapshot(s); err != nil {
			return err
		}
	}
	if len(snaps) == 0 {
		return api.Errorf(api.CodeInvalid, "no snapshots given")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range snaps {
		sn := f.snaps[s]
		if sn == nil {
			return nf(s)
		}
		if _, dup := sn.holds[tag]; dup {
			return api.Errorf(api.CodeAlreadyExists, "cannot hold snapshot '%s': tag already exists on this dataset", s)
		}
	}
	for _, s := range snaps {
		f.snaps[s].holds[tag] = f.clock()
	}
	return nil
}

// Release implements Client.
func (f *Fake) Release(_ context.Context, tag string, snaps ...string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	for _, s := range snaps {
		if err := ValidateSnapshot(s); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range snaps {
		sn := f.snaps[s]
		if sn == nil {
			return nf(s)
		}
		if _, ok := sn.holds[tag]; !ok {
			return api.Errorf(api.CodeNotFound, "cannot release hold from snapshot '%s': no such tag on this dataset", s)
		}
	}
	for _, s := range snaps {
		delete(f.snaps[s].holds, tag)
	}
	f.gc()
	return nil
}

// Holds implements Client.
func (f *Fake) Holds(_ context.Context, snapshot string) ([]Hold, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snaps[snapshot]
	if s == nil {
		return nil, nf(snapshot)
	}
	var out []Hold
	for t, c := range s.holds {
		out = append(out, Hold{Tag: t, Created: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out, nil
}

// Bookmark implements Client.
func (f *Fake) Bookmark(_ context.Context, snapshot, bookmark string) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if err := ValidateBookmark(bookmark); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snaps[snapshot]
	if s == nil {
		return nf(snapshot)
	}
	d1, _ := SplitSnapshot(snapshot)
	if d2, _, _ := strings.Cut(bookmark, "#"); d1 != d2 {
		return api.Errorf(api.CodeInvalid, "bookmark must be in the dataset of the snapshot")
	}
	if f.bms[bookmark] != nil {
		return api.Errorf(api.CodeAlreadyExists, "cannot create bookmark '%s': bookmark exists", bookmark)
	}
	f.bms[bookmark] = &fakeBM{guid: s.guid}
	return nil
}

// Written implements Client.
func (f *Fake) Written(_ context.Context, dataset, since string) (uint64, error) {
	if err := ValidateDataset(dataset); err != nil {
		return 0, err
	}
	if since != "" {
		if err := ValidateSnapshot(since); err != nil {
			return 0, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.ds[dataset]
	if d == nil {
		return 0, nf(dataset)
	}
	if since != "" && f.snaps[since] == nil {
		return 0, nf(since)
	}
	return d.written, nil
}

// GUID implements Client.
func (f *Fake) GUID(_ context.Context, name string) (uint64, error) {
	if _, err := ValidateAny(name); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists(name) {
		return 0, nf(name)
	}
	p, _ := f.getProp(name, "guid")
	return ParseUint(p.Value)
}

// Clones implements Client.
func (f *Fake) Clones(_ context.Context, snapshot string) ([]string, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snaps[snapshot] == nil {
		return nil, nf(snapshot)
	}
	return f.clonesOf(snapshot), nil
}

// Origin implements Client.
func (f *Fake) Origin(_ context.Context, dataset string) (string, error) {
	if err := ValidateDataset(dataset); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.ds[dataset]
	if d == nil {
		return "", nf(dataset)
	}
	return d.origin, nil
}

// fakeStream is the Fake's send stream: one JSON line, then payload.
type fakeStream struct {
	Fake     string `json:"fake"`
	Snapshot string `json:"snapshot"`
	GUID     uint64 `json:"guid"`
	FromGUID uint64 `json:"from_guid,omitempty"`
	Raw      bool   `json:"raw,omitempty"`
}

// Send implements Client. Resume tokens are not supported by the Fake.
func (f *Fake) Send(ctx context.Context, o SendOpts, w io.Writer) error {
	if o.ResumeToken != "" {
		return api.Errorf(api.CodeUnsupported, "fake zfs does not model resume tokens")
	}
	if err := ValidateSnapshot(o.Snapshot); err != nil {
		return err
	}
	f.mu.Lock()
	s := f.snaps[o.Snapshot]
	var hdr fakeStream
	if s != nil {
		hdr = fakeStream{Fake: "zfs", Snapshot: o.Snapshot, GUID: s.guid, Raw: o.Raw}
	}
	var ferr error
	if s == nil {
		ferr = nf(o.Snapshot)
	} else if o.From != "" {
		if k, err := ValidateAny(o.From); err != nil || k == KindDataset {
			ferr = api.Errorf(api.CodeInvalid, "invalid incremental source %q", o.From)
		} else if !f.exists(o.From) {
			ferr = nf(o.From)
		} else {
			p, _ := f.getProp(o.From, "guid")
			hdr.FromGUID, _ = ParseUint(p.Value)
		}
	}
	f.mu.Unlock()
	if ferr != nil {
		return ferr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, _ := json.Marshal(hdr)
	_, err := w.Write(append(b, '\n'))
	return err
}

// Receive implements Client. Incremental streams need the target's newest
// snapshot to match the stream's source guid; a modified target needs Force.
func (f *Fake) Receive(ctx context.Context, o ReceiveOpts, r io.Reader) error {
	if err := ValidateDataset(o.Target); err != nil {
		return err
	}
	if err := validateProps(o.Props); err != nil {
		return err
	}
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
			if len(line) > 4096 {
				return api.Errorf(api.CodeInvalid, "cannot receive: invalid stream")
			}
		}
		if err != nil {
			return api.Errorf(api.CodeInvalid, "cannot receive: invalid stream")
		}
	}
	var h fakeStream
	if json.Unmarshal(line, &h) != nil || h.Fake != "zfs" {
		return api.Errorf(api.CodeInvalid, "cannot receive: invalid stream")
	}
	_, short := SplitSnapshot(h.Snapshot)
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.ds[o.Target]
	if h.FromGUID == 0 {
		if d != nil && !o.Force {
			return api.Errorf(api.CodeAlreadyExists, "cannot receive new filesystem stream: destination '%s' exists", o.Target)
		}
		if d != nil {
			f.removeDS(o.Target)
		}
		if err := f.mkdsLocked(o.Target, "filesystem", o.Props, false); err != nil {
			return err
		}
		d = f.ds[o.Target]
	} else {
		if d == nil {
			return nf(o.Target)
		}
		snaps := f.snapsOf(o.Target)
		if o.Force {
			// roll back to the incremental source, destroying newer snapshots
			for i := len(snaps) - 1; i >= 0 && f.snaps[snaps[i]].guid != h.FromGUID; i-- {
				f.removeSnap(snaps[i])
			}
			d.written = 0
			snaps = f.snapsOf(o.Target)
		}
		if len(snaps) == 0 || f.snaps[snaps[len(snaps)-1]].guid != h.FromGUID {
			return busy(api.CodeChainBroken, ReasonDiverged, "cannot receive incremental stream: most recent snapshot of '%s' does not match incremental source", o.Target)
		}
		if d.written > 0 && !o.Force {
			return busy(api.CodeChainBroken, ReasonDiverged, "cannot receive incremental stream: destination '%s' has been modified since most recent snapshot", o.Target)
		}
		for k, v := range o.Props {
			d.props[k] = v
		}
	}
	full := o.Target + "@" + short
	if f.snaps[full] != nil {
		return api.Errorf(api.CodeAlreadyExists, "cannot receive: snapshot '%s' exists", full)
	}
	f.seq++
	f.snaps[full] = &fakeSnap{guid: h.GUID, seq: f.seq, props: map[string]string{}, holds: map[string]time.Time{}}
	d.written = 0
	return ctx.Err()
}

// ReceiveAbort implements Client; the Fake keeps no partial state.
func (f *Fake) ReceiveAbort(_ context.Context, dataset string) error {
	if err := ValidateDataset(dataset); err != nil {
		return err
	}
	return api.Errorf(api.CodeInvalid, "cannot abort: '%s' does not have any resumable receive state to abort", dataset)
}

// ListPools implements Client.
func (f *Fake) ListPools(context.Context) ([]Pool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Pool
	for n := range f.ds {
		if !strings.Contains(n, "/") {
			out = append(out, Pool{Name: n, Size: 1 << 40, Free: 1 << 40, Health: "ONLINE"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ActiveFeatures implements Client.
func (f *Fake) ActiveFeatures(_ context.Context, p string) ([]string, error) {
	if err := ValidatePool(p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ds[p] == nil {
		return nil, api.Errorf(api.CodeNotFound, "no such pool '%s'", p)
	}
	return append([]string(nil), f.features[p]...), nil
}
