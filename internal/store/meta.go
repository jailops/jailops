package store

import (
	"context"
	"database/sql"

	"jailops.net/jailops/sdk/api"
)

// Meta is a small key/value table for daemon bookkeeping.
type Meta struct{ s *Store }

// Get returns the value or a not_found error.
func (r *Meta) Get(ctx context.Context, key string) (string, error) {
	var v string
	if err := r.s.r.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v); err != nil {
		return "", mapErr(err, "meta "+key)
	}
	return v, nil
}

// Set stores a value.
func (r *Meta) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return api.Errorf(api.CodeInvalid, "empty meta key")
	}
	return r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
		return err
	})
}

// Delete removes a key. Deleting a missing key is not an error.
func (r *Meta) Delete(ctx context.Context, key string) error {
	return r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key = ?", key)
		return err
	})
}
