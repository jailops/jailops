package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"jailops.net/jailops/internal/repo/datastore"
)

// ProbeResult is the outcome of one probe step.
type ProbeResult struct {
	Op  string `json:"op"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

// ErrOverwrites is returned (inside a failed probe) by a service that replaced
// an existing object although the write carried If-None-Match: *. Commit
// markers are not safe on such a service.
var ErrOverwrites = errors.New("the service overwrote an existing object despite If-None-Match: *, so commit markers are not safe on it")

// Probe runs every operation the repository needs against a scratch key under
// "_probe/", one step at a time, so that a missing permission or feature is
// named rather than guessed at. The scratch key is removed at the end. The
// returned error is non-nil if any step failed.
func (b *Backend) Probe(ctx context.Context) ([]ProbeResult, error) {
	var rs []ProbeResult
	var failed []string
	add := func(op string, err error) bool {
		r := ProbeResult{Op: op, OK: err == nil}
		if err != nil {
			r.Err = err.Error()
			failed = append(failed, op)
		}
		rs = append(rs, r)
		return err == nil
	}
	finish := func() ([]ProbeResult, error) {
		if len(failed) > 0 {
			return rs, fmt.Errorf("s3 probe failed: %s", strings.Join(failed, ", "))
		}
		return rs, nil
	}
	if !add("connect and bucket exists", b.HeadBucket(ctx)) {
		return finish()
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	key := "_probe/" + hex.EncodeToString(rnd[:])
	first := []byte("jailops probe, first write")
	second := []byte("jailops probe, SECOND write that must be refused")
	defer func() { _ = b.Delete(context.WithoutCancel(ctx), key) }()

	if !add("put", b.Put(ctx, key, bytes.NewReader(first), int64(len(first)), datastore.PutOptions{})) {
		return finish()
	}
	add("put if-absent refuses an existing key", func() error {
		err := b.Put(ctx, key, bytes.NewReader(second), int64(len(second)), datastore.PutOptions{IfAbsent: true})
		switch {
		case errors.Is(err, datastore.ErrExists):
			return nil
		case err == nil:
			return ErrOverwrites
		}
		return err
	}())
	add("get returns the first write", func() error {
		got, err := readAll(b.Get(ctx, key))
		if err == nil && !bytes.Equal(got, first) {
			err = errors.New("the object does not hold the first write: a refused conditional write changed it")
		}
		return err
	}())
	add("range read", func() error {
		got, err := readAll(b.GetRange(ctx, key, 7, 5))
		if err == nil && !bytes.Equal(got, first[7:12]) {
			err = fmt.Errorf("range 7+5 returned %q", got)
		}
		return err
	}())
	add("head", func() error {
		info, err := b.Head(ctx, key)
		if err == nil && info.Size != int64(len(first)) {
			err = fmt.Errorf("head reports %d bytes, want %d", info.Size, len(first))
		}
		return err
	}())
	add("list shows the object", func() error {
		n := 0
		err := b.List(ctx, "_probe/", func(o datastore.ObjectInfo) error {
			if o.Key == key {
				n++
			}
			return nil
		})
		if err == nil && n != 1 {
			err = fmt.Errorf("the listing shows the new object %d times, want once", n)
		}
		return err
	}())
	add("delete", b.Delete(ctx, key))
	add("delete is idempotent", b.Delete(ctx, key))
	add("get after delete is not found", func() error {
		_, err := b.Get(ctx, key)
		if errors.Is(err, datastore.ErrNotFound) {
			return nil
		}
		if err == nil {
			return errors.New("the object is still readable after the delete")
		}
		return err
	}())
	return finish()
}

func readAll(rc io.ReadCloser, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
