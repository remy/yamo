package discogs

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// window is the period the Discogs budget is counted over.
const window = time.Minute

// maxWait bounds how long a call will block waiting for a slot.
//
// Waiting a little is right: a search fires a handful of requests together and
// briefly queueing them is better than failing one. Waiting a lot is not — a
// browser request that hangs for forty seconds looks broken, and the honest
// answer is to say the budget is spent and when it refills.
const maxWait = 3 * time.Second

// limiter is a sliding-window counter over the last minute of requests.
//
// A sliding window rather than a bucket because that is what Discogs itself
// enforces, and a bucket refilling smoothly would let a burst through that the
// server then rejects. It is deliberately conservative: it counts a request as
// spent before it is sent, so a failure still costs its slot, which is also how
// Discogs counts it.
type limiter struct {
	mu    sync.Mutex
	limit int
	at    []time.Time // when each recent request was sent, oldest first

	// serverRemaining is what Discogs last said was left, and when it said so.
	// The limit is per IP, so another client on this machine spends from the
	// same allowance and the local count alone is optimistic.
	serverRemaining int
	serverAt        time.Time
}

func newLimiter(limit int) *limiter {
	return &limiter{limit: limit, serverRemaining: -1}
}

// prune drops requests that have fallen out of the window. Callers hold mu.
func (l *limiter) prune(now time.Time) {
	cut := now.Add(-window)
	i := 0
	for i < len(l.at) && !l.at[i].After(cut) {
		i++
	}
	l.at = l.at[i:]
}

// remainingLocked is the smaller of what this process has spent and what the
// server last reported, so whichever is more pessimistic wins.
func (l *limiter) remainingLocked(now time.Time) int {
	n := l.limit - len(l.at)
	if l.serverRemaining >= 0 && now.Sub(l.serverAt) < window {
		// The server's figure was true when it was sent; anything spent since
		// has to come off it as well.
		spent := 0
		for _, t := range l.at {
			if t.After(l.serverAt) {
				spent++
			}
		}
		if s := l.serverRemaining - spent; s < n {
			n = s
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

// state reports the limit and what is left of it.
func (l *limiter) state() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.prune(now)
	return l.limit, l.remainingLocked(now)
}

// take reserves a slot, waiting briefly for one if the window is full.
func (l *limiter) take(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		l.prune(now)
		if l.remainingLocked(now) > 0 {
			l.at = append(l.at, now)
			l.mu.Unlock()
			return nil
		}
		wait := window
		if len(l.at) > 0 {
			wait = window - now.Sub(l.at[0])
		}
		l.mu.Unlock()

		if wait <= 0 {
			continue // the window just rolled; re-check rather than spin-wait
		}
		if wait > maxWait {
			return &RateLimitError{RetryAfter: wait}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// observe folds the counters Discogs returns into the local view.
func (l *limiter) observe(h http.Header) {
	rem, err := strconv.Atoi(h.Get("X-Discogs-Ratelimit-Remaining"))
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n, err := strconv.Atoi(h.Get("X-Discogs-Ratelimit")); err == nil && n > 0 {
		l.limit = n
	}
	l.serverRemaining, l.serverAt = rem, time.Now()
}
