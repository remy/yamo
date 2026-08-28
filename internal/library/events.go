package library

import (
	"sync"
	"time"
)

// Event types published on the change bus.
const (
	EventTracksChanged   = "tracks.changed"
	EventCatalogReplaced = "catalog.replaced"
	EventJobProgress     = "job.progress"
	EventJobFinished     = "job.finished"
	EventClipboard       = "clipboard.changed"
)

// Event is a change worth telling other clients about.
//
// This is what lets several interfaces stay in step: an edit made on a phone
// pushes tracks.changed, and the terminal drops the affected rows from its
// cache instead of polling for them.
type Event struct {
	Type     string    `json:"type"`
	At       time.Time `json:"at"`
	TrackIDs []string  `json:"trackIds,omitempty"`
	Job      *Job      `json:"job,omitempty"`
}

// eventBus fans events out to subscribers.
type eventBus struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	next   int
	closed bool
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[int]chan Event{}}
}

// eventBuffer is how far a subscriber may fall behind before it starts losing
// events. A slow client must never block the write that produced one.
const eventBuffer = 64

// Subscribe returns a channel of events and a function to stop listening.
func (b *eventBus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	id := b.next
	b.next++
	ch := make(chan Event, eventBuffer)
	b.subs[id] = ch

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// publish delivers an event to every subscriber, dropping it for any that has
// fallen behind rather than stalling the caller.
func (b *eventBus) publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *eventBus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
