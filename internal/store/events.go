package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync/atomic"

	"jailops.net/jailops/sdk/api"
)

// StoredEvent is an event with its ring sequence number.
type StoredEvent struct {
	ID    int64
	Event api.Event
}

// Events is the event ring: bounded by row count and age.
type Events struct{ s *Store }

var eventAppends atomic.Int64

const eventPruneEvery = 100

// Append stores an event and returns its sequence id. The ring is pruned
// every eventPruneEvery appends.
func (r *Events) Append(ctx context.Context, ev api.Event) (int64, error) {
	meta := ev.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage("null")
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = r.s.now().UTC()
	}
	var id int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO events (ts, type, host, metadata) VALUES (?,?,?,?)",
			ts(ev.Timestamp), string(ev.Type), ev.Host, string(meta))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	if eventAppends.Add(1)%eventPruneEvery == 0 {
		if _, err := r.Prune(ctx); err != nil {
			return id, err
		}
	}
	return id, nil
}

// Prune applies the retention policy and returns the number of events removed.
func (r *Events) Prune(ctx context.Context) (int64, error) {
	var total int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM events WHERE ts < ?", ts(r.s.now().Add(-r.s.eventMaxAge)))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		total += n
		res, err = tx.ExecContext(ctx, "DELETE FROM events WHERE id <= (SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET ?)", r.s.eventMaxRows)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		total += n
		return nil
	})
	return total, err
}

// Since returns up to limit events with id greater than after, oldest first.
func (r *Events) Since(ctx context.Context, after int64, limit int) ([]StoredEvent, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := r.s.r.QueryContext(ctx, "SELECT id, ts, type, host, metadata FROM events WHERE id > ? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredEvent
	for rows.Next() {
		var se StoredEvent
		var t int64
		var typ, meta string
		if err := rows.Scan(&se.ID, &t, &typ, &se.Event.Host, &meta); err != nil {
			return nil, err
		}
		se.Event.Type = api.EventType(typ)
		se.Event.Timestamp = fromTS(t)
		se.Event.Metadata = json.RawMessage(meta)
		out = append(out, se)
	}
	return out, rows.Err()
}

// Count returns the number of events in the ring.
func (r *Events) Count(ctx context.Context) (int, error) {
	var n int
	err := r.s.r.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&n)
	return n, err
}
