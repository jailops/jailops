package state

import (
	"sync"

	"jailops.net/jailops/sdk/api"
)

// EventType classifies a change.
type EventType string

// Event types.
const (
	EventAdded    EventType = "added"
	EventModified EventType = "modified"
	EventDeleted  EventType = "deleted"
	// EventResync means events were dropped because the watcher was too
	// slow; the watcher must List again.
	EventResync EventType = "resync"
)

// Event describes one committed change.
type Event struct {
	Type       EventType
	Kind       api.Kind
	UUID       string
	Name       string
	Project    string
	Generation int64
}

type watcher struct {
	ch   chan Event
	lost bool
}

type watchers struct {
	mu   sync.Mutex
	list map[*watcher]struct{}
}

func (w *watchers) publish(ev Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for sub := range w.list {
		if sub.lost {
			select {
			case sub.ch <- Event{Type: EventResync}:
				sub.lost = false
			default:
			}
		}
		select {
		case sub.ch <- ev:
		default:
			sub.lost = true
		}
	}
}

func (w *watchers) closeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for sub := range w.list {
		close(sub.ch)
	}
	w.list = nil
}

// Watch subscribes to change events with the given channel buffer. Events are
// never delivered blockingly: a full buffer drops events and the next
// delivery is preceded by an EventResync. Call cancel to unsubscribe; the
// channel is closed when the store closes or cancel is called.
func (s *Store) Watch(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	sub := &watcher{ch: make(chan Event, buffer)}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		close(sub.ch)
		return sub.ch, func() {}
	}
	s.watchers.mu.Lock()
	if s.watchers.list == nil {
		s.watchers.list = map[*watcher]struct{}{}
	}
	s.watchers.list[sub] = struct{}{}
	s.watchers.mu.Unlock()
	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			s.watchers.mu.Lock()
			defer s.watchers.mu.Unlock()
			if _, ok := s.watchers.list[sub]; ok {
				delete(s.watchers.list, sub)
				close(sub.ch)
			}
		})
	}
}
