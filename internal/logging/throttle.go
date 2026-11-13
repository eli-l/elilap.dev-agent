package logging

import (
	"sync"
	"time"
)

// maxThrottleKeys is the hard bound on tracked keys. Past it, new keys are not
// tracked and their lines are suppressed, because the map must not grow with a
// caller-controlled key space.
const maxThrottleKeys = 4096

// Throttle allows one log line per key per window. It exists for paths that are
// reachable without credentials — a rate-limit refusal, a budget refusal — where
// an unbounded warning stream is itself a denial of service.
//
// The zero value is usable and behaves as NewThrottle(time.Minute).
type Throttle struct {
	mu        sync.Mutex
	window    time.Duration
	last      map[string]time.Time
	lastPrune time.Time
}

// NewThrottle builds a throttle with the given window.
func NewThrottle(window time.Duration) *Throttle {
	return &Throttle{window: window, last: make(map[string]time.Time)}
}

// Allow reports whether key may log now, and records the attempt. A key beyond
// maxThrottleKeys is suppressed rather than tracked.
func (t *Throttle) Allow(key string) bool {
	if t == nil {
		return false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	window := t.window
	if window <= 0 {
		window = time.Minute
	}
	if t.last == nil {
		t.last = make(map[string]time.Time)
	}
	if last, ok := t.last[key]; ok {
		if now.Sub(last) < window {
			return false
		}
	} else if len(t.last) >= maxThrottleKeys {
		// Sweep at most once per window, so a rotating key cannot turn this
		// into an O(n) walk per request.
		if now.Sub(t.lastPrune) < window {
			return false
		}
		t.pruneLocked(now, window)
		if len(t.last) >= maxThrottleKeys {
			return false
		}
	}
	t.last[key] = now
	t.pruneLocked(now, window)
	return true
}

// pruneLocked drops expired keys, at most once per window.
func (t *Throttle) pruneLocked(now time.Time, window time.Duration) {
	if now.Sub(t.lastPrune) < window {
		return
	}
	t.lastPrune = now
	for k, seen := range t.last {
		if now.Sub(seen) >= window {
			delete(t.last, k)
		}
	}
}
