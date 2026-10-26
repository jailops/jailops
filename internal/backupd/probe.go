package backupd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3"
)

// ProbeBackend checks the behaviour the repository relies on, on any backend:
// write, create-only write that must refuse an existing key without changing
// it, read, range read, list and delete. A backend that overwrites despite
// IfAbsent is refused. S3 backends additionally run their own probe (bucket
// reachable, listing, idempotent delete).
func ProbeBackend(ctx context.Context, b datastore.Backend) error {
	if sb, ok := datastore.Unwrap(b).(*s3.Backend); ok {
		if _, err := sb.Probe(ctx); err != nil {
			return err
		}
	}
	var r [8]byte
	_, _ = rand.Read(r[:])
	key := "_probe/" + hex.EncodeToString(r[:])
	first, second := []byte("probe one"), []byte("probe TWO, must never be stored")
	defer b.Delete(context.WithoutCancel(ctx), key)
	if err := datastore.PutBytes(ctx, b, key, first, datastore.PutOptions{IfAbsent: true}); err != nil {
		return fmt.Errorf("probe: create-only write: %w", err)
	}
	err := datastore.PutBytes(ctx, b, key, second, datastore.PutOptions{IfAbsent: true})
	switch {
	case errors.Is(err, datastore.ErrExists):
	case err == nil:
		return errors.New("probe: the storage overwrote an existing object despite a create-only write; commit receipts would not be safe on it")
	default:
		return fmt.Errorf("probe: second create-only write: %w", err)
	}
	got, err := datastore.ReadAll(ctx, b, key, 1<<20)
	if err != nil || !bytes.Equal(got, first) {
		return fmt.Errorf("probe: read-back differs after a refused overwrite (%v)", err)
	}
	rc, err := b.GetRange(ctx, key, 6, 3)
	if err != nil {
		return fmt.Errorf("probe: range read: %w", err)
	}
	part := make([]byte, 3)
	_, rerr := rc.Read(part)
	rc.Close()
	if rerr != nil && !strings.Contains(rerr.Error(), "EOF") || string(part) != "one" {
		return fmt.Errorf("probe: range read returned %q", part)
	}
	found := false
	if err := b.List(ctx, "_probe/", func(o datastore.ObjectInfo) error { found = found || o.Key == key; return nil }); err != nil || !found {
		return fmt.Errorf("probe: listing does not show the object just written (%v)", err)
	}
	if err := b.Delete(ctx, key); err != nil {
		return fmt.Errorf("probe: delete: %w", err)
	}
	if err := b.Delete(ctx, key); err != nil {
		return fmt.Errorf("probe: deleting a missing key must succeed: %w", err)
	}
	if _, err := b.Head(ctx, key); !errors.Is(err, datastore.ErrNotFound) {
		return fmt.Errorf("probe: the object is still there after the delete (%v)", err)
	}
	return nil
}
