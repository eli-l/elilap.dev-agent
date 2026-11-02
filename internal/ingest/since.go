package ingest

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseSince parses the --since value: an RFC3339 timestamp, a date
// (YYYY-MM-DD, midnight UTC), or a relative duration such as 168h, 7d, or
// 1w measured back from now. Relative values are the convenient way to ask
// for "the last week".
func ParseSince(value string, now time.Time) (time.Time, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return time.Time{}, fmt.Errorf("since: a value is required")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}
	d, err := parseRelative(v)
	if err != nil {
		return time.Time{}, fmt.Errorf("since %q: expected RFC3339, YYYY-MM-DD, or a duration like 168h, 7d, or 1w", value)
	}
	if d < 0 {
		// A negative duration would move the lower bound into the future
		// and silently index nothing.
		return time.Time{}, fmt.Errorf("since %q: duration must not be negative", value)
	}
	return now.UTC().Add(-d), nil
}

// parseRelative extends time.ParseDuration with day and week suffixes, which
// Go deliberately does not support.
func parseRelative(v string) (time.Duration, error) {
	unit := 24 * time.Hour
	if strings.HasSuffix(v, "w") {
		unit = 7 * 24 * time.Hour
		v = strings.TrimSuffix(v, "w")
	} else if strings.HasSuffix(v, "d") {
		v = strings.TrimSuffix(v, "d")
	} else {
		return time.ParseDuration(v)
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || math.IsInf(n, 1) || math.IsNaN(n) {
		return 0, fmt.Errorf("invalid relative duration %q", v)
	}
	// Guard against an overflow turning a huge value into a negative time.
	if n > float64(math.MaxInt64)/float64(unit) {
		return 0, fmt.Errorf("relative duration too large")
	}
	return time.Duration(n * float64(unit)), nil
}
