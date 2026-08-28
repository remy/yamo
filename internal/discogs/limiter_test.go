package discogs

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A spent budget must fail with a stated wait rather than block. The wait
// matters more than it looks: a search fires several requests at once, and a
// browser call that hangs for the rest of the minute reads as a broken server
// rather than as a rate limit.
func TestLimiterRefusesWhenSpentAndSaysWhen(t *testing.T) {
	l := newLimiter(3)
	for i := 0; i < 3; i++ {
		if err := l.take(context.Background()); err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
	}
	if _, rem := l.state(); rem != 0 {
		t.Errorf("remaining = %d, want 0", rem)
	}

	var limited *RateLimitError
	err := l.take(context.Background())
	if !errors.As(err, &limited) {
		t.Fatalf("take on a spent budget = %v, want a RateLimitError", err)
	}
	if limited.RetryAfter <= 0 || limited.RetryAfter > window {
		t.Errorf("RetryAfter = %v, want something inside the window", limited.RetryAfter)
	}
}

// Requests falling out of the window free their slots again.
func TestLimiterSlidesTheWindow(t *testing.T) {
	l := newLimiter(2)
	old := time.Now().Add(-2 * window)
	l.at = []time.Time{old, old}

	if _, rem := l.state(); rem != 2 {
		t.Errorf("remaining = %d, want 2 once the old requests aged out", rem)
	}
	if err := l.take(context.Background()); err != nil {
		t.Errorf("take after the window rolled: %v", err)
	}
}

// The budget is per IP, so anything else on the machine talking to Discogs
// spends from the same allowance. When the server's own count is lower than
// the local one, the server's has to win or this client keeps firing requests
// that come back 429.
func TestLimiterTrustsTheServersCount(t *testing.T) {
	l := newLimiter(25)
	if err := l.take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, rem := l.state(); rem != 24 {
		t.Fatalf("remaining = %d, want 24 before the server is heard from", rem)
	}

	h := http.Header{}
	h.Set("X-Discogs-Ratelimit", "25")
	h.Set("X-Discogs-Ratelimit-Remaining", "2")
	l.observe(h)

	if _, rem := l.state(); rem != 2 {
		t.Errorf("remaining = %d, want 2 — the server said so", rem)
	}
	// Spending after that report has to come off the reported figure too.
	if err := l.take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, rem := l.state(); rem != 1 {
		t.Errorf("remaining = %d, want 1", rem)
	}
}

// A limit the server reports replaces the assumed one, so a client configured
// with a token that Discogs does not honour still paces itself correctly.
func TestLimiterAdoptsTheServersLimit(t *testing.T) {
	l := newLimiter(60)
	h := http.Header{}
	h.Set("X-Discogs-Ratelimit", "25")
	h.Set("X-Discogs-Ratelimit-Remaining", "20")
	l.observe(h)

	if lim, _ := l.state(); lim != 25 {
		t.Errorf("limit = %d, want the 25 the server reported", lim)
	}
}

// Headers that are missing or nonsense must leave the local count alone
// rather than zeroing it.
func TestLimiterIgnoresJunkHeaders(t *testing.T) {
	l := newLimiter(25)
	l.observe(http.Header{})
	l.observe(http.Header{"X-Discogs-Ratelimit-Remaining": []string{"banana"}})
	if lim, rem := l.state(); lim != 25 || rem != 25 {
		t.Errorf("limit/remaining = %d/%d, want 25/25", lim, rem)
	}
}

// A cancelled context must unblock a waiting caller.
func TestLimiterHonoursContext(t *testing.T) {
	l := newLimiter(1)
	if err := l.take(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One slot, spent just now, so the next take would wait for the window to
	// roll — long past maxWait, hence a refusal rather than a block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.take(ctx); err == nil {
		t.Error("take succeeded on a spent budget with a cancelled context")
	}
}
