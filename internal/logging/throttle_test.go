package logging_test

import (
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

func TestThrottleZeroValueIsUsable(t *testing.T) {
	var throttle logging.Throttle
	if !throttle.Allow("alice") {
		t.Fatal("the zero value suppressed the first line")
	}
	if throttle.Allow("alice") {
		t.Fatal("the zero value allowed a second line in the same window")
	}
}

func TestThrottleBoundsLogs(t *testing.T) {
	throttle := logging.NewThrottle(time.Minute)
	if !throttle.Allow("alice") {
		t.Fatal("first log was suppressed")
	}
	if throttle.Allow("alice") {
		t.Fatal("second log in the same window was allowed")
	}
	if !throttle.Allow("bob") {
		t.Fatal("another key was suppressed")
	}
}

// TestThrottleBoundsKeyCardinality pins the hard cap: a caller that rotates the
// key (an unauthenticated, self-asserted user id, say) must not be able to grow
// the map or afford a log line per request.
func TestThrottleBoundsKeyCardinality(t *testing.T) {
	throttle := logging.NewThrottle(time.Minute)
	logged := 0
	for i := 0; i < 20_000; i++ {
		key := "user-" + string(rune('a'+i%26)) + "-" + itoa(i)
		if throttle.Allow(key) {
			logged++
		}
	}
	if logged > 4096 {
		t.Errorf("logged %d lines, want the key cap to bound them", logged)
	}
	if logged == 0 {
		t.Error("the throttle suppressed every key")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
