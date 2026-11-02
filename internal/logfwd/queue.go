package logfwd

import (
	"math/rand/v2"
	"time"
)

// queue is a bounded FIFO of entries. Pushing onto a full queue drops the
// oldest entry and counts it: a sink that cannot keep up loses the oldest
// lines, never the daemon's memory.
type queue struct {
	buf     []Entry
	head, n int
	dropped int64
}

func newQueue(capacity int) *queue { return &queue{buf: make([]Entry, capacity)} }

func (q *queue) len() int { return q.n }

// push appends e; on a full queue it drops the oldest entry and returns it.
func (q *queue) push(e Entry) (dropped Entry, ok bool) {
	if q.n == len(q.buf) {
		dropped, ok = q.buf[q.head], true
		q.buf[q.head] = Entry{}
		q.head = (q.head + 1) % len(q.buf)
		q.n--
		q.dropped++
	}
	q.buf[(q.head+q.n)%len(q.buf)] = e
	q.n++
	return dropped, ok
}

// peek returns up to max entries from the front without removing them.
func (q *queue) peek(max int) []Entry {
	k := min(max, q.n)
	out := make([]Entry, k)
	for i := range k {
		out[i] = q.buf[(q.head+i)%len(q.buf)]
	}
	return out
}

// pop removes k entries from the front.
func (q *queue) pop(k int) {
	k = min(k, q.n)
	for i := range k {
		q.buf[(q.head+i)%len(q.buf)] = Entry{}
	}
	q.head = (q.head + k) % len(q.buf)
	q.n -= k
}

// Backoff bounds.
const (
	backoffBase = time.Second
	backoffMax  = 5 * time.Minute
)

// backoff is the wait after the n-th failure in a row (n >= 1): it doubles
// from one second up to five minutes, and jitter (in [0,1)) takes it to a
// point between half of that and all of it, so sinks that fail together do
// not retry together.
func backoff(n int, jitter float64) time.Duration {
	d := backoffBase
	for i := 1; i < n && d < backoffMax; i++ {
		d *= 2
	}
	d = min(d, backoffMax)
	return d/2 + time.Duration(jitter*float64(d/2))
}

// Clock is the time source of the forwarder; tests drive a fake one.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func defaultJitter() float64 { return rand.Float64() }
