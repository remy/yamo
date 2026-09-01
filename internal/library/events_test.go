package library

import (
	"testing"
)

// A client that resumes has to see what it missed before it sees what
// happened since, or it would apply the two out of order.
func TestEventReplay(t *testing.T) {
	b := newEventBus()
	epoch := b.Epoch()

	b.publish(Event{Type: EventTracksChanged, TrackIDs: []string{"a"}})
	b.publish(Event{Type: EventTracksChanged, TrackIDs: []string{"b"}})
	b.publish(Event{Type: EventTracksChanged, TrackIDs: []string{"c"}})

	_, backlog, cancel := b.SubscribeFrom(epoch, 1)
	defer cancel()

	if len(backlog) != 2 {
		t.Fatalf("resuming after 1 replayed %d events, want 2: %+v", len(backlog), backlog)
	}
	if backlog[0].Seq != 2 || backlog[1].Seq != 3 {
		t.Errorf("replayed sequence numbers %d and %d, want 2 and 3", backlog[0].Seq, backlog[1].Seq)
	}
	if backlog[0].TrackIDs[0] != "b" {
		t.Errorf("the backlog starts at %v, want the event after the one the client saw", backlog[0].TrackIDs)
	}
}

// Every event carries a sequence, from 1, so an id is meaningful the moment
// the first one arrives.
func TestEventSequence(t *testing.T) {
	b := newEventBus()
	ch, _, cancel := b.SubscribeFrom("", 0)
	defer cancel()

	b.publish(Event{Type: EventTracksChanged})
	b.publish(Event{Type: EventCatalogReplaced})

	for want := uint64(1); want <= 2; want++ {
		e := <-ch
		if e.Seq != want {
			t.Errorf("event %d has seq %d", want, e.Seq)
		}
		if e.Epoch != b.Epoch() {
			t.Errorf("event %d carries epoch %q, want %q", want, e.Epoch, b.Epoch())
		}
	}
}

// A client already up to date gets nothing rather than a spurious gap.
func TestEventReplayUpToDate(t *testing.T) {
	b := newEventBus()
	b.publish(Event{Type: EventTracksChanged})

	_, backlog, cancel := b.SubscribeFrom(b.Epoch(), 1)
	defer cancel()
	if len(backlog) != 0 {
		t.Errorf("a client already up to date got %d events: %+v", len(backlog), backlog)
	}
}

// An id from a previous run of the server means something else now, so
// resuming from it must produce a gap rather than the wrong events.
func TestEventReplayAcrossRestart(t *testing.T) {
	b := newEventBus()
	b.publish(Event{Type: EventTracksChanged})
	b.publish(Event{Type: EventTracksChanged})

	_, backlog, cancel := b.SubscribeFrom("an-epoch-from-a-previous-run", 1)
	defer cancel()

	if len(backlog) != 1 || backlog[0].Type != EventGap {
		t.Fatalf("resuming across a restart gave %+v, want one gap", backlog)
	}
	if backlog[0].Missed != -1 {
		t.Errorf("missed = %d, want -1: the count is unknowable across a restart", backlog[0].Missed)
	}
}

// Falling further behind than the history goes is a gap, not silence: the
// client has to be told to refetch rather than trusting its cache.
func TestEventReplayBeyondHistory(t *testing.T) {
	b := newEventBus()
	for i := 0; i < eventHistory+10; i++ {
		b.publish(Event{Type: EventTracksChanged})
	}

	_, backlog, cancel := b.SubscribeFrom(b.Epoch(), 1)
	defer cancel()

	if len(backlog) != 1 || backlog[0].Type != EventGap {
		t.Fatalf("resuming past the window gave %d events, want one gap", len(backlog))
	}
	if backlog[0].Missed <= 0 {
		t.Errorf("missed = %d, want a positive count", backlog[0].Missed)
	}
}

// The history is bounded, or a long-running server would hold every change it
// ever made — and these events carry job results and id lists.
func TestEventHistoryIsBounded(t *testing.T) {
	b := newEventBus()
	for i := 0; i < eventHistory*3; i++ {
		b.publish(Event{Type: EventTracksChanged})
	}
	b.mu.Lock()
	n := len(b.history)
	last := b.history[len(b.history)-1].Seq
	b.mu.Unlock()

	if n != eventHistory {
		t.Errorf("history holds %d events, want it capped at %d", n, eventHistory)
	}
	if last != uint64(eventHistory*3) {
		t.Errorf("the newest event is %d, want %d — the window kept the wrong end", last, eventHistory*3)
	}
}

// Not resuming means not being handed a backlog at all.
func TestSubscribeWithoutResume(t *testing.T) {
	b := newEventBus()
	b.publish(Event{Type: EventTracksChanged})

	_, backlog, cancel := b.SubscribeFrom("", 0)
	defer cancel()
	if len(backlog) != 0 {
		t.Errorf("a fresh subscriber got %d replayed events", len(backlog))
	}
}
