// Package ratelimit is the keyed minute bucket behind the answer budget, the
// private /v1/retrieve embedding path, and failed bearer-auth attempts: a token
// bucket refilled evenly over a one-minute window, so one peer cannot pin the
// single chat-model host.
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

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastPrune time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New builds a limiter allowing perMinute tasks per key. A value <= 0 disables
// limiting; `agent serve --rate-limit 0` is the documented way to opt out.
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
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	l.pruneLocked(now)
	b, ok := l.buckets[key]
	if !ok {
		// A new peer starts with a full bucket: the limit is a sustained
		// rate, not a cold-start penalty.
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	l.addTokens(b, now)
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	need := (1 - b.tokens) / l.refill
	return time.Duration(need * float64(time.Second)), false
}

// addTokens adds the tokens earned since b.last, capped at capacity.
func (l *Limiter) addTokens(b *bucket, now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * l.refill
	if b.tokens > l.capacity {
		b.tokens = l.capacity
	}
	b.last = now
}

// pruneLocked drops buckets that have been idle long enough to be fully
// refilled; deleting such a bucket is equivalent to keeping it. Without this a
// token-id or address churn would grow the map without bound. The sweep runs
// at most once a minute and only once the map is large, so it stays off the
// request path in normal use.
func (l *Limiter) pruneLocked(now time.Time) {
	if len(l.buckets) < 4096 {
		return
	}
	if now.Sub(l.lastPrune) < time.Minute {
		return
	}
	l.lastPrune = now
	for k, b := range l.buckets {
		idle := now.Sub(b.last)
		if idle > time.Minute && b.tokens+idle.Seconds()*l.refill >= l.capacity {
			delete(l.buckets, k)
		}
	}
}
