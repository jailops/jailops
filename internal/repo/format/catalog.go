package format

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
)

const (
	catalogMagic   = "JOCT"
	catalogVersion = 1
	// CatalogHeaderSize is the size of the fixed catalog header.
	CatalogHeaderSize = 8
)

// CatalogFrameWriter writes the framing of a catalog stream: the header, then
// whatever the caller writes (the entry encoding, defined by the catalog
// package), then a SHA-256 trailer of every preceding byte. Nothing is held in
// memory.
type CatalogFrameWriter struct {
	w      io.Writer
	h      hash.Hash
	n      int64
	closed bool
}

// NewCatalogFrameWriter writes the header and returns the writer.
func NewCatalogFrameWriter(w io.Writer) (*CatalogFrameWriter, error) {
	f := &CatalogFrameWriter{w: w, h: sha256.New()}
	if _, err := f.Write(append([]byte(catalogMagic), catalogVersion, 0, 0, 0)); err != nil {
		return nil, err
	}
	return f, nil
}

// Write implements io.Writer.
func (f *CatalogFrameWriter) Write(p []byte) (int, error) {
	if f.closed {
		return 0, errors.New("catalog frame already closed")
	}
	f.h.Write(p)
	n, err := f.w.Write(p)
	f.n += int64(n)
	return n, err
}

// Close writes the trailer. It does not close the underlying writer.
func (f *CatalogFrameWriter) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	n, err := f.w.Write(f.h.Sum(nil))
	f.n += int64(n)
	return err
}

// Len returns the number of bytes written so far, including header and trailer.
func (f *CatalogFrameWriter) Len() int64 { return f.n }

// OpenCatalog verifies the header and trailer of an encoded catalog and
// returns the body between them (the entry encoding).
func OpenCatalog(b []byte) ([]byte, error) {
	if len(b) < CatalogHeaderSize+trailerSize || string(b[:4]) != catalogMagic {
		return nil, errors.New("not a file catalog")
	}
	if b[4] != catalogVersion {
		return nil, fmt.Errorf("unsupported catalog version %d", b[4])
	}
	if b[5] != 0 || b[6] != 0 || b[7] != 0 {
		return nil, errors.New("catalog: reserved bytes set")
	}
	body, err := checkTrailer(b)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w (damaged or truncated)", err)
	}
	return body[CatalogHeaderSize:], nil
}
