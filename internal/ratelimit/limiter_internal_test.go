package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// TestPruneDropsIdleKeys proves the sweep actually deletes abandoned buckets,
// which an external test cannot observe.
func TestPruneDropsIdleKeys(t *testing.T) {
	l := New(60)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	l.SetNow(func() time.Time { return now })

	for i := 0; i < 5000; i++ {
		l.Allow(fmt.Sprintf("peer-%d", i))
	}
	if len(l.buckets) < 4096 {
		t.Fatalf("buckets = %d, want the map over the sweep threshold", len(l.buckets))
	}
	before := len(l.buckets)

	now = now.Add(2 * time.Minute)
	l.Allow("trigger-prune")
	if got := len(l.buckets); got >= before {
		t.Errorf("buckets after idle prune = %d, want fewer than %d", got, before)
	}
	for key, b := range l.buckets {
		if key != "trigger-prune" && b.tokens < l.capacity {
			t.Errorf("kept a non-full bucket %q with %v tokens", key, b.tokens)
		}
	}
}
