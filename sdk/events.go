package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"iter"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// EventOption tunes Events.
type EventOption func(*eventCfg)

type eventCfg struct {
	types      []api.EventType
	lastID     string
	maxFailing int
}

// Types restricts the stream to the given event types.
func Types(t ...api.EventType) EventOption { return func(e *eventCfg) { e.types = t } }

// FromID resumes after a known event id (sent as Last-Event-ID).
func FromID(id string) EventOption { return func(e *eventCfg) { e.lastID = id } }

// MaxReconnects bounds consecutive failed reconnects before the iterator ends
// with the last error (default 10, negative means unlimited).
func MaxReconnects(n int) EventOption { return func(e *eventCfg) { e.maxFailing = n } }

const maxEventLine = 1 << 20

// Events streams /1.0/events (Server-Sent Events). The stream reconnects
// after a drop with the Last-Event-ID of the last event delivered, so events
// are not lost while the server retains them. The iterator ends when ctx is
// done (yielding ctx.Err()), the client is closed, the consumer breaks, or a
// non-retryable error occurs (unauthorized, forbidden, not found, unsupported).
func (c *Client) Events(ctx context.Context, opts ...EventOption) iter.Seq2[api.Event, error] {
	cfg := eventCfg{maxFailing: 10}
	for _, o := range opts {
		o(&cfg)
	}
	return func(yield func(api.Event, error) bool) {
		if err := c.need("events"); err != nil {
			yield(api.Event{}, err)
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(c.t.ctx, cancel)
		defer stop()
		q := url.Values{}
		if len(cfg.types) > 0 {
			ts := make([]string, len(cfg.types))
			for i, t := range cfg.types {
				ts[i] = string(t)
			}
			q.Set("type", strings.Join(ts, ","))
		}
		failing := 0
		retryWait := time.Duration(0)
		for {
			if c.t.ctx.Err() != nil {
				yield(api.Event{}, ErrClosed)
				return
			}
			if ctx.Err() != nil {
				yield(api.Event{}, ctx.Err())
				return
			}
			n, fatal, err := c.streamOnce(ctx, q, &cfg, &retryWait, yield)
			if n < 0 { // consumer stopped
				return
			}
			if n > 0 {
				failing = 0
			}
			if fatal {
				yield(api.Event{}, err)
				return
			}
			if ctx.Err() != nil {
				continue // reported at the top
			}
			failing++
			if cfg.maxFailing >= 0 && failing > cfg.maxFailing {
				if err == nil {
					err = api.Errorf(api.CodeInternal, "event stream closed repeatedly")
				}
				yield(api.Event{}, err)
				return
			}
			d := retryWait
			if d <= 0 {
				ceil := min(c.t.cfg.backoff<<min(failing-1, 16), c.t.cfg.backoffMax)
				d = rand.N(ceil + 1)
			}
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
		}
	}
}

// streamOnce runs one connection. It returns the number of events delivered
// (-1 when the consumer stopped), whether the error is fatal, and the error.
func (c *Client) streamOnce(ctx context.Context, q url.Values, cfg *eventCfg, retryWait *time.Duration, yield func(api.Event, error) bool) (int, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL("/1.0/events", q), nil)
	if err != nil {
		return 0, true, err
	}
	c.decorate(req, newRequestID())
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if cfg.lastID != "" {
		req.Header.Set("Last-Event-ID", cfg.lastID)
	}
	resp, err := c.t.sc.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 0, 4096)
		b := make([]byte, 4096)
		nr, _ := resp.Body.Read(b)
		buf = append(buf, b[:nr]...)
		_, derr := decodeResponse(resp.StatusCode, buf)
		if derr == nil {
			derr = errorFromStatus(resp.StatusCode, buf)
		}
		switch resp.StatusCode {
		case 429, 502, 503, 504:
			return 0, false, derr
		}
		return 0, resp.StatusCode < 500, derr
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventLine)
	var id, name string
	var data []string
	delivered := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(data) > 0 {
				var ev api.Event
				payload := strings.Join(data, "\n")
				if jerr := json.Unmarshal([]byte(payload), &ev); jerr != nil {
					if !yield(api.Event{}, api.Errorf(api.CodeInternal, "bad event payload: %v", jerr)) {
						return -1, true, nil
					}
				} else {
					if ev.Type == "" {
						ev.Type = api.EventType(name)
					}
					if id != "" {
						cfg.lastID = id
					}
					delivered++
					if !yield(ev, nil) {
						return -1, true, nil
					}
				}
			}
			data, name = nil, ""
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			name = value
		case "data":
			data = append(data, value)
		case "retry":
			if ms, err := strconv.Atoi(value); err == nil && ms >= 0 {
				*retryWait = time.Duration(ms) * time.Millisecond
			}
		}
	}
	return delivered, false, sc.Err()
}
