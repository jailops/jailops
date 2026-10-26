package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Session lease durations.
const (
	SessionIdleTimeout = 30 * time.Minute
	SessionMaxLifetime = 12 * time.Hour
)

// Session is a web session lease. The session id itself is never stored,
// only its SHA-256.
type Session struct {
	IDHash      []byte
	UserID      string
	CSRF        string
	Remote      string
	CreatedAt   time.Time
	LastSeen    time.Time
	IdleExpires time.Time
	MaxExpires  time.Time
}

// Sessions is the session repository.
type Sessions struct{ s *Store }

// Create inserts a new session with a fresh lease.
func (r *Sessions) Create(ctx context.Context, idHash []byte, userID, csrf, remote string) (*Session, error) {
	if len(idHash) != 32 || userID == "" || csrf == "" {
		return nil, api.Errorf(api.CodeInvalid, "session needs a 32-byte id hash, user and csrf token")
	}
	now := r.s.now()
	s := &Session{IDHash: idHash, UserID: userID, CSRF: csrf, Remote: remote, CreatedAt: now, LastSeen: now,
		IdleExpires: now.Add(SessionIdleTimeout), MaxExpires: now.Add(SessionMaxLifetime)}
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO sessions (id_hash, user_id, csrf, remote, created_at, last_seen, idle_expires, max_expires) VALUES (?,?,?,?,?,?,?,?)",
			idHash, userID, csrf, remote, ts(now), ts(now), ts(s.IdleExpires), ts(s.MaxExpires))
		return err
	})
	if err != nil {
		return nil, mapErr(err, "session")
	}
	return s, nil
}

// Validate returns the live session and extends its idle lease (never beyond
// the maximum lifetime). An expired session is deleted and reported as
// unauthorized, as is a session of a disabled user.
func (r *Sessions) Validate(ctx context.Context, idHash []byte) (*Session, error) {
	var out *Session
	expired := false
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		var s Session
		var c, ls, ie, me int64
		var disabled int
		err := tx.QueryRowContext(ctx, `SELECT s.user_id, s.csrf, s.remote, s.created_at, s.last_seen, s.idle_expires, s.max_expires, u.disabled
			FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, idHash).
			Scan(&s.UserID, &s.CSRF, &s.Remote, &c, &ls, &ie, &me, &disabled)
		if err != nil {
			if err == sql.ErrNoRows {
				return api.Errorf(api.CodeUnauthorized, "no such session")
			}
			return err
		}
		now := r.s.now()
		s.IDHash = idHash
		s.CreatedAt, s.LastSeen, s.IdleExpires, s.MaxExpires = fromTS(c), fromTS(ls), fromTS(ie), fromTS(me)
		if disabled != 0 || !now.Before(s.IdleExpires) || !now.Before(s.MaxExpires) {
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id_hash = ?", idHash); err != nil {
				return err
			}
			expired = true
			return nil
		}
		idle := now.Add(SessionIdleTimeout)
		if idle.After(s.MaxExpires) {
			idle = s.MaxExpires
		}
		s.LastSeen, s.IdleExpires = now, idle
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET last_seen = ?, idle_expires = ? WHERE id_hash = ?", ts(now), ts(idle), idHash); err != nil {
			return err
		}
		out = &s
		return nil
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, api.Errorf(api.CodeUnauthorized, "session expired")
	}
	return out, nil
}

// Delete ends one session (logout). Deleting a missing session is not an error.
func (r *Sessions) Delete(ctx context.Context, idHash []byte) error {
	return r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id_hash = ?", idHash)
		return err
	})
}

// DeleteForUser ends all sessions of a user.
func (r *Sessions) DeleteForUser(ctx context.Context, userID string) error {
	return r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
		return err
	})
}

// PurgeExpired deletes expired sessions and returns how many.
func (r *Sessions) PurgeExpired(ctx context.Context) (int64, error) {
	var n int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		now := ts(r.s.now())
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE idle_expires <= ? OR max_expires <= ?", now, now)
		if err == nil {
			n, _ = res.RowsAffected()
		}
		return err
	})
	return n, err
}

// Count returns the number of stored sessions.
func (r *Sessions) Count(ctx context.Context) (int, error) {
	var n int
	err := r.s.r.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&n)
	return n, err
}

// SessionInfo describes a live session for listings. Ref identifies it for
// revocation; it is derived from the stored hash and cannot rebuild the cookie.
type SessionInfo struct {
	Ref         string
	UserID      string
	UserName    string
	Remote      string
	CreatedAt   time.Time
	LastSeen    time.Time
	IdleExpires time.Time
	MaxExpires  time.Time
}

// SessionRef is the public reference of a session: hex of the first 8 bytes
// of its stored hash.
func SessionRef(idHash []byte) string {
	if len(idHash) < 8 {
		return ""
	}
	return hex.EncodeToString(idHash[:8])
}

// List returns the live sessions, newest first.
func (r *Sessions) List(ctx context.Context) ([]SessionInfo, error) {
	now := ts(r.s.now())
	rows, err := r.s.r.QueryContext(ctx, `SELECT s.id_hash, s.user_id, u.name, s.remote, s.created_at, s.last_seen, s.idle_expires, s.max_expires
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.idle_expires > ? AND s.max_expires > ? ORDER BY s.created_at DESC, s.id_hash`, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var si SessionInfo
		var h []byte
		var c, ls, ie, me int64
		if err := rows.Scan(&h, &si.UserID, &si.UserName, &si.Remote, &c, &ls, &ie, &me); err != nil {
			return nil, err
		}
		si.Ref = SessionRef(h)
		si.CreatedAt, si.LastSeen, si.IdleExpires, si.MaxExpires = fromTS(c), fromTS(ls), fromTS(ie), fromTS(me)
		out = append(out, si)
	}
	return out, rows.Err()
}

// DeleteByRef ends the session with the given reference and reports whether
// one existed.
func (r *Sessions) DeleteByRef(ctx context.Context, ref string) (bool, error) {
	if len(ref) != 16 {
		return false, nil
	}
	ref = strings.ToLower(ref)
	if _, err := hex.DecodeString(ref); err != nil {
		return false, nil
	}
	found := false
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE lower(hex(substr(id_hash, 1, 8))) = ?", ref)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		found = n > 0
		return nil
	})
	return found, err
}
