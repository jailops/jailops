package sdk

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"jailops.net/jailops/sdk/api"
)

// ExportMagic is the first line of every jail export file (docs/export-import.md).
const ExportMagic = "JAILOPS-EXPORT 1\n"

// The record types of an export file: a type byte, the length of the payload as eight bytes (big endian), the payload.
const (
	RecManifest = 'M'
	RecPart     = 'D'
	RecData     = 'B'
	RecEnd      = 'E'
	RecFooter   = 'F'
)

// MaxExportRecord bounds the payload of any record a reader accepts.
const MaxExportRecord = 64 << 20

// ErrExportTruncated is what reading an export file returns when it ends before its footer.
var ErrExportTruncated = errors.New("the export file is truncated")

// ErrExportCorrupt is wrapped by the errors for a file that does not hold what it says.
var ErrExportCorrupt = errors.New("the export file is corrupt")

// ReadExportRecord reads one record of an export file.
func ReadExportRecord(r io.Reader) (typ byte, payload []byte, err error) {
	var h [9]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, ErrExportTruncated
		}
		return 0, nil, err
	}
	n := binary.BigEndian.Uint64(h[1:])
	if n > MaxExportRecord {
		return 0, nil, fmt.Errorf("%w: a record of %d bytes", ErrExportCorrupt, n)
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, ErrExportTruncated
		}
		return 0, nil, err
	}
	return h[0], payload, nil
}

// ReadExportManifest reads the magic line and the manifest of an export file.
func ReadExportManifest(r io.Reader) (api.ExportManifest, error) {
	var m api.ExportManifest
	line := make([]byte, len(ExportMagic))
	if _, err := io.ReadFull(r, line); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return m, fmt.Errorf("%w: not a jailops export file", ErrExportCorrupt)
		}
		return m, err
	}
	if string(line) != ExportMagic {
		return m, fmt.Errorf("%w: not a jailops export file (version 1)", ErrExportCorrupt)
	}
	typ, payload, err := ReadExportRecord(r)
	if err != nil {
		return m, err
	}
	if typ != RecManifest {
		return m, fmt.Errorf("%w: the manifest is missing", ErrExportCorrupt)
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return m, fmt.Errorf("%w: the manifest: %v", ErrExportCorrupt, err)
	}
	if m.Format != api.ExportFormat {
		return m, fmt.Errorf("%w: format %d is not supported by this version", ErrExportCorrupt, m.Format)
	}
	return m, nil
}

// ScanExport reads the records of a whole export file, without decoding the streams: it finds a file that is cut
// off or whose framing is damaged, and returns the number of streams. The checksums of the streams are verified by
// the importing daemon, which holds the decoder.
func ScanExport(r io.Reader) (int, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	if _, err := ReadExportManifest(br); err != nil {
		return 0, err
	}
	parts := 0
	for {
		typ, payload, err := ReadExportRecord(br)
		if err != nil {
			return parts, err
		}
		switch typ {
		case RecPart:
			parts++
		case RecData, RecEnd:
		case RecFooter:
			var f struct {
				Parts int `json:"parts"`
			}
			if err := json.Unmarshal(payload, &f); err != nil || f.Parts != parts {
				return parts, fmt.Errorf("%w: the footer does not match the parts", ErrExportCorrupt)
			}
			return parts, nil
		default:
			return parts, fmt.Errorf("%w: unexpected record %q", ErrExportCorrupt, typ)
		}
	}
}
