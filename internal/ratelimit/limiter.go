// Package ratelimit is the per-token task budget the A2A surface enforces:
// a token bucket refilled evenly over a one-minute window, so one peer cannot
// pin the single chat-model host.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a keyed token bucket. The zero value is unusable; use New.
type Limiter struct {
	perMinute float64
	capacity  float64
	refill    float64 // tokens per second
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New builds a limiter allowing perMinute tasks per key. A value <= 0 disables
// limiting, which is what the loopback dev server uses when no rate flag is
// given.
func New(perMinute int) *Limiter {
	l := &Limiter{now: time.Now, buckets: make(map[string]*bucket)}
	if perMinute > 0 {
		l.perMinute = float64(perMinute)
		l.capacity = float64(perMinute)
		l.refill = float64(perMinute) / 60
	}
	return l
}

// SetNow overrides the clock. It exists for deterministic tests; production
// code leaves the default wall clock in place.
func (l *Limiter) SetNow(now func() time.Time) {
	if l == nil || now == nil {
		return
	}
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// Allow consumes one task from key's bucket. When the bucket is empty it
// returns false and the duration until one task is available, so the caller
// can send a Retry-After header.
func (l *Limiter) Allow(key string) (time.Duration, bool) {
	if l == nil || l.capacity == 0 {
		return 0, true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)
	b, ok := l.buckets[key]
	if !ok {
		// A new peer starts with a full bucket: the limit is a sustained
		// rate, not a cold-start penalty.
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.refill
		if b.tokens > l.capacity {
			b.tokens = l.capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	need := (1 - b.tokens) / l.refill
	return time.Duration(need * float64(time.Second)), false
}

// pruneLocked drops full buckets that have been idle for a minute. Without it
// a token id churn would grow the map without bound; the thresholds keep the
// sweep off the request path in normal use.
func (l *Limiter) pruneLocked(now time.Time) {
	if len(l.buckets) < 4096 {
		return
	}
	for k, b := range l.buckets {
		if b.tokens >= l.capacity && now.Sub(b.last) > time.Minute {
			delete(l.buckets, k)
		}
	}
}
