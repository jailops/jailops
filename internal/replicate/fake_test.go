package replicate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// memZFS models the parts of ZFS the package relies on: datasets with ordered
// snapshots and guids, holds (which block destroy), bookmarks that survive
// snapshot destruction, resumable receives with partial state, and receives
// that never force. Streams carry a header and deterministic payload bytes;
// they are real byte streams so the pipe and the size and guid checks run.
type memZFS struct {
	mu       sync.Mutex
	ds       map[string]*mdataset
	log      []string
	failSend int64 // when >0, the next send stream errors after this many bytes
}

type mdataset struct {
	snaps   []*msnap
	marks   map[string]SnapInfo
	props   map[string]string
	partial *mpartial
}

type msnap struct {
	SnapInfo
	holds map[string]bool
}

type mpartial struct {
	hdr      mhdr
	received int64
}

type mhdr struct {
	Snapshot  string
	GUID      uint64
	FromGUID  uint64
	Raw       bool
	Encrypted bool
	Created   time.Time
	Total     int64 // payload bytes
	Resume    bool
	Offset    int64
}

var guidCounter atomic.Uint64

func nextGUID() uint64 {
	x := guidCounter.Add(1) * 0x9E3779B97F4A7C15
	x ^= x >> 31
	return x | 1
}

func newMem() *memZFS { return &memZFS{ds: map[string]*mdataset{}} }

func (m *memZFS) logf(f string, a ...any) { m.log = append(m.log, fmt.Sprintf(f, a...)) }

func (m *memZFS) Log() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

func (m *memZFS) ClearLog() { m.mu.Lock(); m.log = nil; m.mu.Unlock() }

func notFound(what string) error { return fmt.Errorf("zfs: %s: does not exist", what) }

func (m *memZFS) get(ds string) (*mdataset, error) {
	d, ok := m.ds[ds]
	if !ok {
		return nil, notFound(ds)
	}
	return d, nil
}

func (m *memZFS) getSnap(ds, name string) (*mdataset, *msnap, error) {
	d, err := m.get(ds)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range d.snaps {
		if s.Name == name {
			return d, s, nil
		}
	}
	return nil, nil, notFound(ds + "@" + name)
}

// Snap creates a snapshot on a source dataset (creating the dataset).
func (m *memZFS) Snap(ds, name string, at time.Time, encrypted bool) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ds[ds]
	if d == nil {
		d = &mdataset{marks: map[string]SnapInfo{}, props: map[string]string{}}
		if encrypted {
			d.props["encryption"] = "aes-256-gcm"
		}
		m.ds[ds] = d
	}
	g := nextGUID()
	d.snaps = append(d.snaps, &msnap{SnapInfo: SnapInfo{Name: name, GUID: g, Created: at}, holds: map[string]bool{}})
	return g
}

func (m *memZFS) Exists(_ context.Context, ds string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.ds[ds]
	return ok, nil
}

func (m *memZFS) Snapshots(_ context.Context, ds string) ([]SnapInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(ds)
	if err != nil {
		return nil, err
	}
	var out []SnapInfo
	for _, s := range d.snaps {
		out = append(out, s.SnapInfo)
	}
	return out, nil
}

func (m *memZFS) Bookmarks(_ context.Context, ds string) ([]SnapInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(ds)
	if err != nil {
		return nil, err
	}
	var out []SnapInfo
	for _, b := range d.marks {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memZFS) Holds(_ context.Context, ds, snap string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.getSnap(ds, snap)
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

func (m *memZFS) Hold(_ context.Context, ds, snap, tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.getSnap(ds, snap)
	if err != nil {
		return err
	}
	if s.holds[tag] {
		return fmt.Errorf("zfs: tag already exists on this dataset")
	}
	s.holds[tag] = true
	m.logf("hold %s %s", snap, tag)
	return nil
}

func (m *memZFS) Release(_ context.Context, ds, snap, tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.getSnap(ds, snap)
	if err != nil {
		return err
	}
	if !s.holds[tag] {
		return fmt.Errorf("zfs: no such tag on this dataset")
	}
	delete(s.holds, tag)
	m.logf("release %s %s", snap, tag)
	return nil
}

func (m *memZFS) CreateBookmark(_ context.Context, ds, snap, bm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s, err := m.getSnap(ds, snap)
	if err != nil {
		return err
	}
	if _, dup := d.marks[bm]; dup {
		return fmt.Errorf("zfs: bookmark exists")
	}
	d.marks[bm] = SnapInfo{Name: bm, GUID: s.GUID, Created: s.Created}
	m.logf("bookmark %s %s", snap, bm)
	return nil
}

func (m *memZFS) DestroyBookmark(_ context.Context, ds, bm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(ds)
	if err != nil {
		return err
	}
	if _, ok := d.marks[bm]; !ok {
		return notFound(bm)
	}
	delete(d.marks, bm)
	m.logf("destroy-bookmark %s", bm)
	return nil
}

func (m *memZFS) DestroySnapshot(_ context.Context, ds, snap string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s, err := m.getSnap(ds, snap)
	if err != nil {
		return err
	}
	if len(s.holds) > 0 {
		return fmt.Errorf("zfs: cannot destroy snapshot %s@%s: dataset is busy", ds, snap)
	}
	for i, x := range d.snaps {
		if x == s {
			d.snaps = append(d.snaps[:i], d.snaps[i+1:]...)
			break
		}
	}
	m.logf("destroy %s", snap)
	return nil
}

func (m *memZFS) GetProp(_ context.Context, ds, p string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(ds)
	if err != nil {
		return "", err
	}
	return d.props[p], nil
}

func (m *memZFS) SetProp(_ context.Context, ds, p, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(ds)
	if err != nil {
		return err
	}
	d.props[p] = v
	return nil
}

func payloadSize(g uint64) int64 { return 300<<10 + int64(g%1000) }

func payloadByte(g uint64, i int64) byte { return byte(g + uint64(i)*7) }

type genReader struct {
	g       uint64
	pos     int64
	end     int64
	failAt  int64 // absolute position to fail at; <=0 disables
	prefix  *bytes.Reader
	failed  bool
	onClose func()
}

func (r *genReader) Read(p []byte) (int, error) {
	if r.prefix != nil && r.prefix.Len() > 0 {
		return r.prefix.Read(p)
	}
	if r.failAt > 0 && r.pos >= r.failAt {
		return 0, errors.New("connection reset")
	}
	if r.pos >= r.end {
		return 0, io.EOF
	}
	n := int64(len(p))
	if r.pos+n > r.end {
		n = r.end - r.pos
	}
	if r.failAt > 0 && r.pos+n > r.failAt {
		n = r.failAt - r.pos
	}
	for i := int64(0); i < n; i++ {
		p[i] = payloadByte(r.g, r.pos+i)
	}
	r.pos += n
	return int(n), nil
}

func (r *genReader) Close() error { return nil }

func (m *memZFS) Send(_ context.Context, o SendOpts) (Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, s, err := m.getSnap(o.Dataset, o.Snapshot)
	if err != nil {
		return Stream{}, err
	}
	h := mhdr{Snapshot: s.Name, GUID: s.GUID, Raw: o.Raw, Encrypted: d.props["encryption"] != "", Created: s.Created, Total: payloadSize(s.GUID)}
	if o.From != "" {
		if o.FromBookmark {
			b, ok := d.marks[o.From]
			if !ok {
				return Stream{}, notFound("#" + o.From)
			}
			h.FromGUID = b.GUID
		} else {
			_, f, err := m.getSnap(o.Dataset, o.From)
			if err != nil {
				return Stream{}, err
			}
			h.FromGUID = f.GUID
		}
	}
	if o.Raw && !h.Encrypted {
		// raw of an unencrypted dataset is just the plain stream
		h.Raw = true
	}
	var start int64
	if o.ResumeToken != "" {
		var t struct {
			To, From uint64
			Offset   int64
		}
		if err := json.Unmarshal([]byte(o.ResumeToken), &t); err != nil || t.To != s.GUID {
			return Stream{}, fmt.Errorf("zfs: invalid resume token")
		}
		h.Resume, h.Offset, start = true, t.Offset, t.Offset
	}
	hb, _ := json.Marshal(h)
	hb = append(hb, '\n')
	r := &genReader{g: s.GUID, pos: start, end: h.Total, prefix: bytes.NewReader(hb)}
	if m.failSend > 0 {
		r.failAt = start + m.failSend
		m.failSend = 0
	}
	return Stream{ReadCloser: r, Size: int64(len(hb)) + h.Total - start}, nil
}

func (m *memZFS) Receive(_ context.Context, o RecvOpts, rd io.Reader) (RecvResult, error) {
	br := bufio.NewReaderSize(rd, 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return RecvResult{}, fmt.Errorf("zfs: cannot receive: invalid stream (bad header): %w", err)
	}
	var h mhdr
	if err := json.Unmarshal(line, &h); err != nil {
		return RecvResult{}, fmt.Errorf("zfs: invalid stream: %w", err)
	}
	m.mu.Lock()
	d := m.ds[o.Dataset]
	// Validate against destination state, never forcing.
	switch {
	case d != nil && d.partial != nil:
		p := d.partial
		if !h.Resume || h.GUID != p.hdr.GUID || h.Offset != p.received {
			m.mu.Unlock()
			return RecvResult{}, fmt.Errorf("zfs: cannot receive: destination contains partially-complete state from \"zfs receive -s\"")
		}
	case h.Resume:
		m.mu.Unlock()
		return RecvResult{}, fmt.Errorf("zfs: cannot receive resume stream: destination has no partial state")
	case h.FromGUID == 0 && d != nil:
		m.mu.Unlock()
		return RecvResult{}, fmt.Errorf("zfs: cannot receive new filesystem stream: destination %s exists; must specify -F to overwrite it", o.Dataset)
	case h.FromGUID != 0:
		if d == nil {
			m.mu.Unlock()
			return RecvResult{}, fmt.Errorf("zfs: cannot receive incremental stream: destination %s does not exist", o.Dataset)
		}
		if len(d.snaps) == 0 || d.snaps[len(d.snaps)-1].GUID != h.FromGUID {
			m.mu.Unlock()
			return RecvResult{}, fmt.Errorf("zfs: cannot receive incremental stream: most recent snapshot of %s does not match incremental source", o.Dataset)
		}
		for _, s := range d.snaps {
			if s.Name == h.Snapshot {
				m.mu.Unlock()
				return RecvResult{}, fmt.Errorf("zfs: cannot receive: snapshot %s@%s already exists", o.Dataset, h.Snapshot)
			}
		}
	}
	if d == nil {
		d = &mdataset{marks: map[string]SnapInfo{}, props: map[string]string{}}
		m.ds[o.Dataset] = d
	}
	var p *mpartial
	if d.partial != nil {
		p = d.partial
	} else {
		p = &mpartial{hdr: h}
		d.partial = p
	}
	m.mu.Unlock()

	// Consume payload outside the lock so a pipe can stall.
	var got int64
	buf := make([]byte, 32<<10)
	var rerr error
	for p.received < p.hdr.Total {
		want := int64(len(buf))
		if rem := p.hdr.Total - p.received; rem < want {
			want = rem
		}
		n, err := br.Read(buf[:want])
		for i := 0; i < n; i++ {
			if buf[i] != payloadByte(p.hdr.GUID, p.received+int64(i)) {
				m.mu.Lock()
				d.partial = nil
				m.mu.Unlock()
				return RecvResult{}, errors.New("zfs: checksum mismatch in stream")
			}
		}
		p.received += int64(n)
		got += int64(n)
		if err != nil {
			rerr = err
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.received < p.hdr.Total {
		if rerr == io.EOF {
			rerr = io.ErrUnexpectedEOF
		}
		return RecvResult{}, fmt.Errorf("zfs: receive interrupted (resumable): %w", rerr)
	}
	d.partial = nil
	if p.hdr.FromGUID == 0 && p.hdr.Raw && p.hdr.Encrypted {
		d.props["encryption"] = "aes-256-gcm"
	}
	d.snaps = append(d.snaps, &msnap{SnapInfo: SnapInfo{Name: p.hdr.Snapshot, GUID: p.hdr.GUID, Created: p.hdr.Created}, holds: map[string]bool{}})
	return RecvResult{Snapshot: p.hdr.Snapshot, GUID: p.hdr.GUID, Bytes: got + int64(len(line))}, nil
}

func (m *memZFS) ResumeInfo(_ context.Context, ds string) (*ResumeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.ds[ds]
	if !ok || d.partial == nil {
		return nil, nil
	}
	p := d.partial
	tok, _ := json.Marshal(struct {
		To, From uint64
		Offset   int64
	}{p.hdr.GUID, p.hdr.FromGUID, p.received})
	return &ResumeInfo{Token: string(tok), FromGUID: p.hdr.FromGUID, ToGUID: p.hdr.GUID, BytesReceived: p.received}, nil
}

func (m *memZFS) AbortResume(_ context.Context, ds string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.ds[ds]
	if !ok || d.partial == nil {
		return fmt.Errorf("zfs: no partial state")
	}
	d.partial = nil
	if len(d.snaps) == 0 {
		delete(m.ds, ds)
	}
	return nil
}

// names of snapshots, for assertions.
func (m *memZFS) snapNames(ds string) string {
	s, _ := m.Snapshots(context.Background(), ds)
	var n []string
	for _, x := range s {
		n = append(n, x.Name)
	}
	return strings.Join(n, ",")
}
