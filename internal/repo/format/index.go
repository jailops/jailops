package format

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

const (
	indexMagic   = "JOIX"
	indexVersion = 1
	indexHeader  = 4 + 1 + 1 + 2 + 8 + 8
	entrySize    = 8 + IDSize

	// IndexDynamic is the only index kind of format version 1: chunks of
	// varying size. Kind 1 is reserved for fixed-size block chunks.
	IndexDynamic = 2

	// MaxIndexSize bounds an index file a reader is willing to parse.
	MaxIndexSize = 256 << 20
)

// Entry is one chunk reference. It covers [previous End, End).
type Entry struct {
	End uint64
	ID  ID
}

// Index is a parsed index file.
type Index struct {
	Size    uint64
	Entries []Entry
}

// Validate checks internal consistency.
func (x *Index) Validate() error {
	var prev uint64
	for i, e := range x.Entries {
		if e.End <= prev {
			return fmt.Errorf("entry %d: end %d not after %d", i, e.End, prev)
		}
		if e.End-prev > MaxChunkSize {
			return fmt.Errorf("entry %d: chunk of %d bytes exceeds the maximum", i, e.End-prev)
		}
		prev = e.End
	}
	if len(x.Entries) == 0 {
		if x.Size != 0 {
			return errors.New("no entries but non-zero size")
		}
		return nil
	}
	if prev != x.Size {
		return fmt.Errorf("last entry ends at %d, size is %d", prev, x.Size)
	}
	return nil
}

// Marshal serialises the index.
func (x *Index) Marshal() ([]byte, error) {
	if err := x.Validate(); err != nil {
		return nil, err
	}
	b := make([]byte, 0, indexHeader+len(x.Entries)*entrySize+trailerSize)
	b = append(b, indexMagic...)
	b = append(b, indexVersion, IndexDynamic, 0, 0)
	b = putU64(b, x.Size)
	b = putU64(b, uint64(len(x.Entries)))
	for _, e := range x.Entries {
		b = putU64(b, e.End)
		b = append(b, e.ID[:]...)
	}
	return appendTrailer(b), nil
}

// ParseIndex parses and validates an index file.
func ParseIndex(b []byte) (*Index, error) {
	if len(b) > MaxIndexSize {
		return nil, errors.New("index: too large")
	}
	body, err := checkTrailer(b)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	if len(body) < indexHeader || string(body[:4]) != indexMagic {
		return nil, errors.New("index: bad magic")
	}
	if body[4] != indexVersion {
		return nil, fmt.Errorf("index: unsupported version %d", body[4])
	}
	if body[5] != IndexDynamic {
		return nil, fmt.Errorf("index: unsupported kind %d", body[5])
	}
	if body[6] != 0 || body[7] != 0 {
		return nil, errors.New("index: reserved bytes set")
	}
	x := &Index{Size: binary.BigEndian.Uint64(body[8:])}
	count := binary.BigEndian.Uint64(body[16:])
	rest := body[indexHeader:]
	if count > uint64(len(rest))/entrySize || uint64(len(rest)) != count*entrySize {
		return nil, errors.New("index: entry count does not match length")
	}
	x.Entries = make([]Entry, count)
	for i := range x.Entries {
		o := i * entrySize
		x.Entries[i].End = binary.BigEndian.Uint64(rest[o:])
		copy(x.Entries[i].ID[:], rest[o+8:o+entrySize])
	}
	if err := x.Validate(); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	return x, nil
}

// FileDigest returns the hex SHA-256 of a whole file (an index or a manifest
// as stored); manifests and receipts record it.
func FileDigest(file []byte) string {
	s := sha256.Sum256(file)
	return hex.EncodeToString(s[:])
}

// ChunkIDs returns the distinct non-zero ids referenced by the index, in index
// order.
func (x *Index) ChunkIDs() []ID {
	seen := make(map[ID]struct{}, min(len(x.Entries), 1<<16))
	var out []ID
	for _, e := range x.Entries {
		if e.ID.IsZero() {
			continue
		}
		if _, ok := seen[e.ID]; ok {
			continue
		}
		seen[e.ID] = struct{}{}
		out = append(out, e.ID)
	}
	return out
}

// Span returns the byte range covered by entry i.
func (x *Index) Span(i int) (off, n uint64) {
	if i > 0 {
		off = x.Entries[i-1].End
	}
	return off, x.Entries[i].End - off
}

// Find returns the index of the entry that holds byte offset off, or -1.
func (x *Index) Find(off uint64) int {
	i := sort.Search(len(x.Entries), func(i int) bool { return x.Entries[i].End > off })
	if i >= len(x.Entries) {
		return -1
	}
	return i
}

// Sub returns the entries covering [from, to), rebased to start at 0. Both
// ends must be chunk boundaries; otherwise an error is returned.
func (x *Index) Sub(from, to uint64) (*Index, error) {
	if from > to || to > x.Size {
		return nil, fmt.Errorf("range [%d,%d) is outside the index", from, to)
	}
	if from == to {
		if from != 0 && x.Find(from-1) < 0 {
			return nil, fmt.Errorf("range [%d,%d) is outside the index", from, to)
		}
		return &Index{}, nil
	}
	i := x.Find(from)
	if i < 0 {
		return nil, fmt.Errorf("range [%d,%d) is outside the index", from, to)
	}
	if off, _ := x.Span(i); off != from {
		return nil, fmt.Errorf("offset %d is not a chunk boundary", from)
	}
	out := &Index{Size: to - from}
	for ; i < len(x.Entries); i++ {
		end := x.Entries[i].End
		if end > to {
			return nil, fmt.Errorf("offset %d is not a chunk boundary", to)
		}
		out.Entries = append(out.Entries, Entry{End: end - from, ID: x.Entries[i].ID})
		if end == to {
			return out, nil
		}
	}
	return nil, fmt.Errorf("range [%d,%d) is outside the index", from, to)
}
