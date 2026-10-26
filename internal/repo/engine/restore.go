package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"jailops.net/jailops/internal/repo/catalog"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/repo/keys"
)

// RestoreOptions describe a restore of one snapshot.
type RestoreOptions struct {
	// Key is the master key (from the key file, the recovery key or an escrow blob).
	Key  *keys.Key
	Repo Repository

	Group string
	// Snapshot is the snapshot id ("" = the latest).
	Snapshot string

	// Stream names the dataset to restore into Dest ("" is allowed when the
	// snapshot has exactly one). Dests restores several streams, name to
	// directory; when set, Stream and Dest are ignored. Destinations must exist
	// and be directories.
	Stream string
	Dest   string
	Dests  map[string]string

	// Paths restricts the restore to these paths and everything below them
	// (single file and subtree restore: only the chunks holding their bytes are
	// fetched).
	Paths []string

	// Extraction options, see catalog.ExtractOptions.
	NoChown     bool
	SkipDevices bool
	Lenient     bool
	Skips       *catalog.Skips
	Attrs       catalog.Attrs

	// NewSink, when set, supplies the sink an extraction is written through (for an unprivileged daemon
	// that has an agent write for it). It is called once per stream with the stream's name and
	// destination; the returned func, if not nil, is called when the stream is done. Without it the
	// destination directory is written directly.
	NewSink func(stream, dest string) (catalog.Sink, func(), error)

	Workers int
	Window  int
}

// RestoreResult reports what was restored.
type RestoreResult struct {
	Snapshot string
	Streams  []RestoredStream
}

// RestoredStream is one restored dataset.
type RestoredStream struct {
	Name    string
	Dest    string
	Entries int
	Bytes   uint64
}

// Restore restores a snapshot (or part of it) into directories. Every chunk is
// decrypted and its id recomputed, every file's SHA-256 is checked, unsafe paths
// are refused and symlinks are never followed.
func Restore(ctx context.Context, o RestoreOptions) (*RestoreResult, error) {
	if o.Key == nil {
		return nil, ErrNoKey
	}
	snap, err := OpenSnapshot(ctx, o.Repo, o.Key.Keys, o.Group, o.Snapshot)
	if err != nil {
		return nil, err
	}
	dests := o.Dests
	if dests == nil {
		name := o.Stream
		if name == "" {
			if len(snap.Manifest.Streams) != 1 {
				return nil, fmt.Errorf("snapshot %s has %d streams: name the one to restore", snap.ID, len(snap.Manifest.Streams))
			}
			name = snap.Manifest.Streams[0].Name
		}
		if o.Dest == "" {
			return nil, errors.New("engine: no destination directory")
		}
		dests = map[string]string{name: o.Dest}
	}
	want := make(map[string]string, len(dests))
	for k, v := range dests {
		want[k] = v
	}
	res := &RestoreResult{Snapshot: snap.ID}
	for i := range snap.Manifest.Streams { // manifest order, deterministic
		st := snap.Manifest.Streams[i]
		dest, ok := want[st.Name]
		if !ok {
			continue
		}
		eo := catalog.ExtractOptions{NoChown: o.NoChown, SkipDevices: o.SkipDevices, Lenient: o.Lenient, Skips: o.Skips, Attrs: o.Attrs, Paths: o.Paths}
		var sink catalog.Sink
		done := func() {}
		if o.NewSink != nil {
			sk, closeSink, err := o.NewSink(st.Name, dest)
			if err != nil {
				return nil, fmt.Errorf("restoring %s: %w", st.Name, err)
			}
			sink = sk
			if closeSink != nil {
				done = closeSink
			}
		} else {
			sink = catalog.LocalSink(dest, o.Attrs)
		}
		var entries int
		if len(o.Paths) > 0 {
			v, err := snap.OpenView(ctx, st.Name, o.Workers, o.Window)
			if err != nil {
				done()
				return nil, err
			}
			if err := v.ExtractTo(sink, eo); err != nil {
				done()
				return nil, fmt.Errorf("restoring %s: %w", st.Name, err)
			}
			entries = v.Tree.Count
		} else {
			tree, err := snap.restoreWhole(ctx, &st, sink, eo, o.Workers, o.Window)
			if err != nil {
				done()
				return nil, fmt.Errorf("restoring %s: %w", st.Name, err)
			}
			entries = tree.Count
		}
		done()
		res.Streams = append(res.Streams, RestoredStream{Name: st.Name, Dest: dest, Entries: entries, Bytes: st.PayloadSize})
		delete(want, st.Name)
	}
	for name := range want {
		return nil, fmt.Errorf("snapshot %s has no stream %q", snap.ID, name)
	}
	return res, nil
}

// restoreWhole streams the payload in order through the read-ahead window into
// the extractor: memory does not depend on the size of the dataset.
func (s *Snapshot) restoreWhole(ctx context.Context, st *format.Stream, sink catalog.Sink, eo catalog.ExtractOptions, workers, window int) (*catalog.Tree, error) {
	tree, err := s.LoadCatalog(ctx, st, workers, window)
	if err != nil {
		return nil, err
	}
	if err := checkCatalogRanges(tree, st.PayloadSize); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	pi, err := s.payloadIndex(st)
	if err != nil {
		return nil, fmt.Errorf("%w: payload range of %s: %v", ErrCorrupt, st.Name, err)
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- tree.ExtractTo(sink, catalog.Sequential(pr), eo)
		_, _ = io.Copy(io.Discard, pr) // never leave the producer blocked
	}()
	var serr error
	if len(pi.Entries) > 0 {
		f := newFetcher(s.keys, s.repo, workers, window)
		serr = f.stream(ctx, pi, func(_ int, data []byte) error {
			_, werr := pw.Write(data)
			return werr
		})
	}
	_ = pw.CloseWithError(serr)
	xerr := <-errc
	if serr != nil && !errors.Is(serr, io.ErrClosedPipe) {
		return nil, serr
	}
	if xerr != nil {
		return nil, xerr
	}
	return tree, nil
}
