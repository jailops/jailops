package datastore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

// PutBytes writes data under key.
func PutBytes(ctx context.Context, b Backend, key string, data []byte, opts PutOptions) error {
	return b.Put(ctx, key, bytes.NewReader(data), int64(len(data)), opts)
}

// ReadAll returns the whole object, refusing objects larger than max bytes so
// that metadata reads cannot exhaust memory.
func ReadAll(ctx context.Context, b Backend, key string, max int64) ([]byte, error) {
	rc, err := b.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("datastore: %s is larger than %d bytes", key, max)
	}
	return data, nil
}

// Exists reports whether key exists.
func Exists(ctx context.Context, b Backend, key string) (bool, error) {
	_, err := b.Head(ctx, key)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	}
	return false, err
}
