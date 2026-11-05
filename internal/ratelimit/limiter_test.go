package ratelimit_test

import (
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/ratelimit"
)

// clock is a manually advanced time source.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newLimiter(t *testing.T, perMinute int) (*ratelimit.Limiter, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	l := ratelimit.New(perMinute)
	l.SetNow(c.Now)
	return l, c
}

func TestLimiterAllowsTheBurstThenRefuses(t *testing.T) {
	l, _ := newLimiter(t, 3)
	for i := 0; i < 3; i++ {
		if _, ok := l.Allow("alice"); !ok {
			t.Fatalf("task %d refused inside the burst", i+1)
		}
	}
	retry, ok := l.Allow("alice")
	if ok {
		t.Fatal("the fourth task was allowed")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("retryAfter = %v, want a positive duration under a minute", retry)
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	l, c := newLimiter(t, 2)
	for i := 0; i < 2; i++ {
		l.Allow("alice")
	}
	if _, ok := l.Allow("alice"); ok {
		t.Fatal("bucket should be empty")
	}
	// Two per minute = one token every 30s.
	c.Advance(30 * time.Second)
	if _, ok := l.Allow("alice"); !ok {
		t.Fatal("one token was not refilled after half a minute")
	}
	if _, ok := l.Allow("alice"); ok {
		t.Fatal("a second task was allowed from one refilled token")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newLimiter(t, 1)
	if _, ok := l.Allow("alice"); !ok {
		t.Fatal("alice refused")
	}
	if _, ok := l.Allow("bob"); !ok {
		t.Fatal("bob's bucket was affected by alice's")
	}
}

func TestLimiterDisabled(t *testing.T) {
	l, _ := newLimiter(t, 0)
	for i := 0; i < 100; i++ {
		if _, ok := l.Allow("alice"); !ok {
			t.Fatalf("task %d refused with limiting disabled", i+1)
		}
	}
	var nilLimiter *ratelimit.Limiter
	if _, ok := nilLimiter.Allow("alice"); !ok {
		t.Fatal("a nil limiter refused a task")
	}
}
