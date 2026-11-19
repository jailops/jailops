package zfsmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The model's send stream is JSON: the files and properties of one snapshot, and the snapshot it is incremental from.
// A raw stream of an encryption root carries its passphrase, as a real one carries the wrapped key: the receiving
// dataset is encrypted with it and has no loaded key.

type sendStream struct {
	V     int               `json:"v"`
	Snap  string            `json:"snap"`
	From  string            `json:"from,omitempty"`
	Props map[string]string `json:"props"`
	Sprop map[string]string `json:"snap_props"`
	Files map[string]string `json:"files"`
	Cum   int64             `json:"cum"`
	Pass  string            `json:"pass,omitempty"`
}

// SendStream is `zfs send [-i from] [-w] snapshot`.
func (m *Model) SendStream(_ context.Context, snapshot, from string, raw bool, w io.Writer) error {
	if err := m.F.Hit("zfs send " + snapshot); err != nil {
		return err
	}
	m.mu.Lock()
	d, s, err := m.lookupSnap(snapshot)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	st := sendStream{V: 1, Snap: s.name, Props: copyMap(d.props), Sprop: copyMap(s.props), Files: copyMap(s.files), Cum: s.cum}
	if from != "" {
		if d.snap(strings.TrimPrefix(from, "@")) == nil {
			m.mu.Unlock()
			return fmt.Errorf("cannot send: incremental source @%s does not exist", from)
		}
		st.From = strings.TrimPrefix(from, "@")
	}
	if raw && d.pass != "" {
		st.Pass = d.pass
	}
	m.mu.Unlock()
	return json.NewEncoder(w).Encode(st)
}

// ReceiveStream is `zfs receive -u [-o prop=...] target`: a full stream makes a new dataset, an incremental one needs
// the snapshot it starts from to be the newest of the target.
func (m *Model) ReceiveStream(_ context.Context, target string, props map[string]string, r io.Reader) error {
	if err := m.F.Hit("zfs receive " + target); err != nil {
		return err
	}
	var st sendStream
	if err := json.NewDecoder(r).Decode(&st); err != nil {
		return fmt.Errorf("cannot receive: invalid stream (%v)", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, exists := m.ds[target]
	if st.From == "" {
		if exists {
			return fmt.Errorf("cannot receive new filesystem stream: destination '%s' exists", target)
		}
		if _, ok := m.ds[parent(target)]; !ok || validName(target) != nil {
			return fmt.Errorf("cannot receive: bad destination '%s'", target)
		}
		d = newDS(target)
		// a plain zfs send carries no properties: the dataset gets the ones the receive sets
		d.props = map[string]string{}
		for k, v := range props {
			d.props[k] = v
		}
		d.pass = st.Pass
		m.ds[target] = d
	} else {
		if !exists {
			return fmt.Errorf("cannot receive incremental stream: destination '%s' does not exist", target)
		}
		if len(d.snaps) == 0 || d.snaps[len(d.snaps)-1].name != st.From {
			return fmt.Errorf("cannot receive incremental stream: most recent snapshot of %s does not match incremental source", target)
		}
		for k, v := range props {
			d.props[k] = v
		}
	}
	d.snaps = append(d.snaps, &snapshot{name: st.Snap, props: copyMap(st.Sprop), files: copyMap(st.Files), cum: st.Cum, holds: map[string]bool{}})
	d.files = copyMap(st.Files)
	d.cum = st.Cum
	d.keyLoaded = false
	m.autoMount(d)
	d.mounted = false // received with -u: never mounted by the receive
	return nil
}
