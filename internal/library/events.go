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

	// EventGap is synthesised for one subscriber rather than published to
	// all of them. It says that events between the one the client last saw
	// and the replay it is about to receive have been lost, so the client
	// knows to refetch rather than to trust its cache.
	EventGap = "stream.gap"
)

// Event is a change worth telling other clients about.
//
// This is what lets several interfaces stay in step: an edit made on a phone
// pushes tracks.changed, and the terminal drops the affected rows from its
// cache instead of polling for them.
type Event struct {
	// Seq numbers events within one run of the server, from 1. It is what a
	// client sends back as Last-Event-ID to resume, and it resets when the
	// server restarts — which is why Epoch exists beside it.
	Seq   uint64 `json:"seq"`
	Epoch string `json:"epoch"`

	Type     string    `json:"type"`
	At       time.Time `json:"at"`
	TrackIDs []string  `json:"trackIds,omitempty"`
	Job      *Job      `json:"job,omitempty"`

	// Missed is set only on a gap event: how many events the subscriber can
	// no longer be shown, or -1 when the count is unknowable because the
	// server restarted underneath it.
	Missed int64 `json:"missed,omitempty"`
}

// eventBus fans events out to subscribers.
type eventBus struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	next   int
	closed bool

	// epoch identifies this run of the server. A sequence number resets when
	// the process restarts, so a client resuming with an id from a previous
	// run must be told to refetch rather than silently handed the wrong
	// events; the epoch is what makes that detectable.
	epoch string

	// seq is the last sequence number issued, and history holds the most
	// recent events for replay. A subscriber that reconnects within the
	// window catches up exactly; one that has been away longer is told it
	// missed something, which is the honest answer.
	seq     uint64
	history []Event
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[int]chan Event{}, epoch: newJobID()}
}

// eventBuffer is how far a subscriber may fall behind before it starts losing
// events. A slow client must never block the write that produced one.
const eventBuffer = 64

// eventHistory is how many recent events are kept for replay. A reconnect
// after a phone's radio drops takes seconds and a batch edit publishes one
// event for the whole job, so a few hundred covers the case this exists for
// without holding the library's change log for ever.
const eventHistory = 256

// Epoch identifies this run of the server, so a client can tell a resumable
// sequence number from one issued before a restart.
func (b *eventBus) Epoch() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

// Subscribe returns a channel of events and a function to stop listening.
func (b *eventBus) Subscribe() (<-chan Event, func()) {
	ch, _, cancel := b.SubscribeFrom("", 0)
	return ch, cancel
}

// SubscribeFrom returns a channel of events, the backlog the caller missed,
// and a function to stop listening.
//
// When epoch matches this run and after names an event still in the history,
// the backlog is exactly the events since it. Otherwise the backlog holds a
// single gap event: the client asked to resume from somewhere this bus can no
// longer reconstruct, and saying so is what tells it to refetch. An empty
// epoch means the caller is not resuming and wants no backlog at all.
func (b *eventBus) SubscribeFrom(epoch string, after uint64) (<-chan Event, []Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan Event)
		close(ch)
		return ch, nil, func() {}
	}

	var backlog []Event
	if epoch != "" {
		backlog = b.replayLocked(epoch, after)
	}

	id := b.next
	b.next++
	ch := make(chan Event, eventBuffer)
	b.subs[id] = ch

	return ch, backlog, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// replayLocked returns the events after a sequence number, or a gap event when
// they can no longer be produced. The caller holds the lock.
func (b *eventBus) replayLocked(epoch string, after uint64) []Event {
	gap := func(missed int64) []Event {
		return []Event{{
			Seq: b.seq, Epoch: b.epoch, Type: EventGap,
			At: time.Now(), Missed: missed,
		}}
	}

	if epoch != b.epoch {
		// A sequence number from a previous run of the server. Its events are
		// gone with the process that issued them and the numbers themselves
		// now mean something else, so how much was missed is unknowable.
		return gap(-1)
	}
	if after >= b.seq {
		return nil // already up to date, or ahead of us; nothing to replay
	}
	if len(b.history) == 0 {
		return gap(int64(b.seq - after))
	}
	// The oldest event still held must be the very next one the client needs.
	// If it is any later, the events between have been dropped from the window
	// — and replaying from here would hand the client a run of events with a
	// silent hole at the front, which is worse than telling it to refetch.
	if b.history[0].Seq > after+1 {
		return gap(int64(b.history[0].Seq - after - 1))
	}
	for i, e := range b.history {
		if e.Seq > after {
			return append([]Event(nil), b.history[i:]...)
		}
	}
	return gap(int64(b.seq - after))
}

// publish delivers an event to every subscriber, dropping it for any that has
// fallen behind rather than stalling the caller.
func (b *eventBus) publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.seq++
	e.Seq, e.Epoch = b.seq, b.epoch

	b.history = append(b.history, e)
	if len(b.history) > eventHistory {
		// Copy down rather than reslice: reslicing keeps the whole backing
		// array alive, and these events hold job results and id lists.
		b.history = append(b.history[:0], b.history[len(b.history)-eventHistory:]...)
	}

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
