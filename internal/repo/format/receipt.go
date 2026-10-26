package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Receipt is commit.json, the commit marker. It is written last and
// create-only; a snapshot exists if and only if it exists.
type Receipt struct {
	Format         int       `json:"format"`
	Committed      time.Time `json:"committed"`
	ChunkCount     int       `json:"chunk_count"`
	IndexBytes     int       `json:"index_bytes"`
	IndexDigest    string    `json:"index_digest"`
	ManifestDigest string    `json:"manifest_digest"`
	// Source is "client" for a snapshot committed by a host, "sync" for one
	// copied from another repository.
	Source string `json:"source"`
}

// Marshal returns the canonical JSON form (what is stored and compared on a
// retried commit).
func (r *Receipt) Marshal() ([]byte, error) {
	c := *r
	c.Format = 1
	c.Committed = c.Committed.UTC()
	return json.Marshal(&c)
}

// ParseReceipt parses and validates commit.json.
func ParseReceipt(b []byte) (*Receipt, error) {
	if len(b) > 64<<10 {
		return nil, errors.New("receipt: too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var r Receipt
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("receipt: %w", err)
	}
	if dec.More() {
		return nil, errors.New("receipt: trailing data")
	}
	if r.Format != 1 {
		return nil, fmt.Errorf("receipt: unsupported format %d", r.Format)
	}
	if !validDigest(r.IndexDigest) || !validDigest(r.ManifestDigest) {
		return nil, errors.New("receipt: bad digest")
	}
	if r.ChunkCount < 0 || r.IndexBytes < 0 {
		return nil, errors.New("receipt: negative count")
	}
	return &r, nil
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
