package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClock is a clock that moves only when the loop sleeps or an attempt
// takes time.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}

// The health check is bounded by a deadline, not by a number of attempts:
// each attempt gets min(5s, the time left), and the loop ends within
// wait_seconds and one attempt, however long the attempts take.
func TestHealthLoop(t *testing.T) {
	refused := errors.New("connection refused")
	for _, c := range []struct {
		name string
		wait time.Duration
		// took is how long each attempt takes; pass is the attempt that passes (0: none)
		took     time.Duration
		pass     int
		timeouts []time.Duration
		ok       bool
	}{
		{"passes at once", 60 * time.Second, 0, 1, []time.Duration{5 * time.Second}, true},
		{"passes on the third", 60 * time.Second, 0, 3, []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second}, true},
		{"fast failures, once a second", 3 * time.Second, 0, 0, []time.Duration{3 * time.Second, 2 * time.Second, time.Second}, false},
		{"slow attempts stop at the deadline", 12 * time.Second, 5 * time.Second, 0, []time.Duration{5 * time.Second, 5 * time.Second}, false},
		{"the last attempt gets what is left", 8 * time.Second, 2 * time.Second, 0, []time.Duration{5 * time.Second, 5 * time.Second, 2 * time.Second}, false},
		{"one second", time.Second, 0, 0, []time.Duration{time.Second}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			clk := &fakeClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
			start := clk.t
			var got []time.Duration
			err := healthLoop(context.Background(), clk.now, clk.sleep, c.wait, func(timeout time.Duration) error {
				got = append(got, timeout)
				clk.t = clk.t.Add(min(c.took, timeout))
				if len(got) == c.pass {
					return nil
				}
				return refused
			})
			if (err == nil) != c.ok || (err != nil && (!errors.Is(err, refused) || !strings.Contains(err.Error(), "within"))) {
				t.Fatalf("%v", err)
			}
			if !slices.Equal(got, c.timeouts) {
				t.Fatalf("attempt timeouts %v, want %v", got, c.timeouts)
			}
			if spent := clk.t.Sub(start); spent > c.wait+5*time.Second {
				t.Fatalf("took %s for a wait of %s", spent, c.wait)
			}
		})
	}
	// a cancelled context ends the loop after the attempt
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clk := &fakeClock{t: time.Unix(0, 0)}
	n := 0
	if err := healthLoop(ctx, clk.now, clk.sleep, time.Minute, func(time.Duration) error { n++; return refused }); err == nil || n != 1 {
		t.Fatalf("%v %d", err, n)
	}
}
