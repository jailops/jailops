// Package jailxfer is the file format of `jailops jail export` and `jail import` (docs/export-import.md).
//
// A file is the line "JAILOPS-EXPORT 1", then records. A record is one type byte, the length of its payload as eight
// bytes (big endian) and the payload:
//
//	M  the manifest (JSON, api.ExportManifest), always first
//	D  the start of a part (JSON, api.ExportPart); the data records of that part follow
//	B  data of the part (a piece of the zfs send stream, compressed when the part says so)
//	E  the end of a part (JSON: the SHA-256 and the length of the stream, before compression)
//	F  the footer (JSON: the number of parts and the SHA-256 over the manifest and the parts' hashes), always last
//
// The reader verifies each part's hash when its E record arrives and the footer at the end, and says "truncated" for a
// file that ends anywhere else.
package jailxfer

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/klauspost/compress/zstd"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Magic is the first line of every export file.
const Magic = sdk.ExportMagic

const (
	recManifest = sdk.RecManifest
	recPart     = sdk.RecPart
	recData     = sdk.RecData
	recEnd      = sdk.RecEnd
	recFooter   = sdk.RecFooter

	// chunk is the size of a data record; maxRecord bounds the payload of any record a reader accepts.
	chunk     = 1 << 20
	maxRecord = sdk.MaxExportRecord
)

// ErrTruncated is what a reader returns for a file that ends before its footer.
var ErrTruncated = sdk.ErrExportTruncated

// ErrCorrupt is wrapped by the errors a reader returns for a file that does not hold what it says.
var ErrCorrupt = sdk.ErrExportCorrupt

type partEnd struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type footer struct {
	Parts  int    `json:"parts"`
	SHA256 string `json:"sha256"`
}

// Writer writes an export file.
type Writer struct {
	w      io.Writer
	roll   hash.Hash // the manifest, then the end record of each part
	parts  int
	active bool
}

func writeRecord(w io.Writer, typ byte, payload []byte) error {
	var h [9]byte
	h[0] = typ
	binary.BigEndian.PutUint64(h[1:], uint64(len(payload)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// NewWriter writes the magic line and the manifest.
func NewWriter(w io.Writer, m api.ExportManifest) (*Writer, error) {
	m.Format = api.ExportFormat
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, Magic); err != nil {
		return nil, err
	}
	if err := writeRecord(w, recManifest, b); err != nil {
		return nil, err
	}
	x := &Writer{w: w, roll: sha256.New()}
	x.roll.Write(b)
	return x, nil
}

// Part starts a part and returns the writer for its stream. Closing it ends the part; one part is open at a time.
func (x *Writer) Part(p api.ExportPart) (io.WriteCloser, error) {
	if x.active {
		return nil, errors.New("jailxfer: a part is still open")
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err := writeRecord(x.w, recPart, b); err != nil {
		return nil, err
	}
	x.active = true
	pw := &partWriter{x: x, sum: sha256.New()}
	pw.out = &chunkWriter{w: x.w}
	if p.Compression == "zstd" {
		z, err := zstd.NewWriter(pw.out, zstd.WithEncoderConcurrency(1))
		if err != nil {
			return nil, err
		}
		pw.z = z
	}
	return pw, nil
}

// Close writes the footer. The file is complete only after it.
func (x *Writer) Close() error {
	if x.active {
		return errors.New("jailxfer: a part is still open")
	}
	b, _ := json.Marshal(footer{Parts: x.parts, SHA256: hex.EncodeToString(x.roll.Sum(nil))})
	return writeRecord(x.w, recFooter, b)
}

type chunkWriter struct {
	w io.Writer
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), chunk)
		if err := writeRecord(c.w, recData, p[:k]); err != nil {
			return n, err
		}
		n += k
		p = p[k:]
	}
	return n, nil
}

type partWriter struct {
	x     *Writer
	out   *chunkWriter
	z     *zstd.Encoder
	sum   hash.Hash
	bytes int64
	buf   []byte
}

func (p *partWriter) Write(b []byte) (int, error) {
	p.sum.Write(b)
	p.bytes += int64(len(b))
	if p.z != nil {
		return p.z.Write(b)
	}
	// zfs send writes small pieces: gather them into full records
	p.buf = append(p.buf, b...)
	for len(p.buf) >= chunk {
		if _, err := p.out.Write(p.buf[:chunk]); err != nil {
			return 0, err
		}
		p.buf = p.buf[chunk:]
	}
	return len(b), nil
}

func (p *partWriter) Close() error {
	defer func() { p.x.active = false }()
	if p.z != nil {
		if err := p.z.Close(); err != nil {
			return err
		}
	} else if len(p.buf) > 0 {
		if _, err := p.out.Write(p.buf); err != nil {
			return err
		}
		p.buf = nil
	}
	b, _ := json.Marshal(partEnd{SHA256: hex.EncodeToString(p.sum.Sum(nil)), Bytes: p.bytes})
	if err := writeRecord(p.x.w, recEnd, b); err != nil {
		return err
	}
	p.x.roll.Write(b)
	p.x.parts++
	return nil
}

// Reader reads an export file.
type Reader struct {
	r        *bufio.Reader
	manifest api.ExportManifest
	rawMan   []byte
	roll     hash.Hash
	parts    int
	open     *partReader
	done     bool
	fail     error // the first truncation or corruption met while reading
}

func readRecord(r io.Reader) (byte, []byte, error) { return sdk.ReadExportRecord(r) }

// NewReader reads the magic line and the manifest.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	line := make([]byte, len(Magic))
	if _, err := io.ReadFull(br, line); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: not a jailops export file", ErrCorrupt)
		}
		return nil, err
	}
	if string(line) != Magic {
		return nil, fmt.Errorf("%w: not a jailops export file (version 1)", ErrCorrupt)
	}
	typ, payload, err := readRecord(br)
	if err != nil {
		return nil, err
	}
	if typ != recManifest {
		return nil, fmt.Errorf("%w: the manifest is missing", ErrCorrupt)
	}
	x := &Reader{r: br, rawMan: payload, roll: sha256.New()}
	if err := json.Unmarshal(payload, &x.manifest); err != nil {
		return nil, fmt.Errorf("%w: the manifest: %v", ErrCorrupt, err)
	}
	if x.manifest.Format != api.ExportFormat {
		return nil, fmt.Errorf("%w: format %d is not supported by this version", ErrCorrupt, x.manifest.Format)
	}
	x.roll.Write(payload)
	return x, nil
}

// Manifest is the manifest of the file.
func (x *Reader) Manifest() api.ExportManifest { return x.manifest }

// Fail is the first truncation or corruption the reader met, nil when there was none. A consumer that reads the stream
// through another process (zfs receive) loses the error in that process's own failure: Fail still names the file.
func (x *Reader) Fail() error { return x.fail }

func (x *Reader) note(err error) error {
	if x.fail == nil && (errors.Is(err, ErrTruncated) || errors.Is(err, ErrCorrupt)) {
		x.fail = err
	}
	return err
}

// Next returns the next part and the reader of its stream, or io.EOF after the footer has been verified. The stream
// has to be read to its end (or left: Next skips what is left) before Next is called again; reading the stream to
// its end verifies the part's hash, and a mismatch is the error of the last Read.
func (x *Reader) Next() (api.ExportPart, io.Reader, error) {
	part, r, err := x.next()
	return part, r, x.note(err)
}

func (x *Reader) next() (api.ExportPart, io.Reader, error) {
	if x.done {
		return api.ExportPart{}, nil, io.EOF
	}
	if x.open != nil {
		if _, err := io.Copy(io.Discard, x.open); err != nil {
			return api.ExportPart{}, nil, err
		}
		x.open = nil
	}
	typ, payload, err := readRecord(x.r)
	if err != nil {
		return api.ExportPart{}, nil, err
	}
	switch typ {
	case recFooter:
		var f footer
		if err := json.Unmarshal(payload, &f); err != nil {
			return api.ExportPart{}, nil, fmt.Errorf("%w: the footer: %v", ErrCorrupt, err)
		}
		if f.Parts != x.parts || f.SHA256 != hex.EncodeToString(x.roll.Sum(nil)) {
			return api.ExportPart{}, nil, fmt.Errorf("%w: the footer does not match the parts", ErrCorrupt)
		}
		x.done = true
		return api.ExportPart{}, nil, io.EOF
	case recPart:
		var p api.ExportPart
		if err := json.Unmarshal(payload, &p); err != nil {
			return api.ExportPart{}, nil, fmt.Errorf("%w: a part header: %v", ErrCorrupt, err)
		}
		pr := &partReader{x: x, sum: sha256.New()}
		pr.in = &chunkReader{x: x, pr: pr}
		if p.Compression == "zstd" {
			z, err := zstd.NewReader(pr.in, zstd.WithDecoderConcurrency(1))
			if err != nil {
				return api.ExportPart{}, nil, err
			}
			pr.z = z
		} else if p.Compression != "" {
			return api.ExportPart{}, nil, fmt.Errorf("%w: compression %q is not supported", ErrCorrupt, p.Compression)
		}
		x.open = pr
		return p, pr, nil
	}
	return api.ExportPart{}, nil, fmt.Errorf("%w: unexpected record %q", ErrCorrupt, typ)
}

// chunkReader reads the data records of one part and stops at its end record.
type chunkReader struct {
	x    *Reader
	pr   *partReader
	buf  []byte
	over bool
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.over {
			return 0, io.EOF
		}
		typ, payload, err := readRecord(c.x.r)
		if err != nil {
			return 0, err
		}
		switch typ {
		case recData:
			c.buf = payload
		case recEnd:
			c.over = true
			c.pr.end = payload
		default:
			return 0, fmt.Errorf("%w: unexpected record %q inside a part", ErrCorrupt, typ)
		}
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// partReader gives the stream of a part (decompressed) and checks its hash at the end.
type partReader struct {
	x     *Reader
	in    *chunkReader
	z     *zstd.Decoder
	sum   hash.Hash
	bytes int64
	end   []byte
	fin   bool
}

func (p *partReader) Read(b []byte) (int, error) {
	n, err := p.read(b)
	return n, p.x.note(err)
}

func (p *partReader) read(b []byte) (int, error) {
	if p.fin {
		return 0, io.EOF
	}
	var n int
	var err error
	if p.z != nil {
		n, err = p.z.Read(b)
	} else {
		n, err = p.in.Read(b)
	}
	p.sum.Write(b[:n])
	p.bytes += int64(n)
	if errors.Is(err, io.EOF) {
		// the compressed stream can end before the end record was read: read to it
		for !p.in.over {
			if _, e := p.in.Read(make([]byte, 1)); e != nil && !errors.Is(e, io.EOF) {
				return n, e
			}
		}
		if p.z != nil {
			p.z.Close()
		}
		if verr := p.verify(); verr != nil {
			return n, verr
		}
		p.fin = true
		return n, io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = ErrTruncated
		}
		return n, err
	}
	return n, nil
}

func (p *partReader) verify() error {
	var e partEnd
	if err := json.Unmarshal(p.end, &e); err != nil {
		return fmt.Errorf("%w: a part end: %v", ErrCorrupt, err)
	}
	if e.Bytes != p.bytes || e.SHA256 != hex.EncodeToString(p.sum.Sum(nil)) {
		return fmt.Errorf("%w: a part does not match its checksum", ErrCorrupt)
	}
	p.x.roll.Write(p.end)
	p.x.parts++
	return nil
}
